package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/chuntley/go-ralph-go/internal/config"
	"github.com/chuntley/go-ralph-go/internal/runner"
	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	var openPR bool
	var minIterations int
	var iterations int
	var instructionsDoc string
	var passTimeout time.Duration
	var passRetries int

	cmd := &cobra.Command{
		Use:   "run [prompt...]",
		Short: "Run the refine loop on an ad-hoc prompt",
		Long: `Runs a goal-driven refine loop against a Claude session in the current
project, then a cleanup pass. The loop drives toward the prompt as a goal and
ends when Claude signals completion (confirmed by verify_command when set), up
to a max-passes safety cap. With --pr, the cleanup pass pushes a branch and
opens a PR (no auto-merge). Without --pr, the loop is fully local and does not
require a git host.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if err := applyOverrides(cfg, minIterations, iterations, instructionsDoc, passTimeout, passRetries); err != nil {
				return err
			}
			prompt := strings.Join(args, " ")
			return runner.RunPrompt(cmd.Context(), cfg, prompt, openPR)
		},
	}

	cmd.Flags().BoolVar(&openPR, "pr", false, "open a PR after the loop (requires git host)")
	cmd.Flags().IntVar(&minIterations, "min-iterations", 0, "override the minimum refine passes before completion is honoured (default: from config, fallback 5)")
	cmd.Flags().IntVarP(&iterations, "iterations", "n", 0, "override the max refine passes safety cap (default: from config, fallback 10)")
	cmd.Flags().StringVar(&instructionsDoc, "instructions-doc", "", "override the doc Claude is told to follow during cleanup (e.g. CLAUDE.md)")
	cmd.Flags().DurationVar(&passTimeout, "pass-timeout", 0, "cap each refine pass (e.g. 45m); 0 = no cap. On timeout the pass is retried in place")
	cmd.Flags().IntVar(&passRetries, "pass-retries", -1, "retries for a crashed/timed-out pass, in place on the same worktree (default from config; 0 = fail on first)")
	return cmd
}
