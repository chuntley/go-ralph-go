package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestStarterTOMLParses guards against a syntax slip in the starter config the
// `ralph init` command writes. Its fields are all commented out, so it must
// decode cleanly and leave Defaults untouched when merged.
func TestStarterTOMLParses(t *testing.T) {
	cfg := Defaults()
	if _, err := toml.Decode(starterTOML, &cfg); err != nil {
		t.Fatalf("starter TOML does not parse: %v", err)
	}
	if cfg.MinIterations != 5 || cfg.Iterations != 10 || cfg.CompletionSentinel != "RALPH_GOAL_COMPLETE" {
		t.Errorf("commented starter TOML should not override defaults: %+v", cfg)
	}
}

// TestValidateClampsMinToMax covers the floor/cap relationship: a minimum above
// the maximum is clamped down (not an error) so a low cap can't deadlock config,
// while a sub-1 minimum is still rejected and min==max is left alone.
func TestValidateClampsMinToMax(t *testing.T) {
	c := Defaults()
	c.MinIterations = 8
	c.Iterations = 5
	if err := c.Validate(); err != nil {
		t.Fatalf("min > max should clamp, not error: %v", err)
	}
	if c.MinIterations != 5 {
		t.Errorf("min should be clamped to max (5), got %d", c.MinIterations)
	}

	c = Defaults()
	c.MinIterations = 0
	if err := c.Validate(); err == nil {
		t.Error("expected error when min_iterations < 1")
	}

	c = Defaults()
	c.MinIterations = 5
	c.Iterations = 5
	if err := c.Validate(); err != nil {
		t.Errorf("min == max should be valid, got: %v", err)
	}
}

func TestEnsureGitignoreCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	if err := ensureGitignore(path, ".ralph/"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), ".ralph/") {
		t.Fatalf("missing entry: %q", body)
	}
}

func TestEnsureGitignoreAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte("node_modules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureGitignore(path, ".ralph/"); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	got := string(body)
	if !strings.Contains(got, "node_modules\n") {
		t.Errorf("preserved entry missing: %q", got)
	}
	if !strings.Contains(got, ".ralph/\n") {
		t.Errorf("new entry missing: %q", got)
	}
}

func TestEnsureGitignoreIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte(".ralph/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureGitignore(path, ".ralph/"); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if strings.Count(string(body), ".ralph/") != 1 {
		t.Errorf("duplicate entry: %q", body)
	}
}

func TestEnsureGitignoreAppendsWhenMissingTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte("node_modules"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureGitignore(path, ".ralph/"); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "node_modules\n.ralph/\n") {
		t.Errorf("missing newline boundary: %q", body)
	}
}

// TestDefaultsMatchDocumentation locks the values returned by Defaults() to
// the numbers documented in the field comments, README, and starter TOML.
// Past drift: the PollInterval doc-comment said 300 while the actual default
// was 60, which would mislead anyone reading the GoDoc. Pin each default so
// future edits to one side without the other fail loudly here.
func TestDefaultsMatchDocumentation(t *testing.T) {
	c := Defaults()
	if c.MinIterations != 5 {
		t.Errorf("MinIterations default: got %d, want 5", c.MinIterations)
	}
	if c.Iterations != 10 {
		t.Errorf("Iterations default: got %d, want 10", c.Iterations)
	}
	if c.CompletionSentinel != "RALPH_GOAL_COMPLETE" {
		t.Errorf("CompletionSentinel default: got %q, want %q", c.CompletionSentinel, "RALPH_GOAL_COMPLETE")
	}
	if c.VerifyCommand != "" {
		t.Errorf("VerifyCommand default: got %q, want empty", c.VerifyCommand)
	}
	if c.InstructionsDoc != "AGENTS.md" {
		t.Errorf("InstructionsDoc default: got %q, want %q", c.InstructionsDoc, "AGENTS.md")
	}
	if c.OutputDir != ".ralph" {
		t.Errorf("OutputDir default: got %q, want %q", c.OutputDir, ".ralph")
	}
	if c.ClaudeBin != "claude" {
		t.Errorf("ClaudeBin default: got %q, want %q", c.ClaudeBin, "claude")
	}
	if c.PollInterval != 60 {
		t.Errorf("PollInterval default: got %d, want 60", c.PollInterval)
	}
	if c.GitHub.CheckIntervalSeconds != 30 {
		t.Errorf("GitHub.CheckIntervalSeconds default: got %d, want 30", c.GitHub.CheckIntervalSeconds)
	}
	if c.GitHub.Labels.Ready != "ready" || c.GitHub.Labels.Working != "ralph-working" || c.GitHub.Labels.Failed != "ralph-failed" {
		t.Errorf("label defaults drifted: %+v", c.GitHub.Labels)
	}
	// Defaults must round-trip through Validate without error so the public
	// "all fields optional" contract in the README holds.
	if err := c.Validate(); err != nil {
		t.Errorf("Defaults() should pass Validate, got: %v", err)
	}
}

func TestEffectiveClaudeConfigDir(t *testing.T) {
	// 1. Explicit config wins over the environment.
	c := &Config{ProjectRoot: "/proj", ClaudeConfigDir: "/explicit/dir"}
	t.Setenv("CLAUDE_CONFIG_DIR", "/env/dir")
	if got := c.EffectiveClaudeConfigDir(); got != "/explicit/dir" {
		t.Errorf("explicit config should win, got %q", got)
	}

	// 2. No explicit config → inherited absolute env var is used as-is.
	c = &Config{ProjectRoot: "/proj"}
	t.Setenv("CLAUDE_CONFIG_DIR", "/env/abs")
	if got := c.EffectiveClaudeConfigDir(); got != "/env/abs" {
		t.Errorf("absolute env should be used, got %q", got)
	}

	// 3. Relative env var resolves against ProjectRoot (claude's cwd).
	c = &Config{ProjectRoot: "/proj"}
	t.Setenv("CLAUDE_CONFIG_DIR", ".claude")
	if got := c.EffectiveClaudeConfigDir(); got != "/proj/.claude" {
		t.Errorf("relative env should resolve against ProjectRoot, got %q", got)
	}

	// 4. Nothing set → empty (system default ~/.claude).
	c = &Config{ProjectRoot: "/proj"}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if got := c.EffectiveClaudeConfigDir(); got != "" {
		t.Errorf("expected empty for system default, got %q", got)
	}
}

func TestLoadDefaultsWhenNoConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigPath != "" {
		t.Errorf("expected empty ConfigPath, got %q", cfg.ConfigPath)
	}
	if cfg.Iterations != 10 {
		t.Errorf("expected default iterations=10, got %d", cfg.Iterations)
	}
}

func TestLoadDiscoversConfigByWalkingUp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ConfigFileName), []byte("iterations = 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Iterations != 3 {
		t.Errorf("expected iterations=3 from config, got %d", cfg.Iterations)
	}
	// macOS adds /private prefix to tmp paths after Chdir+resolve; compare suffix.
	if !strings.HasSuffix(cfg.ProjectRoot, root) && cfg.ProjectRoot != root {
		t.Errorf("expected project root %q, got %q", root, cfg.ProjectRoot)
	}
}

func TestLoadDoesNotAutoDetectLocalClaudeDir(t *testing.T) {
	// Regression: a project-local .claude/ that exists only for memory must
	// NOT be auto-set as CLAUDE_CONFIG_DIR. Doing so produced silent "Not
	// logged in" failures when the dir lacked .credentials.json.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ConfigFileName), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClaudeConfigDir != "" {
		t.Errorf("ClaudeConfigDir must remain empty unless explicitly configured, got %q", cfg.ClaudeConfigDir)
	}
}

func TestLoadRespectsExplicitClaudeConfigDir(t *testing.T) {
	root := t.TempDir()
	cfgContent := `claude_config_dir = ".claude"` + "\n"
	if err := os.WriteFile(filepath.Join(root, ConfigFileName), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cfg.ClaudeConfigDir, ".claude") {
		t.Errorf("expected explicit ClaudeConfigDir to be resolved, got %q", cfg.ClaudeConfigDir)
	}
	if !filepath.IsAbs(cfg.ClaudeConfigDir) {
		t.Errorf("relative ClaudeConfigDir should be made absolute, got %q", cfg.ClaudeConfigDir)
	}
}
