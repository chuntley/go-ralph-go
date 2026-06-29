package runner

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/chuntley/go-ralph-go/internal/claude"
	"github.com/chuntley/go-ralph-go/internal/vcs"
)

// fakeProvider is a tiny stub recording which dispatch path the cleanup defer
// took. We don't need a full Provider — only the methods invoked in the defer
// and (for handleCleanupPR tests) FindPRForBranch / MarkResolved.
type fakeProvider struct {
	// Recorded calls
	failedReason string
	requeued     int
	resolved     int // issue number passed to MarkResolved, or 0 if never called
	skipped      int // issue number passed to MarkSkipped, or 0 if never called

	// Configurable return for FindPRForBranch — handleCleanupPR tests inject
	// "PR exists" / "no PR" outcomes through these. Defaults (nil, nil) keep
	// the historic dispatchCleanup tests untouched.
	findPRResult *vcs.PR
	findPRError  error

	// CreatePR recording + configurable error, for the ensurePR fallback tests.
	createdPR     *vcs.PR
	createPRTitle string
	createPRBody  string
	createPRError error

	// Configurable merge-phase outcomes for the merge-repair tests. Defaults
	// (nil) keep the historic happy-path tests untouched. waitErrSeq/mergeErrSeq,
	// when set, are consumed one entry per call so a test can model "fails then
	// succeeds"; a shorter sequence falls back to the scalar field / nil.
	waitErr     error
	mergeErr    error
	waitErrSeq  []error
	mergeErrSeq []error
	waitCalls   int
	mergeCalls  int
}

func (f *fakeProvider) Name() string                                   { return "fake" }
func (f *fakeProvider) Whoami(context.Context) (string, error)         { return "fake", nil }
func (f *fakeProvider) EnsureLabels(context.Context, vcs.Labels) error { return nil }
func (f *fakeProvider) NextReady(context.Context, vcs.Labels) (*vcs.Issue, error) {
	return nil, vcs.ErrNoReadyIssue
}
func (f *fakeProvider) GetIssue(_ context.Context, n int) (*vcs.Issue, error) {
	return &vcs.Issue{Number: n, Title: fmt.Sprintf("issue %d", n)}, nil
}
func (f *fakeProvider) MarkWorking(context.Context, int, vcs.Labels) error { return nil }
func (f *fakeProvider) MarkFailed(_ context.Context, n int, _ vcs.Labels, reason string) error {
	f.failedReason = reason
	return nil
}
func (f *fakeProvider) MarkRequeued(_ context.Context, n int, _ vcs.Labels) error {
	f.requeued = n
	return nil
}
func (f *fakeProvider) MarkResolved(_ context.Context, n int, _ vcs.Labels) error {
	f.resolved = n
	return nil
}
func (f *fakeProvider) MarkSkipped(_ context.Context, n int, _ vcs.Labels, _ string) error {
	f.skipped = n
	return nil
}
func (f *fakeProvider) FindPRForBranch(context.Context, string) (*vcs.PR, error) {
	return f.findPRResult, f.findPRError
}
func (f *fakeProvider) CreatePR(_ context.Context, head, base, title, body string) (*vcs.PR, error) {
	f.createdPR = &vcs.PR{Number: 999, Branch: head, URL: "https://example.test/pr/999"}
	f.createPRTitle = title
	f.createPRBody = body
	return f.createdPR, f.createPRError
}
func (f *fakeProvider) WaitForChecks(context.Context, int, time.Duration) error {
	i := f.waitCalls
	f.waitCalls++
	if i < len(f.waitErrSeq) {
		return f.waitErrSeq[i]
	}
	return f.waitErr
}
func (f *fakeProvider) SquashMergeAndDelete(context.Context, int) error {
	i := f.mergeCalls
	f.mergeCalls++
	if i < len(f.mergeErrSeq) {
		return f.mergeErrSeq[i]
	}
	return f.mergeErr
}

func TestCleanupDispatchInterruptedRequeues(t *testing.T) {
	p := &fakeProvider{}
	dispatchCleanup(p, 42, vcs.Labels{}, fmt.Errorf("iteration 3: %w", context.Canceled))
	if p.requeued != 42 {
		t.Errorf("expected requeue of issue 42, got requeued=%d", p.requeued)
	}
	if p.failedReason != "" {
		t.Errorf("interrupted run should NOT mark failed, got reason %q", p.failedReason)
	}
}

func TestCleanupDispatchDeadlineExceededRequeues(t *testing.T) {
	p := &fakeProvider{}
	dispatchCleanup(p, 7, vcs.Labels{}, context.DeadlineExceeded)
	if p.requeued != 7 {
		t.Errorf("expected requeue, got requeued=%d", p.requeued)
	}
}

func TestCleanupDispatchOtherErrorMarksFailed(t *testing.T) {
	p := &fakeProvider{}
	dispatchCleanup(p, 99, vcs.Labels{}, errors.New("PR refused: branch protection"))
	if p.requeued != 0 {
		t.Errorf("expected NO requeue on real failure, got %d", p.requeued)
	}
	if p.failedReason == "" {
		t.Errorf("expected failure reason to be posted")
	}
}

func TestCleanupDispatchNotLoggedInRequeues(t *testing.T) {
	// A claude auth failure is a local environment problem, not the issue's
	// fault. The issue must be requeued (returned to the ready queue), NOT burned
	// into the failed pile with a public failure comment.
	p := &fakeProvider{}
	dispatchCleanup(p, 13, vcs.Labels{}, fmt.Errorf("pass 1: %w", claude.ErrNotLoggedIn))
	if p.requeued != 13 {
		t.Errorf("expected requeue of issue 13 on auth failure, got requeued=%d", p.requeued)
	}
	if p.failedReason != "" {
		t.Errorf("auth failure must NOT mark the issue failed, got reason %q", p.failedReason)
	}
}

func TestCleanupDispatchIssueGoneRequeues(t *testing.T) {
	// A not-actionable issue (closed/merged/PR, e.g. re-dispatched after it
	// shipped) must be requeued (working label cleared), NOT marked failed —
	// otherwise the failed count is inflated with no GitHub evidence.
	p := &fakeProvider{}
	dispatchCleanup(p, 55, vcs.Labels{}, fmt.Errorf("%w: issue #55 is closed", errIssueGone))
	if p.requeued != 55 {
		t.Errorf("errIssueGone should requeue issue 55, got requeued=%d", p.requeued)
	}
	if p.failedReason != "" {
		t.Errorf("errIssueGone must NOT mark the issue failed, got reason %q", p.failedReason)
	}
}

func TestCleanupDispatchNilNoop(t *testing.T) {
	p := &fakeProvider{}
	dispatchCleanup(p, 1, vcs.Labels{}, nil)
	if p.requeued != 0 || p.failedReason != "" {
		t.Errorf("nil error should be a noop, got requeued=%d failedReason=%q", p.requeued, p.failedReason)
	}
}
