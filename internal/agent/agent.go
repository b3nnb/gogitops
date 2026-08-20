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
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bennbanks/gogitops/internal/agentlog"
	"github.com/bennbanks/gogitops/internal/alert"
	"github.com/bennbanks/gogitops/internal/config"
	"github.com/bennbanks/gogitops/internal/health"
	"github.com/bennbanks/gogitops/internal/mesh"
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
	repoDir  string
	logger   *agentlog.Logger

	mu               sync.RWMutex
	lastReachable    []string
	lastUnreachable  []string
	lastServiceState map[string]string
	lastPeerState    map[string]bool
	lastGitPull      time.Time
	lastGitResult    string
	cycleCount       int64
}

// New creates an agent from config
func New(node *config.NodeConfig, m *config.MeshConfig, webhook string) *Agent {
	return &Agent{
		node:     node,
		mesh:     m,
		pinger:   mesh.NewPinger(m),
		sender:   alert.NewSender(webhook),
		started:  time.Now(),
		webhook:  webhook,
		logger:   agentlog.Default(),
		lastServiceState: map[string]string{},
		lastPeerState:    map[string]bool{},
	}
}

// SetRepoDir sets the git config repo path (for git pull operations)
func (a *Agent) SetRepoDir(repoDir string) {
	a.repoDir = repoDir
}

// HealthHandler serves GET /v1/health — the peer ping endpoint.
func (a *Agent) HealthHandler(w http.ResponseWriter, r *http.Request) {
	result := health.RunAllChecks(a.node)
	svcMap := map[string]string{}
	for _, s := range result.Services {
		svcMap[s.Name] = s.Status
	}

	a.mu.RLock()
	reachable := a.lastReachable
	unreachable := a.lastUnreachable
	a.mu.RUnlock()

	sys := health.CollectSysInfo()

	h := mesh.PeerHealth{
		Hostname:       a.node.Hostname,
		AgentVersion:   Version,
		UptimeSeconds:  int64(time.Since(a.started).Seconds()),
		NebulaRunning:  a.nebulaRunning(),
		NebulaIP:       a.node.NebulaIP,
		Labels:         a.node.Labels,
		Services:       svcMap,
		DiskWarns:      append(result.DiskWarns, result.DiskCrits...),
		PeersReachable: reachable,
		PeersUnreach:   unreachable,
		System: mesh.SystemInfo{
			OS:     sys.OS,
			Arch:   sys.Arch,
			IP:     sys.IP,
			HostID: sys.HostID,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h)
}

// ConfigHandler serves GET /v1/config — returns the agent's config info
func (a *Agent) ConfigHandler(w http.ResponseWriter, r *http.Request) {
	type configResp struct {
		Hostname   string   `json:"hostname"`
		RepoDir    string   `json:"repo_dir"`
		GitRepo    string   `json:"git_repo"`
		GitBranch  string   `json:"git_branch"`
		GitPullInt string   `json:"git_pull_interval"`
		Labels     []string `json:"labels"`
		Services   []string `json:"services"`
		DiskChecks []string `json:"disk_checks"`
		Groups     []string `json:"groups"`
		Recipes    []string `json:"recipes"`
		Webhook    string   `json:"webhook_configured"`
		Uptime     int64    `json:"uptime_seconds"`
		Cycles     int64    `json:"cycle_count"`
		LastGitPull string  `json:"last_git_pull"`
		LastGitResult string `json:"last_git_result"`
	}

	// Collect service names
	var svcNames []string
	for _, s := range a.node.Services {
		svcNames = append(svcNames, s.Name)
	}
	sort.Strings(svcNames)

	// Collect disk check mounts
	var diskMounts []string
	for _, d := range a.node.Disk {
		diskMounts = append(diskMounts, d.Mount)
	}

	// Find groups and recipes from repo
	var groups, recipes []string
	if a.repoDir != "" {
		groups = listDirNames(a.repoDir + "/groups")
		recipes = listDirNames(a.repoDir + "/recipes")
	}

	a.mu.RLock()
	cycles := a.cycleCount
	lastPull := a.lastGitPull
	lastResult := a.lastGitResult
	a.mu.RUnlock()

	webhookCfg := "none"
	if a.webhook != "" {
		webhookCfg = "configured"
	}

	resp := configResp{
		Hostname:      a.node.Hostname,
		RepoDir:       a.repoDir,
		GitRepo:       a.node.Agent.GitRepo,
		GitBranch:     a.node.Agent.GitBranch,
		GitPullInt:    a.node.Agent.GitPullInterval,
		Labels:        a.node.Labels,
		Services:      svcNames,
		DiskChecks:    diskMounts,
		Groups:        groups,
		Recipes:       recipes,
		Webhook:       webhookCfg,
		Uptime:        int64(time.Since(a.started).Seconds()),
		Cycles:        cycles,
		LastGitPull:   lastPull.Format("2006-01-02T15:04:05"),
		LastGitResult: lastResult,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// LogsHandler serves GET /v1/logs — returns recent log entries
func (a *Agent) LogsHandler(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if r.URL.Query().Get("limit") != "" {
		fmt.Sscanf(r.URL.Query().Get("limit"), "%d", &limit)
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}

	entries, err := agentlog.ReadEntries(limit)
	if err != nil {
		http.Error(w, `{"error": "no logs found"}`, 404)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

// GitPullHandler serves POST /v1/git/pull — forces a git pull
func (a *Agent) GitPullHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, `{"error": "POST required"}`, 405)
		return
	}

	result := a.gitPull()

	a.logger.Actionf("git", "manual git pull: %s", result)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"result":   result,
		"hostname": a.node.Hostname,
		"time":     time.Now().Format("2006-01-02T15:04:05"),
	})
}

// RestartHandler serves POST /v1/restart — restarts the agent process
func (a *Agent) RestartHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, `{"error": "POST required"}`, 405)
		return
	}

	a.logger.Action("agent", "restart requested via API")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":   "restarting",
		"hostname": a.node.Hostname,
	})

	// Flush response then exit — systemd will restart us
	go func() {
		time.Sleep(500 * time.Millisecond)
		a.logger.Action("agent", "agent exiting for restart")
		os.Exit(0)
	}()
}

// gitPull runs git pull in the repo directory
func (a *Agent) gitPull() string {
	if a.repoDir == "" {
		return "no repo directory configured"
	}

	cmd := exec.Command("git", "pull", "--ff-only")
	cmd.Dir = a.repoDir
	out, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(out))
	if err != nil {
		a.logger.Errorf("git", "git pull failed: %s", result)
		result = "error: " + result
	} else {
		if result == "" || strings.Contains(result, "Already up to date") {
			result = "up to date"
		}
		a.logger.Infof("git", "git pull: %s", result)
	}

	a.mu.Lock()
	a.lastGitPull = time.Now()
	a.lastGitResult = result
	a.mu.Unlock()

	return result
}

// Run starts the main agent loop
func (a *Agent) Run(interval time.Duration) {
	a.logger.Infof("agent", "gogitops agent v%s starting on %s (labels: %v)", Version, a.node.Hostname, a.node.Labels)
	log.Printf("gogitops agent v%s starting on %s (labels: %v)", Version, a.node.Hostname, a.node.Labels)

	// Initial git pull
	if a.repoDir != "" {
		a.gitPull()
	}

	// Initial git pull interval parsing
	gitInterval := 5 * time.Minute
	if a.node.Agent.GitPullInterval != "" {
		if d, err := time.ParseDuration(a.node.Agent.GitPullInterval); err == nil {
			gitInterval = d
		}
	}

	// initial state — no alerts on first pass
	a.cycle(true)
	a.logger.Info("agent", "first check cycle complete")

	ticker := time.NewTicker(interval)
	gitTicker := time.NewTicker(gitInterval)
	defer ticker.Stop()
	defer gitTicker.Stop()

	for {
		select {
		case <-ticker.C:
			a.cycle(false)
		case <-gitTicker.C:
			a.gitPull()
		}
	}
}

// cycle runs one check cycle: services, peers, cache write, alerts
func (a *Agent) cycle(first bool) {
	a.mu.Lock()
	a.cycleCount++
	a.mu.Unlock()

	// 1. Service checks
	result := health.RunAllChecks(a.node)

	// 2. Peer pings
	reachable, unreachable := a.pinger.PingAll(a.node.Hostname)
	peersTotal := len(a.mesh.Peers) - 1
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
		ServicesUp:      len(result.Services) - len(downSvcs),
		ServicesTotal:   len(a.node.Services),
		ServicesHealthy: len(downSvcs) == 0,
		ServicesDown:    downSvcs,
		DiskWarn:        len(result.DiskWarns) > 0 || len(result.DiskCrits) > 0,
		DiskWarnMounts:  append(result.DiskWarns, result.DiskCrits...),
		Version:         Version,
		Labels:          a.node.Labels,
		Hostname:        a.node.Hostname,
		NebulaRunning:   a.nebulaRunning(),
	}
	if err := starship.WriteCache(cache); err != nil {
		a.logger.Errorf("agent", "starship cache write failed: %v", err)
	}

	// 4. Alert on state CHANGES (not every cycle)
	if !first {
		a.alertOnChange(result, reachable, unreachable)
	}

	// 5. Log cycle summary if anything interesting
	if len(downSvcs) > 0 && !first {
		a.logger.Warnf("service", "%d services down: %s", len(downSvcs), strings.Join(downSvcs, ", "))
	}

	// 6. Store state for next cycle's change detection
	a.mu.Lock()
	a.lastReachable = reachable
	a.lastUnreachable = unreachable
	a.lastServiceState = map[string]string{}
	for _, s := range result.Services {
		a.lastServiceState[s.Name] = s.Status
	}
	a.lastPeerState = map[string]bool{}
	for _, p := range reachable {
		a.lastPeerState[p] = true
	}
	a.mu.Unlock()
}

// alertOnChange compares current state to last state and alerts on transitions
func (a *Agent) alertOnChange(result health.CheckResult, reachable, unreachable []string) {
	for _, s := range result.Services {
		prev, existed := a.lastServiceState[s.Name]
		if !existed {
			continue
		}
		if prev == "running" && s.Status != "running" {
			a.sender.SendAlert("critical", a.node.Hostname, fmt.Sprintf("service DOWN: %s (%s)", s.Name, s.Detail))
			a.logger.Warnf("service", "service DOWN: %s (%s)", s.Name, s.Detail)
		}
		if prev != "running" && s.Status == "running" {
			a.sender.SendAlert("info", a.node.Hostname, fmt.Sprintf("service RECOVERED: %s", s.Name))
			a.logger.Infof("service", "service RECOVERED: %s", s.Name)
		}
	}

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

	if len(result.DiskCrits) > 0 {
		a.sender.SendAlert("critical", a.node.Hostname, fmt.Sprintf("disk CRITICAL: %v", result.DiskCrits))
		a.logger.Errorf("disk", "disk CRITICAL: %v", result.DiskCrits)
	}
}

// nebulaRunning checks if the nebula process is alive
func (a *Agent) nebulaRunning() bool {
	if _, err := os.Stat("/proc"); err == nil {
		cmd := exec.Command("pgrep", "-f", "nebula")
		if err := cmd.Run(); err == nil {
			return true
		}
	}
	cmd := exec.Command("pgrep", "-f", "nebula")
	return cmd.Run() == nil
}

// listDirNames returns directory names in a path
func listDirNames(path string) []string {
	if path == "" {
		return nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// FilePath returns the log file path (for CLI display)
func LogFilePath() string {
	return agentlog.LogPath()
}

// Ensure imports are used
var _ = filepath.Join
