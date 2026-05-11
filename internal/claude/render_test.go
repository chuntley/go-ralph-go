package claude

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func decode(t *testing.T, line string) *Event {
	t.Helper()
	var ev Event
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	return &ev
}

func TestRenderTextDelta(t *testing.T) {
	ev := decode(t, `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}}`)
	var b bytes.Buffer
	if err := Render(&b, ev); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != "hello" {
		t.Fatalf("want %q, got %q", "hello", got)
	}
}

func TestRenderToolUse(t *testing.T) {
	ev := decode(t, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/x"}}]}}`)
	var b bytes.Buffer
	if err := Render(&b, ev); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.Contains(got, "[tool: Read]") {
		t.Fatalf("missing tool tag: %q", got)
	}
	if !strings.Contains(got, "/x") {
		t.Fatalf("missing input: %q", got)
	}
}

func TestRenderToolResult(t *testing.T) {
	ev := decode(t, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"x"}]}}`)
	var b bytes.Buffer
	if err := Render(&b, ev); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); !strings.Contains(got, "[tool_result]") {
		t.Fatalf("missing tool_result: %q", got)
	}
}

func TestRenderSessionInit(t *testing.T) {
	ev := decode(t, `{"type":"system","subtype":"init","session_id":"abc-123"}`)
	var b bytes.Buffer
	if err := Render(&b, ev); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); !strings.Contains(got, "[session init: abc-123]") {
		t.Fatalf("missing init line: %q", got)
	}
}

func TestRenderResult(t *testing.T) {
	ev := decode(t, `{"type":"result","total_cost_usd":0.12,"duration_ms":3400}`)
	var b bytes.Buffer
	if err := Render(&b, ev); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.Contains(got, "$0.12") || !strings.Contains(got, "3400ms") {
		t.Fatalf("missing result fields: %q", got)
	}
}

func TestRenderUnknownTypeSilent(t *testing.T) {
	ev := decode(t, `{"type":"who_knows"}`)
	var b bytes.Buffer
	if err := Render(&b, ev); err != nil {
		t.Fatal(err)
	}
	if b.Len() != 0 {
		t.Fatalf("unknown type produced output: %q", b.String())
	}
}

func TestRenderToolInputTruncation(t *testing.T) {
	big := strings.Repeat("x", 600)
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Big","input":"` + big + `"}]}}`
	ev := decode(t, line)
	var b bytes.Buffer
	if err := Render(&b, ev); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.Contains(got, "...") {
		t.Fatalf("expected truncation marker: %q", got)
	}
	if len(got) > toolInputMaxLen+64 {
		t.Fatalf("output not truncated: len=%d", len(got))
	}
}

// TestRenderToolInputTruncationPreservesUTF8 — tool inputs containing
// multi-byte runes must not get clipped mid-rune. The rendered text lands in
// .ralph/output.txt; invalid UTF-8 there would break downstream grep/sed
// pipelines that assume the file is well-formed.
func TestRenderToolInputTruncationPreservesUTF8(t *testing.T) {
	// 中 is a 3-byte rune. Fill the input with enough 中s that the truncation
	// budget (toolInputMaxLen=400) lands mid-rune at byte 400 — 400/3 leaves
	// a 1-byte remainder, so position 400 is a continuation byte.
	big := strings.Repeat("中", 200) // 600 bytes, all multi-byte
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Big","input":"` + big + `"}]}}`
	ev := decode(t, line)
	var b bytes.Buffer
	if err := Render(&b, ev); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !utf8.ValidString(got) {
		t.Fatalf("rendered output is not valid UTF-8 — truncation cut a multi-byte rune: %q", got)
	}
	if !strings.Contains(got, "...") {
		t.Fatalf("expected truncation marker: %q", got)
	}
}
