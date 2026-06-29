package cli

import (
	"fmt"

	"github.com/chuntley/go-ralph-go/internal/config"
	"github.com/chuntley/go-ralph-go/internal/runner"
	"github.com/spf13/cobra"
)

func newIssueCmd() *cobra.Command {
	var minIterations int
	var iterations int
	var instructionsDoc string
	var noWorktree bool

	cmd := &cobra.Command{
		Use:   "issue <number>",
		Short: "Work a single issue end-to-end (PR + auto-merge)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var n int
			if _, err := fmt.Sscanf(args[0], "%d", &n); err != nil || n <= 0 {
				return fmt.Errorf("issue number must be a positive integer")
			}
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if err := applyOverrides(cfg, minIterations, iterations, instructionsDoc); err != nil {
				return err
			}
			if noWorktree {
				cfg.Worktrees = false
				if err := cfg.Validate(); err != nil {
					return fmt.Errorf("invalid --no-worktree: %w", err)
				}
			}
			return runner.RunIssue(cmd.Context(), cfg, n)
		},
	}

	cmd.Flags().IntVar(&minIterations, "min-iterations", 0, "override the minimum refine passes before completion is honoured")
	cmd.Flags().IntVarP(&iterations, "iterations", "n", 0, "override the max refine passes safety cap")
	cmd.Flags().StringVar(&instructionsDoc, "instructions-doc", "", "override the doc Claude follows during cleanup")
	cmd.Flags().BoolVar(&noWorktree, "no-worktree", false, "disable worktree isolation; work in-place in the repo root (requires a clean tree)")
	return cmd
}
