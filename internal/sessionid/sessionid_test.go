package sessionid

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorktreeScoping creates a real repository with a linked worktree and
// verifies the B-37 acceptance criteria: different worktrees get different
// ids, the same worktree always gets the same id.
func TestWorktreeScoping(t *testing.T) {
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "t@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	// A commit is required before a worktree can be added.
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")

	wt1 := filepath.Join(base, "wt1")
	wt2 := filepath.Join(base, "wt2")
	runGit(t, repo, "worktree", "add", "-q", "-b", "wt1-branch", wt1)
	runGit(t, repo, "worktree", "add", "-q", "-b", "wt2-branch", wt2)

	idRepo, err := ID(ScopeRepo, "", repo)
	if err != nil {
		t.Fatal(err)
	}
	idWT1a, err := ID(ScopeWorktree, "", wt1)
	if err != nil {
		t.Fatal(err)
	}
	idWT1b, err := ID(ScopeWorktree, "", wt1)
	if err != nil {
		t.Fatal(err)
	}
	idWT2, err := ID(ScopeWorktree, "", wt2)
	if err != nil {
		t.Fatal(err)
	}
	if idWT1a.ID != idWT1b.ID {
		t.Fatalf("same worktree produced different ids: %q vs %q", idWT1a.ID, idWT1b.ID)
	}
	if idWT1a.ID == idWT2.ID {
		t.Fatal("different worktrees produced the same id")
	}
	if idRepo.ID == idWT1a.ID {
		t.Fatal("repo-scoped id collides with worktree-scoped id")
	}
	if idWT1a.OriginPath == idWT2.OriginPath {
		t.Fatalf("origin paths identical: %q", idWT1a.OriginPath)
	}
}

func TestCWDScopeAndPrefix(t *testing.T) {
	dir := t.TempDir()
	info, err := ID(ScopeCWD, "agent", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.ID, "agent-") {
		t.Fatalf("prefix not applied: %q", info.ID)
	}
	if !info.Fallback {
		t.Fatal("cwd scope should be marked fallback")
	}
	if info.OriginPath != dir {
		t.Fatalf("origin = %q", info.OriginPath)
	}
	again, _ := ID(ScopeCWD, "agent", dir)
	if again.ID != info.ID {
		t.Fatal("cwd id not stable")
	}
}

func TestOutsideGitFallsBack(t *testing.T) {
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	info, err := ID(ScopeWorktree, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Fallback {
		t.Fatalf("expected fallback outside a repo, got %#v", info)
	}
}

func TestInvalidScope(t *testing.T) {
	if _, err := ID(Scope("bogus"), "", t.TempDir()); err == nil {
		t.Fatal("invalid scope accepted")
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}
