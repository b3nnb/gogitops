// Package agentmodules ships module SETS inside the agent binary.
//
// The recipe philosophy is thin YAML + fat reusable code. Recipes should
// reference capabilities, not embed pages of bash. This package gives the
// agent its own standard library: Go modules embedded at build time,
// extracted to ~/.cache/gogitops/agent-modules/<manifest>/ on first use,
// resolved by `script: <set>/<name>.go` AFTER recipe-local scripts/ and
// the repo's modules/ library (admins can override any embedded module).
//
// Adding a module: drop a .go file under modules/<set>/. The first comment
// line is its description (shown by `gogitops modules`). Each module is a
// standalone main package — args via script_args, attrs via stdout.
package agentmodules

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed modules
var embedded embed.FS

// manifest hashes the embedded tree (sorted names + sizes) so the
// extraction dir changes whenever the embedded content changes — release
// bumps and dev builds alike. No version-string games needed.
func manifest() string {
	type entry struct {
		name string
		size int64
	}
	var entries []entry
	_ = fs.WalkDir(embedded, "modules", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			info, _ := d.Info()
			entries = append(entries, entry{p, info.Size()})
		}
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	h := uint64(1469598103934665603)
	for _, e := range entries {
		for _, b := range []byte(e.name) {
			h = (h ^ uint64(b)) * 1099511628211
		}
		h = (h ^ uint64(e.size)) * 1099511628211
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, 12)
	for i := 0; i < 6; i++ {
		out = append(out, hexdigits[(h>>(uint(i)*8+4))&0xf], hexdigits[(h>>(uint(i)*8))&0xf])
	}
	return string(out)
}

// Dir returns the extraction root for the current embedded tree,
// extracting on first use and pruning older extractions. Empty string on
// failure (callers treat embedded sets as unavailable).
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	root := filepath.Join(home, ".cache", "gogitops", "agent-modules", manifest())
	if _, err := os.Stat(filepath.Join(root, ".extracted")); err == nil {
		return root
	}
	_ = os.MkdirAll(root, 0755)
	_ = fs.WalkDir(embedded, "modules", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, rerr := embedded.ReadFile(p)
		if rerr != nil {
			return nil
		}
		rel := strings.TrimPrefix(p, "modules"+string(filepath.Separator))
		rel = strings.TrimPrefix(rel, "modules/")
		dst := filepath.Join(root, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(dst), 0755)
		_ = os.WriteFile(dst, data, 0644)
		return nil
	})
	_ = os.WriteFile(filepath.Join(root, ".extracted"), []byte("ok"), 0644)

	// Prune other manifests — each agent version extracts its own.
	if parent := filepath.Dir(root); parent != root {
		if entries, err := os.ReadDir(parent); err == nil {
			for _, e := range entries {
				if e.IsDir() && e.Name() != filepath.Base(root) {
					_ = os.RemoveAll(filepath.Join(parent, e.Name()))
				}
			}
		}
	}
	return root
}

// Module describes one embedded module.
type Module struct {
	Set         string // e.g. "storage"
	Name        string // e.g. "mount-info.go"
	Path        string // slash path under modules/
	Description string // first comment line
}

// List returns every embedded module, sorted by set then name.
func List() []Module {
	var out []Module
	_ = fs.WalkDir(embedded, "modules", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel := strings.TrimPrefix(p, "modules/")
		set := strings.SplitN(rel, "/", 2)
		if len(set) != 2 {
			return nil
		}
		desc := ""
		if data, rerr := embedded.ReadFile(p); rerr == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				line = strings.TrimPrefix(line, "//")
				desc = strings.TrimSpace(line)
				break
			}
		}
		out = append(out, Module{Set: set[0], Name: set[1], Path: rel, Description: desc})
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Set != out[j].Set {
			return out[i].Set < out[j].Set
		}
		return out[i].Name < out[j].Name
	})
	return out
}
