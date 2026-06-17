package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
}

func (f *fakeProvider) Name() string                                   { return "fake" }
func (f *fakeProvider) Whoami(context.Context) (string, error)         { return "fake", nil }
func (f *fakeProvider) EnsureLabels(context.Context, vcs.Labels) error { return nil }
func (f *fakeProvider) NextReady(context.Context, vcs.Labels) (*vcs.Issue, error) {
	return nil, vcs.ErrNoReadyIssue
}
func (f *fakeProvider) GetIssue(context.Context, int) (*vcs.Issue, error)  { return nil, nil }
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
func (f *fakeProvider) FindPRForBranch(context.Context, string) (*vcs.PR, error) {
	return f.findPRResult, f.findPRError
}
func (f *fakeProvider) CreatePR(_ context.Context, head, base, title, body string) (*vcs.PR, error) {
	f.createdPR = &vcs.PR{Number: 999, Branch: head, URL: "https://example.test/pr/999"}
	f.createPRTitle = title
	f.createPRBody = body
	return f.createdPR, f.createPRError
}
func (f *fakeProvider) WaitForChecks(context.Context, int, time.Duration) error { return nil }
func (f *fakeProvider) SquashMergeAndDelete(context.Context, int) error         { return nil }

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

func TestAutoHaltReasonNoHaltOnNotLoggedIn(t *testing.T) {
	// Mirrors dispatchCleanup: the issue was requeued, so this classifier does
	// not produce a "labelled failed" halt. RunAuto halts the worker with the
	// auth fix explicitly, before calling autoHaltReason.
	if halt := autoHaltReason(1, "ralph-failed", fmt.Errorf("pass 1: %w", claude.ErrNotLoggedIn)); halt != nil {
		t.Errorf("auth failure must not halt-as-failed (issue requeued), got %v", halt)
	}
}

func TestCleanupDispatchNilNoop(t *testing.T) {
	p := &fakeProvider{}
	dispatchCleanup(p, 1, vcs.Labels{}, nil)
	if p.requeued != 0 || p.failedReason != "" {
		t.Errorf("nil error should be a noop, got requeued=%d failedReason=%q", p.requeued, p.failedReason)
	}
}

func TestAutoHaltReasonHaltsOnFailure(t *testing.T) {
	// A genuine failure (issue gets the ralph-failed label) must halt auto with
	// an exit reason that names the issue, the label, and wraps the cause.
	cause := errors.New("checks failed on PR #5")
	halt := autoHaltReason(99, "ralph-failed", fmt.Errorf("merge: %w", cause))
	if halt == nil {
		t.Fatal("expected a halt reason on genuine failure")
	}
	if !errors.Is(halt, cause) {
		t.Error("halt reason should wrap the underlying cause")
	}
	for _, want := range []string{"halting", "#99", "ralph-failed"} {
		if !strings.Contains(halt.Error(), want) {
			t.Errorf("halt reason %q missing %q", halt.Error(), want)
		}
	}
}

func TestAutoHaltReasonNoHaltOnSuccessOrCancel(t *testing.T) {
	if halt := autoHaltReason(1, "ralph-failed", nil); halt != nil {
		t.Errorf("success must not halt, got %v", halt)
	}
	// Cancellation / timeout means the issue was requeued, not failed — no halt.
	if halt := autoHaltReason(1, "ralph-failed", fmt.Errorf("pass 3: %w", context.Canceled)); halt != nil {
		t.Errorf("cancellation must not halt (issue requeued), got %v", halt)
	}
	if halt := autoHaltReason(1, "ralph-failed", context.DeadlineExceeded); halt != nil {
		t.Errorf("deadline must not halt, got %v", halt)
	}
}
