// gogitops — decentralized agent mesh for Benn's homelab.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bennbanks/gogitops/internal/agent"
	"github.com/bennbanks/gogitops/internal/config"
)

var (
	version = "dev"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "status":
			cmdStatus(os.Args[2:])
		case "version":
			fmt.Printf("gogitops %s\n", version)
		default:
			fmt.Fprintf(os.Stderr, "unknown command: %s\nusage: gogitops [status|version] or gogitops [daemon flags]\n", os.Args[1])
			os.Exit(1)
		}
		return
	}

	runDaemon()
}

// ── CLI subcommands ──────────────────────────────────────────────────────

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7780", "agent health API address")
	fs.Parse(args)

	resp, err := http.Get("http://" + *addr + "/v1/health")
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ agent not reachable at %s\n", *addr)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var h peerHealth
	if err := json.Unmarshal(body, &h); err != nil {
		fmt.Fprintf(os.Stderr, "❌ bad response from agent\n")
		os.Exit(1)
	}

	fmt.Printf("gogitops %s — %s  uptime %ds\n", h.AgentVersion, h.Hostname, h.UptimeSeconds)
	fmt.Println()

	// Services
	up, down := 0, 0
	for _, v := range h.Services {
		if v == "running" {
			up++
		} else {
			down++
		}
	}
	fmt.Printf("Services: %d/%d running\n", up, up+down)
	for k, v := range h.Services {
		if v == "running" {
			fmt.Printf("  ✅ %s\n", k)
		} else {
			fmt.Printf("  ❌ %s (%s)\n", k, v)
		}
	}

	// Nebula
	fmt.Println()
	if h.NebulaRunning {
		fmt.Println("Nebula: ✅ running")
	} else {
		fmt.Println("Nebula: ❌ down")
	}

	// Peers
	fmt.Println()
	if len(h.PeersReachable) > 0 || len(h.PeersUnreach) > 0 {
		fmt.Printf("Peers: %d up, %d down\n", len(h.PeersReachable), len(h.PeersUnreach))
		for _, p := range h.PeersReachable {
			fmt.Printf("  ✅ %s\n", p)
		}
		for _, p := range h.PeersUnreach {
			fmt.Printf("  ❌ %s\n", p)
		}
	}

	// Disk warnings
	if len(h.DiskWarns) > 0 {
		fmt.Println()
		fmt.Println("Disk warnings:")
		for _, w := range h.DiskWarns {
			fmt.Printf("  ⚠️  %s\n", w)
		}
	}
}

// minimal struct to decode health response
type peerHealth struct {
	Hostname       string            `json:"hostname"`
	AgentVersion   string            `json:"agent_version"`
	UptimeSeconds  int64             `json:"uptime_seconds"`
	NebulaRunning  bool              `json:"nebula_running"`
	Labels         []string          `json:"labels"`
	Services       map[string]string `json:"services"`
	DiskWarns      []string          `json:"disk_warns"`
	PeersReachable []string          `json:"peers_reachable"`
	PeersUnreach   []string          `json:"peers_unreachable"`
}

// ── Daemon mode ──────────────────────────────────────────────────────────

func runDaemon() {
	var (
		repoDir   = flag.String("repo", "/home/benn/Documents/code/GoGitOps", "path to config repo")
		hostFlag  = flag.String("hostname", "", "override hostname (defaults to system hostname; match a nodes/*.yaml name)")
		bindAddr  = flag.String("bind", "", "bind address for health API (default: node's nebula IP)")
		port      = flag.Int("port", 7780, "health API port")
		intervalS = flag.Int("interval", 60, "check interval in seconds")
		webhook   = flag.String("webhook", "", "Discord webhook URL or nenv:<ns>/<key> (empty = no alerts)")
	)
	flag.Parse()

	hostname := *hostFlag
	if hostname == "" {
		hostname = os.Getenv("GOGITOPS_HOSTNAME")
	}
	if hostname == "" {
		hostname = config.DetectHostname()
	}

	// Load node config
	node, err := config.LoadNode(*repoDir, hostname)
	if err != nil {
		log.Fatalf("failed to load node config for %q: %v\n(hint: create nodes/%s.yaml in the repo)", hostname, err, hostname)
	}

	// Load mesh config
	meshCfg, err := config.LoadMesh(*repoDir)
	if err != nil {
		log.Printf("warning: failed to load mesh config: %v — running without peer pings", err)
		meshCfg = &config.MeshConfig{}
	}

	// Resolve nenv: webhook references
	wbhook := resolveWebhook(*webhook)

	// Determine bind address — default to Nebula IP so agent is invisible to LAN
	bind := *bindAddr
	if bind == "" {
		bind = node.NebulaIP
	}
	if bind == "" || bind == "null" {
		bind = "127.0.0.1"
		log.Printf("warning: no nebula_ip in node config — binding to localhost only")
	}

	// Create agent
	a := agent.New(node, meshCfg, wbhook)
	agent.Version = version

	// Start health API
	addr := net.JoinHostPort(bind, fmt.Sprintf("%d", *port))
	http.HandleFunc("/v1/health", a.HealthHandler)
	go func() {
		log.Printf("health API listening on %s", addr)
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Fatalf("health API failed: %v", err)
		}
	}()

	// Run main loop
	interval := time.Duration(*intervalS) * time.Second
	a.Run(interval)
	os.Exit(0)
}

// resolveWebhook resolves nenv: prefixes at daemon startup
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
