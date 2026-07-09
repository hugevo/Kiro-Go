# Kiro-Go

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](https://www.docker.com/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Convert Kiro accounts to OpenAI / Anthropic compatible API service.

[English](README.md) | [中文](README_CN.md)

If this project helps you, a Star would mean a lot.

## Features

- **Compatible API surface** — Anthropic `/v1/messages`, OpenAI `/v1/chat/completions`, and OpenAI `/v1/responses` (with `previous_response_id` chaining), plus `/v1/models`, `/v1/messages/count_tokens`, and `/v1/stats`
- **Multi-account pool** — weighted round-robin selection, model-aware routing, automatic token refresh, and failover (quota / overage / suspension / auth classification) across Kiro IDE, CodeWhisperer, and Amazon Q endpoints
- **SSE streaming** — long streams are never server-killed (`WriteTimeout=0`)
- **Web admin panel** — Accounts, Settings, API docs, and Logs tabs, with privacy mode, batch ops, export, and search; i18n (CN / EN)
- **Multiple auth methods** — AWS Builder ID, IAM Identity Center (Enterprise SSO), hosted Kiro sign-in (Google / GitHub / Microsoft 365 / Entra ID / Azure AD), SSO Token paste, credentials JSON paste
- **Prompt-cache accounting** — Anthropic-style `cache_creation` / `cache_read` token estimation with cross-account sharing, disk persistence, and metrics on `/v1/stats`
- **Prompt sanitization** — Claude Code system-prompt filter, env-noise strip, boundary-marker strip, custom regex filters
- **Thinking mode** — via `-thinking` model suffix or a top-level `thinking` config block
- **Usage tracking & account import/export**
- **Outbound proxy** — SOCKS5 / HTTP, hot-reconfigurable without restart
- **Single static binary** — one external Go dependency (`github.com/google/uuid`), JSON config, one data volume

## Architecture

Kiro-Go is a single-process reverse proxy: a Claude or OpenAI client talks to one local HTTP server, which translates requests into Kiro's internal payload format, dispatches them across a pool of upstream accounts with automatic failover and token refresh, and streams results back.

For full diagrams and design docs see [`docs/`](docs):

- [Project Overview & Requirements](docs/project-overview-pdr.md)
- [Codebase Summary](docs/codebase-summary.md)
- [System Architecture](docs/system-architecture.md)
- [Deployment Guide](docs/deployment-guide.md)
- [Code Standards](docs/code-standards.md) · [Design Guidelines](docs/design-guidelines.md) · [Roadmap](docs/project-roadmap.md)

## Quick Start

### Docker Compose (Recommended)

```bash
git clone https://github.com/Quorinex/Kiro-Go.git
cd Kiro-Go
mkdir -p data
docker-compose up -d
```

### Docker Run

```bash
docker run -d \
  --name kiro-go \
  -p 8080:8080 \
  -e ADMIN_PASSWORD=your_secure_password \
  -v /path/to/data:/app/data \
  --restart unless-stopped \
  ghcr.io/quorinex/kiro-go:latest
```

### Build from Source

```bash
git clone https://github.com/Quorinex/Kiro-Go.git
cd Kiro-Go
go build -o kiro-go .
./kiro-go
```

### Deploy on Zeabur

The repo already includes a `Dockerfile`, so it builds and runs on Zeabur out of the box.

**Option 1: Dashboard (one-click)**

1. Fork this repo to your GitHub account.
2. In Zeabur, create a new service and choose **Deploy from GitHub**, then select your fork.
3. Zeabur auto-detects the `Dockerfile` and builds the image.
4. In the **Networking** tab, expose port `8080` and bind a domain.
5. In the **Variables** tab, set at least `ADMIN_PASSWORD` (admin panel password).
6. Mount a Volume at `/app/data` if you want accounts / config to survive redeploys.

**Option 2: CLI**

```bash
npm i -g zeabur
zeabur auth login
zeabur deploy
```

> Run the commands from the project root. The CLI writes `.zeabur/context.json` to remember the target project / service — it contains personal IDs, so don't commit it.

Once the service is up, open `https://<your-domain>/admin` to log in.

Config is auto-created at `data/config.json`. Mount `/app/data` for persistence. The default admin password is `changeme` — override it via the `ADMIN_PASSWORD` env var or change it in the admin panel before going to production.

## Usage

Open `http://localhost:8080/admin`, log in, add accounts, then call the API:

```bash
# Claude
curl http://localhost:8080/v1/messages \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -d '{"model":"claude-sonnet-4.5","max_tokens":1024,"messages":[{"role":"user","content":"Hello!"}]}'

# OpenAI
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer any" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"Hello!"}]}'
```

## Thinking Mode

Append a suffix (default `-thinking`) to the model name, e.g. `claude-sonnet-4.5-thinking`. Claude-compatible requests that include a top-level `thinking` config such as `{"type":"enabled","budget_tokens":2048}` or `{"type":"adaptive"}` also enable thinking mode automatically. Configure output format in the admin panel under Settings - Thinking Mode.

## Outbound Proxy

For users in restricted network regions, configure an outbound proxy in the admin panel under **Settings - Outbound Proxy Settings**. Supports SOCKS5 and HTTP proxies.

The setting takes effect immediately without restarting.

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `CONFIG_PATH` | Config file path | `data/config.json` |
| `ADMIN_PASSWORD` | Admin panel password (overrides config) | `changeme` |
| `LOG_LEVEL` | Log verbosity: `DEBUG` / `INFO` / `WARN` / `ERROR` (overrides config) | `info` |
| `KIRO_SSO_CALLBACK_BIND` | Bind interface for the Enterprise SSO loopback listener (port `3128`) | loopback |

See the [Deployment Guide](docs/deployment-guide.md) for ports (`8080` API/admin, `3128` Enterprise SSO callback), the `data/` volume, and security hardening.

## Contributing

Friendly discussion is welcome. If you run into issues, try asking Claude Code, Codex, or similar tools for help first — most problems can be solved that way. PRs are even better.

## Contact

Telegram: [@tutua16888](https://t.me/tutua16888)

## Friend Links

- [LINUX DO](https://linux.do)

## Disclaimer

For educational and research purposes only. Not affiliated with Amazon, AWS, or Kiro. Users are responsible for complying with applicable terms of service and laws. Use at your own risk.

## License

[MIT](LICENSE)
