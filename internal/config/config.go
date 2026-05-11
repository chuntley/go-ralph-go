package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

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

	// Iterations is the number of refine iterations before the cleanup
	// pass. Script default: 5.
	Iterations int `toml:"iterations"`

	// InstructionsDoc is the project doc Claude is told to follow during
	// cleanup (e.g. "AGENTS.md", "CLAUDE.md"). Default: "AGENTS.md".
	InstructionsDoc string `toml:"instructions_doc"`

	// RefinePrompt is appended to the work prompt every iteration.
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
	Ready   string `toml:"ready"`    // default: "ready"
	Working string `toml:"working"`  // default: "ralph-working"
	Failed  string `toml:"failed"`   // default: "ralph-failed"
}

// Defaults returns a Config populated with the script's original behaviour.
func Defaults() Config {
	return Config{
		Iterations:      5,
		InstructionsDoc: "AGENTS.md",
		// Self-Refine-style: explicit audit step before refinement; bounded
		// single-fix scope; explicit anti-early-exit guard because LLMs bias
		// toward premature "looks done" verdicts. Iteration-aware via
		// {{iter}}/{{total}}. See README ("How the refine loop works") for
		// the reasoning behind each line.
		RefinePrompt: `This is iteration {{iter}} of {{total}}. The loop will run all {{total}} iterations regardless — do not declare the work complete or skip the fix step. Late iterations exist specifically to catch issues that look subtle on early passes.

AUDIT: Critically review the current state. List the 1-3 most significant remaining issues, ordered by impact: correctness > security > edge cases > test coverage > performance > style. Be specific — name files, functions, line numbers.

FIX: Address the single highest-priority issue from your audit this iteration. Make the focused change and run any relevant tests or linters.

Scope: do not add features outside the original task. Do not refactor outside the issue you're fixing this iteration. One concrete improvement per pass.`,
		CleanupPrompt:   "The Ralph loop just exited{{issue_clause}}. Perform post-loop cleanup per {{instructions_doc}}: push the branch and open a PR.{{closes_clause}}",
		IssuePrompt:     "Work on {{provider}} issue #{{number}}. Read the title and body carefully; if your host CLI is available (`gh` or `glab`), also read the issue comments — they may contain crucial follow-up context.\n\nTitle: {{title}}\n\nBody:\n{{body}}",
		OutputDir:       ".ralph",
		ClaudeBin:       "claude",
		PollInterval:    60,
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
	if c.Iterations < 1 {
		return fmt.Errorf("iterations must be >= 1 (got %d)", c.Iterations)
	}
	if c.Iterations > MaxIterations {
		return fmt.Errorf("iterations must be <= %d (got %d) — guard against runaway cost; raise MaxIterations if you really mean this", MaxIterations, c.Iterations)
	}
	if c.PollInterval < 30 {
		return fmt.Errorf("poll_interval must be >= 30 seconds (got %d) — host rate limits", c.PollInterval)
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

# iterations       = 5            # refine passes before cleanup (max 50)
# instructions_doc = "AGENTS.md"  # doc Claude is told to follow during cleanup
# output_dir       = ".ralph"     # run logs go here (gitignored)
# claude_bin       = "claude"
# poll_interval    = 60           # auto-mode poll seconds (min 30)
# default_branch   = ""           # override auto-detected default branch

# Opt in to a project-local CLAUDE_CONFIG_DIR. Empty (default) = use the
# system-wide claude install — which is almost always what you want.
# Only set this if you have a fully-provisioned project ./.claude with
# credentials.json (not just memory files).
# claude_config_dir = ".claude"

# refine_prompt — runs every iteration after the work prompt.
# Placeholders: {{iter}}, {{total}}.
#
# The default uses a Self-Refine pattern (audit -> single-fix per pass) with
# an explicit anti-early-exit clause because LLMs bias toward premature
# "looks done" verdicts. The loop intentionally runs all N iterations.
#
# refine_prompt = """
# This is iteration {{iter}} of {{total}}. The loop will run all {{total}}
# iterations regardless — do not declare the work complete or skip the fix
# step. Late iterations exist specifically to catch issues that look subtle
# on early passes.
#
# AUDIT: Critically review the current state. List the 1-3 most significant
# remaining issues, ordered by impact: correctness > security > edge cases >
# test coverage > performance > style. Be specific — name files, functions,
# line numbers.
#
# FIX: Address the single highest-priority issue from your audit this
# iteration. Make the focused change and run any relevant tests or linters.
#
# Scope: do not add features outside the original task. Do not refactor
# outside the issue you're fixing this iteration. One concrete improvement
# per pass.
# """

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
# The Ralph loop just exited{{issue_clause}}. Perform post-loop cleanup per
# {{instructions_doc}}: push the branch and open a PR.{{closes_clause}}
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
