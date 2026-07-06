# Fork PRs and the sieve Action

**Short version:** sieve reviews **same-repo pull requests**. Pull requests
opened **from a fork** are detected and skipped with a clean notice (exit 0),
because GitHub does not give fork PRs access to the secrets sieve needs.

## Why fork PRs are skipped

When a pull request comes from a fork, GitHub runs the workflow with a
**read-only `GITHUB_TOKEN`** and **withholds repository secrets** — including
the model API key you store as `SIEVE_API_KEY`. This is a deliberate GitHub
security measure: a fork author must not be able to exfiltrate your secrets or
push to your repo by editing the PR's code.

So on a fork PR, sieve has:

- no API key → it cannot call the model, and
- a read-only token → it could not post a review even if it wanted to.

Rather than fail with a confusing "missing key" error, sieve detects the fork
condition from the event payload
(`pull_request.head.repo.full_name != <this repo>`), prints a notice, writes it
to the job's step summary, and **exits 0** so your CI stays green.

```
notice: fork PR (forker/app → you/app): secrets are unavailable to this
workflow; skipping review. Same-repo PRs are the supported surface.
```

## What is supported (v0.1)

- **Same-repo PRs** (branches pushed to your own repository) — fully supported.
- Fork PRs — detected and skipped, not reviewed.

## What is explicitly NOT supported: `pull_request_target`

There is a well-known pattern for giving fork PRs access to secrets:
triggering on **`pull_request_target`** instead of `pull_request`.
`pull_request_target` runs in the **context of the base repo**, with secrets and
a writable token available.

**Do not do this with a checkout of the PR head.** The combination

```yaml
# DANGEROUS — do not copy this.
on: pull_request_target
jobs:
  review:
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ github.event.pull_request.head.sha }}   # attacker-controlled code
      - uses: gnanam1990/sieve@v0                            # now runs with YOUR secrets
```

runs **untrusted fork code with your secrets in scope** — a remote code
execution vector that has burned many tools in this category. sieve does not
read or execute PR source code (it only sends the diff to the model), but any
*other* step in such a workflow could, and the secret is exposed to the whole
job. **`pull_request_target` + head checkout is unsupported and unsafe.** If you
ever need to review fork PRs, do it via a manually-triggered
(`workflow_dispatch`) run by a maintainer, never automatically.

## Missing key on a same-repo PR

If a *same-repo* run still can't find the API key (self-hosted runner,
mistyped secret name, secret not set), sieve fails with a message that **names
the expected environment variable** (never its value) and writes a fix hint to
the step summary:

```
error: environment variable SIEVE_API_KEY (provider.api_key_env) is unset or empty
```

Fix: set the secret named by the action's `api_key_env_name` input (default
`SIEVE_API_KEY`) in your workflow `env:` from a repository secret:

```yaml
env:
  SIEVE_API_KEY: ${{ secrets.SIEVE_API_KEY }}
```
