// Package runner orchestrates the refine loop + cleanup pass + (optional) PR
// + auto-merge cycle that ralph performs for each task.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

	// Ad-hoc run/run --pr always works in-place in the repo root (no per-issue
	// worktree): the user is driving an interactive prompt against their current
	// checkout, on their chosen base.
	ws := r.rootWorkspace()

	workPrompt := prompt
	var cleanupPrompt string
	if openPR {
		// Same guardrail as issue mode: open the PR from a dedicated branch, never
		// the default branch. Branches off the current HEAD (run mode does not sync
		// the default branch), so the user's chosen base is preserved.
		defaultBranch := r.resolveDefaultBranch(ctx)
		branch := runBranchName(cfg.BranchPrefix, time.Now().Format("20060102-150405"))
		if err := git.CreateWorkBranch(ctx, cfg.ProjectRoot, branch); err != nil {
			return fmt.Errorf("create work branch: %w", err)
		}
		r.log(fmt.Sprintf("On branch %s; commits stay off %s.", branch, defaultBranch))
		workPrompt = branchContextNote(branch, defaultBranch) + "\n\n" + prompt
		cleanupPrompt = renderCleanup(cfg, 0)
	} else {
		cleanupPrompt = "The Ralph loop just exited. Stage and commit any outstanding changes with a clear, descriptive message. Do not push or open a PR."
	}

	if err := r.cycle(ctx, ws, workPrompt, cleanupPrompt); err != nil {
		if errors.Is(err, claude.ErrNotLoggedIn) {
			return r.authFailureError(err)
		}
		return err
	}
	if !openPR {
		r.log("Prompt mode complete; not opening a PR.")
		return nil
	}
	return r.handleCleanupPRWS(ctx, ws, 0, "")
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
	// In worktree mode the repo root is never touched, so a dirty root is fine —
	// that's the whole point (work in the root while ralph runs). Only the
	// in-place path needs a clean tree for its `git checkout`.
	if !r.cfg.Worktrees {
		if err := r.requireCleanTree(ctx); err != nil {
			return fmt.Errorf("in-place issue mode requires a clean working tree (ralph will `git checkout %s && git pull` before working; or drop --no-worktree): %w", r.resolveDefaultBranch(ctx), err)
		}
	}
	if err := r.provider.EnsureLabels(ctx, r.labels); err != nil {
		return fmt.Errorf("ensure labels: %w", err)
	}
	if err := r.processIssue(ctx, issueNum); err != nil {
		if errors.Is(err, claude.ErrNotLoggedIn) {
			return r.authFailureError(err)
		}
		return err
	}
	return nil
}

// RunAuto polls for the oldest ready issue and processes it; loops until
// canceled (or exits after one cycle if once=true). It also halts with an exit
// reason the moment an issue is marked failed (the ralph-failed label) so a
// broken run surfaces to a human instead of the worker quietly moving on.
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
	if r.cfg.MaxParallel > 1 {
		mode = fmt.Sprintf("%s ×%d on %s", mode, r.cfg.MaxParallel, r.provider.Name())
	} else {
		mode = fmt.Sprintf("%s on %s", mode, r.provider.Name())
	}
	r.printStartupBanner(mode)
	// Worktree mode never touches the repo root, so the root tree can be dirty
	// (the developer may be working in it). Only the in-place path needs it clean.
	if !r.cfg.Worktrees {
		if err := r.requireCleanTree(ctx); err != nil {
			return fmt.Errorf("in-place auto mode requires a clean working tree (ralph will `git checkout %s && git pull` before each issue; or drop --no-worktree): %w", r.resolveDefaultBranch(ctx), err)
		}
	}
	if err := r.provider.EnsureLabels(ctx, r.labels); err != nil {
		return fmt.Errorf("ensure labels: %w", err)
	}

	if r.cfg.MaxParallel > 1 {
		return r.runAutoParallel(ctx, once)
	}
	return r.runAutoSequential(ctx, once)
}

// runAutoSequential is the original one-issue-at-a-time loop.
func (r *run) runAutoSequential(ctx context.Context, once bool) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Per-iteration sanity check (in-place mode only): between issues, a
		// botched recovery (e.g. a failed `git checkout main`) could leave the
		// tree dirty and re-fail every subsequent issue; bail explicitly. In
		// worktree mode the root is never touched, so there's nothing to guard.
		if !r.cfg.Worktrees {
			if err := r.requireCleanTree(ctx); err != nil {
				return fmt.Errorf("auto loop halting: %w", err)
			}
		}
		issue, err := r.provider.NextReady(ctx, r.labels)
		switch {
		case errors.Is(err, vcs.ErrNoReadyIssue):
			if once {
				r.log("No ready issues; --once specified, exiting.")
				return nil
			}
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
		err = r.processIssue(ctx, issue.Number)
		switch {
		case errors.Is(err, claude.ErrNotLoggedIn):
			// Environment problem, not an issue problem: the issue was requeued.
			// Halt with the fix rather than spinning and failing every subsequent
			// issue identically until a human notices.
			return r.authFailureError(err)
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			// Interrupted (Ctrl+C / timeout): the issue was requeued, not failed.
			// Exit cleanly via the normal cancellation path.
			return err
		case errors.Is(err, errIssueGone):
			// Not actionable (closed/merged/PR) — a benign skip, neither done nor
			// failed. It was requeued (working label cleared) by dispatchCleanup.
			r.log(fmt.Sprintf("Issue #%d skipped — not actionable: %v", issue.Number, err))
		case err != nil:
			// A genuine failure: dispatchCleanup already labelled it ralph-failed.
			// Keep working the rest of the queue rather than halting the worker.
			r.failed++
			r.log(fmt.Sprintf("Issue #%d failed (continuing): %v  (completed=%d failed=%d)", issue.Number, err, r.completed, r.failed))
		default:
			r.completed++
			r.log(fmt.Sprintf("Issue #%d done. (completed=%d failed=%d)", issue.Number, r.completed, r.failed))
		}
		if once {
			return nil
		}
	}
}

// runAutoParallel works up to cfg.MaxParallel issues concurrently, each in its
// own worktree, rendering a compact live status dashboard. It keeps polling and
// dispatching as slots free up; on the first issue that ends up labelled failed
// it stops accepting new work and drains the in-flight ones before returning.
func (r *run) runAutoParallel(ctx context.Context, once bool) error {
	dash := newDashboard(r.ui)
	dash.start()
	defer dash.stopRender()

	sem := make(chan struct{}, r.cfg.MaxParallel)
	inFlight := map[int]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var haltErr error

	halted := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return haltErr != nil
	}

	for ctx.Err() == nil && !halted() {
		// Acquire a slot (blocks until capacity frees up).
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			goto drain
		}

		issue, err := r.provider.NextReady(ctx, r.labels)
		switch {
		case errors.Is(err, vcs.ErrNoReadyIssue):
			<-sem
			if once {
				goto drain
			}
			select {
			case <-ctx.Done():
				goto drain
			case <-time.After(time.Duration(r.cfg.PollInterval) * time.Second):
			}
			continue
		case err != nil:
			<-sem
			mu.Lock()
			if haltErr == nil {
				haltErr = fmt.Errorf("next ready: %w", err)
			}
			mu.Unlock()
			goto drain
		}

		// A worker may have set haltErr while we were polling — don't start new
		// work after a halt (the top-of-loop check can't catch this window).
		if halted() {
			<-sem
			goto drain
		}

		mu.Lock()
		dup := inFlight[issue.Number]
		if !dup {
			inFlight[issue.Number] = true
		}
		mu.Unlock()
		if dup {
			// The issue is already being worked but NextReady handed it to us
			// again — typically because its `ready` label was re-added (someone
			// flipped ralph-working → ready mid-run) or a prior claim didn't stick.
			// Re-assert the working label so the next poll advances to a different
			// issue instead of starving the others by spinning on this one.
			if err := r.provider.MarkWorking(ctx, issue.Number, r.labels); err != nil {
				dash.event(fmt.Sprintf("warn: re-claim #%d: %v", issue.Number, err))
			}
			<-sem
			select {
			case <-ctx.Done():
				goto drain
			case <-time.After(time.Second):
			}
			continue
		}

		// Claim the issue synchronously — remove its `ready` label NOW, before
		// launching the worker — so the next NextReady poll returns a DIFFERENT
		// issue. The worker's own MarkWorking is async, so without this the
		// dispatcher keeps being handed the same oldest ready issue and
		// parallelism ramps up slowly / appears stuck below max_parallel. The
		// worker's later MarkWorking is idempotent.
		if err := r.provider.MarkWorking(ctx, issue.Number, r.labels); err != nil {
			dash.event(fmt.Sprintf("warn: claim #%d: %v", issue.Number, err))
		}

		dash.event(fmt.Sprintf("picking up #%d: %s", issue.Number, issue.Title))
		wg.Add(1)
		go func(num int, title string) {
			defer wg.Done()
			defer func() { <-sem }()

			row := dash.addRow(num, title, nil)
			perr := r.processIssueRow(ctx, num, row)
			row.finish(perr)

			mu.Lock()
			delete(inFlight, num)
			switch {
			case perr == nil:
				r.completed++
			case errors.Is(perr, claude.ErrNotLoggedIn):
				// Environment problem (no usable login) — halt the whole run with
				// the fix; every other issue would fail identically.
				if haltErr == nil {
					haltErr = r.authFailureError(perr)
				}
			case errors.Is(perr, context.Canceled) || errors.Is(perr, context.DeadlineExceeded):
				// Interrupted/requeued — neither done nor failed; the run is
				// shutting down via ctx anyway.
			case errors.Is(perr, errIssueGone):
				// Not actionable (closed/merged/PR) — a benign skip, not a
				// failure. dispatchCleanup requeued it (cleared the working label).
			default:
				// A genuine failure: it was labelled ralph-failed. Keep working
				// the rest of the queue (no halt) — just record it.
				r.failed++
			}
			completed, failed := r.completed, r.failed
			mu.Unlock()
			dash.setCounts(completed, failed)
		}(issue.Number, issue.Title)

		if once {
			goto drain
		}
	}

drain:
	wg.Wait()
	if haltErr != nil {
		return haltErr
	}
	return ctx.Err()
}

// maxReasonLen caps the failure-reason string we post as a GitHub/GitLab
// comment so long stack traces don't dominate the issue thread.
const maxReasonLen = 500

// errNoChanges signals that the loop produced no commits over the base branch —
// i.e. the work already exists on the base. ralph treats this as a resolved
// no-op (close the issue), NOT a failure. See handleCleanupPRWS / ensurePR.
var errNoChanges = errors.New("no changes to merge")

// errIssueGone signals that an issue isn't actionable — it's closed/merged, is a
// PR, or the fetch failed — typically because it was re-dispatched after it
// already shipped. ralph treats this as a benign skip (requeue, clear the
// working label), NOT a failure: it must not inflate the failed count or post a
// failure comment. See processIssueRow / dispatchCleanup.
var errIssueGone = errors.New("issue not actionable")

// noChangesNote is the comment ralph posts when it closes an issue as already
// resolved (no commits over base).
func noChangesNote(branch, base string) string {
	return fmt.Sprintf("🤖 ralph found no changes were needed: branch `%s` has no commits over `%s`, so the work appears to already exist on `%s`. Closing as resolved — reopen if that's not right.", branch, base, base)
}

// run is the per-invocation state.
type run struct {
	cfg      *config.Config
	session  agentSession
	provider vcs.Provider
	labels   vcs.Labels
	ui       io.Writer

	// newSession, when set, overrides how Claude sessions are built (tests
	// inject a fake). Production leaves it nil and uses claude.NewSession.
	newSession sessionFactory

	// passRetryBackoff is the pause between retries of a crashed/timed-out pass.
	// Set to defaultPassRetryBackoff in newRun; tests build run literals and leave
	// it 0 so retries don't sleep.
	passRetryBackoff time.Duration

	// gitMu serialises git commands that mutate the shared repo (.git refs and
	// worktree admin). Concurrent `git worktree add` / `fetch` / `push` against
	// one repo race on ref locks and fail intermittently (empirically ~1/3 of
	// concurrent worktree adds). In parallel auto mode this guards ralph's own
	// plumbing; it's held only for the brief command, so the per-worktree Claude
	// work it brackets still runs fully in parallel. Uncontended in sequential
	// mode.
	gitMu sync.Mutex

	// Claude login state for the active config dir, resolved once in newRun via
	// `claude auth status` and reused by the startup banner (so the probe — and
	// any macOS Keychain prompt it triggers — runs once per invocation, not
	// twice). authKnown is false when the status couldn't be determined.
	authStatus claude.AuthStatus
	authKnown  bool

	// Auto-mode counters; printed in the run banner between issues.
	completed int
	failed    int
}

func newRun(cfg *config.Config) (*run, error) {
	// Preflight: claude must be available before we set anything else up.
	if _, err := claude.LookupBin(cfg.ClaudeBin); err != nil {
		return nil, err
	}
	// Preflight auth: if claude can tell us conclusively that this config dir has
	// no usable login, stop now — before touching the issue tracker or the
	// working tree — with the exact command to fix it. This is the common
	// new-project footgun: a project-local .claude that holds settings/metadata
	// but was never logged in. When the status can't be determined we proceed; a
	// real auth failure mid-run is still caught and reported via the typed
	// claude.ErrNotLoggedIn surfaced from the session.
	effCfgDir := cfg.EffectiveClaudeConfigDir()
	authStatus, authKnown := claude.QueryAuthStatus(cfg.ClaudeBin, effCfgDir)
	if authKnown && !authStatus.LoggedIn {
		return nil, fmt.Errorf("claude is not logged in for %s — authenticate first:\n    %s\nthen re-run ralph", claudeCfgLabel(effCfgDir), authLoginHint(effCfgDir))
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
	r := &run{
		cfg: cfg,
		labels: vcs.Labels{
			Ready:   cfg.GitHub.Labels.Ready,
			Working: cfg.GitHub.Labels.Working,
			Failed:  cfg.GitHub.Labels.Failed,
		},
		ui:               os.Stdout,
		authStatus:       authStatus,
		authKnown:        authKnown,
		passRetryBackoff: defaultPassRetryBackoff,
	}
	sess, err := r.makeSession(claude.SessionConfig{
		Bin:             cfg.ClaudeBin,
		WorkDir:         cfg.ProjectRoot,
		ClaudeConfigDir: cfg.ClaudeConfigDir,
		OutputDir:       outDir,
		UI:              os.Stdout,
	})
	if err != nil {
		return nil, err
	}
	r.session = sess
	return r, nil
}

func (r *run) close() {
	if r.session != nil {
		_ = r.session.Close()
	}
}

// lockedGit runs fn while holding gitMu — use it for every git command that
// mutates the shared repo (worktree add/remove, fetch, push). See gitMu.
func (r *run) lockedGit(fn func() error) error {
	r.gitMu.Lock()
	defer r.gitMu.Unlock()
	return fn()
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
func (r *run) processIssue(ctx context.Context, issueNum int) error {
	return r.processIssueRow(ctx, issueNum, nil)
}

// processIssueRow is processIssue parameterised by an optional dashboard row
// (non-nil only in parallel auto mode). It prepares the workspace — a per-issue
// git worktree by default, or the repo root in-place — runs the refine cycle in
// it, then drives the PR through checks/merge (with repair). The worktree is
// always torn down on exit.
func (r *run) processIssueRow(ctx context.Context, issueNum int, row *issueRow) (resultErr error) {
	defaultBranch := r.resolveDefaultBranch(ctx)

	// In-place mode mutates the repo root, so it must start clean. Worktree mode
	// leaves the root alone — skip the gate. (Pre-flight, before any label work.)
	if !r.cfg.Worktrees {
		if err := r.requireCleanTree(ctx); err != nil {
			return err
		}
	}

	// Catch-all cleanup: registered up front so EVERY exit path below classifies
	// the outcome and clears any ralph-working label (the parallel dispatcher
	// claims the issue before this worker starts). On Ctrl+C / timeout, or when
	// the issue turns out to be gone (errIssueGone), the issue is *requeued*, not
	// burned into the failed pile.
	defer func() { dispatchCleanup(r.provider, issueNum, r.labels, resultErr) }()

	// Validate FIRST — confirm the issue is still open and isn't actually a PR.
	// A closed/merged issue (e.g. re-dispatched after it already shipped) is NOT
	// a failure: GetIssue's error is wrapped as errIssueGone so it's skipped
	// (no ralph-failed label, no spurious failed count) and the working label is
	// cleared by the deferred cleanup above.
	issue, err := r.provider.GetIssue(ctx, issueNum)
	if err != nil {
		return fmt.Errorf("%w: %v", errIssueGone, err)
	}

	// In-place mode syncs the repo root to the default branch here. Worktree mode
	// fetches origin/<default> and cuts the worktree atomically inside
	// makeWorkspace (under gitMu), so the root is left untouched and nothing is
	// done here.
	if !r.cfg.Worktrees {
		if err := git.CheckoutMain(ctx, r.cfg.ProjectRoot, defaultBranch); err != nil {
			return fmt.Errorf("sync %s: %w (commit or stash any local changes first)", defaultBranch, err)
		}
	}

	if err := r.provider.MarkWorking(ctx, issueNum, r.labels); err != nil {
		if row != nil {
			row.setMessage(fmt.Sprintf("warn: mark working: %v", err))
		} else {
			r.log(fmt.Sprintf("warn: mark working: %v", err))
		}
	}

	// Guardrail: put the loop on a dedicated branch BEFORE any work, so every
	// commit Claude makes — and any push — lands there, never on the protected
	// default branch. In worktree mode this is an isolated `git worktree` cut
	// from origin/<default>; in-place it's `git checkout -B` in the repo root.
	ws, err := r.makeWorkspace(ctx, issueNum, issue.Title, defaultBranch, row)
	if err != nil {
		return fmt.Errorf("prepare workspace: %w", err)
	}
	defer ws.cleanup()
	if row != nil {
		// Live "latest message" for the dashboard: the last line of the current
		// Claude turn, polled each render.
		row.setMessageFn(func() string { return lastLine(ws.session.LastTurnText()) })
		row.setBranch(ws.branch)
	}

	ws.log(fmt.Sprintf("On branch %s (off %s); commits stay off %s.", ws.branch, defaultBranch, defaultBranch))
	ws.section(fmt.Sprintf("Working %s issue #%d: %s", r.provider.Name(), issueNum, issue.Title))

	workPrompt := branchContextNote(ws.branch, defaultBranch) + "\n\n" + renderIssuePrompt(r.cfg, r.provider.Name(), issue)
	cleanupPrompt := renderCleanup(r.cfg, issueNum)

	if err := r.cycle(ctx, ws, workPrompt, cleanupPrompt); err != nil {
		return err
	}
	return r.handleCleanupPRWS(ctx, ws, issueNum, issue.Title)
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

// cycle runs the goal-driven refine loop followed by a cleanup pass.
//
// The loop is not a fixed count: each pass drives toward the goal. It always
// runs at least cfg.MinIterations passes — a completion signal before that
// floor is deliberately ignored so the work gets a baseline number of critical
// re-audits rather than stopping the moment Claude first thinks it's done. From
// the floor up to the cfg.Iterations cap, the loop ends as soon as completion
// is signalled AND confirmed (by VerifyCommand when configured). Claude is
// never told either bound, so it can't pace itself or declare "done" early just
// because a budget is running out.
func (r *run) cycle(ctx context.Context, ws *workspace, workPrompt, cleanupPrompt string) error {
	if err := ws.session.Reset(); err != nil {
		return fmt.Errorf("reset logs: %w", err)
	}
	planRel, planAbs := r.planPaths(ws)
	// Start each task with a clean plan: a stale plan left by a previous issue
	// would mislead the first pass of this one.
	_ = os.Remove(planAbs)

	loopPrompt := renderRefinePrompt(r.cfg, planRel)
	minPasses := r.cfg.MinIterations
	maxPasses := r.cfg.Iterations
	feedback := "" // verify failure / "keep refining" note fed into the next pass
	done := false
	for i := 1; i <= maxPasses && !done; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Fresh context every pass: a brand-new session that cannot see the
		// earlier turns. This is what stops Claude from pacing itself ("I'll
		// finish the rest next time") or rubber-stamping code it remembers
		// writing — state carries over only via the plan file and git. The pass
		// counter/bounds are operator-facing only and stay out of both the prompt
		// AND the on-disk transcript (sectionConsoleOnly), so a fresh pass can't
		// read its own loop position back out of .ralph/output.txt.
		if err := ws.session.StartFresh(); err != nil {
			return fmt.Errorf("start pass %d: %w", i, err)
		}
		ws.sectionConsoleOnly(fmt.Sprintf("Pass %d (min %d, max %d)", i, minPasses, maxPasses))
		prompt := workPrompt + "\n\n" + loopPrompt
		if feedback != "" {
			prompt += "\n\n" + feedback
		}
		if err := r.runGuarded(ctx, ws, fmt.Sprintf("pass %d", i), prompt); err != nil {
			return err
		}

		if !sawSentinel(ws.session.LastTurnText(), r.cfg.CompletionSentinel) {
			feedback = ""
			continue
		}
		// Below the minimum floor, ignore the completion signal and keep going.
		// Push for a genuinely deeper pass rather than a rubber-stamp re-confirm.
		if !honorsCompletion(i, minPasses) {
			ws.log(fmt.Sprintf("Completion signalled on pass %d, but the minimum is %d — continuing to refine.", i, minPasses))
			// Loop-invisible: this is appended to a fresh pass, so it must stand
			// on its own and must not reveal that earlier passes happened.
			feedback = "Before this can be considered finished, do a deeper, genuinely skeptical review — green tests are not proof of correctness. Re-derive the expected behavior directly from the goal and confirm the tests actually assert it (tests can be wrong, tautological, or incomplete); hunt for edge and failure cases and subtle correctness or security issues; and consider whether the solution itself should be reworked. Record anything you find under FINDINGS and fix it. Only signal completion once you genuinely cannot find anything more to improve."
			continue
		}
		// At/above the floor: a self-emitted token is the weakest possible
		// oracle (models declare "done" prematurely), so confirm it against the
		// real build/test suite when one is configured.
		ok, out := r.confirmComplete(ctx, ws)
		if ok {
			ws.log("Goal reported complete and verification passed; ending refine loop.")
			done = true
			continue
		}
		ws.log("Completion was signalled but the verify command failed; continuing the loop.")
		// Loop-invisible: appended to a fresh pass; states the failure as a
		// current fact without revealing that a prior pass claimed completion.
		feedback = "The verification command does not pass yet:\n\n" +
			truncate(out, maxReasonLen) +
			"\n\nThe goal is not complete. Fix the underlying issues so this command passes, and do not signal completion until it does."
	}
	if !done {
		ws.log(fmt.Sprintf("Refine loop hit the %d-pass safety cap without a verified completion; proceeding to cleanup.", maxPasses))
	}

	ws.section("Cleanup pass")
	if err := r.runGuarded(ctx, ws, "cleanup pass", cleanupPrompt); err != nil {
		return err
	}
	return nil
}

// defaultPassRetryBackoff is the pause between retries of a crashed/timed-out
// Claude turn — long enough to let transient pressure (e.g. the memory spike
// that triggered an OOM kill) clear, short enough not to stall the loop.
const defaultPassRetryBackoff = 3 * time.Second

// runGuarded runs one Claude turn against the workspace — a refine pass, the
// cleanup pass, or a merge-repair pass, named by label (e.g. "pass 3", "cleanup
// pass", "repair pass 2") — applying the per-pass timeout (cfg.PassTimeout) and
// the crash/timeout retry policy (cfg.PassRetries). It delegates to
// runPassWithRetry so the policy stays unit-testable without a live session.
//
// Like the refine loop, callers that want attempt 0 to start from a fresh Claude
// session must StartFresh before calling; the guard only resets the session on a
// retry, so whatever state attempt 0 used, a crash-retry starts clean.
func (r *run) runGuarded(ctx context.Context, ws *workspace, label, prompt string) error {
	return runPassWithRetry(ctx, label, r.cfg.PassRetries, r.cfg.PassTimeoutDur, r.passRetryBackoff,
		ws.session.StartFresh,
		func(passCtx context.Context) error { return ws.session.Run(passCtx, prompt) },
		ws.log)
}

// runPassWithRetry runs runOnce under an optional per-pass timeout and retries a
// crashed or timed-out turn in place up to `retries` times, calling startFresh
// (a new claude session on the same worktree) before each retry and logf to
// report it. Committed work lives on the branch/worktree, so a retry rebuilds on
// it — only the failed attempt's uncommitted work is redone.
//
// A timeout surfaces as context.DeadlineExceeded while the parent ctx is still
// live, so it flows through the same retry path as a subprocess crash. Two things
// are deliberately NOT retried, because a re-run can't fix them: a parent-ctx
// cancellation (real Ctrl+C / SIGTERM to ralph → the issue should requeue) and
// claude.ErrNotLoggedIn (an auth/environment problem → the run halts). Both are
// returned wrapped so dispatchCleanup classifies them correctly. Once retries are
// exhausted the error is returned WITHOUT wrapping the underlying context error,
// so a persistently timing-out turn is treated as a genuine failure (MarkFailed)
// rather than reclassified as a requeue.
func runPassWithRetry(
	ctx context.Context,
	label string,
	retries int,
	timeout, backoff time.Duration,
	startFresh func() error,
	runOnce func(context.Context) error,
	logf func(string),
) error {
	var runErr error
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			logf(fmt.Sprintf("%s %s — retry %d/%d (same worktree)",
				label, passFailDesc(runErr, timeout), attempt, retries))
			if err := sleepCtx(ctx, backoff); err != nil {
				return fmt.Errorf("%s: %w", label, err) // Ctrl+C during the backoff
			}
			if err := startFresh(); err != nil {
				return fmt.Errorf("start %s retry %d: %w", label, attempt, err)
			}
		}

		passCtx := ctx
		var cancel context.CancelFunc
		if timeout > 0 {
			passCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		runErr = runOnce(passCtx)
		if cancel != nil {
			cancel()
		}
		if runErr == nil {
			return nil
		}
		// Not retryable: propagate so the caller requeues (ctx) or halts (auth).
		if ctx.Err() != nil || errors.Is(runErr, claude.ErrNotLoggedIn) {
			return fmt.Errorf("%s: %w", label, runErr)
		}
		if attempt >= retries {
			return fmt.Errorf("%s failed after %d retries: %s", label, retries, redactSecrets(runErr.Error()))
		}
	}
}

// passFailDesc renders a short reason for a retryable pass failure: a per-pass
// timeout vs a subprocess crash. Secrets in the crash text are redacted since it
// reaches the operator log.
func passFailDesc(err error, timeout time.Duration) string {
	if timeout > 0 && errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("timed out after %s", timeout)
	}
	return "crashed: " + redactSecrets(err.Error())
}

// sleepCtx waits d, or returns early with ctx.Err() if ctx is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// confirmComplete decides whether a completion signal should actually end the
// loop. With no VerifyCommand configured the token is trusted (with a logged
// caveat); otherwise the command is run in the project root and its exit status
// is the verdict. Returns the combined output for feedback on failure.
func (r *run) confirmComplete(ctx context.Context, ws *workspace) (bool, string) {
	cmd := strings.TrimSpace(r.cfg.VerifyCommand)
	if cmd == "" {
		ws.log("No verify_command configured — accepting the completion signal without an independent gate. Set verify_command in .go-ralph-go for a robust check.")
		return true, ""
	}
	ws.log("Verifying completion: " + cmd)
	return runVerify(ctx, ws.dir, cmd)
}

// runVerify runs cmd via `sh -c` in dir, returning whether it exited cleanly
// plus its combined output (annotated with the command + exit error on
// failure, for feeding back to Claude).
func runVerify(ctx context.Context, dir, cmd string) (bool, string) {
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		return false, fmt.Sprintf("$ %s\nexit: %v\n%s", cmd, err, out)
	}
	return true, string(out)
}

// planPaths returns the plan file path both relative to the project root (for
// telling Claude, which runs with cwd = project root) and absolute (for the
// runner's own file ops). It lives under the gitignored output dir so it never
// pollutes the user's commits/PR.
func (r *run) planPaths(ws *workspace) (rel, abs string) {
	rel = filepath.Join(r.cfg.OutputDir, "plan.md")
	abs = filepath.Join(ws.dir, rel)
	return rel, abs
}

// honorsCompletion reports whether a completion signal seen on pass i (1-based)
// may end the loop, given the minimum-passes floor. Below the floor, early
// completion is ignored so the loop keeps refining. With minPasses=5 the loop
// runs at least 5 passes: passes 1–4 ignore "done", pass 5 may honour it.
func honorsCompletion(i, minPasses int) bool { return i >= minPasses }

// sawSentinel reports whether the completion token appears as its own line in
// text. A whole-line match (not a substring) avoids false positives when Claude
// merely mentions the token mid-sentence.
func sawSentinel(text, sentinel string) bool {
	sentinel = strings.TrimSpace(sentinel)
	if sentinel == "" {
		return false
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == sentinel {
			return true
		}
	}
	return false
}

// renderRefinePrompt fills the goal-driven loop template: {{plan_file}},
// {{sentinel}}, and {{verify}} (a clause that reflects whether a harness-owned
// VerifyCommand gate is configured). Single-pass via strings.NewReplacer for
// placeholder safety (matches renderIssuePrompt / renderCleanup).
func renderRefinePrompt(cfg *config.Config, planFile string) string {
	verify := "Run the project's full build, test, and lint suite."
	if cmd := strings.TrimSpace(cfg.VerifyCommand); cmd != "" {
		verify = fmt.Sprintf("Run the full verification gate. The harness will independently run `%s` and will not accept completion unless it exits cleanly, so make sure it does.", cmd)
	}
	r := strings.NewReplacer(
		"{{plan_file}}", planFile,
		"{{sentinel}}", cfg.CompletionSentinel,
		"{{verify}}", verify,
	)
	return r.Replace(cfg.RefinePrompt)
}

// handleCleanupPR locates the branch the loop worked on and ensures an open
// PR/MR exists for it — opening one itself if the cleanup pass didn't (see
// ensurePR) — then for issue mode waits for checks and squash-merges. For
// ad-hoc prompts (issueNum=0) it stops after ensuring the PR (no auto-merge).
// issueTitle, when non-empty, seeds the title of any PR ralph has to open.
func (r *run) handleCleanupPR(ctx context.Context, issueNum int, issueTitle string) error {
	return r.handleCleanupPRWS(ctx, r.rootWorkspace(), issueNum, issueTitle)
}

// handleCleanupPRWS is handleCleanupPR scoped to a workspace (worktree or root).
// For an issue it drives the PR all the way to merged, and — crucially — when
// checks fail or the merge is refused (broken tests, merge conflicts) it feeds
// the failure back to Claude as a repair pass (rebase onto the updated default
// branch, fix, re-push) and retries, up to cfg.MergeRepairAttempts. That's what
// lets `ralph auto` keep working an issue until it actually merges instead of
// giving up on the first red check.
func (r *run) handleCleanupPRWS(ctx context.Context, ws *workspace, issueNum int, issueTitle string) error {
	branch, err := git.CurrentBranch(ctx, ws.dir)
	if err != nil {
		return fmt.Errorf("detect branch: %w", err)
	}
	defaultBranch := r.resolveDefaultBranch(ctx)
	if branch == defaultBranch {
		return fmt.Errorf("cleanup pass left us on %s — no feature branch to open a PR from (ralph creates one before the loop; the agent must not check out %s)", defaultBranch, defaultBranch)
	}

	pr, err := r.ensurePR(ctx, ws, branch, defaultBranch, issueNum, issueTitle)
	if err != nil {
		// "Nothing to merge" because the work already exists on the base branch
		// is a resolved/no-op outcome, not a failure: close the issue with an
		// explanatory note instead of marking it ralph-failed. (Ad-hoc run --pr,
		// issueNum 0, still surfaces the error — there's no issue to close.)
		if errors.Is(err, errNoChanges) && issueNum > 0 {
			ws.log(fmt.Sprintf("No changes needed — the work already exists on %s; closing #%d as resolved.", defaultBranch, issueNum))
			if e := r.provider.MarkSkipped(ctx, issueNum, r.labels, noChangesNote(branch, defaultBranch)); e != nil {
				ws.log(fmt.Sprintf("warn: mark skipped on issue #%d: %v", issueNum, e))
			}
			r.resetRootIfNeeded(ctx, ws, defaultBranch)
			return nil
		}
		return err
	}

	if issueNum == 0 {
		// Ad-hoc --pr mode: PR ensured but NOT auto-merged.
		ws.log(fmt.Sprintf("PR #%d ready on branch %s (no auto-merge).", pr.Number, branch))
		if pr.URL != "" {
			ws.log("  → " + pr.URL)
		}
		r.resetRootIfNeeded(ctx, ws, defaultBranch)
		return nil
	}

	if pr.URL != "" {
		ws.log(fmt.Sprintf("PR #%d  →  %s", pr.Number, pr.URL))
	}

	interval := time.Duration(r.cfg.GitHub.CheckIntervalSeconds) * time.Second
	attempts := 0
	for {
		ws.section(fmt.Sprintf("Waiting for checks on PR #%d", pr.Number))
		checkErr := r.provider.WaitForChecks(ctx, pr.Number, interval)

		if checkErr == nil {
			ws.section(fmt.Sprintf("Squash-merging PR #%d", pr.Number))
			mergeErr := r.provider.SquashMergeAndDelete(ctx, pr.Number)
			if mergeErr == nil {
				// Defensive close: if Claude's PR body lacked "Closes #N", GitHub
				// won't auto-close the issue on merge and ralph-working would
				// linger forever. Best-effort — failure here doesn't undo the merge.
				if err := r.provider.MarkResolved(ctx, issueNum, r.labels); err != nil {
					ws.log(fmt.Sprintf("warn: mark resolved on issue #%d: %v", issueNum, err))
				}
				ws.log(fmt.Sprintf("Merged PR #%d.", pr.Number))
				r.resetRootIfNeeded(ctx, ws, defaultBranch)
				return nil
			}
			// Merge refused — usually the branch is behind base or conflicts.
			if !mergeRepairable(mergeErr) || attempts >= r.cfg.MergeRepairAttempts {
				r.resetRootIfNeeded(ctx, ws, defaultBranch)
				return fmt.Errorf("merge refused on PR #%d: %w", pr.Number, mergeErr)
			}
			attempts++
			if err := r.repairForMerge(ctx, ws, branch, defaultBranch, attempts, "the merge was refused (likely a conflict or out-of-date branch)", mergeErr.Error()); err != nil {
				r.resetRootIfNeeded(ctx, ws, defaultBranch)
				return err
			}
			continue
		}

		// Checks failed or the wait was cancelled.
		if errors.Is(checkErr, context.Canceled) || errors.Is(checkErr, context.DeadlineExceeded) {
			r.resetRootIfNeeded(ctx, ws, defaultBranch)
			return checkErr
		}
		if attempts >= r.cfg.MergeRepairAttempts {
			r.resetRootIfNeeded(ctx, ws, defaultBranch)
			return fmt.Errorf("checks failed on PR #%d after %d repair attempt(s): %w", pr.Number, attempts, checkErr)
		}
		attempts++
		if err := r.repairForMerge(ctx, ws, branch, defaultBranch, attempts, "the PR's CI checks failed", checkErr.Error()); err != nil {
			r.resetRootIfNeeded(ctx, ws, defaultBranch)
			return err
		}
		// Loop: WaitForChecks re-reads the PR head, so it picks up the new commits.
	}
}

// resetRootIfNeeded restores the repo root to the default branch after an
// in-place run. In worktree mode it is a no-op: the root checkout was never
// touched (the whole point — the developer may be working in it), and the
// worktree is removed by ws.cleanup instead.
func (r *run) resetRootIfNeeded(ctx context.Context, ws *workspace, defaultBranch string) {
	if ws.isWorktree {
		return
	}
	_ = git.CheckoutMain(ctx, r.cfg.ProjectRoot, defaultBranch)
}

// mergeRepairable reports whether a checks/merge failure is worth a repair pass.
// Cancellation/timeout is the user interrupting — propagate it, don't "repair".
// Everything else (red checks, conflicts, transient merge refusals) gets a shot.
func mergeRepairable(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// repairForMerge runs one Claude pass aimed squarely at making the PR mergeable:
// rebase onto the freshly-fetched default branch, fix the failure, commit. ralph
// then force-pushes (a repair may have rewritten history via rebase) so the next
// WaitForChecks runs against the new head.
func (r *run) repairForMerge(ctx context.Context, ws *workspace, branch, defaultBranch string, attempt int, what, detail string) error {
	ws.section(fmt.Sprintf("Merge repair %d/%d — %s", attempt, r.cfg.MergeRepairAttempts, what))
	// Refresh the base so the rebase targets up-to-date upstream.
	if err := r.lockedGit(func() error { return git.Fetch(ctx, ws.dir, "origin", defaultBranch) }); err != nil {
		ws.log("warn: fetch base for repair: " + err.Error())
	}
	planRel, _ := r.planPaths(ws)
	prompt := renderRepairPrompt(r.cfg, planRel, branch, defaultBranch, what, truncate(detail, maxReasonLen))
	if err := ws.session.StartFresh(); err != nil {
		return fmt.Errorf("repair start: %w", err)
	}
	if err := r.runGuarded(ctx, ws, fmt.Sprintf("repair pass %d", attempt), prompt); err != nil {
		return err
	}
	// Make sure the branch's remote-tracking ref is current before force-pushing
	// so --force-with-lease has a baseline to compare against (the branch may
	// have first reached the remote via Claude's own push, leaving this worktree
	// without a tracking ref). Best-effort: a missing remote branch just means
	// the upcoming push creates it.
	if err := r.lockedGit(func() error {
		_ = git.Fetch(ctx, ws.dir, "origin", branch)
		return git.ForcePush(ctx, ws.dir, branch)
	}); err != nil {
		return fmt.Errorf("repair push: %w", err)
	}
	return nil
}

// renderRepairPrompt builds the single-turn prompt for a merge-repair pass.
func renderRepairPrompt(cfg *config.Config, planFile, branch, defaultBranch, what, detail string) string {
	verify := "Run the project's full build, test, and lint suite and show the real output."
	if cmd := strings.TrimSpace(cfg.VerifyCommand); cmd != "" {
		verify = fmt.Sprintf("Run `%s` and make sure it exits cleanly, showing its real output.", cmd)
	}
	return fmt.Sprintf(`You are on git branch `+"`%s`"+`, which is open as a pull request into `+"`%s`"+`. The PR is NOT mergeable yet: %s.

Failure detail reported by the host:
%s

This is a single, non-interactive turn — do everything now, in the foreground, and finish before you end this response:
1. Commit any outstanding changes on this branch so the tree is clean.
2. Bring the branch up to date with the base: `+"`git fetch origin %s`"+`, then `+"`git rebase origin/%s`"+`. If there are conflicts, resolve them correctly (preserve both sides' intent — never discard others' work), then `+"`git rebase --continue`"+` until the rebase completes.
3. Fix the real cause of the failure above. If tests failed, fix the CODE so they pass — never weaken, skip, xfail, comment out, hard-code around, or delete a test to go green. If the only problem was the branch being behind base, step 2 may already resolve it.
4. %s
5. Commit the result on `+"`%s`"+` with a clear message. Do NOT push (the harness force-pushes for you), do NOT open a new PR (one already exists), and do NOT switch to or commit on `+"`%s`"+`.

Keep `+"`%s`"+` accurate. Work only on making this PR mergeable; add nothing unrelated.`,
		branch, defaultBranch, what, detail, defaultBranch, defaultBranch, verify, branch, defaultBranch, planFile)
}

// ensurePR returns the open PR/MR for branch, opening one itself if the cleanup
// pass didn't. This is the deterministic complement to the branch guardrail:
// ralph owns the git/PR mechanics so a run never fails merely because the agent
// committed but ran out of turn before pushing or opening the PR (e.g. it
// backgrounded a long test gate). When no PR exists ralph pushes the branch and
// opens the PR; if the branch carries no commits over base there is genuinely
// nothing to do, so it surfaces that clearly instead of opening an empty PR.
func (r *run) ensurePR(ctx context.Context, ws *workspace, branch, baseBranch string, issueNum int, issueTitle string) (*vcs.PR, error) {
	pr, err := r.provider.FindPRForBranch(ctx, branch)
	if err != nil {
		return nil, fmt.Errorf("find PR: %w", err)
	}
	if pr != nil {
		return pr, nil
	}

	// No PR yet — the cleanup pass committed work but didn't open one. Confirm
	// there is actually work to merge before doing anything remote. In worktree
	// mode the branch was cut from origin/<base>, so compare against that.
	base := baseBranch
	if ws.isWorktree {
		base = "origin/" + baseBranch
	}
	if ahead, err := git.CommitsAhead(ctx, ws.dir, base, branch); err == nil && ahead == 0 {
		return nil, fmt.Errorf("%w: branch %s has no commits over %s", errNoChanges, branch, base)
	}
	ws.log(fmt.Sprintf("No PR found for branch %s — pushing and opening one.", branch))
	if err := r.lockedGit(func() error { return git.Push(ctx, ws.dir, branch) }); err != nil {
		return nil, err
	}
	title, body := ralphPRContent(issueNum, issueTitle, branch)
	pr, err = r.provider.CreatePR(ctx, branch, baseBranch, title, body)
	if err != nil {
		return nil, fmt.Errorf("open PR for branch %s: %w", branch, err)
	}
	ws.log(fmt.Sprintf("Opened PR #%d (ralph fallback — cleanup pass did not open it).", pr.Number))
	return pr, nil
}

// ralphPRContent builds the title/body for a PR ralph opens as a fallback. For
// an issue it seeds the title from the issue and embeds "Closes #N" so the
// merge auto-closes it; for an ad-hoc run it names the branch.
func ralphPRContent(issueNum int, issueTitle, branch string) (title, body string) {
	const note = "🤖 Opened by ralph after the loop: the cleanup pass committed work but did not open a PR itself."
	if issueNum > 0 {
		title = issueTitle
		if title == "" {
			title = fmt.Sprintf("Resolve issue #%d", issueNum)
		}
		return title, fmt.Sprintf("Closes #%d\n\n%s", issueNum, note)
	}
	return fmt.Sprintf("ralph: %s", branch), note
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

// accountLabel renders the Claude login a run will bill against — e.g.
// "you@example.com  ·  usage_based  ·  Acme". Falls back gracefully when no
// oauthAccount is present (raw API-key auth or a missing .claude.json).
func accountLabel(a claude.Account) string {
	if a.Empty() {
		return "(unknown — no oauthAccount in .claude.json; API-key auth?)"
	}
	return a.Label()
}

// claudeAcctLine renders the account a run will actually use, preferring the
// authoritative `claude auth status` identity (which reads the real credential
// store — the macOS Keychain, not a .credentials.json file) over the
// .claude.json metadata. When the status can't be determined it falls back to
// metadata; when it says "not logged in" it flags the metadata as unverified.
func claudeAcctLine(acct claude.Account, st claude.AuthStatus, ok bool) string {
	if ok && st.LoggedIn {
		parts := make([]string, 0, 3)
		if st.Email != "" {
			parts = append(parts, st.Email)
		}
		switch {
		case st.SubscriptionType != "":
			parts = append(parts, st.SubscriptionType+" plan")
		case acct.BillingType != "":
			parts = append(parts, acct.BillingType)
		}
		org := st.OrgName
		if org == "" {
			org = acct.Organization
		}
		if org != "" {
			parts = append(parts, org)
		}
		if len(parts) == 0 {
			return "(logged in)"
		}
		return strings.Join(parts, "  ·  ")
	}
	if ok && !st.LoggedIn {
		return accountLabel(acct) + "  (metadata only — NOT logged in)"
	}
	return accountLabel(acct)
}

// authLoginHint returns the command to authenticate the given config dir.
func authLoginHint(effCfgDir string) string {
	if effCfgDir == "" {
		return "claude auth login"
	}
	return fmt.Sprintf("CLAUDE_CONFIG_DIR=%q claude auth login", effCfgDir)
}

// claudeCfgLabel describes the config dir a run authenticates against, for use
// in operator-facing messages.
func claudeCfgLabel(effCfgDir string) string {
	if effCfgDir == "" {
		return "the system claude config (~/.claude)"
	}
	return effCfgDir
}

// authFailureError turns a claude.ErrNotLoggedIn into an actionable top-level
// failure. The cause is the local environment (no usable login for the active
// config dir), so we point the operator straight at the fix instead of bubbling
// up a bare "exit status 1". Used by every entry point so the guidance is
// identical whether the failure is caught at preflight or mid-run.
func (r *run) authFailureError(err error) error {
	effCfgDir := r.cfg.EffectiveClaudeConfigDir()
	return fmt.Errorf("claude is not logged in for %s — ralph did no work. Authenticate with:\n    %s\nthen re-run ralph (underlying: %v)", claudeCfgLabel(effCfgDir), authLoginHint(effCfgDir), err)
}

// printStartupBanner prints a one-time summary of the effective configuration
// so the developer can confirm ralph picked up the right project + settings.
func (r *run) printStartupBanner(mode string) {
	cfgSrc := r.cfg.ConfigPath
	if cfgSrc == "" {
		cfgSrc = "(defaults; no .go-ralph-go found)"
	}
	effCfgDir := r.cfg.EffectiveClaudeConfigDir()
	claudeCfg := "(system default ~/.claude)"
	switch {
	case r.cfg.ClaudeConfigDir != "":
		claudeCfg = effCfgDir
	case effCfgDir != "":
		claudeCfg = effCfgDir + "  (from $CLAUDE_CONFIG_DIR)"
	}
	fmt.Fprintln(r.ui)
	fmt.Fprintln(r.ui, "ralph")
	fmt.Fprintf(r.ui, "  mode         : %s\n", mode)
	fmt.Fprintf(r.ui, "  project root : %s\n", r.cfg.ProjectRoot)
	acct := claude.LoadAccount(effCfgDir)
	// Reuse the auth status resolved in newRun. A conclusive "not logged in" can
	// never reach here — newRun aborts the run up front with the fix — so the
	// banner only reports the account (logged-in identity, or metadata when the
	// status couldn't be determined).
	fmt.Fprintf(r.ui, "  config       : %s\n", cfgSrc)
	fmt.Fprintf(r.ui, "  claude config: %s\n", claudeCfg)
	fmt.Fprintf(r.ui, "  claude acct  : %s\n", claudeAcctLine(acct, r.authStatus, r.authKnown))
	verify := r.cfg.VerifyCommand
	if verify == "" {
		verify = "(none — completion signal trusted as-is)"
	}
	fmt.Fprintf(r.ui, "  passes       : min %d, max %d\n", r.cfg.MinIterations, r.cfg.Iterations)
	passTimeout := "off"
	if r.cfg.PassTimeoutDur > 0 {
		passTimeout = r.cfg.PassTimeoutDur.String()
	}
	fmt.Fprintf(r.ui, "  pass guard   : retries %d, timeout %s\n", r.cfg.PassRetries, passTimeout)
	fmt.Fprintf(r.ui, "  verify cmd   : %s\n", verify)
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

func bannerText(title string) string {
	return "\n==========================================================\n" +
		fmt.Sprintf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), title) +
		"==========================================================\n"
}

func (r *run) section(title string) {
	b := bannerText(title)
	fmt.Fprint(r.ui, b)
	_ = r.session.WriteBanner(b)
}

// sectionConsoleOnly prints a banner to the operator console but NOT the
// on-disk transcript. Used for the per-pass counter ("Pass 3 (min 5, max 10)"):
// since each pass runs with a fresh context, anything written to .ralph/output.*
// could be read back by Claude — so the loop position is kept off disk.
func (r *run) sectionConsoleOnly(title string) {
	fmt.Fprint(r.ui, bannerText(title))
}
