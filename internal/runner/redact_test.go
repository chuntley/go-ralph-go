package runner

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chuntley/go-ralph-go/internal/claude"
	"github.com/chuntley/go-ralph-go/internal/config"
	"github.com/chuntley/go-ralph-go/internal/vcs"
)

func TestRedactSecrets(t *testing.T) {
	// Test fixtures are built by concatenation so token-shaped substrings
	// ("ghp_<20+>", "glpat-<20+>", etc.) never appear as continuous literals
	// in source — GitHub's push-protection scanner would otherwise block any
	// push of this file. The runtime values still trigger the regexes under
	// test, but the source contains only the prefixes.
	ghpTok := "ghp_" + "abcdefghijklmnopqrst"
	ghpTok2 := "ghp_" + "0123456789abcdefghij1234"
	githubPat := "github_pat_" + "11ABCDEFG0abcdefghij1234567890"
	glpat := "glpat-" + "AbCdEf1234567890XyZW"

	cases := []struct {
		name    string
		in      string
		want    string // exact match
		mustNot string // substring that must be absent (for "redacted" assertions)
	}{
		{
			name: "https with x-access-token userinfo",
			in:   "fatal: unable to access 'https://x-access-token:" + ghpTok + "@github.com/o/r.git/'",
			want: "fatal: unable to access 'https://***@github.com/o/r.git/'",
		},
		{
			name: "https with user:password userinfo",
			in:   "remote: https://alice:hunter2@gitlab.example.com/foo/bar.git failed",
			want: "remote: https://***@gitlab.example.com/foo/bar.git failed",
		},
		{
			name:    "bare github PAT in plain text",
			in:      "auth: " + ghpTok2 + " rejected",
			mustNot: ghpTok2,
		},
		{
			name:    "github_pat_ prefix",
			in:      "auth failed: " + githubPat,
			mustNot: githubPat,
		},
		{
			name:    "gitlab PAT",
			in:      "401: " + glpat + " returned 401",
			mustNot: glpat,
		},
		{
			name: "no secrets, passthrough",
			in:   "merge: branch protection rejected",
			want: "merge: branch protection rejected",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSecrets(tc.in)
			if tc.want != "" && got != tc.want {
				t.Errorf("redactSecrets:\n  in:   %q\n  want: %q\n  got:  %q", tc.in, tc.want, got)
			}
			if tc.mustNot != "" && strings.Contains(got, tc.mustNot) {
				t.Errorf("redactSecrets leaked secret substring %q\n  in:  %q\n  got: %q", tc.mustNot, tc.in, got)
			}
		})
	}
}

func TestCleanupDispatchRedactsSecretInReason(t *testing.T) {
	p := &fakeProvider{}
	tok := "ghp_" + "abcdefghijklmnopqrst" // see TestRedactSecrets for split rationale
	err := fmt.Errorf("sync main: %w", errors.New("fatal: https://x-access-token:"+tok+"@github.com/o/r.git/ not accessible"))
	dispatchCleanup(p, 7, vcs.Labels{}, err)
	if p.failedReason == "" {
		t.Fatal("expected MarkFailed to be invoked")
	}
	if strings.Contains(p.failedReason, tok) {
		t.Errorf("token leaked into public MarkFailed reason: %q", p.failedReason)
	}
	if !strings.Contains(p.failedReason, "***@github.com") {
		t.Errorf("expected redacted userinfo marker in reason, got %q", p.failedReason)
	}
}

// TestRunLogRedactsSecretsBeforeWriting guards the second redaction boundary:
// r.log writes to both stdout (developer terminal, CI capture) and to the
// pretty log on disk. A token wrapped in a logged error string must not
// survive either sink.
func TestRunLogRedactsSecretsBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	sess, err := claude.NewSession(claude.SessionConfig{
		Bin:       "claude",
		WorkDir:   dir,
		OutputDir: filepath.Join(dir, ".ralph"),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	cfg := config.Defaults()
	cfg.ProjectRoot = dir
	var buf bytes.Buffer
	r := &run{cfg: &cfg, session: sess, ui: &buf}

	tok := "ghp_" + "abcdefghijklmnopqrst" // see TestRedactSecrets for split rationale
	r.log("Issue #5 did not complete cleanly: fatal: https://x-access-token:" + tok + "@github.com/o/r.git/")

	out := buf.String()
	if strings.Contains(out, tok) {
		t.Errorf("token leaked into r.log stdout sink: %q", out)
	}
	if !strings.Contains(out, "***@github.com") {
		t.Errorf("expected redaction marker in stdout sink, got %q", out)
	}
}
