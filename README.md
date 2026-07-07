# sieve

A zero-infra, provider-agnostic PR reviewer. One static Go binary — no server,
no container, no `docker pull`. Runs as a **GitHub Action** or a CLI, with your
own model API key.

[![sieve PR review](https://img.shields.io/badge/Action-sieve%20PR%20review-blue?logo=github)](https://github.com/marketplace/actions/sieve-pr-review)
[![release](https://img.shields.io/github/v/release/gnanam1990/sieve)](https://github.com/gnanam1990/sieve/releases/latest)

sieve fetches a PR, parses its diff into an exact line-anchor model, filters
noise, sends batched prompts to the LLM provider of your choice, anchor-validates
every finding against the real diff, routes survivors through a two-tier noise
gate, and posts them as one editable walkthrough comment plus a batched inline
review. Re-runs edit in place and never duplicate comments. **Writes require an
explicit switch (`--post`, or `post: true` in the Action); no config key can
enable them.**

## Quickstart — GitHub Action (≈ 5 minutes)

1. Add your model key as a repo secret named **`SIEVE_API_KEY`**
   (*Settings → Secrets and variables → Actions*).
2. Commit `.github/workflows/sieve.yml`:

```yaml
name: sieve
on: { pull_request: { types: [opened, synchronize, reopened, ready_for_review] } }
permissions: { pull-requests: write, contents: read }
jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: gnanam1990/sieve@v0
        with: { provider: anthropic, model: claude-sonnet-4-6 }
        env:
          SIEVE_API_KEY: ${{ secrets.SIEVE_API_KEY }}
          GITHUB_TOKEN: ${{ github.token }}
```

That's it — open a PR and sieve reviews it. The action downloads the pinned,
checksum-verified `sieve` binary for the runner and runs `sieve review --post`.

- **Fork PRs** are detected and skipped cleanly (secrets are unavailable to
  them) — see [docs/forks.md](docs/forks.md). Same-repo PRs are the supported
  surface.
- **Exit code 2** (a truncated diff or a failed inline post) is a warning, not a
  CI failure, unless you set `fail_on_partial: true`.
- The API key is read from the env var named by `api_key_env_name` (default
  `SIEVE_API_KEY`) — it is **never** an action input, because inputs are echoed
  to logs.

### Workflow templates

Copy a ready-made template into `.github/workflows/` from
[`.github/workflows/templates/`](.github/workflows/templates/):

- `sieve.yml` — Anthropic Claude (default).
- `sieve-openrouter.yml` — OpenRouter/Qwen (budget-friendly).

### Action inputs

| Input | Default | Notes |
|---|---|---|
| `provider` | `anthropic` | `anthropic` \| `openai-compat` \| `fake` |
| `model` | — | required (except `fake`), e.g. `claude-sonnet-4-6` |
| `base_url` | — | required iff `provider: openai-compat` |
| `api_key_env_name` | `SIEVE_API_KEY` | name of the env var holding the key (not the key) |
| `config_path` | `.sieve.yml` | your review-scope + gate config; a provider block is appended only if it has none |
| `post` | `true` | `false` = review without writing |
| `version` | *(VERSION file)* | sieve release to download; `@v0` resolves the matching binary |
| `fail_on_partial` | `false` | fail the job on exit 2 |
| `sarif_files` | — | path to write a SARIF v2.1.0 report, e.g. `sieve.sarif` |
| `verify_signatures` | `false` | require a valid cosign signature for the downloaded binary |

### Marketplace

`sieve PR review` is published on the
[GitHub Marketplace](https://github.com/marketplace/actions/sieve-pr-review).
Use `gnanam1990/sieve@v0` to stay on the latest stable major version.

## Install the CLI

```sh
# Homebrew
brew install gnanam1990/tap/sieve

# curl installer (downloads + checksum-verifies the latest release binary)
curl -fsSL https://raw.githubusercontent.com/gnanam1990/sieve/v0/install.sh | bash

# Go
go install github.com/gnanam1990/sieve/cmd/sieve@latest
```

Or from a checkout: `make build`. Requires Go 1.23+. No CGo. `sieve version`
prints the build version, commit, date, Go version, and platform.

## Quickstart — CLI

```sh
export GITHUB_TOKEN=ghp_...        # or --token
export ANTHROPIC_API_KEY=sk-ant-...
cat > .sieve.yml <<'EOF'
provider:
  type: anthropic
  model: claude-sonnet-5
EOF
sieve review --repo owner/name --pr 123          # review, findings on stdout
sieve review --repo owner/name --pr 123 --post   # …and post to the PR
```

- **stdout** — deterministic JSON `ReviewContext` including `Findings` and the
  `Gate` block (tier per finding, drop/demote counters, fingerprints)
- **stderr** — human summary: file table, findings table, token usage footer

`--dry-run` skips the LLM pass entirely (stage-1 behavior: context dump only).
`--json-only` suppresses the stderr summary (combinable with `--post`). Exit
codes: `0` ok · `2` partial (truncated context, a failed batch, or a failed
inline post) · `1` error.

### SARIF output (GitHub Security tab)

`sieve review --sarif sieve.sarif` writes findings in
[SARIF v2.1.0](https://docs.oasis-open.org/sarif/sarif/v2.1.0/sarif-v2.1.0.html)
format. Upload them with `github/codeql-action/upload-sarif`:

```sh
sieve review --repo owner/name --pr 123 --sarif sieve.sarif
```

In a workflow:

```yaml
- uses: gnanam1990/sieve@v0
  with:
    provider: anthropic
    model: claude-sonnet-5
    sarif_files: sieve.sarif
  env:
    SIEVE_API_KEY: ${{ secrets.SIEVE_API_KEY }}
- uses: github/codeql-action/upload-sarif@v3
  if: always()
  with:
    sarif_file: sieve.sarif
    category: sieve
```

Only active findings (inline + notes) are included; resolved findings are
intentionally omitted because SARIF describes current issues.

## FAQ: is there a Docker image?

No — and that's the point. sieve is a single static binary with no runtime
dependencies; the Action downloads it directly (a few MB, checksum-verified and
cosign-signed) and runs it. A container would add a `docker pull`, a registry,
and image-scanning overhead to buy nothing.

## License

[Apache-2.0](LICENSE).

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

## Incremental re-review

After the first review, a push re-reviews **only the files that changed** since
the last walkthrough (`GET compare/<last>...<head>`); every other finding is
carried forward from the metadata unchanged, keeping its inline thread. A
finding whose anchored line no longer exists in the diff is resolved at **zero
model cost**. sieve falls back to a full review on a force-push/rebase (the base
SHA is no longer an ancestor), an unchanged head SHA, a first run, or `--full` —
the reason is in `Stats.FullReviewReason`, and `Stats.TokensSaved` estimates
what the delta avoided. Toggle with `review.incremental` (default on).

## Learning from outcomes

sieve keeps a **local, append-only** outcome log per repo under
`${XDG_DATA_HOME:-~/.local/share}/sieve/<host>/<owner>/<repo>/events.jsonl`:
what it found, what got fixed, and how people reacted (👍/👎 and resolved
threads). Writes are best-effort — they never fail a review. GitHub stays the
source of truth: **`sieve sync --repo o/r --pr N`** rebuilds the log from the PR
alone (idempotent), so the store is disposable.

```sh
sieve stats --repo owner/name          # per-category addressed-rate, 👍/👎, confidence
sieve learnings --repo owner/name      # draft repo rules from repeated 👎/dismissals
```

**`sieve learnings`** clusters findings maintainers repeatedly rejected and
drafts one *suppressive* rule per cluster (one imperative sentence, generalized —
no line numbers, PR refs, or verbatim code), writing them into
`.sieve/learnings.md` under a `<!-- sieve:learnings -->` marker (your own notes
above the marker are preserved) and **printing a diff**. It **never commits** —
you review and commit the rules yourself. This is deliberate: repo rules change
what the bot flags, so a human must approve them; sieve makes no bot commits,
ever. When `.sieve/learnings.md` is present at the PR head, its rules are
injected into the review prompt (capped at 8 KB) and the walkthrough footer notes
`learnings: N rules active`.

### Confidence calibration (opt-in)

With `review.calibration: true`, sieve scales each finding's confidence by its
category's *addressed-rate* from the local store — `clamp(addressed_rate/0.5,
0.5, 1.0)`, a no-op below a 10-finding sample — **before** the gate. It's
transparent: the JSON records raw vs calibrated per finding and the footer notes
`calibration: on`. Default off; never silently on.

### GitHub Action note

The store lives on the runner's ephemeral filesystem. To let learnings and stats
accumulate across Action runs, cache it with `actions/cache` keyed on the repo:

```yaml
- uses: actions/cache@v4
  with:
    path: ~/.local/share/sieve
    key: sieve-store-${{ github.repository }}
```

Losing the cache is harmless — `sieve sync` rebuilds it from the PR.

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

### Kimi

```yaml
provider:
  type: openai-compat
  model: kimi-for-coding
  base_url: https://api.kimi.com/coding/v1
  api_key_env: KIMI_API_KEY
```

Kimi's API requires `temperature: 1` for `kimi-for-coding`; lower values are
rejected. The fingerprint that drives cross-run "Resolved since last review"
excludes the title, so rephrased findings with the **same anchor line** are no
longer mis-reported as resolved+new. If the model also re-anchors the finding
(changes the reported start line), the fingerprint will still change; this is
a model consistency issue, not a fingerprint issue.

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

## Multi-model pipelines

By default sieve runs **one** model as the reviewer (the `single` pipeline).
For higher precision you can name several providers and route them into a
**judge** or **ensemble** pipeline.

### The providers map

Give each model a name under `providers:`, then point `review.roles` at those
names:

```yaml
providers:
  fast:
    type: openai-compat
    model: qwen/qwen3-coder-next
    base_url: https://openrouter.ai/api/v1
    api_key_env: OPENROUTER_API_KEY
  strong:
    type: anthropic
    model: claude-sonnet-5
    api_key_env: ANTHROPIC_API_KEY

review:
  pipeline: judge
  roles:
    generator: fast    # proposes liberally
    judge: strong      # verifies against the code, drops the weak ones
```

The legacy single `provider:` block still works and is equivalent to a
`providers.default` entry with `review.roles.reviewer: default` — **no migration
needed** to keep your current config. Setting *both* `provider:` and
`providers:` is a hard error. To migrate by hand, move the `provider:` block
under `providers:` as a named entry and select it with a role.

### Pipeline selection guide

| Pipeline | What it does | Cost | Use when |
|---|---|---|---|
| `single` | one reviewer | 1× | the default; fast and cheap |
| `judge` | a liberal generator proposes, a second model verifies each finding against the code (may lower severity/confidence or drop, **never** invents a more-severe finding) | ~1.6× | you want fewer false positives without paying for a strong model on every token — **the recommended upgrade** |
| `ensemble` | 2–3 reviewers run independently; only findings ≥2 of them agree on survive | 2–3× | experimental; you want a recall/precision study, not day-to-day CI |

Measured on the seeded sandbox (`testdata/sandbox`). The first three rows used
local Ollama `kimi-k2.7-code:cloud` — the same model for every role, so the judge
could not benefit from a stronger verifier. The last row is a hosted-model
validation with Kimi `kimi-for-coding`.

| Pipeline | Recall | Precision | Input tokens | Output tokens | Wall time | Notes |
|---|---|---|---|---|---|---|
| `single` (Ollama) | 10/10 | 10/10 | 4,597 | 3,106 | ~45 s | baseline |
| `single/fast` (Ollama) | 10/10 | 10/10 | 4,597 | 3,923 | ~47 s | same model, `max_tokens: 4096` |
| `judge` (Ollama) | 10/10 | 10/10 | 12,595 | 7,705 | ~103 s | generator 9,147/7,193 + judge 3,448/512 |
| `single` (Kimi hosted) | 10/10 | 10/10 | 4,594 | 2,098 | ~55 s | `kimi-for-coding` via `api.kimi.com` |

The Ollama judge row is **not** cheaper than `single` because the verifier uses
the same model as the generator. The expected win (~1.6× cost vs single/strong)
requires a cheap fast generator plus a strong, concise judge; the Kimi single
row confirms hosted-model end-to-end behavior.

### Judge pipeline

The generator is prompted to be liberal — propose every plausible issue — and
the judge verifies each candidate against the same diff the generator saw,
returning a keep/drop verdict with a recalibrated confidence and (never raised)
severity. Malformed judge output triggers one corrective retry; a second
failure **fails open** (the generator's findings pass through untouched) because
the noise gate still stands downstream. Dropped findings are recorded in the
JSON output for transparency, and the walkthrough footer shows per-role tokens
and `pipeline: judge`.

### Ensemble pipeline (experimental)

> ⚠️ **Experimental, 2–3× token cost.** Ensemble runs the whole review 2–3
> times. For almost every real use case the **judge** pipeline gives you better
> precision per token — prefer it. Ensemble exists for measuring agreement, not
> for cheap CI.

```yaml
review:
  pipeline: ensemble
  roles:
    ensemble: [fast, strong, other]   # 2..3 members
```

Findings agree when they share a path, category, and diff side with lines within
±3 (or overlapping ranges). Agreement clusters backed by ≥2 distinct members
survive, carrying the highest-confidence member finding plus the cluster's mean
confidence (`EnsembleMean` in JSON).

### Cost guardrail

`review.max_run_tokens` (optional, 0 = unlimited) refuses a run whose pre-flight
token estimate — the batched prompt bytes over ~4 bytes/token, times the
pipeline multiplier (single 1×, judge 1.6×, ensemble n×) — exceeds the cap. It
exits `1` **before calling any provider**, so a surprise-large PR can't quietly
run up a judge/ensemble bill.

## Self-hosting (daemon mode)

Prefer to run it yourself instead of per-repo Actions? `sieve serve` is a
single-binary daemon: point a GitHub App's webhooks at one small VPS, bring your
own keys, and it reviews every PR — App auth, a signature-verified webhook
receiver, and a crash-safe coalescing queue, in ~one process.

```sh
sieve serve --config /etc/sieve/server.yml
```

Providers are **server-owned** — a repo's `.sieve.yml` may tune review settings
(min_confidence, excludes, …) but never the model or keys, so an untrusted repo
can't choose your spend. Full walkthrough (GitHub App setup, systemd unit,
reverse-proxy TLS): **[docs/self-hosting.md](docs/self-hosting.md)**. Example
config + unit file live in [`deploy/`](deploy/).

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

## Repository context (Stage 8)

sieve can attach lightweight, structured repository context to the prompt
header. Depth is controlled by `review.context_depth`:

| Depth | What is sent | Best for |
|---|---|---|
| `symbols` (default) | Extracted symbols (functions, types) from the changed files only | fast, safe default; no local checkout needed |
| `repomap` | Repo-wide symbol/import index, capped by `context_max_files` | understanding cross-file structure |
| `blast` | Repomap limited to direct + indirect files around the change | targeted impact surface |

```yaml
review:
  context_depth: symbols        # symbols | repomap | blast
  context_max_files: 20         # cap files in repomap/blast (0 = unlimited)
  context_max_tokens: 8000      # rough token budget for context
  context_langs: [go, c, cpp]    # empty = all languages
```

The CLI passes the current working directory as the repo root, so `repomap` and
`blast` work out of the box. In GitHub Actions and daemon mode there is no
checkout on the review path, so `repomap`/`blast` fall back to `symbols`
quietly.

Context is rendered under `# Repository context` in the prompt and is counted
against the pre-flight token budget.

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

Dependency policy: `yaml.v3`, `doublestar`, `go-cmp` (tests only),
`malivvan/tree-sitter` (wazero-backed C/C++ grammar parsing), and
`tetratelabs/wazero` (WASM runtime for grammars). GitHub REST and all LLM APIs
are spoken via stdlib `net/http`. No streaming, no SSE: blocking completion
calls with per-request timeouts.
