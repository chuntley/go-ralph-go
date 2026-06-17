package runner

import (
	"fmt"
	"strings"
)

// slugMaxLen caps the title-derived portion of a branch name. Long enough to
// stay recognisable, short enough that the full ref doesn't get unwieldy.
const slugMaxLen = 40

// slugify turns an issue title into a lower-kebab, branch-safe slug. Runs of
// non-[a-z0-9] collapse to a single dash, leading/trailing dashes are trimmed,
// and the result is capped at slugMaxLen. Returns "task" when nothing usable
// remains (e.g. a title that is all punctuation or non-ASCII) so the branch
// name is always well-formed. Output is pure [a-z0-9-], so byte length == rune
// length and the cap is safe to apply on bytes.
func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if b.Len() > 0 && !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > slugMaxLen {
		slug = slug[:slugMaxLen]
		// Trim back to the last word boundary so the name doesn't end mid-word
		// (e.g. "...home-screen" capped to "...home-sc"). Fall back to the hard
		// cut when there's no dash (a single token longer than the cap).
		if i := strings.LastIndex(slug, "-"); i > 0 {
			slug = slug[:i]
		}
		slug = strings.Trim(slug, "-")
	}
	if slug == "" {
		return "task"
	}
	return slug
}

// issueBranchName builds the working-branch name for an issue, e.g.
// "ralph/issue-3-remove-marketing-text" (prefix "ralph").
func issueBranchName(prefix string, issueNum int, title string) string {
	return joinBranchPrefix(prefix, fmt.Sprintf("issue-%d-%s", issueNum, slugify(title)))
}

// runBranchName builds the working-branch name for an ad-hoc `run --pr`, e.g.
// "ralph/run-20260617-143022" (prefix "ralph"). stamp is a caller-supplied
// timestamp so the name is unique per run.
func runBranchName(prefix, stamp string) string {
	return joinBranchPrefix(prefix, "run-"+stamp)
}

// joinBranchPrefix prepends the configured namespace (default "ralph") as a
// path segment. An empty/slash-only prefix yields the bare base name.
func joinBranchPrefix(prefix, base string) string {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return base
	}
	return prefix + "/" + base
}

// branchContextNote is prepended to the work prompt so Claude knows ralph has
// already put it on a dedicated branch and must not commit to the protected
// default branch. This is the prompt-side complement to the code guardrail
// (ralph creating the branch): the code makes the common case safe regardless
// of instructions, and this note neutralises the one residual path — Claude
// explicitly checking out the default branch and committing there. Deliberately
// worded to coexist with a project's own AGENTS.md branch rules.
func branchContextNote(branch, defaultBranch string) string {
	return fmt.Sprintf(
		"You are already on a dedicated git branch for this task: `%s` (created from `%s`). "+
			"Make ALL commits on this branch. Do NOT switch to, commit to, or push `%s` — "+
			"ralph opens the pull request from your branch after the loop finishes.",
		branch, defaultBranch, defaultBranch,
	)
}
