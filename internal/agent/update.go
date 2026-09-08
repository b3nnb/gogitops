package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ── Agent self-update (git binaries-branch transport) ───────────────────────
//
// The fleet serves agent updates through the same git repo + deploy key every
// agent already uses for config pulls — no HTTP endpoint required, works from
// any network position that can reach github.com (the LAN dashboard is not
// reachable from client-side subnets; releases.bennbot.com was never built).
//
// Branch layout (force-pushed each release, no history growth):
//
//	binaries/VERSION                    e.g. "v0.5.4"
//	binaries/bin/gogitops-<goos>-<goarch>   raw static binaries (ldflags-versioned)
//
// Opt-in by presence: no branch = feature silently off.
// After a successful swap the agent exits 0; the service manager (systemd
// unit with Restart=always, or the macOS cron keepalive) restarts it.

// BinariesBranch is the git branch serving pre-built agent binaries.
const BinariesBranch = "binaries"

// maybeSelfUpdate checks the binaries branch for a newer version and
// hot-swaps the running binary. Called after each git pull cycle.
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

	// Read published version
	showVer := exec.Command("git", "show", remote+":VERSION")
	showVer.Dir = a.repoDir
	vOut, err := showVer.Output()
	if err != nil {
		return
	}
	published := strings.TrimSpace(string(vOut))
	if published == "" {
		return
	}
	if !isNewer(published, Version) {
		return
	}

	// Download this platform's binary
	binPath := fmt.Sprintf("bin/gogitops-%s-%s", runtime.GOOS, runtime.GOARCH)
	showBin := exec.Command("git", "show", remote+":"+binPath)
	showBin.Dir = a.repoDir
	bin, err := showBin.Output()
	if err != nil {
		a.logger.Errorf("update", "download %s failed: %v", binPath, err)
		return
	}
	if len(bin) < 500*1024 {
		a.logger.Errorf("update", "downloaded %s too small (%d bytes) — refusing", binPath, len(bin))
		return
	}

	// Swap atomically: write temp alongside, rename over the running binary.
	// (rename-over-running-exe is legal on Linux and macOS; direct write is not)
	exe, err := os.Executable()
	if err != nil {
		return
	}
	tmp := filepath.Join(filepath.Dir(exe), ".gogitops-update-"+published)
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

	a.logger.Actionf("update", "self-updated v%s -> v%s — restarting", Version, published)
	// Service manager (Restart=always / cron keepalive) restarts us now.
	os.Exit(0)
}

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
			n = n*10 + int(ch - '0')
		}
		out[i] = n
	}
	return out
}