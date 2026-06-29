package runner

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

// TestDashboardConcurrentUpdates drives the render loop concurrently with row
// add/update/finish from many goroutines — the shape parallel auto mode
// produces. Run under -race, it guards the dashboard mutex against the workers
// and the ticker. The writer is io.Discard (safe for concurrent writes) and we
// force isTTY so the render goroutine actually runs.
func TestDashboardConcurrentUpdates(t *testing.T) {
	d := &dashboard{w: io.Discard, isTTY: true, stop: make(chan struct{})}
	d.start()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			row := d.addRow(n, fmt.Sprintf("issue %d", n), func() string { return "live message" })
			row.setBranch(fmt.Sprintf("ralph/issue-%d-x", n))
			for p := 1; p <= 5; p++ {
				row.setStatus(fmt.Sprintf("refine %d/10", p))
				row.setMessage(fmt.Sprintf("working on step %d", p))
			}
			d.setCounts(n, 0)
			var err error
			if n%7 == 0 {
				err = fmt.Errorf("checks failed on PR #%d", n)
			}
			row.finish(err)
		}(i)
	}
	wg.Wait()
	d.stopRender()
	// Idempotent stop must not panic / double-close.
	d.stopRender()
}

// TestDashboardSanitizeClipSpread covers the wrap-proofing helpers that prevent
// the corrupted/duplicated render the full-screen rewrite fixed.
func TestDashboardSanitizeClipSpread(t *testing.T) {
	// sanitize strips ESC/CR, turns tab into space — so an embedded escape
	// sequence can't move the cursor or throw off width accounting.
	got := sanitize("a\x1b[31mb\tc\r")
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, '\r') {
		t.Errorf("sanitize left a control char in %q", got)
	}
	if got != "a[31mb c" {
		t.Errorf("sanitize = %q; want %q", got, "a[31mb c")
	}

	// clip never exceeds the requested rune budget.
	if c := clip("hello world", 5); len([]rune(c)) != 5 {
		t.Errorf("clip width = %d; want 5 (%q)", len([]rune(c)), c)
	}
	if c := clip("hi", 10); c != "hi" {
		t.Errorf("clip should pass short strings through, got %q", c)
	}
	if c := clip("x", 0); c != "" {
		t.Errorf("clip to 0 should be empty, got %q", c)
	}

	// spread pads left/right to exactly width and keeps the right side flush.
	s := spread("#42 title", "4m16s", 30)
	if len([]rune(s)) != 30 {
		t.Errorf("spread width = %d; want 30 (%q)", len([]rune(s)), s)
	}
	if !strings.HasSuffix(s, "4m16s") {
		t.Errorf("spread should end with the right value, got %q", s)
	}
	// When too narrow, it clips the left but still never exceeds width.
	if s := spread("a very long left side", "9s", 10); len([]rune(s)) > 10 {
		t.Errorf("spread overflowed: %q (%d)", s, len([]rune(s)))
	}
}

// TestDashboardNonTTYPlainLines confirms that on a non-terminal writer the
// dashboard emits plain, prefixed lines and NO ANSI escape codes (so piped /
// CI logs stay clean).
func TestDashboardNonTTYPlainLines(t *testing.T) {
	var buf bytes.Buffer
	d := newDashboard(&buf) // bytes.Buffer is not *os.File → isTTY false
	if d.isTTY {
		t.Fatal("a bytes.Buffer must not be detected as a TTY")
	}
	d.start() // no-op for non-TTY
	row := d.addRow(7, "do the thing", nil)
	d.event("picking up #7")
	row.finish(nil)
	d.setCounts(1, 0)
	d.stopRender()

	out := buf.String()
	if strings.Contains(out, "\033[") {
		t.Errorf("non-TTY output must not contain ANSI escapes:\n%q", out)
	}
	if !strings.Contains(out, "#7") {
		t.Errorf("expected issue #7 in plain output:\n%q", out)
	}
	if !strings.Contains(out, "ralph auto stopped") {
		t.Errorf("expected a final summary line:\n%q", out)
	}
}
