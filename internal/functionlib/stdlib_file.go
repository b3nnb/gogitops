// Stdlib, file-state part: typed probes and idempotent package-channel
// installs for state recipes kept reimplementing in shell.
//
// The whole point is STATE IN THE OUTPUTS, NOT STDOUT: a file probe is
// not a grep pipe. Missing file / unreachable URL / not-sudoable are
// results, not errors — the recipe branches on {{func.*}} or lets
// expect:/when_attr:/require= decide.
package functionlib

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

func init() {
	// system.file-state — the typed probe behind "is the file/URL there
	// and does it contain X?". recipes kept writing
	// `curl -s URL | grep -q "\"hardware\""` — five quoting layers deep.
	Register(Function{
		Name:        "system.file-state",
		Description: "File/URL state with typed outputs: exists, matched (contains=), size, dir, exec, and version= (highest vX.Y.Z token in the content when version=true). Missing file/unreachable URL is a result, not an error.",
		Params: []Param{
			{Name: "path", Type: "string", Description: "Local path (~ expands) or http(s):// URL to GET"},
			{Name: "contains", Type: "string", Description: "Substring the content must include — sets matched=true/false"},
			{Name: "version", Type: "bool", Description: "Extract the highest vX.Y.Z(-suffix) token from the content into version="},
			{Name: "timeout", Type: "duration", Description: "URL fetch timeout (default 5s)"},
		},
		Run: fileState,
	})

	// system.apt-install — idempotent apt install with sudo-mode
	// resolution and a post-install binary probe. Typed replacement for
	// the per-step `sudo -n … || pkexec …` / DEBIAN_FRONTEND dances and
	// the command-resolution hang class. Bare package tokens whose name
	// also appears in verify= are ORDERED ALTERNATES (chromium across
	// RPi OS releases): the first apt-resolvable one installs, the rest
	// are skipped; when any alternate command already resolves, the
	// whole group is converged.
	Register(Function{
		Name:        "system.apt-install",
		Description: "Idempotent apt install (bare name = install if missing; name=1.2.3 = exact pin; name=1.2.* = glob). Bare tokens listed in verify= are ordered alternates — first apt-resolvable wins, and the group is converged when any verify command already resolves. Refreshes the index only when installing, resolves the sudo mode (sudo/pkexec) without hanging. not-sudoable / not-apt are results.",
		Params: []Param{
			{Name: "packages", Type: "string", Description: "Comma-separated tokens: htop, nginx=1.18.0-6ubuntu14.4, chromium=1.2.* (required)"},
			{Name: "verify", Type: "string", Description: "Command name(s) that must resolve after install — comma alternates pass when ANY resolves; bare packages tokens named here become ordered alternates"},
			{Name: "update", Type: "bool", Description: "true = always run apt-get update (default: only when something needs installing)"},
		},
		Run: aptInstall,
	})

	// system.flatpak — one typed op for the whole detect-channel →
	// remote-add → install flow. Replaces the when-guard lattice
	// (sudo -n true && ! installed && command -v flatpak …) with a
	// single decision made in Go.
	Register(Function{
		Name:        "system.flatpak",
		Description: "Idempotent flatpak install with automatic channel resolution: system channel when passwordless sudo exists and the app is missing, user channel otherwise; flathub remote ensured per channel. Outputs channel=already|system|user|no-flatpak|not-linux, installed, error. require=true turns not-installed-after-step into an error (drives retries).",
		Params: []Param{
			{Name: "app", Type: "string", Description: "Flathub app id, e.g. md.obsidian.Obsidian (required)"},
			{Name: "require", Type: "bool", Description: "true = app still missing after the step is an error instead of a result"},
		},
		Run: flatpakInstall,
	})
}

// ── system.file-state ────────────────────────────────────────────────────────

func fileState(ctx Context, args map[string]any) (map[string]any, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("path= required")
	}
	contains, _ := args["contains"].(string)
	wantVersion := parseBoolArg(args["version"])

	out := map[string]any{
		"exists":  false,
		"matched": contains == "",
		"status":  0,
		"size":    0,
		"dir":     false,
		"exec":    false,
		"version": "",
		"error":   "",
	}

	var content string
	switch {
	case strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://"):
		timeout := 5 * time.Second
		if t, ok := args["timeout"].(string); ok && t != "" {
			if d, err := time.ParseDuration(t); err == nil {
				timeout = d
			}
		}
		client := &http.Client{Timeout: timeout}
		start := time.Now()
		resp, err := client.Get(path)
		latency := time.Since(start).Milliseconds()
		if err != nil {
			out["error"] = errType(err)
			out["math"] = fmt.Sprintf("GET %s: %s in %dms", path, errType(err), latency)
			return out, nil
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		content = string(body)
		out["status"] = resp.StatusCode
		out["exists"] = resp.StatusCode >= 200 && resp.StatusCode < 300
		out["size"] = len(content)
		out["math"] = fmt.Sprintf("GET %s → %d, %d bytes in %dms", path, resp.StatusCode, len(content), latency)
	default:
		full := expandTilde(path)
		info, err := os.Stat(full)
		if err != nil {
			out["math"] = fmt.Sprintf("stat %s: missing", full)
			return out, nil
		}
		out["exists"] = true
		out["size"] = info.Size()
		out["dir"] = info.IsDir()
		out["exec"] = info.Mode().Perm()&0111 != 0
		if info.Mode().IsRegular() && info.Size() < 1<<20 {
			if b, rerr := os.ReadFile(full); rerr == nil {
				content = string(b)
			}
		}
		out["math"] = fmt.Sprintf("stat %s: %d bytes, dir=%v exec=%v", full, info.Size(), info.IsDir(), out["exec"])
	}

	if contains != "" {
		out["matched"] = strings.Contains(content, contains)
	}
	if wantVersion {
		out["version"] = extractVersion(content)
	}
	return out, nil
}

// extractVersion returns the highest vX.Y.Z(-suffix) token in s (""
// when none). A token is v/V-prefixed, or unprefixed-but-dotted and
// not preceded by an alphanumeric (kills "sha256", "abc123", hex
// hashes) and not IPv4-shaped (kills "10.0.0.213"). Bare digit runs
// (ports, counts) never qualify. Highest = longest token wins; equal
// length keeps the leftmost (earliest-scanned) occurrence.
func extractVersion(s string) string {
	best := ""
	start := -1
	for i := 0; i <= len(s); i++ {
		inTok := i < len(s) && (s[i] == 'v' || s[i] == 'V' || s[i] >= '0' && s[i] <= '9' || s[i] == '.' || s[i] == '-')
		if inTok {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			tok := s[start:i]
			for len(tok) > 1 && (tok[len(tok)-1] == '.' || tok[len(tok)-1] == '-') {
				tok = tok[:len(tok)-1]
				i--
			}
			var prev byte
			if start > 0 {
				prev = s[start-1]
			}
			if versionTokenClass(tok, prev) && len(tok) > len(best) {
				best = tok
			}
			start = -1
		}
	}
	return normalizeVersion(best)
}

// versionTokenClass decides whether a scanned span counts as a version
// token. prev is the byte before the token (0 = string start).
func versionTokenClass(tok string, prev byte) bool {
	if tok == "" || !isVersionToken(tok) {
		return false
	}
	if tok[0] == 'v' || tok[0] == 'V' {
		return true
	}
	// Unprefixed: must be dotted, not preceded by alnum (word-internal
	// digit runs), and not IPv4-shaped.
	if !strings.Contains(tok, ".") {
		return false
	}
	if prev >= 'a' && prev <= 'z' || prev >= 'A' && prev <= 'Z' || prev >= '0' && prev <= '9' {
		return false
	}
	return !isIPv4Shaped(tok)
}

// isIPv4Shaped: exactly 4 dot-separated parts, each 1-3 pure digits.
func isIPv4Shaped(tok string) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if len(p) < 1 || len(p) > 3 {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// isVersionToken: optional v/V, then digit-separated numerics with an
// optional alnum -suffix ("10", "v1.2.3", "0.7.34-1" yes; "a1b2" no).
func isVersionToken(tok string) bool {
	if tok == "" {
		return false
	}
	i := 0
	if tok[0] == 'v' || tok[0] == 'V' {
		i++
	}
	digits := 0
	for ; i < len(tok); i++ {
		c := tok[i]
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == '.':
			if digits == 0 || i+1 >= len(tok) || tok[i+1] < '0' || tok[i+1] > '9' {
				return false
			}
		case c == '-':
			if digits == 0 {
				return false
			}
			for _, r := range tok[i+1:] {
				if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.') {
					return false
				}
			}
			return true
		default:
			return false
		}
	}
	return digits > 0
}

// normalizeVersion lowercases a leading V.
func normalizeVersion(tok string) string {
	if strings.HasPrefix(tok, "V") {
		return "v" + tok[1:]
	}
	return tok
}

// errType shortens transport errors to their class for the math line.
func errType(err error) string {
	if err == nil {
		return "ok"
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "timeout"):
		return "timeout"
	case strings.Contains(s, "connection refused"):
		return "connection-refused"
	case strings.Contains(s, "no such host"):
		return "no-such-host"
	default:
		return "transport-error"
	}
}

// ── system.apt-install ───────────────────────────────────────────────────────

// sudoModeProbe is a package var so tests can stub the privilege-mode
// resolution (the real one shells out to `sudo -n true`, which unit
// tests must not depend on).
var sudoModeProbe = defaultSudoMode

// defaultSudoMode resolves how privileged commands run on this node,
// WITHOUT executing a real payload through the privilege path:
// passwordless sudo (`sudo -n true`), pkexec present, or neither.
func defaultSudoMode() string {
	if err := exec.Command("sudo", "-n", "true").Run(); err == nil {
		return "sudo"
	}
	if p, e := exec.LookPath("pkexec"); e == nil && p != "" {
		return "pkexec"
	}
	return "none"
}

func aptInstall(ctx Context, args map[string]any) (map[string]any, error) {
	pkgsRaw, _ := args["packages"].(string)
	if pkgsRaw == "" {
		return nil, fmt.Errorf("packages= required")
	}
	var toks []string
	for _, t := range strings.Split(pkgsRaw, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			toks = append(toks, t)
		}
	}
	if len(toks) == 0 {
		return nil, fmt.Errorf("packages= empty")
	}
	verify, _ := args["verify"].(string)
	var verifyNames []string
	for _, v := range strings.Split(verify, ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			verifyNames = append(verifyNames, v)
		}
	}

	// Idempotency probe FIRST — reading dpkg state needs no sudo.
	var install []string
	skipped := 0
	inVerify := map[string]bool{}
	for _, v := range verifyNames {
		inVerify[v] = true
	}
	for _, tok := range toks {
		name := tok
		want := ""
		if i := strings.Index(tok, "="); i >= 0 {
			name, want = tok[:i], tok[i+1:]
		}
		if name == "" {
			continue
		}
		// Ordered alternates: a bare token whose name appears in
		// verify= converges when ANY of the verify names is already
		// resolvable, and installs only the first apt-resolvable one.
		if want == "" && inVerify[name] && len(verifyNames) > 1 {
			if verifyOK(verifyNames) {
				skipped++
				continue
			}
			install = append(install, firstAptCandidate(name, verifyNames))
			continue
		}
		ver := dpkgVersion(name)
		if ver == "" {
			install = append(install, tok)
			continue
		}
		if want == "" || want == ver || globMatch(want, ver) {
			skipped++
			continue
		}
		install = append(install, tok)
	}

	if len(install) == 0 {
		return map[string]any{
			"installed": false, "skipped": skipped, "mode": "converged",
			"verify": verifyOK(verifyNames), "error": "",
			"math": "all packages already at desired versions",
		}, nil
	}

	// Platform first (a non-Debian node has no apt regardless of sudo),
	// then privilege.
	if _, err := exec.LookPath("apt-get"); err != nil {
		return map[string]any{
			"installed": false, "skipped": skipped, "mode": "not-apt",
			"verify": false, "error": "apt-get not found",
			"math": "not a Debian-family node",
		}, nil
	}
	mode := sudoModeProbe()
	if mode == "none" {
		return map[string]any{
			"installed": false, "skipped": skipped, "mode": "not-sudoable",
			"verify": false, "error": "no passwordless sudo and no pkexec",
			"math": "apt needs root; none available",
		}, nil
	}

	runPriv := func(argv ...string) *exec.Cmd {
		if mode == "sudo" {
			return exec.Command("sudo", append([]string{"-n"}, argv...)...)
		}
		return exec.Command("pkexec", argv...)
	}
	if out, err := runPriv("apt-get", "update", "-qq").CombinedOutput(); err != nil {
		return map[string]any{
			"installed": false, "skipped": skipped, "mode": mode,
			"verify": false, "error": clipErr(out),
			"math": "apt-get update failed",
		}, nil
	}
	pin := false
	for _, t := range install {
		if strings.Contains(t, "=") {
			pin = true
		}
	}
	argv := []string{"apt-get", "install", "-y", "-q"}
	if pin {
		argv = append(argv, "--allow-downgrades")
	}
	argv = append(argv, install...)
	iCmd := runPriv(argv...)
	iCmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	if out, err := iCmd.CombinedOutput(); err != nil {
		return map[string]any{
			"installed": false, "skipped": skipped, "mode": mode,
			"verify": false, "error": clipErr(out),
			"math": "apt-get install failed",
		}, nil
	}
	math := fmt.Sprintf("apt installed %d pkg(s) via %s", len(install), mode)
	if !verifyOK(verifyNames) {
		return map[string]any{
			"installed": false, "skipped": skipped, "mode": mode,
			"verify": false,
			"error":  "post-install verify failed: " + strings.Join(verifyNames, "/") + " not resolvable",
			"math":   math + " — verify failed",
		}, nil
	}
	return map[string]any{
		"installed": true, "skipped": skipped, "mode": mode,
		"verify": true, "error": "",
		"math": math + " — verify ok",
	}, nil
}

// dpkgVersion returns the installed version of name ("" when absent or
// not fully installed — deinstall state must not satisfy idempotency).
func dpkgVersion(name string) string {
	out, err := exec.Command("dpkg-query", "-W", "-f=${Status} ${Version}", name).Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) >= 4 && fields[0] == "install" && fields[len(fields)-2] == "installed" {
		return fields[len(fields)-1]
	}
	return ""
}

// globMatch implements the name=1.2.* glob (prefix + trailing *).
func globMatch(pattern, ver string) bool {
	if !strings.HasSuffix(pattern, "*") {
		return false
	}
	return strings.HasPrefix(ver, strings.TrimSuffix(pattern, "*"))
}

// verifyOK reports whether any of the alternates resolve on PATH
// (always true when nothing was requested).
func verifyOK(names []string) bool {
	for _, n := range names {
		if _, err := exec.LookPath(n); err == nil {
			return true
		}
	}
	return len(names) == 0
}

// firstAptCandidate picks the first name after `name` (inclusive) that
// apt actually offers — `apt-cache show <pkg> >/dev/null` — falling
// back to name itself when the cache lookup is inconclusive.
func firstAptCandidate(name string, verifyNames []string) string {
	start := 0
	for i, n := range verifyNames {
		if n == name {
			start = i
			break
		}
	}
	for _, n := range verifyNames[start:] {
		if err := exec.Command("apt-cache", "show", n).Run(); err == nil {
			return n
		}
	}
	return name
}

// clipErr trims command output to a single short line for the error field.
func clipErr(out []byte) string {
	s := strings.Join(strings.Fields(string(out)), " ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// ── system.flatpak ───────────────────────────────────────────────────────────

func flatpakInstall(ctx Context, args map[string]any) (map[string]any, error) {
	app, _ := args["app"].(string)
	if app == "" {
		return nil, fmt.Errorf("app= required")
	}
	require := parseBoolArg(args["require"])

	if ctx.OS != "linux" {
		out := map[string]any{
			"channel": "not-linux", "installed": false, "error": "",
			"math": "flatpak is linux-only",
		}
		if require {
			return nil, fmt.Errorf("flatpak: not linux (%s)", ctx.OS)
		}
		return out, nil
	}
	if _, err := exec.LookPath("flatpak"); err != nil {
		out := map[string]any{
			"channel": "no-flatpak", "installed": false,
			"error": "flatpak binary not found",
			"math":  "flatpak not installed on node",
		}
		if require {
			return nil, fmt.Errorf("flatpak: flatpak binary not found")
		}
		return out, nil
	}

	installed := flatpakInstalled(app, false) || flatpakInstalled(app, true)
	channel := ""
	switch {
	case installed:
		channel = "already"
	case sudoModeProbe() == "sudo":
		channel = "system"
	default:
		channel = "user"
	}

	if !installed {
		remote := "https://dl.flathub.org/repo/flathub.flatpakrepo"
		var rcmd *exec.Cmd
		if channel == "system" {
			rcmd = exec.Command("sudo", "-n", "flatpak", "remote-add", "--if-not-exists", "flathub", remote)
		} else {
			rcmd = exec.Command("flatpak", "--user", "remote-add", "--if-not-exists", "flathub", remote)
		}
		if out, err := rcmd.CombinedOutput(); err != nil {
			res := map[string]any{
				"channel": channel, "installed": false,
				"error": clipErr(out), "math": "flathub remote-add failed (" + channel + ")",
			}
			if require {
				return nil, fmt.Errorf("flatpak: remote-add failed: %s", clipErr(out))
			}
			return res, nil
		}
		var icmd *exec.Cmd
		if channel == "system" {
			icmd = exec.Command("sudo", "-n", "flatpak", "install", "-y", "--noninteractive", "flathub", app)
		} else {
			icmd = exec.Command("flatpak", "--user", "install", "-y", "--noninteractive", "flathub", app)
		}
		if out, err := icmd.CombinedOutput(); err != nil {
			res := map[string]any{
				"channel": channel, "installed": false,
				"error": clipErr(out), "math": "flatpak install failed (" + channel + ")",
			}
			if require {
				return nil, fmt.Errorf("flatpak: install failed: %s", clipErr(out))
			}
			return res, nil
		}
		installed = flatpakInstalled(app, channel == "user")
	}

	out := map[string]any{
		"channel":   channel,
		"installed": installed,
		"error":     "",
		"math":      fmt.Sprintf("flatpak %s via %s channel, installed=%v", app, channel, installed),
	}
	if require && !installed {
		return nil, fmt.Errorf("flatpak: %s not installed after step (channel %s)", app, channel)
	}
	return out, nil
}

// flatpakInstalled greps the flatpak list output for the app id, scoped
// to the user installation when user=true, else system+user default view.
func flatpakInstalled(app string, user bool) bool {
	cmd := exec.Command("flatpak", "list", "--app")
	if user {
		cmd = exec.Command("flatpak", "--user", "list", "--app")
	}
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), app)
}
