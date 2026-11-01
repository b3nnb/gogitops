// Tests for stdlib_git.go — system.git-extract. Real git repos in tempdirs
// (git must be on PATH — skipped otherwise): content mode, dest mode with
// exec bit + min_bytes, missing path, and the require vs best-effort fetch
// contract against a bare remote.
package functionlib

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// initRepoT creates a git checkout with the given files committed to main
// and returns (repoDir, cleanup-wired nothing — t.TempDir owns the paths).
func initRepoT(t *testing.T, files map[string]string) string {
	t.Helper()
	gitAvailable(t)
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "functest@example.com")
	run("config", "user.name", "functest")
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-q", "-m", "functest seed")
	return dir
}

func TestGitExtractContent(t *testing.T) {
	repo := initRepoT(t, map[string]string{"VERSION": "v0.7.30\n"})
	out, err := gitExtract(Context{}, map[string]any{
		"ref": "HEAD", "path": "VERSION", "repo": repo,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["content"] != "v0.7.30" {
		t.Errorf("content = %v, want v0.7.30 (trimmed)", out["content"])
	}
	if got := fmt.Sprintf("%v", out["size"]); got != "8" {
		t.Errorf("size = %v, want 8", out["size"])
	}
}

func TestGitExtractContentTooBig(t *testing.T) {
	repo := initRepoT(t, map[string]string{"big.bin": strings.Repeat("x", 128*1024)})
	_, err := gitExtract(Context{}, map[string]any{
		"ref": "HEAD", "path": "big.bin", "repo": repo,
	})
	if err == nil || !strings.Contains(err.Error(), "set dest=") {
		t.Errorf("oversize content-only must demand dest=, got: %v", err)
	}
}

func TestGitExtractDest(t *testing.T) {
	repo := initRepoT(t, map[string]string{"bin/tool": "#!/bin/sh\necho hi\n"})
	dest := filepath.Join(t.TempDir(), "nested", "tool")
	out, err := gitExtract(Context{}, map[string]any{
		"ref": "HEAD", "path": "bin/tool", "dest": dest, "repo": repo, "exec": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["dest"] != dest {
		t.Errorf("dest output = %v, want %s", out["dest"], dest)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("exec=true did not set an exec bit: %v", info.Mode())
	}
	// Atomic swap: no temp leftovers next to the destination.
	leftovers, _ := filepath.Glob(dest + ".gogitops-*")
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func TestGitExtractMinBytes(t *testing.T) {
	repo := initRepoT(t, map[string]string{"tiny": "hi"})
	_, err := gitExtract(Context{}, map[string]any{
		"ref": "HEAD", "path": "tiny", "dest": filepath.Join(t.TempDir(), "tiny"),
		"repo": repo, "min_bytes": "500000",
	})
	if err == nil || !strings.Contains(err.Error(), "min_bytes=500000") {
		t.Errorf("stub extraction must be refused, got: %v", err)
	}
}

func TestGitExtractMissingPath(t *testing.T) {
	repo := initRepoT(t, map[string]string{"real.txt": "x"})
	_, err := gitExtract(Context{}, map[string]any{
		"ref": "HEAD", "path": "definitely/not/here", "repo": repo,
	})
	if err == nil || !strings.Contains(err.Error(), "not readable") {
		t.Errorf("missing path must be an error mentioning 'not readable', got: %v", err)
	}
	// Missing repo dir / not a checkout.
	_, err = gitExtract(Context{}, map[string]any{
		"ref": "HEAD", "path": "x", "repo": filepath.Join(t.TempDir(), "empty"),
	})
	if err == nil || !strings.Contains(err.Error(), "not a git checkout") {
		t.Errorf("non-checkout must error, got: %v", err)
	}
	// Missing args.
	if _, err := gitExtract(Context{}, map[string]any{"ref": "HEAD"}); err == nil {
		t.Errorf("missing path= must error")
	}
}

// TestGitExtractFetchContract is the money test: `require` fails when the
// branch cannot be fetched; `best-effort` installs the stale ref instead —
// exactly the `|| true` tolerance the originals carried.
func TestGitExtractFetchContract(t *testing.T) {
	gitAvailable(t)
	// Bare remote with a binaries branch carrying v0.1 content.
	remote := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("bare init: %v: %s", err, out)
	}
	seed := t.TempDir()
	seedRun := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", seed}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	seedRun("init", "-q", "-b", "main")
	seedRun("config", "user.email", "functest@example.com")
	seedRun("config", "user.name", "functest")
	if err := os.WriteFile(filepath.Join(seed, "payload"), []byte("fresh-content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedRun("add", "-A")
	seedRun("commit", "-q", "-m", "seed")
	seedRun("checkout", "-q", "-b", "binaries")
	if err := os.WriteFile(filepath.Join(seed, "payload"), []byte("stale-content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedRun("commit", "-qam", "stale binaries")
	seedRun("remote", "add", "origin", remote)

	// Working checkout cloned BEFORE the branch exists on the remote —
	// the stale-tracking / missing-branch edge the fetch contract is about.
	work := t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", remote, work).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}

	// The remote binaries branch does not exist yet → require fails,
	// best-effort falls back… only when the stale ref is present. Since
	// neither is present here, BOTH fail — assert the contract edge first.
	_, errRequire := gitExtract(Context{}, map[string]any{
		"ref": "origin/binaries", "path": "payload", "dest": filepath.Join(t.TempDir(), "p"), "repo": work, "fetch": "require",
	})
	if errRequire == nil {
		t.Errorf("require fetch of a nonexistent branch must fail")
	}
	_, errBest := gitExtract(Context{}, map[string]any{
		"ref": "origin/binaries", "path": "payload", "dest": filepath.Join(t.TempDir(), "p"), "repo": work, "fetch": "best-effort",
	})
	if errBest == nil {
		t.Errorf("best-effort with no stale ref present must still fail (nothing to install)")
	}

	// Now push the branch; require must fetch it and install the content.
	seedRun("push", "-q", "origin", "binaries")
	dest := filepath.Join(t.TempDir(), "payload")
	if _, err := gitExtract(Context{}, map[string]any{
		"ref": "origin/binaries", "path": "payload", "dest": dest, "repo": work, "fetch": "require",
	}); err != nil {
		t.Fatalf("require fetch after push failed: %v", err)
	}
	data, _ := os.ReadFile(dest)
	if string(data) != "stale-content\n" {
		t.Errorf("installed content = %q, want the binaries-branch content", data)
	}

	// best-effort with a BROKEN origin still installs the stale ref.
	work2 := t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", remote, work2).CombinedOutput(); err != nil {
		t.Fatalf("clone2: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", work2, "remote", "set-url", "origin", "/nonexistent-remote-xyz").CombinedOutput(); err != nil {
		t.Fatalf("set-url: %v: %s", err, out)
	}
	dest2 := filepath.Join(t.TempDir(), "payload")
	if _, err := gitExtract(Context{}, map[string]any{
		"ref": "origin/binaries", "path": "payload", "dest": dest2, "repo": work2, "fetch": "best-effort",
	}); err != nil {
		t.Fatalf("best-effort must tolerate the broken origin when the stale ref resolves: %v", err)
	}
	data2, _ := os.ReadFile(dest2)
	if string(data2) != "stale-content\n" {
		t.Errorf("stale install = %q, want stale-content", data2)
	}
}
