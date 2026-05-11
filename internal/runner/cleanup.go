package runner

import (
	"context"
	"errors"
	"time"

	"github.com/chuntley/go-ralph-go/internal/vcs"
)

// cleanupTimeout caps how long the post-failure label cleanup may run. Used
// with a *fresh* background context (not the cancelled runtime ctx) so a
// Ctrl+C-during-iteration doesn't also kill the label cleanup itself.
const cleanupTimeout = 30 * time.Second

// dispatchCleanup is the body of processIssue's deferred cleanup. Extracted so
// it can be exercised directly in tests.
//
//   resultErr == nil           → no-op (success path).
//   ctx.Canceled / deadline    → requeue: clear working/failed, restore ready.
//   anything else              → mark failed with the (truncated) reason.
//
// Uses a fresh background context with cleanupTimeout — the originating ctx
// may already be cancelled (that's how we got here on Ctrl+C).
func dispatchCleanup(p vcs.Provider, issueNum int, labels vcs.Labels, resultErr error) {
	if resultErr == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if errors.Is(resultErr, context.Canceled) || errors.Is(resultErr, context.DeadlineExceeded) {
		_ = p.MarkRequeued(ctx, issueNum, labels)
		return
	}
	_ = p.MarkFailed(ctx, issueNum, labels, truncate(resultErr.Error(), maxReasonLen))
}
