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

## Live gates — PENDING (need merge + push + key)

- **Gate 2 (rc release):** after merging the chain to main, tag `v0.0.9-rc1`;
  `release.yml` runs tests then goreleaser, publishes 4 binaries + raw binaries +
  `checksums.txt`, and moves `v0`. Evidence (release URL): _TBD_.
- **Gate 3 (end-to-end action):** a workflow using `gnanam1990/sieve@v0` with
  `post: true` reviews a real PR on a hosted runner. Permalink + step summary:
  _TBD_. (`.github/workflows/action-smoke.yml` provides a `post: false`
  local-checkout smoke via `workflow_dispatch` for a designated PR.)
- **Gate 4 (fork path):** a fork PR into the sandbox exercises the clean-notice
  exit-0 path. The logic is unit-tested offline (`internal/gh` fork detection +
  cmd `TestForkPRSkipsCleanly`); live evidence: _TBD_.
- **Gate 5 (install paths):** `install.sh` and `go install` verified live once a
  release exists (offline: shellcheck-clean, module path correct); Homebrew cask
  generated by goreleaser (tap publish deferred per above).

## Default-tuning / backlog carried forward

Stage 3's calibration (Gate 4 there) is still pending a live model run; the
default-tuning proposal in the Stage 3 notes stands. Stage 4 adds no new
gate-tuning surface.
