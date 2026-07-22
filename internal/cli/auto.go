package cli

import (
	"fmt"
	"time"

	"github.com/chuntley/go-ralph-go/internal/config"
	"github.com/chuntley/go-ralph-go/internal/runner"
	"github.com/spf13/cobra"
)

func newAutoCmd() *cobra.Command {
	var once bool
	var minIterations int
	var iterations int
	var instructionsDoc string
	var pollSeconds int
	var noWorktree bool
	var parallel int
	var passTimeout time.Duration
	var passRetries int

	cmd := &cobra.Command{
		Use:   "auto",
		Short: "Poll the host for ready-labelled issues and work them in order",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if err := applyOverrides(cfg, minIterations, iterations, instructionsDoc, passTimeout, passRetries); err != nil {
				return err
			}
			if pollSeconds > 0 {
				cfg.PollInterval = pollSeconds
			}
			if noWorktree && parallel > 1 {
				return fmt.Errorf("--no-worktree can't be combined with --parallel %d: parallelism needs a worktree per issue", parallel)
			}
			if noWorktree {
				cfg.Worktrees = false
			}
			if parallel > 0 {
				cfg.MaxParallel = parallel
			}
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid flags: %w", err)
			}
			return runner.RunAuto(cmd.Context(), cfg, once)
		},
	}

	cmd.Flags().BoolVar(&once, "once", false, "exit after one issue (or immediately if no issues are ready)")
	cmd.Flags().IntVar(&minIterations, "min-iterations", 0, "override the minimum refine passes before completion is honoured")
	cmd.Flags().IntVarP(&iterations, "iterations", "n", 0, "override the max refine passes safety cap")
	cmd.Flags().StringVar(&instructionsDoc, "instructions-doc", "", "override the doc Claude follows during cleanup")
	cmd.Flags().IntVar(&pollSeconds, "poll", 0, "override poll interval in seconds (min 30; default from config / 60)")
	cmd.Flags().BoolVar(&noWorktree, "no-worktree", false, "disable per-issue worktree isolation; work in-place in the repo root (requires a clean tree)")
	cmd.Flags().IntVar(&parallel, "parallel", 0, "work up to N issues concurrently, each in its own worktree")
	cmd.Flags().DurationVar(&passTimeout, "pass-timeout", 0, "cap each refine pass (e.g. 45m); 0 = no cap. On timeout the pass is retried in place")
	cmd.Flags().IntVar(&passRetries, "pass-retries", -1, "retries for a crashed/timed-out pass, in place on the same worktree (default from config; 0 = fail on first)")
	return cmd
}
