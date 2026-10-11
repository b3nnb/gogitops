//go:build !windows

package functionlib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── system.rc-line ──────────────────────────────────────────────────────────

func TestRcLineTable(t *testing.T) {
	tests := []struct {
		name      string
		initial   string // file contents before the call ("" = no file)
		args      map[string]any
		wantState string
		wantErr   bool
		wantFile  string // exact file contents after the call
	}{
		{
			name:      "missing file: created and appended (like >>)",
			args:      map[string]any{"file": "~/.bashrc", "marker": ".local/bin", "line": `\nexport PATH="$HOME/.local/bin:$PATH"\n`},
			wantState: "appended",
			wantFile:  "\nexport PATH=\"$HOME/.local/bin:$PATH\"\n",
		},
		{
			name:      "marker present: no-op, bytes untouched",
			initial:   "export PATH=\"$HOME/.local/bin:$PATH\"\n",
			args:      map[string]any{"file": "~/.bashrc", "marker": ".local/bin", "line": `\nexport PATH="$HOME/.local/bin:$PATH"\n`},
			wantState: "present",
			wantFile:  "export PATH=\"$HOME/.local/bin:$PATH\"\n",
		},
		{
			name:      "multi-line hook block appends verbatim",
			initial:   "# rc\n",
			args:      map[string]any{"file": "~/.bashrc", "marker": "bin/dashboard", "line": `\n# Login dashboard (first shell only) + alias to re-show\nif [ -z "$GOGITOPS_DASHBOARD_SHOWN" ]; then\n    export GOGITOPS_DASHBOARD_SHOWN=1\n    $HOME/bin/dashboard\nfi\n`},
			wantState: "appended",
			wantFile:  "# rc\n\n# Login dashboard (first shell only) + alias to re-show\nif [ -z \"$GOGITOPS_DASHBOARD_SHOWN\" ]; then\n    export GOGITOPS_DASHBOARD_SHOWN=1\n    $HOME/bin/dashboard\nfi\n",
		},
		{
			name:      "marker is a substring, not the whole line",
			initial:   "alias ll=\"ls -alF\"\n",
			args:      map[string]any{"file": "~/.bashrc", "marker": "alias ll=", "line": `\nalias ll="ls -alF"\nalias dc="docker compose"\n`},
			wantState: "present",
			wantFile:  "alias ll=\"ls -alF\"\n",
		},
		{
			name:      "append to file lacking trailing newline terminates it first",
			initial:   "# partial no newline",
			args:      map[string]any{"file": "~/.bashrc", "marker": "alias dc=", "line": `alias dc="docker compose"\n`},
			wantState: "appended",
			wantFile:  "# partial no newlinealias dc=\"docker compose\"\n",
		},
		{
			name:      "escaped backslash survives as one backslash",
			args:      map[string]any{"file": "~/.bashrc", "marker": "zzz", "line": `a\\nzzzb`},
			wantState: "appended",
			wantFile:  "a\\nzzzb",
		},
		{
			name:    "missing args error",
			args:    map[string]any{"file": "~/.bashrc", "marker": "x"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			rc := filepath.Join(home, ".bashrc")
			if tc.initial != "" {
				if err := os.WriteFile(rc, []byte(tc.initial), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			out, err := rcLine(Context{}, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got out=%v", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out["state"] != tc.wantState {
				t.Errorf("state = %v, want %v (out: %v)", out["state"], tc.wantState, out)
			}
			got, err := os.ReadFile(rc)
			if err != nil {
				t.Fatalf("rc file unreadable: %v", err)
			}
			if string(got) != tc.wantFile {
				t.Errorf("file bytes:\n got  %q\n want %q", string(got), tc.wantFile)
			}
			if out["math"] == "" {
				t.Error("math token missing from outputs")
			}
		})
	}
}

// Re-running an rc-line step must be a no-op — the fleet suite runs these
// recipes every cycle; appends that repeat are churn.
func TestRcLineIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rc := filepath.Join(home, ".bashrc")
	args := map[string]any{"file": "~/.bashrc", "marker": "starship init", "line": `eval "$(starship init bash)"\n`}
	for i := 0; i < 3; i++ {
		out, err := rcLine(Context{}, args)
		if err != nil {
			t.Fatal(err)
		}
		want := "appended"
		if i > 0 {
			want = "present"
		}
		if out["state"] != want {
			t.Fatalf("run %d: state = %v, want %v", i+1, out["state"], want)
		}
	}
	data, _ := os.ReadFile(rc)
	if got := strings.Count(string(data), "starship init"); got != 1 {
		t.Errorf("starship init appears %d times, want 1: %q", got, string(data))
	}
}

// ── system.rc-export ────────────────────────────────────────────────────────

func TestRcExportTable(t *testing.T) {
	tests := []struct {
		name      string
		initial   string
		args      map[string]any
		wantState string
		wantFile  string
		wantErr   bool
	}{
		{
			name:      "fresh file: appended, double-quoted, comment kept",
			args:      map[string]any{"file": "~/.profile", "var": "SSH_AUTH_SOCK", "value": "/run/user/1000/gcr/ssh", "comment": "GCR ssh-agent (modern GNOME)"},
			wantState: "appended",
			wantFile:  "\nexport SSH_AUTH_SOCK=\"/run/user/1000/gcr/ssh\"  # GCR ssh-agent (modern GNOME)\n",
		},
		{
			name:      "existing export rewritten in place, other lines intact",
			initial:   "# top\nexport SSH_AUTH_SOCK=/old/path\nexport HOME=/home/benn\n",
			args:      map[string]any{"file": "~/.bashrc", "var": "SSH_AUTH_SOCK", "value": "/run/user/1000/gcr/ssh"},
			wantState: "updated",
			wantFile:  "# top\nexport SSH_AUTH_SOCK=\"/run/user/1000/gcr/ssh\"\nexport HOME=/home/benn\n",
		},
		{
			name:      "several export lines all rewritten (old sed applied per-line)",
			initial:   "export SSH_AUTH_SOCK=/a\nmid\nexport SSH_AUTH_SOCK=/b\n",
			args:      map[string]any{"file": "~/.bashrc", "var": "SSH_AUTH_SOCK", "value": "/new"},
			wantState: "updated",
			wantFile:  "export SSH_AUTH_SOCK=\"/new\"\nmid\nexport SSH_AUTH_SOCK=\"/new\"\n",
		},
		{
			name:      "leading whitespace before export kept (sed match-offset semantics)",
			initial:   "  export PATH=/usr/bin\n",
			args:      map[string]any{"file": "~/.bashrc", "var": "PATH", "value": "$HOME/.local/bin:$PATH"},
			wantState: "updated",
			wantFile:  "  export PATH=\"$HOME/.local/bin:$PATH\"\n",
		},
		{
			name:      "no export of that var: appended (comment-only mention does not block)",
			initial:   "# SSH_AUTH_SOCK is set by the session\n",
			args:      map[string]any{"file": "~/.bashrc", "var": "SSH_AUTH_SOCK", "value": "/run/user/1000/gcr/ssh"},
			wantState: "appended",
			wantFile:  "# SSH_AUTH_SOCK is set by the session\n\nexport SSH_AUTH_SOCK=\"/run/user/1000/gcr/ssh\"\n",
		},
		{
			name:      "variable prefix must match exactly (PATHH is not PATH)",
			initial:   "export PATHH=x\n",
			args:      map[string]any{"file": "~/.bashrc", "var": "PATH", "value": "/bin"},
			wantState: "appended",
			wantFile:  "export PATHH=x\n\nexport PATH=\"/bin\"\n",
		},
		{
			name:    "missing args error",
			args:    map[string]any{"file": "~/.bashrc", "var": "X"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			// "~/.bashrc" → <home>/.bashrc — TrimPrefix keeps the dot.
			rc := filepath.Join(home, strings.TrimPrefix(anyFileArg(tc.args), "~"))
			if tc.initial != "" {
				if err := os.WriteFile(rc, []byte(tc.initial), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			out, err := rcExport(Context{}, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got out=%v", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out["state"] != tc.wantState {
				t.Errorf("state = %v, want %v (out: %v)", out["state"], tc.wantState, out)
			}
			got, err := os.ReadFile(rc)
			if err != nil {
				t.Fatalf("rc file unreadable: %v", err)
			}
			if string(got) != tc.wantFile {
				t.Errorf("file bytes:\n got  %q\n want %q", string(got), tc.wantFile)
			}
			if tc.initial != "" { // mode must survive in-place rewrites
				if info, err := os.Stat(rc); err == nil && info.Mode().Perm() != 0o640 {
					t.Errorf("mode = %v, want 0640 preserved", info.Mode().Perm())
				}
			}
			if out["math"] == "" {
				t.Error("math token missing from outputs")
			}
		})
	}
}

// anyFileArg pulls the file= arg back out of the table row (all rows here
// live under ~/. to keep the temp-home redirect honest).
func anyFileArg(args map[string]any) string {
	if p, _ := args["file"].(string); p != "" {
		return p
	}
	return "~/.x"
}

// ── system.login-trigger ────────────────────────────────────────────────────

// The unit/plist scaffolds are the whole point of the function — pin them
// byte-for-byte. The arming execs are exercised live by the fleet selftest
// (recipes/test-funcs), not here: unit tests must never touch the live
// user session.
func TestBuildUserUnit(t *testing.T) {
	got := buildUserUnit("Trigger Bifrost automount at login (gogitops)", `for i in 1 2 3; do ls /media/benn/Bifrost >/dev/null 2>&1 && exit 0; sleep 5; done; true`)
	want := "[Unit]\n" +
		"Description=Trigger Bifrost automount at login (gogitops)\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		"ExecStart=/bin/sh -c 'for i in 1 2 3; do ls /media/benn/Bifrost >/dev/null 2>&1 && exit 0; sleep 5; done; true'\n" +
		"[Install]\n" +
		"WantedBy=default.target\n"
	if got != want {
		t.Errorf("unit bytes:\n got  %q\n want %q", got, want)
	}
}

func TestBuildUserUnitQuotesHostilePayload(t *testing.T) {
	got := buildUserUnit("d", `echo "a'b"`)
	if !strings.Contains(got, `ExecStart=/bin/sh -c 'echo "a'\''b"'`) {
		t.Errorf("payload not sh-escaped: %q", got)
	}
}

func TestBuildLaunchAgentPlist(t *testing.T) {
	got := buildLaunchAgentPlist("com.gogitops.mount-trigger-bifrost", `mount | grep -q "on /Volumes/Bifrost" || mount_smbfs //10.2.0.103/Bifrost /Volumes/Bifrost`)
	for _, want := range []string{
		`<string>com.gogitops.mount-trigger-bifrost</string>`,
		"<string>/bin/sh</string>",
		"<string>-c</string>",
		`<string>mount | grep -q &#34;on /Volumes/Bifrost&#34; || mount_smbfs //10.2.0.103/Bifrost /Volumes/Bifrost</string>`,
		"<key>RunAtLoad</key>",
		"<true/>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q:\n%s", want, got)
		}
	}
}

func TestXmlEscape(t *testing.T) {
	if got := xmlEscape(`<a & "b">`); got != "&lt;a &amp; &#34;b&#34;&gt;" {
		t.Errorf("xmlEscape = %q", got)
	}
}

func TestLoginTriggerValidation(t *testing.T) {
	if _, err := loginTrigger(Context{OS: "linux"}, map[string]any{"name": "x"}); err == nil {
		t.Error("missing command must error")
	}
	if _, err := loginTrigger(Context{OS: "linux"}, map[string]any{"name": "../evil", "command": "true"}); err == nil {
		t.Error("path traversal in name= must error")
	}
	if _, err := loginTrigger(Context{OS: "linux"}, map[string]any{"name": "ok name", "command": "true"}); err == nil {
		t.Error("space in name= must error")
	}
	if _, err := loginTrigger(Context{OS: "plan9"}, map[string]any{"name": "x", "command": "true"}); err == nil {
		t.Error("unsupported os must error")
	}
}

func TestValidUnitName(t *testing.T) {
	for _, ok := range []string{"gogitops-mount-trigger-bifrost", "gogitops_mount_trigger", "gogitops.mount"} {
		if !validUnitName(ok) {
			t.Errorf("validUnitName(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", "a b", "x$y"} {
		if validUnitName(bad) {
			t.Errorf("validUnitName(%q) = true, want false", bad)
		}
	}
}

func TestShQuote(t *testing.T) {
	if got := shQuote(`mount | grep -q "on /Volumes/X"`); got != `'mount | grep -q "on /Volumes/X"'` {
		t.Errorf("shQuote = %q", got)
	}
}

// ── net.lan-ip ──────────────────────────────────────────────────────────────

func TestRouteParsers(t *testing.T) {
	route := "10.2.0.0/24 dev enp0s31f6 proto kernel scope link src 10.2.0.102 metric 100"
	if got := srcFromRoute(route); got != "10.2.0.102" {
		t.Errorf("srcFromRoute = %q", got)
	}
	if got := srcFromRoute("default via 10.2.0.1 dev enp0s31f6 proto dhcp metric 100"); got != "" {
		t.Errorf("srcFromRoute without src = %q, want empty", got)
	}
	if got := ifaceFromRoute("   route to: default\ninterface: en0\n"); got != "en0" {
		t.Errorf("ifaceFromRoute = %q, want en0", got)
	}
	if got := ifaceFromRoute("no interface line"); got != "" {
		t.Errorf("ifaceFromRoute missing = %q, want empty", got)
	}
	if got := firstField("10.2.0.102 10.200.0.2 172.17.0.1"); got != "10.2.0.102" {
		t.Errorf("firstField = %q", got)
	}
}

// Live smoke: on any normal agent node the function resolves SOMETHING.
// Never fails the suite on exotic sandboxes — found=false is a result.
func TestLanIPSmoke(t *testing.T) {
	got, err := lanIP(Context{OS: "linux"}, map[string]any{})
	if err != nil {
		t.Fatalf("down probe must be a result, not an error: %v", err)
	}
	if got["math"] == "" {
		t.Error("math token missing from outputs")
	}
	if got["found"] == true && got["ip"] == "" {
		t.Errorf("found=true but ip empty: %v", got)
	}
	if got["found"] == false && got["ip"] != "" {
		t.Errorf("found=false but ip non-empty: %v", got)
	}
}
