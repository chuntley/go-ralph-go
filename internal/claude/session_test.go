package claude

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResetStartsFreshSession verifies that Reset rotates the session id and
// clears the "started" flag. Auto mode relies on this: without it, every
// issue in the auto loop would --resume the same Claude session as the
// previous issue, compounding context across unrelated tasks.
func TestResetStartsFreshSession(t *testing.T) {
	dir := t.TempDir()
	sess, err := NewSession(SessionConfig{
		Bin:       "claude",
		WorkDir:   dir,
		OutputDir: filepath.Join(dir, ".ralph"),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	originalID := sess.ID()
	if originalID == "" {
		t.Fatal("expected non-empty initial session id")
	}

	// Simulate a session that has already done at least one Run.
	sess.started = true

	if err := sess.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if sess.ID() == originalID {
		t.Errorf("Reset did not rotate session id (still %q) — auto mode would share context across issues", sess.ID())
	}
	if sess.started {
		t.Errorf("Reset did not clear started flag — next Run would use --resume instead of --session-id")
	}
}

// TestResetTruncatesLogs verifies the original Reset contract (truncate log
// files) still holds after the session-rotation change.
func TestResetTruncatesLogs(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, ".ralph")
	sess, err := NewSession(SessionConfig{
		Bin:       "claude",
		WorkDir:   dir,
		OutputDir: outDir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	// Force log creation, then dirty the pretty log.
	if err := sess.WriteBanner("pre-reset content\n"); err != nil {
		t.Fatalf("WriteBanner: %v", err)
	}
	if err := sess.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	for _, name := range []string{"output.jsonl", "output.txt", "output.stderr"} {
		st, err := os.Stat(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if st.Size() != 0 {
			t.Errorf("Reset left %s at %d bytes; expected 0", name, st.Size())
		}
	}
}
