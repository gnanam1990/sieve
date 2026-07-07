# Self-hosting sieve (daemon mode)

Run **one binary on one small VPS**, point a GitHub App's webhooks at it, bring
your own model keys. No servers to babysit, no containers, no database — the
daemon is the same `sieve` binary you already have, invoked as `sieve serve`.

The review pipeline is identical to the CLI/Action; the daemon only adds a front
door (webhooks) and a token source (GitHub App auth). TLS terminates at a
reverse proxy — the daemon speaks plain HTTP on localhost.

```
GitHub ──webhook──▶ reverse proxy (TLS) ──▶ 127.0.0.1:8787  sieve serve
                                                             │
                                        App JWT ─▶ installation token ─▶ review ─▶ PR
```

---

## 1. Create the GitHub App

GitHub → **Settings → Developer settings → GitHub Apps → New GitHub App**.

- **Homepage URL**: anything (e.g. your repo).
- **Webhook URL**: `https://sieve.example.com/webhook` (your proxy).
- **Webhook secret**: generate a strong random string; you'll set it in the
  daemon's env as `SIEVE_WEBHOOK_SECRET`. Example: `openssl rand -hex 32`.
- **Permissions → Repository**:
  - **Pull requests**: **Read & write** (post the review).
  - **Contents**: **Read-only** (fetch the diff + optional `.sieve.yml`).
  - **Metadata**: **Read-only** (mandatory baseline).
- **Subscribe to events**: **Pull request**.
- **Where can this app be installed?** Only on this account (typical) or Any.

Create it, then:

1. Note the **App ID** (top of the app's settings page).
2. **Generate a private key** — downloads a `.pem`. Store it on the VPS as
   `/etc/sieve/app.pem` with mode `0600` (the daemon refuses to start otherwise):
   ```sh
   sudo install -m 600 ~/Downloads/your-app.*.private-key.pem /etc/sieve/app.pem
   ```
3. **Install the App** on the repos/org you want reviewed.

---

## 2. Configure the daemon

`/etc/sieve/server.yml`:

```yaml
listen: 127.0.0.1:8787            # TLS terminates at the reverse proxy
app:
  id: 123456                      # your App ID
  private_key_path: /etc/sieve/app.pem
webhook_secret_env: SIEVE_WEBHOOK_SECRET  # env var holding the webhook secret
admin_secret_env: SIEVE_ADMIN_SECRET      # optional: enables /admin endpoint
data_dir: /var/lib/sieve          # queue + delivery + outcome stores
repos_allow: ["your-org/*"]       # optional glob allowlist; omit = all installed repos
workers: 2

# Same schema as .sieve.yml. Providers are SERVER-OWNED — a repo's .sieve.yml
# may override review *settings* (min_confidence, excludes, …) but never the
# provider, model, or keys. Untrusted repos must not choose your spend.
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
    generator: fast
    judge: strong
  max_run_tokens: 300000          # refuse a surprise-huge PR before spending
  context_depth: symbols          # daemon has no local checkout; repomap/blast fall back to symbols
```

Keys are read from the process environment named by `api_key_env` /
`webhook_secret_env` — **never from the config file**. Put them in a root-only
env file (next step).

---

## 3. systemd unit

`/etc/sieve/sieve.env` (mode `0600`, root-owned — holds the secrets):

```sh
SIEVE_WEBHOOK_SECRET=<the webhook secret from step 1>
ANTHROPIC_API_KEY=sk-ant-...
OPENROUTER_API_KEY=sk-or-...
```

`/etc/systemd/system/sieve.service`:

```ini
[Unit]
Description=sieve PR reviewer daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/sieve serve --config /etc/sieve/server.yml
EnvironmentFile=/etc/sieve/sieve.env
DynamicUser=yes
StateDirectory=sieve
# data_dir must match StateDirectory's path:
Environment=HOME=/var/lib/sieve
Restart=on-failure
RestartSec=5s

# Hardening
ProtectSystem=strict
ReadWritePaths=/var/lib/sieve
ProtectHome=yes
PrivateTmp=yes
NoNewPrivileges=yes
ProtectKernelTuning=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6
# app.pem + env file are root-only; the service reads them at start:
LoadCredential=app.pem:/etc/sieve/app.pem

[Install]
WantedBy=multi-user.target
```

> With `DynamicUser=yes` the service runs as an ephemeral user; keep
> `/etc/sieve/app.pem` and `sieve.env` root-readable and use `LoadCredential`
> (or drop `DynamicUser` and run as a dedicated `sieve` user that owns the
> files). Whichever you pick, the key file must stay mode `0600`/`0400`.

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now sieve
sudo systemctl status sieve      # should show the startup checks then "ready"
```

On start the daemon prints one line per validation, then `ready`:

```
[ok] app.id set
[ok] app private key present and mode 0600/0400
[ok] webhook secret present in SIEVE_WEBHOOK_SECRET
[ok] data dir writable (/var/lib/sieve)
[ok] review config valid
ready
```

Any `[FAIL]` line aborts startup with the reason — fix it and restart.

---

## 4. Reverse proxy (TLS)

**Caddy** (`/etc/caddy/Caddyfile`) — automatic HTTPS:

```
sieve.example.com {
    reverse_proxy 127.0.0.1:8787
}
```

**nginx**:

```nginx
server {
    listen 443 ssl;
    server_name sieve.example.com;
    ssl_certificate     /etc/letsencrypt/live/sieve.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/sieve.example.com/privkey.pem;

    location /webhook {
        proxy_pass http://127.0.0.1:8787/webhook;
        proxy_set_header Host $host;
        client_max_body_size 1m;   # payloads are capped at 1 MB
    }
}
```

Only `/webhook` needs to be public. `/healthz` (version, queue depth,
dead-letter count) is handy to expose to your own monitoring. `/admin` is
available only when `admin_secret_env` is set; it returns the same stats plus
the running job keys and the last 100 dead-lettered jobs under HTTP basic auth
(`admin:<secret>`). Do not expose `/admin` to the internet without restricting
it to your own IP range or VPN.

---

## 5. Verify

- GitHub App → **Advanced → Recent Deliveries** shows a green `ping` after setup.
- Open or push a PR on an installed repo → within seconds the walkthrough appears.
- `curl -s https://sieve.example.com/healthz | jq` shows the queue draining.

---

## Quick install on a fresh Linux server

Use the server installer to put the signed binary on `PATH` and then drop in
the systemd unit above:

```sh
curl -fsSL https://sievereview.dev/install-server.sh | bash -s -- -d /usr/local/bin
sudo mkdir -p /etc/sieve /var/lib/sieve
# copy your GitHub App private key and /etc/sieve/server.yml, then:
sudo systemctl enable --now sieve
```

If `cosign` is installed, the installer verifies the release signature against
GitHub Actions OIDC. Use `-s` to skip verification, `-v v0.2.0` to pin a version.

## Fly.io deployment

A ready-to-use `fly.toml` is in the repo root. It builds the Go binary via the
included `Dockerfile`, exposes `sieve serve`, and wires Fly's native Prometheus
scraper to `/metrics`.

```sh
# once
fly apps create sieve-serve
# deploy
fly deploy --dockerfile Dockerfile
# set secrets
fly secrets set SIEVE_WEBHOOK_SECRET=... ANTHROPIC_API_KEY=... OPENROUTER_API_KEY=...
# upload your GitHub App private key as a secret file
fly secrets set SIEVE_APP_PRIVATE_KEY="$(cat /path/to/app.pem)"
```

Then point your GitHub App webhook at `https://sieve-serve.fly.dev/webhook`.

## Observability

The daemon exposes these endpoints on the configured `listen` port:

- `/webhook` — GitHub App webhooks (public via proxy).
- `/healthz` — liveness: version, queue depth, dead-letter count.
- `/admin` — runtime details: version, uptime, queue depth, running jobs, last 100
  dead-lettered jobs. Requires HTTP basic auth (`admin:<secret>`) and is enabled
  only when `admin_secret_env` is set.
- `/metrics` — Prometheus text exposition of:
  - `sieve_reviews_total{outcome,pipeline}`
  - `sieve_review_duration_seconds_bucket/p50/p95/p99`
  - `sieve_tokens_total{role,direction}`
  - `sieve_queue_depth`
  - `sieve_dead_letters`
  - `sieve_workers`

When running on Fly.io, the `[[metrics]]` block in `fly.toml` tells Fly's
scraper to pull `/metrics`; otherwise point your Prometheus to
`http://<host>:<port>/metrics`. All metrics use dependency-free Prometheus text
format — no client library is required.

## Operations

- **Durability / crash recovery.** `data_dir/queue.jsonl` is an append-only log;
  on restart the daemon replays any review that was enqueued but not finished.
  Redeliveries are deduped via `data_dir/deliveries.jsonl`.
  `data_dir/dead.jsonl` persists the last 1000 dead-lettered jobs (with error,
  attempts, and timestamp) for post-mortems. A `kill -9` mid-review loses only
  the in-flight run, which replays on the next start.
- **Coalescing.** A new push for a PR already queued replaces the older job (only
  the newest head is reviewed); a push while a review is running is queued behind
  it. At most one review runs per PR at a time.
- **Backups.** `data_dir` is a cache + queue, not a source of truth — GitHub is.
  You can back it up, but after a total loss the queue simply drains what's
  pending; historical outcomes can be rebuilt per-PR with `sieve sync`.
- **Graceful shutdown.** `systemctl stop sieve` (SIGTERM) stops accepting new
  webhooks, lets in-flight reviews finish (up to 10 min), and flushes queue
  state. Un-started jobs replay on the next start.
- **Capacity (measured 2026-07-06 on a local macOS dev machine, `workers: 2`,
  model served by Ollama).** The workload is I/O-wait (network round-trips to
  GitHub and the model), so a small VPS is the right shape. Observed numbers:
  - Daemon startup validation: <1 s from process start to `ready`.
  - End-to-end webhook → posted review: ~28 s for a small PR with the `judge`
    pipeline (generator + judge) via Ollama.
  - Crash replay: a `kill -9` mid-queue, immediate restart, and the pending job
    finished in ~9 s (only the newest head was replayed; older coalesced heads
    were dropped).
  - Queue coalescing: two `synchronize` webhooks delivered within 50 ms produced
    one pending job behind the in-flight run; `queue_depth` never exceeded 1.
  - A single node with `workers: 2–4` is expected to handle many repos; the
    bottleneck is model latency, not CPU.

## Non-goals

No in-process TLS (that's the proxy's job), no multi-tenancy/billing, no web UI,
no horizontal scaling — a single node is the design point. If you outgrow one
box, run one daemon per org.
