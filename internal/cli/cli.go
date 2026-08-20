// Package cli provides colorful terminal output for gogitops commands.
// Color palette: purple, teal, bright green — matching Benn's terminal aesthetic.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// ANSI color codes
const (
	reset   = "\033[0m"
	bold    = "\033[1m"
	dim     = "\033[2m"
	italic  = "\033[3m"

	// Benn's palette
	purple      = "\033[38;5;141m" // bright purple
	teal        = "\033[38;5;38m"  // deep teal
	tealBright  = "\033[38;5;51m"  // bright cyan-teal
	green       = "\033[38;5;46m"  // bright green
	greenDim    = "\033[38;5;34m"  // darker green
	red         = "\033[38;5;196m" // bright red
	redDim      = "\033[38;5;124m" // darker red
	yellow      = "\033[38;5;226m" // bright yellow
	yellowDim   = "\033[38;5;178m" // amber
	orange      = "\033[38;5;208m" // orange
	white       = "\033[38;5;255m" // bright white
	grey        = "\033[38;5;245m" // light grey
	greyDark    = "\033[38;5;240m" // dark grey
)

// HealthResponse mirrors the agent's /v1/health JSON structure
type HealthResponse struct {
	Hostname       string            `json:"hostname"`
	AgentVersion   string            `json:"agent_version"`
	UptimeSeconds  int64             `json:"uptime_seconds"`
	NebulaRunning  bool              `json:"nebula_running"`
	NebulaIP       string            `json:"nebula_ip"`
	Labels         []string          `json:"labels"`
	Services       map[string]string `json:"services"`
	DiskWarns      []string          `json:"disk_warns"`
	PeersReachable []string          `json:"peers_reachable"`
	PeersUnreach   []string          `json:"peers_unreachable"`
	System         SystemInfo        `json:"system"`
}

type SystemInfo struct {
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	IP      string `json:"ip"`
	HostID  string `json:"host_id"`
}

// FetchHealth queries an agent's health endpoint
func FetchHealth(addr string) (*HealthResponse, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + addr + "/v1/health")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var h HealthResponse
	if err := json.Unmarshal(body, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// ── Helpers ──────────────────────────────────────────────────────────────

func colorForStatus(status string) string {
	switch status {
	case "running":
		return green
	case "stopped", "failed", "exited", "down":
		return red
	case "degraded":
		return yellow
	case "not_configured":
		return grey
	default:
		return grey
	}
}

func iconForStatus(status string) string {
	switch status {
	case "running":
		return "●"
	case "stopped", "failed", "exited", "down":
		return "○"
	case "degraded":
		return "◐"
	case "not_configured":
		return "—"
	default:
		return "?"
	}
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

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

func separator(width int) string {
	return dim + strings.Repeat("─", width) + reset
}

// ── Banner ───────────────────────────────────────────────────────────────

// Banner prints a colorful gogitops header
func Banner() {
	banner := `  ____        ____ _ _    ___            
 / ___| ___  / ___(_) |_ / _ \ _ __  ___ 
| |  _ / _ \| |  _| | __| | | | '_ \/ __|
| |_| | (_) | |_| | | |_| |_| | |_) \__ \
 \____|\___/ \____|_|\__|\___/| .__/|___/
                              |_|        `
	fmt.Printf("%s%s%s\n", tealBright, banner, reset)
}

// ── Commands ─────────────────────────────────────────────────────────────

// PrintStatus renders a colorful, detailed status view for one agent
func PrintStatus(h *HealthResponse, addr string) {
	Banner()
	fmt.Println()

	// Header line
	up, down := 0, 0
	for _, v := range h.Services {
		if v == "running" {
			up++
		} else {
			down++
		}
	}
	healthIcon := green + "●" + reset
	if down > 0 {
		healthIcon = yellow + "◐" + reset
	}
	if up == 0 && down > 0 {
		healthIcon = red + "○" + reset
	}

	fmt.Printf("  %s%s%s %s%s%s  %s%s%s\n",
		bold, purple, h.Hostname, reset,
		bold, h.AgentVersion, reset,
		dim, formatUptime(h.UptimeSeconds))
	fmt.Printf("  %s %s%d/%d services%s\n",
		healthIcon, grey, up, up+down, reset)
	fmt.Println()

	// System info
	fmt.Printf("  %s╭─ System ──────────────────────%s\n", greyDark, reset)
	fmt.Printf("  %s│%s OS       %s%s%s\n", greyDark, reset, white, h.System.OS, reset)
	fmt.Printf("  %s│%s Arch     %s%s%s\n", greyDark, reset, white, h.System.Arch, reset)
	fmt.Printf("  %s│%s IP       %s%s%s\n", greyDark, reset, teal, h.System.IP, reset)
	fmt.Printf("  %s│%s Host ID  %s%s%s\n", greyDark, reset, dim, h.System.HostID, reset)
	if h.NebulaRunning {
		fmt.Printf("  %s│%s Nebula   %s● running%s\n", greyDark, reset, green, reset)
	} else {
		fmt.Printf("  %s│%s Nebula   %s○ down%s\n", greyDark, reset, red, reset)
	}
	fmt.Printf("  %s╰──────────────────────────────%s\n", greyDark, reset)
	fmt.Println()

	// Labels / tags
	if len(h.Labels) > 0 {
		fmt.Printf("  %s╭─ Tags ──────────────────────%s\n", greyDark, reset)
		tags := []string{}
		stacks := []string{}
		for _, l := range h.Labels {
			if strings.HasPrefix(l, "stack:") {
				stacks = append(stacks, l[6:])
			} else {
				tags = append(tags, l)
			}
		}
		if len(tags) > 0 {
			fmt.Printf("  %s│%s Tags     %s%s%s\n", greyDark, reset, teal, strings.Join(tags, " "), reset)
		}
		if len(stacks) > 0 {
			fmt.Printf("  %s│%s Stacks   %s%s%s\n", greyDark, reset, yellowDim, strings.Join(stacks, " "), reset)
		}
		fmt.Printf("  %s╰──────────────────────────────%s\n", greyDark, reset)
		fmt.Println()
	}

	// Services
	fmt.Printf("  %s╭─ Services ───────────────────%s\n", greyDark, reset)
	names := make([]string, 0, len(h.Services))
	for k := range h.Services {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		status := h.Services[name]
		col := colorForStatus(status)
		icon := iconForStatus(status)
		fmt.Printf("  %s│%s %s%s%s  %s%s%s\n",
			greyDark, reset,
			col, icon, reset,
			grey, padRight(name, 22), reset)
		if status != "running" {
			fmt.Printf("  %s   %s%s%s\n", greyDark, redDim, status, reset)
		}
	}
	fmt.Printf("  %s╰──────────────────────────────%s\n", greyDark, reset)
	fmt.Println()

	// Peers
	if len(h.PeersReachable) > 0 || len(h.PeersUnreach) > 0 {
		fmt.Printf("  %s╭─ Peers ──────────────────────%s\n", greyDark, reset)
		for _, p := range h.PeersReachable {
			fmt.Printf("  %s│%s %s●%s  %s%s%s\n", greyDark, reset, green, reset, grey, p, reset)
		}
		for _, p := range h.PeersUnreach {
			fmt.Printf("  %s│%s %s○%s  %s%s%s\n", greyDark, reset, red, reset, greyDark, p, reset)
		}
		fmt.Printf("  %s╰──────────────────────────────%s\n", greyDark, reset)
		fmt.Println()
	}

	// Disk warnings
	if len(h.DiskWarns) > 0 {
		fmt.Printf("  %s╭─ Disk Warnings ──────────────%s\n", greyDark, reset)
		for _, w := range h.DiskWarns {
			fmt.Printf("  %s│%s %s⚠%s  %s%s%s\n", greyDark, reset, yellow, reset, yellowDim, w, reset)
		}
		fmt.Printf("  %s╰──────────────────────────────%s\n", greyDark, reset)
		fmt.Println()
	}
}

// PrintCompact renders a one-line summary (for fleet view)
func PrintCompact(h *HealthResponse) {
	up, down := 0, 0
	for _, v := range h.Services {
		if v == "running" {
			up++
		} else {
			down++
		}
	}
	icon := green + "●" + reset
	svcColor := green
	if down > 0 {
		icon = yellow + "◐" + reset
		svcColor = yellowDim
	}
	if up == 0 && down > 0 {
		icon = red + "○" + reset
		svcColor = redDim
	}

	nebIcon := ""
	if h.NebulaRunning {
		nebIcon = tealBright + "⬡" + reset
	} else {
		nebIcon = greyDark + "⬡" + reset
	}

	fmt.Printf("  %s %s%s%s  %s%d/%d%s  %s  %s%s%s  %s%s%s\n",
		icon,
		bold, padRight(h.Hostname, 14), reset,
		svcColor, up, up+down, reset,
		nebIcon,
		dim, padRight(h.System.OS+"/"+h.System.Arch, 12), reset,
		dim, formatUptime(h.UptimeSeconds), reset)
}

// PrintCompactHeader prints the column header for fleet view
func PrintCompactHeader() {
	fmt.Printf("  %s%-4s  %-14s  %-8s  %-3s  %-12s  %s%s\n",
		greyDark, "", "NODE", "SVC", "NEB", "PLATFORM", "UPTIME", reset)
	fmt.Printf("  %s\n", separator(72))
}

// PrintError prints a colorful error message
func PrintError(msg string) {
	fmt.Fprintf(os.Stderr, "  %s✖%s  %s%s%s\n", red, reset, redDim, msg, reset)
}

// PrintInfo prints a colorful info message
func PrintInfo(msg string) {
	fmt.Printf("  %sℹ%s  %s%s%s\n", teal, reset, grey, msg, reset)
}

// PrintSuccess prints a colorful success message
func PrintSuccess(msg string) {
	fmt.Printf("  %s✓%s  %s%s%s\n", green, reset, greenDim, msg, reset)
}
