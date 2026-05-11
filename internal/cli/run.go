package cli

import (
	"fmt"
	"strings"

	"github.com/chuntley/go-ralph-go/internal/config"
	"github.com/chuntley/go-ralph-go/internal/runner"
	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	var openPR bool
	var iterations int
	var instructionsDoc string

	cmd := &cobra.Command{
		Use:   "run [prompt...]",
		Short: "Run the refine loop on an ad-hoc prompt",
		Long: `Runs N refine iterations against a fresh Claude session in the current
project, then a cleanup pass. With --pr, the cleanup pass pushes a branch
and opens a PR (no auto-merge). Without --pr, the loop is fully local and
does not require a git host.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if err := applyOverrides(cfg, iterations, instructionsDoc); err != nil {
				return err
			}
			prompt := strings.Join(args, " ")
			return runner.RunPrompt(cmd.Context(), cfg, prompt, openPR)
		},
	}

	cmd.Flags().BoolVar(&openPR, "pr", false, "open a PR after the loop (requires git host)")
	cmd.Flags().IntVarP(&iterations, "iterations", "n", 0, "override refine iteration count (default: from config, fallback 5)")
	cmd.Flags().StringVar(&instructionsDoc, "instructions-doc", "", "override the doc Claude is told to follow during cleanup (e.g. CLAUDE.md)")
	return cmd
}
