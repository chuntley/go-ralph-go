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
