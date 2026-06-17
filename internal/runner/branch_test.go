package runner

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Remove the marketing text on the home screen", "remove-the-marketing-text-on-the-home"}, // capped at 40
		{"Fix Done button padding", "fix-done-button-padding"},
		{"  Trim/collapse   ---  punctuation!!! ", "trim-collapse-punctuation"},
		{"Issue #12: 50% faster", "issue-12-50-faster"},
		{"UPPER CASE", "upper-case"},
		{"", "task"},
		{"!!!@@@###", "task"},
		{"café déjà", "caf-d-j"}, // non-ASCII dropped, dashes collapsed/trimmed
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := slugify(tc.in)
			if got != tc.want {
				t.Errorf("slugify(%q) = %q; want %q", tc.in, got, tc.want)
			}
			if len(got) > slugMaxLen {
				t.Errorf("slugify(%q) = %q exceeds cap %d", tc.in, got, slugMaxLen)
			}
			if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
				t.Errorf("slugify(%q) = %q has leading/trailing dash", tc.in, got)
			}
		})
	}
}

func TestIssueBranchName(t *testing.T) {
	if got := issueBranchName("ralph", 3, "Remove marketing text"); got != "ralph/issue-3-remove-marketing-text" {
		t.Errorf("issueBranchName = %q", got)
	}
	// Empty prefix drops the namespace.
	if got := issueBranchName("", 7, "Fix thing"); got != "issue-7-fix-thing" {
		t.Errorf("issueBranchName (no prefix) = %q", got)
	}
	// A prefix with stray slashes is normalised to a single path segment.
	if got := issueBranchName("/ralph/", 1, "x"); got != "ralph/issue-1-x" {
		t.Errorf("issueBranchName (slashes) = %q", got)
	}
}

func TestRunBranchName(t *testing.T) {
	if got := runBranchName("ralph", "20260617-143022"); got != "ralph/run-20260617-143022" {
		t.Errorf("runBranchName = %q", got)
	}
	if got := runBranchName("", "20260617-143022"); got != "run-20260617-143022" {
		t.Errorf("runBranchName (no prefix) = %q", got)
	}
}

func TestBranchContextNoteWarnsOffDefault(t *testing.T) {
	note := branchContextNote("ralph/issue-3-x", "master")
	for _, want := range []string{"ralph/issue-3-x", "master", "Do NOT"} {
		if !strings.Contains(note, want) {
			t.Errorf("branchContextNote missing %q; got %q", want, note)
		}
	}
}
