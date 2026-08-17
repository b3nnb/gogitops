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
		case "version":
			fmt.Printf("gogitops %s\n", version)
		case "daemon", "run":
			runDaemon(os.Args[2:])
		case "dashboard":
			runDashboard(os.Args[2:])
		default:
			if strings.HasPrefix(os.Args[1], "-") {
				// legacy: bare flags means daemon mode
				runDaemon(os.Args[1:])
			} else {
				fmt.Fprintf(os.Stderr, "unknown command: %s\nusage: gogitops [status|version|daemon]\n", os.Args[1])
				os.Exit(1)
			}
		}
		return
	}
	fmt.Fprintf(os.Stderr, "usage: gogitops <command>\n\nCommands:\n  status     Show local agent health\n  daemon     Run the agent\n  dashboard  Fleet status dashboard\n  version    Print version\n")
	os.Exit(1)
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

	// Self-register with the dashboard if configured
	if *dashFlag != "" {
		go registerWithDashboard(*dashFlag, hostname, bind, *port, node)
	}

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

// registerWithDashboard sends a POST /api/register to the dashboard.
// Retries periodically so the dashboard doesn't need to be up first.
func registerWithDashboard(dashURL, hostname, bindAddr string, port int, node *config.NodeConfig) {
	// Build the address the dashboard can reach us at
	address := bindAddr
	if address == "0.0.0.0" {
		// Use the node's best IP
		address = node.Address()
		if address == "" {
			address = "127.0.0.1"
		}
	}
	address = fmt.Sprintf("%s:%d", address, port)

	// Build display IP
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

// ── Dashboard ───────────────────────────────────────────────────────────

func runDashboard(args []string) {
	fs := flag.NewFlagSet("dashboard", flag.ExitOnError)
	port := fs.Int("port", 7781, "HTTP port for the dashboard")
	dbPath := fs.String("db", "", "path to SQLite status database (default: $XDG_CACHE_HOME/gogitops/status.db)")
	repoDir := fs.String("repo", "/home/benn/Documents/code/GoGitOps", "path to config repo (reads mesh.yaml for auto-discovery)")
	nodesFlag := fs.String("nodes", "", "override: comma-separated name=address pairs (e.g. friday=10.2.0.102:7780). Skips mesh.yaml.")
	binDir := fs.String("binaries", "", "path to directory with pre-built binaries (e.g. gogitops-linux-amd64, gogitops-darwin-arm64). Enables /api/binary/ downloads.")
	dashHost := fs.String("host", "10.2.0.102", "external hostname/IP for the dashboard (used in deploy commands and agent registration)")
	fs.Parse(args)

	// Resolve DB path
	if *dbPath == "" {
		cacheDir := os.Getenv("XDG_CACHE_HOME")
		if cacheDir == "" {
			home, _ := os.UserHomeDir()
			cacheDir = home + "/.cache"
		}
		*dbPath = cacheDir + "/gogitops/status.db"
	}

	// Build node list
	var nodes []dashboard.NodeConfig

	if *nodesFlag != "" {
		// Manual override
		for _, pair := range strings.Split(*nodesFlag, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				log.Fatalf("invalid --nodes entry %q (expected name=address)", pair)
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
		// Auto-discover from mesh.yaml
		mesh, err := config.LoadMesh(*repoDir)
		if err != nil {
			log.Fatalf("failed to load mesh.yaml: %v\n(hint: use --nodes to manually specify peers)", err)
		}
		for _, peer := range mesh.Peers {
			addr := peer.Address()
			if addr == "" {
				continue // skip peers with no reachable address
			}
			// DisplayIP: show both IPs if available, otherwise just the one used
			displayIP := peer.LanIP
			if displayIP == "" || displayIP == "null" {
				displayIP = peer.NebulaIP
			} else if peer.NebulaIP != "" && peer.NebulaIP != "null" {
				// Show LAN as primary, Nebula as secondary
				displayIP = peer.LanIP + " (lan) / " + peer.NebulaIP + " (neb)"
			}
			nodes = append(nodes, dashboard.NodeConfig{
				Name:      peer.Hostname,
				Address:   addr,
				DisplayIP: displayIP,
			})
		}
		if len(nodes) == 0 {
			log.Printf("warning: mesh.yaml has no peers with reachable addresses")
		}
	}

	// Open store
	store, err := dashboard.NewStore(*dbPath)
	if err != nil {
		log.Fatalf("failed to open status database: %v", err)
	}
	defer store.Close()

	// Create handler
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
