package claude

import (
	"encoding/json"
	"errors"
	"strings"
)

// ErrNotLoggedIn marks a run that failed because the active CLAUDE_CONFIG_DIR
// has no usable login — claude refused with an authentication error rather than
// doing any work.
//
// The runner treats this as a local environment problem (actionable: log in),
// NOT as a task/issue failure: the issue is requeued rather than marked failed,
// and the run aborts with the exact `claude auth login` command instead of a
// bare "exit status 1". On macOS credentials live in the Keychain keyed by the
// config dir, so a freshly-provisioned project-local .claude (metadata but no
// login) trips this — exactly the new-project onboarding footgun this detects.
var ErrNotLoggedIn = errors.New("claude is not logged in for this config dir")

// authErrorReason returns a human-readable reason when ev signals an
// authentication failure, or "" otherwise. Claude surfaces a missing/invalid
// login two ways, and we match both so a wording change on either path still
// trips the detector:
//
//   - an assistant event whose top-level "error" is "authentication_failed",
//     carrying text like "Not logged in · Please run /login"; and
//   - a result event (is_error) whose text says the same.
func authErrorReason(ev *Event) string {
	if ev == nil {
		return ""
	}
	// Typed signal first: the assistant event's top-level "error" field. This is
	// the most precise — it is set by claude specifically for auth failures.
	if strings.Contains(strings.ToLower(ev.Error), "auth") {
		if txt := assistantText(ev.Message); txt != "" {
			return txt
		}
		return ev.Error
	}
	// Fallback: the terminal result event's error text. Catches the case where
	// the typed field is absent but the surfaced message is unmistakably an auth
	// refusal.
	if ev.Type == "result" && ev.IsError && looksLikeAuthText(ev.Result) {
		return strings.TrimSpace(ev.Result)
	}
	return ""
}

// looksLikeAuthText reports whether s reads like claude's not-logged-in refusal.
// Kept deliberately broad (any of these phrases) so a minor wording change
// doesn't silently turn an auth failure back into a cryptic "exit status 1".
func looksLikeAuthText(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "not logged in") ||
		strings.Contains(l, "please run /login") ||
		strings.Contains(l, "invalid api key") ||
		strings.Contains(l, "authentication_failed")
}

// assistantText concatenates the text content blocks of an assistant/user
// message envelope. Used to recover the human-readable reason attached to an
// auth-error event.
func assistantText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var msg messageEnvelope
	if err := json.Unmarshal(raw, &msg); err != nil {
		return ""
	}
	parts := make([]string, 0, len(msg.Content))
	for _, c := range msg.Content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			parts = append(parts, strings.TrimSpace(c.Text))
		}
	}
	return strings.Join(parts, " ")
}
