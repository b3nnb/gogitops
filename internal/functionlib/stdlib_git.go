// Stdlib: git-branch file extraction — the binaries-branch installer dance
// the agent recipes kept hand-rolling in shell (`git fetch -q origin
// binaries`, `git show origin/binaries:… > tmp`, size guard, chmod, atomic
// mv) as one typed op. Used for the agent self-rescue (gogitops binary off
// origin/binaries) and tool CLIs shipped on the same branch (nenv).
//
// No build tag: plain git + file ops, portable everywhere git exists.
package functionlib

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func init() {
	Register(Function{
		Name:        "system.git-extract",
		Description: "Extract one file from a git ref of a local checkout: fetch the branch first (require = fetch failure is an error; best-effort = stale ref tolerated, like the originals' `|| true`), then git show → atomic temp+rename dest (exec bit optional, min_bytes guard) or content= for small files. Missing path / broken checkout is an error, never a silent success.",
		Params: []Param{
			{Name: "ref", Type: "string", Description: "Git ref to read from (e.g. origin/binaries; HEAD works content-only)"},
			{Name: "path", Type: "string", Description: "File path inside the tree (e.g. bin/gogitops-linux-amd64 or VERSION)"},
			{Name: "dest", Type: "string", Description: "Destination file (~ expands); empty = content-only mode for small files (≤64KB)"},
			{Name: "repo", Type: "string", Description: "Checkout dir (default: the recipe's {{repo}}; ~ expands)"},
			{Name: "fetch", Type: "string", Description: "require (default) = fetch failure is an error | best-effort = fetch failure tolerated when the ref is stale-present"},
			{Name: "min_bytes", Type: "int", Description: "Reject extractions smaller than this (corrupt/stub guard)"},
			{Name: "exec", Type: "bool", Description: "true = chmod +x the destination"},
		},
		Run: gitExtract,
	})
}

// contentOnlyLimit caps content= mode — a 6MB binary must go through dest=,
// never through the pseudo-stdout line.
const contentOnlyLimit = 64 * 1024

func gitExtract(ctx Context, args map[string]any) (map[string]any, error) {
	ref := argStr(args, "ref")
	path := argStr(args, "path")
	if ref == "" || path == "" {
		return nil, fmt.Errorf("ref= and path= are required")
	}
	repoDir := argStr(args, "repo")
	if repoDir == "" {
		repoDir = ctx.RepoDir
	}
	if repoDir == "" {
		return nil, fmt.Errorf("repo= required (no {{repo}} in the run context)")
	}
	repoDir = expandTilde(repoDir)
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return nil, fmt.Errorf("git-extract: %s is not a git checkout (no .git)", repoDir)
	}

	// Fetch first — a remote-tracking ref like origin/binaries is only as
	// fresh as the last fetch, exactly like the originals' `git fetch -q
	// origin binaries`. Non-remote refs (HEAD, a SHA) have nothing to fetch.
	fetchMode := argOr(argStr(args, "fetch"), "require")
	if fetchMode != "require" && fetchMode != "best-effort" {
		return nil, fmt.Errorf("fetch= must be require or best-effort, got %q", fetchMode)
	}
	if branch, remote, ok := splitRemoteRef(ref); ok {
		out, err := exec.Command("git", "-C", repoDir, "fetch", "-q", remote, branch).CombinedOutput()
		if err != nil {
			if fetchMode == "require" {
				return nil, fmt.Errorf("git-extract: fetch %s from %s failed: %s", branch, remote, strings.TrimSpace(string(out)))
			}
			// best-effort: the previously fetched origin/<branch> may still
			// resolve — same tolerance the originals' `|| true` bought.
		}
	}

	out, err := exec.Command("git", "-C", repoDir, "show", ref+":"+path).Output()
	if err != nil {
		detail := ""
		if ee, ok := err.(*exec.ExitError); ok {
			detail = strings.TrimSpace(string(ee.Stderr))
		}
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("git-extract: %s:%s not readable (%s)", ref, path, detail)
	}
	size := len(out)

	if minRaw := argStr(args, "min_bytes"); minRaw != "" {
		minB, perr := strconv.Atoi(minRaw)
		if perr != nil || minB < 0 {
			return nil, fmt.Errorf("min_bytes= must be a non-negative int, got %q", minRaw)
		}
		if size < minB {
			return nil, fmt.Errorf("git-extract: %s:%s is %d bytes (< min_bytes=%d) — refuse to install", ref, path, size, minB)
		}
	}

	// Content-only mode: small files flow through the pseudo-stdout as
	// content= (the VERSION-file shape). Binaries always set dest=.
	if argStr(args, "dest") == "" {
		if size > contentOnlyLimit {
			return nil, fmt.Errorf("git-extract: %s:%s is %d bytes — content-only mode is for small files, set dest=", ref, path, size)
		}
		return map[string]any{
			"size":    size,
			"content": strings.TrimSpace(string(out)),
			"math":    fmt.Sprintf("git-extract %s:%s → content (%d bytes)", ref, path, size),
		}, nil
	}

	dest := expandTilde(argStr(args, "dest"))
	if dir := filepath.Dir(dest); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("git-extract: %v", err)
		}
	}
	mode := os.FileMode(0o644)
	if parseBoolArg(args["exec"]) {
		mode = 0o755
	}
	if err := atomicWrite(dest, out, mode); err != nil {
		return nil, fmt.Errorf("git-extract: %v", err)
	}
	return map[string]any{
		"size": size,
		"dest": dest,
		"math": fmt.Sprintf("git-extract %s:%s → %s (%d bytes, exec=%v)", ref, path, dest, size, mode == 0o755),
	}, nil
}

// splitRemoteRef breaks "origin/binaries" into (binaries, origin, true);
// local refs (HEAD, SHAs) return false — nothing to fetch for them.
func splitRemoteRef(ref string) (branch, remote string, ok bool) {
	i := strings.Index(ref, "/")
	if i <= 0 || i == len(ref)-1 {
		return "", "", false
	}
	// Multi-slash refs (origin/feature/x) keep everything after the first
	// slash as the branch name.
	return ref[i+1:], ref[:i], true
}
