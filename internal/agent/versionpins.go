package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ── Agent version pins (versions.yaml) ───────────────────────────────────────
//
// The config repo can pin the agent version globally, per label group, or per
// node. Resolution order — most specific wins:
//
//	nodes.<hostname>   exact node override ("latest" = force-track, even if
//	                   a matching group pins older)
//	groups.<label>     any matching node label participates; when several
//	                   groups match, the LOWEST version wins — a node stays
//	                   held back until every group it belongs to is ready
//	global             fleet-wide default
//	latest             no pin: track the binaries-branch VERSION, upgrades
//	                   only (the original self-update behavior)
//
// Pins are exact: a pinned node swaps to precisely that version, downgrades
// included. CI retains every release on the binaries branch at
// versions/<tag>/bin/gogitops-<os>-<arch>, so pins to any previously released
// version stay fetchable.

// VersionPins is the parsed versions.yaml from the config repo root.
type VersionPins struct {
	Global string            `yaml:"global"`
	Groups map[string]string `yaml:"groups"`
	Nodes  map[string]string `yaml:"nodes"`
}

// LoadVersionPins reads <repoDir>/versions.yaml. A missing file is not an
// error — it returns (nil, nil): fully unpinned, legacy behavior.
func LoadVersionPins(repoDir string) (*VersionPins, error) {
	data, err := os.ReadFile(filepath.Join(repoDir, "versions.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var p VersionPins
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("versions.yaml: %v", err)
	}
	return &p, nil
}

// ResolveVersion returns the version target for this node and the source it
// resolved from (for logs and the CLI). target is a normalized tag ("v0.6.8")
// or "latest".
func ResolveVersion(pins *VersionPins, hostname string, labels []string) (target, source string) {
	if pins == nil {
		return "latest", "unpinned"
	}
	if v, ok := lookupFold(pins.Nodes, hostname); ok {
		v = strings.TrimSpace(v)
		if v == "" || v == "latest" {
			return "latest", "nodes." + hostname
		}
		return normalizeTag(v), "nodes." + hostname
	}
	// Groups: concrete versions only — the lowest matching pin wins.
	best, bestSrc := "", ""
	for _, l := range labels {
		v, ok := lookupFold(pins.Groups, l)
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" || v == "latest" {
			continue // only concrete pins hold nodes back
		}
		v = normalizeTag(v)
		if best == "" || isNewer(best, v) {
			best, bestSrc = v, "groups."+l
		}
	}
	if best != "" {
		return best, bestSrc
	}
	if g := strings.TrimSpace(pins.Global); g != "" && g != "latest" {
		return normalizeTag(g), "global"
	}
	return "latest", "unpinned"
}

// PinSummary renders a one-line human summary of the pins file (CLI display).
func (p *VersionPins) PinSummary() string {
	if p == nil {
		return "none (no versions.yaml)"
	}
	var parts []string
	if g := strings.TrimSpace(p.Global); g != "" {
		parts = append(parts, "global="+g)
	}
	keys := make([]string, 0, len(p.Groups))
	for k := range p.Groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, "groups."+k+"="+p.Groups[k])
	}
	keys = keys[:0]
	for k := range p.Nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, "nodes."+k+"="+p.Nodes[k])
	}
	if len(parts) == 0 {
		return "none (empty versions.yaml)"
	}
	return strings.Join(parts, " ")
}

// lookupFold is a case-insensitive map lookup (hostnames/labels are
// conventionally lowercase but pins are hand-written).
func lookupFold(m map[string]string, key string) (string, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

// normalizeTag adds the leading "v" if missing ("0.6.8" → "v0.6.8").
func normalizeTag(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || !strings.HasPrefix(v, "v") {
		return "v" + v
	}
	return v
}

// NormalizePinValue cleans a pin value for resolution: "latest"/empty passes
// through; concrete versions gain the leading "v".
func NormalizePinValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "latest" {
		return "latest"
	}
	return normalizeTag(v)
}

// ── Recipe-carried node overrides ───────────────────────────────────────────
//
// Benn's per-node override rule (Sep 12 2026): EVERY global definition must
// be overridable per node — in its own definition file (versions.yaml), in
// the node's own recipe, or inline in any other recipe. Any recipe may
// carry:
//
//	node_overrides:
//	  mini:
//	    version: v0.6.8
//
// The override names its target node explicitly, so it applies regardless of
// the recipe's own label scoping. Future globals (transparency, alert
// routing, …) gain fields here as they ship. Precedence: recipe override >
// versions.yaml nodes > groups > global > latest; when several recipes
// override the same node, the lowest version wins (stay-back bias —
// "latest" loses to any concrete pin).

// RecipeNodeOverride is the per-node override block any recipe may carry.
type RecipeNodeOverride struct {
	Version string `yaml:"version"`
}

type recipeOverrideFile struct {
	Name          string                        `yaml:"name"`
	NodeOverrides map[string]RecipeNodeOverride `yaml:"node_overrides"`
}

// RecipeVersionOverride is a resolved per-node version pin from a recipe.
type RecipeVersionOverride struct {
	Version string
	Recipe  string
}

// LoadRecipeVersionOverride scans recipes/**/*.yaml for node_overrides and
// returns the version override for hostname. Unparseable recipe files are
// skipped — a broken recipe must never break update resolution.
func LoadRecipeVersionOverride(repoDir, hostname string) (RecipeVersionOverride, bool) {
	root := filepath.Join(repoDir, "recipes")
	var found []RecipeVersionOverride
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var rf recipeOverrideFile
		if err := yaml.Unmarshal(data, &rf); err != nil {
			return nil // skip broken recipe files
		}
		for host, ov := range rf.NodeOverrides {
			if !strings.EqualFold(host, hostname) {
				continue
			}
			v := strings.TrimSpace(ov.Version)
			if v == "" {
				continue
			}
			name := strings.TrimSpace(rf.Name)
			if name == "" {
				name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			}
			found = append(found, RecipeVersionOverride{Version: v, Recipe: name})
		}
		return nil
	})
	if len(found) == 0 {
		return RecipeVersionOverride{}, false
	}
	// Lowest version wins; "latest" ranks highest so any concrete pin beats
	// it. Ties keep the first match — WalkDir is lexical, so deterministic.
	best := found[0]
	bestRank := pinRank(best.Version)
	for _, f := range found[1:] {
		if r := pinRank(f.Version); r != bestRank {
			if rankLess(r, bestRank) {
				best, bestRank = f, r
			}
		}
	}
	return best, true
}

// pinRank orders pin values: concrete semver ascending; "latest" = +inf.
func pinRank(v string) [3]int {
	if strings.TrimSpace(v) == "latest" {
		return [3]int{1 << 30, 0, 0}
	}
	return parseSemVer(v)
}

func rankLess(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// ResolveUpdateTarget computes a node's final version target: recipe
// node_overrides beat the versions.yaml chain (nodes > groups > global).
// Returns the pins parse error for the caller to log (fail-safe: pins are
// ignored when unparseable, the agent keeps tracking latest).
func ResolveUpdateTarget(repoDir, hostname string, labels []string) (target, source string, pinsErr error) {
	pins, pinsErr := LoadVersionPins(repoDir)
	target, source = ResolveVersion(pins, hostname, labels)
	if ov, ok := LoadRecipeVersionOverride(repoDir, hostname); ok {
		target, source = NormalizePinValue(ov.Version), "recipe:"+ov.Recipe
	}
	return target, source, pinsErr
}
