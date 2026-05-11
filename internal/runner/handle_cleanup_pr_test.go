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

func mustGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// newRunForTest builds a *run without going through newRun() (which requires
// the claude binary on PATH). Session is intentionally left nil — every test
// path below errors out *before* any r.log / r.section call, so the nil
// session never gets dereferenced. Add a real session if you exercise the
// success path.
func newRunForTest(projectRoot, defaultBranch string, prov vcs.Provider) *run {
	cfg := config.Defaults()
	cfg.ProjectRoot = projectRoot
	cfg.DefaultBranch = defaultBranch
	return &run{
		cfg:      &cfg,
		provider: prov,
		ui:       io.Discard,
	}
}

// TestHandleCleanupPRErrorsIfStuckOnDefaultBranch — if Claude's cleanup pass
// finished without creating a feature branch (still on main), handleCleanupPR
// must surface a clear error rather than try to look up "main" as a PR head.
func TestHandleCleanupPRErrorsIfStuckOnDefaultBranch(t *testing.T) {
	dir := initRepoOnBranch(t, "main", "") // stay on main
	r := newRunForTest(dir, "main", &fakeProvider{})

	err := r.handleCleanupPR(context.Background(), 0)
	if err == nil {
		t.Fatal("expected error when cleanup left us on the default branch")
	}
	if !strings.Contains(err.Error(), "did not create a feature branch") {
		t.Errorf("error should explain the problem; got: %v", err)
	}
}

// TestHandleCleanupPRAdHocModeRequiresAnOpenPR — in `ralph run --pr` mode the
// cleanup pass is supposed to open a PR. If FindPRForBranch returns no PR,
// surface an actionable error mentioning the branch so the user can recover.
func TestHandleCleanupPRAdHocModeRequiresAnOpenPR(t *testing.T) {
	dir := initRepoOnBranch(t, "main", "feature/x")
	prov := &fakeProvider{findPRResult: nil} // simulate "Claude didn't push a PR"
	r := newRunForTest(dir, "main", prov)

	err := r.handleCleanupPR(context.Background(), 0)
	if err == nil {
		t.Fatal("expected error when no PR was opened in --pr mode")
	}
	if !strings.Contains(err.Error(), "feature/x") {
		t.Errorf("error should mention the branch name to aid recovery; got: %v", err)
	}
	if !strings.Contains(err.Error(), "no open PR") {
		t.Errorf("error should explain that no PR was found; got: %v", err)
	}
}

// TestHandleCleanupPRIssueModeRequiresAnOpenPR — in issue mode, a missing PR
// means the refine loop never pushed. Bubble that up so processIssue's defer
// marks the issue failed instead of silently merging nothing.
func TestHandleCleanupPRIssueModeRequiresAnOpenPR(t *testing.T) {
	dir := initRepoOnBranch(t, "main", "feature/y")
	prov := &fakeProvider{findPRResult: nil}
	r := newRunForTest(dir, "main", prov)

	err := r.handleCleanupPR(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error when no PR exists for issue mode")
	}
	if !strings.Contains(err.Error(), "feature/y") {
		t.Errorf("error should mention the branch name; got: %v", err)
	}
	if !strings.Contains(err.Error(), "did not push") {
		t.Errorf("error should explain the loop failed to push; got: %v", err)
	}
}

// TestHandleCleanupPRIssueModeSuccessCallsMarkResolved — the happy path: PR
// found, checks pass, merge succeeds, MarkResolved fires. The MarkResolved
// call is the defensive backstop that closes the issue + clears the working
// label even when Claude's PR body forgot to include "Closes #N"; without it
// the issue would stay open with ralph-working set forever.
func TestHandleCleanupPRIssueModeSuccessCallsMarkResolved(t *testing.T) {
	dir := initRepoOnBranch(t, "main", "feature/z")
	prov := &fakeProvider{
		findPRResult: &vcs.PR{Number: 7, Branch: "feature/z", URL: "https://example.test/pull/7"},
	}
	r := newRunForTest(dir, "main", prov)

	// handleCleanupPR's success path calls r.log, which writes through the
	// session. Wire a real Session backed by t.TempDir() so the writes have
	// somewhere to land — NewSession does not require the claude binary, it
	// only generates a UUID and stores config.
	sess, err := claude.NewSession(claude.SessionConfig{
		WorkDir:   dir,
		OutputDir: filepath.Join(t.TempDir(), ".ralph"),
		UI:        io.Discard,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	r.session = sess

	if err := r.handleCleanupPR(context.Background(), 42); err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if prov.resolved != 42 {
		t.Errorf("MarkResolved was not called for issue 42 (resolved=%d) — without this, the merged issue would stay open with ralph-working set", prov.resolved)
	}
}
