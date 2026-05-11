package claude

import "encoding/json"

// Event is one decoded line from `claude -p --output-format stream-json`.
//
// The wire format is a tagged union by .type; we leave fine-grained content
// in json.RawMessage and let the renderer decode only what it cares about.
type Event struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype,omitempty"`
	Event   json.RawMessage `json:"event,omitempty"`
	Message json.RawMessage `json:"message,omitempty"`

	// system/init
	SessionID string `json:"session_id,omitempty"`

	// assistant — top-level "error" sibling of "message" (e.g.
	// "authentication_failed"). When set, the text content of .message is
	// usually the human-readable reason.
	Error string `json:"error,omitempty"`

	// result
	TotalCostUSD float64 `json:"total_cost_usd,omitempty"`
	DurationMS   int64   `json:"duration_ms,omitempty"`
	IsError      bool    `json:"is_error,omitempty"`
	Result       string  `json:"result,omitempty"`
	APIErrStatus string  `json:"api_error_status,omitempty"`
}

// streamEvent is the inner payload of an .event for stream_event Events.
type streamEvent struct {
	Type  string          `json:"type"`
	Delta json.RawMessage `json:"delta,omitempty"`
}

type textDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// messageEnvelope is the .message payload on assistant/user events.
type messageEnvelope struct {
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name,omitempty"`  // tool_use
	Input json.RawMessage `json:"input,omitempty"` // tool_use
	Text  string          `json:"text,omitempty"`  // text
}
