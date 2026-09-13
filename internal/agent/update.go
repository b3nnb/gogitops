package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// ── Agent self-update (git binaries-branch transport) ───────────────────────
//
// The fleet serves agent updates through the same git repo + deploy key every
// agent already uses for config pulls — no HTTP endpoint required, works from
// any network position that can reach github.com (the LAN dashboard is not
// reachable from client-side subnets; releases.bennbot.com was never built).
//
// Branch layout (republished each release, prior versions retained):
//
//	binaries/VERSION                            e.g. "v0.6.9" — latest
//	binaries/bin/gogitops-<goos>-<goarch>       latest raw binaries
//	binaries/versions/<tag>/bin/gogitops-...    every past release (pin store)
//
// Opt-in by presence: no branch = feature silently off.
// The version TARGET comes from versions.yaml in the config repo (see
// versionpins.go): a pin is exact (downgrades included); unpinned nodes
// track VERSION, upgrade-only.
// After a successful swap the agent exec-restarts in place.

// BinariesBranch is the git branch serving pre-built agent binaries.
const BinariesBranch = "binaries"

// maybeSelfUpdate resolves this node's version target (versions.yaml pin, or
// the binaries-branch VERSION) and hot-swaps the running binary when needed.
// Called after each git pull cycle.
func (a *Agent) maybeSelfUpdate() {
	if a.repoDir == "" {
		return
	}

	// Fetch the binaries branch (absent branch = feature off, stay quiet)
	fetch := exec.Command("git", "fetch", "-q", "origin", BinariesBranch)
	fetch.Dir = a.repoDir
	if err := fetch.Run(); err != nil {
		return
	}
	remote := "origin/" + BinariesBranch

	// Version policy: exact pin (node > group > global) or track latest.
	pins, perr := LoadVersionPins(a.repoDir)
	if perr != nil {
		a.logger.Errorf("update", "%v — ignoring pins this cycle", perr)
	}
	target, source := ResolveVersion(pins, a.node.Hostname, a.node.Labels)

	if target != "latest" {
		// Pinned: exact-match semantics — downgrades allowed.
		if target == normalizeTag(Version) {
			return // already running the pinned version
		}
		binPath := fmt.Sprintf("versions/%s/bin/gogitops-%s-%s", target, runtime.GOOS, runtime.GOARCH)
		bin, err := gitShowBytes(a.repoDir, remote, binPath)
		if err != nil {
			a.logger.Errorf("update", "pinned %s (%s) not fetchable from binaries branch — staying on v%s", target, source, Version)
			return
		}
		a.swapAndRestart(bin, target, "pinned via "+source)
		return
	}

	// Unpinned: track the published VERSION, upgrades only.
	vOut, err := gitShowBytes(a.repoDir, remote, "VERSION")
	if err != nil {
		return
	}
	published := strings.TrimSpace(string(vOut))
	if published == "" || !isNewer(published, Version) {
		return
	}

	// Download this platform's binary
	binPath := fmt.Sprintf("bin/gogitops-%s-%s", runtime.GOOS, runtime.GOARCH)
	bin, err := gitShowBytes(a.repoDir, remote, binPath)
	if err != nil {
		a.logger.Errorf("update", "download %s failed: %v", binPath, err)
		return
	}
	a.swapAndRestart(bin, published, "binaries branch")
}

// swapAndRestart atomically replaces the running binary and exec-restarts.
func (a *Agent) swapAndRestart(bin []byte, toVer, reason string) {
	if len(bin) < 500*1024 {
		a.logger.Errorf("update", "downloaded %s too small (%d bytes) — refusing", toVer, len(bin))
		return
	}

	// Swap atomically: write temp alongside, rename over the running binary.
	// (rename-over-running-exe is legal on Linux and macOS; direct write is not)
	exe, err := os.Executable()
	if err != nil {
		return
	}
	tmp := filepath.Join(filepath.Dir(exe), ".gogitops-update-"+toVer)
	if err := os.WriteFile(tmp, bin, 0755); err != nil {
		a.logger.Errorf("update", "cannot write update (binary location not writable?): %v", err)
		return
	}

	// Apple Silicon SIGKILLs unsigned binaries at exec — ad-hoc sign on macOS
	if runtime.GOOS == "darwin" {
		sign := exec.Command("codesign", "--force", "--sign", "-", tmp)
		if out, err := sign.CombinedOutput(); err != nil {
			os.Remove(tmp)
			a.logger.Errorf("update", "codesign failed: %s", strings.TrimSpace(string(out)))
			return
		}
	}

	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		a.logger.Errorf("update", "swap failed: %v", err)
		return
	}

	a.logger.Actionf("update", "self-updated v%s -> v%s (%s) — restarting in place", Version, toVer, reason)

	// Restart in place: syscall.Exec replaces this process with the new
	// binary, keeping the same PID/lineage — no reliance on the service
	// manager (or spawn context) to relaunch us. (cron/launchd-spawned
	// restarts hang pre-main on macOS — exec sidesteps that entirely.)
	// Fall back to exit 0 (systemd Restart=always / cron keepalive) if
	// exec fails.
	if runtime.GOOS != "windows" {
		if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
			a.logger.Errorf("update", "exec restart failed (%v) — falling back to exit", err)
		}
	}
	// Service manager (Restart=always / cron keepalive) restarts us now.
	os.Exit(0)
}

// gitShowBytes returns a file's content from a remote ref via git transport.
func gitShowBytes(repoDir, remote, path string) ([]byte, error) {
	show := exec.Command("git", "show", remote+":"+path)
	show.Dir = repoDir
	return show.Output()
}

// CheckBinaryUpdate returns the VERSION published on the binaries branch
// ("" if absent). Shared by the CLI update check.
func CheckBinaryUpdate(repoDir string) (string, error) {
	if repoDir == "" {
		return "", fmt.Errorf("no repo directory")
	}
	fetch := exec.Command("git", "fetch", "-q", "origin", BinariesBranch)
	fetch.Dir = repoDir
	if err := fetch.Run(); err != nil {
		return "", fmt.Errorf("fetch %s branch: %v", BinariesBranch, err)
	}
	show := exec.Command("git", "show", "origin/"+BinariesBranch+":VERSION")
	show.Dir = repoDir
	out, err := show.Output()
	if err != nil {
		return "", nil // branch without VERSION = dormant
	}
	return strings.TrimSpace(string(out)), nil
}

// IsNewer exposes the semver comparison for the CLI.
func IsNewer(candidate, current string) bool { return isNewer(candidate, current) }

// isNewer reports whether candidate is a newer semver than current.
// "dev" / empty parses to 0.0.0 — dev builds always adopt the release.
func isNewer(candidate, current string) bool {
	c := parseSemVer(candidate)
	cur := parseSemVer(current)
	for i := 0; i < 3; i++ {
		if c[i] > cur[i] {
			return true
		}
	}
	return false
}

// parseSemVer parses "v0.5.4" / "0.5.4" / "dev" into [major, minor, patch],
// stopping each segment at the first non-digit (v0.5.4-rc1 → 0.5.4).
func parseSemVer(s string) [3]int {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	var out [3]int
	for i, part := range strings.SplitN(s, ".", 3) {
		n := 0
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				break
			}
			n = n*10 + int(ch-'0')
		}
		out[i] = n
	}
	return out
}