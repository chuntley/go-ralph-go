package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"time"
)

// authStatusTimeout bounds the `claude auth status` probe so a hung CLI can't
// stall ralph's startup banner.
const authStatusTimeout = 8 * time.Second

// AuthStatus is the subset of `claude auth status` JSON output ralph uses to
// report — accurately — which login a run will actually use. Credentials may
// live in the macOS Keychain rather than a file, so this (not the presence of
// a .credentials.json) is the source of truth for whether claude is logged in.
type AuthStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	Email            string `json:"email"`
	OrgName          string `json:"orgName"`
	SubscriptionType string `json:"subscriptionType"`
}

// QueryAuthStatus runs `claude auth status` (with CLAUDE_CONFIG_DIR set to
// configDir when non-empty) and parses the result. The bool is false when the
// status could not be determined (claude missing, timeout, or unparseable
// output) — callers must then NOT assume an auth problem.
func QueryAuthStatus(bin, configDir string) (AuthStatus, bool) {
	if bin == "" {
		bin = "claude"
	}
	ctx, cancel := context.WithTimeout(context.Background(), authStatusTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "auth", "status")
	cmd.Env = os.Environ()
	if configDir != "" {
		cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+configDir)
	}
	out, err := cmd.Output()
	if err != nil {
		return AuthStatus{}, false
	}
	return parseAuthStatus(out)
}

// parseAuthStatus decodes the JSON body of `claude auth status`. Split out for
// testability.
func parseAuthStatus(out []byte) (AuthStatus, bool) {
	var s AuthStatus
	if err := json.Unmarshal(bytes.TrimSpace(out), &s); err != nil {
		return AuthStatus{}, false
	}
	return s, true
}
