// Tests for stdlib_unit.go — system.unit-restart, system.unit-repoint,
// system.user-unit. systemctl and sudo are FAKE shims on a temp PATH so the
// suite never touches a real init system; the shims log every invocation to
// a file the tests assert against (side-effect truth, not just pass/fail).
//
//go:build !windows

package functionlib

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBin builds a shim dir with `systemctl` + `sudo` scripts:
//
//   - `systemctl [--user] is-active <unit>` exits 0 only when a marker file
//     for that unit exists in the state dir (tests mark what's "running")
//   - `restart`/`enable` mark every *.service arg active — EXCEPT units
//     whose name contains "restart-fails", which exit 1 (failed op)
//   - `sudo -n systemctl …` forwards to the same systemctl logic; anything
//     but `-n` is refused (a sudo that would prompt must fail fast)
//
// Every invocation appends "<argv…>" to the log so tests verify WHAT ran,
// not just pass/fail.
func fakeBin(t *testing.T) (binDir, logfile string) {
	t.Helper()
	binDir = t.TempDir()
	logfile = filepath.Join(t.TempDir(), "calls.log")
	stateDir := filepath.Join(t.TempDir(), "unit-state")
	t.Setenv("UNIT_STATE_DIR", stateDir)
	systemctl := `#!/bin/sh
echo "$(basename "$0") $*" >> ` + shQuote(logfile) + `
if [ "$1" = "--user" ]; then shift; fi
sub="$1"; shift
case "$sub" in
  is-active)
    if [ -f "$UNIT_STATE_DIR/${1%.service}" ]; then exit 0; else exit 3; fi ;;
  restart)
    for a in "$@"; do case "$a" in *restart-fails*) exit 1 ;; esac; done
    mkdir -p "$UNIT_STATE_DIR"
    for a in "$@"; do : > "$UNIT_STATE_DIR/${a%.service}"; done
    exit 0 ;;
  enable)
    for a in "$@"; do case "$a" in *restart-fails*) exit 1 ;; esac; done
    mkdir -p "$UNIT_STATE_DIR"
    for a in "$@"; do case "$a" in *.service) : > "$UNIT_STATE_DIR/${a%.service}" ;; esac; done
    exit 0 ;;
  daemon-reload)
    exit 0 ;;
esac
exit 0
`
	sudo := `#!/bin/sh
echo "$(basename "$0") $*" >> ` + shQuote(logfile) + `
[ "$1" = "-n" ] || exit 2   # refuse anything but passwordless
shift
if [ "$1" = "systemctl" ]; then
  shift
  exec ` + shQuote(filepath.Join(binDir, "systemctl")) + ` "$@"
fi
exit 0
`
	for name, body := range map[string]string{"systemctl": systemctl, "sudo": sudo} {
		p := filepath.Join(binDir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	return binDir, logfile
}

// markActive simulates a unit that systemd would report active.
func markActive(t *testing.T, names ...string) {
	t.Helper()
	dir := os.Getenv("UNIT_STATE_DIR")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("running"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func callLog(t *testing.T, logfile string) []string {
	t.Helper()
	data, err := os.ReadFile(logfile)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// ── system.unit-restart ─────────────────────────────────────────────────────

func TestUnitRestartTable(t *testing.T) {
	tests := []struct {
		name      string
		active    []string // units the fake systemd reports active
		args      map[string]any
		wantErr   bool
		want      map[string]any // subset assertion
		wantCalls []string       // substrings, in order
	}{
		{
			name:      "nothing active is a result",
			active:    nil,
			args:      map[string]any{"user_units": "idle-a,idle-b", "system_units": "idle-sys"},
			want:      map[string]any{"restarted": "", "scope": "none"},
			wantCalls: []string{"is-active idle-a", "is-active idle-b"},
		},
		{
			name:      "first active user unit wins",
			active:    []string{"gogitops"},
			args:      map[string]any{"user_units": "idle-a,gogitops"},
			want:      map[string]any{"restarted": "gogitops", "scope": "user"},
			wantCalls: []string{"is-active idle-a", "is-active gogitops", "restart gogitops"},
		},
		{
			name:      "failed user restart falls through to the next candidate",
			active:    []string{"restart-fails-a", "gogitops-daemon"},
			args:      map[string]any{"user_units": "restart-fails-a,gogitops-daemon"},
			want:      map[string]any{"restarted": "gogitops-daemon", "scope": "user"},
			wantCalls: []string{"restart restart-fails-a", "restart gogitops-daemon"},
		},
		{
			name:      "system scope via passwordless sudo",
			active:    []string{"gogitops-daemon"},
			args:      map[string]any{"user_units": "idle-a", "system_units": "gogitops-daemon"},
			want:      map[string]any{"restarted": "gogitops-daemon", "scope": "system"},
			wantCalls: []string{"is-active idle-a", "is-active gogitops-daemon", "sudo -n systemctl restart gogitops-daemon"},
		},
		{
			name:      "system active but sudo refuses → scope=none result",
			active:    []string{"restart-fails-sys"},
			args:      map[string]any{"user_units": "idle-a", "system_units": "restart-fails-sys"},
			want:      map[string]any{"restarted": "", "scope": "none"},
			wantCalls: []string{"is-active restart-fails-sys", "sudo -n systemctl restart restart-fails-sys"},
		},
		{
			name:    "no candidates is an input error",
			args:    map[string]any{},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, logfile := fakeBin(t)
			markActive(t, tc.active...)
			out, err := unitRestart(Context{}, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for k, want := range tc.want {
				if got := fmt.Sprintf("%v", out[k]); got != fmt.Sprintf("%v", want) {
					t.Errorf("output[%s] = %q, want %q", k, got, fmt.Sprintf("%v", want))
				}
			}
			log := callLog(t, logfile)
			pos := 0
			for _, want := range tc.wantCalls {
				found := false
				for i := pos; i < len(log); i++ {
					if strings.Contains(log[i], want) {
						pos = i + 1
						found = true
						break
					}
				}
				if !found {
					t.Errorf("call %q not found in order; log = %v", want, log)
				}
			}
		})
	}
}

// ── system.unit-repoint ─────────────────────────────────────────────────────

func writeUnitT(t *testing.T, name, content string) string {
	t.Helper()
	dir, err := userUnitDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name+".service")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUnitRepointTable(t *testing.T) {
	tests := []struct {
		name      string
		units     map[string]string // unit name → initial content (written to the user unit dir)
		args      map[string]any
		wantErr   bool
		want      map[string]any
		wantFiles map[string]string // unit name → expected content after
	}{
		{
			name:  "swaps the deviant path and reloads",
			units: map[string]string{"gogitops": "[Service]\nExecStart=/usr/local/bin/gogitops daemon\n"},
			args:  map[string]any{"units": "gogitops", "from": "/usr/local/bin/gogitops", "to": "/home/u/.local/bin/gogitops"},
			want:  map[string]any{"found": 1, "patched": 1, "names": "gogitops", "reloaded": true, "enabled": 0},
			wantFiles: map[string]string{
				"gogitops": "[Service]\nExecStart=/home/u/.local/bin/gogitops daemon\n",
			},
		},
		{
			name:  "second run is a no-op (no from match, no churn)",
			units: map[string]string{"gogitops": "[Service]\nExecStart=/home/u/.local/bin/gogitops daemon\n"},
			args:  map[string]any{"units": "gogitops", "from": "/usr/local/bin/gogitops", "to": "/home/u/.local/bin/gogitops"},
			want:  map[string]any{"found": 1, "patched": 0, "reloaded": true},
		},
		{
			name:  "missing unit file is skipped",
			units: map[string]string{},
			args:  map[string]any{"units": "gogitops", "from": "/old/path", "to": "/new/path"},
			want:  map[string]any{"found": 0, "patched": 0, "reloaded": false},
		},
		{
			name:    "invalid unit name is refused (path-traversal guard)",
			args:    map[string]any{"units": "../escape", "from": "/a", "to": "/b"},
			wantErr: true,
		},
		{
			name:    "missing required args",
			args:    map[string]any{"units": "gogitops"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, logfile := fakeBin(t)
			t.Setenv("HOME", t.TempDir())
			for name, content := range tc.units {
				writeUnitT(t, name, content)
			}
			out, err := unitRepoint(Context{}, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for k, want := range tc.want {
				if got := fmt.Sprintf("%v", out[k]); got != fmt.Sprintf("%v", want) {
					t.Errorf("output[%s] = %q, want %q (calls: %v)", k, got, fmt.Sprintf("%v", want), callLog(t, logfile))
				}
			}
			for name, want := range tc.wantFiles {
				data, err := os.ReadFile(filepath.Join(mustUserUnitDirT(t), name+".service"))
				if err != nil {
					t.Fatalf("unit %s missing after run: %v", name, err)
				}
				if string(data) != want {
					t.Errorf("unit %s content = %q, want %q", name, data, want)
				}
			}
		})
	}
}

// TestUnitRepointMultiFromHome covers the real rescue shape: two units, two
// deviant paths (one tilde-form, resolved against the TEST home — the unit
// fixtures must carry the same resolved path the from= expands to).
func TestUnitRepointMultiFromHome(t *testing.T) {
	_, logfile := fakeBin(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeUnitT(t, "gogitops", "ExecStart=/usr/local/bin/gogitops daemon\n")
	writeUnitT(t, "gogitops-daemon", "ExecStart="+home+"/Documents/code/GoGitOps/bin/gogitops daemon\n")
	out, err := unitRepoint(Context{}, map[string]any{
		"units": "gogitops,gogitops-daemon",
		"from":  "/usr/local/bin/gogitops,~/Documents/code/GoGitOps/bin/gogitops",
		"to":    "~/.local/bin/gogitops",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v", out["patched"]); got != "2" {
		t.Errorf("patched = %q, want 2", got)
	}
	if got := fmt.Sprintf("%v", out["names"]); got != "gogitops,gogitops-daemon" {
		t.Errorf("names = %q, want both units", got)
	}
	for _, name := range []string{"gogitops", "gogitops-daemon"} {
		data, err := os.ReadFile(filepath.Join(mustUserUnitDirT(t), name+".service"))
		if err != nil {
			t.Fatal(err)
		}
		want := "ExecStart=" + filepath.Join(home, ".local", "bin", "gogitops") + " daemon\n"
		if !strings.Contains(string(data), want) {
			t.Errorf("unit %s = %q, want it to carry %q", name, data, want)
		}
	}
	log := callLog(t, logfile)
	joined := strings.Join(log, "\n")
	if !strings.Contains(joined, "daemon-reload") {
		t.Errorf("daemon-reload missing after patch: %v", log)
	}
}

func TestUnitRepointEnable(t *testing.T) {
	_, logfile := fakeBin(t)
	t.Setenv("HOME", t.TempDir())
	writeUnitT(t, "gogitops-daemon", "ExecStart=/usr/local/bin/gogitops daemon\n")
	out, err := unitRepoint(Context{}, map[string]any{
		"units": "gogitops-daemon", "from": "/usr/local/bin/gogitops",
		"to": "~/.local/bin/gogitops", "enable": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v", out["enabled"]); got != "1" {
		t.Errorf("enabled = %q, want 1", got)
	}
	log := callLog(t, logfile)
	sawEnable := false
	for _, c := range log {
		if strings.Contains(c, "enable gogitops-daemon.service") {
			sawEnable = true
		}
	}
	if !sawEnable {
		t.Errorf("enable call missing from log: %v", log)
	}
}

// ── system.user-unit ────────────────────────────────────────────────────────

func TestUserUnitLifecycle(t *testing.T) {
	fakeBin(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	args := map[string]any{
		"name": "functest-svc", "exec": "%h/.exo/bin/exo",
		"description": "test svc", "after": "network-online.target",
		"restart": "always", "restart_sec": "5", "environment": "HOME=%h",
		"enable": "false",
	}
	out, err := userUnit(Context{}, args)
	if err != nil {
		t.Fatal(err)
	}
	if out["state"] != "written" {
		t.Errorf("first run state = %v, want written", out["state"])
	}
	unitPath := filepath.Join(home, ".config", "systemd", "user", "functest-svc.service")
	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "[Unit]\nDescription=test svc\nAfter=network-online.target\n[Service]\nExecStart=%h/.exo/bin/exo\nRestart=always\nRestartSec=5\nEnvironment=HOME=%h\n[Install]\nWantedBy=default.target\n"
	if string(data) != want {
		t.Errorf("unit content = %q, want %q", data, want)
	}

	// Second run keeps the existing unit (convergence never clobbers).
	out, err = userUnit(Context{}, args)
	if err != nil {
		t.Fatal(err)
	}
	if out["state"] != "kept" {
		t.Errorf("second run state = %v, want kept", out["state"])
	}

	// overwrite=true rewrites.
	args["overwrite"] = "true"
	out, err = userUnit(Context{}, args)
	if err != nil {
		t.Fatal(err)
	}
	if out["state"] != "written" {
		t.Errorf("overwrite run state = %v, want written", out["state"])
	}

	// Hand-edited unit survives overwrite=false — the whole point of kept.
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=hand-edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args["overwrite"] = "false"
	if _, err = userUnit(Context{}, args); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(unitPath)
	if !strings.Contains(string(data), "hand-edited") {
		t.Errorf("hand-edited unit was clobbered: %q", data)
	}
}

func TestUserUnitArm(t *testing.T) {
	_, logfile := fakeBin(t)
	t.Setenv("HOME", t.TempDir())

	out, err := userUnit(Context{}, map[string]any{"name": "functest-arm", "exec": "/bin/true"})
	if err != nil {
		t.Fatal(err)
	}
	if out["enabled"] != true || out["active"] != true {
		t.Errorf("armed outputs = %v / %v, want true / true", out["enabled"], out["active"])
	}
	log := callLog(t, logfile)
	joined := strings.Join(log, "\n")
	for _, want := range []string{"daemon-reload", "enable --now functest-arm.service", "is-active functest-arm.service"} {
		if !strings.Contains(joined, want) {
			t.Errorf("arm sequence missing %q; log = %v", want, log)
		}
	}
}

func TestUserUnitArmFailure(t *testing.T) {
	fakeBin(t)
	t.Setenv("HOME", t.TempDir())

	// strict (default): enable failure is an error.
	_, err := userUnit(Context{}, map[string]any{"name": "active-restart-fails-svc", "exec": "/bin/true"})
	if err == nil {
		t.Errorf("strict enable failure should error")
	}
	// strict=false: recorded as a result, no error.
	out, err := userUnit(Context{}, map[string]any{"name": "active-restart-fails-svc", "exec": "/bin/true", "strict": "false"})
	if err != nil {
		t.Fatalf("strict=false should not error: %v", err)
	}
	if out["enabled"] != false {
		t.Errorf("strict=false enabled = %v, want false", out["enabled"])
	}

	// Invalid name refused.
	if _, err := userUnit(Context{}, map[string]any{"name": "../bad", "exec": "/bin/true"}); err == nil {
		t.Errorf("invalid name should error")
	}
	// Missing required args.
	if _, err := userUnit(Context{}, map[string]any{"name": "x"}); err == nil {
		t.Errorf("missing exec= should error")
	}
}

func mustUserUnitDirT(t *testing.T) string {
	t.Helper()
	dir, err := userUnitDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
