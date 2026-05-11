package cli

import (
	"fmt"

	"github.com/chuntley/go-ralph-go/internal/config"
	"github.com/chuntley/go-ralph-go/internal/runner"
	"github.com/spf13/cobra"
)

func newAutoCmd() *cobra.Command {
	var once bool
	var iterations int
	var instructionsDoc string
	var pollSeconds int

	cmd := &cobra.Command{
		Use:   "auto",
		Short: "Poll the host for ready-labelled issues and work them in order",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if err := applyOverrides(cfg, iterations, instructionsDoc); err != nil {
				return err
			}
			if pollSeconds > 0 {
				cfg.PollInterval = pollSeconds
				if err := cfg.Validate(); err != nil {
					return fmt.Errorf("invalid --poll: %w", err)
				}
			}
			return runner.RunAuto(cmd.Context(), cfg, once)
		},
	}

	cmd.Flags().BoolVar(&once, "once", false, "exit after one issue (or immediately if no issues are ready)")
	cmd.Flags().IntVarP(&iterations, "iterations", "n", 0, "override refine iteration count")
	cmd.Flags().StringVar(&instructionsDoc, "instructions-doc", "", "override the doc Claude follows during cleanup")
	cmd.Flags().IntVar(&pollSeconds, "poll", 0, "override poll interval in seconds (min 30; default from config / 60)")
	return cmd
}
