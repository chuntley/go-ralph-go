package cli

import (
	"github.com/spf13/cobra"
)

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "ralph",
		Short: "Ralph — a non-interactive Claude loop for your project",
		Long: `Ralph drives Claude through a multi-iteration refine loop in your project's
working directory. It can run a one-shot prompt locally, work a single GitHub
issue end-to-end, or poll a repo for issues labelled "ready" and work them
in order.

Per-project defaults live in a ".go-ralph-go" TOML file at the project root.
By default ralph uses your system-wide claude install (and its auth). To opt
in to a project-local config dir, set "claude_config_dir" in .go-ralph-go.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(
		newRunCmd(),
		newIssueCmd(),
		newAutoCmd(),
		newInitCmd(),
		newDoctorCmd(),
		newVersionCmd(),
	)
	return root
}
