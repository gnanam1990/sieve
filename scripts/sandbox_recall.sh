#!/usr/bin/env bash
#
# sandbox_recall.sh — Stage 3 calibration gate (Gate 4).
#
# Creates a PRIVATE sandbox repo UNDER YOUR OWN GitHub account, opens a PR that
# plants ~10 issues (testdata/sandbox/service.go, manifest in plants.md), and
# runs `sieve review --post` against it with a frontier model. Then `fix`
# pushes a fix for two plants and re-runs to exercise the resolved flow.
#
# SAFETY: this only ever touches a repo it creates under the authenticated user
# (never a third party — R1.3). It refuses to --post at any other repo.
#
# Requirements:
#   - gh CLI authenticated (gh auth status)
#   - a frontier model key in the environment:
#       ANTHROPIC_API_KEY  (uses provider.type anthropic), or
#       OPENROUTER_API_KEY (uses provider.type openai-compat against OpenRouter), or
#       KIMI_API_KEY       (uses provider.type openai-compat against Kimi)
#   - run from the repo root: scripts/sandbox_recall.sh <all|create|review|fix>
#
# Usage:
#   scripts/sandbox_recall.sh all      # create repo+PR, then review --post
#   scripts/sandbox_recall.sh fix      # push a 2-plant fix, re-review --post
#
set -euo pipefail

REPO_NAME="${SIEVE_SANDBOX_REPO:-sieve-sandbox-recall}"
BRANCH="planted-issues"
WORKDIR="${SIEVE_SANDBOX_WORKDIR:-$(mktemp -d -t sieve-sandbox)}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

log() { printf '\033[1;34m[sandbox]\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31m[sandbox] error:\033[0m %s\n' "$*" >&2; exit 1; }

require() { command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"; }

owner() { gh api user --jq .login; }

model_config() {
  # Emits a .sieve.yml provider block for the first available key.
  # Ollama is supported for local calibration runs; hosted keys are preferred
  # for final shipping calibration because the numbers inform the README's
  # cost/precision table.
  if [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then
    cat <<YAML
provider:
  type: anthropic
  model: ${SIEVE_MODEL:-claude-sonnet-5}
  api_key_env: ANTHROPIC_API_KEY
  max_tokens: 4096
YAML
  elif [[ -n "${OPENROUTER_API_KEY:-}" ]]; then
    cat <<YAML
provider:
  type: openai-compat
  base_url: https://openrouter.ai/api/v1
  model: ${SIEVE_MODEL:-anthropic/claude-sonnet-5}
  api_key_env: OPENROUTER_API_KEY
  max_tokens: 4096
YAML
  elif [[ -n "${KIMI_API_KEY:-}" ]]; then
    cat <<YAML
provider:
  type: openai-compat
  base_url: https://api.kimi.com/coding/v1
  model: ${SIEVE_MODEL:-kimi-for-coding}
  api_key_env: KIMI_API_KEY
  timeout_seconds: 300
  max_tokens: 4096
YAML
  elif [[ -n "${OLLAMA_API_KEY:-}" ]]; then
    cat <<YAML
provider:
  type: openai-compat
  base_url: http://localhost:11434/v1
  model: ${SIEVE_MODEL:-qwen3-coder:480b-cloud}
  api_key_env: OLLAMA_API_KEY
  timeout_seconds: 300
  max_tokens: 4096
YAML
  else
    die "set ANTHROPIC_API_KEY, OPENROUTER_API_KEY, KIMI_API_KEY, or OLLAMA_API_KEY before running"
  fi
}

build_sieve() {
  log "building sieve binary"
  ( cd "$ROOT" && make build >/dev/null )
}

create_repo_and_pr() {
  require gh; require git
  local o; o="$(owner)"
  log "owner: $o  repo: $REPO_NAME  workdir: $WORKDIR"

  if gh repo view "$o/$REPO_NAME" >/dev/null 2>&1; then
    die "repo $o/$REPO_NAME already exists; delete it (gh repo delete) or set SIEVE_SANDBOX_REPO"
  fi

  log "creating private sandbox repo"
  gh repo create "$o/$REPO_NAME" --private --disable-issues=false >/dev/null

  rm -rf "$WORKDIR" && mkdir -p "$WORKDIR"
  cd "$WORKDIR"
  git init -q -b main
  # Base branch: a clean skeleton so the PR diff is the planted service.go.
  cat > go.mod <<EOF
module github.com/$o/$REPO_NAME

go 1.23
EOF
  cat > README.md <<EOF
# $REPO_NAME

Private sandbox for sieve's Stage 3 recall calibration. Do not use in production.
EOF
  git add -A && git commit -q -m "chore: base skeleton"
  git remote add origin "https://github.com/$o/$REPO_NAME.git"
  git push -q -u origin main

  log "creating PR branch with the planted issues"
  git checkout -q -b "$BRANCH"
  # Plant 6 (hardcoded credential) is a {{PLANT_PASSWORD}} template in the repo,
  # so no credential-looking literal is ever committed to sieve. Substitute a
  # runtime-generated, credential-shaped value here so the model still sees a
  # hardcoded secret in the sandbox PR. The value is alphanumeric+hyphen, so it
  # is safe as a sed replacement.
  plant_pw="Adm1n-$(head -c 16 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-14)"
  sed "s/{{PLANT_PASSWORD}}/${plant_pw}/" "$ROOT/testdata/sandbox/service.go" >service.go
  cp "$ROOT/testdata/sandbox/plants.md" PLANTS.md
  git add -A && git commit -q -m "feat: add service with (planted) issues"
  git push -q -u origin "$BRANCH"

  gh pr create --repo "$o/$REPO_NAME" --base main --head "$BRANCH" \
    --title "Add service layer" \
    --body "Adds the service layer. (Sandbox PR with planted issues for sieve calibration.)" >/dev/null
  local num; num="$(gh pr view "$BRANCH" --repo "$o/$REPO_NAME" --json number --jq .number)"
  echo "$WORKDIR" > "$ROOT/.sandbox_workdir"
  echo "$num"
}

run_review() {
  local o num; o="$(owner)"
  num="$(gh pr view "$BRANCH" --repo "$o/$REPO_NAME" --json number --jq .number)"
  # Guard R1.3: only ever post to the repo we created under our own account.
  [[ "$o/$REPO_NAME" == "$o/${SIEVE_SANDBOX_REPO:-sieve-sandbox-recall}" ]] || die "refusing to post to a non-sandbox repo"

  cd "$WORKDIR"
  model_config > .sieve.yml
  log "running sieve review --post against $o/$REPO_NAME#$num"
  "$ROOT/sieve" review --repo "$o/$REPO_NAME" --pr "$num" --post --debug \
    > "$ROOT/sandbox_review.json" || log "sieve exited non-zero (check exit code semantics)"
  log "review JSON written to sandbox_review.json"
  log "PR: https://github.com/$o/$REPO_NAME/pull/$num"
}

fix_two_plants() {
  require gh; require git
  local o; o="$(owner)"
  [[ -f "$ROOT/.sandbox_workdir" ]] && WORKDIR="$(cat "$ROOT/.sandbox_workdir")"
  [[ -d "$WORKDIR/.git" ]] || die "no sandbox workdir; run 'all' first"
  cd "$WORKDIR"
  git checkout -q "$BRANCH"

  log "fixing plant 5 (swallowed error) and plant 10 (misleading name)"
  # Plant 5: propagate the write error.
  perl -0pi -e 's/func WriteConfig\(path string, data \[\]byte\) \{\n\tos\.WriteFile\(path, data, 0o644\) \/\/nolint\n\}/func WriteConfig(path string, data []byte) error {\n\treturn os.WriteFile(path, data, 0o644)\n}/' service.go
  # Plant 10: rename min -> max.
  perl -0pi -e 's/\/\/ Plant 10 .*\nfunc min\(a, b int\) int \{/\/\/ Corrected: name now matches behavior.\nfunc max(a, b int) int {/' service.go
  perl -0pi -e 's/var _ = min \/\/ keep the misleading helper referenced/var _ = max/' service.go

  git add -A && git commit -q -m "fix: propagate write error and correct min->max"
  git push -q origin "$BRANCH"
  run_review
}

main() {
  case "${1:-all}" in
    all)    build_sieve; create_repo_and_pr >/dev/null; run_review ;;
    create) create_repo_and_pr ;;
    review) build_sieve; run_review ;;
    fix)    build_sieve; fix_two_plants ;;
    *)      die "usage: $0 <all|create|review|fix>" ;;
  esac
}

main "$@"
