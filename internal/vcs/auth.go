package vcs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// DiscoverToken returns a credential for the given provider, trying the env
// var first and falling back to the host's CLI (`gh auth token`, `glab auth
// status -t`). Returns ErrNoToken if nothing is available.
func DiscoverToken(ctx context.Context, provider string) (string, error) {
	switch provider {
	case "github":
		if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
			return tok, nil
		}
		if tok, err := runForToken(ctx, "gh", "auth", "token"); err == nil {
			return tok, nil
		}
		return "", ErrNoToken
	case "gitlab":
		if tok := strings.TrimSpace(os.Getenv("GITLAB_TOKEN")); tok != "" {
			return tok, nil
		}
		// glab auth status --show-token writes to stderr; parse the line that
		// starts with "  Token: ". This is fragile but matches glab's UX.
		if tok, err := runGlabToken(ctx); err == nil {
			return tok, nil
		}
		return "", ErrNoToken
	}
	return "", ErrNoToken
}

// ErrNoToken indicates ralph could not locate credentials for the host.
var ErrNoToken = errors.New("no credentials found; set the host token env var or log in with the host CLI")

func runForToken(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", errors.New("empty token")
	}
	return tok, nil
}

func runGlabToken(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "glab", "auth", "status", "--show-token")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Token:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Token:")), nil
		}
	}
	return "", errors.New("could not parse glab token output")
}
