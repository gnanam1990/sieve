# sieve

A zero-infra, provider-agnostic PR reviewer. Single Go binary, no server, no
containers — runs as a CLI or (later) a GitHub Action with your own API keys.

**Stage 3 status:** review **and post**. sieve fetches a PR, parses its diff
into an exact line-anchor model, applies noise filters, sends batched prompts
to the LLM provider of your choice, anchor-validates every finding against the
real diff, routes survivors through a two-tier noise gate, and — only when you
pass `--post` — writes them to the PR as one editable walkthrough comment plus
a batched inline review. Re-runs edit in place and never duplicate comments.
**Writes require the `--post` flag; no config key can enable them.**

## Install

```sh
go install github.com/gnanam1990/sieve/cmd/sieve@latest
```

Or from a checkout: `make build`. Requires Go 1.23+. No CGo.

## Quickstart

```sh
export GITHUB_TOKEN=ghp_...        # or --token
export ANTHROPIC_API_KEY=sk-ant-...
cat > .sieve.yml <<'EOF'
provider:
  type: anthropic
  model: claude-sonnet-5
EOF
sieve review --repo owner/name --pr 123
```

- **stdout** — deterministic JSON `ReviewContext` including `Findings` and the
  `Gate` block (tier per finding, drop/demote counters, fingerprints)
- **stderr** — human summary: file table, findings table, token usage footer

`--dry-run` skips the LLM pass entirely (stage-1 behavior: context dump only).
`--json-only` suppresses the stderr summary (combinable with `--post`). Exit
codes: `0` ok · `2` partial (truncated context, a failed batch, or a failed
inline post) · `1` error.

Draft PRs are skipped (exit 0, empty findings) unless `review.review_drafts: true`
— this holds **even with `--post`**: a draft is never written to.

## Posting to the PR (`--post`)

```sh
sieve review --repo owner/name --pr 123 --post
```

runs the full review, then routes surviving findings through the noise gate and
writes them to the PR:

- **One walkthrough comment** — created on the first run, **edited in place** on
  every later run (located by a hidden marker). It carries a stats line, a table
  of the top-tier findings, collapsible **Notes / Resolved / Skipped** sections,
  and a hidden metadata block that makes cross-run dedupe stateless.
- **Inline review comments** for the top tier only, submitted as **one review**
  (a single notification), with committable ` ```suggestion ` blocks where a
  finding carries a safe one.
- **Cross-run dedupe** — re-running never duplicates an inline comment. Findings
  that disappear are listed under **Resolved since last review**.

### The `--post` safety model

- **Posting is enabled *only* by the `--post` flag.** There is no config key for
  it — a committed `.sieve.yml` can never turn writes on. A `post:` key anywhere
  in config is a hard error.
- **Every GitHub write goes through one package** (`internal/post`); a test
  greps the tree to prove no other package touches a mutating endpoint.
- `--post` and `--dry-run` are mutually exclusive.
- The GitHub client resolves its token through a `TokenSource` (today a static
  flag/env token; a GitHub App installation token later) — environment access
  stays confined to the command layer.

### The noise gate

Findings are routed by confidence and severity:

```yaml
review:
  min_confidence: 0.6         # drop floor: below this a finding is discarded (counted)
  inline_min_confidence: 0.8  # inline tier requires confidence >= this
  inline_min_severity: major  # inline tier requires severity >= this (major | critical)
  max_inline_comments: 10     # hard cap on inline comments per run (1-30)
```

The pipeline, in order: **within-run dedupe** (overlapping same-category
findings collapse to the highest-confidence one) → **floor** (drop below
`min_confidence`) → **tier routing** (inline needs *both* gates; everything else
surviving the floor becomes a walkthrough note) → **cap** (inline beyond
`max_inline_comments` is *demoted to notes, never dropped*) → **cross-run
dedupe** (findings already posted last run are not re-posted inline).
`inline_min_confidence` must be `>= min_confidence`. Without `--post`, the gate
still runs and its decisions appear in the JSON `Gate` block — only the *writing*
is gated.

### Walkthrough anatomy

```
<!-- sieve:walkthrough -->                          ← locator marker (find + edit in place)
<!-- sieve:meta v1 <base64 json> -->                ← hidden state: head SHA + fingerprints
## sieve review
**2 findings** · 3 notes · 1 resolved · 6 files reviewed, 2 skipped   ← stats

| Severity | Finding | Where |                       ← top-tier (inline) findings only
|---|---|---|
| 🔴 critical | SQL built by string concatenation | `internal/db/query.go:88` |
| 🟠 major | Unchecked error from Close | `internal/gh/client.go:141` |

<details><summary>📝 Notes (3)</summary> … </details>        ← lower tier, grouped by file
<details><summary>✅ Resolved since last review (1)</summary> … </details>
<details><summary>⏭️ Skipped files (2)</summary> … </details>

<sub>model `…` · tokens in 18.2k / out 1.4k · sieve <version></sub>   ← footer
```

Severity markers: 🔴 critical · 🟠 major · 🟡 minor · ⚪ nit. The comment is
kept under GitHub's 65,536-char limit by truncating Notes, then Resolved, then
Skipped — the metadata block is never truncated.

## Provider configuration

API keys are **never** written in config. `.sieve.yml` holds the *name* of an
environment variable (`api_key_env`); sieve reads the key from there at run
time. An `api_key:` field in config is a hard error.

### Anthropic

```yaml
provider:
  type: anthropic
  model: claude-sonnet-5
  api_key_env: ANTHROPIC_API_KEY
```

### OpenAI

```yaml
provider:
  type: openai-compat
  model: gpt-5.2
  base_url: https://api.openai.com/v1
  api_key_env: OPENAI_API_KEY
```

### OpenRouter

```yaml
provider:
  type: openai-compat
  model: qwen/qwen3-coder-next
  base_url: https://openrouter.ai/api/v1
  api_key_env: OPENROUTER_API_KEY
```

### Groq

```yaml
provider:
  type: openai-compat
  model: llama-4.1-70b
  base_url: https://api.groq.com/openai/v1
  api_key_env: GROQ_API_KEY
```

### Ollama (local)

```yaml
provider:
  type: openai-compat
  model: qwen3-coder
  base_url: http://localhost:11434/v1
  api_key_env: OLLAMA_API_KEY   # Ollama ignores the value; set e.g. OLLAMA_API_KEY=ollama
```

`base_url` is required for `openai-compat` — there is no default, so requests
never go somewhere you didn't name. Knobs: `max_tokens` (256–32768, default
4096), `temperature` (0–1, default 0.1), `timeout_seconds` (10–600, default
120, per request). Retries: 429/5xx/Anthropic-overloaded are retried up to 3
attempts with backoff + jitter, honoring `Retry-After`.

## Findings schema

Each finding in `Findings[]`:

| Field | Meaning |
|---|---|
| `Path` | changed file the finding is anchored to |
| `Line` / `EndLine` | anchor line(s); `NewNum` for RIGHT, `OldNum` for LEFT; `EndLine` 0 = single line |
| `Side` | `RIGHT` (new file) or `LEFT` (old file), GitHub Reviews API vocabulary |
| `Severity` | `critical` \| `major` \| `minor` \| `nit` |
| `Confidence` | model-reported calibrated probability 0..1; the noise gate filters and tiers on it |
| `Category` | `bug` \| `security` \| `perf` \| `correctness` \| `test` \| `style` |
| `Title` | one line, ≤120 chars |
| `Body` | markdown: why + fix |
| `Suggestion` | optional replacement code; rendered as a committable ` ```suggestion ` block when safe |

**Anchor gate:** every finding is validated against the parsed diff — the
path must be a kept file and the line(s) must be commentable on the claimed
side within a single hunk. Findings that fail are dropped (never repaired)
and counted in `Stats.FindingsDropped`. This is the anti-hallucination gate:
a comment that would land on the wrong line is worse than no comment.

Output ordering is deterministic: severity, then path, then line.
`Stats` reports `FindingsTotal`, `FindingsDropped`, `BatchesFailed`,
`Requests`, `Retries`, `InputTokens`, `OutputTokens`, and `InlinePostFailed`
(inline comments that failed to post under `--post`). The `Gate` block reports
per-finding tier + fingerprint and the gate counters (`FindingsBelowFloor`,
`InlineDemotedByCap`, `DuplicatesMerged`, `RepeatedInline`, `ResolvedCount`, …).

## Review scope controls

```yaml
review:
  include_file_content: true # attach full file contents to prompts when small
  max_file_content_kb: 64    # per-file attachment cap
  concurrency: 3             # parallel provider calls (1-8)
  review_drafts: false
```

Prompts are greedy-packed into batches of ≤24k estimated input tokens
(bytes/4). A single file exceeding the whole budget is truncated hunk-by-hunk
and marked `TruncatedForReview` in the output.

## Testing with the fake provider

For offline runs and CI, `type: fake` returns a canned response instead of
calling any API — **not for real use**:

```yaml
provider:
  type: fake
  fixture: path/to/canned-response.json   # file content is returned verbatim
```

The fixture should contain the same JSON contract a real model returns:
`{"findings": [...]}`. Findings still go through the anchor gate, so a
fixture with stale line numbers gets dropped — which makes it a good test of
the gate itself.

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

- Diff fetch capped at **5 MB** (truncated at the last complete file
  boundary); GitHub caps file listings at **3000 files**. Either sets
  `Truncated` and exit code 2.
- PR description is truncated to 2 KB in prompts.

## Development

```sh
make test    # go test ./... -race -shuffle=on
make lint    # golangci-lint
make cover   # gates: diff/findings/gate >= 90%, fingerprint 100%, post/render/provider >= 85%, overall >= 85%
make golden  # regenerate parser, prompt, render, and E2E golden files
```

`scripts/sandbox_recall.sh` runs the live calibration gate: it creates a
private sandbox PR under your account with ~10 planted issues and runs
`--post` with a frontier model (needs `ANTHROPIC_API_KEY` or
`OPENROUTER_API_KEY`). See `STAGE_NOTES.md` for the recorded results.

Dependency policy: `yaml.v3`, `doublestar`, `go-cmp` (tests only) — nothing
else. GitHub REST and all LLM APIs are spoken via stdlib `net/http`. No
streaming, no SSE: blocking completion calls with per-request timeouts.
