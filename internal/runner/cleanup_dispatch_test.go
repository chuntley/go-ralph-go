package runner

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/chuntley/go-ralph-go/internal/vcs"
)

// fakeProvider is a tiny stub recording which dispatch path the cleanup defer
// took. We don't need a full Provider — only the methods invoked in the defer.
type fakeProvider struct {
	failedReason string
	requeued     int
}

func (f *fakeProvider) Name() string                              { return "fake" }
func (f *fakeProvider) Whoami(context.Context) (string, error)    { return "fake", nil }
func (f *fakeProvider) EnsureLabels(context.Context, vcs.Labels) error { return nil }
func (f *fakeProvider) NextReady(context.Context, vcs.Labels) (*vcs.Issue, error) {
	return nil, vcs.ErrNoReadyIssue
}
func (f *fakeProvider) GetIssue(context.Context, int) (*vcs.Issue, error) { return nil, nil }
func (f *fakeProvider) MarkWorking(context.Context, int, vcs.Labels) error { return nil }
func (f *fakeProvider) MarkFailed(_ context.Context, n int, _ vcs.Labels, reason string) error {
	f.failedReason = reason
	return nil
}
func (f *fakeProvider) MarkRequeued(_ context.Context, n int, _ vcs.Labels) error {
	f.requeued = n
	return nil
}
func (f *fakeProvider) MarkResolved(context.Context, int, vcs.Labels) error { return nil }
func (f *fakeProvider) FindPRForBranch(context.Context, string) (*vcs.PR, error) {
	return nil, nil
}
func (f *fakeProvider) WaitForChecks(context.Context, int, time.Duration) error { return nil }
func (f *fakeProvider) SquashMergeAndDelete(context.Context, int) error          { return nil }

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

func TestCleanupDispatchNilNoop(t *testing.T) {
	p := &fakeProvider{}
	dispatchCleanup(p, 1, vcs.Labels{}, nil)
	if p.requeued != 0 || p.failedReason != "" {
		t.Errorf("nil error should be a noop, got requeued=%d failedReason=%q", p.requeued, p.failedReason)
	}
}
