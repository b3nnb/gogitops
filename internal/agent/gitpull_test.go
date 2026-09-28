package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bennbanks/gogitops/internal/config"
)

// gitT runs a git command in dir, failing the test on error.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// testRepo builds a bare origin plus a "work" clone with main pushed and
// upstream tracking set. Also returns a second clone for driving remote-side
// commits.
func testRepo(t *testing.T) (origin, work, other string) {
	t.Helper()
	dir := t.TempDir()
	origin = filepath.Join(dir, "origin.git")
	work = filepath.Join(dir, "work")
	other = filepath.Join(dir, "other")
	exec.Command("git", "init", "--bare", "-b", "main", origin).Run()
	gitT(t, dir, "clone", origin, work)
	gitT(t, work, "config", "user.email", "t@example.com")
	gitT(t, work, "config", "user.name", "t")
	gitT(t, work, "commit", "--allow-empty", "-m", "base")
	gitT(t, work, "push", "-u", "origin", "main")
	gitT(t, dir, "clone", origin, other)
	gitT(t, other, "config", "user.email", "t@example.com")
	gitT(t, other, "config", "user.name", "t")
	return origin, work, other
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// newPullTestAgent wires an Agent to a repo dir with a nil logger (all new
// log paths are nil-safe) and a hostname for the mailbox push retry.
func newPullTestAgent(repoDir string) *Agent {
	return &Agent{repoDir: repoDir, node: &config.NodeConfig{Hostname: "t"}}
}

// TestGitPullRecordsOKState: a clean up-to-date pull records last_pull_ok.
func TestGitPullRecordsOKState(t *testing.T) {
	_, work, _ := testRepo(t)
	a := newPullTestAgent(work)
	res := a.gitPull()
	if res != "up to date" {
		t.Fatalf("result = %q, want up to date", res)
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.lastPullOK {
		t.Fatal("lastPullOK = false, want true")
	}
	if a.lastPullErr != "" {
		t.Fatalf("lastPullErr = %q, want empty", a.lastPullErr)
	}
	if a.lastGitPull.IsZero() {
		t.Fatal("lastGitPull not recorded")
	}
}

// TestGitPullDivergenceRebaseSelfHeals: local-only commit + remote commit on
// disjoint files → ff-only fails → rebase heals → both changes present, pull
// ok, divergence recorded, and the local commit re-pushed to the node's
// mailbox branch.
func TestGitPullDivergenceRebaseSelfHeals(t *testing.T) {
	origin, work, other := testRepo(t)

	// Remote advances on file B.
	writeFileT(t, filepath.Join(other, "fileB.txt"), "remote")
	gitT(t, other, "add", "fileB.txt")
	gitT(t, other, "commit", "-m", "remote B")
	gitT(t, other, "push")

	// Local-only commit on file A (the stranded identity-commit shape).
	writeFileT(t, filepath.Join(work, "fileA.txt"), "local")
	gitT(t, work, "add", "fileA.txt")
	gitT(t, work, "commit", "-m", "local A")

	a := newPullTestAgent(work)
	res := a.gitPull()
	if strings.Contains(res, "error:") {
		t.Fatalf("pull result = %q, want self-heal", res)
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.lastPullOK {
		t.Fatalf("lastPullOK = false after self-heal (err=%q)", a.lastPullErr)
	}
	if a.lastDivergenceAt.IsZero() {
		t.Fatal("divergence was not detected/recorded")
	}
	if !strings.Contains(a.lastDivergenceInfo, "ahead 1") || !strings.Contains(a.lastDivergenceInfo, "behind 1") {
		t.Fatalf("lastDivergenceInfo = %q, want ahead 1, behind 1", a.lastDivergenceInfo)
	}

	// Both changes present after rebase.
	for _, f := range []string{"fileA.txt", "fileB.txt"} {
		if _, err := os.Stat(filepath.Join(work, f)); err != nil {
			t.Fatalf("missing %s after self-heal: %v", f, err)
		}
	}

	// Mailbox push retry fired: refs/heads/node/t exists on origin and
	// points at the local HEAD.
	head := gitT(t, work, "rev-parse", "HEAD")
	mailbox := gitT(t, origin, "rev-parse", "refs/heads/node/t")
	if head != mailbox {
		t.Fatalf("mailbox %s != HEAD %s", mailbox, head)
	}
}

// TestGitPullDivergenceConflictResets: same file changed both ways → rebase
// conflicts → LOUD flag + backup ref preserving local-only commits + reset to
// upstream so convergence resumes.
func TestGitPullDivergenceConflictResets(t *testing.T) {
	_, work, other := testRepo(t)

	// Same file, both sides, different content.
	writeFileT(t, filepath.Join(other, "conflict.txt"), "remote wins")
	gitT(t, other, "add", "conflict.txt")
	gitT(t, other, "commit", "-m", "remote side")
	gitT(t, other, "push")

	writeFileT(t, filepath.Join(work, "conflict.txt"), "local orphan")
	gitT(t, work, "add", "conflict.txt")
	gitT(t, work, "commit", "-m", "local side")
	orphanSHA := gitT(t, work, "rev-parse", "HEAD")

	a := newPullTestAgent(work)
	res := a.gitPull()
	if !strings.Contains(res, "backup") {
		t.Fatalf("result = %q, want backup-ref reset", res)
	}

	a.mu.RLock()
	if !a.lastPullOK {
		t.Fatalf("lastPullOK=false after reset self-heal (err=%q)", a.lastPullErr)
	}
	a.mu.RUnlock()

	// Upstream content won — convergence resumed.
	b, err := os.ReadFile(filepath.Join(work, "conflict.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "remote wins" {
		t.Fatalf("conflict.txt = %q, want upstream content", string(b))
	}

	// The orphaned local commit is preserved in a backup ref.
	refs := gitT(t, work, "for-each-ref", "--format=%(refname)", "refs/heads/backup/*")
	if !strings.Contains(refs, "refs/heads/backup/divergence-") {
		t.Fatalf("no backup ref created: %q", refs)
	}
	backup := strings.Split(refs, "\n")[0]
	if got := gitT(t, work, "rev-parse", strings.TrimPrefix(backup, "refs/heads/")); got != orphanSHA {
		t.Fatalf("backup ref %s = %s, want orphaned %s", backup, got, orphanSHA)
	}
}
