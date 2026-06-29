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
	// URL is the web URL of the PR/MR (e.g. https://github.com/o/r/pull/42).
	// Populated by FindPRForBranch so ralph can surface a clickable link in
	// the terminal after a successful merge.
	URL string
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

	// MarkRequeued returns an issue to the ready queue without marking it
	// failed. Used when ralph was interrupted (Ctrl+C) and the user likely
	// wants the next run to pick this issue back up.
	MarkRequeued(ctx context.Context, number int, l Labels) error

	// MarkResolved removes the working label and closes the issue. Called
	// after a successful merge as a defensive backstop in case Claude's PR
	// body lacked "Closes #N" — without this, the issue would stay open with
	// the working label forever (invisible to NextReady, unfindable).
	MarkResolved(ctx context.Context, number int, l Labels) error

	// MarkSkipped closes the issue with an explanatory note WITHOUT marking it
	// failed. Used when ralph determined no changes were needed because the work
	// already exists on the base branch (the loop produced no commits): that's a
	// resolved/no-op outcome, not a failure that needs triage.
	MarkSkipped(ctx context.Context, number int, l Labels, note string) error

	// FindPRForBranch returns the open PR/MR for the given source branch, if any.
	FindPRForBranch(ctx context.Context, branch string) (*PR, error)

	// CreatePR opens a pull/merge request from headBranch into baseBranch with
	// the given title and body, returning the created PR. ralph uses this as a
	// fallback when the cleanup pass committed and pushed work but did not open
	// the PR itself — so the run doesn't fail just because the agent ran out of
	// turn. headBranch must already exist on the remote.
	CreatePR(ctx context.Context, headBranch, baseBranch, title, body string) (*PR, error)

	// WaitForChecks blocks until all required checks finish, returning nil on
	// success or an error describing the failure. Polling cadence is up to
	// the implementation; interval is a suggestion.
	WaitForChecks(ctx context.Context, prNumber int, interval time.Duration) error

	// SquashMergeAndDelete squash-merges the PR/MR and deletes the source branch.
	SquashMergeAndDelete(ctx context.Context, prNumber int) error
}
