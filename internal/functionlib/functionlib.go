// Package functionlib is the agent's native function library — typed Go
// functions callable from recipes via `func: <set>.<name>` steps.
//
// The recipe philosophy is thin YAML + fat reusable code. script: steps
// keep bash/Go scripts at arm's length (args in, stdout out); functionlib
// goes one level up: functions run IN-PROCESS with typed inputs and
// outputs, can call each other directly, and their outputs flow to later
// steps as {{func.<key>}} variables — no stdout parsing required.
//
// Two tiers, strictly separated:
//
//   - STDLIB (controlled): registered only from this package's init()
//     (stdlib.go, stdlib_unix.go). Namespaces are reserved — nothing
//     outside this package can ever register storage.*, net.*, system.*.
//     Changes ship with the agent release, are covered by Go tests, and
//     are gated by versions.yaml (a node on an old agent fails cleanly
//     with "function not available").
//   - USER SPACE: repo modules/ (script: <set>/<name>.go) stays the home
//     for custom/complex logic. User modules may CALL stdlib functions
//     (via `gogitops func-run <name>`, or a future import) but can never
//     define anything in a reserved namespace — the module loader
//     rejects collisions. Dependency flows one way: user → stdlib.
//
// Adding a stdlib function: write it in stdlib*.go, register in init(),
// add a unit test. The JSON Schema (recipe schema) picks up new functions
// automatically from List().
package functionlib

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Context is what a function gets about the run. Vars is read-only recipe
// variable state ({{hostname}}, {{repo}}, attrs, …) for lookups; write
// your outputs, not the world.
type Context struct {
	RepoDir  string
	Hostname string
	OS       string
	Arch     string
	Vars     map[string]string
}

// Param documents one typed input. Type is informal ("string", "int",
// "duration") — for editor help and schema descriptions.
type Param struct {
	Name        string
	Type        string
	Description string
}

// Function is one native capability. Name is "<set>.<capability>",
// e.g. "storage.disk-free". Run receives parsed args, returns typed
// outputs — every value must stringify cleanly (they become
// {{func.<key>}} vars and attr strings downstream).
type Function struct {
	Name        string
	Description string
	Params      []Param
	Run         func(ctx Context, args map[string]any) (map[string]any, error)
}

var (
	registry = map[string]Function{}
	// reserved holds the stdlib set names. Set in initStdlib — the ONLY
	// writer. Once reserved, a set name can never be taken by anyone.
	reserved = map[string]bool{}
)

// reservedByStdlib flips a set name to stdlib-owned. Called exclusively
// from this package's init — a non-stdlib registration of a reserved
// set is a programming error and panics at startup, never at runtime.
func reservedByStdlib(set string) {
	if reserved[set] {
		panic("functionlib: stdlib set " + set + " registered twice")
	}
	reserved[set] = true
}

// Register adds a function to the registry. Stdlib only (see package
// doc for the two-tier split) — a name in a reserved set, a duplicate
// name, or a malformed name panics here at init time so the binary
// never ships with a broken library.
func Register(f Function) {
	set, _, err := SplitName(f.Name)
	if err != nil {
		panic("functionlib: bad function name " + fmt.Sprintf("%q", f.Name) + ": " + err.Error())
	}
	if !reserved[set] {
		panic("functionlib: set " + set + " is not a stdlib set — user functions live in repo modules/, never the native registry")
	}
	if _, dup := registry[f.Name]; dup {
		panic("functionlib: duplicate function " + f.Name)
	}
	if f.Run == nil {
		panic("functionlib: function " + f.Name + " has no Run")
	}
	registry[f.Name] = f
}

// Get looks a function up by name.
func Get(name string) (Function, bool) {
	f, ok := registry[name]
	return f, ok
}

// List returns every registered function, sorted by name.
func List() []Function {
	out := make([]Function, 0, len(registry))
	for _, f := range registry {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ReservedSets returns the stdlib-owned set names, sorted. The module
// loader uses this to reject user modules that try to define files in
// these namespaces.
func ReservedSets() []string {
	sets := make([]string, 0, len(reserved))
	for s := range reserved {
		sets = append(sets, s)
	}
	sort.Strings(sets)
	return sets
}

// SplitName validates "<set>.<capability>" and returns its parts.
func SplitName(name string) (set, capability string, err error) {
	parts := strings.Split(name, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("must be <set>.<name>, e.g. storage.disk-free")
	}
	for _, p := range parts {
		for _, c := range p {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", "", fmt.Errorf("lowercase alphanumeric + dash only")
			}
		}
	}
	return parts[0], parts[1], nil
}

// IsReservedSet reports whether set is stdlib-owned — the collision
// guard for user space (repo modules/, recipe scripts/).
func IsReservedSet(set string) bool {
	return reserved[set]
}

// ParseArgs turns a recipe `args:` string into a typed arg map. Two
// forms are accepted, both after {{var}} substitution by the runner:
//
//	key=value,key2=value2      comma pairs (shlex-safest for yaml)
//	{"key": "value", ...}      JSON object (needed when values contain commas)
//
// Values stay strings; coercion is the function's business (keep it
// forgiving — strconv where needed).
func ParseArgs(s string) (map[string]any, error) {
	out := map[string]any{}
	s = strings.TrimSpace(s)
	if s == "" {
		return out, nil
	}
	if strings.HasPrefix(s, "{") {
		// JSON — parse lazily to keep this package dependency-light
		return parseJSONArgs(s)
	}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("bad arg %q (want key=value)", pair)
		}
		out[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	}
	return out, nil
}

// parseJSONArgs handles the {…} args form (values containing commas).
func parseJSONArgs(s string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	raw := map[string]any{}
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("bad JSON args: %w", err)
	}
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		switch t := v.(type) {
		case string:
			out[k] = t
		case json.Number:
			out[k] = t.String()
		case bool:
			out[k] = fmt.Sprintf("%v", t)
		default:
			b, _ := json.Marshal(t)
			out[k] = string(b)
		}
	}
	return out, nil
}

// FormatOutputs renders typed outputs as a space-separated key=value
// line — the step "stdout". This keeps the entire existing runner
// pipeline (expect:, parse:, set_attr:, attr_prefix:) working on native
// function steps unchanged.
func FormatOutputs(out map[string]any) string {
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, out[k]))
	}
	return strings.Join(parts, " ")
}
