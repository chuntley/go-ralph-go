package vcs

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// urlCredsRE matches the userinfo segment of a URL — i.e. the "user:pass@"
// or "token@" that may sit between any scheme and the host. Matches any
// alphabetic scheme rather than just http(s) so error paths for unrecognised
// schemes (`ftp://...`, `s3://...`) also get redacted. Used by redactURLCreds
// below so ParseRemote error messages don't echo embedded credentials onto
// stderr/stdout (e.g. `git config remote.origin.url` set to
// `https://x-access-token:ghp_...@github.com/...`).
var urlCredsRE = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^@/\s]+@`)

// redactURLCreds masks userinfo in any URL-shaped substring of s so it is safe
// to embed in error messages that may surface to stdout, stderr, or logs.
func redactURLCreds(s string) string {
	return urlCredsRE.ReplaceAllString(s, "${1}***@")
}

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
			return nil, fmt.Errorf("malformed scp-style remote: %q", redactURLCreds(raw))
		}
		host = rest[:colon]
		path = rest[colon+1:]
	case strings.HasPrefix(raw, "ssh://") || strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://"):
		u, err := url.Parse(raw)
		if err != nil {
			// url.Parse's error text includes the original URL, which may carry
			// userinfo. Redact before wrapping. %s (not %w) because we're
			// rewriting the underlying message; the lost errors.Is chain isn't
			// used by callers here.
			return nil, fmt.Errorf("parse url: %s", redactURLCreds(err.Error()))
		}
		host = u.Host
		path = strings.TrimPrefix(u.Path, "/")
	default:
		return nil, fmt.Errorf("unrecognised remote url: %q", redactURLCreds(raw))
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
