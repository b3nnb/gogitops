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