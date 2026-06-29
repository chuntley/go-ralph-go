// Package github implements vcs.Provider against the GitHub REST API.
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chuntley/go-ralph-go/internal/vcs"
	gh "github.com/google/go-github/v66/github"
)

// Provider talks to GitHub.com or GHES via go-github.
type Provider struct {
	client *gh.Client
	owner  string
	repo   string
}

// New builds a Provider authenticated with the given token. baseURL may be
// empty (github.com) or a GitHub Enterprise API root like
// "https://ghe.example.com/api/v3/".
func New(token, owner, repo, baseURL string) (*Provider, error) {
	if token == "" {
		return nil, errors.New("github: empty token")
	}
	if owner == "" || repo == "" {
		return nil, errors.New("github: owner and repo required")
	}
	c := gh.NewClient(nil).WithAuthToken(token)
	if baseURL != "" {
		var err error
		c, err = c.WithEnterpriseURLs(baseURL, baseURL)
		if err != nil {
			return nil, fmt.Errorf("github enterprise base url: %w", err)
		}
	}
	return &Provider{client: c, owner: owner, repo: repo}, nil
}

func (p *Provider) Name() string { return "github" }

func (p *Provider) Whoami(ctx context.Context) (string, error) {
	u, _, err := p.client.Users.Get(ctx, "")
	if err != nil {
		return "", fmt.Errorf("github /user: %w", err)
	}
	return u.GetLogin(), nil
}

func (p *Provider) MarkRequeued(ctx context.Context, number int, l vcs.Labels) error {
	_ = p.removeLabel(ctx, number, l.Working)
	_ = p.removeLabel(ctx, number, l.Failed)
	return p.addLabel(ctx, number, l.Ready)
}

func (p *Provider) MarkResolved(ctx context.Context, number int, l vcs.Labels) error {
	if err := p.removeLabel(ctx, number, l.Working); err != nil {
		return err
	}
	state := "closed"
	_, _, err := p.client.Issues.Edit(ctx, p.owner, p.repo, number, &gh.IssueRequest{State: &state})
	return err
}

func (p *Provider) MarkSkipped(ctx context.Context, number int, l vcs.Labels, note string) error {
	_ = p.removeLabel(ctx, number, l.Working)
	_ = p.removeLabel(ctx, number, l.Ready)
	if _, _, err := p.client.Issues.CreateComment(ctx, p.owner, p.repo, number, &gh.IssueComment{Body: &note}); err != nil {
		return err
	}
	state := "closed"
	_, _, err := p.client.Issues.Edit(ctx, p.owner, p.repo, number, &gh.IssueRequest{State: &state})
	return err
}

// labelDef carries the canonical color/description for a label slot.
type labelDef struct {
	color string
	desc  string
}

var labelDefs = map[string]labelDef{
	"ready":   {"0e8a16", "Ready for ralph to pick up"},
	"working": {"fbca04", "Ralph is currently working this issue"},
	"failed":  {"d73a4a", "Ralph attempted and failed; needs human triage"},
}

func (p *Provider) EnsureLabels(ctx context.Context, l vcs.Labels) error {
	want := map[string]labelDef{
		l.Ready:   labelDefs["ready"],
		l.Working: labelDefs["working"],
		l.Failed:  labelDefs["failed"],
	}
	// Pull existing labels (paginate).
	have := map[string]bool{}
	opts := &gh.ListOptions{PerPage: 100}
	for {
		page, resp, err := p.client.Issues.ListLabels(ctx, p.owner, p.repo, opts)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotFound {
				return fmt.Errorf("github: repo %s/%s not found (404) — check the origin remote, or that your token has access to this repo", p.owner, p.repo)
			}
			return fmt.Errorf("list labels: %w", err)
		}
		for _, lab := range page {
			have[lab.GetName()] = true
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	for name, def := range want {
		if have[name] {
			continue
		}
		lab := &gh.Label{Name: &name, Color: &def.color, Description: &def.desc}
		if _, _, err := p.client.Issues.CreateLabel(ctx, p.owner, p.repo, lab); err != nil {
			return fmt.Errorf("create label %s: %w", name, err)
		}
	}
	return nil
}

func (p *Provider) NextReady(ctx context.Context, l vcs.Labels) (*vcs.Issue, error) {
	// GitHub's list-issues endpoint returns issues AND pull requests in one
	// stream, and PRs can carry the same labels. Fetching just one item and
	// filtering PRs out client-side meant a single ready-labelled PR at the
	// head of the backlog would cause auto mode to falsely report "no ready
	// issue" — paginate until we find a real issue or run out of pages.
	opts := &gh.IssueListByRepoOptions{
		Labels:      []string{l.Ready},
		State:       "open",
		Sort:        "created",
		Direction:   "asc",
		ListOptions: gh.ListOptions{PerPage: 30},
	}
	for {
		issues, resp, err := p.client.Issues.ListByRepo(ctx, p.owner, p.repo, opts)
		if err != nil {
			return nil, fmt.Errorf("list issues: %w", err)
		}
		for _, i := range issues {
			if i.IsPullRequest() {
				continue
			}
			return &vcs.Issue{Number: i.GetNumber(), Title: i.GetTitle(), Body: i.GetBody()}, nil
		}
		if resp == nil || resp.NextPage == 0 {
			return nil, vcs.ErrNoReadyIssue
		}
		opts.Page = resp.NextPage
	}
}

func (p *Provider) GetIssue(ctx context.Context, number int) (*vcs.Issue, error) {
	i, _, err := p.client.Issues.Get(ctx, p.owner, p.repo, number)
	if err != nil {
		return nil, fmt.Errorf("get issue %d: %w", number, err)
	}
	// GitHub PR numbers share the issue sequence; `Issues.Get(pr_number)` returns
	// the PR as an issue-shaped object. Reject so `ralph issue <N>` doesn't try
	// to work a PR.
	if i.IsPullRequest() {
		return nil, fmt.Errorf("#%d is a pull request, not an issue", number)
	}
	if state := i.GetState(); state != "open" {
		return nil, fmt.Errorf("issue #%d is %s — ralph only works open issues", number, state)
	}
	return &vcs.Issue{Number: i.GetNumber(), Title: i.GetTitle(), Body: i.GetBody()}, nil
}

func (p *Provider) MarkWorking(ctx context.Context, number int, l vcs.Labels) error {
	if err := p.removeLabel(ctx, number, l.Ready); err != nil {
		return err
	}
	return p.addLabel(ctx, number, l.Working)
}

func (p *Provider) MarkFailed(ctx context.Context, number int, l vcs.Labels, reason string) error {
	_ = p.removeLabel(ctx, number, l.Working)
	_ = p.removeLabel(ctx, number, l.Ready)
	if err := p.addLabel(ctx, number, l.Failed); err != nil {
		return err
	}
	body := fmt.Sprintf("🤖 ralph exited without merging: %s\n\nSee `.ralph/output.txt` on the runner for full logs.", reason)
	_, _, err := p.client.Issues.CreateComment(ctx, p.owner, p.repo, number, &gh.IssueComment{Body: &body})
	return err
}

func (p *Provider) addLabel(ctx context.Context, n int, name string) error {
	_, _, err := p.client.Issues.AddLabelsToIssue(ctx, p.owner, p.repo, n, []string{name})
	return err
}

func (p *Provider) removeLabel(ctx context.Context, n int, name string) error {
	resp, err := p.client.Issues.RemoveLabelForIssue(ctx, p.owner, p.repo, n, name)
	if err != nil && resp != nil && resp.StatusCode == http.StatusNotFound {
		return nil // label wasn't present; treat as success
	}
	return err
}

func (p *Provider) FindPRForBranch(ctx context.Context, branch string) (*vcs.PR, error) {
	head := fmt.Sprintf("%s:%s", p.owner, branch)
	prs, _, err := p.client.PullRequests.List(ctx, p.owner, p.repo, &gh.PullRequestListOptions{
		State:       "open",
		Head:        head,
		ListOptions: gh.ListOptions{PerPage: 1},
	})
	if err != nil {
		return nil, fmt.Errorf("list PRs: %w", err)
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &vcs.PR{Number: prs[0].GetNumber(), Branch: branch, URL: prs[0].GetHTMLURL()}, nil
}

func (p *Provider) CreatePR(ctx context.Context, headBranch, baseBranch, title, body string) (*vcs.PR, error) {
	npr := &gh.NewPullRequest{
		Title: &title,
		Head:  &headBranch,
		Base:  &baseBranch,
		Body:  &body,
	}
	pr, _, err := p.client.PullRequests.Create(ctx, p.owner, p.repo, npr)
	if err != nil {
		return nil, fmt.Errorf("create PR: %w", err)
	}
	return &vcs.PR{Number: pr.GetNumber(), Branch: headBranch, URL: pr.GetHTMLURL()}, nil
}

// minConfirmZeroChecks is the number of consecutive polls returning zero
// checks before WaitForChecks concludes "this repo has no CI configured".
// Without this, a freshly-pushed PR could see zero checks before GitHub
// Actions has registered them, and ralph would merge prematurely.
const minConfirmZeroChecks = 2

func (p *Provider) WaitForChecks(ctx context.Context, prNumber int, interval time.Duration) error {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	pr, _, err := p.client.PullRequests.Get(ctx, p.owner, p.repo, prNumber)
	if err != nil {
		return fmt.Errorf("get PR: %w", err)
	}
	sha := pr.GetHead().GetSHA()
	if sha == "" {
		return errors.New("PR head sha missing")
	}

	zeroPolls := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		status, _, err := p.client.Repositories.GetCombinedStatus(ctx, p.owner, p.repo, sha, &gh.ListOptions{PerPage: 1})
		if err != nil {
			return fmt.Errorf("combined status: %w", err)
		}
		checks, _, err := p.client.Checks.ListCheckRunsForRef(ctx, p.owner, p.repo, sha, &gh.ListCheckRunsOptions{ListOptions: gh.ListOptions{PerPage: 100}})
		if err != nil {
			return fmt.Errorf("check runs: %w", err)
		}

		state, msg := combineStates(status, checks)
		if state == "success" && !anyChecksExist(status, checks) {
			// No checks observed yet. Wait at least minConfirmZeroChecks polls
			// to give GitHub Actions time to register; if still empty, treat
			// as "no CI configured" and proceed.
			zeroPolls++
			if zeroPolls < minConfirmZeroChecks {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(interval):
				}
				continue
			}
			return nil
		}
		zeroPolls = 0
		switch state {
		case "success":
			return nil
		case "failure":
			return fmt.Errorf("checks failed: %s", msg)
		case "pending":
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			}
		}
	}
}

// anyChecksExist reports whether GitHub has any check signal at all for this
// commit. Used to distinguish "no CI configured" from "CI passed".
func anyChecksExist(status *gh.CombinedStatus, checks *gh.ListCheckRunsResults) bool {
	if status != nil && status.GetTotalCount() > 0 {
		return true
	}
	if checks != nil && len(checks.CheckRuns) > 0 {
		return true
	}
	return false
}

// combineStates folds the combined-status + check-runs lists into a single
// {success, failure, pending} state.
func combineStates(status *gh.CombinedStatus, checks *gh.ListCheckRunsResults) (string, string) {
	pending := false

	// Combined status (legacy statuses API).
	if status != nil && status.GetTotalCount() > 0 {
		switch strings.ToLower(status.GetState()) {
		case "failure", "error":
			return "failure", "combined status: " + status.GetState()
		case "pending":
			pending = true
		}
	}
	// Check runs (modern checks API).
	if checks != nil {
		for _, cr := range checks.CheckRuns {
			switch cr.GetStatus() {
			case "completed":
				switch cr.GetConclusion() {
				case "success", "skipped", "neutral":
					// ok
				default:
					return "failure", fmt.Sprintf("check %q conclusion=%s", cr.GetName(), cr.GetConclusion())
				}
			default:
				pending = true
			}
		}
	}
	if pending {
		return "pending", ""
	}
	return "success", ""
}

func (p *Provider) SquashMergeAndDelete(ctx context.Context, prNumber int) error {
	opts := &gh.PullRequestOptions{MergeMethod: "squash"}
	res, _, err := p.client.PullRequests.Merge(ctx, p.owner, p.repo, prNumber, "", opts)
	if err != nil {
		return fmt.Errorf("merge: %w", err)
	}
	if !res.GetMerged() {
		return fmt.Errorf("merge refused: %s", res.GetMessage())
	}

	// Find branch name from the PR to delete it.
	pr, _, err := p.client.PullRequests.Get(ctx, p.owner, p.repo, prNumber)
	if err != nil {
		return nil // merged ok, branch deletion is best-effort
	}
	branch := pr.GetHead().GetRef()
	if branch == "" {
		return nil
	}
	if _, err := p.client.Git.DeleteRef(ctx, p.owner, p.repo, "heads/"+branch); err != nil {
		// non-fatal; branch may already be gone
		return nil
	}
	return nil
}
