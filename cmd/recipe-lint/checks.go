package main

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

func checkFormat(rf *recipeFile) []Finding {
	var out []Finding

	// Nil guard — rf may be nil if the file couldn't be read.
	if rf == nil {
		return out
	}

	// YAML parse error (rf has data but recipe is nil).
	if rf.recipe == nil {
		// Extract line number from the parse error if present.
		line := 1
		if rf.parseErr != "" {
			// yaml.v3 errors often contain "line N:" — extract it.
			if idx := strings.Index(rf.parseErr, "line "); idx >= 0 {
				rest := rf.parseErr[idx+5:]
				var n int
				fmt.Sscanf(rest, "%d", &n)
				if n > 0 {
					line = n
				}
			}
		}
		msg := "file is not valid YAML or could not be parsed as a recipe"
		if rf.parseErr != "" {
			msg = "YAML parse error: " + rf.parseErr
		}
		out = append(out, Finding{
			File:    rf.path,
			Line:    line,
			Level:   "error",
			Rule:    "yaml-parse",
			Message: msg,
			Suggestion: "fix the YAML syntax error (quote strings containing special characters like : or \")",
		})
		return out
	}

	// Required top-level fields: name, description, version, steps.
	if rf.recipe.Name == "" {
		out = append(out, Finding{
			File:    rf.path,
			Line:    rf.topLevelLine("name"),
			Level:   "error",
			Rule:    "missing-field",
			Message: "required field 'name' is missing or empty",
			Suggestion: "add a top-level 'name:' matching the recipe identifier",
		})
	}
	if rf.recipe.Description == "" {
		out = append(out, Finding{
			File:    rf.path,
			Line:    rf.topLevelLine("description"),
			Level:   "warning",
			Rule:    "missing-field",
			Message: "field 'description' is missing or empty (recommended for all recipes)",
			Suggestion: "add a top-level 'description:' explaining what this recipe does",
		})
	}
	if rf.recipe.Version == "" {
		out = append(out, Finding{
			File:    rf.path,
			Line:    rf.topLevelLine("version"),
			Level:   "warning",
			Rule:    "missing-field",
			Message: "field 'version' is missing or empty (recommended for all recipes)",
			Suggestion: "add a top-level 'version:' e.g. \"1.0.0\"",
		})
	}
	if len(rf.recipe.Steps) == 0 {
		out = append(out, Finding{
			File:    rf.path,
			Line:    rf.topLevelLine("steps"),
			Level:   "error",
			Rule:    "missing-field",
			Message: "required field 'steps' is missing or empty",
			Suggestion: "add a 'steps:' sequence with at least one step",
		})
	}

	// Each step must have a name.
	for i, step := range rf.recipe.Steps {
		if step.Name == "" {
			line := 1
			if i < len(rf.stepNodes) {
				line = rf.stepNodes[i].Line
			}
			out = append(out, Finding{
				File:    rf.path,
				Line:    line,
				Level:   "error",
				Rule:    "step-missing-name",
				Message: fmt.Sprintf("step #%d is missing required field 'name'", i+1),
				Suggestion: "add a 'name:' to this step (unique within the recipe)",
			})
		}
	}

	return out
}

// ── Check 2: Schema compliance ───────────────────────────────────────────

func inEnum(val string, enum []string) bool {
	for _, e := range enum {
		if e == val {
			return true
		}
	}
	return false
}

func checkSchema(rf *recipeFile, enums *schemaEnums) []Finding {
	if rf.recipe == nil {
		return nil
	}
	var out []Finding

	// Top-level labels enum.
	for _, label := range rf.recipe.Labels {
		if !inEnum(label, enums.Labels) {
			out = append(out, Finding{
				File:    rf.path,
				Line:    rf.topLevelLine("labels"),
				Level:   "error",
				Rule:    "label-enum",
				Message: fmt.Sprintf("label '%s' is not in the schema enum (valid: %s)", label, strings.Join(enums.Labels, ", ")),
				Suggestion: fmt.Sprintf("use one of the valid labels: %s", strings.Join(enums.Labels, ", ")),
			})
		}
	}

	for i, step := range rf.recipe.Steps {
		line := rf.stepLine(i, "name")

		// Step must have at least one of: command, script, func, package.
		// (Mount steps are also valid action types.)
		hasAction := step.Command != "" || step.Script != "" || step.Func != "" ||
			step.Package != "" || step.Mount != ""
		if !hasAction {
			out = append(out, Finding{
				File:    rf.path,
				Line:    line,
				Level:   "error",
				Rule:    "step-no-action",
				Message: fmt.Sprintf("step '%s' has no action (needs one of: command, script, func, package, mount)", step.Name),
				Suggestion: "add a 'command:', 'script:', 'func:', 'package:', or 'mount:' field",
			})
		}

		// on_failure enum.
		if step.OnFailure != "" && !inEnum(step.OnFailure, enums.OnFailure) {
			out = append(out, Finding{
				File:    rf.path,
				Line:    rf.stepLine(i, "on_failure"),
				Level:   "error",
				Rule:    "on_failure-enum",
				Message: fmt.Sprintf("step '%s': on_failure '%s' is not valid (must be one of: %s)", step.Name, step.OnFailure, strings.Join(enums.OnFailure, ", ")),
				Suggestion: "use 'abort' or 'continue'",
			})
		}

		// parse enum.
		if step.Parse != "" && !inEnum(step.Parse, enums.Parse) {
			out = append(out, Finding{
				File:    rf.path,
				Line:    rf.stepLine(i, "parse"),
				Level:   "error",
				Rule:    "parse-enum",
				Message: fmt.Sprintf("step '%s': parse '%s' is not valid (must be one of: %s)", step.Name, step.Parse, strings.Join(enums.Parse, ", ")),
				Suggestion: fmt.Sprintf("use one of: %s", strings.Join(enums.Parse, ", ")),
			})
		}

		// func enum.
		if step.Func != "" && !inEnum(step.Func, enums.Func) {
			out = append(out, Finding{
				File:    rf.path,
				Line:    rf.stepLine(i, "func"),
				Level:   "error",
				Rule:    "func-enum",
				Message: fmt.Sprintf("step '%s': func '%s' is not in the function enum (valid: %s)", step.Name, step.Func, strings.Join(enums.Func, ", ")),
				Suggestion: fmt.Sprintf("use one of: %s", strings.Join(enums.Func, ", ")),
			})
		}

		// script_lang enum.
		if step.ScriptLang != "" && !inEnum(step.ScriptLang, enums.ScriptLang) {
			out = append(out, Finding{
				File:    rf.path,
				Line:    rf.stepLine(i, "script_lang"),
				Level:   "error",
				Rule:    "script_lang-enum",
				Message: fmt.Sprintf("step '%s': script_lang '%s' is not valid (must be one of: %s)", step.Name, step.ScriptLang, strings.Join(enums.ScriptLang, ", ")),
				Suggestion: fmt.Sprintf("use one of: %s", strings.Join(enums.ScriptLang, ", ")),
			})
		}

		// os enum.
		if step.OS != "" && !inEnum(step.OS, enums.OS) {
			out = append(out, Finding{
				File:    rf.path,
				Line:    rf.stepLine(i, "os"),
				Level:   "error",
				Rule:    "os-enum",
				Message: fmt.Sprintf("step '%s': os '%s' is not valid (must be one of: %s)", step.Name, step.OS, strings.Join(enums.OS, ", ")),
				Suggestion: fmt.Sprintf("use one of: %s", strings.Join(enums.OS, ", ")),
			})
		}

		// arch enum.
		if step.Arch != "" && !inEnum(step.Arch, enums.Arch) {
			out = append(out, Finding{
				File:    rf.path,
				Line:    rf.stepLine(i, "arch"),
				Level:   "error",
				Rule:    "arch-enum",
				Message: fmt.Sprintf("step '%s': arch '%s' is not valid (must be one of: %s)", step.Name, step.Arch, strings.Join(enums.Arch, ", ")),
				Suggestion: fmt.Sprintf("use one of: %s", strings.Join(enums.Arch, ", ")),
			})
		}

		// labels_required / labels_exclude enums.
		for _, label := range step.LabelsReq {
			if !inEnum(label, enums.LabelsReq) {
				out = append(out, Finding{
					File:    rf.path,
					Line:    rf.stepLine(i, "labels_required"),
					Level:   "error",
					Rule:    "label-enum",
					Message: fmt.Sprintf("step '%s': labels_required '%s' is not in the schema enum", step.Name, label),
					Suggestion: fmt.Sprintf("use one of: %s", strings.Join(enums.LabelsReq, ", ")),
				})
			}
		}
		for _, label := range step.LabelsExcl {
			if !inEnum(label, enums.LabelsExcl) {
				out = append(out, Finding{
					File:    rf.path,
					Line:    rf.stepLine(i, "labels_exclude"),
					Level:   "error",
					Rule:    "label-enum",
					Message: fmt.Sprintf("step '%s': labels_exclude '%s' is not in the schema enum", step.Name, label),
					Suggestion: fmt.Sprintf("use one of: %s", strings.Join(enums.LabelsExcl, ", ")),
				})
			}
		}

		// Duplicate step names.
		for j := 0; j < i; j++ {
			if rf.recipe.Steps[j].Name == step.Name && step.Name != "" {
				out = append(out, Finding{
					File:    rf.path,
					Line:    line,
					Level:   "error",
					Rule:    "duplicate-step-name",
					Message: fmt.Sprintf("step name '%s' is duplicated (first defined at step #%d)", step.Name, j+1),
					Suggestion: "rename this step to be unique within the recipe",
				})
				break
			}
		}
	}

	return out
}

// ── Check 3: Cross-reference validation ──────────────────────────────────

// stepRefRe matches {{.step-name}} (a leading-dot variable referencing a
// prior step's parsed output). Capture group 1 is the step name.
var stepRefRe = regexp.MustCompile(`\{\{\.([a-zA-Z0-9_-]+)\}\}`)

// recipeRunRe matches `gogitops recipe run <name>`. Capture group 1 is the
// recipe name being invoked.
var recipeRunRe = regexp.MustCompile(`gogitops\s+recipe\s+run\s+([a-zA-Z0-9_.\-:]+)`)

func checkCrossRefs(rf *recipeFile, knownRecipes map[string]bool) []Finding {
	if rf.recipe == nil {
		return nil
	}
	var out []Finding

	// Build set of step names in this recipe.
	stepNames := make(map[string]bool)
	for _, step := range rf.recipe.Steps {
		if step.Name != "" {
			stepNames[step.Name] = true
		}
	}

	// System reserved prefixes that look like .X but aren't step refs.
	// e.g. {{.ServerVersion}}, {{.Swarm.LocalNodeState}}, {{.Names}} are
	// docker --format Go templates, not gogitops step refs. We treat any
	// {{.X}} where X contains a dot or is a known docker/template field
	// as NOT a step reference (it's a shell/go-template variable).
	//
	// Real gogitops step refs are {{.step-name}} where step-name is a
	// plain identifier matching a step's name field. The runner stores
	// parsed output by step name; conditions reference the same.

	// Scan each line for {{.X}} references.
	for lineNo, line := range rf.lines {
		matches := stepRefRe.FindAllStringSubmatch(line, -1)
		for _, m := range matches {
			ref := m[1]
			// Skip dots (template subfields like {{.Swarm.Nodes}}).
			if strings.Contains(ref, ".") {
				continue
			}
			// Skip known non-step template fields used by docker/other CLIs.
			if isTemplateField(ref) {
				continue
			}
			if !stepNames[ref] {
				// Only flag in condition: or command: lines, which is
				// where gogitops substitutes step outputs.
				if !looksLikeStepRef(line) {
					continue
				}
				out = append(out, Finding{
					File:    rf.path,
					Line:    lineNo + 1,
					Level:   "error",
					Rule:    "step-ref",
					Message: fmt.Sprintf("{{.%s}} references step '%s' which does not exist in this recipe", ref, ref),
					Suggestion: fmt.Sprintf("add a step named '%s' before this line, or fix the typo", ref),
				})
			}
		}
	}

	// Scan for `gogitops recipe run X` — X must exist in recipes/.
	for lineNo, line := range rf.lines {
		matches := recipeRunRe.FindAllStringSubmatch(line, -1)
		for _, m := range matches {
			recipeName := strings.TrimSuffix(m[1], ".yaml")
			recipeName = strings.TrimSuffix(recipeName, ".yml")
			// Skip comments that are examples only.
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			if !knownRecipes[recipeName] {
				out = append(out, Finding{
					File:    rf.path,
					Line:    lineNo + 1,
					Level:   "error",
					Rule:    "recipe-ref",
					Message: fmt.Sprintf("references recipe '%s' which does not exist in recipes/", recipeName),
					Suggestion: fmt.Sprintf("create recipes/%s.yaml or fix the name", recipeName),
				})
			}
		}
	}

	return out
}

// isTemplateField reports whether ref is a known non-step template field
// (docker --format, Go text/template, etc.).
func isTemplateField(ref string) bool {
	// Common docker --format fields.
	dockerFields := map[string]bool{
		"ServerVersion": true, "OperatingSystem": true, "Swarm": true,
		"Names": true, "Status": true, "ID": true, "Image": true,
		"CreatedAt": true, "RunningFor": true, "Ports": true,
		"Networks": true, "Mounts": true, "Command": true,
		"Platform": true, "Architecture": true, "OSType": true,
		"KernelVersion": true, "Driver": true, "Size": true,
		"Label": true, "Labels": true,
	}
	if dockerFields[ref] {
		return true
	}
	// Go text/template built-ins.
	if ref == "Arch" || ref == "OS" || ref == "Hostname" {
		return true
	}
	return false
}

// looksLikeStepRef reports whether a line is a gogitops step-reference context
// (condition:, when:, command:, or args:). {{.X}} in those fields is treated
// as a step ref; elsewhere (e.g. a raw docker --format) it's a template.
func looksLikeStepRef(line string) bool {
	trimmed := strings.TrimSpace(line)
	for _, key := range []string{"condition:", "when:", "only_if:", "when_attr:"} {
		if strings.HasPrefix(trimmed, key) {
			return true
		}
	}
	// In command/args lines, only flag refs that aren't clearly docker
	// --format usages. Heuristic: if the line contains "--format" it's
	// a CLI template, not a step ref.
	if strings.Contains(line, "--format") || strings.Contains(line, "format '") {
		return false
	}
	if strings.HasPrefix(trimmed, "command:") || strings.HasPrefix(trimmed, "args:") {
		return true
	}
	return false
}

// ── Check 4: Secret detection ────────────────────────────────────────────

type secretPattern struct {
	name    string
	pattern *regexp.Regexp
	level   string // "error" or "warning"
}

var secretPatterns = []secretPattern{
	// High-entropy API keys / tokens (common formats).
	{name: "aws-access-key", pattern: regexp.MustCompile(`AKIA[0-9A-Z]{16}`), level: "error"},
	{name: "aws-secret-key", pattern: regexp.MustCompile(`(?i)aws_secret_access_key\s*[=:]\s*["']?[A-Za-z0-9/+=]{40}`), level: "error"},
	{name: "github-token", pattern: regexp.MustCompile(`(?i)gh[pousr]_[A-Za-z0-9]{36,}`), level: "error"},
	{name: "github-token-classic", pattern: regexp.MustCompile(`(?i)github_token\s*[=:]\s*["'][A-Za-z0-9_]{36,}`), level: "error"},
	{name: "generic-api-key", pattern: regexp.MustCompile(`(?i)(api[_-]?key|apikey)\s*[=:]\s*["'][A-Za-z0-9_\-]{20,}`), level: "warning"},
	{name: "generic-token", pattern: regexp.MustCompile(`(?i)(auth[_-]?token|access[_-]?token|bearer[_-]?token)\s*[=:]\s*["'][A-Za-z0-9_\-]{20,}`), level: "warning"},
	{name: "password-assignment", pattern: regexp.MustCompile(`(?i)password\s*[=:]\s*["'][^"']{8,}`), level: "error"},
	// Private SSH keys.
	{name: "ssh-private-key", pattern: regexp.MustCompile(`-----BEGIN (RSA|EC|OPENSSH|DSA) PRIVATE KEY-----`), level: "error"},
	// Nebula enrollment tokens (Benn's fleet-specific).
	{name: "nebula-enroll-token", pattern: regexp.MustCompile(`(?i)nebula[_-]?enroll[_-]?token\s*[=:]\s*["'][A-Za-z0-9_\-]{16,}`), level: "error"},
	// JWT tokens.
	{name: "jwt", pattern: regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), level: "error"},
	// Slack tokens.
	{name: "slack-token", pattern: regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`), level: "error"},
	// Google API keys.
	{name: "google-api-key", pattern: regexp.MustCompile(`AIza[0-9A-Za-z_\-]{35}`), level: "error"},
}

func scanSecrets(rf *recipeFile) []Finding {
	var out []Finding

	for lineNo, line := range rf.lines {
		// Skip comments.
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Run all secret patterns.
		for _, sp := range secretPatterns {
			if locs := sp.pattern.FindAllStringIndex(line, -1); locs != nil {
				out = append(out, Finding{
					File:    rf.path,
					Line:    lineNo + 1,
					Level:   sp.level,
					Rule:    "secret-" + sp.name,
					Message: fmt.Sprintf("potential %s detected — should not be hardcoded in a public repo", sp.name),
					Suggestion: "move secrets to nenv (netenv) or environment variables; reference via {{vars}} or $ENV",
				})
			}
		}

		// Flag hardcoded private IPs in credential-like contexts.
		out = append(out, scanHardcodedIPs(line, lineNo, rf.path)...)
	}

	return out
}

// scanHardcodedIPs flags private IPs used in credential or sensitive contexts.
// We avoid flagging the many legitimate LAN IPs in commands (mount targets,
// ufw rules) by only flagging them next to credential-assignment keywords.
var privateIPRe = regexp.MustCompile(`\b(10\.\d+\.\d+\.\d+|192\.168\.\d+\.\d+|172\.(1[6-9]|2\d|3[01])\.\d+\.\d+)\b`)

// credAssignRe matches credential-like assignments: keyword=VALUE or
// keyword: VALUE where keyword is a credential term. This avoids false
// positives on words like "token" appearing in "join-token" (a Docker
// Swarm concept) — we require the assignment operator context.
var credAssignRe = regexp.MustCompile(`(?i)(password|passwd|secret|credential|api[_-]?key|auth[_-]?token|access[_-]?token|bearer[_-]?token)\s*[=:]`)

func scanHardcodedIPs(line string, lineNo int, path string) []Finding {
	var out []Finding

	// Only flag if the line contains a credential-assignment pattern.
	if !credAssignRe.MatchString(line) {
		return out
	}

	matches := privateIPRe.FindAllString(line, -1)
	for _, ip := range matches {
		if net.ParseIP(ip) == nil {
			continue
		}
		// Confirm it's a private range.
		parsedIP := net.ParseIP(ip)
		if parsedIP == nil || (!parsedIP.IsPrivate() && !parsedIP.IsLoopback()) {
			continue
		}
		out = append(out, Finding{
			File:    path,
			Line:    lineNo + 1,
			Level:   "warning",
			Rule:    "hardcoded-private-ip",
			Message: fmt.Sprintf("private IP %s appears in a credential/sensitive context — verify this isn't a hardcoded secret", ip),
			Suggestion: "use nenv or a node config value instead of hardcoding the IP",
		})
	}

	return out
}

// ── Check 5: Label consistency (schema enum vs mesh.d) ───────────────────

func checkLabelConsistency(enums *schemaEnums, meshLabels map[string]bool, repoDir string) []Finding {
	var out []Finding

	schemaSet := make(map[string]bool)
	for _, l := range enums.Labels {
		schemaSet[l] = true
	}

	// Labels in schema enum but NOT in any mesh.d node.
	for l := range schemaSet {
		if !meshLabels[l] {
			out = append(out, Finding{
				File:    "recipes/recipe-schema.json",
				Line:    17,
				Level:   "warning",
				Rule:    "label-mesh-mismatch",
				Message: fmt.Sprintf("label '%s' is in the schema enum but not used by any mesh.d node config", l),
				Suggestion: fmt.Sprintf("add '%s' to a node in mesh.d/ or remove it from the schema enum", l),
			})
		}
	}

	// Labels in mesh.d but NOT in schema enum.
	for l := range meshLabels {
		if !schemaSet[l] {
			out = append(out, Finding{
				File:    "mesh.d/",
				Line:    1,
				Level:   "error",
				Rule:    "label-mesh-mismatch",
				Message: fmt.Sprintf("label '%s' is used in mesh.d/ but is not in the schema enum", l),
				Suggestion: fmt.Sprintf("add '%s' to the labels enum in recipes/recipe-schema.json", l),
			})
		}
	}

	return out
}