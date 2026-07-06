# sieve

A zero-infra, provider-agnostic PR reviewer. Single Go binary, no server, no
containers — runs as a CLI or (later) a GitHub Action with your own API keys.

**Stage 2 status:** full read-only review. sieve fetches a PR, parses its
diff into an exact line-anchor model, applies noise filters, sends batched
prompts to the LLM provider of your choice, anchor-validates every finding
against the real diff, and emits findings as JSON + a summary table.
**Nothing is ever written to GitHub yet** — posting comments is stage 3.

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

- **stdout** — deterministic JSON `ReviewContext` including `Findings` (pipe to `jq`)
- **stderr** — human summary: file table, findings table, token usage footer

`--dry-run` skips the LLM pass entirely (stage-1 behavior: context dump only).
`--json-only` suppresses the stderr summary. Exit codes: `0` ok · `2` partial
(truncated context or a failed batch) · `1` error.

Draft PRs are skipped (exit 0, empty findings) unless `review.review_drafts: true`.

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
| `Confidence` | model-reported calibrated probability 0..1 (not yet filtered on — stage 3) |
| `Category` | `bug` \| `security` \| `perf` \| `correctness` \| `test` \| `style` |
| `Title` | one line, ≤120 chars |
| `Body` | markdown: why + fix |
| `Suggestion` | optional replacement code (rendered in stage 3+) |

**Anchor gate:** every finding is validated against the parsed diff — the
path must be a kept file and the line(s) must be commentable on the claimed
side within a single hunk. Findings that fail are dropped (never repaired)
and counted in `Stats.FindingsDropped`. This is the anti-hallucination gate:
a comment that would land on the wrong line is worse than no comment.

Output ordering is deterministic: severity, then path, then line.
`Stats` reports `FindingsTotal`, `FindingsDropped`, `BatchesFailed`,
`Requests`, `Retries`, `InputTokens`, `OutputTokens`.

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
make cover   # gates: diff/findings >= 90%, provider >= 85%, overall >= 80%
make golden  # regenerate parser, prompt, and E2E golden files
```

Dependency policy: `yaml.v3`, `doublestar`, `go-cmp` (tests only) — nothing
else. GitHub REST and all LLM APIs are spoken via stdlib `net/http`. No
streaming, no SSE: blocking completion calls with per-request timeouts.
