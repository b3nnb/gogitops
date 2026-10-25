// Tests for stdlib_gnome.go — system.gnome-keyring. systemctl is stubbed
// via a fake sh on PATH so the suite runs on any dev machine; unit-state
// outputs are asserted against what the fake reports. The rc upsert path
// is the real system.rc-export function (covered there); here we pin the
// choreography: which units get touched, in what order, and what the
// outputs claim.
//
//go:build !windows

package functionlib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSystemctl writes a sh shim named systemctl that records calls and
// reports conventional unit states. Returns the bin dir to put on PATH.
func fakeSystemctl(t *testing.T, behavior string) string {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\n" + behavior + "\nexit 0\n"
	for _, name := range []string{"systemctl", "gcr-ssh-agent"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

func TestGnomeKeyringTable(t *testing.T) {
	tests := []struct {
		name     string
		behavior string
		rcfiles  string // "" = fresh temp .bashrc per run
		verify   bool
		wantErr  bool
		check    func(t *testing.T, out map[string]any, log string)
	}{
		{
			// No commas in the name: the slug becomes the t.TempDir()
			// path, and gnomeKeyring splits rcfiles on commas.
			name: "happy path — enable + mask + rc upsert",
			behavior: `echo "$@" >> "$FAKE_LOG"
case "$*" in *"mask gpg-agent-ssh.socket"*) : ;; esac`,
			rcfiles: "",
			check: func(t *testing.T, out map[string]any, log string) {
				if !strings.Contains(log, "enable --now gcr-ssh-agent.socket") {
					t.Errorf("gcr socket not enabled; log: %s", log)
				}
				if !strings.Contains(log, "mask gpg-agent-ssh.socket") {
					t.Errorf("gpg-agent-ssh not masked; log: %s", log)
				}
				if !strings.Contains(log, "disable --now ssh-agent.service") {
					t.Errorf("ssh-agent.service not disabled; log: %s", log)
				}
				if out["enabled"] != true || out["masked"] != true {
					t.Errorf("enabled/masked = %v/%v, want true/true", out["enabled"], out["masked"])
				}
				sock, _ := out["sock"].(string)
				if !strings.HasPrefix(sock, "/run/user/") || !strings.HasSuffix(sock, "/gcr/ssh") {
					t.Errorf("sock = %q, want /run/user/<uid>/gcr/ssh", sock)
				}
				if n, _ := out["rc_lines"].(int); n != 1 {
					t.Errorf("rc_lines = %v, want 1", out["rc_lines"])
				}
			},
		},
		{
			name:     "verify fails when socket file missing",
			behavior: "exit 0",
			verify:   true,
			wantErr:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.verify {
				// Pin the socket path to a location that cannot exist.
				missing := filepath.Join(t.TempDir(), "no-such-socket")
				old := gcrSocketPath
				gcrSocketPath = func() string { return missing }
				defer func() { gcrSocketPath = old }()
			}
			bin := fakeSystemctl(t, tc.behavior)
			logFile := filepath.Join(t.TempDir(), "log")
			cmd := "#!/bin/sh\necho \"$@\" >> \"$FAKE_LOG\"\n" + tc.behavior
			if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(cmd), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+":"+t.TempDir())
			t.Setenv("FAKE_LOG", logFile)

			rcfiles := tc.rcfiles
			if rcfiles == "" {
				rcfiles = filepath.Join(t.TempDir(), ".bashrc")
			}
			args := map[string]any{"rcfiles": rcfiles}
			if tc.verify {
				args["verify"] = "true"
			}
			out, err := gnomeKeyring(Context{}, args)
			logB, _ := os.ReadFile(logFile)
			log := string(logB)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tc.check(t, out, log)
			// rc file must carry the export (upsert happened) with the
			// comment the gcr-ssh-agent recipe has always written.
			b, rerr := os.ReadFile(expandTilde(rcfiles))
			if rerr != nil {
				t.Fatalf("rc file not written: %v", rerr)
			}
			if !strings.Contains(string(b), `export SSH_AUTH_SOCK="`) ||
				!strings.Contains(string(b), "GCR ssh-agent (modern GNOME)") {
				t.Errorf("rc file missing SSH_AUTH_SOCK upsert: %q", string(b))
			}
		})
	}
}

func TestRegistryHasGnomeKeyring(t *testing.T) {
	if _, ok := Get("system.gnome-keyring"); !ok {
		t.Errorf("stdlib function missing: system.gnome-keyring")
	}
}
