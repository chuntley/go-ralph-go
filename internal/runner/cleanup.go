package runner

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/chuntley/go-ralph-go/internal/claude"
	"github.com/chuntley/go-ralph-go/internal/vcs"
)

// cleanupTimeout caps how long the post-failure label cleanup may run. Used
// with a *fresh* background context (not the cancelled runtime ctx) so a
// Ctrl+C-during-iteration doesn't also kill the label cleanup itself.
const cleanupTimeout = 30 * time.Second

// dispatchCleanup is the body of processIssue's deferred cleanup. Extracted so
// it can be exercised directly in tests.
//
//	resultErr == nil           → no-op (success path).
//	ctx.Canceled / deadline    → requeue: clear working/failed, restore ready.
//	claude not-logged-in       → requeue: a local auth/environment problem, not
//	                             the issue's fault — don't burn it into the
//	                             failed pile; the worker halts separately.
//	anything else              → mark failed with the (truncated) reason.
//
// Uses a fresh background context with cleanupTimeout — the originating ctx
// may already be cancelled (that's how we got here on Ctrl+C).
func dispatchCleanup(p vcs.Provider, issueNum int, labels vcs.Labels, resultErr error) {
	if resultErr == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if errors.Is(resultErr, context.Canceled) || errors.Is(resultErr, context.DeadlineExceeded) || errors.Is(resultErr, claude.ErrNotLoggedIn) {
		_ = p.MarkRequeued(ctx, issueNum, labels)
		return
	}
	_ = p.MarkFailed(ctx, issueNum, labels, truncate(redactSecrets(resultErr.Error()), maxReasonLen))
}

// autoHaltReason decides whether an auto-loop iteration should halt the whole
// worker. It mirrors dispatchCleanup's classification so the two stay in sync:
//
//	processErr == nil                     → no halt (success).
//	ctx.Canceled / DeadlineExceeded       → no halt: the issue was requeued
//	                                         (Ctrl+C / timeout), handled by the
//	                                         normal cancellation path.
//	claude not-logged-in                  → no halt here: the issue was requeued
//	                                         (mirrors dispatchCleanup); RunAuto
//	                                         halts the worker with the auth fix
//	                                         explicitly, before calling this.
//	anything else                         → halt: the issue was just labelled
//	                                         failed, so return an exit reason.
//
// Returning an error (rather than logging and continuing) is what makes
// `ralph auto` stop the moment an issue is marked failed instead of moving on
// to the next one.
func autoHaltReason(issueNum int, failedLabel string, processErr error) error {
	if processErr == nil {
		return nil
	}
	if errors.Is(processErr, context.Canceled) || errors.Is(processErr, context.DeadlineExceeded) || errors.Is(processErr, claude.ErrNotLoggedIn) {
		return nil
	}
	return fmt.Errorf("auto mode halting: issue #%d was labelled %q after failing — review it before resuming auto: %w", issueNum, failedLabel, processErr)
}

// secretPatterns matches credentials we may inadvertently include in error
// strings. The MarkFailed reason is posted as a public issue/MR comment, so a
// PAT embedded in a wrapped git error (e.g. `https://x-access-token:TOKEN@host`
// in `git pull` stderr) would otherwise be visible to anyone who can see the
// issue. Each pattern collapses to "***" via redactSecrets.
//
// Coverage:
//   - URL userinfo: scheme://user:pass@host or scheme://token@host
//   - GitHub tokens: ghp_/gho_/ghs_/ghu_/ghr_/github_pat_ prefixes
//   - GitLab PATs: glpat- prefix
//
// New token formats can be added here without changing callers.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(https?://)[^@/\s]+@`),
	regexp.MustCompile(`gh[opsur]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`glpat-[A-Za-z0-9_\-]{20,}`),
}

func redactSecrets(s string) string {
	for i, re := range secretPatterns {
		if i == 0 {
			s = re.ReplaceAllString(s, "${1}***@")
			continue
		}
		s = re.ReplaceAllString(s, "***")
	}
	return s
}
