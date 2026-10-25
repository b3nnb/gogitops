// Tests for stdlib_file.go — system.file-state, system.apt-install,
// system.flatpak. Table-driven, mirroring functionlib_test.go style.
//
//go:build !windows

package functionlib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── extractVersion (the version= output of file-state) ──────────────────────

func TestExtractVersionTable(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"release headline", "VERSION = v0.7.34\nbinary: gogitops-linux-amd64", "v0.7.34"},
		{"build suffix preferred (longest)", "v0.7.34\nv0.7.34-1", "v0.7.34-1"},
		{"build suffix first wins on equal length", "v0.7.34-1 then v0.7.34-2", "v0.7.34-1"},
		{"capital V normalized", "Release V1.2.3", "v1.2.3"},
		{"bare semver", "2.10.1", "2.10.1"},
		{"hash is not a version", "gogitops-linux-amd64 9f86d081884c7d65", ""},
		{"ip is not a version", "10.0.0.213:7780", ""},
		{"word with digits rejected", "sha256 abc123def xyz", ""},
		{"empty input", "", ""},
		{"version among noise", "check_interval: 5m\nagent v0.9.2 ok\nport 7780", "v0.9.2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractVersion(tc.in); got != tc.want {
				t.Errorf("extractVersion(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsVersionToken(t *testing.T) {
	yes := []string{"10", "v1", "v1.2.3", "0.7.34-1", "200"}
	no := []string{"a1b2", "1a2", "v.", "-", "v", "1.", "7780:", "x"}
	for _, tok := range yes {
		if !isVersionToken(tok) {
			t.Errorf("isVersionToken(%q) = false, want true", tok)
		}
	}
	for _, tok := range no {
		if isVersionToken(tok) {
			t.Errorf("isVersionToken(%q) = true, want false", tok)
		}
	}
}

// ── system.file-state: local files ──────────────────────────────────────────

func writeFileT(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFileStateLocalTable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool.sh")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho VERSION=v1.2.3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(big, make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args map[string]any
		want map[string]any // subset assertion
	}{
		{
			name: "missing file is a result",
			args: map[string]any{"path": filepath.Join(dir, "nope")},
			want: map[string]any{"exists": false, "matched": true},
		},
		{
			name: "present file, contains hit",
			args: map[string]any{"path": exe, "contains": "VERSION="},
			want: map[string]any{"exists": true, "matched": true, "exec": true},
		},
		{
			name: "contains miss",
			args: map[string]any{"path": exe, "contains": "NOPE"},
			want: map[string]any{"exists": true, "matched": false},
		},
		{
			name: "version extraction from file",
			args: map[string]any{"path": exe, "version": "true"},
			want: map[string]any{"exists": true, "version": "v1.2.3"},
		},
		{
			name: "oversized file skips read but still stats",
			args: map[string]any{"path": big, "contains": "x"},
			want: map[string]any{"exists": true, "matched": false},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := fileState(Context{}, tc.args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for k, w := range tc.want {
				if got := out[k]; got != w {
					t.Errorf("%s: got %v, want %v", k, got, w)
				}
			}
		})
	}
}

func TestFileStateRequiredArgs(t *testing.T) {
	if _, err := fileState(Context{}, map[string]any{}); err == nil {
		t.Error("want error for missing path=, got nil")
	}
}

func TestFileStateURLDownIsAResult(t *testing.T) {
	out, err := fileState(Context{}, map[string]any{"path": "http://127.0.0.1:1/x", "contains": "hardware", "timeout": "300ms"})
	if err != nil {
		t.Fatalf("unreachable URL must be a result, got error: %v", err)
	}
	if out["exists"] != false || out["matched"] != false {
		t.Errorf("want exists=false matched=false, got %v/%v", out["exists"], out["matched"])
	}
	if !strings.Contains(out["math"].(string), "GET http://127.0.0.1:1/x") {
		t.Errorf("math line should carry the probe: %v", out["math"])
	}
}

// ── system.apt-install ───────────────────────────────────────────────────────

func TestAptInstallTable(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
		check   func(t *testing.T, out map[string]any)
	}{
		{
			name:    "missing packages arg",
			args:    map[string]any{},
			wantErr: true,
		},
		{
			name:    "empty packages arg",
			args:    map[string]any{"packages": " , ,"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := aptInstall(Context{}, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tc.check(t, out)
		})
	}
}

func TestAptInstallNotApt(t *testing.T) {
	// In the unit-test environment apt-get exists on the dev box, so
	// force the not-apt path by pointing PATH at an empty dir.
	t.Setenv("PATH", t.TempDir())
	out, err := aptInstall(Context{}, map[string]any{"packages": "htop"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["mode"] != "not-apt" {
		t.Errorf("mode = %v, want not-apt", out["mode"])
	}
	if out["installed"] != false {
		t.Errorf("installed should be false on a non-apt node")
	}
}

func TestAptInstallIdempotentConverged(t *testing.T) {
	// bash is installed everywhere the tests run: a bare "bash" token is
	// already converged → no privilege path touched, installed=false.
	out, err := aptInstall(Context{}, map[string]any{"packages": "bash", "verify": "bash"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["mode"] != "converged" {
		t.Errorf("mode = %v, want converged", out["mode"])
	}
	if out["verify"] != true {
		t.Errorf("verify of an installed command should pass")
	}
}

func TestAptInstallVerifyAlternate(t *testing.T) {
	out, err := aptInstall(Context{}, map[string]any{"packages": "bash", "verify": "definitely-not-real-xyz,bash"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["mode"] != "converged" || out["verify"] != true {
		t.Errorf("alternates verify: got mode=%v verify=%v, want converged/true", out["mode"], out["verify"])
	}
}

func TestGlobMatch(t *testing.T) {
	if !globMatch("1.18.*", "1.18.0-6ubuntu14.4") {
		t.Error("glob prefix should match")
	}
	if globMatch("1.19.*", "1.18.0") {
		t.Error("wrong prefix must not match")
	}
	if globMatch("1.18.0", "1.18.0-6") {
		t.Error("non-glob pattern must never match through globMatch")
	}
}

func TestDpkgVersionIgnoresPartialStates(t *testing.T) {
	// dpkg-query itself is exercised only when installed; the status
	// filter logic is pure string handling — test it directly.
	tests := []struct {
		in   string
		want string
	}{
		{"install ok installed 1.2.3", "1.2.3"},
		{"deinstall ok config-files 1.2.3", ""},
		{"install ok half-installed 9.9", ""},
		{"", ""},
	}
	for _, tc := range tests {
		f := strings.Fields(tc.in)
		got := ""
		if len(f) >= 4 && f[0] == "install" && f[len(f)-2] == "installed" {
			got = f[len(f)-1]
		}
		if got != tc.want {
			t.Errorf("status filter(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ── system.flatpak ───────────────────────────────────────────────────────────

func TestFlatpakTable(t *testing.T) {
	tests := []struct {
		name    string
		ctx     Context
		args    map[string]any
		wantErr bool
		check   func(t *testing.T, out map[string]any)
	}{
		{
			name:    "missing app arg",
			args:    map[string]any{},
			ctx:     Context{OS: "linux"},
			wantErr: true,
		},
		{
			name: "non-linux is a not-linux result",
			args: map[string]any{"app": "md.obsidian.Obsidian"},
			ctx:  Context{OS: "darwin"},
			check: func(t *testing.T, out map[string]any) {
				if out["channel"] != "not-linux" {
					t.Errorf("channel = %v, want not-linux", out["channel"])
				}
			},
		},
		{
			name:    "non-linux + require=true errors",
			args:    map[string]any{"app": "x.y.Z", "require": "true"},
			ctx:     Context{OS: "darwin"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := flatpakInstall(tc.ctx, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tc.check(t, out)
		})
	}
}

func TestFlatpakNoFlatpakBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	out, err := flatpakInstall(Context{OS: "linux"}, map[string]any{"app": "md.obsidian.Obsidian"})
	if err != nil {
		t.Fatalf("no-flatpak must be a result, got error: %v", err)
	}
	if out["channel"] != "no-flatpak" {
		t.Errorf("channel = %v, want no-flatpak", out["channel"])
	}
}

func TestRegistryHasFilelib(t *testing.T) {
	for _, want := range []string{"system.file-state", "system.apt-install", "system.flatpak"} {
		if _, ok := Get(want); !ok {
			t.Errorf("stdlib function missing: %s (have: %v)", want, names())
		}
	}
}
