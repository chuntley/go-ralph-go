package vcs

import (
	"context"
	"fmt"
	"strings"

	"github.com/chuntley/go-ralph-go/internal/git"
)

// FactoryConfig carries the inputs needed to build a Provider from a project
// directory. Owner/Repo/Project/Provider/BaseURL are optional overrides; when
// empty they are auto-detected from the git origin remote.
type FactoryConfig struct {
	ProjectDir string

	// Explicit provider name ("github", "gitlab"). Empty means auto-detect.
	Provider string

	// GitHub-style overrides.
	Owner string
	Repo  string

	// GitLab-style overrides.
	Project string // group/subgroup/repo
	BaseURL string // for self-hosted

	// NewGitHub / NewGitLab let the runner package supply concrete constructors
	// without this package importing them (avoiding cycles). Both take a token,
	// plus host-specific identifiers, and return a Provider.
	NewGitHub func(token, owner, repo, baseURL string) (Provider, error)
	NewGitLab func(token, project, baseURL string) (Provider, error)
}

// BuildProvider auto-detects and constructs a Provider for the given project.
func BuildProvider(ctx context.Context, cfg FactoryConfig) (Provider, error) {
	provider := cfg.Provider
	owner, repo, project, baseURL := cfg.Owner, cfg.Repo, cfg.Project, cfg.BaseURL

	if provider == "" || (owner == "" && project == "") {
		raw, err := git.OriginURL(ctx, cfg.ProjectDir)
		if err != nil {
			return nil, fmt.Errorf("auto-detect host: %w", err)
		}
		remote, err := ParseRemote(raw)
		if err != nil {
			return nil, fmt.Errorf("parse origin: %w", err)
		}
		if provider == "" {
			provider = GuessProvider(remote.Host)
			if provider == "" {
				return nil, fmt.Errorf("could not infer provider from host %q; set [github]/[gitlab] in .go-ralph-go or pass an explicit provider", remote.Host)
			}
		}
		if owner == "" {
			owner = remote.Owner
		}
		if repo == "" {
			repo = remote.Repo
		}
		if project == "" {
			project = remote.Project
		}
		if baseURL == "" && provider == "gitlab" && !strings.EqualFold(remote.Host, "gitlab.com") {
			baseURL = "https://" + remote.Host + "/"
		}
	}

	token, err := DiscoverToken(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", provider, err)
	}

	switch provider {
	case "github":
		if cfg.NewGitHub == nil {
			return nil, fmt.Errorf("github factory not registered")
		}
		return cfg.NewGitHub(token, owner, repo, baseURL)
	case "gitlab":
		if cfg.NewGitLab == nil {
			return nil, fmt.Errorf("gitlab factory not registered")
		}
		return cfg.NewGitLab(token, project, baseURL)
	}
	return nil, fmt.Errorf("unsupported provider %q", provider)
}
