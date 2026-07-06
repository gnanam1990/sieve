# Deferred gates — live-validation batch (run after the stage-09 PR merges)

Per the v2 protocol, stages 05–09 are built offline-first and merged on green;
the live gates below run **once, as a single batch**, after the stage-09 PR
merges. These gates are **deferred, not skipped**. None is "done" until its
evidence lands in the owning stage's section below. Run in order in one session;
push the stage tags (`stage-05` … `stage-09`) as each stage's gates clear.

- [x] **Batch setup** — OpenRouter key was invalid; pivoted to local Ollama
  with `OLLAMA_API_KEY=ollama`.
- [x] **stage-03 gate 4 + 5** — seeded private sandbox
  `sieve-sandbox-recall-kimi-2026-07-06`, model `kimi-k2.7-code:cloud` via
  Ollama. First run: **10/10 recall**, 8 inline + 1 note. Fix-push re-run:
  walkthrough edited in place, plants 5 and 10 moved to **Resolved**, no
  duplicate inlines. Calibration report: see new section appended to Stage 3.
- [x] **stage-04 gate 2** — tag `v0.0.10-rc1` on main → release workflow → 4
  binaries + checksums + raw assets published; `v0` moved only because the
  bootstrap override was explicitly passed.
- [x] **stage-04 gates 3–5** — hosted-runner `@v0` review on a sandbox PR (fake
  provider); install.sh + go install verified. Fork sim blocked by same-owner
  GitHub restriction; fork detection covered by unit tests.
- [x] **stage-05 live** — sandbox repo `gnanam1990/sieve-sandbox-recall-kimi-2026-07-06`.
  Gave 👎 to the data-race and HTTP-timeout inlines and ran `sieve learnings`;
  it clustered the negatives into `.sieve/learnings.md` with the rule
  "Do not report potential bugs without a concrete reproduction case or clear
  evidence of incorrect runtime behavior." Committed and pushed the file on the
  `planted-issues` branch. Re-run with `kimi-k2.7-code:cloud` via Ollama posted
  8 findings and the walkthrough footer showed **`learnings: 1 rules active`**.
  During the wipe-store → `sieve sync` → `sieve stats` verification the live and
  synced aggregates diverged because (1) `recordOutcomes` did not emit
  `resolved-anchor-gone` for stale non-active inline comments, and (2) a failed
  run that returned zero findings could close fingerprints that a later
  successful run reopened, so `memory.Aggregate` treated them as addressed.
  Fixed both in `internal/review/outcomes.go` and `internal/memory/stats.go`
  (tests green). After the fixes the category aggregates (`Posted`,
  `AddressedByAnchor`) match between live and sync; `Runs`/`InTok`/`OutTok`
  differ by design because run events are live-only telemetry that `sync`
  intentionally does not reconstruct. One reaction snapshot drifted between the
  live run and the sync (a transient 👎 on a stale comment), which is expected
  because reactions are human-controlled and can change between review and sync.
- [x] **stage-06 live** — fresh private sandbox
  `gnanam1990/sieve-sandbox-stage6-2026-07-06`, PR #1. Because the OpenRouter
  key is still invalid, the only available local model is Ollama
  `kimi-k2.7-code:cloud`; it was used for **all roles**, so the judge could not
  realize its intended cost win (cheap generator + strong judge). All three
  runs still achieved **10/10 recall** and **10/10 precision** against
  `testdata/sandbox/plants.md`:

  | Pipeline | Recall | Precision | In tokens | Out tokens | Wall time | Footer |
  |---|---|---:|---:|---:|---:|---|
  | `single` | 10/10 | 10/10 | 4,597 | 3,106 | ~45 s | `pipeline: single` |
  | `single/fast` | 10/10 | 10/10 | 4,597 | 3,923 | ~47 s | `pipeline: single` |
  | `judge` | 10/10 | 10/10 | 12,595 | 7,705 | ~103 s | `pipeline: judge (killed 0, failed-open 0) · fast 9147/7193 · strong 3448/512` |

  The judge run's walkthrough footer correctly shows per-role tokens and the
  pipeline name. README's pipeline selection table was populated from these
  numbers with a caveat that the cost comparison is not final until a hosted
  `fast` generator can be paired with a stronger judge.
- [x] **stage-07 live** — real GitHub App `sieve-stage7-2026-07-06` (App ID
  `4232122`) created via the manifest flow, installed on
  `gnanam1990/sieve-sandbox-stage6-2026-07-06` (installation `144839895`).
  `sieve serve` started locally on `127.0.0.1:8787` with App auth + Ollama
  providers. A manually replayed, signed `pull_request:opened` webhook for PR #2
  was accepted; the daemon minted an installation token, fetched repo config,
  ran the judge pipeline, and posted a walkthrough comment with footer
  `pipeline: judge · fast 2.7k/1.0k · strong 2.7k/286`.

  Two fast `pull_request:synchronize` replays (head `0000…0001` then real head
  `e67c3aa…`) both returned HTTP 202; `/healthz` showed `queue_depth: 1` after
  both, and `data/queue.jsonl` contains both enqueue records for the same repo/PR
  with only the newest head replayed after the crash. `kill -9` of the daemon
  mid-run, restart with the same command, and the pending job finished — the log
  ended with `op:done` for delivery `stage7-sync-b-0002` (the real head).

  `docs/self-hosting.md` was walked as far as the local dev machine can go: App
  creation, PEM install, env setup, `sieve serve` validation checks, `/healthz`,
  webhook-driven review, coalescing, and crash replay all executed. The systemd
  + reverse-proxy sections require a real VPS/sudo and were not exercised in
  this session; the capacity note in the doc was updated with measured numbers.
- [x] **stage-08 live** — ran all three `context_depth` values against sandbox
  PR #2 (`gnanam1990/sieve-sandbox-stage6-2026-07-06#2`) using local Ollama
  `qwen3-coder-next:cloud`. Preflight estimates and reported input tokens stayed
  well under the 8,000-token `context_max_tokens` cap:

  | Depth | Pre-flight estimate | Input tokens | Output tokens | Context section |
  |---|---:|---:|---:|---|
  | `symbols` | 1,387 | 2,678 | 7 | none (only changed-file symbols) |
  | `repomap` | 1,663 | 3,010 | 7 | `## Repo map` |
  | `blast` | 1,387 | 2,678 | 7 | none for this PR (no indirect files) |

  A logging proxy on `127.0.0.1:11435` captured the full prompt for `repomap` and
  confirmed the `# Repository context` / `## Repo map` section is inserted before
  `# Changed files`. For this tiny sandbox `blast` found no extra files, so its
  estimate matched `symbols`; both are still under the cap.
- [ ] **Finalize defaults** — apply the calibration-derived `min_confidence` /
  `inline_min_confidence` as a small reviewed PR (remove the TODO), merge.
- [ ] **Tag `stage-05`–`stage-09`** — push each stage tag after its live gate
  clears. Then STOP; next is the v0.1.0 launch checklist.

---

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

## Live calibration (gate 4 + 5) — 2026-07-06

Re-run of the seeded sandbox using a **local Ollama** model because the
OpenRouter key in Keychain was invalid. Model: `kimi-k2.7-code:cloud` via
Ollama OpenAI-compat endpoint. Sandbox repo:
`gnanam1990/sieve-sandbox-recall-kimi-2026-07-06`.

### First run — planted issues

10/10 recall against `testdata/sandbox/plants.md`:

| Plant | Expected | Found | Severity match | Notes |
|-------|----------|-------|----------------|-------|
| 1 SQL injection | critical security | ✅ | yes | inline |
| 2 nil deref | critical bug | ✅ | yes | inline |
| 3 data race | critical bug | ✅ | yes | inline |
| 4 slice bound | major bug | ✅ | yes | inline |
| 5 swallowed error | major correctness | ✅ | yes | inline |
| 6 hardcoded password | critical security | ✅ | yes | inline |
| 7 HTTP timeout | major bug | ✅ | yes | inline |
| 8 unbounded read | major security | ✅ | yes | inline |
| 9 dead branch | minor correctness | ✅ | yes | note |
| 10 misleading name | nit style | ✅ | yes | inline |

Result: 8 inline, 1 note, 0 dropped by anchor gate. Tokens in 4,489 / out 2,486.
PR: https://github.com/gnanam1990/sieve-sandbox-recall-kimi-2026-07-06/pull/1

### Fix-push re-run

Fixed plants 5 (propagate error) and 10 (rename `min` → `max`). Re-run with
`scripts/sandbox_recall.sh fix`:

- Walkthrough **edited in place**.
- Plants 5 and 10 listed under **Resolved since last review (7)**.
- 8 remaining findings posted inline; **zero duplicate** inline comments.
- Tokens in 7,079 / out 7,837 (second run sees bigger prompt from context + meta).

### Calibration-derived default recommendation

The local model reported all findings at confidence ≥ 0.90, so the current
defaults (`min_confidence: 0.6`, `inline_min_confidence: 0.8`) kept every
planted issue. No change recommended from this run. A frontier-model run
(Claude Sonnet via OpenRouter) should repeat the gate before v0.1.0 to confirm
cost/precision trade-offs; if its confidence distribution differs materially,
adjust then.

## Coverage at tag time

- `internal/diff`: 90.3% (gate ≥ 90%, package untouched this stage)
- `internal/findings`: 100.0% (gate ≥ 90%)
- `internal/provider/...`: 95.2% (gate ≥ 85%, measured with
  `-coverpkg=./internal/provider/...` since the shared HTTP/retry code in
  the parent package is exercised by the adapter packages' tests)
- overall (`-coverpkg=./...`): 90.0% (gate ≥ 80%)

---

# Stage 3 — Noise Gate + Comment Lifecycle

New packages: `internal/gate` (routing), `internal/fingerprint` (content
anchors), `internal/render` (markdown), `internal/post` (all GitHub writes).
`internal/gh` gains a `TokenSource` abstraction and a single-shot write
transport (`Send`); posting is enabled only by `--post`.

## Decisions / smallest-reasonable-choice notes

- **Config schema: `max_comments` → `max_inline_comments`.** Stage 2 reserved
  `review.max_comments` (1–50, unused). Stage 3's canonical gate config (R2)
  names the inline cap `max_inline_comments` (1–30), so the reserved key was
  renamed rather than kept as a dead alias. The env override followed:
  `SIEVE_MAX_COMMENTS` → `SIEVE_MAX_INLINE_COMMENTS`. `min_confidence` was
  repurposed from its reserved slot; its default dropped 0.7 → 0.6 to match
  R2. A committed `.sieve.yml` still using `max_comments` now fails with the
  unknown-key error — acceptable pre-1.0, and the stage owns the schema.
- **No `post` config key, by construction.** The strict YAML decoder already
  hard-errors on any unknown key, so `post:` anywhere is rejected — asserted by
  `TestPostKeyIsRejected` (R1.1). Posting has exactly one switch: `--post`.
- **Write transport is single-shot.** `gh.Send` does **not** retry. A review or
  comment POST is not idempotent; a blind retry risks double-posting. Safety on
  re-run comes from the dedupe design (locate-by-marker + fingerprint skip), not
  transport retries. Reads keep the retrying path.
- **Posting-isolation test, two ways.** The precise check: the GitHub write
  transport `gh.Send` is only *called* from `internal/post`. The broad net: no
  package under `internal/` references a mutating HTTP method except
  `internal/post` (GitHub) and `internal/provider` (the LLM API, which targets
  the model endpoint, not the GitHub client, and so is outside R1.2's "against
  the GitHub client" scope). Both in `internal/post/isolation_test.go`.
- **Fingerprint excludes line numbers, by design (R4).** `fp =
  sha256(path|side|category|norm(title)|trim(anchorContent))[:16]`. A finding
  that only drifts to a new position keeps its fingerprint (not re-posted);
  content edits, title rewrites, and renames change it. A rename yields a new
  fingerprint (the old one shows as Resolved) — accepted and documented rather
  than chased with rename detection.
- **Metadata fps cap eviction (R5).** The block carries only the *current*
  run's fingerprints (Resolved is derived from the prior block, a one-run
  window, and is not re-persisted). So the 200-entry cap has no "resolved"
  entries to evict "oldest-first"; it degenerates to dropping the least-severe
  current findings, which is the intended size bound. Noted at
  `internal/gate/meta.go`.
- **Non-`--post` runs use `prior = nil`.** Cross-run dedupe (Repeated/Resolved)
  needs the previous walkthrough, which is only fetched when posting. Without
  `--post`, the gate still runs and its tiering lands in the JSON `Gate` block,
  but nothing is marked Repeated/Resolved — matching "without `--post`,
  behavior is exactly Stage 2 plus the gate decisions."
- **Draft + `--post` (R1.4).** The draft skip happens before the review pass, so
  a draft PR with `review_drafts:false` never reaches the gate or the poster:
  skip, notice, exit 0. No separate posting guard needed.
- **Failure model (R7).** Walkthrough failure → hard error → exit 1 (inline
  review not attempted). Partial inline failure → `Stats.InlinePostFailed` → exit
  2, run still succeeds. Wired in `cmd/sieve/main.go`.

## Gates at tag time

`make lint` clean; `make test` green with `-race -shuffle=on` on the local
toolchain (CI mirrors ubuntu + macos). Offline E2E idempotency + resolved
goldens green (`internal/review/testdata/walkthrough_run1.golden.md`,
`walkthrough_run3_resolved.golden.md`).

### Coverage

- `internal/fingerprint`: 100.0% (gate = 100%)
- `internal/gate`: 93.8% (gate ≥ 90%)
- `internal/render`: 96.0% (gate ≥ 85%)
- `internal/post`: 88.2% (gate ≥ 85%)
- overall (`-coverpkg=./...`): 91.7% (gate ≥ 85%)

## Adversarial spec review

Ran a 6-dimension adversarial review (safety R1, gate R3, fingerprint/meta
R4–R5, render R5–R6, post/orchestration R6–R7, completeness) with per-finding
skeptic verification. 168 code reads across the six reviewers; **zero
confirmed defects or spec violations**.

## Gate 4 — Seeded sandbox recall (calibration) — PENDING LIVE RUN

This gate requires a frontier-model API key and creates a live private repo, so
it is **not runnable in the offline build**. Everything needed is committed:

- Planted target: `testdata/sandbox/service.go` (10 issues across severities).
- Plant manifest: `testdata/sandbox/plants.md`.
- Orchestrator: `scripts/sandbox_recall.sh` (`all` creates repo+PR and reviews
  with `--post`; `fix` pushes a 2-plant fix and re-reviews). It only ever posts
  to a private repo it creates under the authenticated user (R1.3).

To run:

```sh
export ANTHROPIC_API_KEY=sk-ant-...      # or OPENROUTER_API_KEY
scripts/sandbox_recall.sh all            # 4a/4b: create + review --post
scripts/sandbox_recall.sh fix            # 4d: fix 2 plants, re-review --post
```

### Calibration report (to fill in after the live run)

- **Recall** (found / missed, per plant): _TBD_ — table keyed by `plants.md` #.
- **Precision** (non-plant findings judged real vs noise): _TBD_.
- **Findings-per-severity histogram:** _TBD_.
- **Confidence distribution:** _TBD_.
- **Token usage (in / out):** _TBD_ (from `sandbox_review.json` `Stats`).
- **Permalinks / screenshots:** walkthrough comment, one inline comment, one
  applied suggestion block: _TBD_.
- **Resolved-flow evidence (4d):** walkthrough edited in place; plants 5 and 10
  under Resolved; zero duplicate inlines: _TBD_.

## Gate 5 — Default-tuning proposal (for review, not silently changed)

Current defaults: `min_confidence 0.6`, `inline_min_confidence 0.8`,
`inline_min_severity major`, `max_inline_comments 10`.

Proposal to revisit **after** the Gate 4 numbers exist:

- If precision on the inline tier is high (few false positives at ≥ 0.8),
  consider lowering `inline_min_confidence` to `0.75` to lift recall of real
  major/critical issues.
- If the walkthrough notes tier is noisy (many low-value sub-0.8 findings),
  consider raising `min_confidence` to `0.65`.
- `inline_min_severity major` and `max_inline_comments 10` look right for a
  single-notification review; hold pending data.

These are **proposals**; the committed defaults are unchanged until the
calibration run justifies a move.

---

# Stage 4 — GitHub Action Packaging + Release Engineering

Deliverables: `.goreleaser.yaml`, `action.yml` (composite), `install.sh`,
`.github/workflows/release.yml` + `action-smoke.yml`, `docs/forks.md`,
action-first README. Sieve code changes limited to R1 (version) and R4 (fork
safety); everything else is packaging.

## Prerequisite deviation (important)

The stage prerequisite is "stage-03 tagged and merged to main". At the time of
building, the actual state was: `origin/main` at the Stage 1 tip, stage-02
pushed (PR #1 open, unmerged), **stage-03 local-only and untagged**. The whole
chain (stage-02 → stage-03 → stage-04) is unmerged. Stage 4 was therefore built
on a branch off `stage-03-noise-gate-posting` (where the code lives) and kept
local per the standing "push/PR/tag only on explicit request" rule.

Consequence: the release-engineering **live gates cannot run until the chain is
merged to main and pushed**, because the release workflow triggers on `v*` tags
pushed to the repo and the Action downloads from GitHub *releases*. All configs
are authored and **validated offline** (see below); the live gates (2/3/4 and
parts of 5) are pending the merge + push + a model API key.

## Decisions / smallest-reasonable-choice notes

- **goreleaser `brews` → `homebrew_casks`.** The spec (R5) says a `brews` block,
  but goreleaser 2.10+ deprecated `brews` (and `homebrew_casks.binary`) —
  `goreleaser check` fails on deprecated keys. Migrated to a binary `homebrew_casks`
  entry (same Homebrew install-path intent) with a post-install hook that strips
  the Gatekeeper quarantine flag (the binary is unsigned; cosign is backlog).
- **Provider config injection.** sieve reads provider type/base_url/api_key_env
  only from a config file (no env override, and adding one is outside the "R1/R4
  only" sieve-change budget). So `action.yml` **appends** a provider block to
  `config_path` only when the file has none — a user's own `provider:` block (and
  all review/gate settings) always wins. Model, type, base_url, api_key_env come
  from inputs; the key is read from the env var named by `api_key_env_name`.
- **Secrets are never inputs.** Every step maps `${{ inputs.* }}` into `env:` and
  references `$VARS` in bash (no `${{ }}` interpolation into shell — injection
  safety), and the model key is only ever an env var, never an input (inputs are
  logged). Documented inline in `action.yml`.
- **Floating major tag.** `scripts/move_major_tag.sh` derives `v0` from the
  release tag, pins the released version into a `VERSION` file, force-moves `v0`,
  and pushes. `action.yml` resolves its version from that `VERSION` file at the
  pinned ref, so `@v0` always downloads the matching binary. The commit is
  guarded (`git diff --cached --quiet || git commit`) so the tag still moves even
  when VERSION is already the released value (the case on the first rc, whose
  commit already carries `VERSION=v0.0.9-rc1`). By convention a major tag tracks
  the latest **stable** release, so the script **skips prereleases by default**;
  `release.yml` sets `MOVE_MAJOR_ON_PRERELEASE=true` for the v0 bootstrap so
  gates 2/3 can exercise `@v0` with the rc — remove that override after the first
  stable `v0.x.0`.
- **Exit-code policy in the Action.** sieve exit 2 (partial) is surfaced as a
  warning and the job stays green unless `fail_on_partial: true` — a truncated
  diff must not break someone's CI.
- **Version internal vs tag.** goreleaser's `{{.Version}}` drops the leading `v`
  (tag `v0.0.9-rc1` → binary reports `sieve 0.0.9-rc1`); the release tag and the
  `VERSION` file keep the `v`. The Action downloads by tag name (with `v`), the
  walkthrough footer shows the `v`-less version. Both are internally consistent.
- **License: Apache-2.0.** Added in the stage-04 PR at the user's direction; the
  goreleaser archive re-includes `LICENSE`.

## Offline validation (done)

- `go test ./... -race` green; `golangci-lint` clean; coverage 92.1% overall
  (all per-package gates met).
- `shellcheck install.sh scripts/*.sh` — clean.
- `actionlint` — clean (all workflows).
- `goreleaser check` — valid.
- `goreleaser release --snapshot --clean` — builds all four binaries
  (linux/darwin × amd64/arm64) with the R1 ldflags, produces tar.gz archives,
  **raw binaries named `sieve_<os>_<arch>`**, and `checksums.txt` (sha256). A
  built binary reports a real stamped version via `sieve version`.
- `go install ./cmd/sieve` produces a `sieve` binary; module path
  `github.com/gnanam1990/sieve/cmd/sieve` is correct (the `@latest` form needs a
  published tag).

## Adversarial security review (4 findings, all fixed)

A 4-dimension adversarial review (action security, fork safety, release
pipeline, install/completeness) with per-finding skeptic verification surfaced
four confirmed defects — all fixed and regression-tested:

1. **critical — `move_major_tag.sh`:** an unconditional `git commit` aborted the
   release job under `set -e` when VERSION was already the released tag (the
   first-rc case), so `v0` never moved and `@v0` broke. Fixed: guard the commit,
   always move + push the tag. Reproduced the failure and the fix in a throwaway
   repo.
2. **major — `internal/gh/client.go`:** `Event.Found` keyed on `base != ""`,
   which is set by the `repository` block present in *every* event — so a
   push/workflow_dispatch/schedule event was misclassified as a fork and a
   legitimate same-repo run was skipped with a bogus notice. Fixed: `Found` now
   requires an actual `pull_request` object (pointer non-nil).
   `TestNonPullRequestEventsAreNotForks` covers it.
3. **major — `move_major_tag.sh`:** moved the "stable" `v0` onto a *prerelease*
   binary unconditionally. Fixed: skip prereleases by default; the bootstrap
   override (above) is explicit and time-boxed.
4. **minor — `install.sh` + `action.yml`:** the checksum `grep` under
   `set -e`/pipefail aborted before the friendly "no checksum entry" message.
   Fixed with `|| true` on the substitution so the explicit diagnostic runs.

## Homebrew tap — one-time setup (R5, do before the first stable release)

1. Create a public repo `gnanam1990/homebrew-tap` (empty is fine; goreleaser
   writes `Casks/sieve.rb`).
2. Create a Personal Access Token (classic: `repo`, or fine-grained: contents
   read/write on `homebrew-tap`) and add it to the **sieve** repo as the secret
   `HOMEBREW_TAP_TOKEN` (the default `GITHUB_TOKEN` cannot push to another repo).
3. On the next **stable** (non-rc) tag, goreleaser pushes the cask;
   `brew install gnanam1990/tap/sieve` then works. Prereleases skip the tap
   (`skip_upload: auto`), so the rc gate needs neither the tap nor the token.

## Live gates — DONE (2026-07-06)

- **Gate 2 (rc release):** Tagged `v0.0.10-rc1` (after a hotfix PR #9 for
  action/install fixes discovered during the batch). Release workflow published:
  - `sieve_darwin_amd64`, `sieve_darwin_arm64`, `sieve_linux_amd64`,
    `sieve_linux_arm64`
  - `sieve_0.0.10-rc1_darwin_amd64.tar.gz`, `sieve_0.0.10-rc1_darwin_arm64.tar.gz`,
    `sieve_0.0.10-rc1_linux_amd64.tar.gz`, `sieve_0.0.10-rc1_linux_arm64.tar.gz`
  - `checksums.txt`
  - `v0` major tag moved to the rc commit via the `MOVE_MAJOR_ON_PRERELEASE`
    bootstrap override (will be removed after first stable v0.x.0).
- **Gate 3 (end-to-end action):** Workflow `sieve action test` in
  `gnanam1990/sieve-sandbox-recall-kimi-2026-07-06` uses `gnanam1990/sieve@v0`
  with a fake provider, downloads `v0.0.10-rc1`, checksum-verifies, and exits 0.
  Run: https://github.com/gnanam1990/sieve-sandbox-recall-kimi-2026-07-06/actions/runs/28814334646
- **Gate 4 (fork path):** Real fork PR cannot be opened from the same owner
  account (GitHub forbids a user from owning both parent and fork). Fork
  detection logic is unit-tested (`internal/gh/event_test.go`); a live fork PR
  should be verified once an external contributor is available.
- **Gate 5 (install paths):**
  - `install.sh` with `SIEVE_VERSION=v0.0.10-rc1` and `PREFIX=/tmp/sieve-install`
    downloaded `sieve_darwin_arm64`, checksum-verified, and installed; `sieve version`
    reported `0.0.10-rc1`.
  - `go install github.com/gnanam1990/sieve/cmd/sieve@v0.0.10-rc1` produced a
    binary; `sieve version` reported `dev` (built by `go install`, no ldflags)
    but the module path/version resolved correctly.

## Default-tuning / backlog carried forward

Stage 3's calibration (Gate 4 there) is still pending a live model run; the
default-tuning proposal in the Stage 3 notes stands. Stage 4 adds no new
gate-tuning surface.

---

# Stage 5 — Incremental Review + Learnings + Local Memory

New packages: `internal/incremental` (delta planning), `internal/memory`
(outcome store + aggregation), `internal/learnings` (clustering + rule
drafting). `internal/gate` metadata went v1→v2; new subcommands `sync`,
`learnings`, `stats`.

## Decisions / smallest-reasonable-choice notes

- **Compact record carries category + endline + tier** beyond the spec's
  `{f,p,l,sd,s,c,t,cid}`: category is required to recompute the content
  fingerprint for the anchor-gone resolution check (R4's fp includes category);
  endline preserves ranges; tier drives the cap-overflow eviction. Title is
  stored **in full** (findings already bound it to ≤120), not truncated to 80 —
  truncation would break the anchor-gone fingerprint recompute.
- **Meta marker is version-agnostic** (`<!-- sieve:meta ` locator + a `vN`
  display token) so a v2 sieve still reads v1 walkthroughs; the schema version
  lives in the JSON `v` field.
- **Delta only on differing head SHA + v2 prior + `--post`.** Same SHA →
  full review (preserves stage-3 idempotency). Non-post runs are always full (no
  prior walkthrough to delta from). Full-fallback reasons are recorded.
- **Inline review posts before the walkthrough** (order change from stage 3) so
  comment IDs can be recovered (via the versioned, defensively-parsed
  `<!-- sieve:fp v1 … -->` marker, validated as 16-hex) and stamped into the v2
  metadata this run. Failure model unchanged: partial inline → exit 2 (still
  posts the walkthrough); walkthrough failure → exit 1.
- **Reactions are per-comment snapshots** (latest wins in aggregation), so
  re-running a review never double-counts. Fetched from the REST comment list
  (inline cids only). **Dismissals** need thread-resolution state, which REST
  cannot report, so one pinned GraphQL query (`internal/gh/graphql.go`,
  `ResolvedThreads`) reads it — a read POST to `/graphql`, allowlisted in the
  posting-isolation test (not a REST mutation).
- **`sieve sync` reconstruction limits.** Run/token history is live-only
  telemetry (not on GitHub) and is omitted. A posted finding no longer active is
  reconstructed as anchor-gone (the common "fixed" case); the live
  re-review-absent nuance (the model changed its mind on a re-reviewed file) is
  not distinguishable from GitHub. Learnings clusters and reaction/dismissal
  aggregates reconstruct exactly — the equivalence test compares aggregates, not
  bytes, and covers idempotency. Rewrite is atomic (temp + rename).
- **`memoryHost` is `github.com`** (sieve targets github.com only today); derive
  from the API base URL when GHE support lands.
- **Calibration and launch-tuning are distinct.** `review.calibration` is the
  runtime, opt-in, per-category confidence scaler. The *shipping* defaults for
  `min_confidence` / `inline_min_confidence` are finalized from the stage-03
  gate-4 report in the live batch — a `// TODO(calibration)` marks the site.

## Offline gates

`make lint` clean; `make test` green `-race -shuffle=on`; coverage floors met —
incremental 100%, memory 92%, gate 94%, fingerprint 100%, render 90.7%, post
88.5%, overall 87.9% (all ≥ their floors). Phase A delta matrix + goldens and
Phase B store/sync/learnings/stats/calibration suites + goldens green.

## Live validation

All Stage 5 live steps are in the deferred-gate checklist at the top of this
file (three-push token-savings run; 👎/dismiss → `learnings` → commit →
`learnings: N active` + no re-flag; wipe → `sync` → `stats` matches). Not run
offline.

---

# Stage 6 — Multi-Model Pipelines (judge + ensemble)

## Decisions / smallest-reasonable-choice notes

- **Named providers map + roles, singular kept for back-compat.** `providers:`
  is a name→provider map; `review.roles` selects which name plays each role.
  The legacy singular `provider:` block normalizes to `providers.default` with
  `review.roles.reviewer: default` — existing configs need no migration. Setting
  both is a hard error with a migration hint. `ActiveRoles()` returns only the
  providers the chosen pipeline actually calls, so a dry run needs none fully
  configured and `ValidateForReview` only checks the ones that matter.
- **Shared generation, per-provider run.** `buildGeneration` assembles the
  batches, anchors, and learnings injection **once**; `runGeneration` runs one
  provider over them. The single reviewer and the judge's generator call it
  once; ensemble calls it per member. This keeps the prompt identical across
  members (real agreement, not prompt drift) and avoids re-fetching file
  content. All `rc.Stats`/`rc.Findings` mutation happens on the main goroutine
  after `wg.Wait()`; goroutines only write their own `results[i]` slot.
- **Judge = liberal generator + verifier.** The generator template is the
  single-reviewer prompt made explicitly liberal; the judge verifies each
  finding against the same diff and returns a per-index verdict. Verdicts are
  strict-decoded (`DisallowUnknownFields`, exactly one verdict per index in
  `[0,n)`); a malformed response gets one corrective retry, then **fails open**
  (generator findings pass through untouched, `JudgeFailedOpen` counted) because
  the noise gate still stands. Severity is clamped: the judge may lower or keep
  but never raise above the generator's (no judge-invented criticals); an
  invalid judge severity is ignored; confidence is trusted, clamped to [0,1].
  Dropped findings are recorded in `JudgeDrops` (JSON output) for transparency.
- **Judge batches per file.** Verdict indices are per-file `0..n-1`; the judge
  is called once per file that has findings, so index→finding mapping stays
  within one file's group. `JudgeUser` self-limits to the 24k budget the same
  way `BuildBatches` does — drop the content attachment, then truncate hunks.
- **Ensemble agreement = path + category + side + (±3 line or range overlap).**
  Side is required (a LEFT and RIGHT anchor at the same number are different
  lines — a defensible tightening of the spec's path/category/line rule).
  Union-find clusters; only clusters with ≥2 **distinct** members survive
  (two findings from one member cannot self-agree). The survivor is the
  highest-confidence member finding, annotated with the cluster mean
  (`EnsembleMean` in JSON). Emission is root-sorted for determinism; the gate
  re-sorts anyway. Ensemble is documented as experimental, 2–3× cost, prefer
  judge.
- **Cost guardrail is pre-flight and provider-free.** `review.max_run_tokens`
  (0 = unlimited) compares the batched-prompt estimate (bytes/4 × multiplier:
  single 1×, judge 1.6×, ensemble n×) against the cap and refuses with the
  estimate **before any LLM call**, mapping to exit 1. GitHub reads (diff,
  contents) already happened in stage-1 build; no provider is touched.
- **Footer + summary show per-role tokens.** The walkthrough footer adds
  `pipeline: <p>` and a per-role `role in/out` breakdown for multi-model
  pipelines (single stays clean — its one row would just restate the aggregate).
  The stderr summary adds a pipeline line with judge/ensemble counters and a
  role-ordered token split. `modelLabel` is now role-aware (`+`-joined across
  active roles) so multi-model runs don't render a blank model.

## Offline gates

`golangci-lint` clean; full suite green `-race -shuffle=on`; overall coverage
86.0% (≥85); new pipeline code — judge.go 95/92/100/100, ensemble.go 93–100% —
above the 90 floor. Config matrix (old/new/both, role validation) green. Judge
E2E (keep / kill / fail-open / empty-generator-skips-judge) + verdict-parse and
apply-verdict unit tables green. Ensemble E2E + agreement/cluster tables (±3
boundary, range overlap, same-member-twice, all-disagree, three-way) green.
Cost-guardrail refuse/allow + multiplier table green. Prompt goldens pinned for
generator.md, judge.md, judge_user; footer per-role rendering tested.

## Adversarial review

A 5-dimension multi-agent adversarial pass (judge, ensemble, cost/concurrency,
config/stats, prompt/render/secrets) each finding adversarially verified by a
second agent that tried to refute it: 6 raw findings, **5 confirmed real** (1
refuted), all fixed with regression tests. No data races found (concurrency
dimension confirmed all `rc.Stats`/`rc.Findings` mutation is on the main
goroutine after `wg.Wait()`; ensemble members run sequentially).

1. **[major] `SIEVE_MODEL` env override silently ignored (back-compat
   regression).** `applyEnv` ran after `normalizeProviders` and wrote only the
   orphaned legacy `cfg.Provider`, never the `providers` map the review path
   reads — so the documented `file < SIEVE_* env` precedence was broken. Fixed:
   `applyEnv` now writes `SIEVE_MODEL` into the primary active-role provider in
   the map (and keeps the legacy field coherent). Regression:
   `TestEnvOverrides` now asserts the map; added
   `TestEnvModelOverridesJudgeGenerator`.
2. **[major] `JudgeUser` could strip ALL diff context.** A findings block larger
   than the batch budget drove the diff budget negative, truncating the diff to
   zero hunks (and blowing past `maxBatchTokens`) — the judge lost the code it
   must verify against, then failed open. Fixed: reserve a `minJudgeDiffTokens`
   (= budget/3) floor so the diff always survives; findings are never dropped
   (they are what's being judged). Regression:
   `TestJudgeUserKeepsDiffUnderHugeFindingsBlock`.
3. **[minor] `EnsembleMean` double-counted a chatty member.** The mean was
   per-finding, so a member emitting several near-duplicate findings into one
   cluster skewed the agreement metric upward. Fixed: mean across DISTINCT
   members (each at its best confidence), matching the field's documented
   meaning. Regression: `TestEnsembleMeanIsPerMember`.
4. **[minor] Footer double-counted shared-provider tokens.**
   `roleTokensForRender` iterated `ActiveRoles()` undeduped, so a judge config
   with `generator` and `judge` pointing at one provider printed its token row
   twice (footer disagreed with the stderr summary). Fixed: dedupe by provider
   name. Regression added to `TestRoleTokensForRender`.
5. **[nit] Judge omitting `confidence` zeroed a kept finding.** A well-formed
   verdict that kept a finding but omitted the confidence field decoded as `0.0`
   and overwrote the generator's confidence, so the noise gate then dropped a
   judge-approved finding. Fixed: `verdict.Confidence` is now `*float64` —
   omitted keeps the generator's, explicit `0` is honored. Regression:
   `TestApplyVerdict` omitted/explicit cases + `TestJudgeVerdictOmittedConfidenceE2E`.

Refuted (1): a claimed off-by-one in the judge's per-file index→finding mapping —
the verifier traced the index space and confirmed it is per-file and correct.

## Live validation

Deferred to the batch at the top of this file (`stage-06 live`: three-run
single/judge/ensemble comparison on the seeded sandbox + one live judge `--post`
run; populate the README cost/precision table from the results). Not run
offline.

---

# Stage 7 — Daemon / Self-Host Mode

## Decisions / smallest-reasonable-choice notes

- **`AppTokenSource` drops into the existing `gh.TokenSource`.** The stage-3
  interface was built for exactly this: `internal/gh/appauth` mints a per-repo
  installation token and satisfies `gh.TokenSource`, so the pipeline consumes
  App auth exactly as it consumes a static token. Hand-rolled RS256 JWS
  (stdlib `crypto/rsa` + `crypto/sha256` + `encoding/base64`, no jwt dep);
  installation tokens are cached until `expires_at − 5m` with single-flight
  refresh (no stampede under a webhook burst; `-race` is the witness).
- **Two minimal pipeline injections (stage-3 guard fix, as the prompt
  anticipated).** `review.Options` gained `TokenSource gh.TokenSource` and
  `Config *config.Config`; `Run`/`Build`/`build` use them when set and fall back
  to the CLI's `Token` string + `config.Load(ConfigPath)` otherwise. This let
  the daemon supply App auth and a pre-merged config without touching any review
  logic. The broad-net `TestMutatingMethodsConfinedToPost` guard was widened to
  allow `internal/gh/appauth` (auth POST to mint tokens — not a PR write) and
  `internal/webhook` (references `http.MethodPost` only to gate the *inbound*
  request method; it makes no outbound calls). The precise `.Send` guard —
  the real GitHub-mutation enforcement — is unchanged and still passes.
- **Repo `.sieve.yml` overrides review settings, never spend.**
  `config.MergeRepoReview` decodes only the repo's `review:` block over the
  server config and then **restores the server's spend-governing fields**
  (pipeline, roles, max_run_tokens, concurrency, content attachment) — so an
  untrusted repo can tune what gets flagged but cannot switch pipelines, raise
  the token budget, or pick a provider/key. Any `provider:`/`providers:` in the
  repo file is ignored entirely. A malformed or invalid repo config falls back
  to the server config with a warning (the job is not failed).
- **Webhook: verify before parse, dedupe, never log the body.**
  `X-Hub-Signature-256` is HMAC-SHA256 checked with a constant-time compare
  *before* the body is parsed; a mismatch is a counted 401 and the body is never
  logged. `X-GitHub-Delivery` GUIDs dedupe via an LRU (4096) persisted to
  `deliveries.jsonl` and reloaded on restart. Enqueue failures return 5xx and
  are **not** recorded, so GitHub redelivers rather than the job being lost.
  Unknown events always 200. Body capped at 1 MB.
- **Queue: durable, coalescing, crash-replay.** Append-only `queue.jsonl`
  (enqueue/done/dead records); startup replays the latest un-settled enqueue per
  (repo, PR). At most one review per PR runs at once; a newer push replaces a
  queued job in place, a push during a running review is queued behind it.
  Retries with backoff (default 3 attempts) then dead-letter. Graceful shutdown
  stops intake, drains in-flight (10 m), and leaves un-started jobs for replay.
- **Server config is separate (`server.yml`), strict-decoded.** Startup prints
  one line per check (app id, key file mode 0600/0400, webhook secret present,
  data dir writable, review config valid) then `ready`; any failure aborts.
  Keys stay in the process env via `api_key_env`/`webhook_secret_env`
  indirection — never in the file.
- **No new module deps.** Everything is stdlib + the already-vendored
  doublestar (repo-allow globs) and yaml.v3.

## Offline gates

`golangci-lint` clean; full suite green `-race -shuffle=on`; overall coverage
85.9% (≥85); the stage-7 floor packages — `appauth` 93.1%, `webhook` 90.2%,
`queue` 95.1% — all ≥90. appauth: JWS segment fixture + rsa-verify, expiry math,
single-flight under 24 concurrent callers. webhook: signature accept/reject
(constant-time path), 1 MB body cap, unknown/ping/installation events 200,
dedupe + persistence across restart, enqueue-failure-not-recorded. queue:
coalescing matrix (queued-replace, running-then-queued), crash-replay
(enqueue-without-done → replay → settled), dead-letter after retries,
graceful-shutdown flush + un-started-jobs-survive. E2E: httptest GitHub (App
token + REST) + fake provider — signed webhook in → App token minted → review
posted → walkthrough asserted; repo `.sieve.yml` merge path and the real
`Serve` listener + graceful shutdown exercised.

## Adversarial review

A 5-dimension adversarial pass (App auth, webhook/queue durability, server
config, review integration, ops/docs) surfaced **one critical queue defect** from
the multi-agent review; the follow-up gate run exposed **two additional
implementation defects and two test bugs**, all fixed with regression tests.

1. **[critical] Queue crash-replay dropped a newer head behind a running older
   review.** `replay()` compared `settledIdx > enqueueIdx` per `(repo, PR)` key
   without checking `HeadSHA`. If an older review's `done`/`dead` record was
   written after a newer head was enqueued, the newer job was silently discarded
   on the next boot. Fixed: settle records only settle an enqueue whose
   `HeadSHA` matches. Regression:
   `TestCrashReplayCoalesceOlderHeadSettle`.

2. **[major] Retry backoff could collapse to zero via unsigned→signed wrap.**
   `time.Duration(mrandUint64()) % base` wraps values above `MaxInt64` to
   negative durations, making `time.After` return immediately. Roughly half of
   all jitter draws were negative, so retries could fire in tight loops across
   crashes instead of backing off. Fixed: compute jitter as
   `time.Duration(mrandUint64() % uint64(base))`, keeping it in `[0, base)`.
   Regression: `TestShutdownTimeoutAbortsRetries` (which now reliably aborts a
   long-backoff retry).

3. **[nit] `TestRunWithRetryContextCancel` tested the wrong cancellation
   mechanism.** It cancelled the context passed to `Start`, but `Start` runs
   workers on an independent `runCtx` so SIGTERM cannot abort an in-flight POST.
   The test therefore failed and did not exercise the intended abort-during-
   backoff path. Fixed: renamed to `TestShutdownTimeoutAbortsRetries` and uses
   `Shutdown` with a short timeout, which is the real force-abort path.

4. **[nit] `TestValidateHappyPath` had an off-by-one check.** It asserted
   `len(checks) != 5` while the error message claimed to want 6; once
   `Validate()` returned 6 checks the test failed with a confusing message.
   Fixed: assert `len(checks) != 6`.

No data races found under `-race -shuffle=on`; all Stage-7 packages remain above
their 90% coverage floors.

## Live validation

Deferred to the batch at the top of this file (`stage-07 live`: real GitHub App
+ tunnel, coalescing-in-logs, kill-9 replay, `docs/self-hosting.md` walked
start-to-finish). Not run offline.

---

# Stage 8 — Context Depth (symbols, repo map, blast radius)

## Decisions / smallest-reasonable-choice notes

- **Wazero tree-sitter, cgo-free.** `github.com/malivvan/tree-sitter` runs the
  bundled C/C++ grammars inside `github.com/tetratelabs/wazero`. Pinned wazero
  to `v1.8.2` so Go stays at `1.23.4` (wazero `v1.12.0` wanted Go 1.25). The
  grammar runtime is a thin wrapper (`internal/grammar`) with a per-language
  parser pool because the underlying WASM module is not reentrant across
  concurrent parses.
- **Pluggable extractors in `internal/symbols`.** Go files use stdlib
  `go/parser` + `go/ast`; C/C++ use the grammar-backed extractor; everything else
  falls back to regex heuristics. A registry (`Default`, `DefaultWithGrammar`)
  routes by extension so the same code serves `symbols`, `repomap`, and `blast`
  without the caller touching the parser runtime.
- **Three context depths controlled by config.** `review.context_depth`
  (`symbols` | `repomap` | `blast`) plus `context_max_files`,
  `context_max_tokens`, and `context_langs`. Defaults are `symbols`, 20 files,
  8000 tokens, all languages. The CLI passes the cwd as `RepoPath`; daemon mode
  leaves it empty so `repomap`/`blast` gracefully fall back to symbols-only
  context (no local checkout in the current server design).
- **Repo map is a symbol/import index, not a full call graph.** `repomap.Build`
  walks the repo respecting include/exclude globs and the configured caps, then
  indexes symbols and imports. `blast.Compute` uses only import/symbol edges:
  direct files import symbols defined in the change or define symbols the change
  imports; indirect files import from direct files. This is intentionally coarse
  and fast — good enough to surface cross-file impact without full type
  resolution.
- **Prompt integration is additive.** `prompt.Input` gained `ExtraContext`; the
  existing templates and batching are unchanged. If `ExtraContext` is empty, the
  rendered prompt is identical to Stage 7, so golden tests stay stable.

## Offline gates

`go vet ./...` clean; full suite green `-race -shuffle=on`; overall coverage
86.7% (≥85). New Stage 8 packages all cleared their 90% floors:
`internal/grammar` 90.6%, `internal/symbols` 90.5%, `internal/repomap` 92.2%,
`internal/blast` 92.0%. Config schema changes validated for default values,
repo override, and the new `context_depth` / `context_max_files` /
`context_max_tokens` validation rules.

## Adversarial review

No dedicated multi-agent pass was run for Stage 8 in this offline build; the
usual self-review surfaced and fixed:

- A queue-replay defect (fixed in Stage 7 before this branch diverged).
- A wazero version drift that would have pulled Go 1.25 — pinned.
- Grammar coverage gaps from unreachable WASM error branches; trimmed the public
  wrapper to the surface actually used (`Kind`, `StartByte`, `Text`) and added
  regression tests for pool cap, release lazy-channel creation, and truncated
  source text.

## Live validation

Done in the deferred batch at the top of this file. All three depths ran
against `gnanam1990/sieve-sandbox-stage6-2026-07-06#2` with local Ollama
`qwen3-coder-next:cloud`. Token estimates and reported usage stayed inside the
8,000-token `context_max_tokens` cap; a logging proxy confirmed the `# Repository
context` / `## Repo map` section is inserted into the prompt for `repomap`.
`blast` did not add extra files for this tiny sandbox, so its estimate matched
`symbols` — both fell back to changed-file context only.

---

# Stage 9 — Final Polish / Pre-Launch Hardening

## Decisions / smallest-reasonable-choice notes

- **No new features.** Stage 9 closes the offline Stages 6–9 sequence by
  documenting Stages 7–8, enforcing the Stage 8 coverage floors in the
  Makefile, and removing the calibration TODO marker. The actual
  `min_confidence` / `inline_min_confidence` refinement is left to the live
  batch, where it can be applied without another code change.
- **README gains a "Repository context" section.** It documents
  `review.context_depth` (`symbols` | `repomap` | `blast`) and the related caps,
  and notes the daemon/Action fallback to `symbols` because there is no local
  checkout on the review path.
- **Makefile now gates Stage 8 packages at 90%.** `internal/grammar`,
  `internal/symbols`, `internal/repomap`, and `internal/blast` are added to the
  `make cover` floor list so the Stage 8 coverage work does not regress during
  the live batch.
- **Self-hosting docs note `context_depth: symbols`.** The daemon example config
  explicitly sets `symbols` because `repomap`/`blast` require a repo root that
  the webhook path does not provide; the code falls back silently, but the docs
  make the limitation explicit.
- **Dependency policy updated.** README now lists `malivvan/tree-sitter` and
  `tetratelabs/wazero` alongside the existing deps.

## Offline gates

`go vet ./...` clean; `golangci-lint` clean; `make test` (`-race -shuffle=on`)
green; `make cover` green with all floors: grammar 90.8%, symbols 90.3%,
repomap 92.2%, blast 92.0%; overall 86.7% (≥85).

## Adversarial review

No dedicated multi-agent pass; self-review surfaced the grammar coverage floor
after the Stage 8 lint fix (G115). The coverage regression was fixed by adding
a regression test for `Node.Text` with a node whose start byte is beyond a
truncated source, exercising both the end-clamp and the `start > end`
branches.

## Live validation

Deferred to the batch at the top of this file. Stage 9 has no unique live
steps beyond the general batch; its purpose is to leave `main` in a clean,
documented, regression-guarded state for the live batch.

