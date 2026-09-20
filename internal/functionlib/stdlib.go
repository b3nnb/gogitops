// Stdlib, portable part. Every stdlib set is declared here via
// reservedByStdlib + its functions registered in init — this file is the
// single place the two-tier boundary is enforced from.
package functionlib

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// githubAPIBase is the GitHub REST root; a package var so unit tests can
// point it at an httptest server (no YAML surface, no prod behavior change).
var githubAPIBase = "https://api.github.com"

func init() {
	reservedByStdlib("storage") // functions: stdlib_unix.go (disk-free needs statfs)
	reservedByStdlib("net")
	reservedByStdlib("system")

	// net.port-check — can we reach host:port right now?
	Register(Function{
		Name:        "net.port-check",
		Description: "TCP connect check to host:port. Outputs up (bool), latency_ms, and error text when down.",
		Params: []Param{
			{Name: "host", Type: "string", Description: "Hostname or IP (required)"},
			{Name: "port", Type: "int", Description: "TCP port (required)"},
			{Name: "timeout", Type: "duration", Description: "Dial timeout (default 3s)"},
		},
		Run: portCheck,
	})

	// net.http-check — does a URL answer, and does the body say what we
	// expect? The typed replacement for the curl|grep wait-loops recipes
	// kept reimplementing. Down = result (like port-check); require=true
	// turns a failed check into an error so retries/on_failure: fire.
	Register(Function{
		Name:        "net.http-check",
		Description: "HTTP GET check: up (any response counts, any status), status, latency_ms; optional contains= body substring; require=true makes a down/missing-contains check an error instead of a result.",
		Params: []Param{
			{Name: "url", Type: "string", Description: "URL to GET (required)"},
			{Name: "timeout", Type: "duration", Description: "Request timeout (default 5s)"},
			{Name: "contains", Type: "string", Description: "Substring the response body must include"},
			{Name: "require", Type: "bool", Description: "true = no response or missing contains= returns an error (drives retries + on_failure:)"},
		},
		Run: httpCheck,
	})

	// net.github-asset — release asset URL from the GitHub API, decoded as
	// JSON instead of grep -oE over the raw response. Same call sites as
	// the old scrapes: default /releases/latest; walk=N walks the N newest
	// releases for the first asset whose download URL matches.
	Register(Function{
		Name:        "net.github-asset",
		Description: "Release asset download URL from the GitHub API (JSON-parsed). Default: latest release only; walk=N walks the N newest releases, first matching asset wins. found=false when nothing matches.",
		Params: []Param{
			{Name: "repo", Type: "string", Description: "owner/repo (required)"},
			{Name: "match", Type: "string", Description: "Substring matched against each asset's browser_download_url (required)"},
			{Name: "walk", Type: "int", Description: "Walk the N newest releases instead of just /releases/latest"},
			{Name: "timeout", Type: "duration", Description: "API timeout (default 15s)"},
		},
		Run: githubAsset,
	})

	// system.which — command lookup with explicit-path fallbacks. Kills the
	// launchd/cron PATH problem (brew) and the YAML-escaping quoting hell
	// the inline BREW=$(...) dances dragged around. Not-found is a result.
	Register(Function{
		Name:        "system.which",
		Description: "Resolve a command to an executable path: PATH lookup first, then explicit candidates in order (~ expands). found=false + empty path is a result, not an error.",
		Params: []Param{
			{Name: "name", Type: "string", Description: "Command name to look up on PATH (required)"},
			{Name: "candidates", Type: "string", Description: "Comma-separated absolute paths to try when PATH lookup fails; use JSON args ({\"candidates\": \"a,b\"}) since commas split plain args"},
		},
		Run: which,
	})

	// system.info — the runner context, typed, for branching recipes.
	Register(Function{
		Name:        "system.info",
		Description: "Node identity + run context: hostname, os, arch. For when_attr branching without shell probes.",
		Params:      nil,
		Run: func(ctx Context, args map[string]any) (map[string]any, error) {
			return map[string]any{
				"hostname": ctx.Hostname,
				"os":       ctx.OS,
				"arch":     ctx.Arch,
			}, nil
		},
	})
}

func portCheck(ctx Context, args map[string]any) (map[string]any, error) {
	host, _ := args["host"].(string)
	if host == "" {
		return nil, fmt.Errorf("host= required")
	}
	portStr, _ := args["port"].(string)
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("port= must be 1-65535, got %q", portStr)
	}
	timeout := 3 * time.Second
	if t, ok := args["timeout"].(string); ok && t != "" {
		if d, err := time.ParseDuration(t); err == nil {
			timeout = d
		}
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, portStr), timeout)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		// A failed check is a RESULT, not an error — the recipe
		// decides via expect:/assert:/on_failure:.
		return map[string]any{
			"up":         false,
			"latency_ms": latency,
			"error":      err.Error(),
			"math":       fmt.Sprintf("tcp dial %s:%d, timeout %s", host, port, timeout),
		}, nil
	}
	conn.Close()
	return map[string]any{
		"up":         true,
		"latency_ms": latency,
		"math":       fmt.Sprintf("tcp dial %s:%d ok in %dms", host, port, latency),
	}, nil
}

// ── net.http-check ──────────────────────────────────────────────────────────

// parseBoolArg coerces an arg string ("true"/"1"/"yes") to a bool.
func parseBoolArg(v any) bool {
	s, _ := v.(string)
	b, err := strconv.ParseBool(s)
	if err != nil {
		return strings.EqualFold(s, "yes")
	}
	return b
}

func httpCheck(ctx Context, args map[string]any) (map[string]any, error) {
	url, _ := args["url"].(string)
	if url == "" {
		return nil, fmt.Errorf("url= required")
	}
	timeout := 5 * time.Second
	if t, ok := args["timeout"].(string); ok && t != "" {
		if d, err := time.ParseDuration(t); err == nil {
			timeout = d
		}
	}
	contains, _ := args["contains"].(string)
	require := parseBoolArg(args["require"])

	client := &http.Client{Timeout: timeout}
	start := time.Now()
	resp, err := client.Get(url)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		// Mirror the originals: curl failing to get ANY response is the
		// only "down" — a 404 body is still a response (curl -s exit 0).
		out := map[string]any{
			"up":         false,
			"status":     0,
			"latency_ms": latency,
			"matched":    contains == "",
			"error":      err.Error(),
			"math":       fmt.Sprintf("GET %s: no response in %dms (%s)", url, latency, timeout),
		}
		if require {
			return nil, fmt.Errorf("http-check: no response from %s: %w", url, err)
		}
		return out, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	matched := contains == "" || strings.Contains(string(body), contains)
	out := map[string]any{
		"up":         true,
		"status":     resp.StatusCode,
		"latency_ms": latency,
		"matched":    matched,
		"math":       fmt.Sprintf("GET %s → %d in %dms (contains %q: %v)", url, resp.StatusCode, latency, contains, matched),
	}
	// require semantics match the shell originals (curl | grep -q): the
	// check fails on a missing contains= substring, NOT on status codes.
	if require && !matched {
		return nil, fmt.Errorf("http-check: %s answered %d but body lacks %q", url, resp.StatusCode, contains)
	}
	return out, nil
}

// ── net.github-asset ────────────────────────────────────────────────────────

// ghRelease is the slice of the GitHub release JSON the function needs.
type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func githubAsset(ctx Context, args map[string]any) (map[string]any, error) {
	repo, _ := args["repo"].(string)
	match, _ := args["match"].(string)
	if repo == "" || match == "" {
		return nil, fmt.Errorf("repo= and match= required")
	}
	if strings.ContainsRune(repo, ' ') {
		return nil, fmt.Errorf("repo= must be owner/repo, got %q", repo)
	}
	timeout := 15 * time.Second
	if t, ok := args["timeout"].(string); ok && t != "" {
		if d, err := time.ParseDuration(t); err == nil {
			timeout = d
		}
	}
	// Default: /releases/latest (the API's newest non-prerelease — same
	// endpoint the old curl used). walk=N: /releases?per_page=N, newest
	// first, first matching asset wins (Obsidian ships desktop assets
	// days behind the newest tag, so its recipe walks).
	path := "/repos/" + repo + "/releases/latest"
	walked := "latest"
	if w, ok := args["walk"].(string); ok && w != "" {
		n, err := strconv.Atoi(w)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("walk= must be a positive int, got %q", w)
		}
		path = fmt.Sprintf("/repos/%s/releases?per_page=%d", repo, n)
		walked = fmt.Sprintf("%d newest releases", n)
	}

	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("GET", githubAPIBase+path, nil)
	if err != nil {
		return nil, fmt.Errorf("github-asset: bad request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "gogitops-agent") // GitHub 403s UA-less requests
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github-asset: %s unreachable: %w", githubAPIBase, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("github-asset: GET %s → %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var releases []ghRelease
	if walked == "latest" {
		var one ghRelease
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<24)).Decode(&one); err != nil {
			return nil, fmt.Errorf("github-asset: bad JSON from %s: %w", path, err)
		}
		releases = []ghRelease{one}
	} else {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<24)).Decode(&releases); err != nil {
			return nil, fmt.Errorf("github-asset: bad JSON from %s: %w", path, err)
		}
	}
	for _, rel := range releases {
		for _, a := range rel.Assets {
			if strings.Contains(a.BrowserDownloadURL, match) {
				return map[string]any{
					"found": true,
					"url":   a.BrowserDownloadURL,
					"tag":   rel.TagName,
					"math":  fmt.Sprintf("GET %s (%s) — asset %q matches %q", path, walked, a.Name, match),
				}, nil
			}
		}
	}
	return map[string]any{
		"found": false,
		"url":   "",
		"tag":   "",
		"math":  fmt.Sprintf("GET %s (%s) — no asset URL contains %q", path, walked, match),
	}, nil
}

// ── system.which ────────────────────────────────────────────────────────────

func which(ctx Context, args map[string]any) (map[string]any, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("name= required")
	}
	cands, _ := args["candidates"].(string)
	var list []string
	for _, c := range strings.Split(cands, ",") {
		c = strings.TrimSpace(c)
		if c != "" {
			list = append(list, c)
		}
	}
	// PATH first — mirrors `command -v` in the same process env.
	if p, err := exec.LookPath(name); err == nil {
		return map[string]any{
			"found": true,
			"path":  p,
			"source": "path",
			"math":  fmt.Sprintf("LookPath(%s) hit; %d candidates untried", name, len(list)),
		}, nil
	}
	// Then explicit candidates, in order — the launchd/cron PATH gap that
	// forced brew recipes to hardcode /opt/homebrew/bin in the first place.
	for i, c := range list {
		full := expandTilde(c)
		if isExecutableFile(full) {
			return map[string]any{
				"found": true,
				"path":  full,
				"source": fmt.Sprintf("candidate:%d", i+1),
				"math":  fmt.Sprintf("LookPath(%s) miss; candidate %d hit: %s", name, i+1, full),
			}, nil
		}
	}
	// Not found is a RESULT — recipes branch on {{func.found}} or skip
	// via their own when: guards, same as a failed command -v before.
	return map[string]any{
		"found":  false,
		"path":   "",
		"source": "none",
		"math":   fmt.Sprintf("LookPath(%s) miss; %d candidates tried, none executable", name, len(list)),
	}, nil
}

// expandTilde replaces a leading ~/ with the user's home dir.
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(p, "~/"))
		}
	}
	return p
}

// isExecutableFile mirrors `[ -x path ]`: regular file with any exec bit.
func isExecutableFile(p string) bool {
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0111 != 0
}
