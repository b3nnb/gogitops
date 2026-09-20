//go:build !windows

package functionlib

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryHasStdlib(t *testing.T) {
	for _, want := range []string{
		"net.port-check", "net.http-check", "net.github-asset",
		"system.info", "system.which", "storage.disk-free",
	} {
		if _, ok := Get(want); !ok {
			t.Errorf("stdlib function missing: %s (have: %v)", want, names())
		}
	}
}

// ── system.which ────────────────────────────────────────────────────────────

func TestWhichTable(t *testing.T) {
	dir := t.TempDir()
	// A real executable in the temp dir to use as a candidate.
	exe := filepath.Join(dir, "fakebrew")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A non-executable file — candidates must reject it.
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		args    map[string]any
		wantFnd bool
		wantSrc string
		wantErr bool
	}{
		{
			name:    "PATH hit (sh is everywhere the agent runs)",
			args:    map[string]any{"name": "sh"},
			wantFnd: true,
			wantSrc: "path",
		},
		{
			name:    "candidate fallback when PATH misses",
			args:    map[string]any{"name": "definitely-not-on-path-xyz", "candidates": exe + "," + plain},
			wantFnd: true,
			wantSrc: "candidate:1",
		},
		{
			name:    "non-executable candidate never matches",
			args:    map[string]any{"name": "definitely-not-on-path-xyz", "candidates": plain},
			wantFnd: false,
			wantSrc: "none",
		},
		{
			name:    "not found is a result, not an error",
			args:    map[string]any{"name": "definitely-not-on-path-xyz"},
			wantFnd: false,
			wantSrc: "none",
		},
		{
			name:    "missing name errors",
			args:    map[string]any{"candidates": exe},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := which(Context{}, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out["found"] != tc.wantFnd {
				t.Errorf("found = %v, want %v (out: %v)", out["found"], tc.wantFnd, out)
			}
			if out["source"] != tc.wantSrc {
				t.Errorf("source = %v, want %v", out["source"], tc.wantSrc)
			}
			if tc.wantFnd && out["path"] == "" {
				t.Errorf("found=true but path empty: %v", out)
			}
			if !tc.wantFnd && out["path"] != "" {
				t.Errorf("found=false but path non-empty: %v", out)
			}
		})
	}
}

func TestWhichTildeExpansion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir) // os.UserHomeDir follows HOME on unix
	bindir := filepath.Join(dir, ".local", "bin")
	if err := os.MkdirAll(bindir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(bindir, "nenv")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := which(Context{}, map[string]any{
		"name":       "definitely-not-on-path-xyz",
		"candidates": "/nope/bin/first,~/.local/bin/nenv",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["found"] != true || out["path"] != exe {
		t.Errorf("tilde candidate not resolved: %v", out)
	}
	if out["source"] != "candidate:2" {
		t.Errorf("source = %v, want candidate:2", out["source"])
	}
}

// ── net.http-check ──────────────────────────────────────────────────────────

func TestHttpCheckTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"hostname":"friday","agent_version":"v9.9.9"}`)
		case "/empty":
			fmt.Fprint(w, "nothing interesting")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tests := []struct {
		name      string
		args      map[string]any
		wantUp    bool
		wantMatch bool
		wantErr   bool
	}{
		{
			name:      "up + contains present",
			args:      map[string]any{"url": srv.URL + "/health", "contains": "hostname", "timeout": "2s"},
			wantUp:    true,
			wantMatch: true,
		},
		{
			name:      "up but contains missing is a result when require unset",
			args:      map[string]any{"url": srv.URL + "/empty", "contains": "hostname", "timeout": "2s"},
			wantUp:    true,
			wantMatch: false,
		},
		{
			name:    "require=true + missing contains errors",
			args:    map[string]any{"url": srv.URL + "/empty", "contains": "hostname", "require": "true"},
			wantErr: true,
		},
		{
			name:      "any status counts as up (mirrors curl -s)",
			args:      map[string]any{"url": srv.URL + "/nope", "timeout": "2s"},
			wantUp:    true,
			wantMatch: true,
		},
		{
			name:      "refused connection is down-as-result (no contains → matched vacuous)",
			args:      map[string]any{"url": "http://127.0.0.1:1/x", "timeout": "300ms"},
			wantUp:    false,
			wantMatch: true,
		},
		{
			name:    "require=true + refused errors",
			args:    map[string]any{"url": "http://127.0.0.1:1/x", "require": "true", "timeout": "300ms"},
			wantErr: true,
		},
		{
			name:    "missing url errors",
			args:    map[string]any{"contains": "x"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := httpCheck(Context{}, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got out=%v", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out["up"] != tc.wantUp {
				t.Errorf("up = %v, want %v (out: %v)", out["up"], tc.wantUp, out)
			}
			if out["matched"] != tc.wantMatch {
				t.Errorf("matched = %v, want %v (out: %v)", out["matched"], tc.wantMatch, out)
			}
		})
	}
}

// ── net.github-asset ────────────────────────────────────────────────────────

func TestGithubAssetTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/syncthing/syncthing/releases/latest":
			// Real-world naming (checked live): version sits between the
			// platform tag and the suffix — syncthing-linux-amd64-v2.1.5.tar.gz.
			fmt.Fprint(w, `{"tag_name":"v2.1.5","assets":[
				{"name":"sha256sum.txt.asc","browser_download_url":"https://github.com/syncthing/syncthing/releases/download/v2.1.5/sha256sum.txt.asc"},
				{"name":"syncthing-linux-amd64-v2.1.5.tar.gz","browser_download_url":"https://github.com/syncthing/syncthing/releases/download/v2.1.5/syncthing-linux-amd64-v2.1.5.tar.gz"},
				{"name":"syncthing-windows-amd64-v2.1.5.zip","browser_download_url":"https://github.com/syncthing/syncthing/releases/download/v2.1.5/syncthing-windows-amd64-v2.1.5.zip"}]}`)
		case "/repos/obsidianmd/obsidian-releases/releases":
			if r.URL.Query().Get("per_page") != "10" {
				http.Error(w, "per_page mismatch", 400)
				return
			}
			fmt.Fprint(w, `[{"tag_name":"v1.13.8","assets":[
				{"name":"Obsidian-1.13.8.apk","browser_download_url":"https://github.com/obsidianmd/obsidian-releases/releases/download/v1.13.8/Obsidian-1.13.8.apk"}]},
				{"tag_name":"v1.13.7","assets":[
				{"name":"Obsidian-1.13.7.dmg","browser_download_url":"https://github.com/obsidianmd/obsidian-releases/releases/download/v1.13.7/Obsidian-1.13.7.dmg"},
				{"name":"obsidian_1.13.7_amd64.deb","browser_download_url":"https://github.com/obsidianmd/obsidian-releases/releases/download/v1.13.7/obsidian_1.13.7_amd64.deb"}]}]`)
		case "/repos/o/k/errors/releases/latest":
			http.Error(w, `{"message":"rate limited"}`, 403)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })

	tests := []struct {
		name     string
		args     map[string]any
		wantFnd  bool
		wantURL  string
		wantTag  string
		wantErr  bool
	}{
		{
			name:    "latest release, matching asset (real syncthing naming)",
			args:    map[string]any{"repo": "syncthing/syncthing", "match": "linux-amd64"},
			wantFnd: true,
			wantURL: "https://github.com/syncthing/syncthing/releases/download/v2.1.5/syncthing-linux-amd64-v2.1.5.tar.gz",
			wantTag: "v2.1.5",
		},
		{
			name:    "walk list, first release has no match, second does",
			args:    map[string]any{"repo": "obsidianmd/obsidian-releases", "match": "amd64.deb", "walk": "10"},
			wantFnd: true,
			wantURL: "https://github.com/obsidianmd/obsidian-releases/releases/download/v1.13.7/obsidian_1.13.7_amd64.deb",
			wantTag: "v1.13.7",
		},
		{
			name:    "no matching asset is a result (found=false)",
			args:    map[string]any{"repo": "syncthing/syncthing", "match": "src.rpm"},
			wantFnd: false,
		},
		{
			name:    "API error surfaces as error (drives retries)",
			args:    map[string]any{"repo": "o/k/errors", "match": "whatever"},
			wantErr: true,
		},
		{
			name:    "bad walk arg errors",
			args:    map[string]any{"repo": "syncthing/syncthing", "match": "x", "walk": "-3"},
			wantErr: true,
		},
		{
			name:    "missing args error",
			args:    map[string]any{"repo": "syncthing/syncthing"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := githubAsset(Context{}, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got out=%v", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out["found"] != tc.wantFnd {
				t.Errorf("found = %v, want %v (out: %v)", out["found"], tc.wantFnd, out)
			}
			if tc.wantFnd {
				if out["url"] != tc.wantURL {
					t.Errorf("url = %v, want %v", out["url"], tc.wantURL)
				}
				if out["tag"] != tc.wantTag {
					t.Errorf("tag = %v, want %v", out["tag"], tc.wantTag)
				}
			}
		})
	}
}

func TestReservedSets(t *testing.T) {
	for _, s := range []string{"storage", "net", "system"} {
		if !IsReservedSet(s) {
			t.Errorf("set %s must be reserved", s)
		}
	}
	if IsReservedSet("user") {
		t.Error("user must not be reserved")
	}
}

func names() []string {
	var out []string
	for _, f := range List() {
		out = append(out, f.Name)
	}
	return out
}

func TestDiskFreeMath(t *testing.T) {
	out, err := diskFree(Context{Hostname: "test"}, map[string]any{"path": "/"})
	if err != nil {
		t.Fatal(err)
	}
	if out["point"] != "/" {
		t.Errorf("point = %v", out["point"])
	}
	total := fmt.Sprint(out["total_gb"])
	if !strings.ContainsAny(total, "0123456789") {
		t.Errorf("total_gb not numeric: %q", total)
	}
	// math must be shown, not just results (Benn's rule)
	if out["math"] == "" {
		t.Error("math token missing from outputs")
	}
}

func TestDiskFreeBadPath(t *testing.T) {
	if _, err := diskFree(Context{}, map[string]any{"path": "/definitely-not-here"}); err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestPortCheckAgainstListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no listener:", err)
	}
	defer ln.Close()
	port := fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
	out, err := portCheck(Context{}, map[string]any{"host": "127.0.0.1", "port": port})
	if err != nil {
		t.Fatal(err)
	}
	if out["up"] != true {
		t.Errorf("up = %v, want true (out: %v)", out["up"], out)
	}
}

func TestPortCheckDownIsAResult(t *testing.T) {
	out, err := portCheck(Context{}, map[string]any{"host": "127.0.0.1", "port": "1", "timeout": "300ms"})
	if err != nil {
		t.Fatalf("down check must be a result, not an error: %v", err)
	}
	if out["up"] != false {
		t.Errorf("up = %v, want false", out["up"])
	}
	if out["error"] == "" {
		t.Error("error output should carry dial error text")
	}
}

func TestPortCheckValidatesArgs(t *testing.T) {
	if _, err := portCheck(Context{}, map[string]any{"port": "80"}); err == nil {
		t.Error("missing host must error")
	}
	if _, err := portCheck(Context{}, map[string]any{"host": "x", "port": "not-a-port"}); err == nil {
		t.Error("bad port must error")
	}
}

func TestParseArgs(t *testing.T) {
	args, err := ParseArgs("host=10.2.0.103, port=53, timeout=1s")
	if err != nil {
		t.Fatal(err)
	}
	if args["host"] != "10.2.0.103" || args["port"] != "53" || args["timeout"] != "1s" {
		t.Errorf("args = %v", args)
	}
	args, err = ParseArgs(`{"path": "/media/benn/Bifrost, extra"}`)
	if err != nil {
		t.Fatal(err)
	}
	if args["path"] != "/media/benn/Bifrost, extra" {
		t.Errorf("json args = %v", args)
	}
	if _, err = ParseArgs("host"); err == nil {
		t.Error("key without = must error")
	}
}

func TestFormatOutputs(t *testing.T) {
	line := FormatOutputs(map[string]any{"used_pct": "41", "total_gb": "916.0"})
	if line != "total_gb=916.0 used_pct=41" {
		t.Errorf("line = %q", line)
	}
}
