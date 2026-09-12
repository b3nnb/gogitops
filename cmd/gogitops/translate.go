package main

import (
	"fmt"
	"strings"
)

// ── Universal step-type translation ─────────────────────────────────────────
// Recipes speak one OS-neutral vocabulary (Linux-leaning); the agent on each
// node translates universal step types into local reality:
//
//	package: figlet              → apt/dnf/zypper/pacman/apk/brew install
//	                               (agent detects the package manager), with
//	                               source fallbacks (pip:<name>)
//	schedule: hourly + command:  → idempotent crontab install (Linux + macOS
//	                               both have cron)
//
// Translation emits a plain bash command string, so translated steps flow
// through the exact same runner machinery (dry-run, retries, expect, attrs).

// parseSourceList parses "[pkg:figlet, pip:pyfiglet]" into its elements.
func parseSourceList(val string) []string {
	// strip inline YAML comments first — "labels: [] # all nodes" must
	// parse as empty, not as a garbage label that never matches
	if idx := strings.Index(val, " #"); idx >= 0 {
		val = val[:idx]
	}
	val = strings.TrimSpace(val)
	val = strings.TrimPrefix(val, "[")
	val = strings.TrimSuffix(val, "]")
	var out []string
	for _, s := range strings.Split(val, ",") {
		s = strings.TrimSpace(s)
		s = unquoteYAML(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// splitSource splits "pkg:figlet" into ("pkg", "figlet", true).
func splitSource(src string) (string, string, bool) {
	i := strings.Index(src, ":")
	if i < 0 {
		return "", "", false
	}
	return src[:i], src[i+1:], true
}

// translatePackage builds a single bash command that installs step.Package
// using the first working source. Source forms: "pkg:<name>" (system package
// manager) and "pip:<name>" (python3 -m pip --user). Empty sources defaults
// to pkg:<package>. Best-effort: exits 0 when already installed or when no
// source works (callers degrade gracefully, e.g. plain-text ASCII fallback).
func translatePackage(step recipeStep) string {
	pkg := step.Package
	sources := step.Sources
	if len(sources) == 0 {
		sources = []string{"pkg:" + pkg}
	}

	var sb strings.Builder
	// Idempotency: already installed as a binary or a python module → skip
	sb.WriteString(fmt.Sprintf("command -v %s >/dev/null 2>&1 && exit 0; ", pkg))
	for _, src := range sources {
		if kind, name, ok := splitSource(src); ok && kind == "pip" {
			sb.WriteString(fmt.Sprintf("python3 -c 'import %s' >/dev/null 2>&1 && exit 0; ", name))
		}
	}
	// Try each source in order
	for _, src := range sources {
		kind, name, ok := splitSource(src)
		if !ok {
			continue
		}
		switch kind {
		case "pkg":
			sb.WriteString(pkgInstallCmd(name))
		case "pip":
			// PEP 668 (Ubuntu 24.04+) blocks bare --user installs → retry with override
			sb.WriteString(fmt.Sprintf("python3 -m pip install --user --quiet %s >/dev/null 2>&1 && exit 0; ", shellQuote(name)))
			sb.WriteString(fmt.Sprintf("python3 -m pip install --user --quiet --break-system-packages %s >/dev/null 2>&1 && exit 0; ", shellQuote(name)))
		}
	}
	// No source worked — best effort, exit clean
	sb.WriteString("exit 0")
	return sb.String()
}

// pkgInstallCmd builds the package-manager-detecting install command for one
// package (port of the former recipes/starship/scripts/install-figlet.sh):
// brew on macOS, apt/dnf/zypper/pacman/apk on Linux; sudo -n when non-root.
func pkgInstallCmd(name string) string {
	q := shellQuote(name)
	return fmt.Sprintf(
		"if command -v brew >/dev/null 2>&1; then brew install %s >/dev/null 2>&1 && exit 0; fi; "+
			"if command -v apt-get >/dev/null 2>&1; then pm='apt-get install -y'; "+
			"elif command -v dnf >/dev/null 2>&1; then pm='dnf install -y'; "+
			"elif command -v zypper >/dev/null 2>&1; then pm='zypper install -y'; "+
			"elif command -v pacman >/dev/null 2>&1; then pm='pacman -S --noconfirm'; "+
			"elif command -v apk >/dev/null 2>&1; then pm='apk add'; "+
			"else pm=''; fi; "+
			"if [ -n \"$pm\" ]; then if [ \"$(id -u)\" = 0 ]; then $pm %s >/dev/null 2>&1 && exit 0; "+
			"elif command -v sudo >/dev/null 2>&1; then sudo -n $pm %s >/dev/null 2>&1 && exit 0; fi; fi; ",
		q, q, q)
}

// dqExpand double-quotes s for bash, escaping ", \ and ` — but leaving $
// unescaped so env refs like $HOME expand at dedup time (legacy cron lines
// written by older recipe versions contain the expanded home path).
func dqExpand(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\', '`':
			sb.WriteByte('\\')
		}
		sb.WriteRune(r)
	}
	sb.WriteByte('"')
	return sb.String()
}

// translateSchedule builds an idempotent crontab-install command. schedule
// accepts presets (hourly, daily, weekly) or a raw 5-field cron spec. The
// installed line is tagged "# gogitops:<step-name>" so re-runs replace (not
// duplicate) the entry; the dedup also removes legacy unmarked lines — both
// the $HOME-expanded form (older recipe versions expanded $HOME at install)
// and the literal form. payload is the substituted command the cron job
// should run.
func translateSchedule(step recipeStep, payload string) string {
	cron := step.Schedule
	switch cron {
	case "hourly":
		cron = "0 * * * *"
	case "daily":
		cron = "0 0 * * *"
	case "weekly":
		cron = "0 0 * * 0"
	}
	marker := "# gogitops:" + step.Name
	line := cron + " " + payload + " " + marker
	return fmt.Sprintf(
		"(crontab -l 2>/dev/null | grep -vF %s | grep -vF %s | grep -vF %s; printf '%%s\\n' %s) | crontab -",
		shellQuote(marker), dqExpand(payload), shellQuote(payload), shellQuote(line))
}
