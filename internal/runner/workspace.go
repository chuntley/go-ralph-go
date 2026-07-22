package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/chuntley/go-ralph-go/internal/claude"
	"github.com/chuntley/go-ralph-go/internal/config"
	"github.com/chuntley/go-ralph-go/internal/git"
)

// workspace is the per-issue execution context: the directory work happens in,
// the Claude session rooted there, and where its log/status output goes. It is
// what makes both worktree isolation and parallel auto mode possible — each
// concurrent issue gets its own workspace, so nothing per-issue lives on the
// shared *run.
//
// Two shapes:
//   - root workspace (Worktrees=false): dir is the repo root and session is the
//     run's single session — today's in-place behaviour, byte-for-byte.
//   - worktree workspace (Worktrees=true): dir is an isolated `git worktree`
//     with its own freshly-created session; the repo root is never touched.
type workspace struct {
	issueNum   int
	branch     string // working branch (informational; cleanup re-detects via HEAD)
	dir        string // cwd for Claude + all git ops (worktree path or repo root)
	outputDir  string // absolute .ralph dir for this workspace
	session    agentSession
	isWorktree bool

	// console, when non-nil, is where log/section banners print (the operator's
	// stdout in sequential mode). nil in dashboard mode, where output is routed
	// to row instead and full detail still lands in the on-disk transcript.
	console io.Writer
	row     *issueRow // dashboard row in parallel mode; nil otherwise

	cleanup func() // tear down the worktree + session; no-op for the root workspace
}

// rootWorkspace wraps the repo root + the run's shared session as a workspace —
// the in-place, non-worktree execution context used for `run`, `run --pr`, and
// (when Worktrees=false) issue/auto.
func (r *run) rootWorkspace() *workspace {
	return &workspace{
		dir:       r.cfg.ProjectRoot,
		outputDir: filepath.Join(r.cfg.ProjectRoot, r.cfg.OutputDir),
		session:   r.session,
		console:   r.ui,
		cleanup:   func() {},
	}
}

// makeWorkspace prepares the working tree + session for an issue. In worktree
// mode it cuts a fresh `git worktree` off origin/<defaultBranch> (leaving the
// repo root untouched) with its own session; otherwise it creates the working
// branch in the repo root and reuses the run's session. defaultBranch must
// already be fetched/synced by the caller. row is non-nil only in the parallel
// dashboard path.
func (r *run) makeWorkspace(ctx context.Context, issueNum int, issueTitle, defaultBranch string, row *issueRow) (*workspace, error) {
	branch := issueBranchName(r.cfg.BranchPrefix, issueNum, issueTitle)

	if !r.cfg.Worktrees {
		// In-place: put the repo root on the working branch (HEAD is on the
		// freshly-pulled default branch, see processIssue), reusing the run's
		// session. If the branch already exists from a prior run, resume it
		// (preserving its commits) rather than -B-resetting to upstream — the
		// in-place mirror of the worktree resume below.
		if git.LocalBranchExists(ctx, r.cfg.ProjectRoot, branch) {
			if err := git.CheckoutBranch(ctx, r.cfg.ProjectRoot, branch); err != nil {
				return nil, fmt.Errorf("resume work branch: %w", err)
			}
			r.logResume(issueNum, branch, nil)
		} else if err := git.CreateWorkBranch(ctx, r.cfg.ProjectRoot, branch); err != nil {
			return nil, fmt.Errorf("create work branch: %w", err)
		}
		ws := r.rootWorkspace()
		ws.issueNum = issueNum
		ws.branch = branch
		return ws, nil
	}

	// Worktree mode: an isolated checkout cut from up-to-date upstream. The repo
	// root is never checked out or modified.
	path := worktreePath(r.cfg, issueNum)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir worktree base: %w", err)
	}
	// Fetch the base and cut the worktree as one critical section: concurrent
	// `git fetch` / `worktree add` against the shared .git race on ref locks and
	// fail intermittently. The lock is released before the (parallel) Claude work
	// begins.
	//
	// Resume prior work: the worktree directory is disposable and always torn down
	// on exit (no orphaned temp dirs), but a prior run's commits live on the issue
	// BRANCH, which survives. So if that branch already exists, cut the fresh
	// worktree from it (preserving those commits) rather than resetting to
	// upstream and discarding them. A brand-new issue starts fresh off
	// origin/<default>.
	resuming := false
	if err := r.lockedGit(func() error {
		if err := git.Fetch(ctx, r.cfg.ProjectRoot, "origin", defaultBranch); err != nil {
			return fmt.Errorf("fetch %s: %w", defaultBranch, err)
		}
		// Clear any leftover from a hard-killed run: RemoveWorktree drops a still-
		// registered worktree (and always prunes the admin record), then RemoveAll
		// nukes an orphaned directory git no longer knows about — either of which
		// would otherwise make `worktree add` fail with "already exists". The branch
		// ref is untouched by this.
		_ = git.RemoveWorktree(ctx, r.cfg.ProjectRoot, path)
		_ = os.RemoveAll(path)
		if git.LocalBranchExists(ctx, r.cfg.ProjectRoot, branch) {
			resuming = true
			return git.AddWorktreeResume(ctx, r.cfg.ProjectRoot, path, branch)
		}
		return git.AddWorktree(ctx, r.cfg.ProjectRoot, path, branch, "origin/"+defaultBranch)
	}); err != nil {
		return nil, err
	}
	if resuming {
		r.logResume(issueNum, branch, row)
	}

	root := r.cfg.ProjectRoot
	// Tear down with a FRESH context so removal still runs even when the run ctx
	// is already cancelled (Ctrl+C, or the very failure we're cleaning up after).
	rmWorktree := func() {
		rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = r.lockedGit(func() error { return git.RemoveWorktree(rmCtx, root, path) })
	}

	outputDir := filepath.Join(path, r.cfg.OutputDir)
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		rmWorktree()
		return nil, fmt.Errorf("mkdir worktree output dir: %w", err)
	}

	sessionUI, console := r.sinks(row)
	sess, err := r.makeSession(claude.SessionConfig{
		Bin:     r.cfg.ClaudeBin,
		WorkDir: path,
		// Keep the ORIGINAL absolute config dir, not one inside the worktree:
		// Claude's login is keyed per config dir, and a fresh worktree checkout
		// of .claude (if the project commits one) would be metadata-only and not
		// logged in. Reusing the root login avoids that footgun.
		ClaudeConfigDir: r.cfg.ClaudeConfigDir,
		OutputDir:       outputDir,
		UI:              sessionUI,
	})
	if err != nil {
		rmWorktree()
		return nil, err
	}

	return &workspace{
		issueNum:   issueNum,
		branch:     branch,
		dir:        path,
		outputDir:  outputDir,
		session:    sess,
		isWorktree: true,
		console:    console,
		row:        row,
		cleanup: func() {
			_ = sess.Close()
			// rmWorktree uses a fresh context, so the worktree is torn down even
			// after Ctrl+C. Committed work survives on the branch ref regardless.
			rmWorktree()
		},
	}, nil
}

// logResume reports that an issue is being resumed on an existing branch (its
// prior committed work is intact). Routed like any pre-workspace message: to the
// dashboard row in parallel mode, else the operator console.
func (r *run) logResume(issueNum int, branch string, row *issueRow) {
	msg := fmt.Sprintf("Resuming issue #%d on existing branch %s — prior committed work is preserved.", issueNum, branch)
	if row != nil {
		row.setMessage(msg)
	} else {
		fmt.Fprintf(r.ui, "[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
	}
}

// sinks chooses where a workspace's Claude stream and ralph log lines go. With a
// dashboard row, raw streaming is silenced (full detail still hits the on-disk
// transcript) and ralph logs feed the row; otherwise both go to the operator's
// console as today.
func (r *run) sinks(row *issueRow) (sessionUI, console io.Writer) {
	if row != nil {
		return io.Discard, nil
	}
	return r.ui, r.ui
}

// worktreePath is where issue N's worktree lives. Default base is a per-repo
// dir under the OS temp dir, kept outside the repo so it needs no .gitignore
// entry and the agent never sees a nested checkout.
func worktreePath(cfg *config.Config, issueNum int) string {
	base := cfg.WorktreeDir
	if base == "" {
		base = filepath.Join(os.TempDir(), "ralph-worktrees", filepath.Base(cfg.ProjectRoot))
	}
	return filepath.Join(base, fmt.Sprintf("issue-%d", issueNum))
}

// log emits a timestamped, secret-redacted line. In dashboard mode it updates
// the issue's row (and the full line still lands in the on-disk transcript via
// the session); otherwise it prints to the console exactly like run.log.
func (ws *workspace) log(msg string) {
	msg = redactSecrets(msg)
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
	if ws.row != nil {
		ws.row.setMessage(msg)
	} else if ws.console != nil {
		fmt.Fprint(ws.console, line)
	}
	if ws.session != nil {
		_ = ws.session.WriteBanner(line)
	}
}

// section prints a banner to the console (or sets the row status) and mirrors it
// into the on-disk transcript.
func (ws *workspace) section(title string) {
	if ws.row != nil {
		ws.row.setStatus(title)
	} else if ws.console != nil {
		fmt.Fprint(ws.console, bannerText(title))
	}
	if ws.session != nil {
		_ = ws.session.WriteBanner(bannerText(title))
	}
}

// sectionConsoleOnly prints a banner to the console (or sets the row status) but
// NOT the on-disk transcript — used for the per-pass counter, which must stay
// out of the transcript a fresh pass could read back. See run.sectionConsoleOnly.
func (ws *workspace) sectionConsoleOnly(status string) {
	if ws.row != nil {
		ws.row.setStatus(status)
	} else if ws.console != nil {
		fmt.Fprint(ws.console, bannerText(status))
	}
}
