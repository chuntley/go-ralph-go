package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chuntley/go-ralph-go/internal/config"
	"github.com/chuntley/go-ralph-go/internal/vcs"
)

func TestMergeRepairable(t *testing.T) {
	if mergeRepairable(nil) {
		t.Error("nil is not a failure to repair")
	}
	if mergeRepairable(context.Canceled) {
		t.Error("cancellation must propagate, not trigger repair")
	}
	if mergeRepairable(context.DeadlineExceeded) {
		t.Error("timeout must propagate, not trigger repair")
	}
	if !mergeRepairable(errors.New("checks failed: build")) {
		t.Error("a real check failure should be repairable")
	}
}

// TestHandleCleanupPRChecksFailNoRepair — with repair disabled
// (merge_repair_attempts = 0), a failing check makes handleCleanupPR surface the
// failure immediately and NOT merge/resolve the issue.
func TestHandleCleanupPRChecksFailNoRepair(t *testing.T) {
	dir := initRepoOnBranch(t, "main", "feature/z")
	prov := &fakeProvider{
		findPRResult: &vcs.PR{Number: 7, Branch: "feature/z", URL: "https://example.test/pull/7"},
		waitErr:      errors.New("checks failed: build broke"),
	}
	r := newRunForTest(t, dir, "main", prov)
	r.cfg.MergeRepairAttempts = 0

	err := r.handleCleanupPR(context.Background(), 42, "Fix the thing")
	if err == nil {
		t.Fatal("expected an error when checks fail and repair is disabled")
	}
	if !strings.Contains(err.Error(), "checks failed") {
		t.Errorf("error should explain the check failure; got: %v", err)
	}
	if prov.resolved != 0 {
		t.Errorf("must NOT resolve an issue whose checks never passed (resolved=%d)", prov.resolved)
	}
}

// TestHandleCleanupPRMergeRefusedNoRepair — same, but the checks pass and the
// merge itself is refused (e.g. a conflict). With repair disabled the failure
// surfaces and the issue is not resolved.
func TestHandleCleanupPRMergeRefusedNoRepair(t *testing.T) {
	dir := initRepoOnBranch(t, "main", "feature/z")
	prov := &fakeProvider{
		findPRResult: &vcs.PR{Number: 7, Branch: "feature/z", URL: "https://example.test/pull/7"},
		mergeErr:     errors.New("merge refused: not mergeable"),
	}
	r := newRunForTest(t, dir, "main", prov)
	r.cfg.MergeRepairAttempts = 0

	err := r.handleCleanupPR(context.Background(), 42, "Fix the thing")
	if err == nil {
		t.Fatal("expected an error when the merge is refused and repair is disabled")
	}
	if !strings.Contains(err.Error(), "merge refused") {
		t.Errorf("error should explain the merge refusal; got: %v", err)
	}
	if prov.resolved != 0 {
		t.Errorf("must NOT resolve an unmerged issue (resolved=%d)", prov.resolved)
	}
}

func TestWorktreePath(t *testing.T) {
	cfg := config.Defaults()
	cfg.ProjectRoot = "/home/me/code/myrepo"

	cfg.WorktreeDir = ""
	got := worktreePath(&cfg, 7)
	if !strings.Contains(got, "ralph-worktrees") || !strings.HasSuffix(got, "issue-7") {
		t.Errorf("default worktree path = %q; want a ralph-worktrees/.../issue-7 path", got)
	}
	if !strings.Contains(got, "myrepo") {
		t.Errorf("default worktree path should be keyed by repo name; got %q", got)
	}

	cfg.WorktreeDir = "/scratch/wts"
	if got := worktreePath(&cfg, 12); got != "/scratch/wts/issue-12" {
		t.Errorf("explicit worktree path = %q; want /scratch/wts/issue-12", got)
	}
}

func TestDashboardTextHelpers(t *testing.T) {
	if got := firstLine("\n\n  hello\nworld"); got != "  hello" {
		t.Errorf("firstLine = %q", got)
	}
	if got := lastLine("a\nb\n\n  c  \n\n"); got != "c" {
		t.Errorf("lastLine = %q; want c", got)
	}
	if got := lastLine("   "); got != "" {
		t.Errorf("lastLine of blank = %q; want empty", got)
	}
	if got := clip("abcdef", 4); got != "abc…" {
		t.Errorf("clip = %q; want abc…", got)
	}
	if got := clip("hi", 10); got != "hi" {
		t.Errorf("clip should pass short strings through; got %q", got)
	}
	if got := elapsed(time.Now().Add(-(2*time.Minute + 5*time.Second))); got != "2m05s" {
		t.Errorf("elapsed = %q; want 2m05s", got)
	}
}
