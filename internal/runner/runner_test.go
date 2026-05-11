package runner

import (
	"strings"
	"testing"

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

func TestRenderRefinePromptIterationAware(t *testing.T) {
	cfg := config.Defaults()
	got := renderRefinePrompt(&cfg, 3, 5)
	if !strings.Contains(got, "iteration 3 of 5") {
		t.Errorf("missing iteration counter: %q", got)
	}
	if !strings.Contains(got, "run all 5 iterations regardless") {
		t.Errorf("missing anti-early-exit guard: %q", got)
	}
}

func TestRenderRefinePromptHasAuditAndFixSections(t *testing.T) {
	cfg := config.Defaults()
	got := renderRefinePrompt(&cfg, 1, 5)
	for _, want := range []string{"AUDIT:", "FIX:", "do not declare the work complete"} {
		if !strings.Contains(got, want) {
			t.Errorf("default refine prompt missing %q: %q", want, got)
		}
	}
}

func TestRenderRefinePromptCustomTemplate(t *testing.T) {
	cfg := config.Defaults()
	cfg.RefinePrompt = "pass {{iter}}/{{total}}"
	if got := renderRefinePrompt(&cfg, 2, 7); got != "pass 2/7" {
		t.Errorf("substitution failed: %q", got)
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
