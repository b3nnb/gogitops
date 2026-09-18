package main

// recipe schema — emit a JSON Schema for recipe YAML so editors autocomplete
// and validate recipes as admins type. Emitted by the binary itself: the
// vocabulary is generated next to the parser, so it never drifts from what
// the agent actually reads. With -repo, fleet labels and hostnames are
// injected as enums — labels autocomplete from mesh.d, node_overrides keys
// from actual hostnames.
//
// Wiring (VS Code, out of the box in this repo): .vscode/settings.json maps
// recipes/**/*.yaml to recipes/recipe-schema.json. The scaffold from
// `recipe new` carries a yaml-language-server modeline for other editors
// (JetBrains, Neovim, Zed all read the same file).
//
// Refresh after agent updates:
//
//	gogitops recipe schema -repo . -out recipes/recipe-schema.json

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/bennbanks/gogitops/internal/config"
)

func cmdRecipeSchema(args []string) {
	fs := flag.NewFlagSet("recipe schema", flag.ExitOnError)
	repoDir := fs.String("repo", "", "inject fleet labels + hostnames from this repo")
	out := fs.String("out", "", "write schema to this file instead of stdout")
	fs.Parse(args)

	var labels, hostnames []string
	if *repoDir != "" {
		resolved := resolveRepoDir(*repoDir)
		if mesh, err := config.LoadMesh(resolved); err == nil {
			seen := map[string]bool{}
			for _, p := range mesh.Peers {
				if p.Hostname != "" && !seen[p.Hostname] {
					seen[p.Hostname] = true
					hostnames = append(hostnames, p.Hostname)
				}
			}
			for _, p := range mesh.Peers {
				for _, l := range p.Labels {
					if l != "" && !seen[l] {
						seen[l] = true
						labels = append(labels, l)
					}
				}
			}
			sort.Strings(labels)
			sort.Strings(hostnames)
		} else {
			fmt.Fprintf(os.Stderr, "⚠ mesh not loadable from %s — schema emitted without fleet enums\n", resolved)
		}
	}

	schema := buildRecipeSchema(labels, hostnames)
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ schema build failed: %v\n", err)
		os.Exit(1)
	}
	data = append(data, '\n')

	if *out != "" {
		if err := os.WriteFile(*out, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "❌ cannot write %s: %v\n", *out, err)
			os.Exit(1)
		}
		fmt.Printf("✅ schema written: %s (%d bytes)\n", *out, len(data))
		return
	}
	os.Stdout.Write(data)
}

// buildRecipeSchema mirrors parseRecipe's vocabulary exactly. Strict
// (additionalProperties: false) on both levels: typos and keys the parser
// silently ignores light up red in the editor instead of doing nothing.
func buildRecipeSchema(labels, hostnames []string) map[string]any {
	str := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	strList := func(desc string, enum []string) map[string]any {
		m := map[string]any{"type": "array", "description": desc,
			"items": map[string]any{"type": "string"}}
		if len(enum) > 0 {
			m["items"].(map[string]any)["enum"] = enum
		}
		return m
	}

	fleetLabels := strList("Which nodes run this recipe (empty = all). Union of every label in mesh.d/ — add new labels to mesh.d first.", labels)
	reqLabels := strList("Node must have ALL of these labels for this step to run.", labels)
	exclLabels := strList("Step is skipped if the node has ANY of these labels.", labels)

	stepProps := map[string]any{
		"name":        str("Unique step identifier within the recipe."),
		"description": str("What this step does (shows in run output + audit log)."),
		"command":     str("Shell command to run. Plain bash — the agent translates per-OS where possible; prefer built-in step types over long bash."),
		"script":      str("Script filename — recipe-local scripts/ is primary, recipes/scripts/ is the shared fallback."),
		"script_args": str("Arguments passed to the script."),
		"script_lang": map[string]any{"type": "string", "enum": []string{"bash", "go", "python3"},
			"description": "Override script language detection."},
		"package":         str("Built-in package step: install this package — agent picks apt/dnf/apk/brew per OS. Idempotent."),
		"sources":         strList("Package name variants per source, e.g. [\"pkg:curl\", \"brew:curl\"] — first matching source wins.", nil),
		"schedule":        str("Built-in schedule step: '5m', 'hourly', or a cron expression — agent installs an idempotent cron/launchd entry. Pair with command: or script:."),
		"os":              map[string]any{"type": "string", "enum": []string{"linux", "darwin", "windows"}, "description": "Only run on this OS."},
		"arch":            map[string]any{"type": "string", "enum": []string{"amd64", "arm64", "arm"}, "description": "Only run on this CPU arch."},
		"expect":          str("Validation: stdout must contain this string."),
		"expect_regex":    str("Validation: stdout must match this regex."),
		"expect_exit":     map[string]any{"type": "integer", "description": "Validation: expected exit code (default 0)."},
		"parse":           map[string]any{"type": "string", "enum": []string{"regex"}, "description": "Parse stdout with `pattern:`."},
		"pattern":         str("Regex for parse: — capture groups become {{1}}, {{2}}, … usable in later steps."),
		"only_if":         str("Skip the step when this condition is false."),
		"when":            str("Shell test — step only runs when this command exits 0. Re-evaluated every run, use to detect capabilities."),
		"on_failure":      map[string]any{"type": "string", "enum": []string{"continue", "abort"}, "description": "What happens when the step fails (default: abort)."},
		"retries":         map[string]any{"type": "integer", "description": "Retry count on failure."},
		"retry_delay":     str("Wait between retries, e.g. 10s."),
		"assert":          str("Assertion expression evaluated against step output."),
		"set_attr":        str("Store stdout as attribute attr.<name> — readable by later steps, the API, and the dashboard."),
		"attr_prefix":     str("Store regex capture groups as attr.<prefix>.1, .2, …"),
		"when_attr":       str("Only run if this attribute condition is true (attr.<name> [== != contains] value)."),
		"only_if_attr":    str("Skip if this attribute condition is true."),
		"labels_required": reqLabels,
		"labels_exclude":  exclLabels,
		"mount":           str("Built-in mount step: volume name (network shares — default path component + macOS /Volumes name), or local device label."),
		"device":          str("Mount step: what to mount — //server/share (SMB), server:/path (NFS), uuid=…, label=…, or /dev/path (local)."),
		"at":              str("Mount step: mount point. Default /mnt/<label> local, /media/<user>/<name> network (Linux); ignored on macOS for network shares)."),
		"options":         str("Mount step: extra mount options (agent adds _netdev, credentials, uid/gid for network shares)."),
		"fstab":           map[string]any{"type": "boolean", "description": "Mount step (local devices): persist to fstab (idempotent; sudo only when needed). Network shares use systemd .mount/.automount units."},
		"credentials":      str("Mount step (network SMB): path to a cifs credentials file. Default ~/.smbcredentials when present; 'none' for guest."),
	}

	nodeOverrideValue := map[string]any{
		"type":                 "object",
		"description":          "Per-node override carried by this recipe — applies to the named node regardless of this recipe's own label scoping.",
		"additionalProperties": false,
		"properties": map[string]any{
			"version": str("Agent version pin, e.g. v0.6.8 or latest — beats versions.yaml for this node."),
		},
	}
	nodeOverrides := map[string]any{
		"type":                 "object",
		"description":          "Per-node global overrides (Benn's rule: every global is overridable per node, from any recipe). Keys are hostnames.",
		"additionalProperties": nodeOverrideValue,
	}
	if len(hostnames) > 0 {
		nodeOverrides["propertyNames"] = map[string]any{"enum": hostnames}
	}

	return map[string]any{
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"title":                "GoGitOps Recipe",
		"description":          "Declarative operational procedure. Emitted by `gogitops recipe schema` — regenerate after agent updates: gogitops recipe schema -repo . -out recipes/recipe-schema.json",
		"type":                 "object",
		"required":             []string{"name", "steps"},
		"additionalProperties": false,
		"properties": map[string]any{
			"name":           str("Recipe identifier — should match the directory and file name."),
			"description":    str("Human-readable description of what this recipe does."),
			"version":        str("Recipe version (bump on breaking changes)."),
			"labels":         fleetLabels,
			"test_module":    map[string]any{"type": "boolean", "description": "Marks this recipe as a test module (excluded from recipe run-all; run via test run)."},
			"node_overrides": nodeOverrides,
			"steps": map[string]any{
				"type":        "array",
				"description": "Ordered steps. Each inherits the runner's guarantees: idempotency, retries, os/arch/label filters, dry-run, expect/assert.",
				"items": map[string]any{
					"type": "object", "required": []string{"name"}, "additionalProperties": false,
					"properties": stepProps,
				},
			},
		},
	}
}

// schemaSummary prints a compact key inventory (used by the help output).
func schemaSummary() string {
	s := buildRecipeSchema(nil, nil)
	props := s["properties"].(map[string]any)
	step := s["properties"].(map[string]any)["steps"].(map[string]any)
	items := step["items"].(map[string]any)
	sprops := items["properties"].(map[string]any)
	tops := make([]string, 0, len(props))
	for k := range props {
		tops = append(tops, k)
	}
	sort.Strings(tops)
	keys := make([]string, 0, len(sprops))
	for k := range sprops {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fmt.Sprintf("top-level: %s | step keys: %s", strings.Join(tops, ", "), strings.Join(keys, ", "))
}
