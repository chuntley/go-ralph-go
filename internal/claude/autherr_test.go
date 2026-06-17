package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAuthErrorReason(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string // expected reason, or "" for "not an auth error"
	}{
		{
			name: "typed authentication_failed on assistant event",
			line: `{"type":"assistant","error":"authentication_failed","message":{"content":[{"type":"text","text":"Not logged in · Please run /login"}]}}`,
			want: "Not logged in · Please run /login",
		},
		{
			name: "typed error without message text falls back to the error code",
			line: `{"type":"assistant","error":"authentication_failed","message":{"content":[]}}`,
			want: "authentication_failed",
		},
		{
			name: "result error text recognised as auth",
			line: `{"type":"result","is_error":true,"result":"Not logged in · Please run /login"}`,
			want: "Not logged in · Please run /login",
		},
		{
			name: "result error text with invalid api key",
			line: `{"type":"result","is_error":true,"result":"Invalid API key · Please run /login"}`,
			want: "Invalid API key · Please run /login",
		},
		{
			name: "ordinary result is not an auth error",
			line: `{"type":"result","is_error":false,"result":"done"}`,
			want: "",
		},
		{
			name: "unrelated runtime error is not an auth error",
			line: `{"type":"result","is_error":true,"result":"tool failed: exit status 2"}`,
			want: "",
		},
		{
			name: "plain assistant text is not an auth error",
			line: `{"type":"assistant","message":{"content":[{"type":"text","text":"working on it"}]}}`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := decodeEvent(t, tc.line)
			if got := authErrorReason(ev); got != tc.want {
				t.Errorf("authErrorReason = %q; want %q", got, tc.want)
			}
		})
	}
}

// TestSessionRunSurfacesAuthReason verifies that an auth-failure event seen
// while consuming the stream is captured so Run can return ErrNotLoggedIn. We
// drive writeEvent directly (the same path consume uses) rather than spawning a
// real claude.
func TestSessionRunSurfacesAuthReason(t *testing.T) {
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

	if sess.authErrText() != "" {
		t.Fatal("expected no auth reason before any events")
	}
	line := `{"type":"assistant","error":"authentication_failed","message":{"content":[{"type":"text","text":"Not logged in · Please run /login"}]}}`
	if err := sess.writeEvent([]byte(line)); err != nil {
		t.Fatalf("writeEvent: %v", err)
	}
	got := sess.authErrText()
	if !strings.Contains(got, "Not logged in") {
		t.Errorf("authErrText = %q; want it to carry claude's reason", got)
	}
}

// TestRunReturnsErrNotLoggedIn drives a real Run against a fake claude that
// emits the not-logged-in stream and exits non-zero — the actual failure shape
// `ralph auto` hits. Run must classify it as ErrNotLoggedIn (so the runner
// requeues + halts with guidance) rather than a bare "claude exited" error.
func TestRunReturnsErrNotLoggedIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake claude uses a /bin/sh shebang")
	}
	dir := t.TempDir()
	// Emit the two events claude produces on an auth failure, then exit 1.
	script := `#!/bin/sh
printf '%s\n' '{"type":"assistant","error":"authentication_failed","message":{"content":[{"type":"text","text":"Not logged in · Please run /login"}]}}'
printf '%s\n' '{"type":"result","is_error":true,"result":"Not logged in · Please run /login","total_cost_usd":0,"duration_ms":12}'
exit 1
`
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}

	sess, err := NewSession(SessionConfig{
		Bin:       bin,
		WorkDir:   dir,
		OutputDir: filepath.Join(dir, ".ralph"),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	err = sess.Run(context.Background(), "do the thing")
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("Run error = %v; want ErrNotLoggedIn", err)
	}
	if !strings.Contains(err.Error(), "Not logged in") {
		t.Errorf("Run error %q should carry claude's reason", err)
	}
	// An auth failure establishes no usable session: the next Run must start
	// fresh (--session-id), not --resume.
	if sess.started {
		t.Error("auth failure must not mark the session started")
	}
}

func decodeEvent(t *testing.T, line string) *Event {
	t.Helper()
	var ev Event
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	return &ev
}
