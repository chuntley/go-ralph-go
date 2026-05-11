package claude

import (
	"fmt"
	"os/exec"
)

// LookupBin resolves bin against PATH and returns the absolute path or a
// human-friendly error explaining how to install Claude Code.
func LookupBin(bin string) (string, error) {
	if bin == "" {
		bin = "claude"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("could not find %q on PATH — install Claude Code (https://docs.claude.com/claude-code) or set claude_bin in .go-ralph-go", bin)
	}
	return path, nil
}
