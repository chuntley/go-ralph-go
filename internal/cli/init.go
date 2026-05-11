package cli

import (
	"fmt"

	"github.com/chuntley/go-ralph-go/internal/config"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter .go-ralph-go config in the current directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.WriteStarter(force); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "Wrote .go-ralph-go and ensured .ralph/ is in .gitignore.")
			fmt.Fprintln(out, "Next: run `ralph doctor` to verify your environment.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing .go-ralph-go file")
	return cmd
}
