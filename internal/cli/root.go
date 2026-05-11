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
If a local ".claude" directory is present, it is used as CLAUDE_CONFIG_DIR so
project-scoped Claude config (agents, MCP, skills, auth) takes effect.`,
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
