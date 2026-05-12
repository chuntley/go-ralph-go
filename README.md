# go-ralph-go

A single-binary CLI that drives [Claude Code](https://docs.claude.com/claude-code) through a [Ralph Wiggums-style refine loop](https://ghuntley.com/ralph/) on your project — locally, against a single GitHub/GitLab issue, or as a polling worker that picks up issues labelled `ready` and ships them as merged PRs.

---

## What it does

For each task ralph receives, it:

1. **Refines N times** (default 5) — runs Claude on the work prompt with a "double-check your work" refine instruction appended each pass, resuming the same session so context compounds.
2. **Cleans up** — runs a final pass telling Claude to push a branch and open a PR (matching your project's `AGENTS.md` / `CLAUDE.md` style guide).
3. **Validates** — waits for CI checks via the host API.
4. **Merges** — squash-merges, deletes the source branch, and closes the issue.
5. **Resets** — checks out the default branch and fast-forwards, leaving your repo clean for the next loop.

It does this in three modes:

| Command                     | When to use                                    | Needs git host? |
| --------------------------- | ---------------------------------------------- | --------------- |
| `ralph run "<prompt>"`      | Ad-hoc local work; refine loop + commit        | No              |
| `ralph run --pr "<prompt>"` | Ad-hoc work, open a PR, no auto-merge          | Yes             |
| `ralph issue <N>`           | Single issue end-to-end (PR + merge)           | Yes             |
| `ralph auto`                | Poll for `ready`-labelled issues and ship them | Yes             |
| `ralph auto --once`         | Same, but exit after one cycle                 | Yes             |

Supports **GitHub** (including GitHub Enterprise) and **GitLab** (cloud and self-hosted). The provider is auto-detected from the `origin` remote.

---

## Install

```bash
go install github.com/chuntley/go-ralph-go/cmd/ralph@latest
```

Or build from source:

```bash
git clone https://github.com/chuntley/go-ralph-go.git
cd go-ralph-go
go build -o ~/bin/ralph ./cmd/ralph
```

---

## Quickstart — the canonical use case

You have an existing GitHub repo. You want to throw issues at ralph and walk away.

```bash
cd ~/myproject              # clean working tree, on main
ralph doctor                # verify claude, git, host token, repo access
ralph init                  # optional: drop a .go-ralph-go starter + add .ralph/ to .gitignore
ralph auto                  # start polling
```

In another window, find an issue on GitHub and add the `ready` label. Within `poll_interval` seconds (default 60s), ralph picks it up, runs the refine loop, opens a PR, waits for CI, squash-merges, and goes back to polling.

**Use Ctrl+C** to stop. Ralph sends Claude a polite `SIGINT` with a 10s grace period and clears any `ralph-working` label before exiting so issues aren't left in a stuck state.

---

## Configuration

Ralph looks for `.go-ralph-go` (TOML) starting at your current directory and walking up. All fields are optional — defaults match the original `ralph.sh` behaviour.

Generate a starter:

```bash
ralph init
```

Key knobs:

```toml
iterations       = 5            # refine passes before cleanup (max 50)
instructions_doc = "AGENTS.md"  # doc Claude is told to follow during cleanup
output_dir       = ".ralph"     # run logs go here (gitignored)
claude_bin       = "claude"     # override if your binary is elsewhere
poll_interval    = 60           # auto-mode poll seconds (min 30)
default_branch   = ""           # override auto-detected default branch

# Prompt templates — all support placeholders shown.

refine_prompt = """
double check that your work is elegant and complete, ensure all edge cases
are covered, security issues addressed, and test coverage is complete.
"""

issue_prompt = """
Work on {{provider}} issue #{{number}}. Read the title and body carefully;
if your host CLI is available (gh or glab), also read the issue comments.

Title: {{title}}

Body:
{{body}}
"""

cleanup_prompt = """
The Ralph loop just exited{{issue_clause}}. Perform post-loop cleanup per
{{instructions_doc}}: push the branch and open a PR.{{closes_clause}}
"""

[github]
owner    = ""                   # auto-detected from origin remote
repo     = ""                   # auto-detected
base_url = ""                   # GitHub Enterprise: "https://ghe.example.com/api/v3/"
check_interval_seconds = 30

[github.labels]
ready   = "ready"
working = "ralph-working"
failed  = "ralph-failed"
```

### Project-local `.claude` config (opt-in)

By default ralph uses your **system-wide claude** install — your `claude` login carries through, no extra config needed. This is what you want 99% of the time.

To opt in to a project-local `CLAUDE_CONFIG_DIR` (project-scoped agents, MCP servers, credentials), set in `.go-ralph-go`:

```toml
claude_config_dir = ".claude"   # absolute or relative to project root
```

Only do this if the directory is **fully provisioned** (contains a `.credentials.json` from a real `claude /login`). A directory that exists only to hold ralph's memory files (`.claude/projects/`) is _not_ a valid `CLAUDE_CONFIG_DIR` — pointing claude at it fails immediately with `Not logged in · Please run /login`. `ralph doctor` will flag this with a `[WARN]` if it spots a configured dir without credentials.

### CLI overrides

Every relevant config knob has a per-invocation flag:

```bash
ralph run -n 3 "small fix"                # iterations override
ralph issue 42 --instructions-doc CLAUDE.md
ralph auto --poll 30 --iterations 5       # 30s poll, 5 iterations
ralph auto --once                         # one cycle then exit
```

---

## Label state machine (auto / issue modes)

| Label           | Meaning                                               |
| --------------- | ----------------------------------------------------- |
| `ready`         | Queued for ralph to pick up                           |
| `ralph-working` | Currently in progress (set on claim, removed on exit) |
| `ralph-failed`  | Exited without successful merge (needs human triage)  |
| _(closed)_      | PR merged; issue auto-closed via "Closes #N"          |

Ralph creates these labels on first run (`EnsureLabels`). Rename them in the `[github.labels]` (or `[gitlab.labels]`) section if your project uses different conventions — colors and descriptions are preserved across renames.

---

## Commands

| Command                     | What it does                                                          |
| --------------------------- | --------------------------------------------------------------------- |
| `ralph run "<prompt>"`      | Refine loop on an ad-hoc prompt; no host required, no PR opened       |
| `ralph run --pr "<prompt>"` | Same plus open a PR (no auto-merge)                                   |
| `ralph issue <N>`           | Single issue end-to-end: refine → PR → wait for checks → squash-merge |
| `ralph auto`                | Poll for ready issues; work them in oldest-first order, forever       |
| `ralph auto --once`         | Same; exit after one issue (or immediately if nothing ready)          |
| `ralph init [--force]`      | Drop a starter `.go-ralph-go` and add `.ralph/` to `.gitignore`       |
| `ralph doctor`              | Probe environment: claude binary, git, remote, token, working tree    |
| `ralph version`             | Print version, Go version, and platform                               |

Every command supports `--help`.

---

## Output

Each run writes three files in the configured `output_dir` (default `.ralph/`):

| File            | Content                                                                                            |
| --------------- | -------------------------------------------------------------------------------------------------- |
| `output.jsonl`  | Raw stream-json from `claude -p` — full event log, replayable                                      |
| `output.txt`    | Pretty-rendered transcript (text deltas, `[tool: Read]`, `[tool_result]`, `[result: $X over Yms]`) |
| `output.stderr` | Claude stderr                                                                                      |

Logs are truncated at the start of each cycle (matching `ralph.sh`). For auto mode, only the most recent issue's transcript is preserved — pull historical data from the host's PR/issue history.

---

## Authentication

Tokens are discovered in this order:

| Provider | First           | Fallback                        |
| -------- | --------------- | ------------------------------- |
| GitHub   | `$GITHUB_TOKEN` | `gh auth token`                 |
| GitLab   | `$GITLAB_TOKEN` | `glab auth status --show-token` |

The `gh`/`glab` fallback means **if you're already logged in via the host CLI, ralph just works** — no extra config.

`ralph doctor` validates the token by hitting the host's `/user` endpoint, so a stale token surfaces here instead of failing inside a run.

---

## The refine prompt — why it looks the way it does

The default refine prompt blends two community-validated patterns:

1. **Self-Refine** ([Madaan et al., NeurIPS 2023](https://arxiv.org/abs/2303.17651)) — separating an explicit **audit** step from the **fix** step beats single-shot "improve this" prompts by ~20% on average. Our default has an `AUDIT:` section that forces Claude to name specific issues before changing anything, and a `FIX:` section that bounds the change to a single highest-priority item.
2. **Anti-early-exit guard** — LLMs reliably bias toward declaring "looks complete" before the work actually is. We tell the model explicitly that the loop runs all N iterations regardless and that late iterations exist to catch what early ones missed. This is why ralph **does not** support a completion token / early exit — by design, every iteration runs.

Single-task-per-iteration scope (from Geoff Huntley's [original Ralph methodology](https://ghuntley.com/ralph/)) prevents the loop from drifting into refactors or new features outside the task.

Placeholders available in `refine_prompt`: `{{iter}}`, `{{total}}`. Override the whole template in `.go-ralph-go` if your project needs different audit dimensions or scope rules.

## How the refine loop works (Claude internals)

The orchestration shells out to:

```
claude -p --verbose --output-format stream-json --include-partial-messages \
       --session-id <uuid>        # first iteration
       --resume <uuid>            # subsequent iterations
       "<work_prompt>\n<refine_prompt>"
```

All iterations share the same session id, so Claude's context compounds across passes — each iteration sees the prior work. The cleanup pass uses `--resume` on the same session.

Stream-json events are decoded natively in Go (no `jq` dependency); the renderer mirrors the original Bash filter:

- `stream_event` → text deltas streamed to terminal
- `assistant` with `tool_use` → `[tool: Name] {input...}` (truncated at 400 chars)
- `user` with `tool_result` → `[tool_result]`
- `system` `init` → `[session init: <id>]`
- `result` → `[result: $cost over Nms]`

On Ctrl+C, ralph sends Claude a polite `SIGINT` with a 10s grace period, then escalates to `SIGKILL`. Any in-flight issue gets its `ralph-working` label cleared by a deferred cleanup that runs even on context cancellation.

---

## Safety guarantees

1. **No work on a dirty tree.** `ralph auto` / `ralph issue` bail at startup if your working tree has uncommitted changes; the auto loop also re-checks between issues to catch mid-run drift.
2. **No silent runaway.** Iteration count is capped (`MaxIterations = 50`) and `poll_interval` has a 30s floor — guards against typos like `--iterations 5000`.
3. **No premature merge.** `WaitForChecks` requires two consecutive zero-check polls before concluding "no CI configured" — gives GitHub Actions / GitLab CI time to register checks on a fresh PR.
4. **No stuck `ralph-working` labels.** Any non-nil exit (including Ctrl+C) clears the working label via a deferred cleanup using a fresh 30s background context. After successful merge, an explicit `MarkResolved` call closes the issue + removes the working label even if Claude's PR body forgot to include `Closes #N`.
5. **No surprise pushes in local mode.** `ralph run` without `--pr` tells Claude explicitly _not_ to push or open a PR.

---

## Extending to new hosts

The `vcs.Provider` interface in `internal/vcs/vcs.go` is the integration seam. To add a new host:

1. Implement `vcs.Provider` for it (see `internal/vcs/github/` and `internal/vcs/gitlab/` for references).
2. Add a case to `GuessProvider` in `internal/vcs/remote.go`.
3. Add a token discovery branch to `DiscoverToken` in `internal/vcs/auth.go`.
4. Register the constructor in the runner's `ensureProvider`.

The provider surface is intentionally small (10 methods): labels, issue queries, PR lookup, check waiting, merge, plus `Whoami` for token validation. A new provider is ~200 lines of Go.

---

## Development

```bash
git clone https://github.com/chuntley/go-ralph-go.git
cd go-ralph-go
go build ./cmd/ralph
go test ./...
go vet ./...
```

Layout:

```
cmd/ralph/                  entrypoint (signal-aware context, ldflags-overridable version)
internal/
  cli/                      cobra commands (run / issue / auto / init / doctor / version)
  config/                   .go-ralph-go discovery, defaults, validation, starter writer
  claude/                   stream-json types, native renderer, session runner
  git/                      git CLI helpers (branch, clean check, default branch, origin URL)
  vcs/                      provider interface, remote URL parser, token discovery, factory
    github/                 GitHub impl via go-github/v66
    gitlab/                 GitLab impl via gitlab.com/gitlab-org/api/client-go
  runner/                   orchestration (RunPrompt / RunIssue / RunAuto)
```

Run tests for a single package:

```bash
go test ./internal/claude/...
```

Smoke-test the CLI in a scratch repo:

```bash
go build -o /tmp/ralph ./cmd/ralph
mkdir /tmp/scratch && cd /tmp/scratch
git init && git remote add origin git@github.com:you/test-repo.git
/tmp/ralph doctor
```

### Setting a version at build time

```bash
go build -ldflags='-X github.com/chuntley/go-ralph-go/internal/cli.version=v1.2.3' ./cmd/ralph
```

Without an ldflags override, `ralph version` falls back to `runtime/debug.ReadBuildInfo` — pseudo-version when installed via `go install ...@latest`, short commit SHA (with `+dirty` suffix if applicable) when built from a local checkout.

---

## Honest limitations

- **One ralph per repo at a time.** No lock file; concurrent ralphs racing on the same repo will collide.
- **No retry on transient host errors.** A 502 from GitHub fails the run; intentional, so real issues aren't masked.
- **Logs are per-cycle, not per-issue.** Auto mode overwrites `.ralph/output.*` between issues. Historical context lives in the host's PR/issue history.
- **`--no-merge` not yet a flag on `issue` mode.** If you want a PR without auto-merge, use `ralph run --pr "Work on issue #N"`.
- **Self-hosted GitLab base URL** is inferred from the remote host; for GHE you must set `[github] base_url` explicitly in `.go-ralph-go`.
