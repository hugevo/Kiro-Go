# Kiro-Go — Deployment Guide

> **Audience:** Operators deploying Kiro-Go for themselves or a small team.
> **Companion docs:** [Project Overview & PDR](project-overview-pdr.md), [System Architecture](system-architecture.md), [Codebase Summary](codebase-summary.md).
> **Repo:** https://github.com/Quorinex/Kiro-Go  |  **Image:** `ghcr.io/quorinex/kiro-go`

Kiro-Go is a single static binary serving one HTTP server. The fastest path to a running instance is Docker Compose; the data lives in one directory.

## 1. Quick Decision

| If you... | Use |
|-----------|-----|
| want the supported, reproducible path | [Docker Compose](#2-docker-compose-recommended) |
| already have a compose-free Docker workflow | [Docker run](#3-docker-run) |
| need a custom build or no Docker | [Build from source](#4-build-from-source) |
| want a managed PaaS | Zeabur (see [README.md](../README.md)) |

## 2. Docker Compose (Recommended)

The repo ships a `docker-compose.yml`. It publishes the API/admin port, pins the SSO callback to loopback, mounts the data volume, sets the config path, and restarts unless stopped.

```bash
git clone https://github.com/Quorinex/Kiro-Go.git
cd Kiro-Go
mkdir -p data
docker-compose up -d
```

What the compose file does (see `docker-compose.yml`):

- `ports: 8080:8080` — API + admin panel.
- `ports: 127.0.0.1:3128:3128` — Enterprise SSO (Microsoft 365) loopback callback, published to the host's loopback only.
- `volumes: ./data:/app/data` — config, prompt-cache, responses, stats.
- `environment: CONFIG_PATH=/app/data/config.json`.
- `environment: KIRO_SSO_CALLBACK_BIND=0.0.0.0` — required so the container-side SSO listener can receive the host-forwarded callback (see [§7 Security](#7-security-hardening)).
- `restart: unless-stopped`.

Then open `http://<host>:8080/admin` and **change the default password before adding accounts** (see [§7](#7-security-hardening)).

## 3. Docker Run

```bash
docker run -d \
  --name kiro-go \
  -p 8080:8080 \
  -e ADMIN_PASSWORD=your_secure_password \
  -v /path/to/data:/app/data \
  --restart unless-stopped \
  ghcr.io/quorinex/kiro-go:latest
```

Notes:

- Add `-p 127.0.0.1:3128:3128` and `-e KIRO_SSO_CALLBACK_BIND=0.0.0.0` if you will use Enterprise SSO (Microsoft 365) sign-in from the host browser.
- Pin to a specific version (`ghcr.io/quorinex/kiro-go:1.1.2`) for reproducibility; `latest` tracks the default branch.

## 4. Build from Source

Requirements: Go 1.21+ (the module declares `go 1.21`; the Docker builder uses 1.23).

```bash
git clone https://github.com/Quorinex/Kiro-Go.git
cd Kiro-Go
go build -o kiro-go .
./kiro-go
```

The binary serves from the working directory: config defaults to `data/config.json` (create `data/` or set `CONFIG_PATH`). Useful first run:

```bash
mkdir -p data
CONFIG_PATH="$PWD/data/config.json" ADMIN_PASSWORD=your_secure_password ./kiro-go
```

## 5. Environment Variables

| Variable | Purpose | Default | Notes |
|----------|---------|---------|-------|
| `CONFIG_PATH` | Config file path | `data/config.json` | Directory is created at startup if missing (`main.go`). |
| `ADMIN_PASSWORD` | Admin panel password | `changeme` (in config) | Overrides the config password at startup. **Set this in production.** |
| `LOG_LEVEL` | Log verbosity | config value (default `info`) | One of `DEBUG`/`INFO`/`WARN`/`ERROR`. Overrides config live. |
| `KIRO_SSO_CALLBACK_BIND` | Bind interface for the Enterprise SSO loopback listener (port 3128) | loopback (`127.0.0.1`/`::1`) | In Docker, set `0.0.0.0` so the published port can reach the listener. See [§7](#7-security-hardening). |

## 6. Port & Volume Reference

### 6.1 Ports

| Port | Purpose | Expose guidance |
|------|---------|-----------------|
| `8080` | Public API + admin panel + health | The only port general clients need. Put it behind your TLS terminator / reverse proxy in production. |
| `3128` | Enterprise SSO (Microsoft 365) loopback callback | Transient (~10-minute login window). The hosted sign-in redirects the browser to `localhost:3128`, so the operator's browser must reach it. Publish host-side to `127.0.0.1` only. |

`GET /health` and `GET /` are liveness/landing checks on the main port.

### 6.2 Volume: `/app/data` (`./data` from source)

Everything stateful lives here. Back it up to back up accounts/config.

| Path | Contents |
|------|----------|
| `data/config.json` | Server settings, accounts (credentials + usage + overage + subscription + trial + ban state), API keys, prompt filters, cache tuning, persisted global stats. Created with defaults on first run. |
| `data/prompt_cache.json` | Prompt-cache accounting entries (SHA-256 fingerprints + TTLs). Debounced 30s writes; loaded on startup. Accounting-only (not response cache). |
| `data/responses/` | OpenAI Responses API state for `previous_response_id` chaining. Atomic tmp + rename writes, 30-day TTL, owner-key-ID scoped. |
| stats snapshot | Runtime stats persisted by the background saver goroutine. |

## 7. Security Hardening

Read this before exposing the service beyond loopback.

### 7.1 Change the admin password

The default admin password is `changeme`. Set it via `ADMIN_PASSWORD` env (applied at startup) or in the admin panel (**Settings**) before any production exposure. The admin gate (`proxy/handler.go` `handleAdminAPI`) accepts the `X-Admin-Password` header or `admin_password` cookie and compares with a constant-time compare.

### 7.2 Decide on API-key mode

`RequireApiKey` defaults to `false` (single-key/legacy fallback). If you enable multi-key mode, the gate is **fail-closed**: requests without a valid, under-quota key are rejected (`proxy/auth.go` `RequireApiKey`). Generate keys in **Settings -> API Keys**; the cleartext key is shown once on creation and thereafter masked.

### 7.3 Put TLS in front

Kiro-Go serves plain HTTP. Terminate TLS with a reverse proxy (Caddy, nginx, Traefik) and expose only the reverse proxy's port. This also lets you bind `0.0.0.0:8080` to loopback only.

### 7.4 Enterprise SSO callback exposure (port 3128)

The hosted Kiro sign-in redirects the browser to `localhost:3128`. The compose file publishes this as `127.0.0.1:3128:3128` so it is **not** reachable on the host's external interfaces, and sets `KIRO_SSO_CALLBACK_BIND=0.0.0.0` inside the container so the forwarded connection can land. The listener is transient (~10-minute login window) and anti-CSRF state-matched.

Caveats:

- `0.0.0.0` does expose 3128 to peer containers on the same Docker network during the login window. On a shared or untrusted Docker network, pin `KIRO_SSO_CALLBACK_BIND` to a specific interface or isolate the network.
- On a remote server where the browser can't reach loopback, use the admin panel's **FeedCallbackURL** affordance to paste the callback URL manually (the browser performs the sign-in; you copy the resulting URL back).

### 7.5 Outbound proxy

If you route upstream traffic through an HTTP/SOCKS5 proxy (admin panel -> **Settings -> Outbound Proxy**), the change takes effect immediately without a restart (`auth/http_client.go` hot-swappable client). Ensure the proxy itself is trusted; all upstream calls (including token refresh) go through it.

### 7.6 External IdP / SSRF posture

External IdP endpoints (Microsoft 365 / Entra / Azure AD) are validated by `auth/kiro_sso.go` `validateExternalIdpEndpoint`: must be `https`, non-IP host, and host suffix in `allowedExternalIdpIssuerSuffixes` (`.microsoftonline.com`, `.microsoftonline.us`, `.microsoftonline.cn`). Do not weaken this guard.

### 7.7 File permissions

`data/config.json` contains credentials and tokens. Restrict its permissions (`chmod 600`) and don't commit it. The repo's `.gitignore` excludes `data/`.

## 8. CI/CD

The GitHub Actions workflow `.github/workflows/docker.yml` ("Build Docker Image"):

- Triggers on push to `main`/`master`/`dev`, tags matching `v*`, PRs to those branches, and `workflow_dispatch`.
- Builds multi-arch images (`linux/amd64`, `linux/arm64`) using QEMU + Buildx.
- Pushes to GHCR at `ghcr.io/quorinex/kiro-go` (lower-cased). Tags applied: `latest` (default branch only), branch ref, semver (`{{version}}`, `{{major}}.{{minor}}`), and SHA.
- Uses GitHub Actions cache (`cache-from/to: type=gha`). PRs build but do not push.

> CI currently builds and publishes images only; it does not run `go test`. Run tests locally before tagging a release (see [Code Standards §6](code-standards.md#6-testing-conventions)).

## 9. Upgrade Notes

- **Backup `data/` first.** A version bump can run config migrations (see below).
- **Config migrations are automatic** (`config/config.go`): legacy single `ApiKey` is promoted to the first entry in `ApiKeys`; per-account legacy `allowOverage` becomes `OverageStatus="ENABLED"`. These run on load.
- **Pull the new image and recreate:** `docker-compose pull && docker-compose up -d`. The `./data` volume carries accounts, keys, cache, and responses across the recreate.
- **Prompt-cache file format** is versioned; expired entries are dropped on load, so an upgrade that changes the format degrades gracefully to a fresh start (no crash).
- **Versioning:** tags `v*` produce semver image tags; pin to a specific semver tag for reproducibility and read the release notes before moving `latest`.

## 10. Smoke Test

After deploy, sanity-check the three layers:

```bash
# 1. health
curl -s http://localhost:8080/health

# 2. stats (needs a valid api key if multi-key mode is on)
curl -s http://localhost:8080/v1/stats -H 'x-api-key: <key>'

# 3. a real completion (Claude format)
curl http://localhost:8080/v1/messages \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -d '{"model":"claude-sonnet-4.5","max_tokens":1024,"messages":[{"role":"user","content":"Hello!"}]}'
```

If `/v1/stats` shows non-zero `failedRequests` or the cache object shows `evictions` climbing under modest load, check the admin **Logs** tab and the per-account state in **Accounts**.

## 11. Troubleshooting Cheat Sheet

| Symptom | First check |
|---------|-------------|
| Admin panel won't load | Is `8080` published? Is a reverse proxy forwarding correctly? |
| `401` on every API call | Multi-key mode is on and the key is missing/over-quota (`proxy/auth.go`). |
| All accounts disabled | Check **Accounts** for cooldown/quota/suspension state; the failover loop caps at 3 retries. |
| SSO login hangs | The browser can't reach `localhost:3128`; use **FeedCallbackURL**, or fix the port publish. |
| Streams cut off early | A reverse proxy in front is buffering or timing out SSE; disable response buffering / raise its timeout. (Kiro-Go itself sets `WriteTimeout=0`.) |
| Large prompts fail upstream | The 900 KB payload cap dropped history but the prompt is still too big; reduce input. |
| Lost state after recreate | The `./data` volume isn't mounted. |
