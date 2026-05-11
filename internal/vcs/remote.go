package vcs

import (
	"fmt"
	"net/url"
	"strings"
)

// Remote describes a parsed origin URL.
type Remote struct {
	Host    string // e.g. github.com, gitlab.com, gitlab.example.com
	Owner   string // user or group
	Repo    string // repo name without ".git"
	Project string // for GitLab: full path "group/subgroup/repo"; for GitHub: "owner/repo"
}

// ParseRemote understands the standard origin URL shapes:
//
//	git@github.com:owner/repo.git
//	ssh://git@github.com/owner/repo.git
//	https://github.com/owner/repo.git
//	https://gitlab.com/group/subgroup/repo.git
//
// For GitLab nested groups, Project preserves the full path; Owner is the
// top-level group and Repo is the leaf.
func ParseRemote(raw string) (*Remote, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty remote url")
	}

	var host, path string

	switch {
	case strings.HasPrefix(raw, "git@"):
		// git@host:path
		rest := strings.TrimPrefix(raw, "git@")
		colon := strings.Index(rest, ":")
		if colon < 0 {
			return nil, fmt.Errorf("malformed scp-style remote: %q", raw)
		}
		host = rest[:colon]
		path = rest[colon+1:]
	case strings.HasPrefix(raw, "ssh://") || strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://"):
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("parse url: %w", err)
		}
		host = u.Host
		path = strings.TrimPrefix(u.Path, "/")
	default:
		return nil, fmt.Errorf("unrecognised remote url: %q", raw)
	}

	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return nil, fmt.Errorf("remote path %q does not look like owner/repo", path)
	}

	return &Remote{
		Host:    host,
		Owner:   parts[0],
		Repo:    parts[len(parts)-1],
		Project: path,
	}, nil
}

// GuessProvider returns "github", "gitlab" or "" given a host. Self-hosted
// installs may set provider explicitly in config.
func GuessProvider(host string) string {
	h := strings.ToLower(host)
	switch {
	case h == "github.com" || strings.HasSuffix(h, ".github.com"):
		return "github"
	case h == "gitlab.com" || strings.Contains(h, "gitlab"):
		return "gitlab"
	}
	return ""
}
