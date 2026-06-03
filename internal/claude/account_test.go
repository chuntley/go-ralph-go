package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAccountFromConfigDir(t *testing.T) {
	dir := t.TempDir()
	const doc = `{
	  "oauthAccount": {
	    "emailAddress": "you@example.com",
	    "billingType": "usage_based",
	    "organizationName": "Acme",
	    "accountUuid": "ignored"
	  },
	  "userID": "ignored"
	}`
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	a := LoadAccount(dir)
	if a.Email != "you@example.com" || a.BillingType != "usage_based" || a.Organization != "Acme" {
		t.Errorf("unexpected account: %+v", a)
	}
	if a.Empty() {
		t.Error("account should not be Empty")
	}
	if got := a.Label(); got != "you@example.com  ·  usage_based  ·  Acme" {
		t.Errorf("Label() = %q", got)
	}
}

func TestLoadAccountMissingOrNoOAuth(t *testing.T) {
	// Missing file → empty, no panic.
	if a := LoadAccount(t.TempDir()); !a.Empty() {
		t.Errorf("missing .claude.json should yield Empty, got %+v", a)
	}
	// Present but no oauthAccount block (e.g. API-key auth) → empty.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"userID":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := LoadAccount(dir)
	if !a.Empty() || a.Label() != "" {
		t.Errorf("no oauthAccount should yield Empty with blank Label, got %+v / %q", a, a.Label())
	}
}
