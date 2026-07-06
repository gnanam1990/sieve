# Stage 2 — Provider Layer + Review Pass + Findings Schema

## R0 — ZERO survey: lift vs rewrite

Surveyed `~/Desktop/dev/zero` provider packages (`internal/zeroruntime`,
`internal/providers/{providerio,anthropic,openai}`, `internal/usage`,
`internal/modelregistry`, `internal/providercatalog`). No ZERO module is
imported; adapted patterns are re-implemented in `internal/` with
attribution comments at the adoption sites.

| ZERO component | Decision | Rationale |
|---|---|---|
| `Provider` interface (`StreamCompletion` → event channel) | **fresh write** | ZERO is streaming-only with tool-call/reasoning event states; sieve is single-shot request/response by spec |
| SSE machinery, stall watchdogs, `ScanSSEDataWithContext` | **not carried** | no streaming anywhere in sieve (spec constraint) |
| Tool-calling / reasoning-block / conversation state | **not carried** | agent concerns; sieve sends one user message |
| `providerio` retry policy (`ShouldRetryStatus`, `Backoff`, `RetryAfter`) | **pattern-lift, policy changed** | lifted: rebuild-per-attempt, Retry-After parsing (seconds + HTTP-date), 30s Retry-After cap, ctx-aware waits. Changed per spec: retryable set is 429 + all 5xx + `overloaded_error` (ZERO: 429/503/529 only), jitter added (ZERO has none), max 3 attempts (ZERO: 6) |
| Bounded 64KB error-body read + `{"error":{type,message}}` parsing | **pattern-lift** | battle-tested defensive habit; same envelope works for Anthropic and OpenAI-compat |
| Anthropic adapter (endpoint, `x-api-key`, `anthropic-version`) | **pattern-lift, simplified** | same wire shape minus streaming, cache breakpoints, thinking budgets |
| OpenAI adapter (`Authorization: Bearer`, `choices[0].message.content`) | **pattern-lift, simplified** | non-streaming so ZERO's forced `stream_options.include_usage` is unnecessary — usage is present in blocking responses |
| ZERO's darwin `DisableKeepAlives` stall fix | **not carried, noted** | that fix targets long-lived streaming agents; sieve's single-shot calls with per-request ctx timeouts don't exhibit the reused-dead-connection hang. Revisit if smoke runs ever stall |
| `NormalizeUsage` cache-token clamping | **not carried** | sieve's Usage is 2 fields; no cache accounting until a cost feature needs it |
| Token estimation | **fresh write** | ZERO has no estimator (usage is provider-reported); sieve needs pre-flight budgeting → bytes/4 heuristic in `internal/prompt` |
| Model registry / provider catalog / credstore / OAuth | **not carried** | 4-layer key resolution is config-file-manager territory; sieve uses exactly one mechanism: env var named by `api_key_env` (ZERO's "layer 2") |

## Decisions / smallest-reasonable-choice notes

- **Ollama quirks found (openai-compat)**: (1) local Ollama ignores the
  `Authorization` value but sieve still requires the named env var to be
  non-empty — docs say set `OLLAMA_API_KEY=ollama`; (2) some compat servers
  omit `usage` in responses → treated as zero, not an error; (3) `max_tokens`
  (not `max_completion_tokens`) is sent because Ollama/OpenRouter/Groq all
  accept it. Nothing is silently special-cased per server.
- **Corrective retry prompt** re-sends the original user prompt with a
  correction note appended (per spec "appended to the same request"), not a
  multi-turn conversation — providers here are stateless single-shot.
- **`Stats.FindingsTotal` counts surviving findings** (post-gate), and
  `FindingsDropped` counts rejects; total raw = sum of both.
- **`EndLine` ranges must sit within a single hunk** — GitHub multi-line
  review comments cannot span hunks, so "same hunk-reachability" is enforced
  literally.
- **Context lines are annotated `R:<NewNum>`** in prompts. They are also
  commentable on LEFT, but one anchor per line keeps the prompt unambiguous;
  the validator still accepts LEFT context anchors if a model emits them.
- **`ReviewContext` gained `Body`** (PR description) since prompts need it;
  `json:",omitempty"` keeps old dry-run outputs identical when empty.
- **Draft skip returns the stage-1 context** with `Findings: []` and zero
  provider traffic; the notice goes to stderr via slog.
- **model/api_key_env/base_url/fixture are validated only for the LLM path**
  (`ValidateForReview`) so `--dry-run` keeps working with an empty config.
  Range checks (max_tokens etc.) always run.
- **System prompt is a `text/template`** rendering `MaxTitleLen` from
  `internal/findings`, so the prompt's contract can't drift from the
  validator. Golden-pinned (`internal/prompt/testdata/system.golden.md`).
- **Fake provider usage** is estimated bytes/4 on both sides so stats
  plumbing is deterministic offline.

## Offline E2E (gate 3)

`TestRunEndToEndFakeGolden` (`internal/review`): fake GitHub serving the
`multi_file_multi_hunk.diff` stage-1 fixture + `provider.type: fake` with a
canned response containing 1 valid + 1 hallucinated-anchor + 1 bad-severity
finding. Exactly 1 survives, `FindingsDropped: 2`, output golden-pinned at
`internal/review/testdata/e2e_fake.golden.json`.

## Live smoke (gate 4)

Provider: local **Ollama** OpenAI-compat endpoint proxying the
`qwen3-coder:480b-cloud` model (no hosted API key present on this machine;
Ollama ignores the bearer value, `OLLAMA_API_KEY=ollama` satisfies the
non-empty check). Config:

```yaml
provider:
  type: openai-compat
  model: qwen3-coder:480b-cloud
  base_url: http://localhost:11434/v1
  api_key_env: OLLAMA_API_KEY
  timeout_seconds: 300
```

Run 1 — `sieve review --repo cli/cli --pr 13784 --config .sieve.yml`, exit **0**:

```
2 files total, 2 to review, 0 skipped, +53 -0 lines
0 findings (0 dropped by anchor gate), 1 requests (0 retries, 0 batches failed), tokens in/out 11507/7
```

Run 2 — `sieve review --repo cli/cli --pr 13723 --config .sieve.yml`, exit **0**:

```
5 files total, 5 to review, 0 skipped, +142 -12 lines
0 findings (0 dropped by anchor gate), 1 requests (0 retries, 0 batches failed), tokens in/out 26270/7
```

Both runs returned `{"findings": []}` (7 output tokens) — clean, small,
already-merged PRs; zero findings is the honest answer and the spec accepts
"anchor-drop counter exercised **or zero**". The findings + drop paths are
exercised end-to-end by the offline E2E golden (1 survivor, 2 dropped).

One test-only flake found during gating: the anthropic timeout test blocked
its handler on `r.Context().Done()`, which under `-coverpkg` instrumentation
sometimes never fired (client-disconnect detection), hanging `srv.Close`.
Fixed by releasing the handler via an explicitly closed channel.

## Coverage at tag time

- `internal/diff`: 90.3% (gate ≥ 90%, package untouched this stage)
- `internal/findings`: 100.0% (gate ≥ 90%)
- `internal/provider/...`: 95.2% (gate ≥ 85%, measured with
  `-coverpkg=./internal/provider/...` since the shared HTTP/retry code in
  the parent package is exercised by the adapter packages' tests)
- overall (`-coverpkg=./...`): 90.0% (gate ≥ 80%)
