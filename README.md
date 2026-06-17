# go-ralph-go

A single-binary CLI that drives [Claude Code](https://docs.claude.com/claude-code) through a [Ralph Wiggums-style refine loop](https://ghuntley.com/ralph/) on your project — locally, against a single GitHub/GitLab issue, or as a polling worker that picks up issues labelled `ready` and ships them as merged PRs.

---

### Install

One-liner for macOS and Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/chuntley/go-ralph-go/main/install.sh | sh
```

Installs to `~/.local/bin/ralph`. Override with `RALPH_INSTALL_DIR=/usr/local/bin` or pin a version with `RALPH_VERSION=v0.1.1`.

**Manual download:** grab a tarball from the [Releases page](https://github.com/chuntley/go-ralph-go/releases), extract `ralph`, and put it on your `$PATH`:

```bash
tar -xzf ralph_*_darwin_arm64.tar.gz
mv ralph ~/bin/ralph
```

Or run the build directly from the repo:

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

## What it does

For each task ralph receives, it:

1. **Branches** — syncs the default branch, then creates and checks out a dedicated working branch (`ralph/issue-<N>-<slug>`, configurable via `branch_prefix`) *before* any work. This is a guardrail, not a suggestion: because the loop starts on a feature branch, every commit Claude makes — and any push — lands there, never on the protected default branch, regardless of what your instructions doc says.
2. **Refines toward the goal** — treats the work prompt as a *goal* and loops Claude over it, keeping durable state in a plan file on disk. It always runs at least `min_iterations` passes (default 5) — early "done" signals are ignored so the work gets real re-audits — then ends as soon as Claude signals the goal is complete (confirmed by your `verify_command` when set), up to a max-passes cap (`iterations`, default 10). Claude is never told either bound.
3. **Cleans up** — runs a final pass telling Claude to push the branch and open a PR (matching your project's `AGENTS.md` / `CLAUDE.md` style guide). This pass is a single non-interactive turn, so the prompt tells Claude to finish synchronously and not background work or defer the push. If it commits work but still doesn't open the PR, **ralph pushes the branch and opens the PR itself** — so a run never fails just because the agent ran out of turn.
4. **Validates** — waits for CI checks via the host API.
5. **Merges** — squash-merges, deletes the source branch, and closes the issue.
6. **Resets** — checks out the default branch and fast-forwards, leaving your repo clean for the next loop.

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
min_iterations   = 5            # MIN refine passes; early "done" is ignored
                                # until the loop has run at least this many
iterations       = 10           # MAX refine passes (safety cap, max 50); once
                                # the min is met, ends early when goal verifies
instructions_doc = "AGENTS.md"  # doc Claude is told to follow during cleanup
output_dir       = ".ralph"     # run logs + plan.md go here (gitignored)
claude_bin       = "claude"     # override if your binary is elsewhere
poll_interval    = 60           # auto-mode poll seconds (min 30)
default_branch   = ""           # override auto-detected default branch

completion_sentinel = "RALPH_GOAL_COMPLETE"  # line Claude emits when goal is done

# The harness-owned completion gate. STRONGLY recommended: when set, ralph runs
# this itself after Claude signals completion and only stops the loop if it
# exits 0 — the real test suite, not Claude's self-report, decides "done".
verify_command = "go test ./... && go build ./... && golangci-lint run"

# Prompt templates — all support placeholders shown.
# refine_prompt placeholders: {{plan_file}}, {{sentinel}}, {{verify}}

# Written as a single-shot task — no mention of "passes"/loops, since ralph runs
# it with a fresh context each time and revealing the loop invites pacing.
refine_prompt = """
You are implementing the goal above to a correct, complete, verified state.
Work from what you can see now: the code, git log, and the plan at {{plan_file}}.

ORIENT: read {{plan_file}} (create it if missing) and recent git log.
AUDIT:  review the code as a skeptical reviewer who didn't write it; assume
        mistakes exist; scrutinize the TESTS themselves (re-derive expected
        values from the goal; a green suite isn't proof). Record issues in
        the plan's FINDINGS.
WORK:   fully drive the goal to done now — implement everything missing/wrong
        and fix all FINDINGS; don't leave straightforward work unfinished.
VERIFY: {{verify}} Show real output; never edit/skip tests to pass.
COMMIT: commit, and refresh {{plan_file}} (acceptance status + findings).
        NEVER record the goal as "complete"/"done" in the plan.
COMPLETION: passing VERIFY is necessary but NOT sufficient. Review the whole
        diff as if you didn't write it; output {{sentinel}} ONLY if the tests
        truly assert the requirement, no rework is warranted, and VERIFY passes.
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

Only do this if the directory has a **real login of its own**. Claude stores credentials _per config dir_ — on Linux as `$CLAUDE_CONFIG_DIR/.credentials.json`, on macOS as a Keychain entry keyed to the dir's path — so a directory that merely holds settings or ralph's memory files (`.claude/projects/`) but was never logged in is _not_ usable: claude fails immediately with `Not logged in · Please run /login`. The `.claude.json` account metadata (email/plan) can be present without any usable login, so don't trust it as proof.

To provision the login, run a real `/login` **with that config dir active**:

```sh
CLAUDE_CONFIG_DIR=/abs/path/to/project/.claude claude auth login
```

ralph guards this for you. `ralph doctor` reports the true login state via `claude auth status` (a `[WARN]` if the dir isn't logged in, or if the status can't be verified). And every run preflights auth before touching the issue tracker: if the configured dir has no usable login, ralph stops up front with the exact `claude auth login` command instead of burning into a cryptic pass-1 failure. If a login lapses mid-run, the active issue is requeued (not marked failed) and the worker halts with the same fix.

### CLI overrides

Every relevant config knob has a per-invocation flag:

```bash
ralph run -n 3 "small fix"                # cap at 3 passes (min clamps to 3)
ralph run --min-iterations 8 -n 15 "..."  # at least 8 passes, at most 15
ralph issue 42 --instructions-doc CLAUDE.md
ralph auto --poll 30 --iterations 5       # 30s poll, cap at 5 passes
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

**`ralph auto` halts when an issue is marked `ralph-failed`.** Rather than quietly moving on to the next issue, the worker exits non-zero with a reason naming the failed issue, so a broken run surfaces to a human for triage instead of compounding. (A `Ctrl+C` / timeout is different: that *requeues* the in-flight issue as `ready` and exits cleanly — it is not a failure.)

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

## The refine loop — why it works the way it does

ralph runs a **goal-driven loop**, not a fixed number of "improve this" passes. The design follows the convergent advice of Geoff Huntley's [Ralph methodology](https://ghuntley.com/ralph/) and Anthropic's guidance on [long-running agents](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents) and [context engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents). Four principles:

1. **The work prompt is a goal, and the loop runs until it's met — within a min/max band.** Each pass drives toward the goal. The loop always runs at least `min_iterations` passes (default 5): a completion signal before the floor is **ignored** and the loop keeps refining, so the work gets a guaranteed baseline of critical re-audits instead of stopping the instant Claude first thinks it's done. Once the floor is met it ends as soon as completion is signalled, up to the `iterations` cap (default 10). Per the agent-loop consensus, criteria-driven termination beats a fixed count — and the minimum floor counters the well-documented bias toward premature "looks done" verdicts.

2. **Each pass runs with a fresh context; durable state lives in a plan file on disk.** Every refine pass is a brand-new Claude session (`--session-id`, never `--resume`) — it cannot see the earlier passes. State carries over only via `.ralph/plan.md` (goal, verify command, acceptance criteria + evidence, findings) and git. This is the canonical Ralph + Anthropic pattern (*"start with a fresh context window… a fresh context improves code review since Claude won't be biased toward code it just wrote"*), and it's load-bearing here: a fresh context is what stops Claude from **pacing itself** ("I'll finish the rest next time") and from **rubber-stamping** code it remembers writing. The plan lives under the gitignored output dir, so it never pollutes your commits, and it never records an overall "complete"/"done" status — completion is the harness's call.

3. **Claude is never told it's being looped at all.** Not just the count — the loop prompt is written as a single-shot task with **no mention of passes, loops, or iteration**, and the pass counter is kept out of both the prompt and the on-disk transcript. Any signal that it will be re-invoked invites pacing and premature wrap-up (Anthropic notes models "naturally try to wrap up work" as they sense a limit), so the design hides the loop entirely; the fresh context per pass enforces it.

4. **Completion is verified by the harness, not trusted from the model.** A self-emitted "done" token is the weakest possible signal: agents declare completion prematurely, and prompt rules like "don't edit tests" barely dent reward-hacking ([METR](https://metr.org/blog/2025-06-05-recent-reward-hacking/) measured 70–95% persistence). So when Claude emits the completion sentinel, ralph runs your `verify_command` *itself* and only stops the loop if it exits 0; otherwise the failure is fed back and another pass runs. There's also a **minimum floor** (`min_iterations`, default 5): a completion signal before the floor is ignored so the work gets a guaranteed baseline of fresh-eyes re-audits. With no `verify_command` set, the token is trusted with a logged caveat — **setting one is strongly recommended.**

5. **Green tests aren't proof.** `AUDIT` reviews the code as a skeptical reviewer who didn't write it — including the **tests themselves** (re-deriving each expected value from the goal, since a test can assert the wrong thing or be incomplete). `COMPLETION` treats a passing `verify_command` as **necessary but not sufficient** and requires a skeptical review of the whole diff confirming the tests truly assert the requirement and no rework is warranted before the sentinel may be emitted. The fresh context per pass (principle 2) is what makes this review genuinely independent rather than a self-rubber-stamp.

Self-Refine-style `AUDIT → WORK → VERIFY` structure ([Madaan et al., NeurIPS 2023](https://arxiv.org/abs/2303.17651)) plus scope guards (no features/refactors/defensive code beyond the goal) round it out. Override the whole template in `.go-ralph-go`; placeholders are `{{plan_file}}`, `{{sentinel}}`, and `{{verify}}`.

## How the refine loop works (Claude internals)

The orchestration shells out to:

```
claude -p --verbose --output-format stream-json --include-partial-messages \
       --session-id <new-uuid>     # a FRESH session every refine pass
       "<work_prompt>\n<refine_prompt>"
```

Every refine pass gets a **new** session id (`--session-id`, never `--resume`), so Claude starts each pass with a clean context and discovers the current state from `.ralph/plan.md` + git — it has no memory of having been run before. The on-disk transcript (`.ralph/output.*`) keeps accumulating across passes for the operator, but Claude is told not to read it. ralph scans each pass's streamed assistant text for the completion sentinel (on its own line); when seen (and at/above the min floor), it runs the `verify_command` gate before ending the loop. Only the final **cleanup pass** uses `--resume` — it continues the last refine pass so it knows the branch and commits it just produced.

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
2. **No silent runaway.** The goal-driven loop is bounded by a max-passes cap (`MaxIterations = 50`) and `poll_interval` has a 30s floor — guards against a goal that never converges or a typo like `--iterations 5000`.
3. **No premature "done".** A self-reported completion signal doesn't end the loop on its own: when `verify_command` is set, ralph re-runs it itself and only stops if it exits 0, so the real test suite — not the model — has the final say.
4. **No premature merge.** `WaitForChecks` requires two consecutive zero-check polls before concluding "no CI configured" — gives GitHub Actions / GitLab CI time to register checks on a fresh PR.
5. **No stuck `ralph-working` labels.** Any non-nil exit (including Ctrl+C) clears the working label via a deferred cleanup using a fresh 30s background context. After successful merge, an explicit `MarkResolved` call closes the issue + removes the working label even if Claude's PR body forgot to include `Closes #N`.
6. **No surprise pushes in local mode.** `ralph run` without `--pr` tells Claude explicitly _not_ to push or open a PR.
7. **No commits to the protected default branch.** In PR-opening modes (issue / auto / `run --pr`) ralph creates and checks out a dedicated working branch *before* the loop, so a project whose instructions say "push each session" can't publish straight to `main`/`master`. The work prompt also tells Claude it's on that branch and must not commit to the default branch, and the cleanup step still backstops by refusing to proceed if the run somehow ended on the default branch.
8. **No lost work when the agent doesn't open the PR.** ralph owns the PR mechanics: if the cleanup pass committed work but didn't open a PR (e.g. it backgrounded a long test gate and ran out of turn), ralph pushes the branch and opens the PR itself. It only errors when there's genuinely nothing to ship (no commits over the base).
9. **No silent failure in `auto`.** The moment an issue is marked `ralph-failed`, `ralph auto` exits non-zero with a reason instead of moving on — a broken run gets human triage rather than compounding across the queue. (Interrupts requeue the issue and exit cleanly; they are not failures.)
10. **No usable-login surprises.** Every run preflights `claude auth status` for the active config dir and stops up front with the exact `claude auth login` command if it isn't logged in; an auth failure mid-run requeues the issue (never marks it failed) and halts with the fix.

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

---

## License

Licensed under the [MIT License](LICENSE).

Copyright © 2026 Chad Huntley
