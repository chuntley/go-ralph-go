package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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
