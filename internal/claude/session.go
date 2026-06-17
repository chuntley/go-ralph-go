package claude

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// gracePeriodOnCancel is how long we let Claude clean up after SIGINT before
// the runtime force-kills it.
const gracePeriodOnCancel = 10 * time.Second

// Session drives a single Claude conversation across multiple turns, persisting
// stream-json output to the configured log files and rendering pretty text to
// the caller-supplied UI writer.
//
// A Run uses --session-id when starting fresh and --resume to continue the
// current conversation. The runner calls StartFresh before each refine turn to
// force a brand-new context (every refine pass starts clean), and lets the
// final cleanup turn --resume the last pass so it knows what was just done.
type Session struct {
	cfg SessionConfig

	id      string
	started bool

	mu     sync.Mutex // guards file handles + turn buffer below
	raw    *os.File   // raw stream-json
	pretty *os.File   // rendered pretty text
	stderr *os.File   // claude stderr

	// turn accumulates the assistant's streamed prose for the most recent Run
	// (text deltas only — not tool I/O). The runner scans it for a completion
	// signal to decide whether to end the loop early. Reset at the start of
	// each Run so LastTurnText reflects only the latest turn.
	turn strings.Builder

	// authReason holds the human-readable reason from an authentication-failure
	// event seen during the current Run (e.g. "Not logged in · Please run
	// /login"), or "" if none. Set in writeEvent, read in Run to return
	// ErrNotLoggedIn, and reset at the start of each Run.
	authReason string
}

type SessionConfig struct {
	// Bin is the claude executable. Defaults to "claude".
	Bin string

	// WorkDir is the directory in which claude runs (i.e. the project root).
	WorkDir string

	// ClaudeConfigDir, if non-empty, is exported as CLAUDE_CONFIG_DIR.
	ClaudeConfigDir string

	// OutputDir is the directory for raw/pretty/stderr logs.
	OutputDir string

	// UI receives the pretty render in real time (typically os.Stdout).
	UI io.Writer

	// ExtraEnv is appended to the inherited environment.
	ExtraEnv []string
}

// NewSession allocates a session with a generated UUID. Files are created
// lazily on first Run so an unused session leaves no artefacts.
func NewSession(cfg SessionConfig) (*Session, error) {
	if cfg.Bin == "" {
		cfg.Bin = "claude"
	}
	if cfg.UI == nil {
		cfg.UI = io.Discard
	}
	id, err := newUUID()
	if err != nil {
		return nil, err
	}
	return &Session{cfg: cfg, id: id}, nil
}

// ID returns the current session id.
func (s *Session) ID() string { return s.id }

// Reset starts a fresh Claude session and truncates the log files. Call
// between full cycles, not between turns: each cycle (refine iterations +
// cleanup pass) is one task and must not share context with the previous
// task. Without this, auto mode would resume the same session for every
// issue and Claude's context would compound across unrelated issues.
func (s *Session) Reset() error {
	if err := s.ensureLogs(); err != nil {
		return err
	}
	id, err := newUUID()
	if err != nil {
		return err
	}
	s.id = id
	s.started = false
	for _, f := range []*os.File{s.raw, s.pretty, s.stderr} {
		if err := f.Truncate(0); err != nil {
			return err
		}
		if _, err := f.Seek(0, 0); err != nil {
			return err
		}
	}
	return nil
}

// StartFresh rotates to a new session id and clears the started flag WITHOUT
// truncating the logs, so the next Run begins a brand-new Claude context
// (--session-id, not --resume) while the on-disk transcript keeps accumulating.
//
// The runner calls this before every refine turn so each turn runs with a fresh
// context: no memory of earlier turns. That removes self-bias toward
// already-written code and any awareness of being run repeatedly (which would
// invite pacing). Durable state crosses turns via the plan file + git, not the
// conversation. Unlike Reset, it preserves the logs and does not reset turn
// capture (Run does that).
func (s *Session) StartFresh() error {
	id, err := newUUID()
	if err != nil {
		return err
	}
	s.id = id
	s.started = false
	return nil
}

// Run executes one turn of the session against the given prompt.
func (s *Session) Run(ctx context.Context, prompt string) error {
	if err := s.ensureLogs(); err != nil {
		return err
	}
	s.mu.Lock()
	s.turn.Reset()
	s.authReason = ""
	s.mu.Unlock()

	args := []string{
		"-p",
		"--verbose",
		"--output-format", "stream-json",
		"--include-partial-messages",
	}
	if s.started {
		args = append(args, "--resume", s.id)
	} else {
		args = append(args, "--session-id", s.id)
	}
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, s.cfg.Bin, args...)
	cmd.Dir = s.cfg.WorkDir
	cmd.Env = s.buildEnv()
	cmd.Stderr = s.stderr
	// Graceful cancellation: SIGINT then SIGKILL after grace period. Default
	// exec.CommandContext SIGKILLs immediately, which can leave Claude state
	// half-written.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = gracePeriodOnCancel

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start claude: %w", err)
	}

	// Stream + decode + render in real time.
	streamErr := s.consume(stdout)
	waitErr := cmd.Wait()

	// An authentication failure is an environment problem, not a crash or a task
	// failure: claude exits non-zero, so its bare exit code is useless to the
	// operator. Surface it as the typed ErrNotLoggedIn (with claude's own reason)
	// so the runner can point at `claude auth login` and requeue rather than mark
	// the issue failed. Checked before the generic exit handling below, and
	// before setting started=true since no usable session was established.
	if reason := s.authErrText(); reason != "" {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: %s", ErrNotLoggedIn, reason)
	}

	if waitErr != nil {
		// If the process exited because ctx was canceled (Ctrl+C, SIGTERM),
		// surface the cancellation itself rather than the exec.ExitError that
		// followed from our cmd.Cancel handler. Callers (runner.dispatchCleanup)
		// distinguish "user interrupt → requeue the issue" from "claude crashed
		// → mark failed" via errors.Is(..., context.Canceled); without this the
		// exec error would mask the cancellation and the issue would be marked
		// failed instead of requeued.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		// Surface claude's error but include any stream parse error context.
		if streamErr != nil {
			return fmt.Errorf("claude exited (%w); stream error: %v", waitErr, streamErr)
		}
		return fmt.Errorf("claude exited: %w", waitErr)
	}
	s.started = true
	return streamErr
}

// authErrText returns the auth-failure reason captured during the current Run,
// or "" if none. Guarded by mu since writeEvent (the setter) runs on the stream
// goroutine.
func (s *Session) authErrText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authReason
}

// LastTurnText returns the assistant's streamed prose from the most recent
// Run (text only, no tool I/O). Used by the runner to scan for a completion
// signal. Returns "" before the first Run.
func (s *Session) LastTurnText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turn.String()
}

// WriteBanner appends a section banner to the pretty log (not the raw JSONL).
// The runner uses this to mirror UI banners into the on-disk transcript.
func (s *Session) WriteBanner(text string) error {
	if err := s.ensureLogs(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pretty == nil {
		return nil
	}
	_, err := s.pretty.WriteString(text)
	return err
}

// Close flushes and closes log file handles.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	for _, f := range []*os.File{s.raw, s.pretty, s.stderr} {
		if f == nil {
			continue
		}
		if err := f.Close(); err != nil && first == nil {
			first = err
		}
	}
	s.raw, s.pretty, s.stderr = nil, nil, nil
	return first
}

func (s *Session) ensureLogs() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.raw != nil {
		return nil
	}
	// 0o700: session logs contain the full Claude transcript (tool inputs and
	// results) which may include credentials Claude observed during the run.
	// Owner-only avoids leaking them to other local users on shared machines.
	if err := os.MkdirAll(s.cfg.OutputDir, 0o700); err != nil {
		return err
	}
	// MkdirAll is a no-op mode-wise on an existing dir, so explicitly tighten
	// pre-existing .ralph/ dirs created by older ralph versions (which used
	// 0o755). Without this, the upgrade path silently keeps world-readable
	// perms on the very logs we're trying to protect.
	if err := os.Chmod(s.cfg.OutputDir, 0o700); err != nil {
		return err
	}
	var err error
	if s.raw, err = openLog(filepath.Join(s.cfg.OutputDir, "output.jsonl")); err != nil {
		return err
	}
	if s.pretty, err = openLog(filepath.Join(s.cfg.OutputDir, "output.txt")); err != nil {
		return err
	}
	if s.stderr, err = openLog(filepath.Join(s.cfg.OutputDir, "output.stderr")); err != nil {
		return err
	}
	return nil
}

func openLog(p string) (*os.File, error) {
	// 0o600: see ensureLogs — these logs may contain credentials and are not
	// meant to be readable by other local users.
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	// O_CREATE only applies mode to a freshly-created file. Pre-existing logs
	// from older ralph versions were created with 0o644; explicitly Chmod so
	// upgraders don't silently keep world-readable transcripts.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (s *Session) buildEnv() []string {
	env := os.Environ()
	if s.cfg.ClaudeConfigDir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+s.cfg.ClaudeConfigDir)
	}
	env = append(env, s.cfg.ExtraEnv...)
	return env
}

// consume reads stream-json lines from r, tees raw bytes to s.raw, decodes
// each line into an Event, renders pretty text to s.pretty and s.cfg.UI.
//
// Malformed lines are written to raw + skipped; they should not abort the run.
func (s *Session) consume(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	// Allow long lines (assistant messages with embedded base64 etc).
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if err := s.writeEvent(line); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// writeEvent serialises a single decoded event to the three sinks (raw log,
// pretty log, UI) under s.mu so a concurrent WriteBanner cannot interleave
// half-flushed text into the pretty log.
func (s *Session) writeEvent(line []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.raw.Write(append(append([]byte{}, line...), '\n')); err != nil {
		return fmt.Errorf("write raw log: %w", err)
	}
	var ev Event
	if err := json.Unmarshal(line, &ev); err != nil {
		return nil // malformed JSON line: skip pretty/UI but keep raw above
	}
	// Capture an authentication failure so Run can return ErrNotLoggedIn. Keep
	// the first reason seen (claude emits it on the assistant event before the
	// terminal result event repeats it).
	if s.authReason == "" {
		if reason := authErrorReason(&ev); reason != "" {
			s.authReason = reason
		}
	}
	// Accumulate assistant prose (text deltas only) so the runner can detect a
	// completion signal. Tool inputs/results are deliberately excluded — they
	// would create false positives if a tool echoed the sentinel token.
	if ev.Type == "stream_event" {
		s.turn.WriteString(renderStream(ev.Event))
	}
	if err := Render(s.pretty, &ev); err != nil {
		return fmt.Errorf("write pretty log: %w", err)
	}
	if err := Render(s.cfg.UI, &ev); err != nil {
		return fmt.Errorf("write ui: %w", err)
	}
	return nil
}

// newUUID produces a v4-ish UUID. We don't import google/uuid for one helper.
func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]), nil
}
