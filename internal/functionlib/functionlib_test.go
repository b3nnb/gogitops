//go:build !windows

package functionlib

import (
	"fmt"
	"net"
	"strings"
	"testing"
)

func TestRegistryHasStdlib(t *testing.T) {
	for _, want := range []string{"net.port-check", "system.info", "storage.disk-free"} {
		if _, ok := Get(want); !ok {
			t.Errorf("stdlib function missing: %s (have: %v)", want, names())
		}
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
