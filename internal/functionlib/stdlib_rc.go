// Stdlib: shell-rc + login-integration functions. Everything here is
// exec/file-based and portable (linux + darwin paths inline). Registered
// from stdlib.go's init — this file only implements the Run bodies.
package functionlib

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// execCommand is exec.Command spelled out for the arming paths.
func execCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// ── system.rc-line ──────────────────────────────────────────────────────────
//
// The fleet's most-repeated recipe primitive, typed: "append this line to
// this rc file unless it's already there". Was ~9 printf-escape copies
// across recipes (starship ×7, new-node ×1, …) — each one a quoting-landmine
// (`printf '\neval "$(starship init bash)"\n'`), each silently wrong the
// moment a quote or backslash moved. line= is printf-INTERPRETED here
// (exactly the escapes the originals relied on: \n \t \\) so recipes pass
// the same content with none of the shell escaping.

func init() {
	Register(Function{
		Name: "system.rc-line",
		Description: "Ensure a line exists in a shell rc file: append when the marker substring is missing, no-op when present (state=present|appended). line= is printf-style (\\n newline, \\t tab, \\\\ backslash — all else literal).",
		Params: []Param{
			{Name: "file", Type: "string", Description: "Rc file path (~ expands; created on append, like >>)"},
			{Name: "marker", Type: "string", Description: "Substring that means \"already installed\""},
			{Name: "line", Type: "string", Description: "Content to append when marker is missing (printf-style escapes, use \\n for newlines)"},
		},
		Run: rcLine,
	})
}

func rcLine(ctx Context, args map[string]any) (map[string]any, error) {
	file, _ := args["file"].(string)
	marker, _ := args["marker"].(string)
	line, _ := args["line"].(string)
	if file == "" || marker == "" || line == "" {
		return nil, fmt.Errorf("file=, marker= and line= are required")
	}
	path := expandTilde(file)
	content := ""
	if data, err := os.ReadFile(path); err == nil {
		content = string(data)
	}
	if strings.Contains(content, marker) {
		return map[string]any{
			"state":  "present",
			"path":   path,
			"marker": marker,
			"math":   fmt.Sprintf("%s already contains %q — no append", path, marker),
		}, nil
	}
	data := rcInterpret(line)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("rc-line: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("rc-line: %v", err)
	}
	defer f.Close()
	if _, err := f.Write([]byte(data)); err != nil {
		return nil, fmt.Errorf("rc-line: %v", err)
	}
	return map[string]any{
		"state":  "appended",
		"path":   path,
		"marker": marker,
		"math":   fmt.Sprintf("%s lacked %q — appended %d bytes", path, marker, len(data)),
	}, nil
}

// rcInterpret expands the printf-style escapes the original recipe commands
// relied on (\n, \t, \\); unknown escapes stay literal. Single pass, so
// `\\n` is a literal backslash followed by n — same as bash printf.
func rcInterpret(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case 't':
				b.WriteByte('\t')
				i++
				continue
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ── system.rc-export ────────────────────────────────────────────────────────
//
// The upsert cousin of rc-line: make sure a shell rc file exports VAR with
// the given value — rewriting an existing export line in place (the gcr
// ssh-agent socket switch needs this: appending a second export would leave
// the hijacker's line last-in wins... depending on shell, i.e. never
// deterministic). Was a quadruple-escaped `sed -i "s|export VAR=.*|…|"`.

func init() {
	Register(Function{
		Name: "system.rc-export",
		Description: "Upsert `export VAR=\"value\"` in a shell rc file: every existing `export VAR=` line is rewritten in place, otherwise the line is appended with an optional comment (state=updated|appended).",
		Params: []Param{
			{Name: "file", Type: "string", Description: "Rc file path (~ expands; created on append, like >>)"},
			{Name: "var", Type: "string", Description: "Variable name (e.g. SSH_AUTH_SOCK)"},
			{Name: "value", Type: "string", Description: "Value — written double-quoted, never shell-expanded"},
			{Name: "comment", Type: "string", Description: "Optional trailing `  # comment`"},
		},
		Run: rcExport,
	})
}

func rcExport(ctx Context, args map[string]any) (map[string]any, error) {
	file, _ := args["file"].(string)
	varName, _ := args["var"].(string)
	value, _ := args["value"].(string)
	comment, _ := args["comment"].(string)
	if file == "" || varName == "" || value == "" {
		return nil, fmt.Errorf("file=, var= and value= are required")
	}
	path := expandTilde(file)
	prefix := "export " + varName + "="
	newLine := prefix + `"` + value + `"`
	if comment != "" {
		newLine += "  # " + comment
	}
	var mode os.FileMode = 0o644
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	data, _ := os.ReadFile(path)
	lines := strings.Split(string(data), "\n")
	updated := false
	for i, ln := range lines {
		if idx := strings.Index(ln, prefix); idx >= 0 {
			// Replicate `s|export VAR=.*|NEW|`: keep anything before the
			// match, replace the rest of the line.
			lines[i] = ln[:idx] + newLine
			updated = true
		}
	}
	if updated {
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), mode); err != nil {
			return nil, fmt.Errorf("rc-export: %v", err)
		}
		return map[string]any{
			"state": "updated",
			"path":  path,
			"var":   varName,
			"math":  fmt.Sprintf("%s: rewrote existing %s line(s) in place", path, varName),
		}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("rc-export: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, mode)
	if err != nil {
		return nil, fmt.Errorf("rc-export: %v", err)
	}
	defer f.Close()
	// Same bytes the originals' `printf '\nexport …\n' >>` produced: the
	// leading newline terminates a no-trailing-newline file, doubles as a
	// blank separator when the file is newline-clean.
	if _, err := f.Write([]byte("\n" + newLine + "\n")); err != nil {
		return nil, fmt.Errorf("rc-export: %v", err)
	}
	return map[string]any{
		"state": "appended",
		"path":  path,
		"var":   varName,
		"math":  fmt.Sprintf("%s: no %s line — appended", path, varName),
	}, nil
}

// ── system.login-trigger ────────────────────────────────────────────────────
//
// "Run this command at every login" — the shape the NAS mount recipes wrote
// by hand twice per share (systemd user unit on Linux, RunAtLoad LaunchAgent
// on macOS), complete with the printf XML/INI scaffolding and quadruple
// escaping. The function owns the scaffold; recipes pass name + command.
// Re-runs rewrite and re-arm (idempotent, same as the originals).

func init() {
	Register(Function{
		Name: "system.login-trigger",
		Description: "Run a command at every login: Linux writes a Type=oneshot systemd user unit (~/.config/systemd/user/<name>.service) then daemon-reload + enable + start; macOS writes a RunAtLoad LaunchAgent (~/Library/LaunchAgents/<name>.plist) then launchctl load. state=armed; re-runs rewrite + re-arm.",
		Params: []Param{
			{Name: "name", Type: "string", Description: "Unit/plist name (e.g. gogitops-mount-trigger-bifrost)"},
			{Name: "command", Type: "string", Description: "Shell command run at each login (passed to /bin/sh -c verbatim — no YAML escaping needed)"},
			{Name: "description", Type: "string", Description: "Unit description (Linux; default: Login trigger (gogitops))"},
		},
		Run: loginTrigger,
	})
}

func loginTrigger(ctx Context, args map[string]any) (map[string]any, error) {
	name, _ := args["name"].(string)
	command, _ := args["command"].(string)
	desc, _ := args["description"].(string)
	if name == "" || command == "" {
		return nil, fmt.Errorf("name= and command= are required")
	}
	if !validUnitName(name) {
		return nil, fmt.Errorf("name= must be [a-z0-9._-]+ (no slashes), got %q", name)
	}
	if desc == "" {
		desc = "Login trigger (gogitops)"
	}
	switch ctx.OS {
	case "linux":
		return armSystemdUserTrigger(name, desc, command)
	case "darwin":
		return armLaunchdTrigger(name, command)
	default:
		return nil, fmt.Errorf("system.login-trigger: unsupported os %q", ctx.OS)
	}
}

// validUnitName guards the unit/plist FILENAME built from name= — a hostile
// or typo'd value must never escape ~/.config/systemd/user/ or
// ~/Library/LaunchAgents/.
func validUnitName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// buildUserUnit renders the Type=oneshot user unit. Same section/line order
// the printf scaffolds produced.
func buildUserUnit(desc, command string) string {
	return "[Unit]\n" +
		"Description=" + desc + "\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		"ExecStart=/bin/sh -c " + shQuote(command) + "\n" +
		"[Install]\n" +
		"WantedBy=default.target\n"
}

// buildLaunchAgentPlist renders the RunAtLoad agent. Command is XML-escaped
// (the raw printf scaffold never escaped — fine for its payload, a landmine
// for any payload containing <, >, or &).
func buildLaunchAgentPlist(label, command string) string {
	return "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
		"<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n" +
		"<plist version=\"1.0\">\n" +
		"<dict>\n" +
		"  <key>Label</key>\n" +
		"  <string>" + label + "</string>\n" +
		"  <key>ProgramArguments</key>\n" +
		"  <array>\n" +
		"    <string>/bin/sh</string>\n" +
		"    <string>-c</string>\n" +
		"    <string>" + xmlEscape(command) + "</string>\n" +
		"  </array>\n" +
		"  <key>RunAtLoad</key>\n" +
		"  <true/>\n" +
		"</dict>\n" +
		"</plist>\n"
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;")
	return r.Replace(s)
}

// shQuote single-quote-wraps for /bin/sh -c payloads ('\'' escaping) — the
// safe form for arbitrary commands, unlike the originals' hand-escaped
// double quotes.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func armSystemdUserTrigger(name, desc, command string) (map[string]any, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("login-trigger: %v", err)
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	unitPath := filepath.Join(dir, name+".service")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("login-trigger: %v", err)
	}
	// Overwrite every run — same as the originals' `printf … > file`.
	if err := os.WriteFile(unitPath, []byte(buildUserUnit(desc, command)), 0o644); err != nil {
		return nil, fmt.Errorf("login-trigger: %v", err)
	}
	// systemctl --user needs the runtime dir when the agent runs from a
	// system-level unit — same guard the original commands carried.
	env := os.Environ()
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		env = append(env, "XDG_RUNTIME_DIR=/run/user/"+strconv.Itoa(os.Getuid()))
	}
	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", name + ".service"},
		{"--user", "start", name + ".service"},
	} {
		cmd := execCommand("systemctl", args...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("login-trigger: systemctl --user %s: %v: %s", args[0]+" "+args[1], err, strings.TrimSpace(string(out)))
		}
	}
	return map[string]any{
		"state": "armed",
		"path":  unitPath,
		"name":  name,
		"math":  fmt.Sprintf("user unit %s written + enabled + started (oneshot)", name),
	}, nil
}

func armLaunchdTrigger(name, command string) (map[string]any, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("login-trigger: %v", err)
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	plistPath := filepath.Join(dir, name+".plist")
	label := "com.gogitops." + strings.TrimPrefix(name, "gogitops-")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("login-trigger: %v", err)
	}
	if err := os.WriteFile(plistPath, []byte(buildLaunchAgentPlist(label, command)), 0o644); err != nil {
		return nil, fmt.Errorf("login-trigger: %v", err)
	}
	// unload first (ignore errors — first run has nothing loaded), then
	// load must succeed.
	_ = execCommand("launchctl", "unload", plistPath).Run()
	if out, err := execCommand("launchctl", "load", plistPath).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("login-trigger: launchctl load: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return map[string]any{
		"state": "armed",
		"path":  plistPath,
		"label": label,
		"name":  name,
		"math":  fmt.Sprintf("LaunchAgent %s written + loaded (RunAtLoad)", name),
	}, nil
}
