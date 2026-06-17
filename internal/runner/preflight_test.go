package runner

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/chuntley/go-ralph-go/internal/config"
)

// fakeClaude writes an executable stub at <dir>/claude that answers
// `claude auth status` with the given JSON and exit code, and returns its path.
// Used to drive newRun's auth preflight without a real claude.
func fakeClaude(t *testing.T, dir, statusJSON string, statusExit int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake claude uses a /bin/sh shebang")
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"auth\" ] && [ \"$2\" = \"status\" ]; then\n" +
		"  printf '%s\\n' '" + statusJSON + "'\n" +
		"  exit " + strconv.Itoa(statusExit) + "\n" +
		"fi\n" +
		"exit 0\n"
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return bin
}

func newRunTestConfig(t *testing.T, bin string) *config.Config {
	t.Helper()
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.ProjectRoot = root
	cfg.ClaudeBin = bin
	// Pin the config dir so EffectiveClaudeConfigDir is deterministic regardless
	// of any ambient CLAUDE_CONFIG_DIR in the test environment.
	cfg.ClaudeConfigDir = filepath.Join(root, ".claude")
	return &cfg
}

func TestNewRunPreflightBlocksWhenLoggedOut(t *testing.T) {
	dir := t.TempDir()
	bin := fakeClaude(t, dir, `{"loggedIn":false}`, 0)
	cfg := newRunTestConfig(t, bin)

	r, err := newRun(cfg)
	if r != nil {
		r.close()
		t.Fatal("expected newRun to return a nil run when logged out")
	}
	if err == nil {
		t.Fatal("expected newRun to fail the auth preflight when logged out")
	}
	for _, want := range []string{"not logged in", "claude auth login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("preflight error %q missing %q", err.Error(), want)
		}
	}
}

func TestNewRunPreflightProceedsWhenLoggedIn(t *testing.T) {
	dir := t.TempDir()
	bin := fakeClaude(t, dir, `{"loggedIn":true,"email":"a@b.co","subscriptionType":"max"}`, 0)
	cfg := newRunTestConfig(t, bin)

	r, err := newRun(cfg)
	if err != nil {
		t.Fatalf("newRun should succeed when logged in: %v", err)
	}
	t.Cleanup(func() { r.close() })
	if !r.authKnown || !r.authStatus.LoggedIn {
		t.Errorf("expected cached authKnown+LoggedIn; got known=%v status=%+v", r.authKnown, r.authStatus)
	}
}

func TestNewRunPreflightProceedsWhenStatusUnavailable(t *testing.T) {
	// `claude auth status` failing (older claude, transient, denied keychain
	// prompt) is inconclusive — newRun must NOT block; the run proceeds and any
	// real auth failure is caught mid-run via claude.ErrNotLoggedIn.
	dir := t.TempDir()
	bin := fakeClaude(t, dir, ``, 1)
	cfg := newRunTestConfig(t, bin)

	r, err := newRun(cfg)
	if err != nil {
		t.Fatalf("newRun should proceed when status is unavailable: %v", err)
	}
	t.Cleanup(func() { r.close() })
	if r.authKnown {
		t.Errorf("expected authKnown=false when status unavailable")
	}
}
