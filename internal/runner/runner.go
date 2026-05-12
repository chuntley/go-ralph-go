// Package runner orchestrates the refine loop + cleanup pass + (optional) PR
// + auto-merge cycle that ralph performs for each task.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chuntley/go-ralph-go/internal/claude"
	"github.com/chuntley/go-ralph-go/internal/config"
	"github.com/chuntley/go-ralph-go/internal/git"
	"github.com/chuntley/go-ralph-go/internal/vcs"
	ghprov "github.com/chuntley/go-ralph-go/internal/vcs/github"
	glprov "github.com/chuntley/go-ralph-go/internal/vcs/gitlab"
)

// RunPrompt runs the refine loop on an ad-hoc prompt. With openPR=true, the
// cleanup pass pushes a branch and opens a PR/MR (no auto-merge).
func RunPrompt(ctx context.Context, cfg *config.Config, prompt string, openPR bool) error {
	r, err := newRun(cfg)
	if err != nil {
		return err
	}
	defer r.close()
	r.printStartupBanner(modeLabel(openPR))

	if openPR {
		if err := r.ensureProvider(ctx); err != nil {
			return fmt.Errorf("--pr requires a recognised git host: %w", err)
		}
	}

	workPrompt := prompt
	var cleanupPrompt string
	if openPR {
		cleanupPrompt = renderCleanup(cfg, 0)
	} else {
		cleanupPrompt = "The Ralph loop just exited. Stage and commit any outstanding changes with a clear, descriptive message. Do not push or open a PR."
	}

	if err := r.cycle(ctx, workPrompt, cleanupPrompt); err != nil {
		return err
	}
	if !openPR {
		r.log("Prompt mode complete; not opening a PR.")
		return nil
	}
	return r.handleCleanupPR(ctx, 0)
}

// RunIssue works a single issue end-to-end: loop, cleanup, PR, checks, merge.
func RunIssue(ctx context.Context, cfg *config.Config, issueNum int) error {
	r, err := newRun(cfg)
	if err != nil {
		return err
	}
	defer r.close()
	if err := r.ensureProvider(ctx); err != nil {
		return err
	}
	r.printStartupBanner(fmt.Sprintf("issue #%d on %s", issueNum, r.provider.Name()))
	if err := r.requireCleanTree(ctx); err != nil {
		return fmt.Errorf("issue mode requires a clean working tree (ralph will `git checkout %s && git pull` before working): %w", r.resolveDefaultBranch(ctx), err)
	}
	if err := r.provider.EnsureLabels(ctx, r.labels); err != nil {
		return fmt.Errorf("ensure labels: %w", err)
	}
	return r.processIssue(ctx, issueNum)
}

// RunAuto polls for the oldest ready issue and processes it; loops until
// canceled (or exits after one cycle if once=true).
func RunAuto(ctx context.Context, cfg *config.Config, once bool) error {
	r, err := newRun(cfg)
	if err != nil {
		return err
	}
	defer r.close()
	if err := r.ensureProvider(ctx); err != nil {
		return err
	}
	mode := "auto"
	if once {
		mode = "auto --once"
	}
	r.printStartupBanner(fmt.Sprintf("%s on %s", mode, r.provider.Name()))
	if err := r.requireCleanTree(ctx); err != nil {
		return fmt.Errorf("auto mode requires a clean working tree (ralph will `git checkout %s && git pull` before each issue): %w", r.resolveDefaultBranch(ctx), err)
	}
	if err := r.provider.EnsureLabels(ctx, r.labels); err != nil {
		return fmt.Errorf("ensure labels: %w", err)
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Per-iteration sanity check: between issues, a botched recovery (e.g.
		// failed `git checkout main` after a merge error) could leave the tree
		// dirty. Without this guard the loop would re-fail every subsequent
		// issue on the per-issue dirty-tree check; bail explicitly so the dev
		// sees what happened.
		if err := r.requireCleanTree(ctx); err != nil {
			return fmt.Errorf("auto loop halting: %w", err)
		}
		issue, err := r.provider.NextReady(ctx, r.labels)
		switch {
		case errors.Is(err, vcs.ErrNoReadyIssue):
			if once {
				r.log("No ready issues; --once specified, exiting.")
				return nil
			}
			r.log(fmt.Sprintf("No ready issues; sleeping %ds.", r.cfg.PollInterval))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(r.cfg.PollInterval) * time.Second):
			}
			continue
		case err != nil:
			return fmt.Errorf("next ready: %w", err)
		}

		r.section(fmt.Sprintf("Picking up issue #%d: %s  (completed=%d failed=%d)", issue.Number, issue.Title, r.completed, r.failed))
		if err := r.processIssue(ctx, issue.Number); err != nil {
			r.failed++
			r.log(fmt.Sprintf("Issue #%d did not complete cleanly: %v", issue.Number, err))
		} else {
			r.completed++
			r.log(fmt.Sprintf("Issue #%d merged. (completed=%d failed=%d)", issue.Number, r.completed, r.failed))
		}
		if once {
			return nil
		}
	}
}

// maxReasonLen caps the failure-reason string we post as a GitHub/GitLab
// comment so long stack traces don't dominate the issue thread.
const maxReasonLen = 500

// run is the per-invocation state.
type run struct {
	cfg      *config.Config
	session  *claude.Session
	provider vcs.Provider
	labels   vcs.Labels
	ui       io.Writer

	// Auto-mode counters; printed in the run banner between issues.
	completed int
	failed    int
}

func newRun(cfg *config.Config) (*run, error) {
	// Preflight: claude must be available before we set anything else up.
	if _, err := claude.LookupBin(cfg.ClaudeBin); err != nil {
		return nil, err
	}
	outDir := filepath.Join(cfg.ProjectRoot, cfg.OutputDir)
	// 0o700: see internal/claude/session.go ensureLogs — the run output dir
	// holds session transcripts that may contain credentials. Chmod after
	// MkdirAll because MkdirAll won't change the mode of an existing dir, and
	// older ralph versions created .ralph/ at 0o755.
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir output dir: %w", err)
	}
	if err := os.Chmod(outDir, 0o700); err != nil {
		return nil, fmt.Errorf("chmod output dir: %w", err)
	}
	sess, err := claude.NewSession(claude.SessionConfig{
		Bin:             cfg.ClaudeBin,
		WorkDir:         cfg.ProjectRoot,
		ClaudeConfigDir: cfg.ClaudeConfigDir,
		OutputDir:       outDir,
		UI:              os.Stdout,
	})
	if err != nil {
		return nil, err
	}
	return &run{
		cfg:     cfg,
		session: sess,
		labels: vcs.Labels{
			Ready:   cfg.GitHub.Labels.Ready,
			Working: cfg.GitHub.Labels.Working,
			Failed:  cfg.GitHub.Labels.Failed,
		},
		ui: os.Stdout,
	}, nil
}

func (r *run) close() {
	if r.session != nil {
		_ = r.session.Close()
	}
}

func (r *run) ensureProvider(ctx context.Context) error {
	if r.provider != nil {
		return nil
	}
	p, err := vcs.BuildProvider(ctx, vcs.FactoryConfig{
		ProjectDir: r.cfg.ProjectRoot,
		Owner:      r.cfg.GitHub.Owner,
		Repo:       r.cfg.GitHub.Repo,
		BaseURL:    r.cfg.GitHub.BaseURL,
		NewGitHub: func(token, owner, repo, baseURL string) (vcs.Provider, error) {
			return ghprov.New(token, owner, repo, baseURL)
		},
		NewGitLab: func(token, project, baseURL string) (vcs.Provider, error) {
			return glprov.New(token, project, baseURL)
		},
	})
	if err != nil {
		return err
	}
	r.provider = p
	return nil
}

// processIssue is the per-issue orchestration: sync main, mark working, refine
// loop, cleanup pass, locate PR, watch checks, merge.
//
// If the function exits with a non-nil error, the issue is marked failed and
// any "ralph-working" label is cleared. This includes ctx cancellation so a
// Ctrl-C mid-run doesn't leave an issue stuck in "working".
func (r *run) processIssue(ctx context.Context, issueNum int) (resultErr error) {
	defaultBranch := r.resolveDefaultBranch(ctx)

	if err := r.requireCleanTree(ctx); err != nil {
		return err
	}

	// Validate FIRST — fetch and confirm the issue is open and isn't actually a
	// PR. We do this before any label mutation or checkout so a closed-or-PR
	// number doesn't strand the issue with ralph-working.
	issue, err := r.provider.GetIssue(ctx, issueNum)
	if err != nil {
		return fmt.Errorf("fetch issue: %w", err)
	}

	if err := git.CheckoutMain(ctx, r.cfg.ProjectRoot, defaultBranch); err != nil {
		return fmt.Errorf("sync %s: %w (commit or stash any local changes first)", defaultBranch, err)
	}
	if err := r.provider.MarkWorking(ctx, issueNum, r.labels); err != nil {
		r.log(fmt.Sprintf("warn: mark working: %v", err))
	}

	// Catch-all cleanup: any non-nil exit clears the working label. On user
	// interrupt (Ctrl+C / SIGTERM → context cancellation) we *requeue* the
	// issue rather than marking it failed — the user almost certainly wants
	// the next ralph run to retry it, not have it sitting in the failed pile.
	defer func() { dispatchCleanup(r.provider, issueNum, r.labels, resultErr) }()

	r.section(fmt.Sprintf("Working %s issue #%d: %s", r.provider.Name(), issueNum, issue.Title))

	workPrompt := renderIssuePrompt(r.cfg, r.provider.Name(), issue)
	cleanupPrompt := renderCleanup(r.cfg, issueNum)

	if err := r.cycle(ctx, workPrompt, cleanupPrompt); err != nil {
		return err
	}
	return r.handleCleanupPR(ctx, issueNum)
}

// resolveDefaultBranch honours an explicit cfg.DefaultBranch first, then falls
// back to git auto-detection. Auto-detection covers main/master; explicit
// config is the escape hatch for develop/trunk/etc.
func (r *run) resolveDefaultBranch(ctx context.Context) string {
	if r.cfg.DefaultBranch != "" {
		return r.cfg.DefaultBranch
	}
	br, _ := git.DefaultBranch(ctx, r.cfg.ProjectRoot)
	return br
}

// truncate clips s so its byte length doesn't exceed n, appending a trailing
// marker when truncation occurred. The cut snaps backward to the last UTF-8
// rune boundary so the result is always valid UTF-8 — the truncated string is
// posted as a public issue/MR comment via MarkFailed, so a half-rune at the
// cut would be user-visible (best case: U+FFFD; worst case: API rejection).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const marker = " …[truncated]"
	if n <= len(marker) {
		return s[:lastRuneBoundary(s, n)]
	}
	return s[:lastRuneBoundary(s, n-len(marker))] + marker
}

// lastRuneBoundary returns the largest i ≤ pos such that s[:i] ends on a UTF-8
// rune boundary (i.e. s[i] is either past the end or a rune-start byte).
func lastRuneBoundary(s string, pos int) int {
	if pos >= len(s) {
		return len(s)
	}
	for pos > 0 && !utf8.RuneStart(s[pos]) {
		pos--
	}
	return pos
}

// requireCleanTree returns an actionable error if the working tree has
// uncommitted changes — prevents `git checkout main` from failing midway.
func (r *run) requireCleanTree(ctx context.Context) error {
	clean, err := git.IsClean(ctx, r.cfg.ProjectRoot)
	if err != nil {
		return err
	}
	if !clean {
		return errors.New("working tree has uncommitted changes — commit or stash them before ralph runs")
	}
	return nil
}

// cycle runs the configured number of refine iterations + a cleanup pass.
func (r *run) cycle(ctx context.Context, workPrompt, cleanupPrompt string) error {
	if err := r.session.Reset(); err != nil {
		return fmt.Errorf("reset logs: %w", err)
	}
	for i := 1; i <= r.cfg.Iterations; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		r.section(fmt.Sprintf("Iteration %d / %d", i, r.cfg.Iterations))
		refine := renderRefinePrompt(r.cfg, i, r.cfg.Iterations)
		prompt := workPrompt + "\n\n" + refine
		if err := r.session.Run(ctx, prompt); err != nil {
			return fmt.Errorf("iteration %d: %w", i, err)
		}
	}
	r.section("Cleanup pass")
	if err := r.session.Run(ctx, cleanupPrompt); err != nil {
		return fmt.Errorf("cleanup pass: %w", err)
	}
	return nil
}

// renderRefinePrompt substitutes {{iter}} and {{total}} into the refine
// template. Single-pass via strings.NewReplacer for placeholder safety
// (matches renderIssuePrompt / renderCleanup).
func renderRefinePrompt(cfg *config.Config, iter, total int) string {
	r := strings.NewReplacer(
		"{{iter}}", fmt.Sprintf("%d", iter),
		"{{total}}", fmt.Sprintf("%d", total),
	)
	return r.Replace(cfg.RefinePrompt)
}

// handleCleanupPR locates the branch Claude pushed, finds the open PR/MR for
// it, waits for checks, and squash-merges. For ad-hoc prompts (issueNum=0)
// only the PR existence check runs.
func (r *run) handleCleanupPR(ctx context.Context, issueNum int) error {
	branch, err := git.CurrentBranch(ctx, r.cfg.ProjectRoot)
	if err != nil {
		return fmt.Errorf("detect branch: %w", err)
	}
	defaultBranch := r.resolveDefaultBranch(ctx)
	if branch == defaultBranch {
		return fmt.Errorf("cleanup pass left us on %s — Claude did not create a feature branch", defaultBranch)
	}

	if issueNum == 0 {
		// Ad-hoc --pr mode: PR opened but NOT auto-merged.
		pr, err := r.provider.FindPRForBranch(ctx, branch)
		if err != nil {
			return fmt.Errorf("find PR: %w", err)
		}
		if pr == nil {
			return fmt.Errorf("no open PR found for branch %s — Claude may have committed but not pushed; check `git log` and re-run with --pr or push manually", branch)
		}
		r.log(fmt.Sprintf("Opened PR #%d on branch %s (no auto-merge).", pr.Number, branch))
		if pr.URL != "" {
			r.log("  → " + pr.URL)
		}
		_ = git.CheckoutMain(ctx, r.cfg.ProjectRoot, defaultBranch)
		return nil
	}

	pr, err := r.provider.FindPRForBranch(ctx, branch)
	if err != nil {
		return fmt.Errorf("find PR: %w", err)
	}
	if pr == nil {
		return fmt.Errorf("no open PR for branch %s — refine loop did not push a PR", branch)
	}

	if pr.URL != "" {
		r.log(fmt.Sprintf("Found PR #%d  →  %s", pr.Number, pr.URL))
	}
	r.log(fmt.Sprintf("Waiting for checks on PR #%d...", pr.Number))
	interval := time.Duration(r.cfg.GitHub.CheckIntervalSeconds) * time.Second
	if err := r.provider.WaitForChecks(ctx, pr.Number, interval); err != nil {
		_ = git.CheckoutMain(ctx, r.cfg.ProjectRoot, defaultBranch)
		return fmt.Errorf("checks failed on PR #%d: %w", pr.Number, err)
	}

	r.log(fmt.Sprintf("Checks passed; squash-merging PR #%d...", pr.Number))
	if err := r.provider.SquashMergeAndDelete(ctx, pr.Number); err != nil {
		_ = git.CheckoutMain(ctx, r.cfg.ProjectRoot, defaultBranch)
		return fmt.Errorf("merge refused on PR #%d: %w", pr.Number, err)
	}
	// Defensive close: if Claude's PR body lacked "Closes #N", GitHub won't
	// auto-close the issue on merge and ralph-working would linger forever.
	// Best-effort — failure here doesn't undo the merge.
	if err := r.provider.MarkResolved(ctx, issueNum, r.labels); err != nil {
		r.log(fmt.Sprintf("warn: mark resolved on issue #%d: %v", issueNum, err))
	}
	_ = git.CheckoutMain(ctx, r.cfg.ProjectRoot, defaultBranch)
	return nil
}

// renderCleanup expands the cleanup template, filling {{instructions_doc}},
// {{issue_clause}}, and {{closes_clause}}. Single-pass for placeholder safety.
func renderCleanup(cfg *config.Config, issueNum int) string {
	issueClause := ""
	closesClause := ""
	if issueNum > 0 {
		issueClause = fmt.Sprintf(" working on issue #%d", issueNum)
		closesClause = fmt.Sprintf(" Include \"Closes #%d\" in the PR body so the issue auto-closes when the PR merges.", issueNum)
	}
	r := strings.NewReplacer(
		"{{instructions_doc}}", cfg.InstructionsDoc,
		"{{issue_clause}}", issueClause,
		"{{closes_clause}}", closesClause,
	)
	return r.Replace(cfg.CleanupPrompt)
}

// renderIssuePrompt expands cfg.IssuePrompt for the given issue, filling
// {{provider}}, {{number}}, {{title}}, {{body}}.
//
// Uses strings.NewReplacer for a single-pass replacement so user content (e.g.
// a body containing literal "{{title}}" text) is never re-substituted into
// later placeholders.
func renderIssuePrompt(cfg *config.Config, providerName string, issue *vcs.Issue) string {
	r := strings.NewReplacer(
		"{{provider}}", providerName,
		"{{number}}", fmt.Sprintf("%d", issue.Number),
		"{{title}}", issue.Title,
		"{{body}}", issue.Body,
	)
	return r.Replace(cfg.IssuePrompt)
}

func modeLabel(openPR bool) string {
	if openPR {
		return "run --pr (open PR, no auto-merge)"
	}
	return "run (local only)"
}

// printStartupBanner prints a one-time summary of the effective configuration
// so the developer can confirm ralph picked up the right project + settings.
func (r *run) printStartupBanner(mode string) {
	cfgSrc := r.cfg.ConfigPath
	if cfgSrc == "" {
		cfgSrc = "(defaults; no .go-ralph-go found)"
	}
	claudeCfg := r.cfg.ClaudeConfigDir
	if claudeCfg == "" {
		claudeCfg = "(system default)"
	}
	fmt.Fprintln(r.ui)
	fmt.Fprintln(r.ui, "ralph")
	fmt.Fprintf(r.ui, "  mode         : %s\n", mode)
	fmt.Fprintf(r.ui, "  project root : %s\n", r.cfg.ProjectRoot)
	fmt.Fprintf(r.ui, "  config       : %s\n", cfgSrc)
	fmt.Fprintf(r.ui, "  claude config: %s\n", claudeCfg)
	fmt.Fprintf(r.ui, "  iterations   : %d\n", r.cfg.Iterations)
	fmt.Fprintf(r.ui, "  output dir   : %s\n", filepath.Join(r.cfg.ProjectRoot, r.cfg.OutputDir))
	fmt.Fprintln(r.ui)
}

func (r *run) log(msg string) {
	// Redact before writing: several call sites format wrapped errors (`%v`)
	// whose chain can include git stderr or remote URLs carrying credentials.
	// dispatchCleanup redacts at the MarkFailed boundary; this redacts at the
	// stdout / pretty-log boundary so credentials don't survive in terminal
	// scrollback or CI capture either.
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), redactSecrets(msg))
	fmt.Fprint(r.ui, line)
	_ = r.session.WriteBanner(line)
}

func (r *run) section(title string) {
	banner := "\n==========================================================\n" +
		fmt.Sprintf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), title) +
		"==========================================================\n"
	fmt.Fprint(r.ui, banner)
	_ = r.session.WriteBanner(banner)
}
