package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func pinsDir(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if content != "" {
		if err := os.WriteFile(filepath.Join(dir, "versions.yaml"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadVersionPinsMissing(t *testing.T) {
	p, err := LoadVersionPins(pinsDir(t, ""))
	if p != nil || err != nil {
		t.Fatalf("missing file should be (nil, nil), got (%v, %v)", p, err)
	}
}

func TestLoadVersionPinsFull(t *testing.T) {
	dir := pinsDir(t, "global: v0.6.9\ngroups:\n  gpu: v0.6.8\nnodes:\n  framework: latest\n")
	p, err := LoadVersionPins(dir)
	if err != nil || p == nil {
		t.Fatalf("load failed: %v", err)
	}
	if p.Global != "v0.6.9" || p.Groups["gpu"] != "v0.6.8" || p.Nodes["framework"] != "latest" {
		t.Fatalf("bad parse: %+v", p)
	}
}

func TestLoadVersionPinsGarbage(t *testing.T) {
	if _, err := LoadVersionPins(pinsDir(t, "global: [not a string")); err == nil {
		t.Fatal("garbage yaml should error")
	}
}

func TestResolveUnpinned(t *testing.T) {
	tgt, src := ResolveVersion(nil, "friday", []string{"gpu"})
	if tgt != "latest" || src != "unpinned" {
		t.Fatalf("nil pins → latest/unpinned, got %s/%s", tgt, src)
	}
	p := &VersionPins{}
	tgt, src = ResolveVersion(p, "friday", []string{"gpu"})
	if tgt != "latest" || src != "unpinned" {
		t.Fatalf("empty pins → latest/unpinned, got %s/%s", tgt, src)
	}
}

func TestResolveNodeBeatsGroupAndGlobal(t *testing.T) {
	p := &VersionPins{Global: "v0.6.9", Groups: map[string]string{"gpu": "v0.6.8"}, Nodes: map[string]string{"mini": "v0.6.7"}}
	tgt, src := ResolveVersion(p, "mini", []string{"gpu"})
	if tgt != "v0.6.7" || src != "nodes.mini" {
		t.Fatalf("node pin must win, got %s/%s", tgt, src)
	}
}

func TestResolveNodeLatestOverridesGroup(t *testing.T) {
	p := &VersionPins{Groups: map[string]string{"compute": "v0.6.8"}, Nodes: map[string]string{"framework": "latest"}}
	tgt, src := ResolveVersion(p, "framework", []string{"compute"})
	if tgt != "latest" || src != "nodes.framework" {
		t.Fatalf("nodes.latest must override group, got %s/%s", tgt, src)
	}
}

func TestResolveGroupLowestWins(t *testing.T) {
	p := &VersionPins{Groups: map[string]string{"gpu": "v0.6.8", "compute": "v0.6.9"}}
	tgt, src := ResolveVersion(p, "mini", []string{"gpu", "compute"})
	if tgt != "v0.6.8" || src != "groups.gpu" {
		t.Fatalf("lowest group pin must win, got %s/%s", tgt, src)
	}
}

func TestResolveGlobal(t *testing.T) {
	p := &VersionPins{Global: "v0.6.8"}
	tgt, src := ResolveVersion(p, "friday", []string{"gpu"})
	if tgt != "v0.6.8" || src != "global" {
		t.Fatalf("global fallback, got %s/%s", tgt, src)
	}
}

func TestResolveGlobalLatest(t *testing.T) {
	p := &VersionPins{Global: "latest"}
	tgt, _ := ResolveVersion(p, "friday", nil)
	if tgt != "latest" {
		t.Fatalf("global: latest must stay latest, got %s", tgt)
	}
}

func TestResolveNormalizesTags(t *testing.T) {
	p := &VersionPins{Global: "0.6.8"} // no leading v
	tgt, _ := ResolveVersion(p, "friday", nil)
	if tgt != "v0.6.8" {
		t.Fatalf("tags normalize to v-prefix, got %s", tgt)
	}
}

func TestResolveCaseInsensitiveKeys(t *testing.T) {
	p := &VersionPins{Nodes: map[string]string{"Framework": "v0.6.9"}}
	tgt, src := ResolveVersion(p, "framework", nil)
	if tgt != "v0.6.9" || src != "nodes.framework" {
		t.Fatalf("hostname lookup should fold case, got %s/%s", tgt, src)
	}
}

func TestPinSummary(t *testing.T) {
	if s := (*VersionPins)(nil).PinSummary(); s == "" {
		t.Fatal("nil summary should print")
	}
	p := &VersionPins{Global: "latest", Groups: map[string]string{"gpu": "v0.6.8"}}
	if s := p.PinSummary(); s == "" {
		t.Fatal("summary should render")
	}
}

// ── recipe-carried node overrides ───────────────────────────────────────────

func recipeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRecipeOverrideBasic(t *testing.T) {
	dir := recipeRepo(t, map[string]string{
		"recipes/hold/hold.yaml": "name: hold\nnode_overrides:\n  mini:\n    version: v0.6.8\n",
	})
	ov, ok := LoadRecipeVersionOverride(dir, "mini")
	if !ok || ov.Version != "v0.6.8" || ov.Recipe != "hold" {
		t.Fatalf("want hold/v0.6.8, got %+v ok=%v", ov, ok)
	}
	if _, ok := LoadRecipeVersionOverride(dir, "friday"); ok {
		t.Fatal("override must not match other nodes")
	}
}

func TestRecipeOverrideBeatsPinsFile(t *testing.T) {
	dir := recipeRepo(t, map[string]string{
		"versions.yaml":          "global: latest\nnodes:\n  mini: v0.6.9\n",
		"recipes/hold/hold.yaml": "name: hold\nnode_overrides:\n  mini:\n    version: v0.6.8\n",
	})
	tgt, src, err := ResolveUpdateTarget(dir, "mini", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tgt != "v0.6.8" || src != "recipe:hold" {
		t.Fatalf("recipe override must win, got %s/%s", tgt, src)
	}
}

func TestRecipeOverrideLowestWins(t *testing.T) {
	dir := recipeRepo(t, map[string]string{
		"recipes/a/a.yaml": "name: a\nnode_overrides:\n  mini: {version: v0.6.9}\n",
		"recipes/b/b.yaml": "name: b\nnode_overrides:\n  mini: {version: v0.6.8}\n",
	})
	ov, ok := LoadRecipeVersionOverride(dir, "mini")
	if !ok || ov.Version != "v0.6.8" {
		t.Fatalf("lowest must win, got %+v ok=%v", ov, ok)
	}
}

func TestRecipeOverrideLatestLosesToConcrete(t *testing.T) {
	dir := recipeRepo(t, map[string]string{
		"recipes/a/a.yaml": "name: a\nnode_overrides:\n  mini: {version: latest}\n",
		"recipes/b/b.yaml": "name: b\nnode_overrides:\n  mini: {version: v0.6.8}\n",
	})
	ov, ok := LoadRecipeVersionOverride(dir, "mini")
	if !ok || ov.Version != "v0.6.8" {
		t.Fatalf("concrete pin must beat latest, got %+v ok=%v", ov, ok)
	}
}

func TestRecipeOverrideLatestForcesTrack(t *testing.T) {
	// recipe "latest" beats a versions.yaml node pin — the escape hatch
	dir := recipeRepo(t, map[string]string{
		"versions.yaml":      "nodes:\n  mini: v0.6.8\n",
		"recipes/go/go.yaml": "name: go\nnode_overrides:\n  mini: {version: latest}\n",
	})
	tgt, src, _ := ResolveUpdateTarget(dir, "mini", nil)
	if tgt != "latest" || src != "recipe:go" {
		t.Fatalf("recipe latest must override pin, got %s/%s", tgt, src)
	}
}

func TestRecipeOverrideSkipsBrokenFiles(t *testing.T) {
	dir := recipeRepo(t, map[string]string{
		"recipes/bad/bad.yaml":   "name: [broken\n",
		"recipes/hold/hold.yaml": "name: hold\nnode_overrides:\n  mini:\n    version: v0.6.7\n",
	})
	ov, ok := LoadRecipeVersionOverride(dir, "mini")
	if !ok || ov.Version != "v0.6.7" {
		t.Fatalf("broken file must be skipped, got %+v ok=%v", ov, ok)
	}
}

func TestRecipeOverrideNoRecipes(t *testing.T) {
	dir := recipeRepo(t, map[string]string{
		"versions.yaml": "global: latest\n",
	})
	if _, ok := LoadRecipeVersionOverride(dir, "mini"); ok {
		t.Fatal("no recipes → no override")
	}
	tgt, src, err := ResolveUpdateTarget(dir, "mini", nil)
	if err != nil || tgt != "latest" {
		t.Fatalf("clean repo → latest, got %s/%s/%v", tgt, src, err)
	}
}

func TestNormalizePinValue(t *testing.T) {
	for in, want := range map[string]string{
		"latest":   "latest",
		"":         "latest",
		"0.6.8":    "v0.6.8",
		"v0.6.8":   "v0.6.8",
		" v0.6.9 ": "v0.6.9",
	} {
		if got := NormalizePinValue(in); got != want {
			t.Fatalf("NormalizePinValue(%q) = %q, want %q", in, got, want)
		}
	}
}
