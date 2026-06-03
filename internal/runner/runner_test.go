package runner

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/chuntley/go-ralph-go/internal/claude"
	"github.com/chuntley/go-ralph-go/internal/config"
	"github.com/chuntley/go-ralph-go/internal/vcs"
)

func TestRenderCleanupAdHoc(t *testing.T) {
	cfg := config.Defaults()
	cfg.InstructionsDoc = "AGENTS.md"
	got := renderCleanup(&cfg, 0)
	if !strings.Contains(got, "AGENTS.md") {
		t.Errorf("missing instructions doc: %q", got)
	}
	if strings.Contains(got, "Closes #") {
		t.Errorf("ad-hoc should not include closes clause: %q", got)
	}
	if strings.Contains(got, "issue #") {
		t.Errorf("ad-hoc should not include issue clause: %q", got)
	}
}

func TestRenderCleanupIssue(t *testing.T) {
	cfg := config.Defaults()
	cfg.InstructionsDoc = "CLAUDE.md"
	got := renderCleanup(&cfg, 42)
	for _, want := range []string{"CLAUDE.md", "issue #42", "Closes #42"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %q", want, got)
		}
	}
}

func TestRenderIssuePrompt(t *testing.T) {
	cfg := config.Defaults()
	issue := &vcs.Issue{Number: 7, Title: "Fix login", Body: "Steps to repro:\n1. ..."}
	got := renderIssuePrompt(&cfg, "github", issue)
	for _, want := range []string{"github", "#7", "Fix login", "Steps to repro"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %q", want, got)
		}
	}
}

func TestRenderIssuePromptPlaceholderInBodyNotReprocessed(t *testing.T) {
	cfg := config.Defaults()
	// Body contains a literal "{{title}}" — must NOT be re-substituted into
	// the title's slot after the body lands.
	issue := &vcs.Issue{Number: 1, Title: "real title", Body: "see {{title}} above"}
	got := renderIssuePrompt(&cfg, "github", issue)
	if !strings.Contains(got, "see {{title}} above") {
		t.Errorf("body placeholder was re-substituted: %q", got)
	}
	// The actual title placeholder should still be filled in once.
	if !strings.Contains(got, "real title") {
		t.Errorf("title not substituted: %q", got)
	}
}

func TestRenderIssuePromptPlaceholderInTitleNotReprocessed(t *testing.T) {
	cfg := config.Defaults()
	// Title contains literal "{{body}}" — must remain literal in output.
	issue := &vcs.Issue{Number: 1, Title: "fix {{body}} crash", Body: "Steps: …"}
	got := renderIssuePrompt(&cfg, "github", issue)
	if !strings.Contains(got, "fix {{body}} crash") {
		t.Errorf("title placeholder was re-substituted with body: %q", got)
	}
}

func TestDefaultRefinePromptIsIterationAgnostic(t *testing.T) {
	got := config.Defaults().RefinePrompt
	// Claude must never learn how many passes will run, so the prompt carries
	// neither the legacy {{iter}}/{{total}} placeholders nor any "iteration N
	// of M" phrasing.
	for _, banned := range []string{"{{iter}}", "{{total}}", "iteration", "of {{total}}", "passes regardless"} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(banned)) {
			t.Errorf("refine prompt leaks iteration awareness via %q: %q", banned, got)
		}
	}
}

func TestDefaultRefinePromptIsGoalDriven(t *testing.T) {
	got := config.Defaults().RefinePrompt
	// The goal-driven loop hinges on these sections + the durable-state and
	// completion-signal placeholders being present.
	for _, want := range []string{"ORIENT:", "AUDIT:", "WORK:", "VERIFY:", "COMMIT:", "COMPLETION:", "{{plan_file}}", "{{sentinel}}", "{{verify}}"} {
		if !strings.Contains(got, want) {
			t.Errorf("default refine prompt missing %q", want)
		}
	}
	// Guard against self-bias and rubber-stamping: the prompt must force
	// re-review of prior work, scrutiny of the tests themselves, and reject the
	// "nothing changed / already verified, so it's done" rationalization.
	for _, want := range []string{
		"earlier passes", "adversarial", "skeptical",
		"nothing changed since the last pass", // counters the exact failure observed
		"re-derive",                           // tests must be re-derived from the goal
		"necessary but NOT sufficient",        // green tests aren't proof
	} {
		if !strings.Contains(got, want) {
			t.Errorf("refine prompt missing anti-rubber-stamp guidance %q", want)
		}
	}
}

func TestRenderRefinePromptSubstitutes(t *testing.T) {
	cfg := config.Defaults()
	cfg.CompletionSentinel = "DONE_TOKEN"
	got := renderRefinePrompt(&cfg, ".ralph/plan.md")
	// No template placeholder of any kind may survive rendering — catches a
	// stray/typo'd "{{...}}" the targeted checks below would miss.
	if strings.Contains(got, "{{") {
		t.Errorf("unsubstituted placeholder remains in rendered prompt: %q", got)
	}
	for _, leftover := range []string{"{{plan_file}}", "{{sentinel}}", "{{verify}}"} {
		if strings.Contains(got, leftover) {
			t.Errorf("placeholder %q was not substituted", leftover)
		}
	}
	if !strings.Contains(got, ".ralph/plan.md") {
		t.Errorf("plan file path not substituted: %q", got)
	}
	if !strings.Contains(got, "DONE_TOKEN") {
		t.Errorf("sentinel not substituted: %q", got)
	}
	// With no VerifyCommand, the verify clause is the generic fallback.
	if !strings.Contains(got, "full build, test, and lint suite") {
		t.Errorf("expected generic verify clause: %q", got)
	}
}

func TestRenderRefinePromptVerifyCommandClause(t *testing.T) {
	cfg := config.Defaults()
	cfg.VerifyCommand = "go test ./..."
	got := renderRefinePrompt(&cfg, ".ralph/plan.md")
	if !strings.Contains(got, "go test ./...") {
		t.Errorf("verify command not surfaced in prompt: %q", got)
	}
	if !strings.Contains(got, "independently") {
		t.Errorf("expected harness-verify clause when VerifyCommand set: %q", got)
	}
}

func TestSawSentinel(t *testing.T) {
	const tok = "RALPH_GOAL_COMPLETE"
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"own line", "all done\nRALPH_GOAL_COMPLETE\n", true},
		{"own line trailing space", "work\n  RALPH_GOAL_COMPLETE  \n", true},
		{"only line", "RALPH_GOAL_COMPLETE", true},
		{"mid sentence not matched", "I will print RALPH_GOAL_COMPLETE when done.", false},
		{"absent", "still working on it\n", false},
		{"empty text", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sawSentinel(tc.text, tok); got != tc.want {
				t.Errorf("sawSentinel(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
	if sawSentinel("anything", "") {
		t.Error("empty sentinel must never match")
	}
}

func TestClaudeAcctLine(t *testing.T) {
	meta := claude.Account{BillingType: "stripe_subscription", Organization: "MetaOrg"}
	// Logged in → authoritative email + subscription plan + org (from status).
	st := claude.AuthStatus{LoggedIn: true, Email: "you@example.com", SubscriptionType: "max", OrgName: "Acme"}
	if got := claudeAcctLine(meta, st, true); got != "you@example.com  ·  max plan  ·  Acme" {
		t.Errorf("logged-in line = %q", got)
	}
	// Status says NOT logged in → metadata flagged as unverified.
	if got := claudeAcctLine(meta, claude.AuthStatus{LoggedIn: false}, true); !strings.Contains(got, "NOT logged in") {
		t.Errorf("expected not-logged-in flag, got %q", got)
	}
	// Status unavailable → plain metadata label, no flag.
	if got := claudeAcctLine(meta, claude.AuthStatus{}, false); strings.Contains(got, "NOT logged in") {
		t.Errorf("status-unavailable should not flag: %q", got)
	}
}

func TestAuthLoginHint(t *testing.T) {
	if got := authLoginHint(""); got != "claude auth login" {
		t.Errorf("default hint = %q", got)
	}
	if got := authLoginHint("/p/.claude"); got != `CLAUDE_CONFIG_DIR="/p/.claude" claude auth login` {
		t.Errorf("custom-dir hint = %q", got)
	}
}

func TestHonorsCompletion(t *testing.T) {
	const min = 5
	// Passes 1–4 must ignore an early completion signal; pass 5+ may honour it.
	for i := 1; i <= 4; i++ {
		if honorsCompletion(i, min) {
			t.Errorf("pass %d should NOT honour completion (min=%d)", i, min)
		}
	}
	for i := 5; i <= 7; i++ {
		if !honorsCompletion(i, min) {
			t.Errorf("pass %d should honour completion (min=%d)", i, min)
		}
	}
	// min=1 means a first-pass completion is allowed.
	if !honorsCompletion(1, 1) {
		t.Error("with min=1, pass 1 should honour completion")
	}
}

func TestRunVerify(t *testing.T) {
	ctx := context.Background()
	if ok, _ := runVerify(ctx, t.TempDir(), "true"); !ok {
		t.Error("expected `true` to verify ok")
	}
	ok, out := runVerify(ctx, t.TempDir(), "echo boom >&2; exit 3")
	if ok {
		t.Error("expected nonzero exit to fail verification")
	}
	if !strings.Contains(out, "boom") || !strings.Contains(out, "exit:") {
		t.Errorf("failure output should include stderr + exit annotation: %q", out)
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in, want string
		n        int
	}{
		{"short", "short", 100},
		{"exactly10c", "exactly10c", 10},
		{"this is a very long error message that needs trimming for sure", "this is a very long error …[truncated]", 40},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := truncate(tc.in, tc.n)
			if got != tc.want {
				t.Errorf("truncate(%q,%d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

// TestTruncatePreservesUTF8 — truncating across a multi-byte rune must not
// leave an orphan continuation byte in the result, since the output is posted
// as a public issue/MR comment.
func TestTruncatePreservesUTF8(t *testing.T) {
	// 20 copies of 中 = 60 bytes. With n=20 the byte cut at n-len(marker)=5
	// would land mid-rune; we expect the function to snap back to the
	// preceding rune boundary.
	s := strings.Repeat("中", 20)
	for _, n := range []int{5, 7, 13, 20, 40, 59} {
		got := truncate(s, n)
		if !utf8.ValidString(got) {
			t.Errorf("truncate(中×20, %d) produced invalid UTF-8: %q", n, got)
		}
		if len(got) > n {
			t.Errorf("truncate(中×20, %d) exceeded budget: len=%d", n, len(got))
		}
	}
}
