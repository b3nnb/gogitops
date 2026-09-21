package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecipeFileKey(t *testing.T) {
	cases := map[string]string{
		"recipes/nas-bifrost-mount.yaml":              "nas-bifrost-mount",
		"recipes/setup-nenv.yaml":                     "setup-nenv",
		"/abs/repo/recipes/test-recipe/test-recipe.yaml": "test-recipe",
	}
	for in, want := range cases {
		if got := recipeFileKey(in); got != want {
			t.Errorf("recipeFileKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHashFileBytes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.yaml")
	os.WriteFile(p, []byte("hello"), 0644)
	h1, err := hashFileBytes(p)
	if err != nil || h1 == "" {
		t.Fatalf("hashFileBytes: %v %q", err, h1)
	}
	os.WriteFile(p, []byte("world"), 0644)
	h2, _ := hashFileBytes(p)
	if h1 == h2 {
		t.Error("hash should change when content changes")
	}
	if _, err := hashFileBytes(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing file should error")
	}
}

func TestApplyStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	st := applyState{
		NodeConfigHash: "abc",
		LastFullSweep:  "2026-09-19T00:00:00Z",
		Recipes: map[string]applyRecipeState{
			"nas-bifrost-mount": {Hash: "h1", Status: "ok", At: "now"},
			"setup-nenv":        {Hash: "h2", Status: "failed", Detail: "boom", At: "now"},
		},
	}
	saveApplyState(st)
	got := loadApplyState()
	if got.NodeConfigHash != "abc" || got.LastFullSweep != "2026-09-19T00:00:00Z" {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.Recipes["nas-bifrost-mount"].Status != "ok" {
		t.Errorf("recipe state mismatch: %+v", got.Recipes["nas-bifrost-mount"])
	}
	if got.Recipes["setup-nenv"].Detail != "boom" {
		t.Errorf("failed detail lost: %+v", got.Recipes["setup-nenv"])
	}

	// empty/missing file → zero state with initialized map
	os.Remove(filepath.Join(dir, "gogitops", "apply-state.json"))
	fresh := loadApplyState()
	if fresh.Recipes == nil {
		t.Error("loadApplyState must init the map")
	}
	if fresh.NodeConfigHash != "" {
		t.Errorf("fresh state should be empty, got %+v", fresh)
	}
}

func TestLastMeaningfulLine(t *testing.T) {
	out := []byte("\x1b[38;5;196m  first failure: step mount-bifrost: root part failed\x1b[0m\n\nrun-all complete\n")
	got := lastMeaningfulLine(out)
	want := "run-all complete"
	if got != want {
		t.Errorf("lastMeaningfulLine = %q, want %q", got, want)
	}
	if lastMeaningfulLine([]byte("\n\n")) != "" {
		t.Error("all-empty output should give empty string")
	}
}

func TestParseRecipeAutoApply(t *testing.T) {
	r := parseRecipe("name: x\nauto_apply: true\nlabels: []\nsteps:\n  - name: s\n    command: echo hi\n")
	if !r.AutoApply {
		t.Error("auto_apply: true must parse")
	}
	r2 := parseRecipe("name: y\nlabels: []\nsteps:\n  - name: s\n    command: echo hi\n")
	if r2.AutoApply {
		t.Error("auto_apply default must be false")
	}
}


// ── labelsMatch: case-insensitive (Benn, Sep 20) ─────────────────────────────
func TestLabelsMatchCaseInsensitive(t *testing.T) {
	cases := []struct {
		name     string
		node     []string
		req      []string
		excl     []string
		expected bool
	}{
		{"exact", []string{"nas"}, []string{"nas"}, nil, true},
		{"recipe-upper-node-lower", []string{"nas"}, []string{"NAS"}, nil, true},
		{"node-upper-recipe-lower", []string{"New-Node"}, []string{"new-node"}, nil, true},
		{"mixed-both", []string{"WiFi"}, []string{"wifi"}, nil, true},
		{"exclude-case", []string{"Admin"}, nil, []string{"admin"}, false},
		{"exclude-safe", []string{"linux"}, nil, []string{"admin"}, true},
		{"still-strict-on-value", []string{"nas"}, []string{"nas-storage"}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := labelsMatch(c.node, c.req, c.excl)
			if got != c.expected {
				t.Errorf("labelsMatch(%v, %v, %v) = %v, want %v", c.node, c.req, c.excl, got, c.expected)
			}
		})
	}
}
