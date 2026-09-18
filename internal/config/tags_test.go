package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testNodeYAML = `hostname: framework
lan_ip: 10.0.0.229

# Tags (docs/tags.md — plain words; new-node/needs-setup mark it for
# first-time-setup recipes, flip them off in git once verified enrolled)
tags:
  - new-node
  - needs-setup

services: []
`

const testNodeYAMLMachine = `hostname: framework
lan_ip: 10.0.0.229
labels:
- compute
- nas
services: []
`

func writeTestNode(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nodes"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nodes", "framework.yaml"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestApplySelfTagsEditsTagsKey(t *testing.T) {
	dir := writeTestNode(t, testNodeYAML)

	before, after, changed, err := ApplySelfTags(dir, "framework",
		[]string{"nas", "admin"}, []string{"new-node"})
	if err != nil {
		t.Fatalf("ApplySelfTags: %v", err)
	}
	if !changed {
		t.Fatalf("expected change, before=%v after=%v", before, after)
	}
	if !reflect.DeepEqual(before, []string{"new-node", "needs-setup"}) {
		t.Errorf("before = %v, want [new-node needs-setup]", before)
	}
	want := []string{"needs-setup", "nas", "admin"}
	if !reflect.DeepEqual(after, want) {
		t.Errorf("after = %v, want %v", after, want)
	}

	out, err := os.ReadFile(filepath.Join(dir, "nodes", "framework.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Comments must survive the edit.
	if !strings.Contains(string(out), "# Tags (docs/tags.md") {
		t.Errorf("comment lost in edit:\n%s", out)
	}
	// Untouched keys must survive.
	if !strings.Contains(string(out), "lan_ip: 10.0.0.229") || !strings.Contains(string(out), "services: []") {
		t.Errorf("other keys lost in edit:\n%s", out)
	}
	// Edited tags present, removed tag gone.
	for _, want := range []string{"needs-setup", "nas", "admin"} {
		if !strings.Contains(string(out), "- "+want) {
			t.Errorf("tag %q missing from file:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "- new-node") {
		t.Errorf("removed tag still present as a list item:\n%s", out)
	}
}

func TestApplySelfTagsEditsLabelsKey(t *testing.T) {
	dir := writeTestNode(t, testNodeYAMLMachine)

	_, _, changed, err := ApplySelfTags(dir, "framework", []string{"laptop"}, nil)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "nodes", "framework.yaml"))
	if !strings.Contains(string(out), "- laptop") || !strings.Contains(string(out), "- nas") {
		t.Errorf("labels edit wrong:\n%s", out)
	}
	if strings.Contains(string(out), "\ntags:") {
		t.Errorf("machine-style file must not gain a second tags key:\n%s", out)
	}
}

func TestApplySelfTagsIdempotent(t *testing.T) {
	dir := writeTestNode(t, testNodeYAML)

	if _, _, changed, err := ApplySelfTags(dir, "framework", []string{"nas"}, []string{"new-node"}); err != nil || !changed {
		t.Fatalf("first run changed=%v err=%v", changed, err)
	}
	before, after, changed, err := ApplySelfTags(dir, "framework", []string{"nas"}, []string{"new-node"})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Errorf("second run must be a no-op: before=%v after=%v", before, after)
	}
}

func TestApplySelfTagsMissingNode(t *testing.T) {
	dir := writeTestNode(t, testNodeYAML)
	if _, _, _, err := ApplySelfTags(dir, "ghost", []string{"x"}, nil); err == nil {
		t.Fatal("expected error for unregistered node")
	}
}

func TestApplySelfTagsNoLabelsKey(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "nodes"), 0755)
	os.WriteFile(filepath.Join(dir, "nodes", "solo.yaml"),
		[]byte("hostname: solo\nservices: []\n"), 0644)

	_, after, changed, err := ApplySelfTags(dir, "solo", []string{"compute"}, nil)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !reflect.DeepEqual(after, []string{"compute"}) {
		t.Errorf("after=%v, want [compute]", after)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "nodes", "solo.yaml"))
	if !strings.Contains(string(out), "labels:\n  - compute") && !strings.Contains(string(out), "labels:\n- compute") {
		t.Errorf("labels key not appended:\n%s", out)
	}
}

// TestLoadNodeTagsAlias: hand-written `tags:` files load with Labels filled.
func TestLoadNodeTagsAlias(t *testing.T) {
	dir := writeTestNode(t, testNodeYAML)
	cfg, err := LoadNodeStrict(dir, "framework")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Labels, []string{"new-node", "needs-setup"}) {
		t.Errorf("Labels = %v, want [new-node needs-setup] (tags: alias not normalized)", cfg.Labels)
	}
}
