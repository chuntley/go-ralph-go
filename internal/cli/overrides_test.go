package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/chuntley/go-ralph-go/internal/config"
)

func TestApplyOverridesIterationsAccepted(t *testing.T) {
	cfg := config.Defaults()
	if err := applyOverrides(&cfg, 0, 20, "", 0, -1); err != nil {
		t.Fatal(err)
	}
	if cfg.Iterations != 20 {
		t.Errorf("iterations not applied: got %d", cfg.Iterations)
	}
}

func TestApplyOverridesMinIterationsAccepted(t *testing.T) {
	cfg := config.Defaults()
	if err := applyOverrides(&cfg, 3, 0, "", 0, -1); err != nil {
		t.Fatal(err)
	}
	if cfg.MinIterations != 3 {
		t.Errorf("min iterations not applied: got %d", cfg.MinIterations)
	}
}

func TestApplyOverridesMinClampedToMax(t *testing.T) {
	cfg := config.Defaults()
	// Lowering only the max below the default min clamps the min down rather
	// than erroring (so `ralph run -n 3` just works).
	if err := applyOverrides(&cfg, 0, 3, "", 0, -1); err != nil {
		t.Fatalf("low max override should clamp min, not error: %v", err)
	}
	if cfg.Iterations != 3 || cfg.MinIterations != 3 {
		t.Errorf("expected min and max both 3 after clamp, got min=%d max=%d", cfg.MinIterations, cfg.Iterations)
	}
}

func TestApplyOverridesIterationsCapped(t *testing.T) {
	cfg := config.Defaults()
	err := applyOverrides(&cfg, 0, 500, "", 0, -1)
	if err == nil {
		t.Fatal("expected error on iteration overflow")
	}
	if !strings.Contains(err.Error(), "iterations must be") {
		t.Errorf("error message lacks guidance: %v", err)
	}
}

func TestApplyOverridesInstructionsDoc(t *testing.T) {
	cfg := config.Defaults()
	if err := applyOverrides(&cfg, 0, 0, "CLAUDE.md", 0, -1); err != nil {
		t.Fatal(err)
	}
	if cfg.InstructionsDoc != "CLAUDE.md" {
		t.Errorf("instructions doc not applied: got %q", cfg.InstructionsDoc)
	}
}

func TestApplyOverridesNoOpsLeaveDefaults(t *testing.T) {
	cfg := config.Defaults()
	if err := applyOverrides(&cfg, 0, 0, "", 0, -1); err != nil {
		t.Fatal(err)
	}
	if cfg.MinIterations != 5 || cfg.Iterations != 10 || cfg.InstructionsDoc != "AGENTS.md" {
		t.Errorf("zero overrides should leave defaults intact: %+v", cfg)
	}
	// pass_retries default (2) must survive a no-op override — the -1 sentinel
	// means "unset", NOT "set to -1".
	if cfg.PassRetries != 2 {
		t.Errorf("pass_retries default should survive no-op override: got %d", cfg.PassRetries)
	}
}

func TestApplyOverridesPassTimeoutAndRetries(t *testing.T) {
	cfg := config.Defaults()
	if err := applyOverrides(&cfg, 0, 0, "", 45*time.Minute, 5); err != nil {
		t.Fatal(err)
	}
	if cfg.PassTimeoutDur != 45*time.Minute {
		t.Errorf("pass_timeout not applied: got %v", cfg.PassTimeoutDur)
	}
	if cfg.PassRetries != 5 {
		t.Errorf("pass_retries not applied: got %d", cfg.PassRetries)
	}
}

func TestApplyOverridesPassRetriesZeroIsMeaningful(t *testing.T) {
	cfg := config.Defaults()
	// 0 is a real value (fail on first crash), distinct from the -1 "unset"
	// sentinel — it must override the default of 2.
	if err := applyOverrides(&cfg, 0, 0, "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if cfg.PassRetries != 0 {
		t.Errorf("explicit --pass-retries 0 should override the default: got %d", cfg.PassRetries)
	}
}
