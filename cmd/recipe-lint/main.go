// Package main implements recipe-lint — a static validator/linter for gogitops
// recipe YAML files.
//
// Recipe types are mirrored from cmd/gogitops/main.go (package main, not
// importable) with the Condition field added (used in recipes but not in the
// runner struct). The schema enum and mesh.d label set are loaded at startup
// so checks can validate labels against both sources.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Version is the recipe-lint release version. Overridden at build time with:
//   go build -ldflags "-X main.Version=1.0.0" ./cmd/recipe-lint/
var Version = "dev"

// ── Recipe types (mirrors cmd/gogitops/main.go ~line 2049) ─────────────────

type recipeStep struct {
	Name         string      `yaml:"name"`
	Description  string      `yaml:"description"`
	Command      string      `yaml:"command"`
	Script       string      `yaml:"script"`
	ScriptArgs   string      `yaml:"script_args"`
	ScriptLang   string      `yaml:"script_lang"`
	Func         string      `yaml:"func"`
	FuncArgs     interface{} `yaml:"args"` // can be string or JSON object
	Package      string      `yaml:"package"`
	Sources      interface{} `yaml:"sources"` // can be []string or a string
	Schedule     string      `yaml:"schedule"`
	OS           string      `yaml:"os"`
	Arch         string      `yaml:"arch"`
	LabelsReq    []string    `yaml:"labels_required"`
	LabelsExcl   []string    `yaml:"labels_exclude"`
	Expect       string      `yaml:"expect"`
	ExpectRegex  string      `yaml:"expect_regex"`
	ExpectExit   *int        `yaml:"expect_exit"`
	Parse        string      `yaml:"parse"`
	Pattern      string      `yaml:"pattern"`
	OnlyIf       string      `yaml:"only_if"`
	When         string      `yaml:"when"`
	OnFailure    string      `yaml:"on_failure"`
	Retries      int         `yaml:"retries"`
	RetryDelay   string      `yaml:"retry_delay"`
	Assert       string      `yaml:"assert"`
	SetAttr      string      `yaml:"set_attr"`
	AttrPrefix   string      `yaml:"attr_prefix"`
	WhenAttr     string      `yaml:"when_attr"`
	OnlyIfAttr   string      `yaml:"only_if_attr"`
	Mount        string      `yaml:"mount"`
	MountDevice  string      `yaml:"device"`
	MountAt      string      `yaml:"at"`
	MountOptions string      `yaml:"options"`
	MountFstab   bool         `yaml:"fstab"`
	MountCreds   string      `yaml:"credentials"`
	Tags         *stepTags   `yaml:"tags"`
	Condition    string      `yaml:"condition"` // not in runner struct; used in recipes for step-ref conditions
}

type stepTags struct {
	Add    []string `yaml:"add"`
	Remove []string `yaml:"remove"`
}

type recipeParam struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
}

type recipe struct {
	Name        string        `yaml:"name"`
	Description string        `yaml:"description"`
	Version     string        `yaml:"version"`
	Labels      []string      `yaml:"labels"`
	TestModule  bool          `yaml:"test_module"`
	AutoApply   bool          `yaml:"auto_apply"`
	Params      []recipeParam `yaml:"params"`
	Steps       []recipeStep  `yaml:"steps"`
}

// ── Finding ───────────────────────────────────────────────────────────────

type Finding struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Level      string `json:"level"` // "error" | "warning" | "info"
	Rule       string `json:"rule"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

// ── Parsed recipe file with line tracking ─────────────────────────────────

type recipeFile struct {
	path      string       // relative path (for display)
	absPath   string       // absolute path
	data      []byte        // raw file content
	recipe    *recipe       // decoded struct (nil if parse error)
	rootMap   *yaml.Node    // root mapping node (for line lookups)
	stepNodes []*yaml.Node  // per-step mapping nodes
	lines     []string      // raw lines for line-by-line scanning
	parseErr  string        // YAML parse error message (empty if OK)
}

// topLevelLine returns the line number of a top-level key, or 1 if absent.
func (rf *recipeFile) topLevelLine(key string) int {
	if rf.rootMap == nil || rf.rootMap.Kind != yaml.MappingNode {
		return 1
	}
	for i := 0; i+1 < len(rf.rootMap.Content); i += 2 {
		if rf.rootMap.Content[i].Value == key {
			return rf.rootMap.Content[i].Line
		}
	}
	return 1
}

// stepLine returns the line number of a key within step idx, or the step's
// first line if the key is absent.
func (rf *recipeFile) stepLine(idx int, key string) int {
	if idx >= len(rf.stepNodes) {
		return 1
	}
	node := rf.stepNodes[idx]
	if node.Kind != yaml.MappingNode {
		return node.Line
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i].Line
		}
	}
	return node.Line
}

// hasNameKey reports whether the raw YAML data contains a top-level name: key.
// Used to distinguish recipe files from data files (e.g. mesh.yaml).
func hasNameKey(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "name:") || strings.HasPrefix(trimmed, "name :") {
			return true
		}
	}
	return false
}

// loadRecipeFile parses a YAML file, returning the decoded recipe and node
// tree for line tracking. Returns an error if YAML parsing fails.
func loadRecipeFile(absPath, relPath string) (*recipeFile, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}

	rf := &recipeFile{
		path:    relPath,
		absPath: absPath,
		data:    data,
		lines:   strings.Split(string(data), "\n"),
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		rf.parseErr = err.Error()
		return rf, err
	}

	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		rf.rootMap = root.Content[0]
		// Decode into struct for data access.
		var r recipe
		if err := root.Decode(&r); err != nil {
			rf.parseErr = err.Error()
			return rf, err
		}
		rf.recipe = &r
		// Find the steps sequence node for per-step line tracking.
		if rf.rootMap != nil && rf.rootMap.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(rf.rootMap.Content); i += 2 {
				if rf.rootMap.Content[i].Value == "steps" && rf.rootMap.Content[i+1].Kind == yaml.SequenceNode {
					rf.stepNodes = rf.rootMap.Content[i+1].Content
				}
			}
		}
	}

	return rf, nil
}

// ── Schema enum loading ───────────────────────────────────────────────────

// schemaEnums holds the enum values extracted from recipe-schema.json.
type schemaEnums struct {
	Labels        []string
	OnFailure     []string
	Parse         []string
	Func          []string
	ScriptLang    []string
	OS            []string
	Arch          []string
	LabelsReq     []string
	LabelsExcl    []string
}

func toStrSlice(v interface{}) []string {
	if arr, ok := v.([]interface{}); ok {
		out := make([]string, 0, len(arr))
		for _, e := range arr {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// loadSchemaEnums reads recipe-schema.json and extracts all enum arrays.
func loadSchemaEnums(path string) (*schemaEnums, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var schema map[string]interface{}
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("schema JSON parse: %w", err)
	}

	se := &schemaEnums{}
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		return se, nil
	}

	// Top-level labels enum.
	if labels, ok := props["labels"].(map[string]interface{}); ok {
		if items, ok := labels["items"].(map[string]interface{}); ok {
			se.Labels = toStrSlice(items["enum"])
		}
	}

	// Step-level property enums.
	if steps, ok := props["steps"].(map[string]interface{}); ok {
		if items, ok := steps["items"].(map[string]interface{}); ok {
			if stepProps, ok := items["properties"].(map[string]interface{}); ok {
				getEnum := func(key string) []string {
					if pm, ok := stepProps[key].(map[string]interface{}); ok {
						return toStrSlice(pm["enum"])
					}
					return nil
				}
				getItemEnum := func(key string) []string {
					if pm, ok := stepProps[key].(map[string]interface{}); ok {
						if im, ok := pm["items"].(map[string]interface{}); ok {
							return toStrSlice(im["enum"])
						}
					}
					return nil
				}
				se.OnFailure = getEnum("on_failure")
				se.Parse = getEnum("parse")
				se.Func = getEnum("func")
				se.ScriptLang = getEnum("script_lang")
				se.OS = getEnum("os")
				se.Arch = getEnum("arch")
				se.LabelsReq = getItemEnum("labels_required")
				se.LabelsExcl = getItemEnum("labels_exclude")
			}
		}
	}

	return se, nil
}

// ── Mesh.d label loading ──────────────────────────────────────────────────

type meshPeer struct {
	Hostname string   `yaml:"hostname"`
	Labels   []string `yaml:"labels"`
}

type meshConfig struct {
	Peers []meshPeer `yaml:"peers"`
}

// loadMeshLabels reads all *.yaml in dir and returns the union of labels and
// per-host label sets.
func loadMeshLabels(dir string) (allLabels map[string]bool, perHost map[string][]string, err error) {
	allLabels = make(map[string]bool)
	perHost = make(map[string][]string)

	files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	for _, f := range files {
		data, e := os.ReadFile(f)
		if e != nil {
			continue
		}
		var mc meshConfig
		if e := yaml.Unmarshal(data, &mc); e != nil {
			continue
		}
		for _, peer := range mc.Peers {
			perHost[peer.Hostname] = append(perHost[peer.Hostname], peer.Labels...)
			for _, l := range peer.Labels {
				allLabels[l] = true
			}
		}
	}
	return allLabels, perHost, nil
}

// ── Output ────────────────────────────────────────────────────────────────

func outputText(findings []Finding, fix bool) {
	var errs, warns, infos int
	for _, f := range findings {
		switch f.Level {
		case "error":
			errs++
		case "warning":
			warns++
		case "info":
			infos++
		}
		fmt.Fprintf(os.Stdout, "%s:%d: [%s] %s\n", f.File, f.Line, strings.ToUpper(f.Level), f.Message)
		if fix && f.Suggestion != "" {
			fmt.Fprintf(os.Stdout, "  → fix: %s\n", f.Suggestion)
		}
	}
	fmt.Fprintf(os.Stdout, "\n%d error(s), %d warning(s), %d info(s)\n", errs, warns, infos)
}

func outputJSON(findings []Finding) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(findings)
}

// ── Main ──────────────────────────────────────────────────────────────────

func main() {
	repoDir := flag.String("repo", ".", "path to gogitops repo root")
	recipesDir := flag.String("recipes-dir", "", "path to recipes/ dir (default: <repo>/recipes)")
	schemaPath := flag.String("schema", "", "path to recipe-schema.json (default: <recipes>/recipe-schema.json)")
	meshDir := flag.String("mesh-dir", "", "path to mesh.d/ dir (default: <repo>/mesh.d)")
	jsonOut := flag.Bool("json", false, "output findings as JSON (for CI integration)")
	fix := flag.Bool("fix", false, "print suggested fixes (does not auto-apply)")
	versionFlag := flag.Bool("version", false, "print version and exit")
	help := flag.Bool("h", false, "show usage")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "recipe-lint — gogitops recipe YAML validator/linter\n\n")
		fmt.Fprintf(os.Stderr, "Usage: recipe-lint [flags] [path...]\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExits non-zero if any error-level findings are reported.\n")
	}
	flag.Parse()

	if *versionFlag {
		fmt.Fprintf(os.Stdout, "recipe-lint %s\n", Version)
		os.Exit(0)
	}

	if *help {
		flag.Usage()
		os.Exit(0)
	}

	// Resolve paths.
	if *recipesDir == "" {
		*recipesDir = filepath.Join(*repoDir, "recipes")
	}
	if *schemaPath == "" {
		*schemaPath = filepath.Join(*recipesDir, "recipe-schema.json")
	}
	if *meshDir == "" {
		*meshDir = filepath.Join(*repoDir, "mesh.d")
	}

	// Load schema enums.
	enums, err := loadSchemaEnums(*schemaPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "recipe-lint: cannot load schema %s: %v\n", *schemaPath, err)
		os.Exit(2)
	}

	// Load mesh.d labels.
	meshLabels, _, err := loadMeshLabels(*meshDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "recipe-lint: cannot load mesh.d from %s: %v\n", *meshDir, err)
		os.Exit(2)
	}

	// Find recipe YAML files.
	var recipePaths []string
	args := flag.Args()
	if len(args) > 0 {
		for _, a := range args {
			abs, _ := filepath.Abs(a)
			recipePaths = append(recipePaths, abs)
		}
	} else {
		filepath.WalkDir(*recipesDir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			if ext != ".yaml" && ext != ".yml" {
				return nil
			}
			// Skip the schema file itself.
			if filepath.Base(p) == "recipe-schema.json" {
				return nil
			}
			// Skip data files (no top-level name: key).
			data, e := os.ReadFile(p)
			if e != nil || !hasNameKey(data) {
				return nil
			}
			recipePaths = append(recipePaths, p)
			return nil
		})
	}

	// Load and parse all recipe files.
	var files []*recipeFile
	recipeNames := make(map[string]bool)
	for _, p := range recipePaths {
		rf, err := loadRecipeFile(p, relPath(*repoDir, p))
		if err != nil {
			// YAML parse error — record as a finding (rf may be nil on read error).
			if rf != nil {
				files = append(files, rf) // rf has raw data even if recipe is nil
			}
			continue
		}
		if rf.recipe != nil && rf.recipe.Name != "" {
			recipeNames[rf.recipe.Name] = true
		}
		files = append(files, rf)
	}

	// Run all checks.
	var findings []Finding

	// Check 1+2+3: per-file format, schema, cross-ref, secrets.
	for _, rf := range files {
		findings = append(findings, checkFormat(rf)...)
		findings = append(findings, checkSchema(rf, enums)...)
		findings = append(findings, checkCrossRefs(rf, recipeNames)...)
		findings = append(findings, scanSecrets(rf)...)
	}

	// Check 5: label consistency (schema enum vs mesh.d).
	findings = append(findings, checkLabelConsistency(enums, meshLabels, *repoDir)...)

	// Sort by file, then line.
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		return findings[i].Line < findings[j].Line
	})

	// Output.
	if *jsonOut {
		outputJSON(findings)
	} else {
		outputText(findings, *fix)
	}

	// Exit non-zero on any error-level finding.
	for _, f := range findings {
		if f.Level == "error" {
			os.Exit(1)
		}
	}
}

// relPath returns a display-friendly relative path.
func relPath(base, p string) string {
	r, err := filepath.Rel(base, p)
	if err != nil {
		return p
	}
	return r
}