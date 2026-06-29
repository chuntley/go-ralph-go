package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chuntley/go-ralph-go/internal/config"
	"github.com/chuntley/go-ralph-go/internal/git"
)

// initRepoWithOriginOnMain makes a work repo pushed to a bare origin, left
// checked out on `main` with a clean tree — the starting state for an auto run.
func initRepoWithOriginOnMain(t *testing.T) string {
	t.Helper()
	bare := t.TempDir()
	mustGitCmd(t, bare, "init", "--bare", "-b", "main")
	dir := initRepoOnBranch(t, "main", "")
	mustGitCmd(t, dir, "remote", "add", "origin", bare)
	mustGitCmd(t, dir, "push", "-u", "origin", "main")
	return dir
}

// TestMakeWorkspaceWorktreeIsolation is the core guarantee behind parallel auto
// mode: a worktree workspace is a separate checkout on its own branch, and the
// repo root is never moved off its branch — so a developer can keep working in
// the root while ralph runs. Teardown removes the worktree dir.
func TestMakeWorkspaceWorktreeIsolation(t *testing.T) {
	ctx := context.Background()
	root := initRepoWithOriginOnMain(t)

	// Dirty the root tree on purpose: worktree mode must not care.
	if err := os.WriteFile(filepath.Join(root, "scratch.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults() // Worktrees = true
	cfg.ProjectRoot = root
	cfg.WorktreeDir = t.TempDir()
	r := &run{cfg: &cfg, ui: io.Discard}

	ws, err := r.makeWorkspace(ctx, 7, "Fix the thing", "main", nil)
	if err != nil {
		t.Fatalf("makeWorkspace: %v", err)
	}
	if !ws.isWorktree {
		t.Fatal("expected a worktree workspace by default")
	}
	if ws.branch != "ralph/issue-7-fix-the-thing" {
		t.Errorf("branch = %q", ws.branch)
	}
	if ws.dir == root {
		t.Fatal("worktree dir must not be the repo root")
	}
	if _, err := os.Stat(filepath.Join(ws.dir, "README.md")); err != nil {
		t.Errorf("worktree should be a real checkout: %v", err)
	}
	if br, _ := git.CurrentBranch(ctx, ws.dir); br != "ralph/issue-7-fix-the-thing" {
		t.Errorf("worktree HEAD = %q; want the issue branch", br)
	}
	// The root must still be on main with its dirty file intact.
	if br, _ := git.CurrentBranch(ctx, root); br != "main" {
		t.Errorf("repo root moved to %q; worktree mode must leave it on main", br)
	}
	if _, err := os.Stat(filepath.Join(root, "scratch.txt")); err != nil {
		t.Errorf("root working changes should be untouched: %v", err)
	}
	if ws.session == nil {
		t.Error("worktree workspace should have its own session")
	}

	ws.cleanup()
	if _, err := os.Stat(ws.dir); !os.IsNotExist(err) {
		t.Errorf("cleanup should remove the worktree dir (err=%v)", err)
	}
}

// TestMakeWorkspaceConcurrent creates many worktrees off one repo at once — the
// shape parallel auto mode produces. Without gitMu serialising the shared-repo
// plumbing, concurrent `git worktree add` / `fetch` race on ref locks and a
// large fraction fail; with it, all succeed. Run under -race it also guards the
// gitMu usage.
func TestMakeWorkspaceConcurrent(t *testing.T) {
	ctx := context.Background()
	root := initRepoWithOriginOnMain(t)

	cfg := config.Defaults()
	cfg.ProjectRoot = root
	cfg.WorktreeDir = t.TempDir()
	r := &run{cfg: &cfg, ui: io.Discard}

	const n = 12
	var wg sync.WaitGroup
	errs := make([]error, n)
	wss := make([]*workspace, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			wss[i], errs[i] = r.makeWorkspace(ctx, i+1, "concurrent", "main", nil)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("makeWorkspace #%d failed (concurrent worktree race?): %v", i+1, err)
			continue
		}
		if _, statErr := os.Stat(filepath.Join(wss[i].dir, "README.md")); statErr != nil {
			t.Errorf("worktree #%d is not a real checkout: %v", i+1, statErr)
		}
	}
	for _, ws := range wss {
		if ws != nil {
			ws.cleanup()
		}
	}
}

// TestMakeWorkspaceInPlace verifies that with worktrees disabled, makeWorkspace
// reproduces the original in-place behaviour: it creates the branch in the repo
// root, reuses the root, and is not flagged as a worktree.
func TestMakeWorkspaceInPlace(t *testing.T) {
	ctx := context.Background()
	root := initRepoWithOriginOnMain(t)

	cfg := config.Defaults()
	cfg.Worktrees = false
	cfg.ProjectRoot = root
	r := newRunForTest(t, root, "main", &fakeProvider{})
	r.cfg = &cfg

	ws, err := r.makeWorkspace(ctx, 9, "Do it", "main", nil)
	if err != nil {
		t.Fatalf("makeWorkspace: %v", err)
	}
	if ws.isWorktree {
		t.Error("expected an in-place workspace when worktrees=false")
	}
	if ws.dir != root {
		t.Errorf("in-place workspace dir = %q; want repo root", ws.dir)
	}
	if br, _ := git.CurrentBranch(ctx, root); br != "ralph/issue-9-do-it" {
		t.Errorf("in-place mode should check out the branch in the root; got %q", br)
	}
}
