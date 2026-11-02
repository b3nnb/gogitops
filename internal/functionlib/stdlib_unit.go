// Stdlib: systemd user-unit lifecycle — the three unit surgeries the fleet
// recipes kept hand-rolling in shell:
//
//   - system.unit-restart: restart whichever unit is actually active, trying
//     user scope first, then system scope through passwordless sudo only
//     (sudo -n never prompts — a node without passwordless sudo simply
//     reports scope=none).
//   - system.unit-repoint: swap deviant binary paths inside user unit files
//     + daemon-reload (the /usr/local/bin → ~/.local/bin convergences).
//   - system.user-unit: write + arm a long-running user unit — the service
//     cousin of system.login-trigger's oneshot, minus the printf scaffolds.
//
// systemctl idioms follow stdlib_rc.go's login-trigger: XDG_RUNTIME_DIR is
// injected when missing so `systemctl --user` also works from a system-level
// agent context.
//
//go:build !windows

package functionlib

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func init() {
	Register(Function{
		Name:        "system.unit-restart",
		Description: "Restart whichever unit from the ordered candidates is currently active: user scope first (systemctl --user), then system scope via passwordless sudo (sudo -n — never prompts). Nothing active is a result (scope=none), not an error — gate fallbacks (e.g. an API restart) on it.",
		Params: []Param{
			{Name: "user_units", Type: "string", Description: "Comma-separated user unit candidates, tried in order (e.g. gogitops,gogitops-daemon)"},
			{Name: "system_units", Type: "string", Description: "Comma-separated system unit candidates, tried only after every user candidate missed; restarted via sudo -n (passwordless only)"},
		},
		Run: unitRestart,
	})

	Register(Function{
		Name:        "system.unit-repoint",
		Description: "Repoint binary paths inside systemd user units: every from= path substring found in ~/.config/systemd/user/<units>.service is rewritten to to= (atomic swap), then daemon-reload. Units without a unit file are skipped (found= counts what exists). enable=true also systemctl --user enable's each found unit (already-enabled stays ok).",
		Params: []Param{
			{Name: "units", Type: "string", Description: "Comma-separated USER unit names whose unit files get patched (e.g. gogitops,gogitops-daemon)"},
			{Name: "from", Type: "string", Description: "Comma-separated path substrings to replace; use JSON args when values contain commas (~ expands)"},
			{Name: "to", Type: "string", Description: "Replacement path (~ expands)"},
			{Name: "enable", Type: "bool", Description: "true = also systemctl --user enable each found unit"},
		},
		Run: unitRepoint,
	})

	Register(Function{
		Name:        "system.user-unit",
		Description: "Write a long-running systemd user unit (~/.config/systemd/user/<name>.service) and arm it: daemon-reload + enable --now, with active= reporting the result. overwrite=false (default) keeps an existing unit — convergence never clobbers a hand-edited one; overwrite=true rewrites every run. strict=false turns a failed enable --now into a result (enabled=false) instead of an error.",
		Params: []Param{
			{Name: "name", Type: "string", Description: "Unit name (e.g. exo) — becomes <name>.service in ~/.config/systemd/user/"},
			{Name: "exec", Type: "string", Description: "ExecStart value, verbatim (e.g. %h/.exo/bin/exo — %h stays a systemd specifier)"},
			{Name: "description", Type: "string", Description: "Unit Description= (default: the unit name)"},
			{Name: "after", Type: "string", Description: "After= dependency (e.g. network-online.target)"},
			{Name: "restart", Type: "string", Description: "Restart= policy (default on-failure)"},
			{Name: "restart_sec", Type: "string", Description: "RestartSec= value (seconds)"},
			{Name: "environment", Type: "string", Description: "One Environment= line, verbatim (e.g. HOME=%h)"},
			{Name: "enable", Type: "bool", Description: "Arm the unit: daemon-reload + enable --now (default true)"},
			{Name: "overwrite", Type: "bool", Description: "true = rewrite the unit file even when it already exists"},
			{Name: "strict", Type: "bool", Description: "true (default) = enable failure is an error; false = recorded as enabled=false result"},
		},
		Run: userUnit,
	})
}

// ── shared helpers ──────────────────────────────────────────────────────────

// argStr reads a string arg, tolerating the JSON-args map form.
func argStr(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

// argOr returns def when s is empty.
func argOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// argBoolDefault behaves like parseBoolArg but with an explicit default for
// the ABSENT key: enable-style flags default true (arm it), overwrite-style
// flags default false (never clobber).
func argBoolDefault(args map[string]any, key string, def bool) bool {
	v, ok := args[key].(string)
	if !ok || v == "" {
		return def
	}
	return parseBoolArg(v)
}

// splitList splits a comma-separated arg into trimmed, non-empty items.
func splitList(v any) []string {
	s, _ := v.(string)
	var out []string
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// systemctlEnv is the exec env for systemctl calls: XDG_RUNTIME_DIR injected
// when missing so `systemctl --user` works from a system-level agent context
// (same guard the login-trigger originals carried).
func systemctlEnv() []string {
	env := os.Environ()
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		env = append(env, "XDG_RUNTIME_DIR=/run/user/"+strconv.Itoa(os.Getuid()))
	}
	return env
}

// runSystemctl runs systemctl (with --user when user scope) under env.
// Missing systemctl (darwin, containers) is just an error — callers treat
// every failure as "did not happen", never a prompt or a crash.
func runSystemctl(user bool, env []string, args ...string) error {
	full := args
	if user {
		full = append([]string{"--user"}, args...)
	}
	cmd := exec.Command("systemctl", full...)
	cmd.Env = env
	return cmd.Run()
}

// unitActive reports whether a unit is active in the given scope. is-active
// exits non-zero for inactive/missing units — both mean "not active".
func unitActive(user bool, unit string, env []string) bool {
	return runSystemctl(user, env, "is-active", unit) == nil
}

// userUnitDir is ~/.config/systemd/user — where every stdlib unit write lands.
func userUnitDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

// atomicWrite replaces path via a sibling temp file + rename — the same
// swap-then-move shape the originals used (`cp .new && mv`), never a partial
// in-place overwrite of a live unit or binary.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".gogitops-new"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ── system.unit-restart ─────────────────────────────────────────────────────

func unitRestart(ctx Context, args map[string]any) (map[string]any, error) {
	userUnits := splitList(args["user_units"])
	sysUnits := splitList(args["system_units"])
	if len(userUnits) == 0 && len(sysUnits) == 0 {
		return nil, fmt.Errorf("user_units= or system_units= required")
	}
	env := systemctlEnv()

	for _, u := range userUnits {
		if !unitActive(true, u, env) {
			continue
		}
		if err := runSystemctl(true, env, "restart", u); err != nil {
			continue // same shape as the originals' `restart && …` chains — next candidate
		}
		return map[string]any{
			"restarted": u,
			"scope":     "user",
			"math":      fmt.Sprintf("restarted user unit %s", u),
		}, nil
	}
	for _, u := range sysUnits {
		if !unitActive(false, u, env) {
			continue
		}
		// sudo -n only: a sudo that would prompt must fail fast, the way
		// the originals' `sudo -n … 2>/dev/null ||` shaped it.
		cmd := exec.Command("sudo", "-n", "systemctl", "restart", u)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = out
			continue
		}
		return map[string]any{
			"restarted": u,
			"scope":     "system",
			"math":      fmt.Sprintf("restarted system unit %s via passwordless sudo", u),
		}, nil
	}
	return map[string]any{
		"restarted": "",
		"scope":     "none",
		"math":      fmt.Sprintf("no candidate active (user: %d, system: %d tried)", len(userUnits), len(sysUnits)),
	}, nil
}

// ── system.unit-repoint ─────────────────────────────────────────────────────

func unitRepoint(ctx Context, args map[string]any) (map[string]any, error) {
	units := splitList(args["units"])
	froms := splitList(args["from"])
	to := expandTilde(argStr(args, "to"))
	if len(units) == 0 || len(froms) == 0 || to == "" {
		return nil, fmt.Errorf("units=, from= and to= are required")
	}
	for _, u := range units {
		if !validUnitName(u) {
			return nil, fmt.Errorf("unit name %q must be [a-z0-9._-]+ (no slashes)", u)
		}
	}
	enable := parseBoolArg(args["enable"])
	env := systemctlEnv()
	dir, err := userUnitDir()
	if err != nil {
		return nil, fmt.Errorf("unit-repoint: %v", err)
	}

	var found, patched, enabled []string
	for _, u := range units {
		path := filepath.Join(dir, u+".service")
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue // no unit file here — the originals' [ -f "$F" ] guard
		}
		found = append(found, u)
		content := string(data)
		changed := false
		for _, f := range froms {
			fx := expandTilde(f)
			if fx != "" && strings.Contains(content, fx) {
				content = strings.ReplaceAll(content, fx, to)
				changed = true
			}
		}
		if changed {
			mode := os.FileMode(0o644)
			if info, serr := os.Stat(path); serr == nil {
				mode = info.Mode().Perm()
			}
			if err := atomicWrite(path, []byte(content), mode); err != nil {
				return nil, fmt.Errorf("unit-repoint: %s: %v", path, err)
			}
			patched = append(patched, u)
		}
		if enable {
			if err := runSystemctl(true, env, "enable", u+".service"); err == nil {
				enabled = append(enabled, u)
			}
		}
	}
	// daemon-reload only pays off when at least one unit file exists — and
	// only after its content may have changed. systemd treats a redundant
	// reload as a no-op either way.
	reloaded := false
	if len(found) > 0 {
		reloaded = runSystemctl(true, env, "daemon-reload") == nil
	}
	return map[string]any{
		"found":    len(found),
		"patched":  len(patched),
		"names":    strings.Join(patched, ","),
		"reloaded": reloaded,
		"enabled":  len(enabled),
		"math": fmt.Sprintf("units found=%d patched=%d (%s) reloaded=%v enabled=%d",
			len(found), len(patched), strings.Join(patched, ","), reloaded, len(enabled)),
	}, nil
}

// ── system.user-unit ────────────────────────────────────────────────────────

// buildServiceUnit renders the long-running unit. Same section/line order as
// the printf scaffolds it replaces ([Unit] → [Service] → [Install]), optional
// lines omitted entirely.
func buildServiceUnit(desc, after, execStart, restart, restartSec, environment string) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=" + desc + "\n")
	if after != "" {
		b.WriteString("After=" + after + "\n")
	}
	b.WriteString("[Service]\n")
	b.WriteString("ExecStart=" + execStart + "\n")
	if restart != "" {
		b.WriteString("Restart=" + restart + "\n")
	}
	if restartSec != "" {
		b.WriteString("RestartSec=" + restartSec + "\n")
	}
	if environment != "" {
		b.WriteString("Environment=" + environment + "\n")
	}
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

func userUnit(ctx Context, args map[string]any) (map[string]any, error) {
	name := argStr(args, "name")
	execStart := argStr(args, "exec")
	if name == "" || execStart == "" {
		return nil, fmt.Errorf("name= and exec= are required")
	}
	if !validUnitName(name) {
		return nil, fmt.Errorf("name= must be [a-z0-9._-]+ (no slashes), got %q", name)
	}
	desc := argOr(argStr(args, "description"), name)
	after := argStr(args, "after")
	restart := argOr(argStr(args, "restart"), "on-failure")
	restartSec := argStr(args, "restart_sec")
	environment := argStr(args, "environment")
	enable := argBoolDefault(args, "enable", true)
	overwrite := argBoolDefault(args, "overwrite", false)
	strict := argBoolDefault(args, "strict", true)

	dir, err := userUnitDir()
	if err != nil {
		return nil, fmt.Errorf("user-unit: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("user-unit: %v", err)
	}
	unitPath := filepath.Join(dir, name+".service")

	state := "kept"
	if _, statErr := os.Stat(unitPath); statErr != nil || overwrite {
		if err := os.WriteFile(unitPath, []byte(buildServiceUnit(desc, after, execStart, restart, restartSec, environment)), 0o644); err != nil {
			return nil, fmt.Errorf("user-unit: %v", err)
		}
		state = "written"
	}

	enabled, active := false, false
	var math string
	if enable {
		env := systemctlEnv()
		if err := runSystemctl(true, env, "daemon-reload"); err != nil {
			return nil, fmt.Errorf("user-unit: daemon-reload: %v", err)
		}
		cmd := exec.Command("systemctl", "--user", "enable", "--now", name+".service")
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			if strict {
				return nil, fmt.Errorf("user-unit: enable --now %s: %v: %s", name, err, strings.TrimSpace(string(out)))
			}
			// best-effort mode: record and move on — the convergence step
			// stays green, the armed=false state is visible in outputs.
			math = fmt.Sprintf("unit %s %s but enable --now failed: %v", name, state, err)
			return map[string]any{
				"state":   state,
				"path":    unitPath,
				"name":    name,
				"enabled": false,
				"active":  false,
				"math":    math,
			}, nil
		}
		enabled = true
		active = runSystemctl(true, env, "is-active", name+".service") == nil
		math = fmt.Sprintf("unit %s %s, enabled, active=%v", name, state, active)
	} else {
		math = fmt.Sprintf("unit %s %s (not armed — enable=false)", name, state)
	}
	return map[string]any{
		"state":   state,
		"path":    unitPath,
		"name":    name,
		"enabled": enabled,
		"active":  active,
		"math":    math,
	}, nil
}
