package runner

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chuntley/go-ralph-go/internal/claude"
	"github.com/chuntley/go-ralph-go/internal/config"
	"github.com/chuntley/go-ralph-go/internal/vcs"
)

// initRepoOnBranch makes a fresh git repo in t.TempDir() with one initial
// commit on `branch`, and (optionally) checks out a feature branch on top.
// Mirrors the helper in internal/git/git_test.go; duplicated here because
// test helpers in another package aren't importable.
func initRepoOnBranch(t *testing.T, branch, featureBranch string) string {
	t.Helper()
	dir := t.TempDir()
	mustGitCmd(t, dir, "init", "-b", branch)
	mustGitCmd(t, dir, "config", "user.email", "test@example.com")
	mustGitCmd(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitCmd(t, dir, "add", ".")
	mustGitCmd(t, dir, "commit", "-m", "init")
	if featureBranch != "" {
		mustGitCmd(t, dir, "checkout", "-b", featureBranch)
	}
	return dir
}

// initRepoWithOriginAndFeature sets up a work repo wired to a bare `origin`,
// then checks out featureBranch with ONE commit ahead of the default branch —
// the state ensurePR's fallback needs (a real remote to push to and commits to
// open a PR for).
func initRepoWithOriginAndFeature(t *testing.T, branch, featureBranch string) string {
	t.Helper()
	bare := t.TempDir()
	mustGitCmd(t, bare, "init", "--bare", "-b", branch)

	dir := initRepoOnBranch(t, branch, "")
	mustGitCmd(t, dir, "remote", "add", "origin", bare)
	mustGitCmd(t, dir, "push", "-u", "origin", branch)

	mustGitCmd(t, dir, "checkout", "-b", featureBranch)
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitCmd(t, dir, "add", ".")
	mustGitCmd(t, dir, "commit", "-m", "feature work")
	return dir
}

func mustGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// newRunForTest builds a *run without going through newRun() (which requires
// the claude binary on PATH). A discard-backed Session is attached so the
// r.log calls on the success / fallback paths have somewhere to write.
func newRunForTest(t *testing.T, projectRoot, defaultBranch string, prov vcs.Provider) *run {
	t.Helper()
	cfg := config.Defaults()
	cfg.ProjectRoot = projectRoot
	cfg.DefaultBranch = defaultBranch
	sess, err := claude.NewSession(claude.SessionConfig{
		WorkDir:   projectRoot,
		OutputDir: filepath.Join(t.TempDir(), ".ralph"),
		UI:        io.Discard,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return &run{
		cfg:      &cfg,
		provider: prov,
		session:  sess,
		ui:       io.Discard,
	}
}

// TestHandleCleanupPRErrorsIfStuckOnDefaultBranch — if the cleanup pass finished
// on the default branch (the agent ignored its branch and checked out main),
// handleCleanupPR must surface a clear error rather than try to open a PR from
// the default branch.
func TestHandleCleanupPRErrorsIfStuckOnDefaultBranch(t *testing.T) {
	dir := initRepoOnBranch(t, "main", "") // stay on main
	r := newRunForTest(t, dir, "main", &fakeProvider{})

	err := r.handleCleanupPR(context.Background(), 0, "")
	if err == nil {
		t.Fatal("expected error when cleanup left us on the default branch")
	}
	if !strings.Contains(err.Error(), "no feature branch") {
		t.Errorf("error should explain the problem; got: %v", err)
	}
}

// TestHandleCleanupPRErrorsWhenBranchHasNoCommits — on a feature branch with no
// commits over the base and no PR, there is genuinely nothing to open a PR for.
// ensurePR must say so (and must NOT attempt a push — there's no origin here).
func TestHandleCleanupPRErrorsWhenBranchHasNoCommits(t *testing.T) {
	dir := initRepoOnBranch(t, "main", "feature/empty") // branched, no extra commit
	prov := &fakeProvider{findPRResult: nil}
	r := newRunForTest(t, dir, "main", prov)

	err := r.handleCleanupPR(context.Background(), 42, "Fix the thing")
	if err == nil {
		t.Fatal("expected error when the branch carries no work")
	}
	if !strings.Contains(err.Error(), "no commits over") {
		t.Errorf("error should explain there's nothing to PR; got: %v", err)
	}
	if prov.createdPR != nil {
		t.Error("must not open a PR for an empty branch")
	}
}

// TestHandleCleanupPROpensPRWhenMissingIssueMode — the core guardrail: the loop
// committed work on the branch but the cleanup pass never opened a PR. ralph
// must push the branch, open the PR itself (with Closes #N), then merge and
// resolve — instead of failing the issue.
func TestHandleCleanupPROpensPRWhenMissingIssueMode(t *testing.T) {
	dir := initRepoWithOriginAndFeature(t, "main", "ralph/issue-42-x")
	prov := &fakeProvider{findPRResult: nil} // no PR opened by the agent
	r := newRunForTest(t, dir, "main", prov)

	if err := r.handleCleanupPR(context.Background(), 42, "Fix the thing"); err != nil {
		t.Fatalf("expected ralph to open the PR and succeed, got: %v", err)
	}
	if prov.createdPR == nil {
		t.Fatal("ralph should have opened a PR when none existed")
	}
	if prov.createPRTitle != "Fix the thing" {
		t.Errorf("PR title = %q; want the issue title", prov.createPRTitle)
	}
	if !strings.Contains(prov.createPRBody, "Closes #42") {
		t.Errorf("PR body should auto-close the issue; got: %q", prov.createPRBody)
	}
	if prov.resolved != 42 {
		t.Errorf("MarkResolved should fire after merge (resolved=%d)", prov.resolved)
	}
}

// TestHandleCleanupPROpensPRWhenMissingAdHoc — `run --pr` with no PR: ralph
// opens one but does NOT merge it.
func TestHandleCleanupPROpensPRWhenMissingAdHoc(t *testing.T) {
	dir := initRepoWithOriginAndFeature(t, "main", "ralph/run-x")
	prov := &fakeProvider{findPRResult: nil}
	r := newRunForTest(t, dir, "main", prov)

	if err := r.handleCleanupPR(context.Background(), 0, ""); err != nil {
		t.Fatalf("expected ralph to open the PR and succeed, got: %v", err)
	}
	if prov.createdPR == nil {
		t.Fatal("ralph should have opened a PR when none existed")
	}
	if prov.createPRTitle != "ralph: ralph/run-x" {
		t.Errorf("ad-hoc PR title = %q; want branch-derived title", prov.createPRTitle)
	}
	if prov.resolved != 0 {
		t.Error("ad-hoc --pr must NOT merge/resolve")
	}
}

// TestHandleCleanupPRIssueModeSuccessCallsMarkResolved — the happy path when the
// agent DID open the PR: checks pass, merge succeeds, MarkResolved fires (the
// defensive backstop that closes the issue + clears ralph-working even when the
// PR body lacked "Closes #N").
func TestHandleCleanupPRIssueModeSuccessCallsMarkResolved(t *testing.T) {
	dir := initRepoOnBranch(t, "main", "feature/z")
	prov := &fakeProvider{
		findPRResult: &vcs.PR{Number: 7, Branch: "feature/z", URL: "https://example.test/pull/7"},
	}
	r := newRunForTest(t, dir, "main", prov)

	if err := r.handleCleanupPR(context.Background(), 42, "Fix the thing"); err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if prov.createdPR != nil {
		t.Error("must not open a new PR when one already exists")
	}
	if prov.resolved != 42 {
		t.Errorf("MarkResolved was not called for issue 42 (resolved=%d) — without this, the merged issue would stay open with ralph-working set", prov.resolved)
	}
}
