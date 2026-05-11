package cli

import (
	"strings"
	"testing"

	"github.com/chuntley/go-ralph-go/internal/config"
)

func TestApplyOverridesIterationsAccepted(t *testing.T) {
	cfg := config.Defaults()
	if err := applyOverrides(&cfg, 10, ""); err != nil {
		t.Fatal(err)
	}
	if cfg.Iterations != 10 {
		t.Errorf("iterations not applied: got %d", cfg.Iterations)
	}
}

func TestApplyOverridesIterationsCapped(t *testing.T) {
	cfg := config.Defaults()
	err := applyOverrides(&cfg, 500, "")
	if err == nil {
		t.Fatal("expected error on iteration overflow")
	}
	if !strings.Contains(err.Error(), "iterations must be") {
		t.Errorf("error message lacks guidance: %v", err)
	}
}

func TestApplyOverridesInstructionsDoc(t *testing.T) {
	cfg := config.Defaults()
	if err := applyOverrides(&cfg, 0, "CLAUDE.md"); err != nil {
		t.Fatal(err)
	}
	if cfg.InstructionsDoc != "CLAUDE.md" {
		t.Errorf("instructions doc not applied: got %q", cfg.InstructionsDoc)
	}
}

func TestApplyOverridesNoOpsLeaveDefaults(t *testing.T) {
	cfg := config.Defaults()
	if err := applyOverrides(&cfg, 0, ""); err != nil {
		t.Fatal(err)
	}
	if cfg.Iterations != 5 || cfg.InstructionsDoc != "AGENTS.md" {
		t.Errorf("zero overrides should leave defaults intact: %+v", cfg)
	}
}
