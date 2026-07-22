package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const ConfigFileName = ".go-ralph-go"

// MaxIterations caps how many refine passes ralph will run in a single cycle.
// Without this, a stray --iterations 5000 would burn money for a day.
const MaxIterations = 50

// Config is the merged effective configuration for a ralph run.
//
// All fields have sensible defaults so a project may ship with no
// .go-ralph-go file at all. Defaults are filled in by Load() after parsing.
type Config struct {
	// ProjectRoot is the discovered project root (directory containing
	// .go-ralph-go, or the git root, or cwd). Always absolute. Not
	// settable from the file.
	ProjectRoot string `toml:"-"`

	// ConfigPath is the path to the .go-ralph-go file that was loaded,
	// or "" if no file was found and defaults are in use.
	ConfigPath string `toml:"-"`

	// MinIterations is the minimum number of refine passes the loop runs before
	// it will honour a completion signal. Completion before this floor is
	// ignored and the loop keeps refining — this guarantees the work gets a
	// baseline number of critical re-audits instead of stopping the moment
	// Claude first thinks it's done. Enforced silently by the harness; Claude is
	// never told the floor. Default: 5. Must be >= 1; if it exceeds Iterations
	// it is clamped down to Iterations (a low cap implies a low floor).
	MinIterations int `toml:"min_iterations"`

	// Iterations is the MAXIMUM number of refine passes before ralph forces the
	// cleanup pass. The loop is goal-driven: once MinIterations is reached it
	// ends as soon as Claude signals the goal is complete (see
	// CompletionSentinel). This is the upper safety cap against runaway cost.
	// Default: 10. Must be >= 1 and <= MaxIterations; if it is below
	// MinIterations, the minimum is clamped down to match.
	Iterations int `toml:"iterations"`

	// CompletionSentinel is the exact line Claude is told to emit, on its own
	// line, once the goal is fully met and verified. The runner treats it as a
	// request to end the loop — confirmed by VerifyCommand when one is set.
	// Default: "RALPH_GOAL_COMPLETE". Override only if it could collide with
	// your task's actual output.
	CompletionSentinel string `toml:"completion_sentinel"`

	// VerifyCommand, if set, is the harness-owned completion gate: when Claude
	// emits the sentinel, ralph runs this command (via `sh -c`) in the project
	// root and only ends the loop if it exits 0. This is the robust defense
	// against premature "done" / reward-hacking — a self-emitted token is the
	// weakest possible oracle, so the real build/test suite gets the final say.
	// Empty (default) means the token is trusted as-is, with a logged caveat.
	// Example: "go test ./... && go build ./... && golangci-lint run".
	VerifyCommand string `toml:"verify_command"`

	// InstructionsDoc is the project doc Claude is told to follow during
	// cleanup (e.g. "AGENTS.md", "CLAUDE.md"). Default: "AGENTS.md".
	InstructionsDoc string `toml:"instructions_doc"`

	// RefinePrompt is the goal-driven loop prompt appended to the work prompt
	// every pass. Placeholders: {{plan_file}} (path to the durable plan file),
	// {{sentinel}} (the CompletionSentinel line), and {{verify}} (a clause
	// derived from VerifyCommand telling Claude how completion is checked).
	RefinePrompt string `toml:"refine_prompt"`

	// CleanupPrompt is the cleanup-pass prompt used in PR-opening modes.
	// {{instructions_doc}}, {{issue_clause}}, {{closes_clause}} are substituted.
	CleanupPrompt string `toml:"cleanup_prompt"`

	// IssuePrompt is the work-prompt template for issue/auto modes. Available
	// placeholders: {{provider}}, {{number}}, {{title}}, {{body}}.
	IssuePrompt string `toml:"issue_prompt"`

	// DefaultBranch overrides the auto-detected default branch (main, master,
	// develop, trunk, ...). Empty means "auto-detect".
	DefaultBranch string `toml:"default_branch"`

	// BranchPrefix namespaces the per-task working branch ralph creates and
	// checks out BEFORE the refine loop in PR-opening modes (issue, auto, and
	// `run --pr`). With the default "ralph", an issue branch is
	// "ralph/issue-<N>-<slug>" and an ad-hoc --pr run is "ralph/run-<timestamp>".
	//
	// This is the guardrail against committing straight to the protected default
	// branch: because ralph starts the loop on a feature branch, every commit
	// Claude makes — and any push — lands on that branch, never on the default
	// branch, regardless of what the project's instructions doc says. Set to ""
	// to drop the namespace (branches become "issue-<N>-<slug>").
	BranchPrefix string `toml:"branch_prefix"`

	// OutputDir is where ralph-output.{txt,jsonl,stderr} are written,
	// relative to ProjectRoot. Default: ".ralph".
	OutputDir string `toml:"output_dir"`

	// ClaudeBin is the claude CLI to invoke. Default: "claude".
	ClaudeBin string `toml:"claude_bin"`

	// ClaudeConfigDir, if set, is exported as CLAUDE_CONFIG_DIR so claude uses
	// a project-local config directory instead of your system-wide one.
	// When empty (the default), ralph lets claude use its system-wide config
	// — which is almost always what you want, since system claude already
	// holds your OAuth/login.
	//
	// Only set this when you have a fully-provisioned project-local
	// .claude/ directory (credentials.json + settings + agents). A directory
	// that exists only to hold ralph's memory files (.claude/projects/) is
	// NOT a valid CLAUDE_CONFIG_DIR — pointing claude at it produces a
	// "Not logged in" failure.
	//
	// Set to ".claude" (or any relative/absolute path) in .go-ralph-go to
	// opt in.
	ClaudeConfigDir string `toml:"claude_config_dir"`

	// PollInterval is the seconds between polls in auto mode. Default: 60.
	// Validate() enforces a minimum of 30 to stay within host rate limits.
	PollInterval int `toml:"poll_interval"`

	// Worktrees controls per-issue git worktree isolation (default: true). When
	// on, each issue is worked in its own `git worktree` (a separate checkout
	// cut from up-to-date upstream) instead of in-place in the repo root. The
	// root checkout is never touched — so a developer can keep working in it
	// (even with a dirty tree) while ralph works issues in parallel worktrees.
	// This is the prerequisite for MaxParallel > 1. Set to false to restore the
	// original in-place behaviour (work the branch directly in the repo root).
	Worktrees bool `toml:"worktrees"`

	// WorktreeDir is the base directory under which per-issue worktrees are
	// created (one subdir per issue). Empty (default) means a per-repo dir under
	// the OS temp dir. Kept OUTSIDE the repo so worktrees never need a
	// .gitignore entry and the agent never sees a nested checkout. Only used
	// when Worktrees is true.
	WorktreeDir string `toml:"worktree_dir"`

	// MaxParallel is how many issues auto mode may work concurrently, each in
	// its own worktree. Default: 3. Parallelism requires Worktrees (concurrent
	// issues cannot share one checkout), so Validate clamps MaxParallel to 1
	// whenever Worktrees is off. When MaxParallel > 1, auto mode renders a
	// compact live status dashboard instead of streaming one issue's raw output.
	// Set to 1 for sequential mode with full streaming output.
	MaxParallel int `toml:"max_parallel"`

	// MergeRepairAttempts caps how many times ralph will, after a PR fails its
	// checks or refuses to merge (broken tests, merge conflicts), feed the
	// failure back to Claude as a repair pass — rebasing onto the updated
	// default branch, fixing the failure, re-pushing — before retrying the
	// merge. This is what lets `ralph auto` keep working an issue until it
	// actually merges rather than giving up on the first red check. Default: 3.
	// 0 disables repair (fail on the first check/merge failure, pre-repair
	// behaviour).
	MergeRepairAttempts int `toml:"merge_repair_attempts"`

	// PassTimeout optionally caps the wall-clock time a single refine pass may
	// run, as a Go duration string (e.g. "45m"). Empty (default) = no cap. When a
	// pass exceeds it, ralph cancels the claude subprocess (graceful SIGINT, then
	// SIGKILL after a short grace) and — exactly like a crashed pass — retries it
	// in place on the same worktree (see PassRetries), so committed work is
	// preserved. Parsed into PassTimeoutDur by Validate.
	PassTimeout string `toml:"pass_timeout"`

	// PassTimeoutDur is PassTimeout parsed to a duration. Runtime-only (0 = no
	// cap); populated by Validate, never read from the file.
	PassTimeoutDur time.Duration `toml:"-"`

	// PassRetries caps how many times a crashed OR timed-out refine pass is
	// retried in place, against the same worktree, before the issue is marked
	// failed. The worktree keeps every committed pass, so only the failed
	// attempt's own uncommitted work is redone. A crash here means the claude
	// subprocess exited non-zero (e.g. OOM/SIGTERM) or hit PassTimeout — not a
	// Ctrl+C or an auth failure, neither of which is ever retried. Default: 2.
	// 0 = fail on the first crash/timeout (previous behaviour). Distinct from
	// MergeRepairAttempts, which governs the post-PR checks/merge phase.
	PassRetries int `toml:"pass_retries"`

	// GitHub holds GitHub-specific overrides.
	GitHub GitHubConfig `toml:"github"`
}

type GitHubConfig struct {
	// Owner / Repo override the auto-detected origin remote. Usually empty.
	Owner string `toml:"owner"`
	Repo  string `toml:"repo"`

	// BaseURL points at a GitHub Enterprise install (e.g.
	// "https://ghe.example.com/api/v3/"). Empty means github.com.
	BaseURL string `toml:"base_url"`

	// Labels overrides the state-machine label names.
	Labels LabelConfig `toml:"labels"`

	// CheckIntervalSeconds is the gh-equivalent of `gh pr checks --interval`.
	// Default: 30.
	CheckIntervalSeconds int `toml:"check_interval_seconds"`
}

type LabelConfig struct {
	Ready   string `toml:"ready"`   // default: "ready"
	Working string `toml:"working"` // default: "ralph-working"
	Failed  string `toml:"failed"`  // default: "ralph-failed"
}

// Defaults returns a Config populated with the script's original behaviour.
func Defaults() Config {
	return Config{
		MinIterations:      5,
		Iterations:         10,
		CompletionSentinel: "RALPH_GOAL_COMPLETE",
		InstructionsDoc:    "AGENTS.md",
		// Goal-driven loop. The work prompt is the GOAL; this prompt drives toward
		// it. The runner runs it repeatedly with a FRESH context each time (a new
		// session, never --resume) until the goal verifies — but the prompt is
		// deliberately written as a single-shot task with NO mention of passes,
		// loops, or iteration. That, plus the fresh context, is what stops Claude
		// from pacing itself ("I'll finish the rest next time") or rubber-stamping
		// code it remembers writing. Durable state crosses runs via the plan file
		// on disk ({{plan_file}}) and git — never the conversation. Completion is
		// the harness's decision, signaled only by {{sentinel}}. See README
		// ("The refine loop") for the reasoning behind each step.
		RefinePrompt: `You are implementing the goal described above to a correct, complete, and verified state. Work from what you can see right now — the code, ` + "`git log`" + `, and the plan file at {{plan_file}}, which together hold the current state of the work.

ORIENT: Read {{plan_file}} and skim recent ` + "`git log`" + ` to understand where things stand. If the plan file does not exist yet, create it with these sections:
  GOAL — the goal restated in one paragraph.
  VERIFY — the exact command(s) that prove the work correct (build + tests + lint).
  ACCEPTANCE — concrete, checkable criteria, each with its current status and the evidence (real command output) backing it.
  FINDINGS — known problems that still need fixing (bugs, weak or missing tests, rework); remove an item only once it is actually fixed and verified.
  OUT OF SCOPE — explicit non-goals, to fight scope creep.
Rely only on these and the code itself — do not read ralph's own run logs under the output directory.

AUDIT: Review the current code against the goal as a skeptical reviewer who did NOT write it. Assume mistakes exist and that finding them is your job; never trust that something is correct just because the plan or a comment claims so — confirm it against the code. Scrutinize three things specifically:
  - Correctness — does the code actually satisfy the goal across edge cases, error paths, and unusual inputs?
  - The tests themselves — re-derive each expected value from the goal (not from the current code) and confirm the tests assert THAT; check they exercise the real required behavior rather than the implementation, and that negative / boundary / failure cases aren't missing. A green suite is NOT proof: a test can assert the wrong thing, be tautological, or be incomplete.
  - The approach — could the solution be reworked to be simpler, clearer, or more robust, and is the acceptance set itself correct and complete?
Record every problem you find under FINDINGS, ordered by impact: correctness > security > edge cases > test coverage > performance > style.

WORK: Fully drive the goal to a correct, complete state now. Implement everything the goal requires that is missing or wrong, and fix every item in FINDINGS — do not stop at a single change and do not leave straightforward work unfinished. (Only if the goal is genuinely too large to finish coherently in one sitting, complete as much as you can fully verify and describe the rest precisely under FINDINGS.)

VERIFY: {{verify}} Show the command and its real output — do not assert success. A criterion is satisfied only when its check actually passes. Never edit, delete, weaken, skip, hard-code around, or add skip/xfail to a test to make it pass — fix the underlying code. If a test is genuinely wrong, leave it in place and record it under FINDINGS.

COMMIT: Commit the finished work with a clear message, and update {{plan_file}} — refresh the ACCEPTANCE statuses (with their evidence) and the FINDINGS list. Never record the goal itself as "complete", "done", or "finished" in the plan; that status is determined separately, not by the plan's contents.

SCOPE: Do not add features, abstractions, or defensive code beyond the goal, and do not refactor code unrelated to what you are fixing.

COMPLETION: A passing VERIFY command is necessary but NOT sufficient — green tests do not prove correctness (they can assert the wrong values, test the implementation instead of the requirement, or omit cases entirely). Before signaling completion, do a final skeptical review of the ENTIRE change against the base branch (e.g. ` + "`git diff`" + `) as a reviewer who did NOT write it, and confirm ALL of: (1) you independently re-derived each acceptance criterion from the goal and the tests genuinely assert it; (2) the tests cover the real behavior plus its edge and failure cases; (3) no MATERIAL rework remains — that is, nothing left would change correctness, complete a requirement the goal actually asks for, fix an invalid or missing test, or harden the code against realistic inputs; (4) the VERIFY command passes with its real output shown above. The completion bar is "correct, complete, and verified against the goal" — NOT "perfect" or "nothing left to polish." A codebase can always be improved further; that is not a reason to withhold completion. If all four hold, output the line {{sentinel}} alone on its own line with nothing after it, even if minor non-material items remain. Handle remaining items by kind: fix anything MATERIAL now and do NOT signal completion; for non-material polish (cosmetic style, naming preferences, optional refactors, speculative hardening for inputs that cannot occur, nice-to-haves beyond the goal), record it under FINDINGS and still signal completion — do not fix it and do not let it block the sentinel. Only genuine uncertainty about a MATERIAL point should withhold the sentinel; resolve it, then signal.`,
		CleanupPrompt:       "The Ralph loop just exited{{issue_clause}}. This is a single, non-interactive cleanup turn: there is no later turn, and any process you start in the background is killed the moment this turn ends. Do everything now, in the foreground, and finish before you end this response. Per {{instructions_doc}}, commit anything still outstanding, push the branch, and open a PR. Do NOT run verification (tests, builds, lint) or any other work in the background, and do NOT defer the push until after something else finishes — if you want to run checks first, run them synchronously now, then push and open the PR in this same response.{{closes_clause}}",
		IssuePrompt:         "Work on {{provider}} issue #{{number}}. Read the title and body carefully; if your host CLI is available (`gh` or `glab`), also read the issue comments — they may contain crucial follow-up context.\n\nTitle: {{title}}\n\nBody:\n{{body}}",
		OutputDir:           ".ralph",
		ClaudeBin:           "claude",
		BranchPrefix:        "ralph",
		PollInterval:        60,
		Worktrees:           true,
		MaxParallel:         3,
		MergeRepairAttempts: 3,
		PassRetries:         2,
		GitHub: GitHubConfig{
			Labels: LabelConfig{
				Ready:   "ready",
				Working: "ralph-working",
				Failed:  "ralph-failed",
			},
			CheckIntervalSeconds: 30,
		},
	}
}

// Validate normalises and checks the merged config. It is called by Load
// after merging file contents on top of Defaults.
func (c *Config) Validate() error {
	if c.MinIterations < 1 {
		return fmt.Errorf("min_iterations must be >= 1 (got %d)", c.MinIterations)
	}
	if c.Iterations < 1 {
		return fmt.Errorf("iterations must be >= 1 (got %d)", c.Iterations)
	}
	if c.Iterations > MaxIterations {
		return fmt.Errorf("iterations must be <= %d (got %d) — guard against runaway cost; raise MaxIterations if you really mean this", MaxIterations, c.Iterations)
	}
	// The floor can't exceed the cap. Rather than erroring (which would make a
	// low `-n`/`iterations` cap collide with the default min of 5), clamp the
	// minimum down — "cap at N passes" implies a floor of at most N. The startup
	// banner prints the effective min/max so the clamp is visible.
	if c.MinIterations > c.Iterations {
		c.MinIterations = c.Iterations
	}
	if c.PollInterval < 30 {
		return fmt.Errorf("poll_interval must be >= 30 seconds (got %d) — host rate limits", c.PollInterval)
	}
	if c.MaxParallel < 1 {
		return fmt.Errorf("max_parallel must be >= 1 (got %d)", c.MaxParallel)
	}
	// Worktrees is the master switch for parallelism: concurrent issues each need
	// their own checkout, so without worktrees we can only run one at a time.
	// Clamp rather than error so disabling worktrees (or --no-worktree) silently
	// falls back to sequential instead of forcing the user to also zero this out.
	if !c.Worktrees && c.MaxParallel > 1 {
		c.MaxParallel = 1
	}
	if c.MergeRepairAttempts < 0 {
		return fmt.Errorf("merge_repair_attempts must be >= 0 (got %d)", c.MergeRepairAttempts)
	}
	if c.PassRetries < 0 {
		return fmt.Errorf("pass_retries must be >= 0 (got %d)", c.PassRetries)
	}
	// pass_timeout is an optional Go duration string; parse it once here into the
	// runtime-only PassTimeoutDur so the hot path never re-parses. Empty = no cap.
	c.PassTimeoutDur = 0
	if s := strings.TrimSpace(c.PassTimeout); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("pass_timeout %q is not a valid duration (e.g. \"45m\"): %w", c.PassTimeout, err)
		}
		if d <= 0 {
			return fmt.Errorf("pass_timeout must be a positive duration (got %q)", c.PassTimeout)
		}
		c.PassTimeoutDur = d
	}
	return nil
}

// Load discovers and parses .go-ralph-go starting from the current working
// directory and walking upward. If no config file is found, defaults are
// returned with ProjectRoot set to the git root (if any) or cwd.
func Load() (*Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	cfg := Defaults()
	cfgPath, found := findConfigFile(cwd)
	if found {
		cfg.ConfigPath = cfgPath
		cfg.ProjectRoot = filepath.Dir(cfgPath)
		if err := mergeFromFile(&cfg, cfgPath); err != nil {
			return nil, fmt.Errorf("parse %s: %w", cfgPath, err)
		}
	} else {
		// No config file — fall back to git root or cwd.
		cfg.ProjectRoot = discoverGitRoot(cwd)
	}

	// Resolve CLAUDE_CONFIG_DIR. Only honour an explicit setting in
	// .go-ralph-go — never auto-detect a .claude/ directory. Auto-detection
	// turned out to be a footgun: a memory-only .claude/ would override the
	// system-wide claude and produce a "Not logged in" failure.
	if cfg.ClaudeConfigDir != "" && !filepath.IsAbs(cfg.ClaudeConfigDir) {
		cfg.ClaudeConfigDir = filepath.Join(cfg.ProjectRoot, cfg.ClaudeConfigDir)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// EffectiveClaudeConfigDir returns the config directory the claude CLI will
// actually use for a run — which is NOT always what ralph configures:
//
//  1. an explicit claude_config_dir in .go-ralph-go (already absolutised), else
//  2. an inherited CLAUDE_CONFIG_DIR environment variable — relative values are
//     resolved against ProjectRoot, matching how claude resolves them since
//     ralph runs claude with cwd = ProjectRoot, else
//  3. "" meaning claude's system default (~/.claude).
//
// Surfacing this (rather than just ClaudeConfigDir) is what lets the banner and
// doctor show the real account/plan a run will bill against.
func (c *Config) EffectiveClaudeConfigDir() string {
	if c.ClaudeConfigDir != "" {
		return c.ClaudeConfigDir
	}
	env := os.Getenv("CLAUDE_CONFIG_DIR")
	if env == "" {
		return ""
	}
	if !filepath.IsAbs(env) {
		return filepath.Join(c.ProjectRoot, env)
	}
	return env
}

func findConfigFile(start string) (string, bool) {
	dir := start
	for {
		candidate := filepath.Join(dir, ConfigFileName)
		if isFile(candidate) {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func discoverGitRoot(start string) string {
	dir := start
	for {
		if isDir(filepath.Join(dir, ".git")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

func mergeFromFile(cfg *Config, path string) error {
	_, err := toml.DecodeFile(path, cfg)
	return err
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// WriteStarter writes a starter .go-ralph-go file in the current directory
// and, if a .gitignore exists or none is present, ensures ".ralph/" is
// listed so the run output never accidentally lands in version control.
func WriteStarter(force bool) error {
	target := ConfigFileName
	if _, err := os.Stat(target); err == nil && !force {
		return errors.New(".go-ralph-go already exists (pass --force to overwrite)")
	}
	if err := os.WriteFile(target, []byte(starterTOML), 0o644); err != nil {
		return err
	}
	return ensureGitignore(".gitignore", ".ralph/")
}

// ensureGitignore appends entry to gitignorePath if not already present.
// Creates the file if missing.
func ensureGitignore(gitignorePath, entry string) error {
	existing, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range splitLines(string(existing)) {
		if line == entry {
			return nil
		}
	}
	out := string(existing)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out += "\n"
	}
	out += entry + "\n"
	return os.WriteFile(gitignorePath, []byte(out), 0o644)
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

const starterTOML = `# go-ralph-go project config — defaults shown, uncomment to override.
# Run "ralph doctor" any time to verify ralph picked up this file and your
# environment is wired correctly.

# min_iterations   = 5            # MINIMUM refine passes before a completion
#                                 # signal is honoured. The loop always runs at
#                                 # least this many passes (early "done" signals
#                                 # are ignored) so the work gets real re-audits.
# iterations       = 10           # MAXIMUM refine passes (safety cap, max 50).
#                                 # Once the minimum is reached, the loop ends as
#                                 # soon as the goal verifies complete.
# instructions_doc = "AGENTS.md"  # doc Claude is told to follow during cleanup
# output_dir       = ".ralph"     # run logs + plan.md go here (gitignored)
# claude_bin       = "claude"
# poll_interval    = 60           # auto-mode poll seconds (min 30)
# default_branch   = ""           # override auto-detected default branch

# worktrees        = true         # DEFAULT. Work each issue in its OWN git
#                                 # worktree (an isolated checkout cut from
#                                 # up-to-date upstream). The repo root is never
#                                 # touched — keep working in it (even dirty)
#                                 # while ralph runs. Set false for the original
#                                 # in-place behaviour (works the branch in the
#                                 # repo root; requires a clean tree).
# worktree_dir     = ""           # base dir for per-issue worktrees; empty =
#                                 # a per-repo dir under the OS temp dir. Kept
#                                 # OUTSIDE the repo. Only used when worktrees=true.
# max_parallel     = 3            # how many issues "ralph auto" works at once,
#                                 # each in its own worktree (DEFAULT 3). >1 shows
#                                 # a compact live status dashboard; set 1 for
#                                 # sequential mode with full streaming output.
#                                 # Clamped to 1 automatically when worktrees=false.
# merge_repair_attempts = 3       # after a PR fails checks or refuses to merge
#                                 # (broken tests / conflicts), how many times to
#                                 # feed the failure back to Claude (rebase, fix,
#                                 # re-push) before retrying the merge. 0 disables.
# pass_timeout     = ""           # optional wall-clock cap per refine pass, as a
#                                 # Go duration (e.g. "45m"); "" = no cap. On a
#                                 # timeout the claude subprocess is cancelled and
#                                 # the pass is retried in place (see pass_retries),
#                                 # preserving every committed pass on the worktree.
# pass_retries     = 2            # how many times a crashed OR timed-out refine
#                                 # pass is retried in place on the same worktree
#                                 # before the issue is marked failed (DEFAULT 2).
#                                 # 0 = fail on the first crash/timeout. Ctrl+C and
#                                 # auth failures are never retried.
# branch_prefix    = "ralph"      # namespace for the per-task branch ralph
#                                 # creates before the loop in PR-opening modes
#                                 # (issue/auto/run --pr): "ralph/issue-<N>-<slug>".
#                                 # This guardrail keeps every commit + push off
#                                 # the protected default branch. "" drops the
#                                 # namespace ("issue-<N>-<slug>").

# completion_sentinel = "RALPH_GOAL_COMPLETE"  # line Claude emits when the goal
#                                              # is done; ends the loop.

# verify_command — the harness-owned completion gate. When set, ralph runs this
# (via sh -c) in the project root after Claude signals completion and only ends
# the loop if it exits 0. This is the robust defense against premature "done" /
# reward-hacking: the real test suite, run by ralph, gets the final say. Highly
# recommended. Empty (default) = trust the signal as-is, with a logged caveat.
#
# Best practice: point this at a SINGLE source of truth your CI also runs (a
# "make verify", "bun run verify", "./scripts/verify.sh", etc.) rather than
# spelling the steps out here — otherwise this gate silently drifts from what
# the project actually checks. Define the checklist once in the repo; reference
# it from both CI and ralph.
# verify_command = "make verify"     # or "bun run verify", "./scripts/verify.sh"

# Opt in to a project-local CLAUDE_CONFIG_DIR. Empty (default) = use the
# system-wide claude install — which is almost always what you want.
# Only set this if you have a fully-provisioned project ./.claude with
# credentials.json (not just memory files).
# claude_config_dir = ".claude"

# refine_prompt — the goal-driven loop prompt, appended to the work prompt
# every pass. Placeholders: {{plan_file}}, {{sentinel}}, {{verify}}.
#
# The default treats the work prompt as a GOAL and drives toward it over as many
# passes as it takes, keeping durable state in a plan file on disk (not in the
# conversation, and not an iteration counter Claude can see). Each pass orients
# from the plan, audits, makes verified progress, commits, and updates the plan;
# the loop ends only when Claude emits {{sentinel}} and the goal verifies. See
# the README ("The refine loop") for the full default and the reasoning. Override
# here only if your project needs different structure or scope rules.

# issue_prompt = """
# Work on {{provider}} issue #{{number}}. Read the title and body carefully;
# if your host CLI is available (gh or glab), also read the issue comments.
#
# Title: {{title}}
#
# Body:
# {{body}}
# """

# cleanup_prompt = """
# The Ralph loop just exited{{issue_clause}}. This is a single, non-interactive
# cleanup turn — there is no later turn and background tasks are killed when it
# ends. Do everything now, in the foreground: per {{instructions_doc}}, commit
# anything outstanding, push the branch, and open a PR before ending this
# response. Do not run verification in the background or defer the push.{{closes_clause}}
# """

# [github]
# owner = ""
# repo  = ""
# base_url = ""                   # GitHub Enterprise: "https://ghe.example.com/api/v3/"
# check_interval_seconds = 30

# [github.labels]
# ready   = "ready"
# working = "ralph-working"
# failed  = "ralph-failed"
`
