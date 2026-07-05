# sieve

A zero-infra, provider-agnostic PR reviewer. Single Go binary, no server, no
containers — runs as a CLI or (later) a GitHub Action with your own API keys.

**Stage 1 status:** read-only dry run. sieve fetches a PR, parses its diff
into an exact line-anchor model, applies noise filters, and dumps the
resulting `ReviewContext` as JSON. No LLM calls, no writes to GitHub.

## Install

```sh
go install github.com/gnanam1990/sieve/cmd/sieve@latest
```

Or from a checkout:

```sh
make build   # produces ./sieve
```

Requires Go 1.23+. No CGo.

## Quickstart

```sh
export GITHUB_TOKEN=ghp_...   # or pass --token
sieve review --repo cli/cli --pr 13791 --dry-run
```

- **stdout** — deterministic, byte-stable JSON `ReviewContext` (pipe it to `jq`)
- **stderr** — human summary: PR header, per-file table, stats footer

```
cli/cli#13791 "chore(deps): bump github.com/klauspost/compress ..." by dependabot[bot]
base b300f2ec7ec9 -> head 62f01e7aac99

FILE    STATUS    +  -  REVIEW
go.mod  modified  1  1  keep
go.sum  modified  2  2  skip: default exclude: **/go.sum

2 files total, 1 to review, 1 skipped, +3 -3 lines
```

Flags: `--json-only` suppresses the stderr summary (CI use); `--debug` raises
log verbosity.

Exit codes: `0` success · `2` partial (diff or file listing truncated) ·
`1` error.

### GitHub Actions mode

When `--repo`/`--pr` are absent, sieve falls back to `GITHUB_REPOSITORY` and
the `pull_request.number` in the event payload at `GITHUB_EVENT_PATH`, so a
bare `sieve review --dry-run` works inside a `pull_request` workflow.

## Configuration

Optional `.sieve.yml` at the repo root. Every key is optional; unknown keys
are a **hard error** (typo protection).

```yaml
paths:
  exclude:            # globs (doublestar ** semantics), matched against the
    - "docs/**"       # new path (old path for deletes)
    - "**/*.gen.go"

review:
  max_comments: 10    # reserved for stage 3; range 1-50
  min_confidence: 0.7 # reserved for stage 3; range 0.0-1.0

provider:
  model: ""           # reserved for stage 2
```

Precedence: built-in defaults → `.sieve.yml` → environment → flags.

Environment overrides: `SIEVE_MAX_COMMENTS`, `SIEVE_MIN_CONFIDENCE`,
`SIEVE_MODEL`, `SIEVE_EXCLUDE` (comma-separated globs, appended to the file's
list). Token: `--token` flag or `GITHUB_TOKEN`.

## Default excludes

Applied before configured globs; binary files are always skipped. Every skip
is reported with its reason.

| Category | Patterns |
|---|---|
| Vendored / build output | `vendor/`, `node_modules/`, `dist/`, `build/`, `.next/`, `target/` (any depth) |
| Minified / derived | `*.min.js`, `*.min.css`, `*.map` |
| Generated code | `*.pb.go`, `*_generated.go`, `*.gen.go` |
| Lockfiles | `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`, `bun.lockb`, `go.sum`, `Cargo.lock`, `poetry.lock`, `Pipfile.lock`, `composer.lock`, `Gemfile.lock` |
| Snapshots | `*.snap` |
| Images / fonts / archives | `png jpg jpeg gif webp ico woff woff2 ttf eot zip gz tar` |

`go.mod` is deliberately **not** excluded — dependency changes are
reviewable; `go.sum` is noise.

## Limits

- Diff fetch is capped at **5 MB**; larger diffs are truncated at the last
  complete file boundary and flagged `Truncated` (exit code 2).
- GitHub caps the PR file listing at **3000 files**; hitting it also flags
  `Truncated`.

## Development

```sh
make test    # go test ./... -race -shuffle=on
make lint    # golangci-lint
make cover   # enforces internal/diff >= 90%, overall >= 80%
make golden  # regenerate diff-parser golden files (UPDATE_GOLDEN=1)
```

Dependency policy: `yaml.v3`, `doublestar`, `go-cmp` (tests only) — nothing
else. GitHub REST is spoken via stdlib `net/http`.
