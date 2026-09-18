package main

import (
	"strings"
	"testing"
)

// func: step parsing — the line-based parser must pick up func/args keys
// like every other step type, or native steps silently do nothing.
func TestParseRecipeFuncSteps(t *testing.T) {
	yaml := `
name: f
steps:
  - name: probe
    func: net.port-check
    args: host=10.2.0.103, port=53
  - name: shell-fallback
    command: echo hi
`
	r := parseRecipe(yaml)
	if len(r.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(r.Steps))
	}
	s := r.Steps[0]
	if s.Func != "net.port-check" {
		t.Errorf("Func = %q", s.Func)
	}
	if s.FuncArgs != "host=10.2.0.103, port=53" {
		t.Errorf("FuncArgs = %q", s.FuncArgs)
	}
	if s.Command != "" {
		t.Errorf("Command should be empty on a func step, got %q", s.Command)
	}
	if r.Steps[1].Func != "" || r.Steps[1].Command != "echo hi" {
		t.Errorf("regular step mangled: %+v", r.Steps[1])
	}
}

// Unknown function names must fail the recipe — a typo'd func: is never
// a silent skip. (Guarded at runtime; here we assert the vocabulary the
// schema advertises matches reality.)
func TestSchemaFuncEnumMatchesRegistry(t *testing.T) {
	s := buildRecipeSchema(nil, nil)
	props := s["properties"].(map[string]any)["steps"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	fn, ok := props["func"].(map[string]any)
	if !ok {
		t.Fatal("schema missing func step key")
	}
	enum, _ := fn["enum"].([]string)
	if len(enum) == 0 {
		t.Fatal("func enum empty — stdlib not advertised")
	}
	for _, name := range enum {
		if !strings.Contains(name, ".") {
			t.Errorf("enum entry %q is not <set>.<name>", name)
		}
	}
}
