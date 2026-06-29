// Package gitlab implements vcs.Provider against the GitLab REST API.
package gitlab

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chuntley/go-ralph-go/internal/vcs"
	gl "gitlab.com/gitlab-org/api/client-go"
)

// Provider talks to gitlab.com or self-hosted GitLab via the official SDK.
type Provider struct {
	client  *gl.Client
	project string // full path with namespace, e.g. "group/subgroup/repo"
}

// New builds a Provider authenticated with the given token. baseURL may be
// empty for gitlab.com; pass "https://gitlab.example.com/" for self-hosted.
func New(token, project, baseURL string) (*Provider, error) {
	if token == "" {
		return nil, errors.New("gitlab: empty token")
	}
	if project == "" {
		return nil, errors.New("gitlab: project required")
	}
	var opts []gl.ClientOptionFunc
	if baseURL != "" {
		opts = append(opts, gl.WithBaseURL(baseURL))
	}
	c, err := gl.NewClient(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("gitlab client: %w", err)
	}
	return &Provider{client: c, project: project}, nil
}

func (p *Provider) Name() string { return "gitlab" }

func (p *Provider) Whoami(ctx context.Context) (string, error) {
	u, _, err := p.client.Users.CurrentUser(gl.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("gitlab /user: %w", err)
	}
	return u.Username, nil
}

func (p *Provider) MarkRequeued(ctx context.Context, number int, l vcs.Labels) error {
	remove := gl.LabelOptions{l.Working, l.Failed}
	add := gl.LabelOptions{l.Ready}
	_, _, err := p.client.Issues.UpdateIssue(p.project, int64(number), &gl.UpdateIssueOptions{
		RemoveLabels: &remove,
		AddLabels:    &add,
	}, gl.WithContext(ctx))
	return err
}

func (p *Provider) MarkResolved(ctx context.Context, number int, l vcs.Labels) error {
	remove := gl.LabelOptions{l.Working}
	closeEvent := "close"
	_, _, err := p.client.Issues.UpdateIssue(p.project, int64(number), &gl.UpdateIssueOptions{
		RemoveLabels: &remove,
		StateEvent:   &closeEvent,
	}, gl.WithContext(ctx))
	return err
}

func (p *Provider) MarkSkipped(ctx context.Context, number int, l vcs.Labels, note string) error {
	remove := gl.LabelOptions{l.Ready, l.Working}
	closeEvent := "close"
	if _, _, err := p.client.Issues.UpdateIssue(p.project, int64(number), &gl.UpdateIssueOptions{
		RemoveLabels: &remove,
		StateEvent:   &closeEvent,
	}, gl.WithContext(ctx)); err != nil {
		return err
	}
	_, _, err := p.client.Notes.CreateIssueNote(p.project, int64(number), &gl.CreateIssueNoteOptions{Body: &note}, gl.WithContext(ctx))
	return err
}

var labelColors = map[string]string{
	"ready":   "#0e8a16",
	"working": "#fbca04",
	"failed":  "#d73a4a",
}

func (p *Provider) EnsureLabels(ctx context.Context, l vcs.Labels) error {
	want := map[string]string{
		l.Ready:   labelColors["ready"],
		l.Working: labelColors["working"],
		l.Failed:  labelColors["failed"],
	}
	have := map[string]bool{}
	opts := &gl.ListLabelsOptions{ListOptions: gl.ListOptions{PerPage: 100}}
	for {
		page, resp, err := p.client.Labels.ListLabels(p.project, opts, gl.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("list labels: %w", err)
		}
		for _, lab := range page {
			have[lab.Name] = true
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	for name, color := range want {
		if have[name] {
			continue
		}
		if _, _, err := p.client.Labels.CreateLabel(p.project, &gl.CreateLabelOptions{
			Name:  &name,
			Color: &color,
		}, gl.WithContext(ctx)); err != nil {
			return fmt.Errorf("create label %s: %w", name, err)
		}
	}
	return nil
}

func (p *Provider) NextReady(ctx context.Context, l vcs.Labels) (*vcs.Issue, error) {
	state := "opened"
	order := "created_at"
	sort := "asc"
	opts := &gl.ListProjectIssuesOptions{
		State:       &state,
		Labels:      &gl.LabelOptions{l.Ready},
		OrderBy:     &order,
		Sort:        &sort,
		ListOptions: gl.ListOptions{PerPage: 1},
	}
	issues, _, err := p.client.Issues.ListProjectIssues(p.project, opts, gl.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	if len(issues) == 0 {
		return nil, vcs.ErrNoReadyIssue
	}
	i := issues[0]
	return &vcs.Issue{Number: int(i.IID), Title: i.Title, Body: i.Description}, nil
}

func (p *Provider) GetIssue(ctx context.Context, number int) (*vcs.Issue, error) {
	i, _, err := p.client.Issues.GetIssue(p.project, int64(number), gl.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("get issue %d: %w", number, err)
	}
	if i.State != "opened" {
		return nil, fmt.Errorf("issue #%d is %s — ralph only works open issues", number, i.State)
	}
	return &vcs.Issue{Number: int(i.IID), Title: i.Title, Body: i.Description}, nil
}

func (p *Provider) MarkWorking(ctx context.Context, number int, l vcs.Labels) error {
	remove := gl.LabelOptions{l.Ready}
	add := gl.LabelOptions{l.Working}
	_, _, err := p.client.Issues.UpdateIssue(p.project, int64(number), &gl.UpdateIssueOptions{
		RemoveLabels: &remove,
		AddLabels:    &add,
	}, gl.WithContext(ctx))
	return err
}

func (p *Provider) MarkFailed(ctx context.Context, number int, l vcs.Labels, reason string) error {
	remove := gl.LabelOptions{l.Ready, l.Working}
	add := gl.LabelOptions{l.Failed}
	if _, _, err := p.client.Issues.UpdateIssue(p.project, int64(number), &gl.UpdateIssueOptions{
		RemoveLabels: &remove,
		AddLabels:    &add,
	}, gl.WithContext(ctx)); err != nil {
		return err
	}
	body := fmt.Sprintf("🤖 ralph exited without merging: %s\n\nSee `.ralph/output.txt` on the runner for full logs.", reason)
	_, _, err := p.client.Notes.CreateIssueNote(p.project, int64(number), &gl.CreateIssueNoteOptions{Body: &body}, gl.WithContext(ctx))
	return err
}

func (p *Provider) FindPRForBranch(ctx context.Context, branch string) (*vcs.PR, error) {
	state := "opened"
	mrs, _, err := p.client.MergeRequests.ListProjectMergeRequests(p.project, &gl.ListProjectMergeRequestsOptions{
		State:        &state,
		SourceBranch: &branch,
		ListOptions:  gl.ListOptions{PerPage: 1},
	}, gl.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("list MRs: %w", err)
	}
	if len(mrs) == 0 {
		return nil, nil
	}
	return &vcs.PR{Number: int(mrs[0].IID), Branch: branch, URL: mrs[0].WebURL}, nil
}

func (p *Provider) CreatePR(ctx context.Context, headBranch, baseBranch, title, body string) (*vcs.PR, error) {
	mr, _, err := p.client.MergeRequests.CreateMergeRequest(p.project, &gl.CreateMergeRequestOptions{
		SourceBranch: &headBranch,
		TargetBranch: &baseBranch,
		Title:        &title,
		Description:  &body,
	}, gl.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("create MR: %w", err)
	}
	return &vcs.PR{Number: int(mr.IID), Branch: headBranch, URL: mr.WebURL}, nil
}

// minConfirmZeroPipelines is the number of consecutive polls returning a nil
// HeadPipeline before WaitForChecks concludes "this repo has no CI". Without
// this, GitLab's pipeline-registration delay could cause premature merges.
const minConfirmZeroPipelines = 2

func (p *Provider) WaitForChecks(ctx context.Context, prNumber int, interval time.Duration) error {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	zeroPolls := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		mr, _, err := p.client.MergeRequests.GetMergeRequest(p.project, int64(prNumber), nil, gl.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("get MR: %w", err)
		}
		if mr.HeadPipeline == nil {
			zeroPolls++
			if zeroPolls < minConfirmZeroPipelines {
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
		switch strings.ToLower(mr.HeadPipeline.Status) {
		case "success", "skipped":
			return nil
		case "failed", "canceled":
			return fmt.Errorf("pipeline %s", mr.HeadPipeline.Status)
		case "manual":
			// Pipeline is awaiting a required manual job (e.g. security scan,
			// gated test). Auto-merging here would skip a CI signal the project
			// explicitly required, so bail loudly rather than treating it as
			// success.
			return errors.New("pipeline status=manual: required manual jobs have not been run — trigger them in GitLab, or remove the manual gate, before re-running ralph")
		default:
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			}
		}
	}
}

func (p *Provider) SquashMergeAndDelete(ctx context.Context, prNumber int) error {
	squash := true
	delBr := true
	_, _, err := p.client.MergeRequests.AcceptMergeRequest(p.project, int64(prNumber), &gl.AcceptMergeRequestOptions{
		Squash:                   &squash,
		ShouldRemoveSourceBranch: &delBr,
	}, gl.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("merge MR: %w", err)
	}
	return nil
}
