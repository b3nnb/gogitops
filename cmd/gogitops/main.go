// gogitops — decentralized agent mesh for Benn's homelab.
//
// Phase 1: health checks, peer pings over Nebula, Discord alerts, starship cache.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/bennbanks/gogitops/internal/agent"
	"github.com/bennbanks/gogitops/internal/config"
)

var (
	version = "dev"
)

func main() {
	var (
		repoDir    = flag.String("repo", "/home/benn/Documents/code/GoGitOps", "path to config repo")
		hostFlag   = flag.String("hostname", "", "override hostname (defaults to system hostname; match a nodes/*.yaml name)")
		bindAddr   = flag.String("bind", "", "bind address for health API (default: node's nebula IP)")
		port       = flag.Int("port", 7780, "health API port")
		intervalS  = flag.Int("interval", 60, "check interval in seconds")
		webhook    = flag.String("webhook", "", "Discord webhook URL or nenv:<ns>/<key> (empty = no alerts)")
		netenvURL  = flag.String("netenv", "", "netenv server URL (overrides agent config)")
		netenvToken = flag.String("netenv-token", "", "netenv API token (overrides agent config)")
		showVer    = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Printf("gogitops %s\n", version)
		return
	}

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

	// Apply netenv flag overrides
	if *netenvURL != "" {
		node.Agent.NetEnvURL = *netenvURL
	}
	if *netenvToken != "" {
		node.Agent.NetEnvToken = *netenvToken
	}

	// Determine bind address — default to Nebula IP so agent is invisible to LAN
	bind := *bindAddr
	if bind == "" {
		bind = node.NebulaIP
	}
	if bind == "" || bind == "null" {
		bind = "127.0.0.1" // fallback: no nebula IP configured, localhost only
		log.Printf("warning: no nebula_ip in node config — binding to localhost only")
	}

	// Create agent
	a := agent.New(node, meshCfg, *webhook)
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
