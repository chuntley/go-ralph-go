package claude

import "testing"

func TestParseAuthStatus(t *testing.T) {
	const loggedIn = `{
	  "loggedIn": true,
	  "authMethod": "claude.ai",
	  "email": "you@example.com",
	  "orgName": "Acme",
	  "subscriptionType": "max"
	}`
	st, ok := parseAuthStatus([]byte("\n" + loggedIn + "\n"))
	if !ok {
		t.Fatal("expected parse ok")
	}
	if !st.LoggedIn || st.Email != "you@example.com" || st.SubscriptionType != "max" || st.OrgName != "Acme" {
		t.Errorf("unexpected parse: %+v", st)
	}

	st, ok = parseAuthStatus([]byte(`{"loggedIn": false, "authMethod": "none"}`))
	if !ok || st.LoggedIn {
		t.Errorf("expected ok + loggedIn=false, got ok=%v st=%+v", ok, st)
	}

	if _, ok := parseAuthStatus([]byte("not json")); ok {
		t.Error("non-JSON should fail to parse")
	}
}
