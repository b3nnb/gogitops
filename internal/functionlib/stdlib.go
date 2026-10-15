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
	"os/user"
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

	// system.info — the runner context, typed, for branching recipes and
	// user-scoped paths (authorized_keys.d/<user>) without shell probes.
	Register(Function{
		Name:        "system.info",
		Description: "Node identity + run context: hostname, os, arch, user (effective user — os/user lookup, $USER fallback). For when_attr branching and <user>-path addressing without uname/whoami parse-chaining.",
		Params:      nil,
		Run: func(ctx Context, args map[string]any) (map[string]any, error) {
			return map[string]any{
				"hostname": ctx.Hostname,
				"os":       ctx.OS,
				"arch":     ctx.Arch,
				"user":     effectiveUser(),
			}, nil
		},
	})

	// net.source-ip — which local source address would the KERNEL use to
	// reach host:port? A UDP "connect" (no packet is ever sent), the same
	// trick the agent's /v1/health system.ip uses. Typed replacement for
	// the per-OS fallback dances (ip -4 route get | grep -oP 'src \K',
	// hostname -I, ipconfig getifaddr ×3, awk '{print $1}') recipes kept
	// reimplementing. No route is a RESULT (found=false), not an error.
	Register(Function{
		Name:        "net.source-ip",
		Description: "Kernel-chosen local source IP toward host:port (UDP dial, no traffic sent). Outputs found, ip. No route/unresolvable host is a result (found=false), not an error.",
		Params: []Param{
			{Name: "host", Type: "string", Description: "Destination the kernel would route to (required) — e.g. the beacon IP for 'the address the beacon polls back', 1.1.1.1 for the default route"},
			{Name: "port", Type: "int", Description: "UDP target port (default 80 — only affects route selection, nothing is sent)"},
			{Name: "timeout", Type: "duration", Description: "Dial timeout (default 2s)"},
		},
		Run: sourceIP,
	})

	// net.json-get — GET a URL, extract ONE value by dot-path (a.b[0].c).
	// Typed replacement for the curl | grep -oE '"key":[^,]*' | cut -d'"'
	// scrapes over JSON documents — the exact fragile-parse class that was
	// quadruple-quote-escaped in YAML twice over. Down/missing/bad-JSON is
	// a RESULT (found=false, value=""), so expect:/when_attr: decide.
	Register(Function{
		Name:        "net.json-get",
		Description: "GET a JSON document and extract one value by dot-path (hostname, agent.version, peers[0].name). Outputs found, value, status. Unreachable URL / bad JSON / missing key is a result (found=false), not an error.",
		Params: []Param{
			{Name: "url", Type: "string", Description: "URL to GET (required)"},
			{Name: "key", Type: "string", Description: "Dot-path into the JSON: top-level key, a.b nested, list[0] index, items[2].name (required)"},
			{Name: "timeout", Type: "duration", Description: "Request timeout (default 5s)"},
		},
		Run: jsonGet,
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

// ── net.source-ip ───────────────────────────────────────────────────────────

// sourceIP asks the kernel which local source address it would use to
// reach host:port — a UDP DialTimeout, nothing is ever written to the
// socket. Same trick as internal/health's SystemInfo IP (toward 8.8.8.8).
func sourceIP(ctx Context, args map[string]any) (map[string]any, error) {
	host, _ := args["host"].(string)
	if host == "" {
		return nil, fmt.Errorf("host= required")
	}
	port := "80"
	if p, ok := args["port"].(string); ok && p != "" {
		port = p
	}
	timeout := 2 * time.Second
	if t, ok := args["timeout"].(string); ok && t != "" {
		if d, err := time.ParseDuration(t); err == nil {
			timeout = d
		}
	}
	conn, err := net.DialTimeout("udp", net.JoinHostPort(host, port), timeout)
	if err != nil {
		// No route / unresolvable host is a RESULT — the recipe decides.
		return map[string]any{
			"found": false,
			"ip":    "",
			"error": err.Error(),
			"math":  fmt.Sprintf("udp dial %s:%s — no source address: %v", host, port, err),
		}, nil
	}
	defer conn.Close()
	ip := conn.LocalAddr().(*net.UDPAddr).IP.String()
	return map[string]any{
		"found": true,
		"ip":    ip,
		"math":  fmt.Sprintf("udp dial %s:%s → local %s (no packet sent)", host, port, ip),
	}, nil
}

// ── net.json-get ────────────────────────────────────────────────────────────

func jsonGet(ctx Context, args map[string]any) (map[string]any, error) {
	url, _ := args["url"].(string)
	key, _ := args["key"].(string)
	if url == "" || key == "" {
		return nil, fmt.Errorf("url= and key= required")
	}
	timeout := 5 * time.Second
	if t, ok := args["timeout"].(string); ok && t != "" {
		if d, err := time.ParseDuration(t); err == nil {
			timeout = d
		}
	}
	// miss renders every not-found outcome as a RESULT with the same
	// output keys, so expect:/when_attr: see one stable shape.
	miss := func(status int, errText, math string) (map[string]any, error) {
		return map[string]any{
			"found":  false,
			"value":  "",
			"status": status,
			"error":  errText,
			"math":   math,
		}, nil
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return miss(0, err.Error(), fmt.Sprintf("GET %s — no response: %v", url, err))
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<24))
	if readErr != nil {
		return miss(resp.StatusCode, readErr.Error(), fmt.Sprintf("GET %s → %d — body read failed: %v", url, resp.StatusCode, readErr))
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return miss(resp.StatusCode, "bad JSON: "+err.Error(), fmt.Sprintf("GET %s → %d — body is not JSON", url, resp.StatusCode))
	}
	val, ok := jsonLookup(doc, key)
	if !ok {
		return miss(resp.StatusCode, fmt.Sprintf("key %q not found", key), fmt.Sprintf("GET %s → %d — no JSON value at %q", url, resp.StatusCode, key))
	}
	return map[string]any{
		"found":  true,
		"value":  stringifyJSON(val),
		"status": resp.StatusCode,
		"math":   fmt.Sprintf("GET %s → %d — %s = %s", url, resp.StatusCode, key, stringifyJSON(val)),
	}, nil
}

// jsonLookup walks a decoded JSON tree by dot-path with [n] array indexes:
// hostname, agent.version, peers[0].name, list[1][2], [0].name. Keys
// containing a literal '.' are not addressable (documented limitation).
func jsonLookup(root any, path string) (any, bool) {
	cur := root
	for _, seg := range strings.Split(path, ".") {
		// Each segment: name, name[0], name[0][1], or just [0].
		name := seg
		var idxs []int
		for {
			open := strings.Index(name, "[")
			if open < 0 {
				break
			}
			rel := strings.Index(name[open:], "]")
			if rel < 0 {
				return nil, false
			}
			n, err := strconv.Atoi(name[open+1 : open+rel])
			if err != nil {
				return nil, false
			}
			idxs = append(idxs, n)
			name = name[:open] + name[open+rel+1:]
		}
		if name != "" {
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			cur, ok = m[name]
			if !ok {
				return nil, false
			}
		}
		for _, n := range idxs {
			arr, ok := cur.([]any)
			if !ok || n < 0 || n >= len(arr) {
				return nil, false
			}
			cur = arr[n]
		}
	}
	if cur == nil {
		return nil, false
	}
	return cur, true
}

// stringifyJSON renders a decoded JSON value the way it would print:
// strings raw, numbers/bools as-is, objects/arrays as compact JSON.
func stringifyJSON(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprintf("%v", t)
		}
		return string(b)
	}
}

// effectiveUser is whoami, typed: the effective user running the agent or
// recipe. os/user first (getpwuid on the effective uid); $USER fallback
// covers macOS DS-local users under CGO_ENABLED=0 builds (no /etc/passwd
// entry); "" when neither knows — callers branch on the emptiness.
func effectiveUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}
