package claude

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const toolInputMaxLen = 400

// Render translates a decoded stream-json Event into a chunk of human-readable
// text and writes it to w. It is the native Go equivalent of the jq filter the
// original ralph.sh used. Unknown event types produce no output (intentional).
func Render(w io.Writer, ev *Event) error {
	chunk := renderChunk(ev)
	if chunk == "" {
		return nil
	}
	_, err := io.WriteString(w, chunk)
	return err
}

func renderChunk(ev *Event) string {
	switch ev.Type {
	case "stream_event":
		return renderStream(ev.Event)
	case "assistant":
		return renderAssistant(ev.Message)
	case "user":
		return renderUser(ev.Message)
	case "system":
		if ev.Subtype == "init" {
			id := ev.SessionID
			if id == "" {
				id = "?"
			}
			return fmt.Sprintf("[session init: %s]\n", id)
		}
	case "result":
		return fmt.Sprintf("\n\n[result: $%g over %dms]\n\n", ev.TotalCostUSD, ev.DurationMS)
	}
	return ""
}

func renderStream(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var se streamEvent
	if err := json.Unmarshal(raw, &se); err != nil {
		return ""
	}
	if se.Type != "content_block_delta" || len(se.Delta) == 0 {
		return ""
	}
	var td textDelta
	if err := json.Unmarshal(se.Delta, &td); err != nil {
		return ""
	}
	if td.Type != "text_delta" {
		return ""
	}
	return td.Text
}

func renderAssistant(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var msg messageEnvelope
	if err := json.Unmarshal(raw, &msg); err != nil {
		return ""
	}
	var b strings.Builder
	for _, c := range msg.Content {
		if c.Type != "tool_use" {
			continue
		}
		input := string(c.Input)
		if len(input) > toolInputMaxLen {
			input = input[:toolInputMaxLen] + "..."
		}
		fmt.Fprintf(&b, "\n\n[tool: %s] %s\n", c.Name, input)
	}
	return b.String()
}

func renderUser(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var msg messageEnvelope
	if err := json.Unmarshal(raw, &msg); err != nil {
		return ""
	}
	var b strings.Builder
	for _, c := range msg.Content {
		if c.Type == "tool_result" {
			b.WriteString("\n[tool_result]\n")
		}
	}
	return b.String()
}
