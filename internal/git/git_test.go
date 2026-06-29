package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a fresh git repo in a temp dir and returns its path.
// The repo has a single initial commit on the default branch (named "main"
// to match modern git config — overridable via -b for tests that need it).
func initRepo(t *testing.T, branch string) string {
	t.Helper()
	if branch == "" {
		branch = "main"
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "-b", branch)
	mustGit(t, dir, "config", "user.email", "test@example.com")
	mustGit(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "init")
	return dir
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestIsRepoTrueInsideGitRepo(t *testing.T) {
	dir := initRepo(t, "main")
	if !IsRepo(context.Background(), dir) {
		t.Error("expected IsRepo=true inside an initialised git repo")
	}
}

func TestIsRepoFalseOutsideGitRepo(t *testing.T) {
	dir := t.TempDir()
	if IsRepo(context.Background(), dir) {
		t.Error("expected IsRepo=false in a non-git directory")
	}
}

func TestIsCleanReportsClean(t *testing.T) {
	dir := initRepo(t, "main")
	clean, err := IsClean(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("expected clean tree after fresh init+commit")
	}
}

func TestIsCleanDetectsUntrackedFile(t *testing.T) {
	dir := initRepo(t, "main")
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean, err := IsClean(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if clean {
		t.Error("expected dirty tree with untracked file")
	}
}

func TestIsCleanDetectsModification(t *testing.T) {
	dir := initRepo(t, "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean, err := IsClean(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if clean {
		t.Error("expected dirty tree after modifying tracked file")
	}
}

func TestIsCleanIgnoresGitignoredFiles(t *testing.T) {
	dir := initRepo(t, "main")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ralph/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", ".gitignore")
	mustGit(t, dir, "commit", "-m", "add gitignore")
	if err := os.Mkdir(filepath.Join(dir, ".ralph"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ralph", "output.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean, err := IsClean(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("expected clean tree — gitignored entries shouldn't count as dirty")
	}
}

func TestCurrentBranchReturnsCheckedOutBranch(t *testing.T) {
	dir := initRepo(t, "main")
	got, err := CurrentBranch(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "main" {
		t.Errorf("got %q, want main", got)
	}
	mustGit(t, dir, "checkout", "-b", "feature/x")
	got, err = CurrentBranch(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "feature/x" {
		t.Errorf("got %q, want feature/x", got)
	}
}

func TestCreateWorkBranchCreatesAndChecksOut(t *testing.T) {
	dir := initRepo(t, "main")
	if err := CreateWorkBranch(context.Background(), dir, "ralph/issue-3-fix-thing"); err != nil {
		t.Fatalf("CreateWorkBranch: %v", err)
	}
	got, err := CurrentBranch(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ralph/issue-3-fix-thing" {
		t.Errorf("current branch = %q; want ralph/issue-3-fix-thing", got)
	}
}

func TestCreateWorkBranchResetsExistingBranch(t *testing.T) {
	// A re-run of the same issue must not fail on "branch already exists" and
	// must start fresh from the current default-branch HEAD (not stale commits).
	dir := initRepo(t, "main")
	mustGit(t, dir, "checkout", "-b", "ralph/issue-3-fix-thing")
	// Put a commit on the stale branch that a fresh run should NOT inherit.
	if err := os.WriteFile(filepath.Join(dir, "stale.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "stale work")
	mustGit(t, dir, "checkout", "main")

	if err := CreateWorkBranch(context.Background(), dir, "ralph/issue-3-fix-thing"); err != nil {
		t.Fatalf("CreateWorkBranch on existing branch: %v", err)
	}
	got, err := CurrentBranch(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ralph/issue-3-fix-thing" {
		t.Errorf("current branch = %q; want ralph/issue-3-fix-thing", got)
	}
	// The branch was reset to main's HEAD, so the stale commit's file is gone.
	if _, err := os.Stat(filepath.Join(dir, "stale.txt")); !os.IsNotExist(err) {
		t.Errorf("expected stale.txt to be absent after -B reset to main; stat err=%v", err)
	}
}

func TestCommitsAheadCountsBranchCommits(t *testing.T) {
	dir := initRepo(t, "main")
	mustGit(t, dir, "checkout", "-b", "feature")
	// No commits yet → 0 ahead.
	n, err := CommitsAhead(context.Background(), dir, "main", "feature")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("fresh branch: got %d ahead, want 0", n)
	}
	// Two commits → 2 ahead.
	for i, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustGit(t, dir, "add", ".")
		mustGit(t, dir, "commit", "-m", "c"+string(rune('0'+i)))
	}
	n, err = CommitsAhead(context.Background(), dir, "main", "feature")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("got %d ahead, want 2", n)
	}
}

func TestPushSendsBranchToOrigin(t *testing.T) {
	bare := t.TempDir()
	mustGit(t, bare, "init", "--bare", "-b", "main")
	dir := initRepo(t, "main")
	mustGit(t, dir, "remote", "add", "origin", bare)
	mustGit(t, dir, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "work")

	if err := Push(context.Background(), dir, "feature"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	// The bare origin should now have the feature ref.
	cmd := exec.Command("git", "rev-parse", "--verify", "feature")
	cmd.Dir = bare
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("feature branch not on origin after Push: %v\n%s", err, out)
	}
	// Idempotent: pushing again with no new commits is still a success.
	if err := Push(context.Background(), dir, "feature"); err != nil {
		t.Errorf("re-Push should be a no-op success: %v", err)
	}
}

func TestDefaultBranchPrefersOriginHEAD(t *testing.T) {
	// Make a "remote" bare repo with develop as default, clone it, verify we
	// pick up develop via symbolic-ref of refs/remotes/origin/HEAD.
	bare := t.TempDir()
	mustGit(t, bare, "init", "--bare", "-b", "develop")

	work := initRepo(t, "develop")
	mustGit(t, work, "remote", "add", "origin", bare)
	mustGit(t, work, "push", "-u", "origin", "develop")
	mustGit(t, work, "remote", "set-head", "origin", "develop")

	got, err := DefaultBranch(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	if got != "develop" {
		t.Errorf("got %q, want develop", got)
	}
}

func TestDefaultBranchFallsBackToLocalMain(t *testing.T) {
	dir := initRepo(t, "main")
	got, err := DefaultBranch(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "main" {
		t.Errorf("got %q, want main", got)
	}
}

func TestDefaultBranchFallsBackToLocalMaster(t *testing.T) {
	dir := initRepo(t, "master")
	got, err := DefaultBranch(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "master" {
		t.Errorf("got %q, want master (fallback)", got)
	}
}

func TestOriginURLReturnsConfiguredRemote(t *testing.T) {
	dir := initRepo(t, "main")
	const want = "git@github.com:test/repo.git"
	mustGit(t, dir, "remote", "add", "origin", want)
	got, err := OriginURL(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestOriginURLErrorsWhenMissing(t *testing.T) {
	dir := initRepo(t, "main")
	if _, err := OriginURL(context.Background(), dir); err == nil {
		t.Error("expected error when no origin remote is configured")
	}
}

// TestWorktreeRoundTrip exercises Fetch + AddWorktree + RemoveWorktree against a
// repo wired to a bare origin: a worktree is cut from origin/main into its own
// directory on a new branch, then removed — and the branch ref survives removal
// so already-committed work is never lost.
func TestWorktreeRoundTrip(t *testing.T) {
	ctx := context.Background()
	bare := t.TempDir()
	mustGit(t, bare, "init", "--bare", "-b", "main")

	work := initRepo(t, "main")
	mustGit(t, work, "remote", "add", "origin", bare)
	mustGit(t, work, "push", "-u", "origin", "main")

	if err := Fetch(ctx, work, "origin", "main"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	wtBase := t.TempDir()
	wtPath := filepath.Join(wtBase, "issue-7")
	if err := AddWorktree(ctx, work, wtPath, "ralph/issue-7-x", "origin/main"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "README.md")); err != nil {
		t.Fatalf("worktree should be a real checkout: %v", err)
	}
	br, err := CurrentBranch(ctx, wtPath)
	if err != nil || br != "ralph/issue-7-x" {
		t.Fatalf("worktree branch = %q, %v; want ralph/issue-7-x", br, err)
	}

	// Commit in the worktree so the branch has work, then remove the worktree.
	if err := os.WriteFile(filepath.Join(wtPath, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, wtPath, "add", ".")
	mustGit(t, wtPath, "commit", "-m", "work")

	if err := RemoveWorktree(ctx, work, wtPath); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir should be gone after RemoveWorktree (err=%v)", err)
	}
	// The branch ref must still exist (commits aren't lost with the worktree).
	if _, err := CurrentBranch(ctx, work); err != nil {
		t.Fatalf("root repo still usable: %v", err)
	}
	if out, err := run(ctx, work, "git", "rev-parse", "--verify", "ralph/issue-7-x"); err != nil {
		t.Errorf("branch ref should survive worktree removal: %v\n%s", err, out)
	}
}

// TestForcePushAfterRebase confirms ForcePush succeeds where a plain push would
// be rejected: the local branch is rebased (history rewritten) after being
// pushed, exactly the merge-repair situation.
func TestForcePushAfterRebase(t *testing.T) {
	ctx := context.Background()
	bare := t.TempDir()
	mustGit(t, bare, "init", "--bare", "-b", "main")

	work := initRepo(t, "main")
	mustGit(t, work, "remote", "add", "origin", bare)
	mustGit(t, work, "push", "-u", "origin", "main")

	mustGit(t, work, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, work, "add", ".")
	mustGit(t, work, "commit", "-m", "a")
	if err := Push(ctx, work, "feature"); err != nil {
		t.Fatalf("initial Push: %v", err)
	}
	// Rewrite history: amend the commit so the remote ref is no longer an ancestor.
	mustGit(t, work, "commit", "--amend", "-m", "a (amended)")
	if err := ForcePush(ctx, work, "feature"); err != nil {
		t.Fatalf("ForcePush after rewrite should succeed: %v", err)
	}
}
