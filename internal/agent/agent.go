// Package agent implements the main agent loop: check services, ping peers,
// write starship cache, and alert on state changes.
package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/bennbanks/gogitops/internal/alert"
	"github.com/bennbanks/gogitops/internal/config"
	"github.com/bennbanks/gogitops/internal/health"
	"github.com/bennbanks/gogitops/internal/mesh"
	"github.com/bennbanks/gogitops/internal/netenv"
	"github.com/bennbanks/gogitops/internal/starship"
)

// Version is the agent version — set via ldflags at build time
var Version = "dev"

// Agent is the running agent instance
type Agent struct {
	node     *config.NodeConfig
	mesh     *config.MeshConfig
	pinger   *mesh.Pinger
	sender   *alert.Sender
	started  time.Time
	webhook  string

	// last state for change detection
	lastServiceState map[string]string
	lastPeerState    map[string]bool

	// netenv client for key rotation sync
	netenvClient   *netenv.Client
	localKeyVersion int // last known active key version from netenv
}

// New creates an agent from config
func New(node *config.NodeConfig, m *config.MeshConfig, webhook string) *Agent {
	var nvc *netenv.Client
	if node.Agent.NetEnvURL != "" && node.Agent.NetEnvToken != "" {
		nvc = netenv.NewClient(node.Agent.NetEnvURL, node.Agent.NetEnvToken, node.Hostname)
		log.Printf("netenv client configured: %s", node.Agent.NetEnvURL)
	}

	// Resolve nenv: references (webhook URL, netenv token itself)
	if nvc != nil {
		if len(webhook) > 5 && webhook[:5] == "nenv:" {
			if resolved, err := nvc.ResolveReference(webhook); err == nil {
				webhook = resolved
			} else {
				log.Printf("warning: failed to resolve webhook reference %q: %v", webhook, err)
			}
		}
		if len(node.Agent.NetEnvToken) > 5 && node.Agent.NetEnvToken[:5] == "nenv:" {
			// Bootstrap: need a temporary client with a bootstrap token to resolve the actual token
			// For now, log a warning — the token should be set directly or via env var
			log.Printf("warning: netenv_token is a nenv: reference — set the actual token value or use GOGITOPS_NETENV_TOKEN env var")
		}
	}

	return &Agent{
		node:     node,
		mesh:     m,
		pinger:   mesh.NewPinger(m),
		sender:   alert.NewSender(webhook),
		started:  time.Now(),
		webhook:  webhook,
		lastServiceState: map[string]string{},
		lastPeerState:    map[string]bool{},
		netenvClient:     nvc,
	}
}

// HealthHandler serves GET /v1/health — the peer ping endpoint.
// Only listens on the Nebula interface (enforced by bind address in main).
func (a *Agent) HealthHandler(w http.ResponseWriter, r *http.Request) {
	result := health.RunAllChecks(a.node)
	svcMap := map[string]string{}
	for _, s := range result.Services {
		svcMap[s.Name] = s.Status
	}

	reachable, unreachable := a.pinger.PingAll(a.node.Hostname)

	h := mesh.PeerHealth{
		Hostname:       a.node.Hostname,
		AgentVersion:   Version,
		UptimeSeconds:  int64(time.Since(a.started).Seconds()),
		NebulaRunning:  a.nebulaRunning(),
		Labels:         a.node.Labels,
		Services:       svcMap,
		DiskWarns:      append(result.DiskWarns, result.DiskCrits...),
		PeersReachable: reachable,
		PeersUnreach:   unreachable,
		KeyVersion:     a.localKeyVersion,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h)
}

// Run starts the main agent loop
func (a *Agent) Run(interval time.Duration) {
	log.Printf("gogitops agent v%s starting on %s (labels: %v)", Version, a.node.Hostname, a.node.Labels)

	// initial state — no alerts on first pass
	a.cycle(true)

	ticker := time.NewTicker(interval)
	for range ticker.C {
		a.cycle(false)
	}
}

// cycle runs one check cycle: services, peers, cache write, netenv sync, alerts
func (a *Agent) cycle(first bool) {
	// 1. Service checks
	result := health.RunAllChecks(a.node)

	// 2. Peer pings
	reachable, unreachable := a.pinger.PingAll(a.node.Hostname)
	peersTotal := len(a.mesh.Peers) - 1 // exclude self
	peersHealthy := len(reachable)

	// 3. Write starship cache
	var downSvcs []string
	for _, s := range result.Services {
		if s.Status != "running" {
			downSvcs = append(downSvcs, s.Name)
		}
	}
	cache := starship.PromptData{
		PeersTotal:      peersTotal,
		PeersHealthy:    peersHealthy,
		ServicesHealthy: len(downSvcs) == 0,
		ServicesDown:    downSvcs,
		DiskWarn:        len(result.DiskWarns) > 0 || len(result.DiskCrits) > 0,
		DiskWarnMounts:  append(result.DiskWarns, result.DiskCrits...),
		Version:         Version,
		Labels:          a.node.Labels,
		Hostname:        a.node.Hostname,
		NebulaRunning:   a.nebulaRunning(),
		KeyVersion:      a.localKeyVersion,
	}
	if err := starship.WriteCache(cache); err != nil {
		log.Printf("starship cache write failed: %v", err)
	}

	// 4. NetEnv key rotation sync
	if a.netenvClient != nil {
		a.syncNetEnvKeys()
	}

	// 5. Alert on state CHANGES (not every cycle)
	if !first {
		a.alertOnChange(result, reachable, unreachable)
	}

	// 6. Store state for next cycle's change detection
	a.lastServiceState = map[string]string{}
	for _, s := range result.Services {
		a.lastServiceState[s.Name] = s.Status
	}
	a.lastPeerState = map[string]bool{}
	for _, p := range reachable {
		a.lastPeerState[p] = true
	}
}

// syncNetEnvKeys checks netenv for the active key version and reports local state.
func (a *Agent) syncNetEnvKeys() {
	// Get current key state from netenv
	ks, err := a.netenvClient.GetKeys()
	if err != nil {
		log.Printf("netenv key sync failed: %v", err)
		return
	}

	// Check if our local version matches
	if a.localKeyVersion != ks.ActiveVersion {
		log.Printf("netenv key rotation detected: v%d → v%d", a.localKeyVersion, ks.ActiveVersion)
		a.localKeyVersion = ks.ActiveVersion
	}

	// Check-in to netenv with our current key version
	if err := a.netenvClient.CheckIn(a.localKeyVersion); err != nil {
		log.Printf("netenv check-in failed: %v", err)
	}

	// Also check rotation state
	rs, err := a.netenvClient.GetRotationState()
	if err == nil && rs.Overdue {
		log.Printf("WARNING: netenv key rotation is overdue (next was %s)", rs.NextRotation)
	}
}

// alertOnChange compares current state to last state and alerts on transitions
func (a *Agent) alertOnChange(result health.CheckResult, reachable, unreachable []string) {
	// Service state changes: running→down or down→running
	for _, s := range result.Services {
		prev, existed := a.lastServiceState[s.Name]
		if !existed {
			continue
		}
		if prev == "running" && s.Status != "running" {
			a.sender.SendAlert("critical", a.node.Hostname, fmt.Sprintf("service DOWN: %s (%s)", s.Name, s.Detail))
		}
		if prev != "running" && s.Status == "running" {
			a.sender.SendAlert("info", a.node.Hostname, fmt.Sprintf("service RECOVERED: %s", s.Name))
		}
	}

	// Peer state changes
	reachSet := map[string]bool{}
	for _, p := range reachable {
		reachSet[p] = true
	}
	for peer, wasUp := range a.lastPeerState {
		nowUp := reachSet[peer]
		if wasUp && !nowUp {
			a.sender.SendAlert("critical", a.node.Hostname, fmt.Sprintf("peer UNREACHABLE: %s", peer))
		}
		if !wasUp && nowUp {
			a.sender.SendAlert("info", a.node.Hostname, fmt.Sprintf("peer BACK ONLINE: %s", peer))
		}
	}

	// Disk threshold crossings
	if len(result.DiskCrits) > 0 {
		a.sender.SendAlert("critical", a.node.Hostname, fmt.Sprintf("disk CRITICAL: %v", result.DiskCrits))
	}
}

// nebulaRunning checks if the nebula process is alive
func (a *Agent) nebulaRunning() bool {
	if _, err := os.Stat("/proc"); err == nil {
		// Linux — use pgrep
		cmd := exec.Command("pgrep", "-f", "nebula")
		if err := cmd.Run(); err == nil {
			return true
		}
	}
	// macOS fallback — pgrep exists there too
	cmd := exec.Command("pgrep", "-f", "nebula")
	return cmd.Run() == nil
}
