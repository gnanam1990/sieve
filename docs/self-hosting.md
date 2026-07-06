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
dead-letter count) is handy to expose to your own monitoring, but nothing else
is served.

---

## 5. Verify

- GitHub App → **Advanced → Recent Deliveries** shows a green `ping` after setup.
- Open or push a PR on an installed repo → within seconds the walkthrough appears.
- `curl -s https://sieve.example.com/healthz | jq` shows the queue draining.

---

## Operations

- **Durability / crash recovery.** `data_dir/queue.jsonl` is an append-only log;
  on restart the daemon replays any review that was enqueued but not finished.
  Redeliveries are deduped via `data_dir/deliveries.jsonl`. A `kill -9` mid-review
  loses only the in-flight run, which replays on the next start.
- **Coalescing.** A new push for a PR already queued replaces the older job (only
  the newest head is reviewed); a push while a review is running is queued behind
  it. At most one review runs per PR at a time.
- **Backups.** `data_dir` is a cache + queue, not a source of truth — GitHub is.
  You can back it up, but after a total loss the queue simply drains what's
  pending; historical outcomes can be rebuilt per-PR with `sieve sync`.
- **Graceful shutdown.** `systemctl stop sieve` (SIGTERM) stops accepting new
  webhooks, lets in-flight reviews finish (up to 10 min), and flushes queue
  state. Un-started jobs replay on the next start.
- **Capacity.** The workload is I/O-wait (network round-trips to GitHub and the
  model), so one small VPS handles many repos on `workers: 2–4`. Concrete
  throughput/latency numbers are _measured at the live-validation batch_.

## Non-goals

No in-process TLS (that's the proxy's job), no multi-tenancy/billing, no web UI,
no Docker image, no horizontal scaling — a single node is the design point. If
you outgrow one box, run one daemon per org.
