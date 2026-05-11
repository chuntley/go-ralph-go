package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is overridable at link time via:
//
//	go build -ldflags='-X github.com/chuntley/go-ralph-go/internal/cli.version=v1.2.3'
//
// When unset, we fall back to runtime/debug.ReadBuildInfo which reports the
// pseudo-version when installed via `go install ...@latest`.
var version = ""

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print ralph version info",
		Run: func(cmd *cobra.Command, args []string) {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "ralph %s\n", resolveVersion())
			fmt.Fprintf(out, "  go         : %s\n", runtime.Version())
			fmt.Fprintf(out, "  platform   : %s/%s\n", runtime.GOOS, runtime.GOARCH)
		},
	}
}

func resolveVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "(unknown)"
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	// Look for vcs.revision in build settings (set when building from source).
	rev, dirty := "", ""
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "+dirty"
			}
		}
	}
	if rev != "" {
		if len(rev) > 7 {
			rev = rev[:7]
		}
		return rev + dirty
	}
	return "(devel)"
}
