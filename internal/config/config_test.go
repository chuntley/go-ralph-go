package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if cfg.Iterations != 5 {
		t.Errorf("expected default iterations=5, got %d", cfg.Iterations)
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

func TestLoadAutoDetectsLocalClaudeDir(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ConfigFileName), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(root, ".claude")
	if err := os.Mkdir(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cfg.ClaudeConfigDir, ".claude") {
		t.Errorf("expected ClaudeConfigDir to end with .claude, got %q", cfg.ClaudeConfigDir)
	}
}
