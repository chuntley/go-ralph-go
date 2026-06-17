package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/chuntley/go-ralph-go/internal/claude"
	"github.com/chuntley/go-ralph-go/internal/config"
	"github.com/chuntley/go-ralph-go/internal/git"
	"github.com/chuntley/go-ralph-go/internal/vcs"
	ghprov "github.com/chuntley/go-ralph-go/internal/vcs/github"
	glprov "github.com/chuntley/go-ralph-go/internal/vcs/gitlab"
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose ralph's environment in the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd.Context(), cmd.OutOrStdout())
		},
	}
}

// runDoctor probes each thing ralph relies on and prints a pass/fail report.
// It never returns an error for individual checks — the report itself is the
// output. A non-nil error is reserved for catastrophic problems (e.g. cwd
// inaccessible).
func runDoctor(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "ralph doctor")
	fmt.Fprintf(out, "         version      : %s\n", resolveVersion())
	fmt.Fprintln(out)

	// 1. Config
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(out, "  [FAIL] load config: %v\n", err)
		return nil
	}
	src := cfg.ConfigPath
	if src == "" {
		src = "(defaults; no .go-ralph-go found)"
	}
	fmt.Fprintf(out, "  [OK]   project root : %s\n", cfg.ProjectRoot)
	fmt.Fprintf(out, "         config       : %s\n", src)
	fmt.Fprintf(out, "         passes       : min %d, max %d\n", cfg.MinIterations, cfg.Iterations)
	if cfg.VerifyCommand != "" {
		fmt.Fprintf(out, "         verify cmd   : %s\n", cfg.VerifyCommand)
	} else {
		fmt.Fprintf(out, "         verify cmd   : (none — completion signal trusted as-is)\n")
	}

	// 2. Claude binary
	if path, err := claude.LookupBin(cfg.ClaudeBin); err != nil {
		fmt.Fprintf(out, "  [FAIL] claude binary : %v\n", err)
	} else {
		fmt.Fprintf(out, "  [OK]   claude binary: %s\n", path)
	}
	effCfgDir := cfg.EffectiveClaudeConfigDir()
	source := ""
	if cfg.ClaudeConfigDir == "" && effCfgDir != "" {
		source = " (from $CLAUDE_CONFIG_DIR)"
	}
	if effCfgDir != "" {
		if st, err := os.Stat(effCfgDir); err != nil || !st.IsDir() {
			fmt.Fprintf(out, "  [FAIL] CLAUDE_CONFIG_DIR: %s — directory missing%s\n", effCfgDir, source)
		} else {
			fmt.Fprintf(out, "  [OK]   CLAUDE_CONFIG_DIR: %s%s\n", effCfgDir, source)
		}
	} else {
		fmt.Fprintln(out, "         CLAUDE_CONFIG_DIR: (system-wide — default ~/.claude; opt in via claude_config_dir in .go-ralph-go)")
	}
	// Auth state from `claude auth status` — the real source of truth. On macOS
	// credentials live in the Keychain, not a .credentials.json file, so a file
	// check would be unreliable.
	authHint := "claude auth login"
	statusHint := "claude auth status"
	if effCfgDir != "" {
		authHint = fmt.Sprintf("CLAUDE_CONFIG_DIR=%q claude auth login", effCfgDir)
		statusHint = fmt.Sprintf("CLAUDE_CONFIG_DIR=%q claude auth status", effCfgDir)
	}
	switch st, ok := claude.QueryAuthStatus(cfg.ClaudeBin, effCfgDir); {
	case ok && st.LoggedIn:
		plan := st.SubscriptionType
		if plan == "" {
			plan = "—"
		}
		fmt.Fprintf(out, "  [OK]   claude auth : logged in as %s (plan: %s)\n", st.Email, plan)
	case ok && !st.LoggedIn:
		fmt.Fprintf(out, "  [WARN] claude auth : NOT logged in for this config dir — run `%s`\n", authHint)
	default:
		// Couldn't run `claude auth status` (older claude, transient failure, or a
		// denied keychain prompt). Fall back to .claude.json metadata — but flag it
		// as a WARN, never an OK: that metadata is just a cached account label, NOT
		// proof of a usable login. A project-local config dir can carry the
		// metadata while having no credential at all (the classic onboarding
		// footgun), so tell the operator to verify before trusting it.
		if acct := claude.LoadAccount(effCfgDir); !acct.Empty() {
			fmt.Fprintf(out, "  [WARN] claude auth : could not verify login; .claude.json metadata says %s\n", acct.Label())
			fmt.Fprintf(out, "                       metadata is NOT a login — confirm with `%s` (then `%s` if needed)\n", statusHint, authHint)
		} else {
			fmt.Fprintf(out, "  [WARN] claude auth : could not verify login — confirm with `%s`\n", statusHint)
		}
	}

	// 3. Git
	if !git.IsRepo(ctx, cfg.ProjectRoot) {
		fmt.Fprintln(out, "  [WARN] not inside a git repo — only `ralph run` (local) will work")
		return nil
	}
	fmt.Fprintln(out, "  [OK]   git          : repo detected")

	clean, _ := git.IsClean(ctx, cfg.ProjectRoot)
	if clean {
		fmt.Fprintln(out, "  [OK]   working tree : clean")
	} else {
		fmt.Fprintln(out, "  [WARN] working tree : has uncommitted changes (commit or stash before issue/auto modes)")
	}

	branch, _ := git.DefaultBranch(ctx, cfg.ProjectRoot)
	fmt.Fprintf(out, "         default branch: %s\n", branch)

	// 4. Remote + provider
	remoteURL, err := git.OriginURL(ctx, cfg.ProjectRoot)
	if err != nil {
		fmt.Fprintln(out, "  [WARN] origin remote: none configured — issue/auto modes unavailable")
		return nil
	}
	r, err := vcs.ParseRemote(remoteURL)
	if err != nil {
		fmt.Fprintf(out, "  [FAIL] parse origin url: %v\n", err)
		return nil
	}
	provider := vcs.GuessProvider(r.Host)
	if provider == "" {
		fmt.Fprintf(out, "  [WARN] unrecognised host %q — set provider in .go-ralph-go to use issue/auto modes\n", r.Host)
		return nil
	}
	fmt.Fprintf(out, "  [OK]   host         : %s (provider=%s, project=%s)\n", r.Host, provider, r.Project)

	// 5. Host token discovery + live validation via Whoami.
	tok, err := vcs.DiscoverToken(ctx, provider)
	if err != nil {
		fmt.Fprintf(out, "  [FAIL] %s auth   : %v\n", provider, err)
		fmt.Fprintln(out)
		return nil
	}

	var prov vcs.Provider
	switch provider {
	case "github":
		baseURL := cfg.GitHub.BaseURL
		prov, err = ghprov.New(tok, r.Owner, r.Repo, baseURL)
	case "gitlab":
		baseURL := ""
		if r.Host != "gitlab.com" {
			baseURL = "https://" + r.Host + "/"
		}
		prov, err = glprov.New(tok, r.Project, baseURL)
	}
	if err != nil {
		fmt.Fprintf(out, "  [FAIL] %s client : %v\n", provider, err)
		fmt.Fprintln(out)
		return nil
	}
	user, err := prov.Whoami(ctx)
	if err != nil {
		fmt.Fprintf(out, "  [FAIL] %s auth   : token discovered but call failed: %v\n", provider, err)
	} else {
		fmt.Fprintf(out, "  [OK]   %s auth   : authenticated as %s\n", provider, user)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Run `ralph run \"<prompt>\"` for a local pass, or `ralph auto --once` to dry the full flow.")
	return nil
}
