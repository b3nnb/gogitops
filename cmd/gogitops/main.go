// gogitops — fleet management via GitOps. Go binary per device, Git repo as truth.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/bennbanks/gogitops/internal/agent"
	"github.com/bennbanks/gogitops/internal/cli"
	"github.com/bennbanks/gogitops/internal/config"
	"github.com/bennbanks/gogitops/internal/dashboard"
)

var (
	version = "dev"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "status":
			cmdStatus(os.Args[2:])
		case "fleet":
			cmdFleet(os.Args[2:])
		case "info":
			cmdInfo(os.Args[2:])
		case "config":
			cmdConfig(os.Args[2:])
		case "logs":
			cmdLogs(os.Args[2:])
		case "watch":
			cmdWatch(os.Args[2:])
		case "git-pull":
			cmdGitPull(os.Args[2:])
		case "restart":
			cmdRestart(os.Args[2:])
		case "version", "--version", "-v":
			cli.Banner()
			fmt.Printf("\n  \033[38;5;141m%s\033[0m\n\n", version)
		case "daemon", "run":
			runDaemon(os.Args[2:])
		case "dashboard":
			runDashboard(os.Args[2:])
		case "recipe":
			cmdRecipe(os.Args[2:])
		case "help", "--help", "-h":
			printHelp()
		default:
			if strings.HasPrefix(os.Args[1], "-") {
				runDaemon(os.Args[1:])
			} else {
				cli.PrintError(fmt.Sprintf("unknown command: %s", os.Args[1]))
				printHelp()
				os.Exit(1)
			}
		}
		return
	}
	printHelp()
}

func printHelp() {
	cli.Banner()
	fmt.Printf(`
  USAGE

    gogitops <command> [flags]

  COMMANDS

    status     Show local agent health (services, tags, peers, system)
    fleet      Show all agents in the fleet (compact view)
    info       Show detailed attributes for a node (tags, groups, config)
    config     Show agent configuration (repo, git, labels, groups, recipes)
    logs       Show recent agent activity log
    watch      Live monitoring — auto-refreshing status view
    git-pull   Force a git pull on the agent's config repo
    restart    Restart the agent daemon
    daemon     Run the agent daemon
    dashboard  Fleet status dashboard (web UI)
    recipe     Recipe management (new, list, validate)
    version    Print version

  FLAGS

    -addr      Agent address for status/info (default: 127.0.0.1:7780)
    -repo      Path to git config repo (default: current dir)
    -port      Port for daemon/dashboard
    -hostname  Override hostname

`)
}

// ── status: show local or remote agent health ────────────────────────────

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7780", "agent health API address")
	fs.Parse(args)

	h, err := cli.FetchHealth(*addr)
	if err != nil {
		cli.PrintError(fmt.Sprintf("agent not reachable at %s", *addr))
		os.Exit(1)
	}
	cli.PrintStatus(h, *addr)
}

// ── fleet: show all agents in compact view ───────────────────────────────

func cmdFleet(args []string) {
	fs := flag.NewFlagSet("fleet", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to gogitops repo (reads mesh.yaml)")
	addrList := fs.String("nodes", "", "comma-separated name=address pairs (overrides mesh.yaml)")
	fs.Parse(args)

	// Build node list
	var nodes []struct {
		name    string
		address string
	}

	if *addrList != "" {
		for _, pair := range strings.Split(*addrList, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) != 2 {
				continue
			}
			addr := parts[1]
			if !strings.Contains(addr, ":") {
				addr = addr + ":7780"
			}
			nodes = append(nodes, struct {
				name    string
				address string
			}{parts[0], addr})
		}
	} else {
		mesh, err := config.LoadMesh(*repoDir)
		if err == nil {
			for _, peer := range mesh.Peers {
				addr := peer.Address()
				if addr == "" {
					continue
				}
				nodes = append(nodes, struct {
					name    string
					address string
				}{peer.Hostname, addr})
			}
		}
	}

	if len(nodes) == 0 {
		cli.PrintInfo("no nodes found. Use --nodes flag or configure mesh.yaml")
		return
	}

	cli.Banner()
	fmt.Printf("\n  \033[1m\033[38;5;141mFleet Status\033[0m — %d nodes\n\n", len(nodes))
	cli.PrintCompactHeader()

	// Query all nodes in parallel
	results := make(chan *cli.HealthResponse, len(nodes))
	errors := make(chan error, len(nodes))

	for _, n := range nodes {
		go func(name, addr string) {
			h, err := cli.FetchHealth(addr)
			if err != nil {
				errors <- fmt.Errorf("%s: %v", name, err)
				return
			}
			results <- h
		}(n.name, n.address)
	}

	for i := 0; i < len(nodes); i++ {
		select {
		case h := <-results:
			cli.PrintCompact(h)
		case err := <-errors:
			fmt.Printf("  %s○%s  %s%s%s  %s%s%s\n",
				"\033[38;5;196m", "\033[0m",
				"\033[1m", padRight(extractName(err.Error(), 14), 14), "\033[0m",
				"\033[38;5;240m", "unreachable", "\033[0m")
		case <-time.After(6 * time.Second):
			fmt.Printf("  %s○%s  %s%s%s  %s%s%s\n",
				"\033[38;5;196m", "\033[0m",
				"\033[1m", "timeout", "\033[0m",
				"\033[38;5;240m", "timed out", "\033[0m")
		}
	}
	fmt.Println()
}

func extractName(errStr string, width int) string {
	// Extract node name from "name: error" format
	parts := strings.SplitN(errStr, ":", 2)
	name := strings.TrimSpace(parts[0])
	return padRight(name, width)
}

// ── info: show detailed node config (tags, groups, services, attributes) ─

func cmdInfo(args []string) {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7780", "agent health API address")
	repoDir := fs.String("repo", ".", "path to gogitops repo")
	fs.Parse(args)

	hostname := ""
	if fs.NArg() > 0 {
		hostname = fs.Arg(0)
	}

	// Fetch live health data
	h, err := cli.FetchHealth(*addr)
	if err != nil {
		cli.PrintError(fmt.Sprintf("agent not reachable at %s", *addr))
		os.Exit(1)
	}

	cli.Banner()
	fmt.Println()

	// Header
	fmt.Printf("  \033[1m\033[38;5;141m%s\033[0m %s\n\n", h.Hostname, h.AgentVersion)

	// System attributes
	fmt.Printf("  %s╭─ Attributes ─────────────────%s\n", "\033[38;5;240m", "\033[0m")
	fmt.Printf("  %s│%s Hostname    %s%s%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;255m", h.Hostname, "\033[0m")
	fmt.Printf("  %s│%s OS          %s%s%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;255m", h.System.OS, "\033[0m")
	fmt.Printf("  %s│%s Arch        %s%s%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;255m", h.System.Arch, "\033[0m")
	fmt.Printf("  %s│%s IP          %s%s%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;38m", h.System.IP, "\033[0m")
	fmt.Printf("  %s│%s Host ID     %s%s%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;245m", h.System.HostID, "\033[0m")
	fmt.Printf("  %s│%s Uptime      %s%s%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;245m", formatUptime(h.UptimeSeconds), "\033[0m")
	fmt.Printf("  %s│%s Version     %s%s%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;245m", h.AgentVersion, "\033[0m")
	if h.NebulaRunning {
		fmt.Printf("  %s│%s Nebula      %s● running%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;46m", "\033[0m")
	} else {
		fmt.Printf("  %s│%s Nebula      %s○ down%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;196m", "\033[0m")
	}
	fmt.Printf("  %s╰──────────────────────────────%s\n", "\033[38;5;240m", "\033[0m")
	fmt.Println()

	// Labels — categorized
	if len(h.Labels) > 0 {
		roles := []string{}
		tags := []string{}
		stacks := []string{}
		for _, l := range h.Labels {
			if strings.HasPrefix(l, "stack:") {
				stacks = append(stacks, l[6:])
			} else if strings.Contains(l, "-host") || strings.Contains(l, "compute") {
				roles = append(roles, l)
			} else {
				tags = append(tags, l)
			}
		}
		fmt.Printf("  %s╭─ Labels ─────────────────────%s\n", "\033[38;5;240m", "\033[0m")
		if len(roles) > 0 {
			fmt.Printf("  %s│%s %sRoles%s    %s", "\033[38;5;240m", "\033[0m", "\033[38;5;141m", "\033[0m", "")
			for _, r := range roles {
				fmt.Printf("%s%s%s ", "\033[38;5;141m", r, "\033[0m")
			}
			fmt.Println()
		}
		if len(tags) > 0 {
			fmt.Printf("  %s│%s %sTags%s     %s", "\033[38;5;240m", "\033[0m", "\033[38;5;38m", "\033[0m", "")
			for _, t := range tags {
				fmt.Printf("%s%s%s ", "\033[38;5;38m", t, "\033[0m")
			}
			fmt.Println()
		}
		if len(stacks) > 0 {
			fmt.Printf("  %s│%s %sStacks%s   %s", "\033[38;5;240m", "\033[0m", "\033[38;5;178m", "\033[0m", "")
			for _, s := range stacks {
				fmt.Printf("%s%s%s ", "\033[38;5;178m", s, "\033[0m")
			}
			fmt.Println()
		}
		fmt.Printf("  %s╰──────────────────────────────%s\n", "\033[38;5;240m", "\033[0m")
		fmt.Println()
	}

	// Groups membership (from repo config)
	if hostname != "" {
		groups := findGroupsForNode(*repoDir, hostname)
		if len(groups) > 0 {
			fmt.Printf("  %s╭─ Groups ─────────────────────%s\n", "\033[38;5;240m", "\033[0m")
			for _, g := range groups {
				fmt.Printf("  %s│%s %s▸ %s%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;38m", g, "\033[0m")
			}
			fmt.Printf("  %s╰──────────────────────────────%s\n", "\033[38;5;240m", "\033[0m")
			fmt.Println()
		}
	}

	// Services
	fmt.Printf("  %s╭─ Services (%d) ──────────────%s\n", "\033[38;5;240m", len(h.Services), "\033[0m")
	names := make([]string, 0, len(h.Services))
	for k := range h.Services {
		names = append(names, k)
	}
	// Sort alphabetically
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[i] > names[j] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	up, down := 0, 0
	for _, name := range names {
		status := h.Services[name]
		var icon, col string
		if status == "running" {
			icon = "●"
			col = "\033[38;5;46m"
			up++
		} else {
			icon = "○"
			col = "\033[38;5;196m"
			down++
		}
		fmt.Printf("  %s│%s %s%s%s  %s%s%s", "\033[38;5;240m", "\033[0m", col, icon, "\033[0m", "\033[38;5;245m", padRight(name, 22), "\033[0m")
		if status != "running" {
			fmt.Printf("  %s%s%s", "\033[38;5;124m", status, "\033[0m")
		}
		fmt.Println()
	}
	fmt.Printf("  \033[38;5;240m╰── \033[38;5;46m%d up\033[0m, \033[38;5;196m%d down\033[0m \033[38;5;240m───────────\033[0m\n", up, down)
	fmt.Println()

	// Peers
	if len(h.PeersReachable) > 0 || len(h.PeersUnreach) > 0 {
		fmt.Printf("  %s╭─ Peers ──────────────────────%s\n", "\033[38;5;240m", "\033[0m")
		for _, p := range h.PeersReachable {
			fmt.Printf("  %s│%s %s●%s  %s%s%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;46m", "\033[0m", "\033[38;5;245m", p, "\033[0m")
		}
		for _, p := range h.PeersUnreach {
			fmt.Printf("  %s│%s %s○%s  %s%s%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;196m", "\033[0m", "\033[38;5;240m", p, "\033[0m")
		}
		fmt.Printf("  %s╰──────────────────────────────%s\n", "\033[38;5;240m", "\033[0m")
		fmt.Println()
	}

	// Disk warnings
	if len(h.DiskWarns) > 0 {
		fmt.Printf("  %s╭─ Disk Warnings ──────────────%s\n", "\033[38;5;240m", "\033[0m")
		for _, w := range h.DiskWarns {
			fmt.Printf("  %s│%s %s⚠%s  %s%s%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;226m", "\033[0m", "\033[38;5;178m", w, "\033[0m")
		}
		fmt.Printf("  %s╰──────────────────────────────%s\n", "\033[38;5;240m", "\033[0m")
		fmt.Println()
	}
}

// ── config: show agent configuration ─────────────────────────────────────

func cmdConfig(args []string) {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7780", "agent health API address")
	fs.Parse(args)

	resp, err := http.Get("http://" + *addr + "/v1/config")
	if err != nil {
		cli.PrintError(fmt.Sprintf("agent not reachable at %s", *addr))
		os.Exit(1)
	}
	defer resp.Body.Close()

	var cfg struct {
		Hostname      string   `json:"hostname"`
		RepoDir       string   `json:"repo_dir"`
		GitRepo       string   `json:"git_repo"`
		GitBranch     string   `json:"git_branch"`
		GitPullInt    string   `json:"git_pull_interval"`
		Labels        []string `json:"labels"`
		Services      []string `json:"services"`
		DiskChecks    []string `json:"disk_checks"`
		Groups        []string `json:"groups"`
		Recipes       []string `json:"recipes"`
		Webhook       string   `json:"webhook_configured"`
		Uptime        int64    `json:"uptime_seconds"`
		Cycles        int64    `json:"cycle_count"`
		LastGitPull   string   `json:"last_git_pull"`
		LastGitResult string   `json:"last_git_result"`
	}
	json.NewDecoder(resp.Body).Decode(&cfg)

	cli.Banner()
	fmt.Println()

	// Header
	fmt.Printf("  \033[1m\033[38;5;141m%s\033[0m configuration\n\n", cfg.Hostname)

	// Git config
	fmt.Printf("  \033[38;5;240m╭─ GitOps ──────────────────────\033[0m\n")
	fmt.Printf("  \033[38;5;240m│\033[0m Repo       \033[38;5;255m%s\033[0m\n", cfg.RepoDir)
	if cfg.GitRepo != "" {
		fmt.Printf("  \033[38;5;240m│\033[0m Remote     \033[38;5;38m%s\033[0m\n", cfg.GitRepo)
		fmt.Printf("  \033[38;5;240m│\033[0m Branch     \033[38;5;255m%s\033[0m\n", cfg.GitBranch)
		fmt.Printf("  \033[38;5;240m│\033[0m Pull every \033[38;5;255m%s\033[0m\n", cfg.GitPullInt)
	} else {
		fmt.Printf("  \033[38;5;240m│\033[0m \033[38;5;240mno git remote configured\033[0m\n")
	}
	if cfg.LastGitPull != "" {
		gitColor := "\033[38;5;46m"
		if cfg.LastGitResult != "" && !strings.Contains(cfg.LastGitResult, "up to date") {
			gitColor = "\033[38;5;226m"
		}
		fmt.Printf("  \033[38;5;240m│\033[0m Last pull  %s%s\033[0m\n", gitColor, cfg.LastGitPull)
		fmt.Printf("  \033[38;5;240m│\033[0m Result     %s%s\033[0m\n", gitColor, cfg.LastGitResult)
	}
	fmt.Printf("  \033[38;5;240m╰──────────────────────────────\033[0m\n\n")

	// Runtime stats
	fmt.Printf("  \033[38;5;240m╭─ Runtime ────────────────────\033[0m\n")
	fmt.Printf("  \033[38;5;240m│\033[0m Uptime     \033[38;5;245m%s\033[0m\n", formatUptime(cfg.Uptime))
	fmt.Printf("  \033[38;5;240m│\033[0m Cycles     \033[38;5;245m%d\033[0m\n", cfg.Cycles)
	fmt.Printf("  \033[38;5;240m│\033[0m Webhook    \033[38;5;245m%s\033[0m\n", cfg.Webhook)
	fmt.Printf("  \033[38;5;240m╰──────────────────────────────\033[0m\n\n")

	// Labels
	if len(cfg.Labels) > 0 {
		roles := []string{}
		tags := []string{}
		stacks := []string{}
		for _, l := range cfg.Labels {
			if strings.HasPrefix(l, "stack:") {
				stacks = append(stacks, l[6:])
			} else if strings.Contains(l, "-host") || strings.Contains(l, "compute") {
				roles = append(roles, l)
			} else {
				tags = append(tags, l)
			}
		}
		fmt.Printf("  \033[38;5;240m╭─ Labels ─────────────────────\033[0m\n")
		if len(roles) > 0 {
			fmt.Printf("  \033[38;5;240m│\033[0m \033[38;5;141mRoles\033[0m    ")
			for _, r := range roles {
				fmt.Printf("\033[38;5;141m%s\033[0m ", r)
			}
			fmt.Println()
		}
		if len(tags) > 0 {
			fmt.Printf("  \033[38;5;240m│\033[0m \033[38;5;38mTags\033[0m     ")
			for _, t := range tags {
				fmt.Printf("\033[38;5;38m%s\033[0m ", t)
			}
			fmt.Println()
		}
		if len(stacks) > 0 {
			fmt.Printf("  \033[38;5;240m│\033[0m \033[38;5;178mStacks\033[0m   ")
			for _, s := range stacks {
				fmt.Printf("\033[38;5;178m%s\033[0m ", s)
			}
			fmt.Println()
		}
		fmt.Printf("  \033[38;5;240m╰──────────────────────────────\033[0m\n\n")
	}

	// Groups
	if len(cfg.Groups) > 0 {
		fmt.Printf("  \033[38;5;240m╭─ Groups (%d) ────────────────\033[0m\n", len(cfg.Groups))
		for _, g := range cfg.Groups {
			fmt.Printf("  \033[38;5;240m│\033[0m \033[38;5;38m▸\033[0m %s\n", g)
		}
		fmt.Printf("  \033[38;5;240m╰──────────────────────────────\033[0m\n\n")
	}

	// Recipes
	if len(cfg.Recipes) > 0 {
		fmt.Printf("  \033[38;5;240m╭─ Recipes (%d) ───────────────\033[0m\n", len(cfg.Recipes))
		for _, r := range cfg.Recipes {
			fmt.Printf("  \033[38;5;240m│\033[0m \033[38;5;178m▸\033[0m %s\n", r)
		}
		fmt.Printf("  \033[38;5;240m╰──────────────────────────────\033[0m\n\n")
	}

	// Services
	if len(cfg.Services) > 0 {
		fmt.Printf("  \033[38;5;240m╭─ Services (%d) ──────────────\033[0m\n", len(cfg.Services))
		for _, s := range cfg.Services {
			fmt.Printf("  \033[38;5;240m│\033[0m \033[38;5;245m%s\033[0m\n", s)
		}
		fmt.Printf("  \033[38;5;240m╰──────────────────────────────\033[0m\n\n")
	}

	// Disk checks
	if len(cfg.DiskChecks) > 0 {
		fmt.Printf("  \033[38;5;240m╭─ Disk Checks ────────────────\033[0m\n")
		for _, d := range cfg.DiskChecks {
			fmt.Printf("  \033[38;5;240m│\033[0m \033[38;5;245m%s\033[0m\n", d)
		}
		fmt.Printf("  \033[38;5;240m╰──────────────────────────────\033[0m\n")
	}
}

// ── logs: show agent activity log ────────────────────────────────────────

func cmdLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7780", "agent health API address")
	count := fs.Int("n", 30, "number of log entries to show")
	fs.Parse(args)

	resp, err := http.Get(fmt.Sprintf("http://%s/v1/logs?limit=%d", *addr, *count))
	if err != nil {
		cli.PrintError(fmt.Sprintf("agent not reachable at %s", *addr))
		os.Exit(1)
	}
	defer resp.Body.Close()

	var entries []struct {
		Timestamp string `json:"ts"`
		Level     string `json:"level"`
		Category  string `json:"cat"`
		Message   string `json:"msg"`
		Detail    string `json:"detail"`
	}
	json.NewDecoder(resp.Body).Decode(&entries)

	if len(entries) == 0 {
		cli.PrintInfo("no log entries found")
		return
	}

	cli.Banner()
	fmt.Printf("\n  \033[1m\033[38;5;141mActivity Log\033[0m — %d entries\n\n", len(entries))

	for _, e := range entries {
		var levelCol, levelIcon string
		switch e.Level {
		case "info":
			levelCol = "\033[38;5;245m"
			levelIcon = " "
		case "warn":
			levelCol = "\033[38;5;226m"
			levelIcon = "⚠"
		case "error":
			levelCol = "\033[38;5;196m"
			levelIcon = "✖"
		case "action":
			levelCol = "\033[38;5;51m"
			levelIcon = "▸"
		default:
			levelCol = "\033[38;5;245m"
			levelIcon = " "
		}

		// Category color
		var catCol string
		switch e.Category {
		case "git":
			catCol = "\033[38;5;178m"
		case "service":
			catCol = "\033[38;5;46m"
		case "agent":
			catCol = "\033[38;5;141m"
		case "disk":
			catCol = "\033[38;5;226m"
		case "peer":
			catCol = "\033[38;5;38m"
		default:
			catCol = "\033[38;5;245m"
		}

		ts := e.Timestamp
		if len(ts) > 10 {
			ts = ts[11:] // just time, not date
		}

		fmt.Printf("  %s%s\033[0m  \033[38;5;240m%s\033[0m  %s%-6s\033[0m  %s\n",
			levelCol, levelIcon,
			ts,
			catCol, e.Category,
			e.Message)
		if e.Detail != "" {
			fmt.Printf("  %s       %s\033[0m\n", levelCol, e.Detail)
		}
	}
	fmt.Println()
}

// ── watch: live monitoring ───────────────────────────────────────────────

func cmdWatch(args []string) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7780", "agent health API address")
	intervalS := fs.Int("interval", 5, "refresh interval in seconds")
	fs.Parse(args)

	interval := time.Duration(*intervalS) * time.Second

	cli.Banner()
	fmt.Printf("\n  \033[1m\033[38;5;141mLive Monitor\033[0m — refreshing every %ds (Ctrl+C to exit)\n\n", *intervalS)

	for {
		// Clear screen and redraw
		fmt.Print("\033[2J\033[H") // clear screen + move cursor to top-left
		cli.Banner()
		fmt.Printf("\n  \033[1m\033[38;5;141mLive Monitor\033[0m — refreshing every %ds (Ctrl+C to exit)\n\n", *intervalS)

		h, err := cli.FetchHealth(*addr)
		if err != nil {
			cli.PrintError(fmt.Sprintf("agent not reachable at %s", *addr))
		} else {
			// Compact status
			up, down := 0, 0
			for _, v := range h.Services {
				if v == "running" {
					up++
				} else {
					down++
				}
			}
			healthIcon := "\033[38;5;46m●\033[0m"
			if down > 0 {
				healthIcon = "\033[38;5;226m◐\033[0m"
			}

			fmt.Printf("  %s \033[1m%s\033[0m  %s%d/%d services\033[0m  \033[38;5;240m%s\033[0m  \033[38;5;240mup %s\033[0m\n\n",
				healthIcon, h.Hostname,
				"\033[38;5;46m", up, up+down,
				h.System.OS+"/"+h.System.Arch,
				formatUptime(h.UptimeSeconds))

			// Services in two columns
			names := make([]string, 0, len(h.Services))
			for k := range h.Services {
				names = append(names, k)
			}
			sort.Strings(names)

			mid := (len(names) + 1) / 2
			for i := 0; i < mid; i++ {
				left := names[i]
				leftStatus := h.Services[left]
				leftIcon := "\033[38;5;46m●\033[0m"
				if leftStatus != "running" {
					leftIcon = "\033[38;5;196m○\033[0m"
				}

				line := fmt.Sprintf("  %s \033[38;5;245m%-20s\033[0m", leftIcon, left)

				if i+mid < len(names) {
					right := names[i+mid]
					rightStatus := h.Services[right]
					rightIcon := "\033[38;5;46m●\033[0m"
					if rightStatus != "running" {
						rightIcon = "\033[38;5;196m○\033[0m"
					}
					line += fmt.Sprintf("  %s \033[38;5;245m%-20s\033[0m", rightIcon, right)
				}
				fmt.Println(line)
			}

			// Nebula + disk
			fmt.Println()
			if h.NebulaRunning {
				fmt.Printf("  \033[38;5;46m●\033[0m Nebula  ")
			} else {
				fmt.Printf("  \033[38;5;196m○\033[0m Nebula  ")
			}
			if len(h.DiskWarns) > 0 {
				fmt.Printf(" \033[38;5;226m⚠ Disk: %s\033[0m", strings.Join(h.DiskWarns, ", "))
			}
			fmt.Println()
		}

		fmt.Printf("\n  \033[38;5;240m%s\033[0m\n", time.Now().Format("15:04:05"))
		time.Sleep(interval)
	}
}

// ── git-pull: force git pull via API ─────────────────────────────────────

func cmdGitPull(args []string) {
	fs := flag.NewFlagSet("git-pull", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7780", "agent health API address")
	fs.Parse(args)

	resp, err := http.Post("http://"+*addr+"/v1/git/pull", "application/json", nil)
	if err != nil {
		cli.PrintError(fmt.Sprintf("agent not reachable at %s", *addr))
		os.Exit(1)
	}
	defer resp.Body.Close()

	var result struct {
		Result   string `json:"result"`
		Hostname string `json:"hostname"`
		Time     string `json:"time"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	if strings.Contains(result.Result, "error") {
		cli.PrintError(fmt.Sprintf("git pull failed: %s", result.Result))
	} else {
		cli.PrintSuccess(fmt.Sprintf("git pull: %s", result.Result))
		fmt.Printf("  \033[38;5;240m%s @ %s\033[0m\n", result.Hostname, result.Time)
	}
}

// ── restart: restart the agent via API ───────────────────────────────────

func cmdRestart(args []string) {
	fs := flag.NewFlagSet("restart", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7780", "agent health API address")
	fs.Parse(args)

	resp, err := http.Post("http://"+*addr+"/v1/restart", "application/json", nil)
	if err != nil {
		cli.PrintError(fmt.Sprintf("agent not reachable at %s", *addr))
		os.Exit(1)
	}
	defer resp.Body.Close()

	var result struct {
		Status   string `json:"status"`
		Hostname string `json:"hostname"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	cli.PrintSuccess(fmt.Sprintf("restart triggered on %s", result.Hostname))
	fmt.Printf("  \033[38;5;240magent will exit and systemd will restart it\033[0m\n")
}

// findGroupsForNode loads all group YAMLs and finds which ones include this node
func findGroupsForNode(repoDir, hostname string) []string {
	groupsDir := repoDir + "/groups"
	entries, err := os.ReadDir(groupsDir)
	if err != nil {
		return nil
	}

	var result []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		path := groupsDir + "/" + entry.Name()
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		// Simple check: does the group YAML reference this hostname or its labels?
		content := string(data)
		if strings.Contains(content, "node: "+hostname) || strings.Contains(content, hostname) {
			name := strings.TrimSuffix(entry.Name(), ".yaml")
			result = append(result, name)
		}
	}
	return result
}

// ── Recipe commands (unchanged) ──────────────────────────────────────────

func cmdRecipe(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: gogitops recipe <subcommand>\n\nSubcommands:\n  new <name>       Scaffold a new recipe directory with template + examples\n  list             List all recipes in the repo\n  validate <file>  Validate a recipe YAML file\n")
		os.Exit(1)
	}
	switch args[0] {
	case "new":
		recipeNew(args[1:])
	case "list":
		recipeList()
	case "validate":
		recipeValidate(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown recipe subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func recipeNew(args []string) {
	fs := flag.NewFlagSet("recipe new", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to gogitops repo (recipes/ directory)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "usage: gogitops recipe new <name> [--repo path]\n\nCreates a recipe directory with a commented YAML template.\n")
		os.Exit(1)
	}

	name := fs.Arg(0)
	recipesDir := *repoDir + "/recipes"
	recipeDir := recipesDir + "/" + name

	if _, err := os.Stat(recipeDir); err == nil {
		fmt.Fprintf(os.Stderr, "❌ recipe directory already exists: %s\n", recipeDir)
		os.Exit(1)
	}

	if err := os.MkdirAll(recipeDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "❌ failed to create directory: %v\n", err)
		os.Exit(1)
	}

	template := `# GoGitOps Recipe: ` + name + `
name: ` + name + `
description: "TODO: Human-readable description"
version: "1.0.0"
labels: []
steps: []
`

	recipeFile := recipeDir + "/" + name + ".yaml"
	if err := os.WriteFile(recipeFile, []byte(template), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "❌ failed to write recipe file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Created recipe: %s\n", recipeDir)
	fmt.Printf("   %s  — YAML template\n", recipeFile)
}

func recipeList() {
	recipesDir := "recipes"
	entries, err := os.ReadDir(recipesDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ cannot read recipes directory: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Recipes in %s/:\n\n", recipesDir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		yamlFile := recipesDir + "/" + e.Name() + "/" + e.Name() + ".yaml"
		if _, err := os.Stat(yamlFile); err == nil {
			fmt.Printf("  📦 %s\n", e.Name())
		}
	}
}

func recipeValidate(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: gogitops recipe validate <file.yaml>\n")
		os.Exit(1)
	}
	file := args[0]
	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ cannot read file: %v\n", err)
		os.Exit(1)
	}
	content := string(data)
	errors := []string{}
	if !strings.Contains(content, "name:") {
		errors = append(errors, "missing required field: name")
	}
	if !strings.Contains(content, "steps:") {
		errors = append(errors, "missing required field: steps")
	}
	if len(errors) > 0 {
		fmt.Printf("❌ %s has issues:\n", file)
		for _, e := range errors {
			fmt.Printf("   - %s\n", e)
		}
		os.Exit(1)
	}
	fmt.Printf("✅ %s looks valid\n", file)
}

// ── Daemon mode ──────────────────────────────────────────────────────────

func runDaemon(args []string) {
	var (
		repoDir   = flag.String("repo", "/home/benn/Documents/code/GoGitOps", "path to config repo")
		hostFlag  = flag.String("hostname", "", "override hostname (defaults to system hostname; match a nodes/*.yaml name)")
		bindAddr  = flag.String("bind", "", "bind address for health API (default: node's nebula IP)")
		port      = flag.Int("port", 7780, "health API port")
		intervalS = flag.Int("interval", 60, "check interval in seconds")
		webhook   = flag.String("webhook", "", "Discord webhook URL or nenv:<ns>/<key> (empty = no alerts)")
		dashFlag  = flag.String("dashboard", "", "dashboard URL to register with (e.g. http://10.2.0.102:7781)")
	)
	flag.CommandLine.Parse(args)

	hostname := *hostFlag
	if hostname == "" {
		hostname = os.Getenv("GOGITOPS_HOSTNAME")
	}
	if hostname == "" {
		hostname = config.DetectHostname()
	}

	node, err := config.LoadNode(*repoDir, hostname)
	if err != nil {
		log.Fatalf("failed to load node config for %q: %v\n(hint: create nodes/%s.yaml in the repo)", hostname, err, hostname)
	}

	meshCfg, err := config.LoadMesh(*repoDir)
	if err != nil {
		log.Printf("warning: failed to load mesh config: %v — running without peer pings", err)
		meshCfg = &config.MeshConfig{}
	}

	wbhook := resolveWebhook(*webhook)

	bind := *bindAddr
	if bind == "" {
		bind = node.NebulaIP
	}
	if bind == "" || bind == "null" {
		bind = "127.0.0.1"
		log.Printf("warning: no nebula_ip in node config — binding to localhost only")
	}

	a := agent.New(node, meshCfg, wbhook)
	agent.Version = version

	addr := net.JoinHostPort(bind, fmt.Sprintf("%d", *port))
	http.HandleFunc("/v1/health", a.HealthHandler)
	http.HandleFunc("/v1/config", a.ConfigHandler)
	http.HandleFunc("/v1/logs", a.LogsHandler)
	http.HandleFunc("/v1/git/pull", a.GitPullHandler)
	http.HandleFunc("/v1/restart", a.RestartHandler)
	a.SetRepoDir(*repoDir)
	go func() {
		log.Printf("health API listening on %s", addr)
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Fatalf("health API failed: %v", err)
		}
	}()

	if *dashFlag != "" {
		go registerWithDashboard(*dashFlag, hostname, bind, *port, node)
	}

	interval := time.Duration(*intervalS) * time.Second
	a.Run(interval)
	os.Exit(0)
}

func resolveWebhook(url string) string {
	if strings.HasPrefix(url, "nenv:") {
		ref := url[5:]
		parts := strings.SplitN(ref, "/", 2)
		ns, key := "global", ref
		if len(parts) == 2 {
			ns, key = parts[0], parts[1]
		}
		out, err := exec.Command("nenv", "get", ns, key).Output()
		if err == nil {
			resolved := strings.TrimSpace(string(out))
			if resolved != "" {
				return resolved
			}
		}
		log.Printf("warning: failed to resolve nenv:%s — using raw value", ref)
	}
	return url
}

func registerWithDashboard(dashURL, hostname, bindAddr string, port int, node *config.NodeConfig) {
	address := bindAddr
	if address == "0.0.0.0" {
		address = node.Address()
		if address == "" {
			address = "127.0.0.1"
		}
	}
	address = fmt.Sprintf("%s:%d", address, port)

	displayIP := node.LanIP
	if displayIP != "" && displayIP != "null" && node.NebulaIP != "" && node.NebulaIP != "null" {
		displayIP = node.LanIP + " (lan) / " + node.NebulaIP + " (neb)"
	} else if displayIP == "" || displayIP == "null" {
		displayIP = node.NebulaIP
	}

	payload, _ := json.Marshal(map[string]string{
		"hostname":   hostname,
		"address":    address,
		"display_ip": displayIP,
	})

	client := &http.Client{Timeout: 5 * time.Second}
	for {
		url := strings.TrimRight(dashURL, "/") + "/api/register"
		resp, err := client.Post(url, "application/json", strings.NewReader(string(payload)))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				log.Printf("registered with dashboard at %s", dashURL)
				return
			}
		}
		log.Printf("dashboard registration failed (will retry): %v", err)
		time.Sleep(30 * time.Second)
	}
}

// ── Dashboard ────────────────────────────────────────────────────────────

func runDashboard(args []string) {
	fs := flag.NewFlagSet("dashboard", flag.ExitOnError)
	port := fs.Int("port", 7781, "HTTP port for the dashboard")
	dbPath := fs.String("db", "", "path to SQLite status database")
	repoDir := fs.String("repo", "/home/benn/Documents/code/GoGitOps", "path to config repo")
	nodesFlag := fs.String("nodes", "", "override: comma-separated name=address pairs")
	binDir := fs.String("binaries", "", "path to pre-built binaries directory")
	dashHost := fs.String("host", "10.2.0.102", "external hostname/IP")
	fs.Parse(args)

	if *dbPath == "" {
		cacheDir := os.Getenv("XDG_CACHE_HOME")
		if cacheDir == "" {
			home, _ := os.UserHomeDir()
			cacheDir = home + "/.cache"
		}
		*dbPath = cacheDir + "/gogitops/status.db"
	}

	var nodes []dashboard.NodeConfig

	if *nodesFlag != "" {
		for _, pair := range strings.Split(*nodesFlag, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				log.Fatalf("invalid --nodes entry %q", pair)
			}
			name := parts[0]
			addr := parts[1]
			if !strings.Contains(addr, ":") {
				addr = addr + ":7780"
			}
			displayIP := addr
			if idx := strings.LastIndex(addr, ":"); idx >= 0 {
				displayIP = addr[:idx]
			}
			nodes = append(nodes, dashboard.NodeConfig{
				Name:      name,
				Address:   addr,
				DisplayIP: displayIP,
			})
		}
	} else {
		mesh, err := config.LoadMesh(*repoDir)
		if err == nil {
			for _, peer := range mesh.Peers {
				addr := peer.Address()
				if addr == "" {
					continue
				}
				displayIP := peer.LanIP
				if displayIP == "" || displayIP == "null" {
					displayIP = peer.NebulaIP
				} else if peer.NebulaIP != "" && peer.NebulaIP != "null" {
					displayIP = peer.LanIP + " (lan) / " + peer.NebulaIP + " (neb)"
				}
				nodes = append(nodes, dashboard.NodeConfig{
					Name:      peer.Hostname,
					Address:   addr,
					DisplayIP: displayIP,
				})
			}
		}
		if len(nodes) == 0 {
			log.Printf("no static nodes in mesh.yaml — waiting for nodes to self-register")
		}
	}

	store, err := dashboard.NewStore(*dbPath)
	if err != nil {
		log.Fatalf("failed to open status database: %v", err)
	}
	defer store.Close()

	dashURL := fmt.Sprintf("http://%s:%d", *dashHost, *port)
	h := dashboard.NewHandler(store, nodes, dashURL, *binDir)

	http.Handle("/", h)
	http.Handle("/index.html", h)
	http.Handle("/api/status", h)

	listenAddr := fmt.Sprintf(":%d", *port)
	fmt.Printf("gogitops dashboard on :%d — monitoring %d nodes\n", *port, len(nodes))
	for _, n := range nodes {
		fmt.Printf("  %s → %s (display: %s)\n", n.Name, n.Address, n.DisplayIP)
	}
	if err := http.ListenAndServe(listenAddr, nil); err != nil {
		log.Fatalf("dashboard server failed: %v", err)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

func formatUptime(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	if seconds < 86400 {
		return fmt.Sprintf("%dh %dm", seconds/3600, (seconds%3600)/60)
	}
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	return fmt.Sprintf("%dd %dh", days, hours)
}
