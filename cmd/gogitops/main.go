// gogitops — fleet management via GitOps. Go binary per device, Git repo as truth.
package main

import (
	"crypto/sha256"
	"encoding/hex"
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
	"sync"
	"time"

	"github.com/bennbanks/gogitops/internal/agent"
	"github.com/bennbanks/gogitops/internal/agentmodules"
	"github.com/bennbanks/gogitops/internal/alert"
	"github.com/bennbanks/gogitops/internal/cli"
	"github.com/bennbanks/gogitops/internal/config"
	"github.com/bennbanks/gogitops/internal/dashboard"
	"github.com/bennbanks/gogitops/internal/starship"
)

var (
	version = "dev"
)

// ── reclone: global repo reset (-reclone / --reclone on any command) ───────

// looksLikeGogitopsRepo guards the wipe: the dir must be a git repo AND
// carry at least one gogitops marker dir. Refuses anything else — never
// nuke a random directory (or $HOME) by flag typo. ONE marker suffices: a
// broken repo is the whole reason -reclone exists, so the guard must stay
// passable when half the tree is already missing.
func looksLikeGogitopsRepo(dir string) bool {
	if dir == "" || dir == "/" {
		return false
	}
	if home, err := os.UserHomeDir(); err == nil && dir == home {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false
	}
	for _, m := range []string{"recipes", "nodes", "mesh.d", "modules", "test_modules", "install"} {
		if fi, err := os.Stat(filepath.Join(dir, m)); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// recloneRepo wipes the checkout and clones fresh from origin. Every check
// runs BEFORE the wipe; if any fails the tree is untouched.
func recloneRepo(dir string) {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if !looksLikeGogitopsRepo(dir) {
		fmt.Printf("  \033[38;5;196m✖ reclone refused: %s does not look like a gogitops repo (.git + recipes/nodes/mesh.d/...)\033[0m\n", dir)
		os.Exit(1)
	}
	originOut, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	origin := strings.TrimSpace(string(originOut))
	if err != nil || origin == "" {
		fmt.Printf("  \033[38;5;196m✖ reclone refused: no origin remote — wiping would lose the repo\033[0m\n")
		os.Exit(1)
	}
	branchOut, _ := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	branch := strings.TrimSpace(string(branchOut))
	if branch == "" || branch == "HEAD" {
		branch = "main"
	}
	fmt.Printf("  \033[38;5;226m⟲ recloning %s (branch %s)\033[0m\n", dir, branch)
	if err := os.RemoveAll(dir); err != nil {
		fmt.Printf("  \033[38;5;196m✖ wipe failed: %v\033[0m\n", err)
		os.Exit(1)
	}
	cout, err := exec.Command("git", "clone", "--quiet", "--branch", branch, origin, dir).CombinedOutput()
	if err != nil {
		fmt.Printf("  \033[38;5;196m✖ clone failed: %s\n  repo dir is gone — re-clone manually: git clone --branch %s %s %s\033[0m\n",
			strings.TrimSpace(string(cout)), branch, origin, dir)
		os.Exit(1)
	}
	fmt.Printf("  \033[38;5;46m✓ fresh clone\033[0m\n")
}

func main() {
	// Global -reclone/--reclone: wipe + fresh clone of the repo, then run
	// the command. Stripped from argv so every subcommand ignores it.
	// Safe-guarded: refuses dirs that don't look like a gogitops repo or
	// have no origin — never wipes what it can't re-download.
	if len(os.Args) > 1 {
		hasRC := false
		for _, a := range os.Args[1:] {
			if a == "-reclone" || a == "--reclone" {
				hasRC = true
			}
		}
		if hasRC {
			cleaned := []string{}
			repo := ""
			args := os.Args[1:]
			for i := 0; i < len(args); i++ {
				a := args[i]
				if a == "-reclone" || a == "--reclone" {
					continue
				}
				if (a == "-repo" || a == "--repo") && i+1 < len(args) {
					repo = args[i+1]
					cleaned = append(cleaned, a, args[i+1])
					i++
					continue
				}
				cleaned = append(cleaned, a)
			}
			recloneRepo(resolveRepoDir(repo))
			os.Args = append([]string{os.Args[0]}, cleaned...)
		}
	}
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
			fmt.Printf("\n  \033[38;5;141m%s\033[0m  \033[38;5;240m%s/%s\033[0m\n\n", version, runtime.GOOS, runtime.GOARCH)
		case "daemon", "run":
			runDaemon(os.Args[2:])
		case "dashboard":
			runDashboard(os.Args[2:])
		case "recipe":
			cmdRecipe(os.Args[2:])
		case "inspect":
			cmdInspect(os.Args[2:])
		case "update":
			cmdUpdate(os.Args[2:])

		case "test":
			cmdTest(os.Args[2:])
		case "attrs":
			cmdAttrs(os.Args[2:])
		case "modules":
			cmdModules(os.Args[2:])
		case "deploy":
			cmdDeploy(os.Args[2:])
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
	p := "\033[38;5;141m" // purple — section headers
	r := "\033[0m"

	fmt.Printf(`
  %sUSAGE%s

    gogitops <command> [subcommand] [flags]
    gogitops <command> -h         flags for that command
    gogitops                      show this manual
`, p, r)

	fmt.Printf(`
  %sSTATUS%s

    status              agent health — system, tags, services, peers, disk
    fleet               compact one-line-per-node fleet overview (parallel)
    info [node]         deep dive — attributes, tags, groups, services
    config              agent configuration — repo, git, labels, recipes
    logs                recent activity log             (-n N, default 30)
    watch               live auto-refresh (Ctrl+C)     (-interval N, default 5)
    version             version + platform

    agent commands accept -addr <host:port> (default 127.0.0.1:7780)
`, p, r)

	fmt.Printf(`
  %sAGENT CONTROL%s

    git-pull            force immediate git pull on the config repo
    restart             restart the agent daemon        (-hard: unit reload)
    set <key> <value>   edit config: repo, branch, dashboard, webhook,
                        hostname, bind, port, interval
    update              self-update check — running vs binaries branch
    daemon              run the agent daemon (unit/launchd entrypoint)
    dashboard           fleet status web UI (beacon, default port 7781)
`, p, r)

	fmt.Printf(`
  %sRECIPES & TESTS%s   (repo operations — run from any checkout)

    recipe new <name>   scaffold recipes/<name>/ (template + scripts/)
    recipe list         list all recipes
    recipe validate <f> parse + attribute-ref check (never executes)
    recipe run <name>   execute by name or YAML path
                        flags: -repo, -hostname, --pull (git pull first),
                        --dry-run, --verbose
    recipe run-all      pull + run EVERY recipe applicable to this node
                        (labels scope per recipe; test modules excluded).
                        compact output; -v for full steps. Recipe steps
                        are expected to be idempotent — applied state
                        shows as skipped, so this doubles as a
                        diagnostic sweep of what applies + what drifted.

  GLOBAL FLAG (works on any command):
    -reclone            wipe + fresh clone of the repo, then run the
                        command. Refuses dirs that don't look like a
                        gogitops repo or have no origin (nothing it
                        can't re-download is ever wiped)
    test list           available test modules
    test run <name>     run one test module
    test run-all        run all; exit 1 on fail (CI-able)
    attrs scan          universal attribute catalog (--json)
    attrs verify        validate all recipes; exit 1 on fail
    inspect             collect node attributes (test_modules/)
    modules             callable module library — repo modules/ + the
                        agent's embedded sets (resolution: recipe
                        scripts/ → repo modules/ → agent sets)

    built-in step types (no bash in the yaml): package:, schedule:,
    mount:  (mount: <label> + device: uuid=|label=|path + at: +
            options: + fstab:) — idempotent, sudo only when needed

    flags: -repo, --verbose, --json
`, p, r)

	fmt.Printf(`
  %sDEPLOY%s

    deploy              generate install snippets for a new node — daemon
                        command, agent.env, systemd unit, launchd plist
                        flags: -hostname <name> (required), -os linux|darwin,
                        -arch amd64|arm64, -format cmd|install|config|
                        systemd|launchd|all, -dashboard, -repo <url>
`, p, r)

	fmt.Printf(`
  %sFLAGS%s

    -addr <host:port>    target agent         (default 127.0.0.1:7780)
    -repo <path>         config repo          (default: auto-detect)
    -nodes <name=addr>   node list override   (fleet, dashboard)
    -hostname <name>     hostname override    (daemon, recipe run)
    -bind <addr>         health API bind      (daemon, default LAN IP)
    -port <N>            port                 (daemon 7780, dashboard 7781)
    -interval <sec>      check cycle          (daemon 60, watch 5)
    -webhook <url>       Discord alerts       (daemon; nenv:ns/key ok)

  %sREMOTE AGENTS%s

    gogitops status   -addr 10.0.0.229:7780
    gogitops config   -addr mini:7780
    gogitops git-pull -addr 10.200.0.4:7780
    gogitops restart  -addr 10.0.0.229:7780 -hard
`, p, r, p, r)

	fmt.Printf(`
  %sSELF-UPDATE%s

    Agents fetch the binaries branch (git transport) on every git tick and
    hot-swap + exec-restart when a newer VERSION appears. Release:

      git tag vX.Y.Z && git push origin vX.Y.Z

    CI builds all 5 platforms — linux amd64/arm64, darwin amd64/arm64,
    windows amd64 — and publishes VERSION + binaries to the branch. The
    daemon binary path must be user-writable (e.g. ~/.local/bin/gogitops);
    root-owned paths (like /usr/local/bin) cannot self-swap.

    Version pinning — versions.yaml in the config repo:

      global: v0.6.9            fleet-wide default
      groups: {gpu: v0.6.8}     per-label pin — lowest matching pin wins
      nodes: {framework: v0.6.9} per-hostname override ("latest" allowed)
      omitted / "latest" = track the binaries-branch VERSION, upgrades only.
      Pins are exact — downgrades included; every release stays fetchable
      on the binaries branch under versions/<tag>/bin/.

  %sINSTALL%s

    gogitops deploy -hostname <name> -os <os> -arch <arch>
      prints the daemon command, agent.env, systemd unit, or launchd
      plist for that node — copy-paste ready.

    From an existing checkout, grab a platform binary:
      git fetch origin binaries
      git show origin/binaries:bin/gogitops-<os>-<arch> > gogitops && chmod +x gogitops

  %sAGENT API%s

    GET  /v1/health          health snapshot (services, peers, system)
    GET  /v1/config          agent configuration
    GET  /v1/logs?limit=N    recent activity log
    GET  /v1/attrs/catalog   universal attribute catalog
    POST /v1/git/pull        force immediate git pull
    POST /v1/restart         restart agent
`, p, r, p, r, p, r)
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
	repoDir := fs.String("repo", ".", "path to gogitops repo (reads mesh.d/)")
	addrList := fs.String("nodes", "", "comma-separated name=address pairs (overrides mesh.d/)")
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
		cli.PrintInfo("no nodes found. Use --nodes flag or configure mesh.d/")
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

// ── universal attribute catalog ──────────────────────────────────────────
//
// scanRepoAttrs walks recipes/ and test_modules/ and catalogs every
// attribute the repo can produce — the universal attribute vocabulary.
// This is what makes attributes discoverable: `gogitops attrs scan` lists
// them, recipe validate warns on references to attrs nothing defines,
// and the agent serves the catalog at /v1/attrs/catalog.

type AttrDef struct {
	Name   string `json:"name"`   // e.g. docker_version, load.*, tests.pass
	Kind   string `json:"kind"`   // set_attr | attr_prefix | engine
	Source string `json:"source"` // recipe or test-module name
	File   string `json:"file"`   // path relative to repo
	Step   string `json:"step,omitempty"`
}

func scanRepoAttrs(repoDir string) []AttrDef {
	resolved := resolveRepoDir(repoDir)
	var defs []AttrDef
	seen := map[string]bool{}

	add := func(d AttrDef) {
		key := d.Name + "|" + d.Source
		if !seen[key] {
			seen[key] = true
			defs = append(defs, d)
		}
	}

	scanFile := func(path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		r := parseRecipe(string(data))
		rel, _ := filepath.Rel(resolved, path)
		for _, s := range r.Steps {
			if s.SetAttr != "" {
				add(AttrDef{Name: s.SetAttr, Kind: "set_attr", Source: r.Name, File: rel, Step: s.Name})
			}
			if s.AttrPrefix != "" {
				add(AttrDef{Name: s.AttrPrefix + ".*", Kind: "attr_prefix", Source: r.Name, File: rel, Step: s.Name})
			}
			if s.Assert != "" {
				add(AttrDef{Name: "assert." + s.Name + ".status", Kind: "engine", Source: r.Name, File: rel, Step: s.Name})
			}
		}
	}

	// recipes/<r>/<r>.yaml + recipes/<r>/tests/*.yaml
	if entries, err := os.ReadDir(filepath.Join(resolved, "recipes")); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			base := filepath.Join(resolved, "recipes", e.Name())
			scanFile(filepath.Join(base, e.Name()+".yaml"))
			if tests, err := os.ReadDir(filepath.Join(base, "tests")); err == nil {
				for _, t := range tests {
					if strings.HasSuffix(t.Name(), ".yaml") || strings.HasSuffix(t.Name(), ".yml") {
						scanFile(filepath.Join(base, "tests", t.Name()))
					}
				}
			}
		}
	}
	// test_modules/*.yaml
	if entries, err := os.ReadDir(filepath.Join(resolved, "test_modules")); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".yaml") || strings.HasSuffix(e.Name(), ".yml") {
				scanFile(filepath.Join(resolved, "test_modules", e.Name()))
			}
		}
	}
	// engine synthetics — the test suite always writes these
	for _, n := range []string{"tests.pass", "tests.fail", "tests.skip", "tests.total", "tests.failing", "tests.last_run", "tests.scope"} {
		add(AttrDef{Name: n, Kind: "engine", Source: "test-suite", File: "(engine)"})
	}

	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

// attrKnown: is an attribute resolvable? Exact catalog def, wildcard
// catalog def (load.* covers load.1), or a live value in the device store.
func attrKnown(plain string, defs []AttrDef, store map[string]string) bool {
	if v, ok := store[plain]; ok && v != "" {
		return true
	}
	for _, d := range defs {
		if d.Name == plain {
			return true
		}
		if strings.HasSuffix(d.Name, ".*") && strings.HasPrefix(plain, strings.TrimSuffix(d.Name, "*")) {
			return true
		}
	}
	return false
}

func suggestAttr(plain string, defs []AttrDef) string {
	best, bestDist := "", 99
	for _, d := range defs {
		name := strings.TrimSuffix(d.Name, ".*")
		dist := editDistance(plain, name)
		if dist < bestDist {
			best, bestDist = name, dist
		}
	}
	if bestDist <= 3 && bestDist < len(plain) {
		return " (did you mean " + best + "?)"
	}
	return ""
}

func editDistance(a, b string) int {
	la, lb := len(a), len(b)
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = minInt(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[lb]
}

func minInt(vals ...int) int {
	m := vals[0]
	for _, v := range vals[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

var attrRefRe = regexp.MustCompile(`attr\.[A-Za-z0-9_.-]+`)

// extractAttrRefs collects every attr.<name> referenced by a recipe's steps
// ({{attr.x}} substitutions and when_attr/only_if_attr conditions).
func extractAttrRefs(r recipe) []string {
	refs := map[string]bool{}
	for _, s := range r.Steps {
		for _, field := range []string{s.Command, s.When, s.WhenAttr, s.OnlyIfAttr, s.Expect, s.ExpectRegex} {
			if field == "" {
				continue
			}
			for _, m := range attrRefRe.FindAllString(field, -1) {
				refs[m] = true
			}
		}
	}
	out := make([]string, 0, len(refs))
	for k := range refs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cmdUpdate reports the self-update state: running version vs the binaries
// branch. Check-only — the daemon hot-swaps itself (internal/agent/update.go).
func cmdUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to the config repo (needs the binaries branch remote)")
	hostname := fs.String("hostname", "", "node hostname for pin resolution (default: system hostname)")
	fs.Parse(args)
	resolved := resolveRepoDir(*repoDir)

	published, err := agent.CheckBinaryUpdate(resolved)
	cli.Banner()
	running := agent.Version
	pub := strings.TrimPrefix(published, "v") // normalize display
	run := strings.TrimPrefix(running, "v")
	runDisp := "v" + run
	if run == "dev" {
		runDisp = "dev"
	}
	switch {
	case err != nil:
		fmt.Printf("  update check: unreachable — %v\n", err)
		fmt.Printf("  running: %s | binaries branch: unknown (feature off until reachable)\n", runDisp)
	case published == "":
		fmt.Printf("  update check: no VERSION on the binaries branch — self-update dormant\n")
		fmt.Printf("  running: %s\n", runDisp)
	case agent.IsNewer(published, running):
		fmt.Printf("  update check: AVAILABLE — %s → v%s (daemon swaps + exec-restarts on its git tick)\n", runDisp, pub)
	case published == running:
		fmt.Printf("  update check: up to date (%s)\n", runDisp)
	default:
		fmt.Printf("  update check: binaries branch older (v%s) than running (%s) — no action\n", pub, runDisp)
	}

	// Version pin policy (versions.yaml) + what THIS node resolves to.
	pins, perr := agent.LoadVersionPins(resolved)
	if perr != nil {
		fmt.Printf("  pins: unreadable — %v\n", perr)
		return
	}
	if pins == nil {
		return
	}
	fmt.Printf("  pins: %s\n", pins.PinSummary())
	host := config.DetectHostname()
	if *hostname != "" {
		host = *hostname
	}
	var labels []string
	if mesh, merr := config.LoadMesh(resolved); merr == nil {
		for _, p := range mesh.Peers {
			if strings.EqualFold(p.Hostname, host) {
				labels = p.Labels
				break
			}
		}
	}
	tgt, src := agent.ResolveVersion(pins, host, labels)
	fmt.Printf("  this node (%s): %s via %s\n", host, tgt, src)
}

func cmdAttrs(args []string) {
	// optional subcommands read nicer: gogitops attrs scan / attrs verify
	sub := ""
	if len(args) > 0 && (args[0] == "scan" || args[0] == "verify") {
		sub = args[0]
		args = args[1:]
	}
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprintf(os.Stderr, "usage: gogitops attrs [scan|verify]\n\nSubcommands:\n  scan    universal attribute catalog — every attr the repo can produce\n  verify  validate all recipes (parse + attr refs, never executed)\n\nFlags: -repo <path>, --json\n")
		os.Exit(0)
	}
	fs := flag.NewFlagSet("attrs", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to gogitops repo")
	jsonOut := fs.Bool("json", false, "machine-readable JSON")
	fs.Parse(args)

	if sub == "verify" {
		os.Exit(cmdAttrsVerify(resolveRepoDir(*repoDir), false, *jsonOut))
	}

	defs := scanRepoAttrs(*repoDir)
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(defs)
		return
	}

	cli.Banner()
	fmt.Printf("\n  \033[1m\033[38;5;141mUniversal attribute catalog\033[0m\n")
	fmt.Printf("  \033[38;5;240mevery attribute the repo can produce — recipes, tests, engine\033[0m\n\n")
	bySource := map[string][]AttrDef{}
	for _, d := range defs {
		bySource[d.Source] = append(bySource[d.Source], d)
	}
	sources := make([]string, 0, len(bySource))
	for s := range bySource {
		sources = append(sources, s)
	}
	sort.Strings(sources)
	for _, s := range sources {
		fmt.Printf("  \033[38;5;38m%s\033[0m\n", s)
		for _, d := range bySource[s] {
			kind := "\033[38;5;240mset_attr\033[0m"
			switch d.Kind {
			case "attr_prefix":
				kind = "\033[38;5;80mcaptures\033[0m"
			case "engine":
				kind = "\033[38;5;141mengine\033[0m"
			}
			fmt.Printf("    \033[38;5;46m●\033[0m %-28s %s\n", d.Name, kind)
		}
	}
	fmt.Printf("\n  \033[1m%d attributes\033[0m across %d sources\n\n", len(defs), len(sources))
}

func cmdAttrsVerify(resolved string, verbose bool, jsonOut bool) int {
	results, attrs, setKeys := verifyRepoRecipes(resolved, verbose)
	if jsonOut {
		type verdict struct {
			Recipe string `json:"recipe"`
			Valid  bool   `json:"valid"`
		}
		var out []verdict
		for _, r := range results {
			out = append(out, verdict{strings.TrimPrefix(r.name, "validate:"), r.status == "pass"})
		}
		w := json.NewEncoder(os.Stdout)
		w.SetIndent("", "  ")
		_ = w.Encode(out)
	} else {
		cli.Banner()
		fmt.Printf("\n  \033[1m\033[38;5;141mRecipe verification\033[0m \033[38;5;240m(parse + attr refs, never executed)\033[0m\n\n")
		for _, r := range results {
			if r.status == "pass" {
				fmt.Printf("  \033[38;5;46m✓\033[0m %s\n", strings.TrimPrefix(r.name, "validate:"))
			} else {
				fmt.Printf("  \033[38;5;196m✖\033[0m %s\n", strings.TrimPrefix(r.name, "validate:"))
			}
		}
		failed := 0
		for _, r := range results {
			if r.status == "fail" {
				failed++
			}
		}
		fmt.Printf("\n  \033[1m%d recipes\033[0m verified, \033[38;5;196m%d failed\033[0m\n\n", len(results), failed)
	}
	// persist verdicts as local attrs (decentralized, private — stays on node)
	persistTestAttrs(attrs, setKeys, results, "recipe-verification")
	if failed := countFailed(results); failed > 0 {
		return 1
	}
	return 0
}

func countFailed(results []testResult) int {
	n := 0
	for _, r := range results {
		if r.status == "fail" {
			n++
		}
	}
	return n
}

func cmdRecipe(args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintf(os.Stderr, "usage: gogitops recipe <subcommand>\n\nSubcommands:\n  new <name>        Scaffold a new recipe directory with template + examples\n  list              List all recipes in the repo\n  validate <file>   Validate a recipe YAML file\n  run <file>        Execute a recipe YAML file\n  run <name>        Execute a recipe by name (searches recipes/ dir)\n  run-all           Pull + run EVERY recipe applicable to this node\n")
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
		recipeRun(args[1:], nil)
	case "run-all":
		recipeRunAll(args[1:])
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
		if (args[i] == "--repo" || args[i] == "-repo") && i+1 < len(args) {
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
# Universal step vocabulary — the AGENT translates per-OS, the recipe never changes.
#   command:   plain bash (universal prereq)
#   package: X + sources: "[pkg:X, pip:alt]"   → agent picks apt/dnf/apk/brew/...
#   schedule: hourly + command: Y              → agent installs idempotent cron
#   when: guard runs on every OS (detect capabilities, don't assume them)
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

	// Recipe-local scripts dir — the PRIMARY script location (self-contained
	// recipes; shared recipes/scripts/ is only a fallback for shared utilities)
	scriptsDir := recipeDir + "/scripts"
	if err := os.MkdirAll(scriptsDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "❌ failed to create scripts dir: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Created recipe: %s\n", recipeDir)
	fmt.Printf("   %s  — YAML template\n", recipeFile)
	fmt.Printf("   %s  — recipe-local scripts (primary script location)\n", scriptsDir)
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

	// Attribute reference check (WARNINGS, not blocks): every attr.<x> the
	// recipe references must be defined somewhere in the repo catalog or
	// exist live in the device store — catches typos before runtime.
	var warnings []string
	r := parseRecipe(content)
	if len(r.Steps) > 0 {
		store := readDeviceAttrs()
		defs := scanRepoAttrs(repoRootFromFile(file))
		for _, ref := range extractAttrRefs(r) {
			plain := strings.TrimPrefix(ref, "attr.")
			if !attrKnown(plain, defs, store) {
				warnings = append(warnings, fmt.Sprintf("unknown attribute %q — not defined by any recipe/module, not in the device store%s", ref, suggestAttr(plain, defs)))
			}
		}
	}

	if len(warnings) > 0 {
		fmt.Printf("✅ %s looks valid (%d warning%s)\n", file, len(warnings), map[bool]string{true: "", false: "s"}[len(warnings) == 1])
		for _, wn := range warnings {
			fmt.Printf("   \033[38;5;178m⚠ %s\033[0m\n", wn)
		}
		return
	}
	fmt.Printf("✅ %s looks valid\n", file)
}

// verifyRepoRecipes validates every recipe in the repo — parse + attribute
// references — WITHOUT running them. Runs on the agent cycle (decentralized:
// each node computes verdicts from its own checkout) and via `attrs verify`.
// Verdicts persist as local attrs: recipe.<name>.valid / .warnings —
// readable by other recipes via {{attr.recipe.<name>.valid}}.
func verifyRepoRecipes(resolved string, verbose bool) ([]testResult, map[string]string, map[string]bool) {
	var results []testResult
	attrs := map[string]string{}
	setKeys := map[string]bool{}
	store := readDeviceAttrs()
	defs := scanRepoAttrs(resolved)

	recipesDir := filepath.Join(resolved, "recipes")
	entries, err := os.ReadDir(recipesDir)
	if err != nil {
		return results, attrs, setKeys
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		file := filepath.Join(recipesDir, name, name+".yaml")
		content, err := os.ReadFile(file)
		if err != nil {
			continue // no recipe file in this dir
		}
		r := parseRecipe(string(content))
		if r.Name == "" || r.TestModule {
			continue
		}

		// attribute references: known = repo catalog ∪ device store
		var warnings []string
		for _, ref := range extractAttrRefs(r) {
			plain := strings.TrimPrefix(ref, "attr.")
			if attrKnown(plain, defs, store) {
				continue
			}
			warnings = append(warnings, fmt.Sprintf("unknown attribute \"attr.%s\"%s", plain, suggestAttr(plain, defs)))
		}

		valid := len(warnings) == 0
		key := "attr.recipe." + name + ".valid"
		attrs[key] = strconv.FormatBool(valid)
		setKeys[key] = true
		if len(warnings) > 0 {
			wkey := "attr.recipe." + name + ".warnings"
			attrs[wkey] = strings.Join(warnings, "; ")
			setKeys[wkey] = true
		}

		status := "pass"
		if !valid {
			status = "fail"
		}
		results = append(results, testResult{"recipes", "validate:" + name, status})
		if verbose {
			for _, w := range warnings {
				fmt.Printf("    \033[38;5;214m⚠ %s: %s\033[0m\n", name, w)
			}
		}
	}
	return results, attrs, setKeys
}

// repoRootFromFile walks up from a recipe file to find the repo root
// (the nearest ancestor containing a recipes/ dir); falls back to the
// standard auto-detect (cwd or ~/.config/gogitops) for files outside a repo.
func repoRootFromFile(file string) string {
	dir := filepath.Dir(file)
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "recipes")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return resolveRepoDir(".")
}

// ── Recipe runner ─────────────────────────────────────────────────────────

type recipeStep struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Command      string   `yaml:"command"`
	Script       string   `yaml:"script"`
	ScriptArgs   string   `yaml:"script_args"`
	ScriptLang   string   `yaml:"script_lang"`
	Package      string   `yaml:"package"`
	Sources      []string `yaml:"sources"`
	Schedule     string   `yaml:"schedule"`
	OS           string   `yaml:"os"`
	Arch         string   `yaml:"arch"`
	LabelsReq    []string `yaml:"labels_required"`
	LabelsExcl   []string `yaml:"labels_exclude"`
	Expect       string   `yaml:"expect"`
	ExpectRegex  string   `yaml:"expect_regex"`
	ExpectExit   *int     `yaml:"expect_exit"`
	Parse        string   `yaml:"parse"`
	Pattern      string   `yaml:"pattern"`
	OnlyIf       string   `yaml:"only_if"`
	When         string   `yaml:"when"`
	OnFailure    string   `yaml:"on_failure"`
	Retries      int      `yaml:"retries"`
	RetryDelay   string   `yaml:"retry_delay"`
	Assert       string   `yaml:"assert"`
	SetAttr      string   `yaml:"set_attr"`
	AttrPrefix   string   `yaml:"attr_prefix"`
	WhenAttr     string   `yaml:"when_attr"`
	OnlyIfAttr   string   `yaml:"only_if_attr"`
	Mount        string   `yaml:"mount"`
	MountDevice  string   `yaml:"device"`
	MountAt      string   `yaml:"at"`
	MountOptions string   `yaml:"options"`
	MountFstab   bool     `yaml:"fstab"`
}

type recipe struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Version     string   `yaml:"version"`
	Labels      []string `yaml:"labels"`
	TestModule  bool     `yaml:"test_module"`
	Params      []struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Required    bool   `yaml:"required"`
	} `yaml:"params"`
	Steps []recipeStep `yaml:"steps"`
}

func recipeRun(args []string, all *runAllCtx) {
	fs := flag.NewFlagSet("recipe run", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to gogitops repo")
	hostFlag := fs.String("hostname", "", "node hostname for label scoping + {{hostname}} (default: detected system hostname)")
	dryRun := fs.Bool("dry-run", false, "print commands without executing")
	verbose := fs.Bool("verbose", false, "show full command output")
	vAlias := fs.Bool("v", false, "alias for --verbose")
	pull := fs.Bool("pull", false, "git pull the repo before running (fresh recipes)")

	// Separate flags from positional args
	var positional []string
	var flagArgs []string
	valueFlags := map[string]bool{"--repo": true, "-repo": true, "--hostname": true, "-hostname": true}
	for i := 0; i < len(args); i++ {
		if valueFlags[args[i]] && i+1 < len(args) {
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
		fmt.Fprintf(os.Stderr, "usage: gogitops recipe run <file.yaml|name> [--repo path] [--pull] [--dry-run] [--verbose]\n\n")
		fmt.Fprintf(os.Stderr, "  Executes a recipe step-by-step.\n")
		fmt.Fprintf(os.Stderr, "  --pull      Git pull the repo first (one-command fresh run)\n")
		fmt.Fprintf(os.Stderr, "  --dry-run   Print each command without running it\n")
		fmt.Fprintf(os.Stderr, "  --verbose   Show full command output (stdout+stderr)\n")
		os.Exit(1)
	}

	target := positional[0]
	resolved := resolveRepoDir(*repoDir)

	// --pull: fetch fresh recipes before running. Best-effort — a failed
	// pull (offline, dirty tree) warns and runs what's on disk.
	if *pull {
		gitPullRepo(resolved)
	}

	// Resolve recipe file path
	// If the target is a directory (e.g. directory-based recipe name), fall
	// through to the candidate paths below instead of failing to read it.
	recipeFile := target
	if fi, err := os.Stat(recipeFile); err != nil || fi.IsDir() {
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
		"repo": resolved,
	}
	// Hostname: --hostname flag wins (fleet identity), else detected system hostname
	if *hostFlag != "" {
		vars["hostname"] = *hostFlag
	} else {
		vars["hostname"] = config.DetectHostname()
	}
	// Detect OS/arch
	vars["os"] = runtime.GOOS
	vars["arch"] = runtime.GOARCH

	// Attribute map — persists across steps within a recipe run.
	// Hydrated from the device attr store first, so every recipe can read
	// the node's test results + collected attrs: {{attr.tests.fail}},
	// when_attr: "attr.tests.fail == 0", {{attr.docker_version}}, ...
	attrs := map[string]string{}
	hydrateDeviceAttrs(attrs, vars)

	// Banner
	// run-all: compact one-liner per recipe; single: full banner
	showDetail := all == nil || (*verbose || *vAlias)
	if all == nil {
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
	} else {
		dr := ""
		if *dryRun {
			dr = " (DRY RUN)"
		}
		fmt.Printf("\n  \033[1m\033[38;5;141m▸ %s\033[0m \033[38;5;240m— %d steps%s\033[0m\n", r.Name, len(r.Steps), dr)
	}

	// Node labels for label scoping. Only loaded when the node yaml exists —
	// recipe runs must not trigger LoadNode's auto-registration side effect.
	nodeLabels := []string{}
	if _, err := os.Stat(filepath.Join(vars["repo"], "nodes", vars["hostname"]+".yaml")); err == nil {
		if n, err := config.LoadNode(vars["repo"], vars["hostname"]); err == nil {
			nodeLabels = n.Labels
		}
	}

	// Recipe-level label gate (recipe labels: node must have ALL of them;
	// empty = all nodes)
	if len(r.Labels) > 0 && !labelsMatch(nodeLabels, r.Labels, nil) {
		fmt.Printf("  \033[38;5;240m⊘ %s — node labels don't satisfy recipe labels %v\033[0m\n", r.Name, r.Labels)
		if all != nil {
			all.recipeSkipped++
		}
		return
	}

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

		// Substitute variables in command or script
		cmd := substituteVars(step.Command, vars)

		// If script: is specified, resolve and execute a script file
		if step.Script != "" {
			scriptPath := resolveScriptPath(step.Script, resolved, recipeFile)
			if scriptPath == "" {
				fmt.Printf("  \033[38;5;196m✖ script not found: %s (searched recipes/<name>/scripts/, modules/, agent sets)\033[0m\n", step.Script)
				failed++
				if step.OnFailure == "" || step.OnFailure == "abort" {
					if all != nil {
						all.recipeFailed++
						all.failedAt = fmt.Sprintf("%s step %d: %s (script not found: %s)", r.Name, stepNum, displayName, step.Script)
						return
					}
					fmt.Printf("\n  \033[38;5;196m✖ Recipe aborted at step %d: %s\033[0m\n", stepNum, displayName)
					fmt.Printf("  \033[38;5;240m%d passed, %d skipped, %d failed\033[0m\n\n", passed, skipped, failed)
					os.Exit(1)
				}
				continue
			}
			// Substitute vars in script args
			scriptArgs := substituteVars(step.ScriptArgs, vars)
			// Determine execution method based on extension or script_lang
			lang := step.ScriptLang
			if lang == "" {
				lang = detectScriptLang(scriptPath)
			}
			switch lang {
			case "go":
				// Go modules: compiled once + cached (rebuilt on source change)
				cmd = fmt.Sprintf("%s %s", shellQuote(compiledGoModule(scriptPath)), scriptArgs)
			case "bash", "sh":
				cmd = fmt.Sprintf("bash %s %s", shellQuote(scriptPath), scriptArgs)
			case "python", "python3":
				cmd = fmt.Sprintf("python3 %s %s", shellQuote(scriptPath), scriptArgs)
			default:
				// Auto-detect: .sh → bash, .go → go run, .py → python3, otherwise try direct execution
				cmd = fmt.Sprintf("%s %s", shellQuote(scriptPath), scriptArgs)
			}
		}

		// Universal step types — the agent translates to local reality.
		// package: figlet → package-manager install (brew/apt/dnf/apk/…)
		// schedule: hourly + command: → idempotent crontab install
		// Translated steps reuse the whole runner pipeline below.
		if step.Package != "" {
			cmd = translatePackage(step)
		}
		if step.Schedule != "" {
			cmd = translateSchedule(step, cmd)
		}
		if step.Mount != "" {
			cmd = translateMount(step)
		}

		// OS filter
		if step.OS != "" && step.OS != vars["os"] {
			if showDetail {
				fmt.Printf("  \033[38;5;240m⊘ %d/%d %s (skipped: os=%s, host=%s)\033[0m\n", stepNum, len(r.Steps), displayName, step.OS, vars["os"])
			}
			skipped++
			continue
		}
		// Arch filter
		if step.Arch != "" && step.Arch != vars["arch"] {
			if showDetail {
				fmt.Printf("  \033[38;5;240m⊘ %d/%d %s (skipped: arch=%s)\033[0m\n", stepNum, len(r.Steps), displayName, step.Arch)
			}
			skipped++
			continue
		}
		// Label filters: labels_required = node must have ALL of them;
		// labels_exclude = node must have NONE of them
		if !labelsMatch(nodeLabels, step.LabelsReq, step.LabelsExcl) {
			if showDetail {
				fmt.Printf("  \033[38;5;240m⊘ %d/%d %s (skipped: labels required=%v exclude=%v)\033[0m\n", stepNum, len(r.Steps), displayName, step.LabelsReq, step.LabelsExcl)
			}
			skipped++
			continue
		}

		// when: condition — run shell test, skip if exit != 0
		if step.When != "" {
			whenCmd := substituteVars(step.When, vars)
			wCmd := exec.Command("bash", "-c", whenCmd)
			if err := wCmd.Run(); err != nil {
				if showDetail {
					fmt.Printf("  \033[38;5;240m⊘ %d/%d %s (skipped: when condition false)\033[0m\n", stepNum, len(r.Steps), displayName)
				}
				skipped++
				continue
			}
		}

		// only_if: condition (inverse of when — skip if false)
		if step.OnlyIf != "" {
			onlyCmd := substituteVars(step.OnlyIf, vars)
			oCmd := exec.Command("bash", "-c", onlyCmd)
			if err := oCmd.Run(); err != nil {
				if showDetail {
					fmt.Printf("  \033[38;5;240m⊘ %d/%d %s (skipped: only_if false)\033[0m\n", stepNum, len(r.Steps), displayName)
				}
				skipped++
				continue
			}
		}

		// when_attr: condition on attributes — skip if condition is false
		if step.WhenAttr != "" {
			cond := substituteVars(step.WhenAttr, vars)
			if !evalAttrCondition(cond, attrs) {
				if showDetail {
					fmt.Printf("  \033[38;5;240m⊘ %d/%d %s (skipped: when_attr false)\033[0m\n", stepNum, len(r.Steps), displayName)
				}
				skipped++
				continue
			}
		}

		// only_if_attr: condition on attributes — skip if condition is false
		if step.OnlyIfAttr != "" {
			cond := substituteVars(step.OnlyIfAttr, vars)
			if !evalAttrCondition(cond, attrs) {
				if showDetail {
					fmt.Printf("  \033[38;5;240m⊘ %d/%d %s (skipped: only_if_attr false)\033[0m\n", stepNum, len(r.Steps), displayName)
				}
				skipped++
				continue
			}
		}

		// Description
		if showDetail {
			if step.Description != "" {
				fmt.Printf("  \033[38;5;38m▸ %d/%d %s\033[0m — \033[38;5;245m%s\033[0m\n", stepNum, len(r.Steps), displayName, step.Description)
			} else {
				fmt.Printf("  \033[38;5;38m▸ %d/%d %s\033[0m\n", stepNum, len(r.Steps), displayName)
			}
		}

		// Dry run — just print
		if *dryRun {
			if showDetail {
				fmt.Printf("     \033[38;5;240m$ %s\033[0m\n", cmd)
			}
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

		// Assert: record pass/fail as an attribute
		if step.Assert != "" {
			assertPassed := lastExitCode == 0 || (step.ExpectExit != nil && lastExitCode == *step.ExpectExit)
			if step.Expect != "" && !strings.Contains(lastOutput, step.Expect) {
				assertPassed = false
			}
			if step.ExpectRegex != "" {
				matched, _ := regexp.MatchString(step.ExpectRegex, lastOutput)
				if !matched {
					assertPassed = false
				}
			}
			attrKey := "attr.assert." + displayName
			if assertPassed {
				attrs[attrKey+".status"] = "pass"
				if showDetail {
					fmt.Printf("     \033[38;5;46m✓ assert: %s\033[0m\n", step.Assert)
				}
			} else {
				attrs[attrKey+".status"] = "fail"
				fmt.Printf("     \033[38;5;196m✖ assert: %s\033[0m\n", step.Assert)
			}
		}

		// set_attr: store command output as an attribute
		if step.SetAttr != "" {
			attrVal := strings.TrimSpace(lastOutput)
			attrs["attr."+step.SetAttr] = attrVal
			// Also make available as a variable for subsequent steps
			vars["attr."+step.SetAttr] = attrVal
			if showDetail {
				fmt.Printf("     \033[38;5;178m⊙ attr.%s = %s\033[0m\n", step.SetAttr, truncateStr(attrVal, 60))
			}
		}

		// attr_prefix: store regex capture groups as attributes
		if step.AttrPrefix != "" && step.Parse == "regex" && step.Pattern != "" {
			re, err := regexp.Compile(step.Pattern)
			if err == nil {
				matches := re.FindStringSubmatch(lastOutput)
				if len(matches) > 1 {
					for idx, m := range matches[1:] {
						key := fmt.Sprintf("attr.%s.%d", step.AttrPrefix, idx+1)
						val := strings.TrimSpace(m)
						attrs[key] = val
						vars[key] = val
						if showDetail {
							fmt.Printf("     \033[38;5;178m⊙ %s = %s\033[0m\n", key, truncateStr(val, 60))
						}
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
			if showDetail {
				fmt.Printf("     \033[38;5;46m✓\033[0m\n")
			}
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
				if all != nil {
					all.recipeFailed++
					all.failedAt = fmt.Sprintf("%s step %d: %s (exit %d)", r.Name, stepNum, displayName, lastExitCode)
					return
				}
				fmt.Printf("\n  \033[38;5;196m✖ Recipe aborted at step %d: %s\033[0m\n", stepNum, displayName)
				fmt.Printf("  \033[38;5;240m%d passed, %d skipped, %d failed\033[0m\n\n", passed, skipped, failed)
				os.Exit(1)
			}
		}
	}

	// Summary
	if all != nil {
		if failed > 0 {
			fmt.Printf("  \033[38;5;226m⚠ %s: %d passed, %d skipped, %d failed\033[0m\n", r.Name, passed, skipped, failed)
			all.recipeFailed++
			if all.failedAt == "" {
				all.failedAt = fmt.Sprintf("%s (%d failed steps)", r.Name, failed)
			}
		} else {
			fmt.Printf("  \033[38;5;46m✓ %s: %d passed, %d skipped\033[0m\n", r.Name, passed, skipped)
			all.recipeOK++
		}
		return
	}
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
			} else if strings.HasPrefix(trimmed, "labels:") {
				r.Labels = parseSourceList(strings.TrimSpace(strings.TrimPrefix(trimmed, "labels:")))
			} else if strings.HasPrefix(trimmed, "test_module:") {
				v := strings.TrimSpace(strings.TrimPrefix(trimmed, "test_module:"))
				r.TestModule = v == "true" || v == "yes"
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
		case "script":
			currentStep.Script = val
		case "script_args":
			currentStep.ScriptArgs = val
		case "script_lang":
			currentStep.ScriptLang = val
		case "package":
			currentStep.Package = val
		case "sources":
			currentStep.Sources = parseSourceList(val)
		case "schedule":
			currentStep.Schedule = val
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
		case "assert":
			currentStep.Assert = val
		case "set_attr":
			currentStep.SetAttr = val
		case "attr_prefix":
			currentStep.AttrPrefix = val
		case "when_attr":
			currentStep.WhenAttr = val
		case "only_if_attr":
			currentStep.OnlyIfAttr = val
		case "labels_required":
			currentStep.LabelsReq = parseSourceList(val)
		case "labels_exclude":
			currentStep.LabelsExcl = parseSourceList(val)
		case "mount":
			currentStep.Mount = val
		case "device":
			currentStep.MountDevice = val
		case "at":
			currentStep.MountAt = val
		case "options":
			currentStep.MountOptions = val
		case "fstab":
			currentStep.MountFstab = val == "true" || val == "yes"
		}
	}

	if currentStep != nil {
		r.Steps = append(r.Steps, *currentStep)
	}

	return r
}

// labelsMatch reports whether nodeLabels satisfies required (all present)
// and exclude (none present). Empty required+exclude = always matches.
func labelsMatch(nodeLabels, required, exclude []string) bool {
	have := map[string]bool{}
	for _, l := range nodeLabels {
		have[l] = true
	}
	for _, l := range required {
		if !have[l] {
			return false
		}
	}
	for _, l := range exclude {
		if have[l] {
			return false
		}
	}
	return true
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

// evalAttrCondition evaluates an attribute-based condition expression.
// Supported formats:
//
//	attr.<name>                  — truthy check (non-empty and not "false" and not "0")
//	attr.<name> == <value>       — equality
//	attr.<name> != <value>       — inequality
//	attr.<name> contains <value> — substring check
func evalAttrCondition(expr string, attrs map[string]string) bool {
	expr = strings.TrimSpace(expr)

	// attr.<name> contains <value>
	if parts := strings.SplitN(expr, " contains ", 2); len(parts) == 2 {
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		attrVal, ok := attrs[key]
		if !ok {
			return false
		}
		return strings.Contains(attrVal, val)
	}

	// attr.<name> != <value>
	if parts := strings.SplitN(expr, " != ", 2); len(parts) == 2 {
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		attrVal, ok := attrs[key]
		if !ok {
			return val != "" // not present ≠ non-empty value → true; not present ≠ "" → false
		}
		return attrVal != val
	}

	// attr.<name> == <value>
	if parts := strings.SplitN(expr, " == ", 2); len(parts) == 2 {
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		attrVal, ok := attrs[key]
		if !ok {
			return val == ""
		}
		return attrVal == val
	}

	// Truthy check: attr.<name> — true if key exists and value is non-empty, not "false", not "0"
	attrVal, ok := attrs[expr]
	if !ok {
		return false
	}
	attrVal = strings.TrimSpace(attrVal)
	return attrVal != "" && attrVal != "false" && attrVal != "0"
}

// truncateStr truncates a string to maxLen characters with "..." if needed
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	// Truncate at first newline within range, or hard truncate
	firstNL := strings.Index(s[:maxLen], "\n")
	if firstNL >= 0 {
		return s[:firstNL] + "..."
	}
	return s[:maxLen-3] + "..."
}

// ── Script resolution helpers ────────────────────────────────────────────

// resolveScriptPath finds a script file by searching (self-containment first):
// 1. recipes/<recipe-name>/scripts/<name> (recipe-local — the primary location)
// 2. modules/<name> (fleet library — cross-recipe shared modules)
// 3. As-is (absolute or relative path)
func resolveScriptPath(scriptName string, repoDir string, recipeFile string) string {
	// If it's already a valid path, use it directly
	if _, err := os.Stat(scriptName); err == nil {
		return scriptName
	}

	// Derive recipe directory for recipe-local scripts
	recipeDir := filepath.Dir(recipeFile)
	recipeName := filepath.Base(recipeDir)
	// If recipe is a flat file (not in a dir), recipeName is the filename
	if recipeName == "recipes" {
		recipeName = strings.TrimSuffix(filepath.Base(recipeFile), ".yaml")
	}

	candidates := []string{
		filepath.Join(recipeDir, "scripts", scriptName),
		filepath.Join(repoDir, "recipes", recipeName, "scripts", scriptName),
		filepath.Join(repoDir, "modules", scriptName), // fleet library — cross-recipe shared modules
	}

	// Agent-embedded module sets — the agent's own standard library,
	// extracted to ~/.cache/gogitops/agent-modules/. Repo-local and repo
	// modules/ win, so admins can override any embedded module.
	if amDir := agentmodules.Dir(); amDir != "" {
		candidates = append(candidates, filepath.Join(amDir, scriptName))
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}

	return ""
}

// compiledGoModule returns the path of a cached compiled binary for a .go
// module, rebuilding when the source hash changes. `go run` recompiles on
// every call (~1-2s each); cached binaries make fleet modules instant.
// Falls back to the source path if compilation fails (callers use go run).
func compiledGoModule(srcPath string) string {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return srcPath
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])[:12]
	home, err := os.UserHomeDir()
	if err != nil {
		return srcPath
	}
	cacheDir := filepath.Join(home, ".cache", "gogitops", "modules")
	binPath := filepath.Join(cacheDir, strings.TrimSuffix(filepath.Base(srcPath), ".go")+"-"+hash)
	if _, err := os.Stat(binPath); err == nil {
		return binPath // cached, still fresh
	}
	_ = os.MkdirAll(cacheDir, 0755)
	build := exec.Command("go", "build", "-o", binPath, srcPath)
	if err := build.Run(); err != nil {
		return srcPath // let go run surface the compile error
	}
	return binPath
}

// detectScriptLang determines the script language from file extension
func detectScriptLang(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".sh":
		return "bash"
	case ".go":
		return "go"
	case ".py":
		return "python3"
	default:
		return ""
	}
}

// shellQuote wraps a path in single quotes for safe shell execution.
// If the path contains single quotes, escapes them.
func shellQuote(s string) string {
	if !strings.ContainsAny(s, " 	'\"$`\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// translateMount builds the bash for the mount: universal step type —
// mounting a drive is a FIRST-CLASS agent function, not a page of YAML.
//
//	step:
//	  - name: backup-drive
//	    mount: backup                # label (used for fstab comment + attrs)
//	    device: uuid=XXXX | label=NAME | /dev/sdb1
//	    at: /mnt/backup              # default /mnt/<label>
//	    options: defaults,noatime    # default defaults
//	    fstab: true                  # idempotent /etc/fstab persistence
//
// Idempotent: already-mounted is a pass with state=already-mounted. fstab
// appends only when the mountpoint has no line yet (never clobbers). sudo
// is used only when not root. darwin gets a clear failure (use os: linux).
func translateMount(step recipeStep) string {
	if runtime.GOOS == "darwin" {
		return `echo "mount: darwin mounts via diskutil — gate this step with os: linux or use an admin script"; exit 1`
	}
	dev := step.MountDevice
	switch {
	case strings.HasPrefix(dev, "uuid="):
		dev = "/dev/disk/by-uuid/" + strings.TrimPrefix(dev, "uuid=")
	case strings.HasPrefix(dev, "label="):
		dev = "/dev/disk/by-label/" + strings.TrimPrefix(dev, "label=")
	}
	at := step.MountAt
	if at == "" {
		at = "/mnt/" + step.Mount
	}
	opts := step.MountOptions
	if opts == "" {
		opts = "defaults"
	}
	fstabBlock := ""
	if step.MountFstab {
		fstabBlock = fmt.Sprintf(`
if ! grep -qE "[[:space:]]%s[[:space:]]" /etc/fstab 2>/dev/null; then
  echo "$M_DEV $M_AT auto $M_OPTS 0 0 # gogitops: %s" | $SUDO tee -a /etc/fstab >/dev/null
  echo "fstab=entry-added point=$M_AT"
else
  echo "fstab=present point=$M_AT"
fi`, at, step.Mount)
	}
	return fmt.Sprintf(`M_DEV=%s; M_AT=%s; M_OPTS=%s
[ -e "$M_DEV" ] || { echo "state=fail reason=device-not-found device=$M_DEV"; exit 1; }
SUDO=""; [ "$(id -u)" != 0 ] && SUDO="sudo"%s
if mountpoint -q "$M_AT" 2>/dev/null; then
  SRC=$(findmnt -n -o SOURCE "$M_AT" 2>/dev/null || awk -v at="$M_AT" '$2==at{print $1}' /proc/mounts | head -1)
  echo "state=already-mounted device=$SRC point=$M_AT"
  exit 0
fi
mkdir -p "$M_AT" || exit 1
$SUDO mount -o "$M_OPTS" "$M_DEV" "$M_AT" || { echo "state=fail reason=mount-failed device=$M_DEV point=$M_AT"; exit 1; }
echo "state=mounted device=$M_DEV point=$M_AT opts=$M_OPTS"`,
		shellQuote(dev), shellQuote(at), shellQuote(opts), fstabBlock)
}

// ── modules: list callable modules (repo library + agent sets) ────────────

func cmdModules(args []string) {
	repoDir := resolveRepoDir(".")
	cli.Banner()
	fmt.Printf("\n  \033[1m\033[38;5;141mModule library\033[0m \033[38;5;240mrecipes call these via script: <name>\033[0m\n\n")
	fmt.Printf("  \033[38;5;240mresolution: recipe scripts/ → repo modules/ → agent sets (embedded)\033[0m\n\n")

	fmt.Printf("  \033[38;5;38mrepo library\033[0m \033[38;5;240m%s/modules/\033[0m\n", repoDir)
	entries, _ := os.ReadDir(filepath.Join(repoDir, "modules"))
	any := false
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), ".sh") && !strings.HasSuffix(e.Name(), ".py")) {
			continue
		}
		any = true
		desc := ""
		if data, err := os.ReadFile(filepath.Join(repoDir, "modules", e.Name())); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				desc = strings.TrimSpace(strings.TrimPrefix(line, "//"))
				break
			}
		}
		fmt.Printf("    \033[38;5;46m●\033[0m %-28s \033[38;5;240m%s\033[0m\n", e.Name(), desc)
	}
	if !any {
		fmt.Printf("    \033[38;5;240m(none)\033[0m\n")
	}
	fmt.Println()

	fmt.Printf("  \033[38;5;178magent sets\033[0m \033[38;5;240membedded in the agent binary\033[0m\n")
	curSet := ""
	for _, m := range agentmodules.List() {
		if m.Set != curSet {
			curSet = m.Set
			fmt.Printf("  \033[38;5;178m%s/\033[0m\n", m.Set)
		}
		fmt.Printf("    \033[38;5;46m●\033[0m %-28s \033[38;5;240m%s\033[0m\n", m.Name, m.Description)
	}
	fmt.Println()
	fmt.Printf("  \033[38;5;240mGo modules compile+cache on first use (needs go on the node); .sh/.py run anywhere.\033[0m\n\n")
}

// ── recipe run-all: pull + run every recipe applicable to this node ──────

// runAllCtx threads run-all state through recipeRun: compact output,
// abort-tolerance (a failed recipe doesn't kill the sweep), and the
// final summary counts.
type runAllCtx struct {
	verbose       bool
	recipeOK      int
	recipeFailed  int
	recipeSkipped int
	failedAt      string // first failure, for the final summary
}

// gitPullRepo pulls --ff-only, best-effort. Shared by recipe run --pull
// and recipe run-all.
func gitPullRepo(resolved string) {
	pc := exec.Command("git", "-C", resolved, "pull", "--ff-only")
	pout, perr := pc.CombinedOutput()
	pmsg := strings.TrimSpace(string(pout))
	if perr != nil {
		fmt.Printf("  \033[38;5;226m⚠ git pull failed — running local checkout: %s\033[0m\n", pmsg)
	} else if pmsg != "" && pmsg != "Already up to date." {
		fmt.Printf("  \033[38;5;46m▸ pulled: %s\033[0m\n", pmsg)
	}
}

// discoverRecipes lists every recipe file in recipes/: dir recipes
// (recipes/<name>/<name>.yaml) and flat files (recipes/*.yaml). Test
// modules (test_module: true) are excluded — those belong to test run-all.
func discoverRecipes(repoDir string) []string {
	var files []string
	root := filepath.Join(repoDir, "recipes")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.IsDir() {
			cand := filepath.Join(root, e.Name(), e.Name()+".yaml")
			if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
				files = append(files, cand)
			}
			continue
		}
		if strings.HasSuffix(e.Name(), ".yaml") || strings.HasSuffix(e.Name(), ".yml") {
			files = append(files, filepath.Join(root, e.Name()))
		}
	}
	sort.Strings(files)
	// exclude test modules
	var recipes []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "test_module: true") {
			continue
		}
		recipes = append(recipes, f)
	}
	return recipes
}

func recipeRunAll(args []string) {
	fs := flag.NewFlagSet("recipe run-all", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to gogitops repo")
	hostFlag := fs.String("hostname", "", "node hostname for label scoping (default: detected system hostname)")
	noPull := fs.Bool("no-pull", false, "skip the git pull (pull is run-all's default behavior)")
	dryRun := fs.Bool("dry-run", false, "print commands without executing")
	verbose := fs.Bool("verbose", false, "full step output (compact by default)")
	vAlias := fs.Bool("v", false, "alias for --verbose")

	var positional []string
	var flagArgs []string
	valueFlags := map[string]bool{"--repo": true, "-repo": true, "--hostname": true, "-hostname": true}
	for i := 0; i < len(args); i++ {
		if valueFlags[args[i]] && i+1 < len(args) {
			flagArgs = append(flagArgs, args[i], args[i+1])
			i++
		} else if strings.HasPrefix(args[i], "-") {
			flagArgs = append(flagArgs, args[i])
		} else {
			positional = append(positional, args[i])
		}
	}
	_ = fs.Parse(flagArgs)

	resolved := resolveRepoDir(*repoDir)
	cli.Banner()
	if !*noPull {
		gitPullRepo(resolved)
	}

	hostname := *hostFlag
	if hostname == "" {
		hostname = config.DetectHostname()
	}

	files := discoverRecipes(resolved)
	if len(files) == 0 {
		cli.PrintError("no recipes found in " + resolved + "/recipes/")
		os.Exit(1)
	}
	fmt.Printf("\n  \033[1m\033[38;5;141mRun-all\033[0m \033[38;5;240m— %d recipes, node %s\033[0m\n\n", len(files), hostname)

	ctx := &runAllCtx{verbose: *verbose || *vAlias}
	for _, f := range files {
		rargs := []string{f, "--repo", resolved, "--hostname", hostname}
		if *dryRun {
			rargs = append(rargs, "--dry-run")
		}
		if ctx.verbose {
			rargs = append(rargs, "--verbose")
		}
		recipeRun(rargs, ctx)
	}

	fmt.Printf("\n  \033[1m\033[38;5;141mrun-all complete\033[0m — %d ok, %d failed, %d not-applicable\n",
		ctx.recipeOK, ctx.recipeFailed, ctx.recipeSkipped)
	if ctx.failedAt != "" {
		fmt.Printf("  \033[38;5;196mfirst failure: %s\033[0m\n", ctx.failedAt)
		os.Exit(1)
	}
}

// ── test: run test modules as a real test suite ───────────────────────────
//
// gogitops test promotes the inspect-style test modules into a real test
// runner: pass/fail/skip counts, red/green output, non-zero exit on any
// failure (CI-able). Test files are YAML with `test_module: true` — the same
// recipe vocabulary (command/script/expect/assert/when/...), so tests are
// written by operators and agents, not Go devs. Reusable Go modules plug in
// via `script: <name>.go` from recipe-local scripts/ dirs.
//
// Discovery:
//   test_modules/*.yaml          — fleet-wide test modules (common, per-OS)
//   recipes/<r>/tests/*.yaml    — recipe-scoped test suites
// Only files with `test_module: true` run — normal recipes are NEVER executed
// by the test runner (they can install things).

type testResult struct {
	module string
	name   string
	status string // pass, fail, skip
}

func runTestModule(r recipe, modPath string, currentOS, currentArch, resolved string, verbose bool) ([]testResult, map[string]string, map[string]bool) {
	var results []testResult
	vars := map[string]string{
		"repo":     resolved,
		"hostname": config.DetectHostname(),
		"os":       currentOS,
		"arch":     currentArch,
	}
	// {{self}} = the binary running the tests — selftests exercise the CURRENT
	// engine, never a stale installed copy that might shadow it on PATH.
	if selfPath, err := os.Executable(); err == nil {
		vars["self"] = selfPath
	}
	// Hydrate the device attribute store so when_attr/only_if_attr and
	// {{attr.x}} see persistent local state (e.g. attr.tests.fail from the
	// last suite run). setAttrKeys tracks what THIS run actually set —
	// hydration is read-only, so merely-read attrs never leak back into
	// the store on persist.
	allAttrs := map[string]string{}
	hydrateDeviceAttrs(allAttrs, vars)
	setAttrKeys := map[string]bool{}

	for i, step := range r.Steps {
		displayName := step.Name
		if displayName == "" {
			displayName = fmt.Sprintf("step-%d", i+1)
		}

		// Filters → skip
		if step.OS != "" && step.OS != currentOS {
			results = append(results, testResult{r.Name, displayName, "skip"})
			continue
		}
		if step.Arch != "" && step.Arch != currentArch {
			results = append(results, testResult{r.Name, displayName, "skip"})
			continue
		}
		if step.WhenAttr != "" || step.OnlyIfAttr != "" {
			cond := substituteVars(step.WhenAttr+step.OnlyIfAttr, vars)
			if !evalAttrCondition(cond, allAttrs) {
				results = append(results, testResult{r.Name, displayName, "skip"})
				continue
			}
		}
		if step.When != "" {
			guard := substituteVars(step.When, vars)
			if out, err := exec.Command("bash", "-c", guard).CombinedOutput(); err != nil {
				_ = out
				results = append(results, testResult{r.Name, displayName, "skip"})
				continue
			}
		}

		// Execute — command: or script: (reusable Go/shell/python modules)
		var output string
		exitCode := 0
		if step.Script != "" {
			scriptPath := resolveScriptPath(step.Script, resolved, modPath)
			if scriptPath == "" {
				results = append(results, testResult{r.Name, displayName, "fail"})
				continue
			}
			lang := step.ScriptLang
			if lang == "" {
				lang = detectScriptLang(scriptPath)
			}
			var cmdArgs []string
			switch lang {
			case "go":
				cmdArgs = []string{compiledGoModule(scriptPath)}
			case "python3":
				cmdArgs = []string{"python3", scriptPath}
			case "bash":
				cmdArgs = []string{"bash", scriptPath}
			default:
				cmdArgs = []string{scriptPath}
			}
			if step.ScriptArgs != "" {
				cmdArgs = append(cmdArgs, step.ScriptArgs)
			}
			out, err := exec.Command(cmdArgs[0], cmdArgs[1:]...).CombinedOutput()
			output = strings.TrimSpace(string(out))
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else {
					exitCode = 1
				}
			}
		} else {
			cmd := substituteVars(step.Command, vars)
			out, err := exec.Command("bash", "-c", cmd).CombinedOutput()
			output = strings.TrimSpace(string(out))
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else {
					exitCode = 1
				}
			}
		}

		// Validation
		success := exitCode == 0
		if step.Expect != "" && !strings.Contains(output, step.Expect) {
			success = false
		}
		if step.ExpectRegex != "" {
			matched, _ := regexp.MatchString(step.ExpectRegex, output)
			if !matched {
				success = false
			}
		}
		if step.ExpectExit != nil && exitCode != *step.ExpectExit {
			success = false
		}

		// Captures + attributes (for cross-step flow)
		if step.Parse == "regex" && step.Pattern != "" {
			re, compileErr := regexp.Compile(step.Pattern)
			if compileErr == nil {
				matches := re.FindStringSubmatch(output)
				if len(matches) > 1 {
					for idx, m := range matches[1:] {
						vars[fmt.Sprintf("%d", idx+1)] = strings.TrimSpace(m)
					}
				}
			}
		}
		if step.SetAttr != "" {
			allAttrs["attr."+step.SetAttr] = output
			vars["attr."+step.SetAttr] = output
			setAttrKeys["attr."+step.SetAttr] = true
		}
		if step.AttrPrefix != "" {
			for k, v := range vars {
				if regexp.MustCompile(`^\d+$`).MatchString(k) {
					key := "attr." + step.AttrPrefix + "." + k
					allAttrs[key] = v
					vars[key] = v
					setAttrKeys[key] = true
				}
			}
		}

		label := step.Assert
		if label == "" {
			label = displayName
		}
		status := "pass"
		if !success {
			status = "fail"
		}
		results = append(results, testResult{r.Name, label, status})
		if verbose {
			fmt.Printf("    \033[38;5;240m%s\033[0m\n", output)
		}
	}
	return results, allAttrs, setAttrKeys
}

func discoverTestModules(resolved string) []string {
	var found []string
	tmDir := filepath.Join(resolved, "test_modules")
	if entries, err := os.ReadDir(tmDir); err == nil {
		for _, e := range entries {
			n := e.Name()
			if strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml") {
				found = append(found, filepath.Join(tmDir, n))
			}
		}
	}
	// recipe-scoped suites: recipes/<r>/tests/*.yaml
	rDir := filepath.Join(resolved, "recipes")
	recipes, err := os.ReadDir(rDir)
	if err == nil {
		for _, rc := range recipes {
			if !rc.IsDir() {
				continue
			}
			tDir := filepath.Join(rDir, rc.Name(), "tests")
			if entries, err := os.ReadDir(tDir); err == nil {
				for _, e := range entries {
					n := e.Name()
					if strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml") {
						found = append(found, filepath.Join(tDir, n))
					}
				}
			}
		}
	}
	return found
}

func cmdTest(args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintf(os.Stderr, "usage: gogitops test <subcommand>\n\nSubcommands:\n  list                     List available test modules\n  run <name-or-path>       Run one test module (by name or YAML path)\n  run-all                  Run every test_module: true YAML (test_modules/ + recipes/*/tests/)\n\nFlags: --repo path, --verbose\n")
		os.Exit(1)
	}
	sub := args[0]
	fs := flag.NewFlagSet("test "+sub, flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to gogitops repo")
	verbose := fs.Bool("verbose", false, "show full command output")
	// split flags from positional args
	var positional []string
	var flagArgs []string
	for i := 1; i < len(args); i++ {
		if (args[i] == "--repo" || args[i] == "-repo") && i+1 < len(args) {
			flagArgs = append(flagArgs, args[i], args[i+1])
			i++
		} else if strings.HasPrefix(args[i], "-") {
			flagArgs = append(flagArgs, args[i])
		} else {
			positional = append(positional, args[i])
		}
	}
	fs.Parse(flagArgs)

	resolved := resolveRepoDir(*repoDir)
	currentOS := runtime.GOOS
	currentArch := runtime.GOARCH

	switch sub {
	case "list":
		cli.Banner()
		fmt.Printf("\n  \033[1m\033[38;5;141mAvailable test modules\033[0m\n\n")
		any := false
		for _, p := range discoverTestModules(resolved) {
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			r := parseRecipe(string(data))
			if !r.TestModule {
				continue
			}
			any = true
			rel, _ := filepath.Rel(resolved, p)
			fmt.Printf("  \033[38;5;46m●\033[0m %-24s \033[38;5;240m%s\033[0m\n", r.Name, rel)
		}
		if !any {
			fmt.Printf("  \033[38;5;240m(none found)\033[0m\n")
		}
		fmt.Println()

	case "run":
		if len(positional) == 0 {
			fmt.Fprintf(os.Stderr, "usage: gogitops test run <name-or-path>\n")
			os.Exit(1)
		}
		target := positional[0]
		var path string
		if _, err := os.Stat(target); err == nil {
			path = target
		} else {
			path = filepath.Join(resolved, "test_modules", target+".yaml")
			if _, err := os.Stat(path); err != nil {
				fmt.Fprintf(os.Stderr, "\u274c test module not found: %s\n", target)
				os.Exit(1)
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\u274c cannot read %s: %v\n", path, err)
			os.Exit(1)
		}
		r := parseRecipe(string(data))
		if !r.TestModule {
			fmt.Fprintf(os.Stderr, "\u274c %s is not a test module (missing `test_module: true`) — refusing to run\n", path)
			os.Exit(1)
		}
		cli.Banner()
		results, attrs, setKeys := runTestModule(r, path, currentOS, currentArch, resolved, *verbose)
		persistTestAttrs(attrs, setKeys, results, "module:"+r.Name)
		os.Exit(printTestSummary(results))

	case "run-all":
		cli.Banner()
		var all []testResult
		mergedAttrs := map[string]string{}
		mergedSetKeys := map[string]bool{}
		for _, p := range discoverTestModules(resolved) {
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			r := parseRecipe(string(data))
			if !r.TestModule {
				continue
			}
			fmt.Printf("\n  \033[1m\033[38;5;141mModule: %s\033[0m \033[38;5;240m(%s)\033[0m\n", r.Name, p)
			results, attrs, setKeys := runTestModule(r, p, currentOS, currentArch, resolved, *verbose)
			all = append(all, results...)
			for k, v := range attrs {
				mergedAttrs[k] = v
				mergedSetKeys[k] = mergedSetKeys[k] || setKeys[k]
			}
		}
		vr, va, vk := verifyRepoRecipes(resolved, *verbose)
		all = append(all, vr...)
		for k, v := range va {
			mergedAttrs[k] = v
			mergedSetKeys[k] = mergedSetKeys[k] || vk[k]
		}
		persistTestAttrs(mergedAttrs, mergedSetKeys, all, "suite")
		os.Exit(printTestSummary(all))

	default:
		fmt.Fprintf(os.Stderr, "unknown test subcommand: %s\n", sub)
		os.Exit(1)
	}
}

func printTestSummary(results []testResult) int {
	pass, fail, skip := 0, 0, 0
	for _, r := range results {
		switch r.status {
		case "pass":
			pass++
			fmt.Printf("  \033[38;5;46m\u2713\033[0m %s\n", r.name)
		case "fail":
			fail++
			fmt.Printf("  \033[38;5;196m\u2716\033[0m %s\n", r.name)
		default:
			skip++
			fmt.Printf("  \033[38;5;240m\u2b1c\033[0m %s\n", r.name)
		}
	}
	fmt.Printf("\n  \033[1m%d passed\033[0m, \033[38;5;196m%d failed\033[0m, \033[38;5;240m%d skipped\033[0m\n\n", pass, fail, skip)
	if fail > 0 {
		return 1
	}
	return 0
}

// ── inspect: run test modules and collect attributes ─────────────────────

func cmdInspect(args []string) {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to gogitops repo")
	verbose := fs.Bool("verbose", false, "show full command output from test steps")
	fs.Parse(args)

	resolved := resolveRepoDir(*repoDir)
	testModulesDir := resolved + "/test_modules"

	// Determine current OS for module selection
	currentOS := runtime.GOOS
	currentArch := runtime.GOARCH

	// Load and run applicable test modules in priority order:
	// common.yaml first, then OS-specific, then docker (if docker present)
	moduleOrder := []string{
		"common.yaml",
		currentOS + ".yaml",
		"docker.yaml",
	}

	// Collect all attributes across modules
	allAttrs := map[string]string{}
	assertResults := []struct {
		module string
		name   string
		status string
	}{}

	cli.Banner()
	fmt.Printf("\n  \033[1m\033[38;5;141mNode Inspector\033[0m\n")
	fmt.Printf("  \033[38;5;240mOS: %s/%s  Repo: %s\033[0m\n\n", currentOS, currentArch, resolved)

	modulesRun := 0
	for _, modFile := range moduleOrder {
		modPath := testModulesDir + "/" + modFile
		data, err := os.ReadFile(modPath)
		if err != nil {
			continue // skip missing modules
		}

		r := parseRecipe(string(data))
		if r.Name == "" || len(r.Steps) == 0 {
			continue
		}

		modulesRun++
		moduleName := strings.TrimSuffix(modFile, ".yaml")
		fmt.Printf("  \033[38;5;141m▸ Module: %s\033[0m\n", moduleName)
		if r.Description != "" {
			fmt.Printf("  \033[38;5;240m  %s\033[0m\n", r.Description)
		}

		// Build vars for this module
		vars := map[string]string{
			"repo":     resolved,
			"hostname": config.DetectHostname(),
			"os":       currentOS,
			"arch":     currentArch,
		}

		for i, step := range r.Steps {
			stepNum := i + 1
			displayName := step.Name
			if displayName == "" {
				displayName = fmt.Sprintf("step-%d", stepNum)
			}

			// OS filter
			if step.OS != "" && step.OS != currentOS {
				continue
			}
			// Arch filter
			if step.Arch != "" && step.Arch != currentArch {
				continue
			}

			// when_attr condition
			if step.WhenAttr != "" {
				cond := substituteVars(step.WhenAttr, vars)
				if !evalAttrCondition(cond, allAttrs) {
					continue
				}
			}

			// only_if_attr condition
			if step.OnlyIfAttr != "" {
				cond := substituteVars(step.OnlyIfAttr, vars)
				if !evalAttrCondition(cond, allAttrs) {
					continue
				}
			}

			cmd := substituteVars(step.Command, vars)

			// Run command
			eCmd := exec.Command("bash", "-c", cmd)
			out, err := eCmd.CombinedOutput()
			output := strings.TrimSpace(string(out))
			exitCode := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else {
					exitCode = 1
				}
			}

			// Determine success
			success := exitCode == 0
			if step.Expect != "" && !strings.Contains(output, step.Expect) {
				success = false
			}
			if step.ExpectRegex != "" {
				matched, _ := regexp.MatchString(step.ExpectRegex, output)
				if !matched {
					success = false
				}
			}
			if step.ExpectExit != nil && exitCode != *step.ExpectExit {
				success = false
			}

			// Parse output for variables
			if step.Parse == "regex" && step.Pattern != "" {
				re, compileErr := regexp.Compile(step.Pattern)
				if compileErr == nil {
					matches := re.FindStringSubmatch(output)
					if len(matches) > 1 {
						for idx, m := range matches[1:] {
							vars[fmt.Sprintf("%d", idx+1)] = strings.TrimSpace(m)
						}
					}
				}
			}

			// Assert: record pass/fail
			if step.Assert != "" {
				attrKey := "attr.assert." + displayName
				status := "pass"
				if !success {
					status = "fail"
				}
				allAttrs[attrKey+".status"] = status
				assertResults = append(assertResults, struct {
					module string
					name   string
					status string
				}{moduleName, step.Assert, status})

				icon := "\033[38;5;46m✓\033[0m"
				if !success {
					icon = "\033[38;5;196m✖\033[0m"
				}
				fmt.Printf("    %s %s\033[0m\n", icon, step.Assert)
			}

			// set_attr
			if step.SetAttr != "" {
				allAttrs["attr."+step.SetAttr] = output
				vars["attr."+step.SetAttr] = output
			}

			// attr_prefix
			if step.AttrPrefix != "" && step.Parse == "regex" && step.Pattern != "" {
				re, compileErr := regexp.Compile(step.Pattern)
				if compileErr == nil {
					matches := re.FindStringSubmatch(output)
					if len(matches) > 1 {
						for idx, m := range matches[1:] {
							key := fmt.Sprintf("attr.%s.%d", step.AttrPrefix, idx+1)
							val := strings.TrimSpace(m)
							allAttrs[key] = val
							vars[key] = val
						}
					}
				}
			}

			// Verbose output
			if *verbose && output != "" {
				for _, line := range strings.Split(output, "\n") {
					fmt.Printf("      \033[38;5;240m%s\033[0m\n", line)
				}
			}
		}
		fmt.Println()
	}

	// Print attribute summary
	fmt.Printf("  \033[38;5;240m╭─ Attributes (%d) ──────────────────\033[0m\n", len(allAttrs))
	// Sort attribute keys
	attrKeys := make([]string, 0, len(allAttrs))
	for k := range allAttrs {
		attrKeys = append(attrKeys, k)
	}
	sort.Strings(attrKeys)

	passCount := 0
	failCount := 0
	for _, k := range attrKeys {
		v := allAttrs[k]
		if strings.HasPrefix(k, "assert.") {
			if v == "pass" {
				passCount++
			} else {
				failCount++
			}
			// Skip printing assert status in attributes list — shown in summary
			continue
		}
		fmt.Printf("  \033[38;5;240m│\033[0m \033[38;5;178m%s\033[0m = \033[38;5;255m%s\033[0m\n", k, truncateStr(v, 60))
	}
	fmt.Printf("  \033[38;5;240m╰──────────────────────────────────\033[0m\n\n")

	// Assert summary
	if len(assertResults) > 0 {
		fmt.Printf("  \033[38;5;240m╭─ Assertions (%d) ─────────────────\033[0m\n", len(assertResults))
		for _, a := range assertResults {
			icon := "\033[38;5;46m✓\033[0m"
			statusCol := "\033[38;5;46m"
			if a.status == "fail" {
				icon = "\033[38;5;196m✖\033[0m"
				statusCol = "\033[38;5;196m"
			}
			fmt.Printf("  \033[38;5;240m│\033[0m %s %s%s\033[0m  \033[38;5;240m[%s]\033[0m\n", icon, statusCol, a.name, a.module)
		}
		fmt.Printf("  \033[38;5;240m╰── \033[38;5;46m%d pass\033[0m, \033[38;5;196m%d fail\033[0m \033[38;5;240m────────────\033[0m\n", passCount, failCount)
		fmt.Println()
	}

	if modulesRun == 0 {
		fmt.Printf("  \033[38;5;226m⚠ No test modules found in %s/\033[0m\n\n", testModulesDir)
	}
}

// ── Daemon mode ──────────────────────────────────────────────────────────

// ── agent fleet tests: every node runs the YAML test suite locally ──────
//
// The daemon runs test_module: true YAML on its own ticker and reports
// three ways, all local-first:
//   1. /v1/tests      — JSON of the latest results (pass/fail/skip per step)
//   2. starship cache — tests_failures merged into the prompt data
//   3. Discord alerts — on NEW failures (and recoveries) only, matching
//                       the alertOnChange pattern: silent when healthy.

// ── device attribute store: local, persistent, recipe-parseable ──────────
//
// Test/recipe runs write their attrs here (~/.cache/gogitops/attrs.json).
// Suite summaries land as tests.* keys; module attrs (docker_version,
// uptime.*, ...) persist for local parsing; the next run hydrates them back
// so when_attr conditions work across runs. Served via /v1/attrs.

// hydrateDeviceAttrs loads the persistent device attr store into an in-run
// attrs map + vars map. Keys gain the canonical "attr." prefix that
// when_attr/only_if_attr/{{attr.x}} use; the store itself keeps plain keys
// (human/jq friendly: "tests.pass", "docker_version").
func hydrateDeviceAttrs(attrs, vars map[string]string) {
	for k, v := range readDeviceAttrs() {
		ak := k
		if !strings.HasPrefix(k, "attr.") {
			ak = "attr." + k
		}
		attrs[ak] = v
		if vars != nil {
			vars[ak] = v
		}
	}
}

func attrStorePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "gogitops", "attrs.json")
}

func readDeviceAttrs() map[string]string {
	m := map[string]string{}
	data, err := os.ReadFile(attrStorePath())
	if err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

func writeDeviceAttrs(m map[string]string) {
	p := attrStorePath()
	_ = os.MkdirAll(filepath.Dir(p), 0755)
	f, err := os.Create(p)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	_ = enc.Encode(m)
}

// persistTestAttrs merges module attrs + suite summary into the device store.
// In-run attr maps use the "attr." prefix; the store keeps plain keys.
// scope records what the tests.* numbers describe: "suite" (full run-all /
// agent cycle) or "module:<name>" (single-module run — tests.* then describe
// THAT module only, not the whole suite).
func persistTestAttrs(attrs map[string]string, setKeys map[string]bool, all []testResult, scope string) {
	pass, fail, skip := 0, 0, 0
	var failing []string
	for _, r := range all {
		switch r.status {
		case "pass":
			pass++
		case "fail":
			fail++
			failing = append(failing, r.module+"/"+r.name)
		default:
			skip++
		}
	}
	store := map[string]string{}
	for k, v := range attrs {
		if !setKeys[k] {
			continue // hydrated (merely read) attrs never persist
		}
		store[strings.TrimPrefix(k, "attr.")] = v
	}
	store["tests.pass"] = strconv.Itoa(pass)
	store["tests.fail"] = strconv.Itoa(fail)
	store["tests.skip"] = strconv.Itoa(skip)
	store["tests.total"] = strconv.Itoa(pass + fail + skip)
	store["tests.failing"] = strings.Join(failing, ",")
	store["tests.scope"] = scope
	store["tests.last_run"] = time.Now().Format(time.RFC3339)
	writeDeviceAttrs(store)
}

func deviceAttrsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(readDeviceAttrs())
}

var fleetTestState struct {
	mu      sync.Mutex
	when    time.Time
	pass    int
	fail    int
	skip    int
	results []testResult
}

func fleetTestsHandler(w http.ResponseWriter, r *http.Request) {
	fleetTestState.mu.Lock()
	defer fleetTestState.mu.Unlock()
	resp := map[string]any{
		"when":    fleetTestState.when,
		"pass":    fleetTestState.pass,
		"fail":    fleetTestState.fail,
		"skip":    fleetTestState.skip,
		"results": fleetTestState.results,
		"attrs":   readDeviceAttrs(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// selectFleetModules applies inspect's file-level selection: common.yaml,
// the current OS module, docker.yaml, and every other module (which must
// self-guard via step os/arch/when filters or module labels).
func selectFleetModules(paths []string, goos string, nodeLabels []string) []string {
	var selected []string
	for _, p := range paths {
		base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		if base == goos || base == "common" || base == "docker" {
			selected = append(selected, p)
			continue
		}
		// other modules: label gate (module labels must be a subset of node's)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		r := parseRecipe(string(data))
		if !r.TestModule {
			continue
		}
		ok := true
		nodeSet := map[string]bool{}
		for _, l := range nodeLabels {
			nodeSet[l] = true
		}
		for _, ml := range r.Labels {
			if !nodeSet[ml] {
				ok = false
				break
			}
		}
		if ok {
			selected = append(selected, p)
		}
	}
	return selected
}

func runFleetTestLoop(interval time.Duration, repoDir, hostname, webhook string, nodeLabels []string) {
	// let the daemon settle before the first suite
	time.Sleep(10 * time.Second)
	runFleetTests(repoDir, hostname, webhook, nodeLabels)
	ticker := time.NewTicker(interval)
	for range ticker.C {
		runFleetTests(repoDir, hostname, webhook, nodeLabels)
	}
}

func runFleetTests(repoDir, hostname, webhook string, nodeLabels []string) {
	resolved := resolveRepoDir(repoDir)
	var all []testResult
	mergedAttrs := map[string]string{}
	mergedSetKeys := map[string]bool{}
	for _, p := range selectFleetModules(discoverTestModules(resolved), runtime.GOOS, nodeLabels) {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		r := parseRecipe(string(data))
		if !r.TestModule {
			continue
		}
		results, attrs, setKeys := runTestModule(r, p, runtime.GOOS, runtime.GOARCH, resolved, false)
		all = append(all, results...)
		for k, v := range attrs {
			mergedAttrs[k] = v
			mergedSetKeys[k] = mergedSetKeys[k] || setKeys[k]
		}
	}

	// Recipe verification: the agent validates every recipe in its checkout
	// (decentralized — verdicts computed on-node, stored locally, never pushed)
	vr, va, vk := verifyRepoRecipes(resolved, false)
	all = append(all, vr...)
	for k, v := range va {
		mergedAttrs[k] = v
		mergedSetKeys[k] = mergedSetKeys[k] || vk[k]
	}

	persistTestAttrs(mergedAttrs, mergedSetKeys, all, "suite")
	pass, fail, skip := 0, 0, 0
	nowFailing := map[string]bool{}
	for _, r := range all {
		switch r.status {
		case "pass":
			pass++
		case "fail":
			fail++
			nowFailing[r.module+"/"+r.name] = true
		default:
			skip++
		}
	}
	fleetTestState.mu.Lock()
	prevFailing := map[string]bool{}
	for _, r := range fleetTestState.results {
		if r.status == "fail" {
			prevFailing[r.module+"/"+r.name] = true
		}
	}
	fleetTestState.when = time.Now()
	fleetTestState.pass, fleetTestState.fail, fleetTestState.skip = pass, fail, skip
	fleetTestState.results = all
	fleetTestState.mu.Unlock()

	starship.SetTests(fail, pass+fail+skip)

	// alert on NEW failures and recoveries only — silent when steady
	if webhook != "" && (fail > 0 || len(prevFailing) > 0) {
		sender := alert.NewSender(webhook)
		var newlyFailing, recovered []string
		for k := range nowFailing {
			if !prevFailing[k] {
				newlyFailing = append(newlyFailing, k)
			}
		}
		for k := range prevFailing {
			if !nowFailing[k] {
				recovered = append(recovered, k)
			}
		}
		sort.Strings(newlyFailing)
		sort.Strings(recovered)
		if len(newlyFailing) > 0 {
			_ = sender.SendAlert("warn", hostname, fmt.Sprintf("fleet tests: %d newly failing — %s", len(newlyFailing), strings.Join(newlyFailing, ", ")))
		}
		if len(recovered) > 0 {
			_ = sender.SendAlert("ok", hostname, fmt.Sprintf("fleet tests: %d recovered — %s", len(recovered), strings.Join(recovered, ", ")))
		}
	}
}

func runDaemon(args []string) {
	var (
		repoDir   = flag.String("repo", "", "path to config repo (default: auto-detect — ~/.config/gogitops, /etc/gogitops, else cwd)")
		hostFlag  = flag.String("hostname", "", "override hostname (defaults to system hostname; match a nodes/*.yaml name)")
		bindAddr  = flag.String("bind", "", "bind address for health API (default: node's LAN IP, then nebula IP, then 0.0.0.0)")
		port      = flag.Int("port", 7780, "health API port")
		intervalS = flag.Int("interval", 60, "check interval in seconds")
		webhook   = flag.String("webhook", "", "Discord webhook URL or nenv:<ns>/<key> (empty = no alerts)")
		dashFlag  = flag.String("dashboard", "", "dashboard URL to register with (e.g. http://10.2.0.102:7781)")
	)
	flag.CommandLine.Parse(args)
	repoPath := resolveRepoDir(*repoDir)

	hostname := *hostFlag
	if hostname == "" {
		hostname = os.Getenv("GOGITOPS_HOSTNAME")
	}
	if hostname == "" {
		hostname = config.DetectHostname()
	}

	node, err := config.LoadNode(repoPath, hostname)
	if err != nil {
		log.Fatalf("failed to load node config for %q: %v\n(hint: create nodes/%s.yaml in the repo)", hostname, err, hostname)
	}

	meshCfg, err := config.LoadMesh(repoPath)
	if err != nil {
		log.Printf("warning: failed to load mesh config: %v — running without peer pings", err)
		meshCfg = &config.MeshConfig{}
	}

	// Machine identity sync: refresh our own mesh entry with live-detected
	// truth (IPs, NICs, machine-id). Portable MACs declared in the node yaml
	// (portable_macs:) override sysfs USB detection — they move OUT of the
	// identity set into portable inventory. Byte-stable: only writes on
	// real change (this repo git-pulls itself).
	syncNIC := config.DetectNICs()
	syncNIC.Portable = append(syncNIC.Portable, node.PortableMacs...)
	syncMid := config.DetectMachineID()
	if syncMid == "" {
		syncMid = node.MachineID
	}
	config.SyncSelfToMesh(repoPath, hostname, config.DetectNebulaIP(), config.DetectLanIP(), syncNIC, syncMid)

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
	http.HandleFunc("/v1/tests", fleetTestsHandler)
	http.HandleFunc("/v1/attrs", deviceAttrsHandler)
	scanRepo := repoPath
	http.HandleFunc("/v1/attrs/catalog", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(scanRepoAttrs(scanRepo))
	})
	a.SetRepoDir(repoPath)
	go func() {
		log.Printf("health API listening on %s", addr)
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Fatalf("health API failed: %v", err)
		}
	}()

	if *dashFlag != "" {
		go registerWithDashboard(*dashFlag, hostname, bind, *port, node)
	}

	// Fleet self-tests: every agent runs the YAML test suite locally.
	// tests_interval node config (default 30m, "off"/"0" disables).
	testsInterval := 30 * time.Minute
	if ti := node.Agent.TestsInterval; ti != "" {
		if ti == "off" || ti == "0" {
			testsInterval = 0
		} else if d, err := time.ParseDuration(ti); err == nil {
			testsInterval = d
		}
	}
	if testsInterval > 0 {
		log.Printf("fleet tests enabled: running suite every %s (results on /v1/tests)", testsInterval)
		go runFleetTestLoop(testsInterval, repoPath, hostname, wbhook, node.Labels)
	} else {
		log.Printf("fleet tests disabled (tests_interval=%q)", node.Agent.TestsInterval)
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
			if resp.StatusCode == 200 {
				log.Printf("registered with dashboard at %s", dashURL)
				resp.Body.Close()
				// Re-register periodically so mesh/IP changes (wake from
				// sleep, DHCP changes, stale repo data) self-heal instead of
				// persisting for the daemon's lifetime.
				time.Sleep(10 * time.Minute)
				continue
			}
			log.Printf("dashboard registration failed: status %d (will retry)", resp.StatusCode)
			resp.Body.Close()
		} else {
			log.Printf("dashboard registration failed (will retry): %v", err)
		}
		time.Sleep(30 * time.Second)
	}
}

// ── deploy: generate agent install snippets for a node ───────────────────

func cmdDeploy(args []string) {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	host := fs.String("hostname", "<hostname>", "node name (match a nodes/*.yaml)")
	targetOS := fs.String("os", "linux", "target OS: linux | darwin")
	targetArch := fs.String("arch", "amd64", "target arch: amd64 | arm64")
	bind := fs.String("bind", "0.0.0.0", "health API bind address")
	port := fs.String("port", "7780", "health API port")
	dash := fs.String("dashboard", "", "beacon URL (default: $GOGITOPS_DASHBOARD_URL or http://10.2.0.102:7781)")
	repo := fs.String("repo", "", "git config repo URL baked into the installer config (e.g. git@github.com:b3nnb/gogitops.git)")
	format := fs.String("format", "all", "cmd | install | config | systemd | launchd | all")
	fs.Parse(args)

	dashURL := *dash
	if dashURL == "" {
		dashURL = os.Getenv("GOGITOPS_DASHBOARD_URL")
	}
	if dashURL == "" {
		dashURL = "http://10.2.0.102:7781"
	}

	if *targetOS != "linux" && *targetOS != "darwin" {
		cli.PrintError(fmt.Sprintf("unsupported target OS %q (linux and darwin only)", *targetOS))
		os.Exit(1)
	}

	cli.Banner()
	if *repo != "" {
		fmt.Printf("\n  %sDeploy snippets for %s%s — %s/%s, beacon %s, repo %s\n\n",
			"\033[1m\033[38;5;141m", *host, "\033[0m", *targetOS, *targetArch, dashURL, *repo)
	} else {
		fmt.Printf("\n  %sDeploy snippets for %s%s — %s/%s, beacon %s\n\n",
			"\033[1m\033[38;5;141m", *host, "\033[0m", *targetOS, *targetArch, dashURL)
	}

	// Installer config lines — baked into /etc/gogitops/agent.env (linux)
	// or the LaunchAgent plist (darwin).
	var envLines []string
	envLines = append(envLines, fmt.Sprintf("GOGITOPS_HOSTNAME=%s", *host))
	if dashURL != "" {
		envLines = append(envLines, fmt.Sprintf("GOGITOPS_DASHBOARD_URL=%s", dashURL))
	}
	if *repo != "" {
		envLines = append(envLines, fmt.Sprintf("GOGITOPS_REPO_URL=%s", *repo))
	}

	daemonCmd := fmt.Sprintf("gogitops daemon \\\n  -hostname %s \\\n  -bind %s \\\n  -port %s \\\n  -interval 60 \\\n  -dashboard %s", *host, *bind, *port, dashURL)

	if *format == "all" || *format == "cmd" {
		fmt.Printf("  %s╭─ Daemon Command %s\n", "\033[38;5;240m", "\033[0m")
		for _, l := range strings.Split(daemonCmd, "\n") {
			fmt.Printf("  %s│%s %s%s%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;255m", l, "\033[0m")
		}
		fmt.Printf("  %s╰──────────────────%s\n\n", "\033[38;5;240m", "\033[0m")
	}

	if *format == "all" || *format == "config" {
		fmt.Printf("  %s╭─ Installer Config %s /etc/gogitops/agent.env\n", "\033[38;5;240m", "\033[0m")
		for _, l := range envLines {
			fmt.Printf("  %s│%s %s%s%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;255m", l, "\033[0m")
		}
		if *targetOS == "darwin" {
			fmt.Printf("  %s│%s %s(darwin: set as EnvironmentVariables in the LaunchAgent plist)%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;245m", "\033[0m")
		}
		fmt.Printf("  %s╰──────────────────%s\n\n", "\033[38;5;240m", "\033[0m")
	}

	if *format == "all" || *format == "install" {
		install := fmt.Sprintf("curl -sL %s/api/binary/%s/%s -o /usr/local/bin/gogitops && \\\nchmod +x /usr/local/bin/gogitops && \\\n%s", dashURL, *targetOS, *targetArch, daemonCmd)
		if *targetOS == "darwin" {
			install = fmt.Sprintf("# macOS: download binary, ad-hoc sign, then run\ncurl -sL %s/api/binary/%s/%s -o /usr/local/bin/gogitops && \\\nchmod +x /usr/local/bin/gogitops && \\\ncodesign --force --sign - /usr/local/bin/gogitops && \\\n%s", dashURL, *targetOS, *targetArch, daemonCmd)
		}
		// Bake installer config (linux): write agent.env first
		if *targetOS == "linux" {
			cfgWrite := "sudo mkdir -p /etc/gogitops"
			for _, l := range envLines {
				cfgWrite += fmt.Sprintf(" && \\\necho '%s' | sudo tee -a /etc/gogitops/agent.env > /dev/null", l)
			}
			install = cfgWrite + " && \\\n" + install
		}
		// Bake config repo: clone before the daemon starts
		if *repo != "" {
			install = strings.Replace(install, "gogitops daemon", fmt.Sprintf("git clone %s ~/.config/gogitops && \\\ngogitops daemon", *repo), 1)
		}
		fmt.Printf("  %s╭─ One-Liner Install %s\n", "\033[38;5;240m", "\033[0m")
		for _, l := range strings.Split(install, "\n") {
			fmt.Printf("  %s│%s %s%s%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;255m", l, "\033[0m")
		}
		fmt.Printf("  %s╰──────────────────%s\n\n", "\033[38;5;240m", "\033[0m")
	}

	if (*format == "all" || *format == "systemd") && *targetOS == "linux" {
		systemd := fmt.Sprintf("[Unit]\nDescription=GoGitOps Agent\nAfter=network.target\n\n[Service]\nType=simple\nExecStart=/usr/local/bin/gogitops daemon \\\n  -hostname %s \\\n  -bind %s \\\n  -port %s \\\n  -interval 60 \\\n  -dashboard %s\nRestart=always\nRestartSec=5\n\n[Install]\nWantedBy=multi-user.target", *host, *bind, *port, dashURL)
		fmt.Printf("  %s╭─ Systemd Unit %s  (system service)\n", "\033[38;5;240m", "\033[0m")
		for _, l := range strings.Split(systemd, "\n") {
			fmt.Printf("  %s│%s %s%s%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;255m", l, "\033[0m")
		}
		fmt.Printf("  %s╰──────────────────%s\n\n", "\033[38;5;240m", "\033[0m")
	}

	if (*format == "all" || *format == "launchd") && *targetOS == "darwin" {
		envXML := ""
		if dashURL != "" {
			envXML += fmt.Sprintf("    <key>GOGITOPS_DASHBOARD_URL</key>\n    <string>%s</string>\n", dashURL)
		}
		if *repo != "" {
			envXML += fmt.Sprintf("    <key>GOGITOPS_REPO_URL</key>\n    <string>%s</string>\n", *repo)
		}
		envDict := ""
		if envXML != "" {
			envDict = fmt.Sprintf("    <key>EnvironmentVariables</key>\n    <dict>\n%s    </dict>\n", envXML)
		}
		launchd := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.benn.gogitops</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/gogitops</string>
        <string>daemon</string>
        <string>-hostname</string>
        <string>%s</string>
        <string>-bind</string>
        <string>%s</string>
        <string>-port</string>
        <string>%s</string>
        <string>-interval</string>
        <string>60</string>
        <string>-dashboard</string>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/gogitops.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/gogitops.err</string>
%s</dict>
</plist>`, *host, *bind, *port, dashURL, envDict)
		fmt.Printf("  %s╭─ LaunchAgent (macOS) %s  ~/Library/LaunchAgents/com.benn.gogitops.plist\n", "\033[38;5;240m", "\033[0m")
		for _, l := range strings.Split(launchd, "\n") {
			fmt.Printf("  %s│%s %s%s%s\n", "\033[38;5;240m", "\033[0m", "\033[38;5;255m", l, "\033[0m")
		}
		fmt.Printf("  %s╰──────────────────%s\n\n", "\033[38;5;240m", "\033[0m")
	}

	fmt.Printf("  %sDeploy an agent on the target machine — it self-registers with the beacon.%s\n\n", "\033[38;5;245m", "\033[0m")
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
			log.Printf("no static nodes in mesh.d/ — waiting for nodes to self-register")
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
