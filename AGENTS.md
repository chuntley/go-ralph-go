# AGENTS.md — contributor and ralph instructions

This file is read by ralph's cleanup pass (see `cleanup_prompt` default) and
applies equally to human contributors. The release pipeline relies on the
commit convention below — please follow it.

## What this project is

`ralph` is a Go CLI that drives Claude Code through a multi-iteration refine
loop, with optional GitHub/GitLab issue → PR → merge automation. Single
binary, installable via `go install`. Architecture overview is in `README.md`.

## Commit message convention — Conventional Commits

Every commit on `main` must follow [Conventional Commits](https://www.conventionalcommits.org/).
`release-please` parses these to compute the next semver version automatically;
non-conforming commits are ignored by version bumping and produce no entry in
the changelog.

Format:

```
<type>(optional scope): <short summary>

<optional body>

<optional footers>
```

Types and their semver effect:

| Type | Bump | Use for |
|---|---|---|
| `feat:` | **minor** | New user-facing capability (`feat: add ralph plan command`) |
| `fix:` | **patch** | Bug fix (`fix: requeue interrupted issues on Ctrl+C`) |
| `refactor:` | patch | Code shape change with no behaviour change |
| `perf:` | patch | Performance improvement |
| `docs:` | patch | Docs only |
| `test:` | patch | Tests only |
| `build:` | patch | Build system / dependencies |
| `ci:` | patch | CI config |
| `chore:` | patch | Anything else (vendoring, formatting) |

**Major bumps** use either of:

```
feat!: rename Provider methods
```

or a `BREAKING CHANGE:` footer:

```
feat: replace MarkFailed signature

BREAKING CHANGE: MarkFailed now takes a Reason struct instead of a string.
```

Pre-1.0 (we currently are): release-please bumps **minor** for any `feat`
and **patch** for anything else, regardless of `!`. After 1.0 the major-bump
rules above kick in normally.

Examples in this repo's style:

```
feat(runner): surface PR URL after successful merge
fix(github): treat 404 on label remove as success
refactor(claude): extract dispatchCleanup for testability
docs: explain CLAUDE_CONFIG_DIR opt-in semantics
ci: add release-please + goreleaser pipeline
```

## Branch and PR workflow

- Never commit directly to `main`. Work on a feature branch.
- Open a PR against `main`. CI runs `go build`, `go test`, and `go vet`.
- Squash-merge — keeps `main` linear and makes the commit subject (which
  must be a Conventional Commit) the unit `release-please` parses. If
  squash-merging, edit the merge commit subject to a valid type if the
  branch commits weren't conventional.
- When `release-please` opens its "Release vX.Y.Z" PR, review the generated
  CHANGELOG and merge it to cut the release.

## Build / test / lint

```bash
go build ./...
go test ./...
go vet ./...
```

These are the same commands CI runs. Pre-push checklist: all three clean.

## Running ralph on this repo

This repo dogfoods itself. To run ralph against itself:

```bash
go build -o ./ralph ./cmd/ralph
./ralph doctor                          # verify env
./ralph run -n 1 "<small task>"         # 1-iteration dry run
./ralph run --pr "<task>"               # full loop + PR, no auto-merge
./ralph auto --once                     # full loop + auto-merge for ONE
                                        # ready-labelled issue
```

The cleanup pass will stage and commit outstanding changes following the
Conventional Commit convention above — that's why this file matters for
ralph's autonomous runs, not just human PRs.

## Releases

Driven entirely by commits to `main`:

1. PR merged with conventional commit subject.
2. `release-please` workflow opens or updates a "Release vX.Y.Z" PR that
   bumps `.release-please-manifest.json`, updates `CHANGELOG.md`, and waits
   for human approval.
3. Merging the release PR creates a `vX.Y.Z` tag.
4. The tag triggers `goreleaser` to build cross-platform binaries
   (darwin/linux × amd64/arm64) and publish them as a GitHub Release.

Version baking: `goreleaser` injects the tag into
`github.com/chuntley/go-ralph-go/internal/cli.version` via `ldflags`, so
`ralph version` on a released binary reports the real semver. Source builds
(`go install ...@latest` or local `go build`) fall back to
`runtime/debug.ReadBuildInfo` (pseudo-version or `+dirty` SHA).

## Code conventions

- Idiomatic Go; pass `go vet` with no warnings.
- New packages live under `internal/`. Public API stays minimal.
- Test files (`*_test.go`) co-located with the code they cover. Use
  `t.TempDir()` for fixtures (see `internal/git/git_test.go`).
- Errors: wrap with `fmt.Errorf("context: %w", err)`. Use `errors.Is` for
  sentinel checks (see `vcs.ErrNoReadyIssue`).
- Comments: only where the *why* isn't obvious (matches `CLAUDE.md`-style
  guidance). Don't narrate the *what*.
- Provider interface (`internal/vcs/vcs.go`) is the integration seam for
  new hosts. Keep it small; add methods only when the runner actually
  needs them.
