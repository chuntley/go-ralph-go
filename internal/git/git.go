package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// CurrentBranch returns the abbreviated HEAD ref for repo at dir.
func CurrentBranch(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, dir, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// CheckoutMain checks out the local main branch and fast-forwards.
func CheckoutMain(ctx context.Context, dir, mainBranch string) error {
	if _, err := run(ctx, dir, "git", "checkout", mainBranch); err != nil {
		return fmt.Errorf("checkout %s: %w", mainBranch, err)
	}
	if _, err := run(ctx, dir, "git", "pull", "--ff-only"); err != nil {
		return fmt.Errorf("pull --ff-only: %w", err)
	}
	return nil
}

// CreateWorkBranch creates a NEW work branch off the current HEAD and checks it
// out, using `git checkout -B` (which resets it if it somehow already exists).
// The caller is expected to have checked out and fast-forwarded the default
// branch first (see CheckoutMain), so the new branch branches from up-to-date
// upstream. To PRESERVE a prior run's commits on an existing branch, callers
// check LocalBranchExists first and use CheckoutBranch instead (see
// makeWorkspace) — mirroring the worktree-mode AddWorktreeResume path.
func CreateWorkBranch(ctx context.Context, dir, branch string) error {
	if _, err := run(ctx, dir, "git", "checkout", "-B", branch); err != nil {
		return fmt.Errorf("create work branch %s: %w", branch, err)
	}
	return nil
}

// CheckoutBranch switches to an existing local branch, restoring its committed
// state. Unlike CreateWorkBranch it does not reset the branch, so a prior run's
// commits on it are preserved — the in-place counterpart of AddWorktreeResume.
func CheckoutBranch(ctx context.Context, dir, branch string) error {
	if _, err := run(ctx, dir, "git", "checkout", branch); err != nil {
		return fmt.Errorf("checkout %s: %w", branch, err)
	}
	return nil
}

// Push pushes branch to origin and sets upstream. Idempotent: re-pushing an
// already-pushed branch with no new commits is a no-op success. ralph uses this
// to make sure a branch the agent committed to is on the remote before opening
// a PR for it.
func Push(ctx context.Context, dir, branch string) error {
	if _, err := run(ctx, dir, "git", "push", "-u", "origin", branch); err != nil {
		return fmt.Errorf("push %s: %w", branch, err)
	}
	return nil
}

// ForcePush pushes branch to origin with --force-with-lease, used after a
// merge-repair pass that may have rebased the branch onto an updated default
// branch (rewriting history) — a plain push would be rejected as non-fast-
// forward. --force-with-lease (rather than --force) refuses to clobber remote
// commits ralph hasn't seen, so a human pushing to the same branch isn't
// silently overwritten.
func ForcePush(ctx context.Context, dir, branch string) error {
	if _, err := run(ctx, dir, "git", "push", "--force-with-lease", "-u", "origin", branch); err != nil {
		return fmt.Errorf("force-push %s: %w", branch, err)
	}
	return nil
}

// Fetch updates remote-tracking refs for ref from remote WITHOUT touching any
// working tree. ralph uses this in worktree mode to refresh the base branch
// (e.g. origin/main) before cutting a per-issue worktree or rebasing one for a
// merge repair — all without disturbing the user's root checkout.
func Fetch(ctx context.Context, dir, remote, ref string) error {
	if _, err := run(ctx, dir, "git", "fetch", remote, ref); err != nil {
		return fmt.Errorf("fetch %s %s: %w", remote, ref, err)
	}
	return nil
}

// AddWorktree creates an isolated working tree at path for an issue, on a fresh
// branch cut from base (typically "origin/<default>"). It uses -B so it resets
// the branch to base rather than failing on "already exists"; callers that want
// to PRESERVE a prior run's commits on an existing branch use AddWorktreeResume
// instead (see makeWorkspace). The repo at repoDir is never checked out or
// modified — only the new worktree dir is — so a user can keep working in the
// root checkout while ralph runs here in parallel.
func AddWorktree(ctx context.Context, repoDir, path, branch, base string) error {
	if _, err := run(ctx, repoDir, "git", "worktree", "add", "-B", branch, path, base); err != nil {
		return fmt.Errorf("worktree add %s (%s off %s): %w", path, branch, base, err)
	}
	return nil
}

// AddWorktreeResume checks out an EXISTING branch into a new worktree at path,
// preserving that branch's commits — unlike AddWorktree, whose -B resets the
// branch to a base. Used when a prior run for the same issue left commits on the
// branch and we want to build on them rather than discard them. Fails if branch
// is already checked out in another worktree.
func AddWorktreeResume(ctx context.Context, repoDir, path, branch string) error {
	if _, err := run(ctx, repoDir, "git", "worktree", "add", path, branch); err != nil {
		return fmt.Errorf("worktree add %s (resume branch %s): %w", path, branch, err)
	}
	return nil
}

// LocalBranchExists reports whether branch exists as a local ref in the repo at
// dir.
func LocalBranchExists(ctx context.Context, dir, branch string) bool {
	_, err := run(ctx, dir, "git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// RemoveWorktree tears down a worktree created by AddWorktree and prunes the
// administrative record. Uses --force because the worktree may hold uncommitted
// scratch files (e.g. a .ralph/ output dir) that a plain remove would refuse to
// discard; the branch ref itself survives removal, so committed work is never
// lost — a later run can resume the branch (see AddWorktreeResume).
//
// The prune runs UNCONDITIONALLY, even when `remove` fails (e.g. the directory
// was already deleted by a crashed run): otherwise a stale registration lingers
// and would block re-adding the same branch with "already checked out".
func RemoveWorktree(ctx context.Context, repoDir, path string) error {
	_, removeErr := run(ctx, repoDir, "git", "worktree", "remove", "--force", path)
	_, _ = run(ctx, repoDir, "git", "worktree", "prune")
	if removeErr != nil {
		return fmt.Errorf("worktree remove %s: %w", path, removeErr)
	}
	return nil
}

// CommitsAhead returns how many commits branch is ahead of base (i.e. the
// commits that would be merged), using local refs. Zero means the branch holds
// no work to open a PR for.
func CommitsAhead(ctx context.Context, dir, base, branch string) (int, error) {
	out, err := run(ctx, dir, "git", "rev-list", "--count", base+".."+branch)
	if err != nil {
		return 0, fmt.Errorf("rev-list %s..%s: %w", base, branch, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parse commit count %q: %w", strings.TrimSpace(out), err)
	}
	return n, nil
}

// IsClean reports whether the working tree and index are clean.
func IsClean(ctx context.Context, dir string) (bool, error) {
	out, err := run(ctx, dir, "git", "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return strings.TrimSpace(out) == "", nil
}

// OriginURL returns the URL of the `origin` remote.
func OriginURL(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, dir, "git", "config", "--get", "remote.origin.url")
	if err != nil {
		return "", errors.New("no origin remote configured")
	}
	return strings.TrimSpace(out), nil
}

// IsRepo reports whether dir is inside a git work tree.
func IsRepo(ctx context.Context, dir string) bool {
	_, err := run(ctx, dir, "git", "rev-parse", "--is-inside-work-tree")
	return err == nil
}

// DefaultBranch returns the default branch reported by origin, falling back
// to "main" then "master". A network-free implementation is preferred but
// not always possible.
func DefaultBranch(ctx context.Context, dir string) (string, error) {
	// Try symbolic-ref of origin/HEAD (set by `git clone` / `git remote set-head`).
	if out, err := run(ctx, dir, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		ref := strings.TrimSpace(out)
		if idx := strings.Index(ref, "/"); idx >= 0 {
			return ref[idx+1:], nil
		}
	}
	// Fall back to local main/master existence.
	for _, candidate := range []string{"main", "master"} {
		if _, err := run(ctx, dir, "git", "rev-parse", "--verify", candidate); err == nil {
			return candidate, nil
		}
	}
	return "main", nil
}

func run(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Surface git's own diagnostic text alongside the exit status so
		// callers' wraps carry the real reason (e.g. "fatal: Not possible to
		// fast-forward, aborting") instead of just "exit status 128". Without
		// this, CheckoutMain failures in auto mode are essentially undebuggable
		// from ralph's logs alone.
		if snippet := strings.TrimSpace(string(out)); snippet != "" {
			return string(out), fmt.Errorf("%s: %w", snippet, err)
		}
	}
	return string(out), err
}
