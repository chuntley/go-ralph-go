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

// TestLogPermissionsAreOwnerOnly guards the security fix that the session log
// directory and files are not world-readable. The logs include the full Claude
// transcript (tool inputs/results) which can contain credentials Claude saw
// during the run — anything beyond owner-read is a leak vector on shared hosts.
func TestLogPermissionsAreOwnerOnly(t *testing.T) {
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

	// Force lazy log creation.
	if err := sess.WriteBanner("touch\n"); err != nil {
		t.Fatalf("WriteBanner: %v", err)
	}

	dirSt, err := os.Stat(outDir)
	if err != nil {
		t.Fatalf("stat outDir: %v", err)
	}
	if perm := dirSt.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("output dir %s has group/other bits set (%#o); want owner-only", outDir, perm)
	}

	for _, name := range []string{"output.jsonl", "output.txt", "output.stderr"} {
		st, err := os.Stat(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if perm := st.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("log file %s has group/other bits set (%#o); want owner-only", name, perm)
		}
	}
}

// TestLogPermissionsTightenOnUpgradePath simulates a .ralph/ directory left
// behind by an older ralph build (dir 0o755, files 0o644). After the next
// ensureLogs runs, both must be tightened to 0o700 / 0o600 — otherwise the
// security fix only protects fresh installs and silently regresses for every
// existing user.
func TestLogPermissionsTightenOnUpgradePath(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, ".ralph")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("seed outDir: %v", err)
	}
	// Force the loose mode in case umask flipped some bits during MkdirAll.
	if err := os.Chmod(outDir, 0o755); err != nil {
		t.Fatalf("seed chmod dir: %v", err)
	}
	preExisting := filepath.Join(outDir, "output.txt")
	if err := os.WriteFile(preExisting, []byte("legacy\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := os.Chmod(preExisting, 0o644); err != nil {
		t.Fatalf("seed chmod file: %v", err)
	}

	sess, err := NewSession(SessionConfig{
		Bin:       "claude",
		WorkDir:   dir,
		OutputDir: outDir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	if err := sess.WriteBanner("trigger ensureLogs\n"); err != nil {
		t.Fatalf("WriteBanner: %v", err)
	}

	dirSt, err := os.Stat(outDir)
	if err != nil {
		t.Fatalf("stat outDir: %v", err)
	}
	if perm := dirSt.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("upgrade-path: outDir still has group/other bits set (%#o); expected owner-only", perm)
	}
	st, err := os.Stat(preExisting)
	if err != nil {
		t.Fatalf("stat preExisting: %v", err)
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("upgrade-path: pre-existing log file still has group/other bits set (%#o); expected owner-only", perm)
	}
}

// TestStartFreshRotatesIDWithoutTruncatingLogs verifies that StartFresh gives
// the next Run a brand-new session (so it uses --session-id, not --resume) but
// — unlike Reset — leaves the accumulating transcript intact.
func TestStartFreshRotatesIDWithoutTruncatingLogs(t *testing.T) {
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

	// Simulate a session that has already run a turn, with content in the logs.
	sess.started = true
	if err := sess.WriteBanner("prior pass content\n"); err != nil {
		t.Fatalf("WriteBanner: %v", err)
	}
	prevID := sess.ID()

	if err := sess.StartFresh(); err != nil {
		t.Fatalf("StartFresh: %v", err)
	}
	if sess.ID() == prevID {
		t.Error("StartFresh did not rotate the session id — next Run would --resume")
	}
	if sess.started {
		t.Error("StartFresh did not clear started — next Run would --resume instead of --session-id")
	}
	// Logs must be preserved (NOT truncated), so the operator transcript keeps
	// accumulating across fresh passes.
	st, err := os.Stat(filepath.Join(dir, ".ralph", "output.txt"))
	if err != nil {
		t.Fatalf("stat pretty log: %v", err)
	}
	if st.Size() == 0 {
		t.Error("StartFresh truncated the transcript; it must be preserved")
	}
}

// TestLastTurnTextCapturesAssistantProse verifies that writeEvent accumulates
// streamed assistant text (and only that) into the turn buffer the runner scans
// for a completion signal. Tool I/O must be excluded so a tool echoing the
// sentinel can't trigger a false completion.
func TestLastTurnTextCapturesAssistantProse(t *testing.T) {
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
	if err := sess.ensureLogs(); err != nil {
		t.Fatalf("ensureLogs: %v", err)
	}

	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"all done\n"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"RALPH_GOAL_COMPLETE"}}}`,
		// Tool use must NOT be captured into the turn buffer.
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo RALPH_GOAL_COMPLETE"}}]}}`,
	}
	for _, l := range lines {
		if err := sess.writeEvent([]byte(l)); err != nil {
			t.Fatalf("writeEvent: %v", err)
		}
	}

	got := sess.LastTurnText()
	if got != "all done\nRALPH_GOAL_COMPLETE" {
		t.Errorf("turn text = %q; want assistant prose only", got)
	}

	// A fresh Run resets the buffer.
	sess.turn.Reset()
	if sess.LastTurnText() != "" {
		t.Error("expected empty turn text after reset")
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
