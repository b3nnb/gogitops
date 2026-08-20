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
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
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
		case "set":
			cmdSet(os.Args[2:])
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
    logs       Show recent agent activity log (-n N for count)
    watch      Live monitoring — auto-refreshing status (Ctrl+C to exit)
    git-pull   Force a git pull on the agent's config repo
    restart    Restart the agent daemon
    set        Set config values (repo, branch, dashboard, webhook, hostname)
    daemon     Run the agent daemon
    dashboard  Fleet status dashboard (web UI on :7781)
    recipe     Recipe management (new, list, validate)
    version    Print version

  FLAGS

    -addr <host:port>     Agent address (default: 127.0.0.1:7780)
    -repo <path>          Path to git config repo (default: .)
    -port <N>             Port for daemon/dashboard
    -hostname <name>      Override detected hostname
    -interval <seconds>   Check cycle interval (default: 60)
    -n <N>                Number of log entries (logs command)
    -nodes <name=addr>    Comma-separated node list (fleet command)

  REMOTE AGENTS

    All commands accept -addr to target a remote agent:
      gogitops status -addr 10.0.0.229:7780
      gogitops config -addr 10.0.0.251:7780
      gogitops git-pull -addr 10.200.0.4:7780
      gogitops restart -addr 10.0.0.229:7780

  INSTALL ON A NEW MACHINE

    # Linux (amd64)
    curl -sL github.com/b3nnb/gogitops/releases/download/v0.5.0/gogitops_0.5.0_amd64.deb -o /tmp/gogitops.deb
    sudo dpkg -i /tmp/gogitops.deb

    # macOS (arm64)
    curl -sL github.com/b3nnb/gogitops/releases/download/v0.5.0/gogitops-installer-darwin-arm64.tar.gz | tar xz
    bash install.sh

    The installer auto-detects hostname, LAN/Nebula IP, starts the daemon,
    and registers with the dashboard. Zero prompts.

  AGENT API

    GET  /v1/health          Health snapshot (services, peers, system)
    GET  /v1/config          Agent configuration (repo, git, labels, recipes)
    GET  /v1/logs?limit=N    Recent activity log entries
    POST /v1/git/pull        Force immediate git pull
    POST /v1/restart         Restart agent (systemd auto-restarts)

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
		fmt.Printf("  %s│%s Nebula      %s● up%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;46m", "\033[0m")
	} else if h.NebulaIP != "" {
		fmt.Printf("  %s│%s Nebula      %s○ down%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;196m", "\033[0m")
	} else {
		fmt.Printf("  %s│%s Nebula      %s— not configured%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;245m", "\033[0m")
	}
	fmt.Printf("  %s╰──────────────────────────────%s\n", "\033[38;5;240m", "\033[0m")
	fmt.Println()

	// Tags
	if len(h.Labels) > 0 {
		tags := []string{}
		stacks := []string{}
		for _, l := range h.Labels {
			if strings.HasPrefix(l, "stack:") {
				stacks = append(stacks, l[6:])
			} else {
				tags = append(tags, l)
			}
		}
		fmt.Printf("  %s╭─ Tags ─────────────────────%s\n", "\033[38;5;240m", "\033[0m")
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

	// Peers — only show if at least one is reachable (standalone nodes have no peers)
	if len(h.PeersReachable) > 0 {
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
	hard := fs.Bool("hard", false, "daemon-reload + systemctl restart (picks up service file changes)")
	fs.Parse(args)

	if *hard {
		// Full systemctl restart — reloads unit file, picks up env changes
		fmt.Printf("  \033[38;5;51m▸\033[0m Running daemon-reload + restart...\n")
		reloadCmd := exec.Command("sudo", "systemctl", "daemon-reload")
		if out, err := reloadCmd.CombinedOutput(); err != nil {
			cli.PrintError(fmt.Sprintf("daemon-reload failed: %s", strings.TrimSpace(string(out))))
			os.Exit(1)
		}
		restartCmd := exec.Command("sudo", "systemctl", "restart", "gogitops")
		if out, err := restartCmd.CombinedOutput(); err != nil {
			cli.PrintError(fmt.Sprintf("restart failed: %s", strings.TrimSpace(string(out))))
			os.Exit(1)
		}
		// Wait for agent to come back up
		for i := 0; i < 15; i++ {
			time.Sleep(500 * time.Millisecond)
			resp, err := http.Get("http://" + *addr + "/v1/health")
			if err == nil {
				resp.Body.Close()
				cli.PrintSuccess("agent restarted and healthy")
				return
			}
		}
		cli.PrintError("agent did not come back within 7s — check: systemctl status gogitops")
		return
	}

	// Soft restart — tell agent to exit, systemd restarts it
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
	fmt.Printf("  \033[38;5;240muse --hard to also daemon-reload (picks up service file changes)\033[0m\n")
}

// ── set: edit agent config values ────────────────────────────────────────

func cmdSet(args []string) {
	if len(args) == 0 {
		cli.Banner()
		fmt.Println()
		fmt.Printf("  \033[1m\033[38;5;141mSet Configuration\033[0m\n\n")
		fmt.Printf("  Usage: gogitops set <key> <value>\n\n")
		fmt.Printf("  \033[38;5;240mKeys:\033[0m\n")
		fmt.Printf("    repo <url>         Git config repo URL\n")
		fmt.Printf("    branch <name>      Git branch (default: main)\n")
		fmt.Printf("    dashboard <url>    Beacon/dashboard URL for self-registration\n")
		fmt.Printf("    webhook <url>      Discord webhook URL or nenv:<ns>/<key>\n")
		fmt.Printf("    hostname <name>    Override detected hostname\n")
		fmt.Printf("    bind <addr>        Health API bind address\n")
		fmt.Printf("    port <num>         Health API port\n")
		fmt.Printf("    interval <sec>     Check cycle interval\n\n")
		fmt.Printf("  \033[38;5;240mExamples:\033[0m\n")
		fmt.Printf("    gogitops set repo git@github.com:org/fleet-config.git\n")
		fmt.Printf("    gogitops set dashboard http://10.2.0.102:7781\n")
		fmt.Printf("    gogitops set webhook nenv:gogitops/DISCORD_WEBHOOK\n\n")
		return
	}

	key := args[0]
	if len(args) < 2 {
		cli.PrintError(fmt.Sprintf("no value provided for '%s'", key))
		fmt.Fprintf(os.Stderr, "  usage: gogitops set %s <value>\n", key)
		os.Exit(1)
	}
	value := args[1]

	// Map keys to config file keys / env vars
	envFile := "/etc/gogitops/agent.env"
	configFile := "/etc/gogitops/config.yaml"

	// Keys that go in agent.env (env vars read by systemd)
	envKeys := map[string]string{
		"repo":      "GOGITOPS_REPO_URL",
		"branch":    "GOGITOPS_REPO_BRANCH",
		"dashboard": "GOGITOPS_DASHBOARD_URL",
		"hostname":  "GOGITOPS_HOSTNAME",
		"bind":      "GOGITOPS_BIND",
		"port":      "GOGITOPS_PORT",
		"interval":  "GOGITOPS_INTERVAL",
	}

	// Keys that go in config.yaml (non-systemd config)
	configKeys := map[string]string{
		"webhook": "webhook",
	}

	// Keys that need a daemon restart
	needsRestart := map[string]bool{
		"repo": true, "branch": true, "dashboard": true,
		"webhook": true, "bind": true, "port": true, "interval": true,
		"hostname": true,
	}

	if envKey, ok := envKeys[key]; ok {
		// Update agent.env
		if err := updateEnvFile(envFile, envKey, value); err != nil {
			// Try user-local path if /etc isn't writable
			home, _ := os.UserHomeDir()
			altEnv := home + "/.config/gogitops/agent.env"
			if err2 := updateEnvFile(altEnv, envKey, value); err2 != nil {
				cli.PrintError(fmt.Sprintf("failed to update %s: %v", envFile, err))
				os.Exit(1)
			}
			envFile = altEnv
		}

		// If setting repo URL, clone it if not already present
		if key == "repo" {
			home, _ := os.UserHomeDir()
			repoDir := home + "/.config/gogitops"
			if _, err := os.Stat(repoDir + "/.git"); err != nil {
				branch := "main"
				// Try to read branch from env file
				if b, err := readEnvValue(envFile, "GOGITOPS_REPO_BRANCH"); err == nil && b != "" {
					branch = b
				}
				fmt.Printf("  \033[38;5;51m▸\033[0m Cloning repo...\n")
				cmd := exec.Command("git", "clone", "--branch", branch, value, repoDir)
				if out, err := cmd.CombinedOutput(); err != nil {
					cloneErr := strings.TrimSpace(string(out))
					// If dir exists but isn't a git repo, init + remote add + pull instead
					if strings.Contains(cloneErr, "already exists") {
						fmt.Printf("  \033[38;5;51m▸\033[0m Dir exists — initializing git repo...\n")
						initCmd := exec.Command("git", "init")
						initCmd.Dir = repoDir
						if initOut, initErr := initCmd.CombinedOutput(); initErr != nil {
							cli.PrintError(fmt.Sprintf("git init failed: %s", strings.TrimSpace(string(initOut))))
						} else {
							// Set remote (overwrite if exists)
							remoteCmd := exec.Command("git", "remote", "add", "origin", value)
							remoteCmd.Dir = repoDir
							if _, remoteErr := remoteCmd.CombinedOutput(); remoteErr != nil {
								// remote might already exist — try set-url
								setURLCmd := exec.Command("git", "remote", "set-url", "origin", value)
								setURLCmd.Dir = repoDir
								setURLCmd.CombinedOutput()
							}
							// Fetch and checkout branch
							fetchCmd := exec.Command("git", "fetch", "origin", branch)
							fetchCmd.Dir = repoDir
							if fetchOut, fetchErr := fetchCmd.CombinedOutput(); fetchErr != nil {
								cli.PrintError(fmt.Sprintf("git fetch failed: %s", strings.TrimSpace(string(fetchOut))))
							} else {
								checkoutCmd := exec.Command("git", "checkout", branch)
							checkoutCmd.Dir = repoDir
							checkoutCmd.CombinedOutput()
								cli.PrintSuccess(fmt.Sprintf("initialized and fetched to %s", repoDir))
							}
						}
					} else {
						cli.PrintError(fmt.Sprintf("clone failed: %s", cloneErr))
					}
				} else {
					cli.PrintSuccess(fmt.Sprintf("cloned to %s", repoDir))
				}
			}
		}

		cli.PrintSuccess(fmt.Sprintf("%s = %s", key, value))
		fmt.Printf("  \033[38;5;240mwritten to %s\033[0m\n", envFile)
	} else if configKey, ok := configKeys[key]; ok {
		// Update config.yaml
		if err := updateConfigYaml(configFile, configKey, value); err != nil {
			home, _ := os.UserHomeDir()
			altConfig := home + "/.config/gogitops/config.yaml"
			if err2 := updateConfigYaml(altConfig, configKey, value); err2 != nil {
				cli.PrintError(fmt.Sprintf("failed to update %s: %v", configFile, err))
				os.Exit(1)
			}
			configFile = altConfig
		}
		cli.PrintSuccess(fmt.Sprintf("%s = %s", key, value))
		fmt.Printf("  \033[38;5;240mwritten to %s\033[0m\n", configFile)
	} else {
		cli.PrintError(fmt.Sprintf("unknown config key: %s", key))
		fmt.Fprintf(os.Stderr, "  run 'gogitops set' for available keys\n")
		os.Exit(1)
	}

	if needsRestart[key] {
		fmt.Printf("  \033[38;5;226m⚠\033[0m Restart agent for changes to take effect: \033[38;5;255mgogitops restart\033[0m\n")
	}
}

// updateEnvFile updates a KEY=value line in an env file, or adds it if missing
func updateEnvFile(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		// Create the file (ensure parent dir exists)
		_ = os.MkdirAll(filepath.Dir(path), 0755)
		content := fmt.Sprintf("# gogitops agent environment\n%s=%s\n", key, value)
		return os.WriteFile(path, []byte(content), 0644)
	}

	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+"=") {
			lines[i] = fmt.Sprintf("%s=%s", key, value)
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, fmt.Sprintf("%s=%s", key, value))
	}

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
}

// readEnvValue reads a KEY=value from an env file
func readEnvValue(path, key string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+"=") {
			return strings.TrimPrefix(trimmed, key+"="), nil
		}
	}
	return "", fmt.Errorf("key not found")
}

// updateConfigYaml updates a key: value line in a YAML config file
func updateConfigYaml(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		// Create the file (ensure parent dir exists)
		_ = os.MkdirAll(filepath.Dir(path), 0755)
		content := fmt.Sprintf("# gogitops configuration\n%s: \"%s\"\n", key, value)
		return os.WriteFile(path, []byte(content), 0644)
	}

	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+":") {
			lines[i] = fmt.Sprintf("%s: \"%s\"", key, value)
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, fmt.Sprintf("%s: \"%s\"", key, value))
	}

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
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

// resolveRepoDir returns the gogitops repo path.
// Precedence: explicit flag > ~/.config/gogitops (if .git exists) > /etc/gogitops (if .git exists) > "."
func resolveRepoDir(flagVal string) string {
	if flagVal != "" && flagVal != "." {
		return flagVal
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		home + "/.config/gogitops",
		"/etc/gogitops",
	}
	for _, dir := range candidates {
		if _, err := os.Stat(dir + "/.git"); err == nil {
			return dir
		}
		// Also accept if recipes/ dir exists (non-git config repo)
		if _, err := os.Stat(dir + "/recipes"); err == nil {
			return dir
		}
	}
	return "."
}

func cmdRecipe(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: gogitops recipe <subcommand>\n\nSubcommands:\n  new <name>        Scaffold a new recipe directory with template + examples\n  list              List all recipes in the repo\n  validate <file>   Validate a recipe YAML file\n  run <file>        Execute a recipe YAML file\n  run <name>        Execute a recipe by name (searches recipes/ dir)\n")
		os.Exit(1)
	}
	switch args[0] {
	case "new":
		recipeNew(args[1:])
	case "list":
		recipeList()
	case "validate":
		recipeValidate(args[1:])
	case "run":
		recipeRun(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown recipe subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func recipeNew(args []string) {
	// Parse flags from full args to handle --repo before or after name
	// Go flag package requires flags before positional args, so we rearrange
	fs := flag.NewFlagSet("recipe new", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to gogitops repo (recipes/ directory)")

	// Separate flags from positional args manually
	var positional []string
	var flagArgs []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--repo" && i+1 < len(args) {
			flagArgs = append(flagArgs, args[i], args[i+1])
			i++ // skip value
		} else if strings.HasPrefix(args[i], "-") {
			flagArgs = append(flagArgs, args[i])
		} else {
			positional = append(positional, args[i])
		}
	}
	fs.Parse(flagArgs)

	if len(positional) < 1 {
		fmt.Fprintf(os.Stderr, "usage: gogitops recipe new <name> [--repo path]\n\nCreates a recipe directory with a commented YAML template.\n")
		os.Exit(1)
	}

	name := positional[0]
	resolved := resolveRepoDir(*repoDir)
	recipesDir := resolved + "/recipes"
	recipeDir := recipesDir + "/" + name

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
	fs := flag.NewFlagSet("recipe list", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to gogitops repo (recipes/ directory)")
	fs.Parse(os.Args[3:]) // skip "gogitops recipe list"
	resolved := resolveRepoDir(*repoDir)
	recipesDir := resolved + "/recipes"
	entries, err := os.ReadDir(recipesDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ cannot read recipes directory: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Recipes in %s/:\n\n", recipesDir)
	for _, e := range entries {
		if e.IsDir() {
			// Directory-based recipe: recipes/<name>/<name>.yaml
			yamlFile := recipesDir + "/" + e.Name() + "/" + e.Name() + ".yaml"
			if _, err := os.Stat(yamlFile); err == nil {
				fmt.Printf("  📦 %s\n", e.Name())
			}
		} else if strings.HasSuffix(e.Name(), ".yaml") {
			// Flat recipe: recipes/<name>.yaml
			name := strings.TrimSuffix(e.Name(), ".yaml")
			fmt.Printf("  📦 %s\n", name)
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

// ── Recipe runner ─────────────────────────────────────────────────────────

type recipeStep struct {
	Name          string `yaml:"name"`
	Description   string `yaml:"description"`
	Command       string `yaml:"command"`
	OS            string `yaml:"os"`
	Arch          string `yaml:"arch"`
	LabelsReq     []string `yaml:"labels_required"`
	Expect        string `yaml:"expect"`
	ExpectRegex   string `yaml:"expect_regex"`
	ExpectExit    *int   `yaml:"expect_exit"`
	Parse         string `yaml:"parse"`
	Pattern       string `yaml:"pattern"`
	OnlyIf        string `yaml:"only_if"`
	When          string `yaml:"when"`
	OnFailure     string `yaml:"on_failure"`
	Retries       int    `yaml:"retries"`
	RetryDelay    string `yaml:"retry_delay"`
}

type recipe struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Version     string `yaml:"version"`
	Labels      []string `yaml:"labels"`
	Params      []struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Required    bool   `yaml:"required"`
	} `yaml:"params"`
	Steps []recipeStep `yaml:"steps"`
}

func recipeRun(args []string) {
	fs := flag.NewFlagSet("recipe run", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to gogitops repo")
	dryRun := fs.Bool("dry-run", false, "print commands without executing")
	verbose := fs.Bool("verbose", false, "show full command output")

	// Separate flags from positional args
	var positional []string
	var flagArgs []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--repo" && i+1 < len(args) {
			flagArgs = append(flagArgs, args[i], args[i+1])
			i++
		} else if strings.HasPrefix(args[i], "-") {
			flagArgs = append(flagArgs, args[i])
		} else {
			positional = append(positional, args[i])
		}
	}
	fs.Parse(flagArgs)

	if len(positional) < 1 {
		fmt.Fprintf(os.Stderr, "usage: gogitops recipe run <file.yaml|name> [--repo path] [--dry-run] [--verbose]\n\n")
		fmt.Fprintf(os.Stderr, "  Executes a recipe step-by-step.\n")
		fmt.Fprintf(os.Stderr, "  --dry-run   Print each command without running it\n")
		fmt.Fprintf(os.Stderr, "  --verbose   Show full command output (stdout+stderr)\n")
		os.Exit(1)
	}

	target := positional[0]
	resolved := resolveRepoDir(*repoDir)

	// Resolve recipe file path
	recipeFile := target
	if _, err := os.Stat(recipeFile); err != nil {
		// Try as a name: recipes/<name>.yaml or recipes/<name>/<name>.yaml
		candidates := []string{
			resolved + "/recipes/" + target + ".yaml",
			resolved + "/recipes/" + target + "/" + target + ".yaml",
		}
		found := false
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				recipeFile = c
				found = true
				break
			}
		}
		if !found {
			cli.PrintError(fmt.Sprintf("recipe not found: %s (tried %s)", target, strings.Join(candidates, ", ")))
			os.Exit(1)
		}
	}

	// Parse YAML (simple line-based parser — no external deps)
	data, err := os.ReadFile(recipeFile)
	if err != nil {
		cli.PrintError(fmt.Sprintf("cannot read recipe: %v", err))
		os.Exit(1)
	}

	r := parseRecipe(string(data))
	if r.Name == "" {
		cli.PrintError("recipe missing 'name' field")
		os.Exit(1)
	}
	if len(r.Steps) == 0 {
		cli.PrintError("recipe has no steps")
		os.Exit(1)
	}

	// Build variable map for substitution
	vars := map[string]string{
		"repo":     resolved,
		"hostname": config.DetectHostname(),
	}
	// Detect OS/arch
	vars["os"] = runtime.GOOS
	vars["arch"] = runtime.GOARCH

	// Banner
	cli.Banner()
	fmt.Printf("\n  \033[1m\033[38;5;141mRecipe: %s\033[0m\n", r.Name)
	if r.Description != "" {
		fmt.Printf("  \033[38;5;240m%s\033[0m\n", r.Description)
	}
	fmt.Printf("  \033[38;5;240m%d steps%s\033[0m\n\n", len(r.Steps), func() string {
		if *dryRun {
			return " (DRY RUN)"
		}
		return ""
	}())

	// Execute steps
	passed := 0
	skipped := 0
	failed := 0
	for i, step := range r.Steps {
		stepNum := i + 1
		displayName := step.Name
		if displayName == "" {
			displayName = fmt.Sprintf("step-%d", stepNum)
		}

		// Substitute variables in command
		cmd := substituteVars(step.Command, vars)

		// OS filter
		if step.OS != "" && step.OS != vars["os"] {
			fmt.Printf("  \033[38;5;240m⊘ %d/%d %s (skipped: os=%s, host=%s)\033[0m\n", stepNum, len(r.Steps), displayName, step.OS, vars["os"])
			skipped++
			continue
		}
		// Arch filter
		if step.Arch != "" && step.Arch != vars["arch"] {
			fmt.Printf("  \033[38;5;240m⊘ %d/%d %s (skipped: arch=%s)\033[0m\n", stepNum, len(r.Steps), displayName, step.Arch)
			skipped++
			continue
		}

		// when: condition — run shell test, skip if exit != 0
		if step.When != "" {
			whenCmd := substituteVars(step.When, vars)
			wCmd := exec.Command("bash", "-c", whenCmd)
			if err := wCmd.Run(); err != nil {
				fmt.Printf("  \033[38;5;240m⊘ %d/%d %s (skipped: when condition false)\033[0m\n", stepNum, len(r.Steps), displayName)
				skipped++
				continue
			}
		}

		// only_if: condition (inverse of when — skip if false)
		if step.OnlyIf != "" {
			onlyCmd := substituteVars(step.OnlyIf, vars)
			oCmd := exec.Command("bash", "-c", onlyCmd)
			if err := oCmd.Run(); err != nil {
				fmt.Printf("  \033[38;5;240m⊘ %d/%d %s (skipped: only_if false)\033[0m\n", stepNum, len(r.Steps), displayName)
				skipped++
				continue
			}
		}

		// Description
		if step.Description != "" {
			fmt.Printf("  \033[38;5;38m▸ %d/%d %s\033[0m — \033[38;5;245m%s\033[0m\n", stepNum, len(r.Steps), displayName, step.Description)
		} else {
			fmt.Printf("  \033[38;5;38m▸ %d/%d %s\033[0m\n", stepNum, len(r.Steps), displayName)
		}

		// Dry run — just print
		if *dryRun {
			fmt.Printf("     \033[38;5;240m$ %s\033[0m\n", cmd)
			passed++
			continue
		}

		// Execute with retries
		maxRetries := step.Retries
		if maxRetries == 0 {
			maxRetries = 1
		}
		retryDelay := 2 * time.Second
		if step.RetryDelay != "" {
			if d, err := time.ParseDuration(step.RetryDelay); err == nil {
				retryDelay = d
			}
		}

		var lastOutput string
		var lastExitCode int
		var stepErr error
		for attempt := 1; attempt <= maxRetries; attempt++ {
			eCmd := exec.Command("bash", "-c", cmd)
			var out []byte
			out, stepErr = eCmd.CombinedOutput()
			lastOutput = string(out)
			lastExitCode = 0
			if stepErr != nil {
				if exitErr, ok := stepErr.(*exec.ExitError); ok {
					lastExitCode = exitErr.ExitCode()
				} else {
					lastExitCode = 1
				}
			}

			// Check expectations
			success := true
			if step.Expect != "" && !strings.Contains(lastOutput, step.Expect) {
				success = false
			}
			if step.ExpectRegex != "" {
				matched, _ := regexp.MatchString(step.ExpectRegex, lastOutput)
				if !matched {
					success = false
				}
			}
			if step.ExpectExit != nil && lastExitCode != *step.ExpectExit {
				success = false
			}
			if lastExitCode != 0 && step.ExpectExit == nil {
				success = false
			}

			if success {
				break
			}
			if attempt < maxRetries {
				fmt.Printf("     \033[38;5;226m↻ retry %d/%d (waiting %s)\033[0m\n", attempt, maxRetries, retryDelay)
				time.Sleep(retryDelay)
			}
		}

		// Parse output if requested
		if step.Parse == "regex" && step.Pattern != "" {
			re, err := regexp.Compile(step.Pattern)
			if err == nil {
				matches := re.FindStringSubmatch(lastOutput)
				if len(matches) > 1 {
					for idx, m := range matches[1:] {
						vars[fmt.Sprintf("%d", idx+1)] = strings.TrimSpace(m)
					}
				}
			}
		}

		// Verbose output
		if *verbose && lastOutput != "" {
			for _, line := range strings.Split(strings.TrimSpace(lastOutput), "\n") {
				fmt.Printf("     \033[38;5;240m%s\033[0m\n", line)
			}
		}

		// Result
		if lastExitCode == 0 || (step.ExpectExit != nil && lastExitCode == *step.ExpectExit) {
			fmt.Printf("     \033[38;5;46m✓\033[0m\n")
			passed++
		} else {
			failureAction := step.OnFailure
			if failureAction == "" {
				failureAction = "abort"
			}
			fmt.Printf("     \033[38;5;196m✖ (exit %d)\033[0m\n", lastExitCode)
			if *verbose == false && lastOutput != "" {
				// Show last 3 lines of output on failure
				lines := strings.Split(strings.TrimSpace(lastOutput), "\n")
				start := len(lines) - 3
				if start < 0 {
					start = 0
				}
				for _, line := range lines[start:] {
					fmt.Printf("     \033[38;5;196m%s\033[0m\n", line)
				}
			}
			failed++
			if failureAction == "abort" {
				fmt.Printf("\n  \033[38;5;196m✖ Recipe aborted at step %d: %s\033[0m\n", stepNum, displayName)
				fmt.Printf("  \033[38;5;240m%d passed, %d skipped, %d failed\033[0m\n\n", passed, skipped, failed)
				os.Exit(1)
			}
		}
	}

	// Summary
	fmt.Printf("\n  ")
	if failed > 0 {
		fmt.Printf("\033[38;5;226m⚠ %s complete: %d passed, %d skipped, %d failed\033[0m\n\n", r.Name, passed, skipped, failed)
	} else {
		fmt.Printf("\033[38;5;46m✓ %s complete: %d passed, %d skipped\033[0m\n\n", r.Name, passed, skipped)
	}
}

// parseRecipe does a simple line-based YAML parse for recipe files.
// Avoids adding gopkg.in/yaml.v3 dependency.
func parseRecipe(content string) recipe {
	r := recipe{}
	var inSteps bool
	var currentStep *recipeStep

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Top-level keys (no indent)
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "	") {
			inSteps = false
			if strings.HasPrefix(trimmed, "name:") {
				r.Name = strings.TrimSpace(strings.TrimPrefix(trimmed, "name:"))
				r.Name = unquoteYAML(r.Name)
				} else if strings.HasPrefix(trimmed, "description:") {
				r.Description = strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
				r.Description = unquoteYAML(r.Description)
				} else if strings.HasPrefix(trimmed, "version:") {
				r.Version = strings.TrimSpace(strings.TrimPrefix(trimmed, "version:"))
				r.Version = unquoteYAML(r.Version)
			} else if strings.HasPrefix(trimmed, "steps:") {
				inSteps = true
			}
			continue
		}

		// Steps section
		if !inSteps {
			continue
		}

		// New step starts with "  - name:"
		if strings.HasPrefix(trimmed, "- name:") || strings.HasPrefix(trimmed, "- name :") {
			if currentStep != nil {
				r.Steps = append(r.Steps, *currentStep)
			}
			currentStep = &recipeStep{}
			val := strings.TrimSpace(strings.SplitN(trimmed, ":", 2)[1])
			currentStep.Name = unquoteYAML(val)
			continue
		}

		// Also handle "- " followed by other key on same line
		if strings.HasPrefix(trimmed, "- ") && currentStep == nil {
			currentStep = &recipeStep{}
			trimmed = strings.TrimPrefix(trimmed, "- ")
		} else if strings.HasPrefix(trimmed, "- ") && currentStep != nil {
			// Previous step done, start new one
			r.Steps = append(r.Steps, *currentStep)
			currentStep = &recipeStep{}
			trimmed = strings.TrimPrefix(trimmed, "- ")
		}

		if currentStep == nil {
			continue
		}

		// Parse key: value
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		// Only strip outer wrapping quotes (double or single)
		val = unquoteYAML(val)

		switch key {
		case "name":
			currentStep.Name = val
		case "description":
			currentStep.Description = val
		case "command":
			currentStep.Command = val
		case "os":
			currentStep.OS = val
		case "arch":
			currentStep.Arch = val
		case "expect":
			currentStep.Expect = val
		case "expect_regex":
			currentStep.ExpectRegex = val
		case "expect_exit":
			n, _ := strconv.Atoi(val)
			currentStep.ExpectExit = &n
		case "parse":
			currentStep.Parse = val
		case "pattern":
			currentStep.Pattern = val
		case "only_if":
			currentStep.OnlyIf = val
		case "when":
			currentStep.When = val
		case "on_failure":
			currentStep.OnFailure = val
		case "retries":
			currentStep.Retries, _ = strconv.Atoi(val)
		case "retry_delay":
			currentStep.RetryDelay = val
		case "labels_required":
			// Simple parse: [a, b] → skip for now
		}
	}

	if currentStep != nil {
		r.Steps = append(r.Steps, *currentStep)
	}

	return r
}

// unquoteYAML removes wrapping quotes from a YAML value.
// Only strips if the value starts AND ends with the same quote char.
func unquoteYAML(s string) string {
	if len(s) >= 2 {
		if s[0] == '"' && s[len(s)-1] == '"' {
			return s[1 : len(s)-1]
		}
		if s[0] == '\'' && s[len(s)-1] == '\'' {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// substituteVars replaces {{var}} placeholders in a string
func substituteVars(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
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
		bind = node.LanIP
	}
	if bind == "" {
		bind = node.NebulaIP
	}
	if bind == "" {
		bind = "0.0.0.0"
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
