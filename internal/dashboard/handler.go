// Package dashboard provides the HTTP handler for the GoGitOps status dashboard.
package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NodeConfig describes a known node to check.
// Address is the full host:port to reach the agent (e.g. "10.2.0.102:7780" or "100.2.0.2:7780").
// DisplayIP is shown in the UI — can be a Nebula IP, LAN IP, or hostname.
type NodeConfig struct {
	Name      string
	Address   string // full host:port for health check
	DisplayIP string // IP shown in dashboard table
}

// liveResult holds the outcome of a real-time health check against a node.
type liveResult struct {
	NodeName     string
	DisplayIP     string
	Healthy      bool
	ServicesUp   int
	ServicesTotal int
	Version      string
	ResponseMs   int
	Error        string
	CheckedAt    time.Time
}

// apiNodeStatus is the JSON shape returned by /api/status.
type apiNodeStatus struct {
	NodeName     string  `json:"node_name"`
	DisplayIP    string  `json:"ip"`
	Online       bool    `json:"online"`
	Healthy      bool    `json:"healthy"`
	HealthStatus string  `json:"health_status"` // "healthy" | "degraded" | "down"
	ServicesUp   int     `json:"services_up"`
	ServicesTotal int    `json:"services_total"`
	Version      string  `json:"version"`
	ResponseMs   int     `json:"response_time_ms"`
	Error        string  `json:"error,omitempty"`
	CheckedAt    string  `json:"checked_at"`
	Uptime24h    float64 `json:"uptime_24h_pct"`
}

// Handler serves the dashboard HTML and the API endpoints.
type Handler struct {
	store     *Store
	nodes     []NodeConfig
	mu        sync.Mutex
	cache     []liveResult // last live check results for /api/status
	dashURL   string       // the dashboard's own external URL (told to registering agents)
	binDir    string       // directory containing pre-built binaries for /api/binary/
}

// NewHandler creates a new dashboard handler.
func NewHandler(store *Store, nodes []NodeConfig, dashURL, binDir string) *Handler {
	return &Handler{
		store:   store,
		nodes:   nodes,
		dashURL: dashURL,
		binDir:  binDir,
	}
}

// ServeHTTP routes requests to either the dashboard page or the API endpoints.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/status":
		h.handleAPI(w, r)
	case r.URL.Path == "/api/register" && r.Method == "POST":
		h.handleRegister(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/binary/"):
		h.handleBinary(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/node/"):
		name := strings.TrimPrefix(r.URL.Path, "/api/node/")
		h.handleNodeAPI(w, r, name)
	case r.URL.Path == "/" || r.URL.Path == "/index.html":
		h.handleDashboard(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleAPI returns JSON status for all nodes.
func (h *Handler) handleAPI(w http.ResponseWriter, r *http.Request) {
	results, _ := h.checkAllNodesMerged(r.Context())

	statuses := make([]apiNodeStatus, 0, len(results))
	for _, res := range results {
		uptime, _ := h.store.Get24hSummary(r.Context(), res.NodeName)
		online := res.Error == ""
		healthStatus := "down"
		if online {
			if res.ServicesUp == res.ServicesTotal && res.ServicesTotal > 0 {
				healthStatus = "healthy"
			} else {
				healthStatus = "degraded"
			}
		}
		s := apiNodeStatus{
			NodeName:     res.NodeName,
			DisplayIP:    res.DisplayIP,
			Online:       online,
			Healthy:      res.Healthy,
			HealthStatus: healthStatus,
			ServicesUp:   res.ServicesUp,
			ServicesTotal: res.ServicesTotal,
			Version:      res.Version,
			ResponseMs:   res.ResponseMs,
			Uptime24h:    uptime,
			CheckedAt:    res.CheckedAt.Format(time.RFC3339),
		}
		if res.Error != "" {
			s.Error = res.Error
		}
		statuses = append(statuses, s)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(statuses)
}

// handleDashboard runs live checks, records them, then renders the HTML page.
func (h *Handler) handleDashboard(w http.ResponseWriter, r *http.Request) {
	results, allNodes := h.checkAllNodesMerged(r.Context())

	// Build template data
	type row struct {
		NodeName      string
		DisplayIP     string
		Online        bool
		OnlineEmoji   string // 🟢 or 🔴
		HealthStatus  string // "healthy", "degraded", "down"
		HealthEmoji   string
		Services      string // "12/12"
		Version       string
		LastCheckin   string // relative time
		Uptime24h     string // "99.8%"
		ResponseMs    int
		Address       string // for drill-down link
	}

	rows := make([]row, 0, len(results))
	for i, res := range results {
		addr := ""
		if i < len(allNodes) {
			addr = allNodes[i].Address
		}
		rw := row{
			NodeName:    res.NodeName,
			DisplayIP:   res.DisplayIP,
			Version:     res.Version,
			ResponseMs:  res.ResponseMs,
			Services:    formatServices(res.ServicesUp, res.ServicesTotal),
			LastCheckin: formatRelTime(res.CheckedAt),
			Address:     addr,
		}

		// Online status: can we reach the agent at all?
		online := res.Error == ""
		rw.Online = online
		if online {
			rw.OnlineEmoji = "🟢"
		} else {
			rw.OnlineEmoji = "🔴"
		}

		// Health status: service-level (only meaningful if online)
		if !online {
			rw.HealthStatus = "down"
			rw.HealthEmoji = "⚫"
		} else if res.ServicesUp == res.ServicesTotal && res.ServicesTotal > 0 {
			rw.HealthStatus = "healthy"
			rw.HealthEmoji = "💚"
		} else {
			rw.HealthStatus = "degraded"
			rw.HealthEmoji = "🟡"
		}

		uptime, _ := h.store.Get24hSummary(r.Context(), res.NodeName)
		rw.Uptime24h = formatUptime(uptime)

		rows = append(rows, rw)
	}

	data := map[string]interface{}{
		"Rows":    rows,
		"Empty":   len(allNodes) == 0,
		"Now":     time.Now().Format("15:04:05 MST"),
		"DashURL": h.dashURL,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTmpl.Execute(w, data); err != nil {
		log.Printf("dashboard template error: %v", err)
	}
}

// checkAllNodesMerged performs live health checks against all configured + registered nodes.
// Returns the results and the merged node list (for address lookups).
func (h *Handler) checkAllNodesMerged(ctx context.Context) ([]liveResult, []NodeConfig) {
	// Merge static nodes with self-registered nodes
	allNodes := h.nodes

	regNodes, err := h.store.GetRegisteredNodes(ctx)
	if err == nil {
		// Track which names are already in the static list
		seen := map[string]bool{}
		for _, n := range allNodes {
			seen[n.Name] = true
		}
		for _, rn := range regNodes {
			if !seen[rn.NodeName] {
				allNodes = append(allNodes, NodeConfig{
					Name:      rn.NodeName,
					Address:   rn.Address,
					DisplayIP: rn.DisplayIP,
				})
			}
		}
	}

	var wg sync.WaitGroup
	results := make([]liveResult, len(allNodes))

	for i, nc := range allNodes {
		wg.Add(1)
		go func(idx int, cfg NodeConfig) {
			defer wg.Done()
			results[idx] = h.checkNode(ctx, cfg)
		}(i, nc)
	}
	wg.Wait()

	// Record results to store
	for _, res := range results {
		err := h.store.RecordCheck(ctx, res.NodeName, res.DisplayIP, res.Healthy, res.ServicesUp, res.ServicesTotal, res.Version, res.ResponseMs)
		if err != nil {
			log.Printf("record check for %s: %v", res.NodeName, err)
		}
	}

	// Cache for /api/status
	h.mu.Lock()
	h.cache = results
	h.mu.Unlock()

	return results, allNodes
}

// checkNode performs a single health check against a node.
func (h *Handler) checkNode(ctx context.Context, cfg NodeConfig) liveResult {
	start := time.Now()
	res := liveResult{
		NodeName:  cfg.Name,
		DisplayIP: cfg.DisplayIP,
		CheckedAt: time.Now(),
	}

	client := &http.Client{Timeout: 5 * time.Second}
	url := "http://" + cfg.Address + "/v1/health"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}

	resp, err := client.Do(req)
	elapsed := time.Since(start)
	res.ResponseMs = int(elapsed.Milliseconds())
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		res.Error = "HTTP " + strconv.Itoa(resp.StatusCode)
		return res
	}

	// Decode the health response — we only need services and version.
	var health struct {
		AgentVersion string            `json:"agent_version"`
		Services     map[string]string `json:"services"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		res.Error = "decode error"
		return res
	}

	res.Healthy = true
	res.Version = health.AgentVersion
	up, total := 0, 0
	for _, v := range health.Services {
		total++
		if strings.EqualFold(v, "running") {
			up++
		}
	}
	res.ServicesUp = up
	res.ServicesTotal = total

	return res
}

// formatServices returns "up/total" string.
func formatServices(up, total int) string {
	if total == 0 {
		return "—"
	}
	return strconv.Itoa(up) + "/" + strconv.Itoa(total)
}

// formatRelTime returns a human-readable relative time string.
func formatRelTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < 2*time.Minute:
		return "1m ago"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 2*time.Hour:
		return "1h ago"
	default:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	}
}

// formatUptime returns a percentage string.
func formatUptime(pct float64) string {
	if pct == 0 {
		return "—"
	}
	return strconv.FormatFloat(pct, 'f', 1, 64) + "%"
}

// handleNodeAPI returns the full health payload from a single node's agent.
// This is the drill-down: fetches /v1/health from the agent directly.
func (h *Handler) handleNodeAPI(w http.ResponseWriter, r *http.Request, name string) {
	// Find the node's address — check static config then registered nodes
	var addr string
	for _, n := range h.nodes {
		if n.Name == name {
			addr = n.Address
			break
		}
	}
	if addr == "" {
		regNodes, err := h.store.GetRegisteredNodes(r.Context())
		if err == nil {
			for _, rn := range regNodes {
				if rn.NodeName == name {
					addr = rn.Address
					break
				}
			}
		}
	}
	if addr == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "node not found"})
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	url := "http://" + addr + "/v1/health"
	resp, err := client.Get(url)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"node_name": name,
			"online":    false,
			"error":     err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	// Proxy the agent's full health response
	w.Header().Set("Content-Type", "application/json")
	io.Copy(w, resp.Body)
}

// handleBinary serves pre-built binaries for /api/binary/<os>/<arch>.
// Expects files named like "gogitops-linux-amd64", "gogitops-darwin-arm64" in h.binDir.
func (h *Handler) handleBinary(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/binary/"), "/")
	if len(parts) != 2 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "expected /api/binary/<os>/<arch>"})
		return
	}
	goos, goarch := parts[0], parts[1]
	filename := "gogitops-" + goos + "-" + goarch
	path := h.binDir + "/" + filename

	http.ServeFile(w, r, path)
}

// registrationRequest is the payload for POST /api/register.
// Agents send this on startup to announce themselves to the dashboard.
type registrationRequest struct {
	Hostname  string `json:"hostname"`
	Address   string `json:"address"`   // e.g. "10.2.0.102:7780"
	DisplayIP string `json:"display_ip"` // e.g. "10.2.0.102 (lan) / 10.200.0.2 (neb)"
}

// handleRegister accepts POST /api/register from agents.
// The agent tells the dashboard its name and how to reach it.
// The dashboard stores it and will start health-checking it.
func (h *Handler) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON"})
		return
	}

	if req.Hostname == "" || req.Address == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "hostname and address required"})
		return
	}

	// Default port if not specified
	if !strings.Contains(req.Address, ":") {
		req.Address = req.Address + ":7780"
	}

	if req.DisplayIP == "" {
		// Strip port for display
		req.DisplayIP = req.Address
		if idx := strings.LastIndex(req.Address, ":"); idx >= 0 {
			req.DisplayIP = req.Address[:idx]
		}
	}

	if err := h.store.RegisterNode(r.Context(), req.Hostname, req.Address, req.DisplayIP); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	log.Printf("node registered: %s → %s (%s)", req.Hostname, req.Address, req.DisplayIP)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "registered",
		"hostname":  req.Hostname,
		"address":   req.Address,
		"dashboard": h.dashURL,
	})
}
