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
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
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
	node    *config.NodeConfig
	mesh    *config.MeshConfig
	pinger  *mesh.Pinger
	sender  *alert.Sender
	started time.Time
	webhook string
	repoDir string
	logger  *agentlog.Logger

	mu               sync.RWMutex
	lastReachable    []string
	lastUnreachable  []string
	lastServiceState map[string]string
	lastPeerState    map[string]bool
	lastGitPull      time.Time
	lastGitResult    string
	cycleCount       int64

	// Git convergence state (BCR-57 starvation class-bug fix): surfaced via
	// /v1/health so dashboards and watchdogs can see a starved pull instead
	// of trusting a silently stale node. lastGitPull is the shared
	// timestamp of the last pull attempt.
	lastPullOK         bool
	lastPullErr        string
	lastDivergenceAt   time.Time
	lastDivergenceInfo string
	lastPushOK         bool
	lastPushErr        string
	// hw is the static hardware inventory, collected once in the
	// background at startup (nil until the first probe round finishes).
	hw *mesh.HardwareSpecs
}

// New creates an agent from config
func New(node *config.NodeConfig, m *config.MeshConfig, webhook string) *Agent {
	a := &Agent{
		node:             node,
		mesh:             m,
		pinger:           mesh.NewPinger(m),
		sender:           alert.NewSender(webhook),
		started:          time.Now(),
		webhook:          webhook,
		logger:           agentlog.Default(),
		lastServiceState: map[string]string{},
		lastPeerState:    map[string]bool{},
	}
	// Hardware inventory: collect once, off the critical path — some probes
	// (dmidecode, lspci, system_profiler) take a moment and health serving
	// must not block on them.
	go a.collectHardware()
	return a
}

// collectHardware gathers the node's hardware specs once and caches them.
func (a *Agent) collectHardware() {
	hw := toMeshHardware(health.CollectHardwareSpecs())
	a.mu.Lock()
	a.hw = &hw
	a.mu.Unlock()
}

// toMeshHardware maps the collected health inventory to the wire type.
func toMeshHardware(h health.HardwareSpecs) mesh.HardwareSpecs {
	m := mesh.HardwareSpecs{
		CPU:         h.CPU,
		CPUCores:    h.CPUCores,
		RAMGB:       h.RAMGB,
		RAMType:     h.RAMType,
		Motherboard: h.Motherboard,
	}
	for _, g := range h.GPUs {
		m.GPUs = append(m.GPUs, mesh.GPUInfo{Model: g.Model, VRAMMB: g.VRAMMB, Driver: g.Driver})
	}
	for _, d := range h.Disks {
		m.Disks = append(m.Disks, mesh.DiskInfo{Model: d.Model, SizeGB: d.SizeGB, Rotational: d.Rotational})
	}
	return m
}

// SetRepoDir sets the git config repo path (for git pull operations)
func (a *Agent) SetRepoDir(repoDir string) {
	a.repoDir = repoDir
}

// toDiskMounts maps health disk results to the mesh wire type.
func toDiskMounts(in []health.DiskStatus) []mesh.DiskMount {
	if len(in) == 0 {
		return nil
	}
	out := make([]mesh.DiskMount, 0, len(in))
	for _, d := range in {
		out = append(out, mesh.DiskMount{Mount: d.Mount, UsedPct: d.UsedPct, WarnPct: d.WarnPct, CritPct: d.CritPct})
	}
	return out
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
	hw := a.hw
	pullOK, pullAt, pullErr := a.lastPullOK, a.lastGitPull, a.lastPullErr
	divAt, divInfo := a.lastDivergenceAt, a.lastDivergenceInfo
	pushErr := a.lastPushErr
	a.mu.RUnlock()

	sys := health.CollectSysInfo()

	h := mesh.PeerHealth{
		Hostname:       a.node.Hostname,
		Nickname:       a.node.Nickname,
		MachineID:      a.node.MachineID,
		AgentVersion:   Version,
		UptimeSeconds:  int64(time.Since(a.started).Seconds()),
		NebulaRunning:  a.nebulaRunning(),
		NebulaIP:       a.nebulaIP(),
		Labels:         a.node.Labels,
		Services:       svcMap,
		DiskWarns:      append(result.DiskWarns, result.DiskCrits...),
		Disk:           toDiskMounts(result.Disk),
		PeersReachable: reachable,
		PeersUnreach:   unreachable,
		System: mesh.SystemInfo{
			OS:     sys.OS,
			Arch:   sys.Arch,
			IP:     sys.IP,
			HostID: sys.HostID,
		},
		Hardware: hw, // nil → omitted (old agents / still probing)
	}

	// Pull convergence state (BCR-57): omitted until the first pull so old
	// agents stay wire-compatible; last_pull_ok=false means the node is
	// starved — dashboards and watchdogs must not trust its recipes.
	if !pullAt.IsZero() {
		ok := pullOK
		h.LastPullOK = &ok
		h.LastPullAt = pullAt.Format(time.RFC3339)
		h.LastPullError = pullErr
	}
	if !divAt.IsZero() {
		h.LastDivergenceAt = divAt.Format(time.RFC3339)
		h.LastDivergenceInfo = divInfo
	}
	h.LastPushError = pushErr

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h)
}

// ConfigHandler serves GET /v1/config — returns the agent's config info
func (a *Agent) ConfigHandler(w http.ResponseWriter, r *http.Request) {
	type configResp struct {
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

	a.mu.RLock()
	pullOK, pullErr := a.lastPullOK, a.lastPullErr
	divAt := a.lastDivergenceAt
	a.mu.RUnlock()

	resp := map[string]interface{}{
		"result":          result,
		"hostname":        a.node.Hostname,
		"time":            time.Now().Format("2006-01-02T15:04:05"),
		"last_pull_ok":    pullOK,
		"last_pull_error": pullErr,
	}
	if !divAt.IsZero() {
		resp["last_divergence_at"] = divAt.Format(time.RFC3339)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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

	// Flush response, then restart IN PLACE: syscall.Exec replaces this
	// process image with the same binary — same PID, no reliance on a
	// service manager or spawn context. Same mechanism the self-updater
	// uses (proven on every node): cron/launchd-spawned relaunches hang
	// pre-main on macOS — exec sidesteps that entirely. Fall back to
	// exit 0 (systemd Restart=always / cron keepalive) if exec fails.
	go func() {
		time.Sleep(500 * time.Millisecond)
		a.logger.Action("agent", "agent restarting in place")
		if runtime.GOOS != "windows" {
			if exe, err := os.Executable(); err == nil {
				if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
					a.logger.Errorf("agent", "exec restart failed (%v) — falling back to exit", err)
				}
			}
		}
		a.logger.Action("agent", "agent exiting for restart")
		os.Exit(0)
	}()
}

// gitOut runs a git command in dir, returning trimmed combined output and
// success. Shared helper for the pull convergence path.
func gitOut(dir string, args ...string) (string, bool) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

// loudf is the LOUD path (BCR-57): the agent log ring served by /v1/logs AND
// stderr (systemd journal) — never a bare stderr print that dashboards and
// watchdogs cannot see. Nil-logger-safe for tests.
func (a *Agent) loudf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if a.logger != nil {
		a.logger.Errorf("git", "%s", msg)
	}
	log.Printf("[gogitops] %s", msg)
}

// quietf logs to the ring + journal at info level. Nil-logger-safe.
func (a *Agent) quietf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if a.logger != nil {
		a.logger.Infof("git", "%s", msg)
	}
	log.Printf("[gogitops] %s", msg)
}

// actionf logs a state-changing action to the ring + journal. Nil-logger-safe.
func (a *Agent) actionf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if a.logger != nil {
		a.logger.Actionf("git", "%s", msg)
	}
	log.Printf("[gogitops] %s", msg)
}

// divergence reports (behind, ahead) vs the branch's upstream. ok=false when
// there is no upstream or git cannot answer (e.g. repo without remotes).
func (a *Agent) divergence() (behind, ahead int, ok bool) {
	out, ok := gitOut(a.repoDir, "rev-list", "--left-right", "--count", "@{upstream}...HEAD")
	if !ok {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(out, "%d	%d", &behind, &ahead); err != nil {
		return 0, 0, false
	}
	return behind, ahead, true
}

// pushMailboxRetry re-pushes HEAD to this node's submission mailbox branch
// (refs/heads/node/<hostname>, force — single-writer by design) whenever the
// local branch carries commits origin lacks. This is the retry loop that
// finishes the starved submit path: a submit that failed for missing push
// credentials keeps retrying every git tick and flows out the moment creds
// exist. Failures are logged LOUDLY on transition (not every tick).
func (a *Agent) pushMailboxRetry(ahead int) {
	if a.node == nil || a.node.Hostname == "" || ahead == 0 {
		return
	}
	out, ok := gitOut(a.repoDir, "push", "--force", "origin", "HEAD:refs/heads/node/"+a.node.Hostname)
	a.mu.Lock()
	prevErr := a.lastPushErr
	wasOK := a.lastPushOK
	a.lastPushOK = ok
	if ok {
		a.lastPushErr = ""
	} else {
		a.lastPushErr = clampOut(out, 500)
	}
	a.mu.Unlock()
	if ok {
		if !wasOK || prevErr != "" {
			a.actionf("mesh mailbox push RECOVERED: HEAD pushed to refs/heads/node/%s", a.node.Hostname)
		}
		return
	}
	if a.lastPushErr != prevErr || !wasOK {
		a.loudf("MESH PUSH FAILED — %d local commits stay on main (starvation risk): %s", ahead, a.lastPushErr)
	}
}

// clampOut trims multi-line git output for log lines.
func clampOut(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// gitPull converges the config repo. Starvation class-bug fix (BCR-57):
//
// Previously a node with local-only identity commits (mesh self-refreshes
// that could not be pushed) would fail its ff-only pull the moment origin
// advanced, and starve SILENTLY forever — recipes stopped converging and
// nothing anywhere said why. Now:
//
//  1. origin is fetched and divergence (ahead+behind) is detected BEFORE the
//     pull and logged LOUDLY (log ring + journal), even when self-heal then
//     succeeds — a silent rebase is exactly the failure mode nobody noticed.
//  2. ff-only failure self-heals via rebase (autostash keeps local edits).
//  3. a rebase conflict no longer wedges the node: local-only commits are
//     preserved in a backup ref, flagged LOUDLY, then main resets to
//     upstream so convergence always resumes.
//  4. pull state (last_pull_ok + error) is recorded and surfaced via
//     /v1/health for dashboards and watchdogs.
//  5. any remaining local-only commits are retried to the node's mailbox
//     branch every tick, so they reach origin as soon as push auth allows.
func (a *Agent) gitPull() string {
	if a.repoDir == "" {
		return "no repo directory configured"
	}
	run := func(args ...string) (string, bool) { return gitOut(a.repoDir, args...) }

	// 1. Fetch so divergence is measurable before the pull.
	if out, ok := run("fetch", "origin"); !ok {
		a.loudf("git fetch FAILED — pulls may starve on stale refs: %s", clampOut(out, 300))
		// fall through: a stale-refs pull is still better than none.
	}

	// 2. Divergence check + LOUD pre-log.
	behind, ahead, divOK := a.divergence()
	if divOK && ahead > 0 && behind > 0 {
		local, _ := run("log", "--format=%h %s", "@{upstream}..HEAD")
		a.mu.Lock()
		a.lastDivergenceAt = time.Now()
		a.lastDivergenceInfo = fmt.Sprintf("ahead %d, behind %d", ahead, behind)
		a.mu.Unlock()
		a.loudf("PULL DIVERGENCE: main is ahead %d / behind %d of upstream — self-heal starting. Local-only commits:\n%s",
			ahead, behind, clampOut(local, 800))
	}

	// 3. Fast-forward pull.
	pullOut, pullOK := run("pull", "--ff-only")
	result := pullOut
	errFailed := !pullOK
	if errFailed {
		// Diverged (or dirty) — rebase our local-only commits onto the
		// fetched upstream. Per-node mesh.d/ files are disjoint, so a
		// rebase is normally clean; autostash protects uncommitted
		// repo-local state (older gits ignore it and fail cleanly).
		rbOut, rbOK := run("-c", "rebase.autoStash=true", "pull", "--rebase")
		if rbOK {
			errFailed = false
			result = rbOut
			if divOK && ahead > 0 && behind > 0 {
				a.actionf("self-heal: rebased %d local-only commit(s) onto upstream (was behind %d) — starvation avoided", ahead, behind)
			}
		} else {
			// Unwedge any half-finished rebase and restore the autostash.
			run("rebase", "--abort")
			run("stash", "pop")
			// 3b. Rebase conflicted. Flag LOUDLY, preserve the orphaned
			// commits in a backup ref, then reset main to upstream so
			// recipe convergence resumes no matter what. The backup ref
			// keeps every local-only commit recoverable by hand.
			ts := time.Now().UTC().Format("20060102T150405Z")
			backup := "backup/divergence-" + ts
			shas, _ := run("log", "--format=%h %s", "@{upstream}..HEAD")
			if _, bok := run("branch", backup); !bok {
				a.loudf("self-heal: could not create backup ref %s", backup)
			}
			if rsOut, rsOK := run("reset", "--hard", "@{upstream}"); rsOK {
				a.loudf("STARVATION KILLED: rebase conflicted; main reset to upstream. Local-only commits preserved in %s:\n%s",
					backup, clampOut(shas, 800))
				a.actionf("self-heal: reset main to upstream after conflict; recover via %s", backup)
				result = "self-heal: reset to upstream after conflict (backup: " + backup + ")"
				errFailed = false
			} else {
				a.loudf("git pull failed AND self-heal failed — MAIN IS STARVED, recipes will not converge: %s", clampOut(rsOut, 400))
				result = "error: " + clampOut(rsOut, 300)
			}
		}
	}
	if !errFailed {
		if result == "" || strings.Contains(result, "Already up to date") {
			result = "up to date"
		}
		a.quietf("git pull: %s", result)
	} else {
		a.loudf("git pull failed: %s", clampOut(result, 400))
		result = "error: " + clampOut(result, 400)
	}

	// 4. Record pull state for /v1/health.
	a.mu.Lock()
	a.lastGitPull = time.Now()
	a.lastGitResult = result
	a.lastPullOK = !errFailed
	if errFailed {
		a.lastPullErr = clampOut(result, 300)
	} else {
		a.lastPullErr = ""
	}
	a.mu.Unlock()

	// 5. Retry the mailbox push while local-only commits persist.
	if !errFailed {
		if _, aheadNow, ok := a.divergence(); ok && aheadNow > 0 {
			a.pushMailboxRetry(aheadNow)
		}
	}

	return result
}

// Run starts the main agent loop
func (a *Agent) Run(interval time.Duration) {
	a.logger.Infof("agent", "gogitops agent %s starting on %s (labels: %v)", Version, a.node.Hostname, a.node.Labels)
	log.Printf("gogitops agent %s starting on %s (labels: %v)", Version, a.node.Hostname, a.node.Labels)

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
			a.maybeSelfUpdate()
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

	// 2. Peer pings (only if peers are configured — standalone nodes skip this)
	var reachable, unreachable []string
	peersTotal := 0
	peersHealthy := 0
	if len(a.mesh.Peers) > 1 {
		reachable, unreachable = a.pinger.PingAll(a.node.Hostname)
		peersTotal = len(a.mesh.Peers) - 1
		peersHealthy = len(reachable)
	}

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
// nebulaIP prefers the node yaml declaration; falls back to live
// interface detection — dnclient names its interface defined1, self-hosted
// nebula uses nebula0, so detection scans for 10.200.0.x on ANY interface.
func (a *Agent) nebulaIP() string {
	if a.node.NebulaIP != "" && a.node.NebulaIP != "null" {
		return a.node.NebulaIP
	}
	return config.DetectNebulaIP()
}

// nebulaRunning: up if any of
//  1. a nebula process is running (self-hosted nebula binary)
//  2. a dnclient process is running (Defined Networking's wrapper — it
//     embeds nebula, so the process name never contains "nebula"; this
//     was the source of the long-standing false "down" on dnclient nodes)
//  3. any interface carries a 10.200.0.x address (the tunnel exists —
//     strongest signal, works regardless of process naming)
func (a *Agent) nebulaRunning() bool {
	for _, pat := range []string{"nebula", "dnclient"} {
		if err := exec.Command("pgrep", "-f", pat).Run(); err == nil {
			return true
		}
	}
	return config.DetectNebulaIP() != ""
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
