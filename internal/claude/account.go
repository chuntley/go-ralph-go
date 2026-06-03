package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Account holds the non-secret Claude Code account metadata ralph surfaces in
// its startup banner so you can see which login a run is billing against. No
// tokens or credentials are read — only the descriptive oauthAccount block.
type Account struct {
	Email        string
	BillingType  string
	Organization string
}

// Empty reports whether no account info could be resolved.
func (a Account) Empty() bool {
	return a.Email == "" && a.BillingType == "" && a.Organization == ""
}

// Label renders the populated fields as "email · billing · org" (omitting any
// that are blank). Returns "" when Empty.
func (a Account) Label() string {
	parts := make([]string, 0, 3)
	for _, p := range []string{a.Email, a.BillingType, a.Organization} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "  ·  ")
}

// LoadAccount reads account metadata from Claude Code's .claude.json. When
// claudeConfigDir is set it looks there ($CLAUDE_CONFIG_DIR/.claude.json);
// otherwise it falls back to ~/.claude.json. Best-effort: a zero Account is
// returned (never an error) if the file is missing, unreadable, or has no
// oauthAccount block (e.g. raw API-key auth rather than an OAuth login).
func LoadAccount(claudeConfigDir string) Account {
	path := claudeJSONPath(claudeConfigDir)
	if path == "" {
		return Account{}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Account{}
	}
	var doc struct {
		OAuthAccount struct {
			EmailAddress     string `json:"emailAddress"`
			BillingType      string `json:"billingType"`
			OrganizationName string `json:"organizationName"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return Account{}
	}
	return Account{
		Email:        doc.OAuthAccount.EmailAddress,
		BillingType:  doc.OAuthAccount.BillingType,
		Organization: doc.OAuthAccount.OrganizationName,
	}
}

func claudeJSONPath(claudeConfigDir string) string {
	if claudeConfigDir != "" {
		return filepath.Join(claudeConfigDir, ".claude.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude.json")
}
