// Package dashboard provides the HTTP handler for the GoGitOps status dashboard.
package dashboard

import (
	"context"
	"encoding/json"
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
	Healthy      bool    `json:"healthy"`
	ServicesUp   int     `json:"services_up"`
	ServicesTotal int    `json:"services_total"`
	Version      string  `json:"version"`
	ResponseMs   int     `json:"response_time_ms"`
	Error        string  `json:"error,omitempty"`
	CheckedAt    string  `json:"checked_at"`
	Uptime24h    float64 `json:"uptime_24h_pct"`
}

// Handler serves the dashboard HTML and the /api/status JSON endpoint.
type Handler struct {
	store *Store
	nodes []NodeConfig
	mu    sync.Mutex
	cache []liveResult // last live check results for /api/status
}

// NewHandler creates a new dashboard handler.
func NewHandler(store *Store, nodes []NodeConfig) *Handler {
	return &Handler{
		store: store,
		nodes: nodes,
	}
}

// ServeHTTP routes requests to either the dashboard page or the API endpoint.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/status":
		h.handleAPI(w, r)
	case "/", "/index.html":
		h.handleDashboard(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleAPI returns JSON status for all nodes.
func (h *Handler) handleAPI(w http.ResponseWriter, r *http.Request) {
	results := h.checkAllNodes(r.Context())

	statuses := make([]apiNodeStatus, 0, len(results))
	for _, res := range results {
		uptime, _ := h.store.Get24hSummary(r.Context(), res.NodeName)
		s := apiNodeStatus{
			NodeName:     res.NodeName,
			DisplayIP:    res.DisplayIP,
			Healthy:      res.Healthy,
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
	results := h.checkAllNodes(r.Context())

	// Build template data
	type row struct {
		NodeName    string
		DisplayIP   string
		Status      string // "healthy", "degraded", "down"
		StatusEmoji string
		Services    string // "12/12"
		Version     string
		LastCheckin string // relative time
		Uptime24h   string // "99.8%"
		ResponseMs  int
	}

	rows := make([]row, 0, len(results))
	for _, res := range results {
		rw := row{
			NodeName:   res.NodeName,
			DisplayIP:  res.DisplayIP,
			Version:    res.Version,
			ResponseMs: res.ResponseMs,
			Services:   formatServices(res.ServicesUp, res.ServicesTotal),
			LastCheckin: formatRelTime(res.CheckedAt),
		}

		// Determine status
		if res.Error != "" {
			rw.Status = "down"
			rw.StatusEmoji = "🔴"
			// Check if there was a recent stale record
			if time.Since(res.CheckedAt) < 15*time.Minute {
				rw.Status = "degraded"
				rw.StatusEmoji = "🟡"
			}
		} else {
			rw.Status = "healthy"
			rw.StatusEmoji = "🟢"
			if res.ServicesUp < res.ServicesTotal {
				rw.Status = "degraded"
				rw.StatusEmoji = "🟡"
			}
		}

		uptime, _ := h.store.Get24hSummary(r.Context(), res.NodeName)
		rw.Uptime24h = formatUptime(uptime)

		rows = append(rows, rw)
	}

	data := map[string]interface{}{
		"Rows":  rows,
		"Empty": len(h.nodes) == 0,
		"Now":   time.Now().Format("15:04:05 MST"),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTmpl.Execute(w, data); err != nil {
		log.Printf("dashboard template error: %v", err)
	}
}

// checkAllNodes performs live health checks against all configured nodes.
func (h *Handler) checkAllNodes(ctx context.Context) []liveResult {
	var wg sync.WaitGroup
	results := make([]liveResult, len(h.nodes))

	for i, nc := range h.nodes {
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

	return results
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
