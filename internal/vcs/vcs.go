// Package vcs defines a provider-agnostic interface for the GitHub-style
// orchestration ralph does (labels, ready-issue polling, PR check-watching,
// squash-merge) so additional hosts (GitLab, etc.) can plug in.
package vcs

import (
	"context"
	"errors"
	"time"
)

// ErrNoReadyIssue is returned by NextReady when nothing is queued.
var ErrNoReadyIssue = errors.New("no ready issue")

// Issue is the minimal cross-host issue shape ralph needs.
type Issue struct {
	Number int
	Title  string
	Body   string
}

// PR is the minimal cross-host pull/merge-request shape ralph needs.
type PR struct {
	Number int
	Branch string
}

// Labels names the three labels in the ralph state machine.
type Labels struct {
	Ready, Working, Failed string
}

// Provider is the host-side interface ralph drives. Implementations should be
// safe for sequential use; ralph does not run multiple issues concurrently.
type Provider interface {
	// Name returns the host short name ("github", "gitlab", ...). Used in logs.
	Name() string

	// Whoami returns the authenticated user identifier (login/handle). It
	// double-doubles as a token-validity check — a failed call means the
	// credentials are bad. Used by `ralph doctor`.
	Whoami(ctx context.Context) (string, error)

	// EnsureLabels creates any missing labels in the configured set.
	EnsureLabels(ctx context.Context, l Labels) error

	// NextReady returns the oldest open issue with the ready label, or
	// ErrNoReadyIssue if none are queued.
	NextReady(ctx context.Context, l Labels) (*Issue, error)

	// GetIssue fetches a single issue by number.
	GetIssue(ctx context.Context, number int) (*Issue, error)

	// MarkWorking transitions an issue from ready → working.
	MarkWorking(ctx context.Context, number int, l Labels) error

	// MarkFailed transitions an issue from working/ready → failed and posts a
	// comment explaining the failure.
	MarkFailed(ctx context.Context, number int, l Labels, reason string) error

	// MarkResolved removes the working label and closes the issue. Called
	// after a successful merge as a defensive backstop in case Claude's PR
	// body lacked "Closes #N" — without this, the issue would stay open with
	// the working label forever (invisible to NextReady, unfindable).
	MarkResolved(ctx context.Context, number int, l Labels) error

	// FindPRForBranch returns the open PR/MR for the given source branch, if any.
	FindPRForBranch(ctx context.Context, branch string) (*PR, error)

	// WaitForChecks blocks until all required checks finish, returning nil on
	// success or an error describing the failure. Polling cadence is up to
	// the implementation; interval is a suggestion.
	WaitForChecks(ctx context.Context, prNumber int, interval time.Duration) error

	// SquashMergeAndDelete squash-merges the PR/MR and deletes the source branch.
	SquashMergeAndDelete(ctx context.Context, prNumber int) error
}
