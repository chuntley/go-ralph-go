package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chuntley/go-ralph-go/internal/claude"
)

// runPassTest runs runPassWithRetry with a fixed label and zero backoff so
// retries don't sleep.
func runPassTest(ctx context.Context, retries int, timeout time.Duration,
	startFresh func() error, runOnce func(context.Context) error, logf func(string)) error {
	return runPassWithRetry(ctx, "pass 1", retries, timeout, 0, startFresh, runOnce, logf)
}

func TestRunPassCrashRetriesThenSucceeds(t *testing.T) {
	var runs, fresh int
	runOnce := func(context.Context) error {
		runs++
		if runs < 3 { // crash twice, then succeed
			return fmt.Errorf("claude exited: exit status 143")
		}
		return nil
	}
	startFresh := func() error { fresh++; return nil }

	err := runPassTest(context.Background(), 2, 0, startFresh, runOnce, func(string) {})
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if runs != 3 {
		t.Errorf("expected 3 run attempts (2 crashes + 1 success), got %d", runs)
	}
	if fresh != 2 { // one StartFresh before each of the 2 retries
		t.Errorf("expected 2 StartFresh calls before retries, got %d", fresh)
	}
}

func TestRunPassCrashExhaustsRetries(t *testing.T) {
	var runs int
	runOnce := func(context.Context) error {
		runs++
		return fmt.Errorf("claude exited: exit status 143")
	}

	err := runPassTest(context.Background(), 2, 0, func() error { return nil }, runOnce, func(string) {})
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if runs != 3 { // initial + 2 retries
		t.Errorf("expected 3 run attempts, got %d", runs)
	}
	// The exhausted error must NOT wrap a context error, or dispatchCleanup would
	// misclassify a repeated timeout as a requeue instead of a genuine failure.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Errorf("exhausted error should not wrap a context error: %v", err)
	}
	if !strings.Contains(err.Error(), "after 2 retries") {
		t.Errorf("error should note retry exhaustion, got: %v", err)
	}
}

func TestRunPassTimeoutRetriesInPlace(t *testing.T) {
	var runs int
	// Each attempt blocks past the timeout, so the per-pass ctx expires and Run
	// returns its DeadlineExceeded — the same path a real timeout takes.
	runOnce := func(ctx context.Context) error {
		runs++
		<-ctx.Done()
		return ctx.Err()
	}
	var logged string
	logf := func(s string) { logged += s + "\n" }

	err := runPassWithRetry(context.Background(), "pass 1", 1, 10*time.Millisecond, 0,
		func() error { return nil }, runOnce, logf)
	if err == nil {
		t.Fatal("expected failure after timeout retries exhausted")
	}
	if runs != 2 { // initial + 1 retry, both time out
		t.Errorf("expected 2 attempts, got %d", runs)
	}
	if !strings.Contains(logged, "timed out after 10ms") {
		t.Errorf("retry log should describe the timeout, got: %q", logged)
	}
	// Classification-critical: a genuinely-exhausted TIMEOUT must not wrap a
	// context error, or dispatchCleanup would requeue it forever instead of
	// marking the issue failed.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Errorf("exhausted-timeout error must not wrap a context error: %v", err)
	}
}

func TestRunPassCtxCancelNotRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // parent already cancelled — a real Ctrl+C, not our per-pass timeout

	var runs int
	runOnce := func(context.Context) error {
		runs++
		return context.Canceled
	}

	err := runPassTest(ctx, 3, 0, func() error { return nil }, runOnce, func(string) {})
	if err == nil {
		t.Fatal("expected the cancellation to propagate")
	}
	if runs != 1 {
		t.Errorf("a parent cancellation must not be retried, got %d attempts", runs)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation must stay unwrapped for requeue classification, got: %v", err)
	}
}

func TestRunPassAuthErrorNotRetried(t *testing.T) {
	var runs int
	runOnce := func(context.Context) error {
		runs++
		return fmt.Errorf("%w: run `claude auth login`", claude.ErrNotLoggedIn)
	}

	err := runPassTest(context.Background(), 3, 0, func() error { return nil }, runOnce, func(string) {})
	if err == nil {
		t.Fatal("expected the auth error to propagate")
	}
	if runs != 1 {
		t.Errorf("an auth failure must not be retried, got %d attempts", runs)
	}
	if !errors.Is(err, claude.ErrNotLoggedIn) {
		t.Errorf("auth error must stay wrapped so the run halts, got: %v", err)
	}
}

func TestRunPassUsesLabelInError(t *testing.T) {
	// The guard is shared by refine/cleanup/repair turns via a label; the label
	// must surface in the returned error so operators can tell which turn failed.
	runOnce := func(context.Context) error { return fmt.Errorf("boom") }
	err := runPassWithRetry(context.Background(), "cleanup pass", 0, 0, 0,
		func() error { return nil }, runOnce, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "cleanup pass") {
		t.Errorf("error should carry the label, got: %v", err)
	}
}
