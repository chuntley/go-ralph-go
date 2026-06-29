package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chuntley/go-ralph-go/internal/claude"
	"github.com/chuntley/go-ralph-go/internal/config"
	"github.com/chuntley/go-ralph-go/internal/vcs"
)

// commitSession is a fake agentSession that, on each Run, writes a unique file
// in its working directory and commits it — so a worktree actually accumulates
// the commits the real flow expects (PR fallback, merge). It needs no `claude`
// binary, letting the integration tests below drive the full orchestration.
type commitSession struct {
	dir string
	n   int
}

func (s *commitSession) Reset() error      { return nil }
func (s *commitSession) StartFresh() error { return nil }
func (s *commitSession) Run(ctx context.Context, _ string) error {
	s.n++
	f := filepath.Join(s.dir, fmt.Sprintf("work-%d.txt", s.n))
	if err := os.WriteFile(f, []byte(fmt.Sprintf("work %d\n", s.n)), 0o644); err != nil {
		return err
	}
	run := func(args ...string) error {
		c := exec.CommandContext(ctx, "git", args...)
		c.Dir = s.dir
		out, err := c.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %v: %v\n%s", args, err, out)
		}
		return nil
	}
	if err := run("add", "-A"); err != nil {
		return err
	}
	return run("commit", "-m", fmt.Sprintf("work %d", s.n))
}
func (s *commitSession) LastTurnText() string     { return "" }
func (s *commitSession) WriteBanner(string) error { return nil }
func (s *commitSession) Close() error             { return nil }

func commitSessionFactory() sessionFactory {
	return func(c claude.SessionConfig) (agentSession, error) {
		return &commitSession{dir: c.WorkDir}, nil
	}
}

func intRun(t *testing.T, root string, prov vcs.Provider) *run {
	t.Helper()
	cfg := config.Defaults()
	cfg.ProjectRoot = root
	cfg.DefaultBranch = "main"
	cfg.WorktreeDir = t.TempDir()
	cfg.MinIterations = 1
	cfg.Iterations = 1
	return &run{
		cfg:        &cfg,
		provider:   prov,
		ui:         io.Discard,
		newSession: commitSessionFactory(),
		labels:     vcs.Labels{Ready: "ready", Working: "ralph-working", Failed: "ralph-failed"},
	}
}

// TestMergeRepairPositive drives a full single-issue flow where the PR's checks
// fail once and then pass: ralph must run a repair pass (commit + force-push)
// and retry the merge, ending merged + resolved. Exercises handleCleanupPRWS's
// repair loop end-to-end against a real worktree + bare origin.
func TestMergeRepairPositive(t *testing.T) {
	root := initRepoWithOriginOnMain(t)
	prov := &fakeProvider{
		findPRResult: nil, // force ralph's push+open-PR fallback
		waitErrSeq:   []error{errors.New("checks failed: flaky build")},
		// after the sequence, waitErr is nil (default) → checks pass
	}
	r := intRun(t, root, prov)
	r.cfg.MergeRepairAttempts = 3

	if err := r.processIssueRow(context.Background(), 42, nil); err != nil {
		t.Fatalf("issue should end merged after one repair: %v", err)
	}
	if prov.createdPR == nil {
		t.Error("ralph should have opened the PR (cleanup fallback)")
	}
	if prov.resolved != 42 {
		t.Errorf("issue should be resolved after a successful repaired merge (resolved=%d)", prov.resolved)
	}
	if prov.waitCalls < 2 {
		t.Errorf("checks should have been waited on at least twice (fail then pass); got %d", prov.waitCalls)
	}
}

// parProvider is a concurrency-safe vcs.Provider for the parallel dispatcher
// tests: a ready queue plus per-PR merge control, all guarded by a mutex since
// worker goroutines call it concurrently.
type parProvider struct {
	mu          sync.Mutex
	ready       []vcs.Issue
	nextPR      int
	prToBranch  map[int]string
	failBranch  string       // a PR on this branch refuses to merge (forces a failure)
	closed      map[int]bool // GetIssue returns an error for these (closed/merged/PR)
	resolved    map[int]bool
	failed      map[int]string
	skipped     map[int]bool
	claimed     map[int]bool // MarkWorking-ed: NextReady stops returning these
	workingSeen map[int]bool
}

func newParProvider(n int) *parProvider {
	p := &parProvider{
		prToBranch:  map[int]string{},
		closed:      map[int]bool{},
		resolved:    map[int]bool{},
		failed:      map[int]string{},
		skipped:     map[int]bool{},
		claimed:     map[int]bool{},
		workingSeen: map[int]bool{},
	}
	for i := 1; i <= n; i++ {
		p.ready = append(p.ready, vcs.Issue{Number: i, Title: fmt.Sprintf("issue %d", i)})
	}
	return p
}

func (p *parProvider) Name() string                           { return "fake" }
func (p *parProvider) Whoami(context.Context) (string, error) { return "fake", nil }
func (p *parProvider) EnsureLabels(context.Context, vcs.Labels) error {
	return nil
}
func (p *parProvider) NextReady(context.Context, vcs.Labels) (*vcs.Issue, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Models GitHub: an issue keeps showing as ready until MarkWorking clears its
	// label. The dispatcher must claim (MarkWorking) it synchronously to advance.
	for _, iss := range p.ready {
		if !p.claimed[iss.Number] {
			i := iss
			return &i, nil
		}
	}
	return nil, vcs.ErrNoReadyIssue
}
func (p *parProvider) GetIssue(_ context.Context, n int) (*vcs.Issue, error) {
	p.mu.Lock()
	gone := p.closed[n]
	p.mu.Unlock()
	if gone {
		return nil, fmt.Errorf("issue #%d is closed — ralph only works open issues", n)
	}
	return &vcs.Issue{Number: n, Title: fmt.Sprintf("issue %d", n)}, nil
}
func (p *parProvider) MarkWorking(_ context.Context, n int, _ vcs.Labels) error {
	p.mu.Lock()
	p.claimed[n] = true
	p.workingSeen[n] = true
	p.mu.Unlock()
	return nil
}
func (p *parProvider) MarkFailed(_ context.Context, n int, _ vcs.Labels, reason string) error {
	p.mu.Lock()
	p.failed[n] = reason
	p.mu.Unlock()
	return nil
}
func (p *parProvider) MarkRequeued(context.Context, int, vcs.Labels) error { return nil }
func (p *parProvider) MarkSkipped(_ context.Context, n int, _ vcs.Labels, _ string) error {
	p.mu.Lock()
	p.skipped[n] = true
	p.mu.Unlock()
	return nil
}
func (p *parProvider) MarkResolved(_ context.Context, n int, _ vcs.Labels) error {
	p.mu.Lock()
	p.resolved[n] = true
	p.mu.Unlock()
	return nil
}
func (p *parProvider) FindPRForBranch(context.Context, string) (*vcs.PR, error) {
	return nil, nil // always force the push + open-PR fallback
}
func (p *parProvider) CreatePR(_ context.Context, headBranch, _, _, _ string) (*vcs.PR, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextPR++
	p.prToBranch[p.nextPR] = headBranch
	return &vcs.PR{Number: p.nextPR, Branch: headBranch}, nil
}
func (p *parProvider) WaitForChecks(context.Context, int, time.Duration) error { return nil }
func (p *parProvider) SquashMergeAndDelete(_ context.Context, pr int) error {
	p.mu.Lock()
	branch := p.prToBranch[pr]
	fail := p.failBranch != "" && branch == p.failBranch
	p.mu.Unlock()
	if fail {
		return errors.New("merge refused: not mergeable (conflict)")
	}
	return nil
}

// TestRunAutoParallelSkipsClosedIssue reproduces the spurious-"failed" bug: an
// issue that is no longer actionable (already closed/merged, e.g. re-dispatched
// after it shipped) must be SKIPPED — not counted as failed and not marked
// ralph-failed — while the other issues still merge.
func TestRunAutoParallelSkipsClosedIssue(t *testing.T) {
	const n = 3
	const goneN = 2
	root := initRepoWithOriginOnMain(t)
	prov := newParProvider(n)
	prov.closed[goneN] = true // GetIssue(#2) will report it closed
	r := intRun(t, root, prov)
	r.cfg.MaxParallel = 2
	r.cfg.PollInterval = 1

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.runAutoParallel(ctx, false) }()

	deadline := time.Now().Add(30 * time.Second)
	for {
		if res, _ := prov.counts(); res >= n-1 { // the two open issues merge
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("timed out waiting for the open issues to merge")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done

	res, failed := prov.counts()
	if failed != 0 {
		t.Errorf("a closed/not-actionable issue must NOT count as failed; failed=%d", failed)
	}
	if res != n-1 {
		t.Errorf("the other %d issues should merge; resolved=%d", n-1, res)
	}
	prov.mu.Lock()
	_, wronglyFailed := prov.failed[goneN]
	prov.mu.Unlock()
	if wronglyFailed {
		t.Errorf("closed issue #%d must not be marked ralph-failed", goneN)
	}
}

func (p *parProvider) counts() (resolved, failed int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.resolved), len(p.failed)
}

// TestRunAutoParallelMergesAll runs several issues through the parallel
// dispatcher, two at a time, each in its own worktree, and confirms they all
// merge and resolve. This is the core happy-path coverage of runAutoParallel.
func TestRunAutoParallelMergesAll(t *testing.T) {
	const n = 5
	root := initRepoWithOriginOnMain(t)
	prov := newParProvider(n)
	r := intRun(t, root, prov)
	r.cfg.MaxParallel = 2
	r.cfg.PollInterval = 1 // fast re-poll once the queue drains

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.runAutoParallel(ctx, false) }()

	// Wait until everything resolved, then cancel to stop the poller.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if res, _ := prov.counts(); res >= n {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("timed out; only %d/%d resolved", func() int { r, _ := prov.counts(); return r }(), n)
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done

	res, failed := prov.counts()
	if res != n {
		t.Errorf("resolved = %d; want %d", res, n)
	}
	if failed != 0 {
		t.Errorf("failed = %d; want 0", failed)
	}
	if r.completed != n {
		t.Errorf("completed counter = %d; want %d", r.completed, n)
	}
}

// TestRunAutoParallelKeepsGoingOnFailure confirms that when one issue can't be
// merged (and repair is disabled) the dispatcher marks it failed but KEEPS
// working the rest of the queue — it does not halt — and the other issues still
// merge. The run is stopped by cancelling once everything has been processed.
func TestRunAutoParallelKeepsGoingOnFailure(t *testing.T) {
	const n = 3
	const failN = 2
	root := initRepoWithOriginOnMain(t)
	prov := newParProvider(n)
	prov.failBranch = issueBranchName("ralph", failN, fmt.Sprintf("issue %d", failN))
	r := intRun(t, root, prov)
	r.cfg.MaxParallel = 2
	r.cfg.PollInterval = 1
	r.cfg.MergeRepairAttempts = 0 // a refused merge fails immediately

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.runAutoParallel(ctx, false) }()

	deadline := time.Now().Add(30 * time.Second)
	for {
		res, failed := prov.counts()
		if res+failed >= n {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("timed out; resolved=%d failed=%d", res, failed)
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done // must return (cancelled), not hang

	res, failed := prov.counts()
	if failed != 1 {
		t.Errorf("failed = %d; want exactly the one bad issue", failed)
	}
	if res != n-1 {
		t.Errorf("the other %d issues should still merge; resolved=%d", n-1, res)
	}
	prov.mu.Lock()
	_, markedFailed := prov.failed[failN]
	prov.mu.Unlock()
	if !markedFailed {
		t.Errorf("issue #%d should be the one marked failed", failN)
	}
}
