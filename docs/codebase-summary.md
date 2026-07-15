# Kiro-Go — Codebase Summary

> **Audience:** New contributors and reviewers who need a fast, accurate map of the codebase.
> **Companion docs:** [Project Overview & PDR](project-overview-pdr.md) (the "what/why"), [System Architecture](system-architecture.md) (the "how it flows"), [Code Standards](code-standards.md) (the "how to write").

## 1. What It Is

Kiro-Go is a **reverse proxy written in Go** that converts Kiro / AWS Q Developer / CodeWhisperer accounts into **OpenAI- and Anthropic (Claude)-compatible API endpoints**. You point a Claude or OpenAI client at a local HTTP server; requests are transparently translated and served by a pool of upstream Kiro accounts, with automatic failover, token refresh, and prompt-cache accounting.

- **Module:** `kiro-go`, declared `go 1.21` (`go.mod`). The Docker builder uses `golang:1.23-alpine` with `CGO_ENABLED=0` cross-compile.
- **Version:** 1.1.2 (`version.json`).
- **External dependencies:** exactly one — `github.com/google/uuid v1.6.0` (`go.mod`, `go.sum`). Everything else is the standard library.
- **Repo:** https://github.com/Quorinex/Kiro-Go
- **Entry point:** `main.go` (`func main`), which wires config -> logger -> admin-password override -> account pool -> `proxy.NewHandler()` -> a single `http.Server` on `Host:Port` (default `0.0.0.0:8080`).

## 2. Size & Shape

| Metric | Value | Source |
|--------|-------|--------|
| Tracked files | 90 | `git ls-files \| wc -l` |
| Go source files (non-test + test) | 55 | `git ls-files '*.go' \| wc -l` |
| Go LOC — `proxy/` | ~15,500 | `wc -l proxy/*.go` |
| Go LOC — `auth/` | ~2,460 | `wc -l auth/*.go` |
| Go LOC — `config/` | ~1,810 | `wc -l config/*.go` |
| Go LOC — `pool/` | ~840 | `wc -l pool/*.go` |
| `main.go` | 81 lines | `wc -l main.go` |
| Web assets | 16 files, ~14,350 LOC | `find web -type f` |

> Note on totals: the scout brief's headline figure of "~20,844 LOC Go, ~12,353 LOC web" is the right order of magnitude; the per-package counts above were measured with `wc -l` against tracked `.go` and `web/` files at the time of writing and include blank/comment lines. The `web/` total includes vendored Tailwind (274 KB) and FontAwesome, so the *authored* web LOC is much smaller (the bulk is `web/app.js` ~169 KB and `web/index-legacy.html` ~176 KB).

## 3. Tech Stack

| Layer | Choice |
|-------|--------|
| Language | Go 1.21 (stdlib-first; one external module) |
| HTTP server | `net/http` single `http.Server`, `WriteTimeout=0` for SSE |
| Upstream protocol | AWS event-stream binary parsing (`proxy/kiro.go` `parseEventStream`) |
| Storage | JSON files under `data/` (no database) |
| Admin UI | Vanilla-JS SPA statically served (`web/`), Tailwind + FontAwesome vendored, CN/EN i18n |
| Packaging | Multi-stage Dockerfile; multi-arch images via GHA (`linux/amd64`, `linux/arm64`) to GHCR |
| Config | Single JSON file, `sync.RWMutex`-guarded, ~80 getters/updaters |

## 4. Package Map

| Package | Responsibility | Key files | Approx. LOC |
|---------|----------------|-----------|-------------|
| `main` | Bootstrap: resolve config path, init config/logger, apply `ADMIN_PASSWORD`, start pool + handler + server. Documented timeouts. | `main.go` | 81 |
| `proxy/` (core, 16 non-test files) | HTTP routing, all endpoint handlers, runtime stats, prompt-cache tracker, models cache, translator, upstream clients, failover, OpenAI Responses, admin api-key handlers. | `handler.go` (~129 KB), `translator.go` (~66 KB), `cache_tracker.go` (~24 KB), `kiro.go` (~25 KB), `kiro_api.go` (~26 KB), `account_failover.go`, `auth.go`, `responses_handler.go` + `responses_*.go`, `admin_apikeys.go`, `token_estimator.go`, `kiro_headers.go`, `kiro_overage.go` | ~15,500 |
| `pool/` | Process-wide `AccountPool` singleton: weighted round-robin selection, model-aware routing, success/error recording, overage/quota/expiry state. Does NOT own credential storage (reads `config.Account` snapshots). | `account.go` | ~840 |
| `config/` | Central JSON-backed store, single config file, ~80 getters/updaters, legacy migrations. Multi-API-key management. | `config.go` (~40 KB), `apikeys.go` | ~1,810 |
| `auth/` | All authentication/credential flows (Start/Poll pattern), global hot-swappable HTTP client with per-proxy cache, token refresh dispatcher, SSRF-hardened external IdP validation. | `builderid.go`, `iam_sso.go`, `kiro_sso.go` (~906 lines), `oidc.go`, `sso_token.go`, `http_client.go` | ~2,460 |
| `logger/` | Lightweight leveled logger (DEBUG<INFO<WARN<ERROR) on stdlib `log`; `LOG_LEVEL` env overrides config. | `logger.go` | small |
| `web/` (not Go) | Admin panel SPA + legacy fallback, styles, toasts, i18n locales, vendored Tailwind/FontAwesome, favicon/icon. | `index.html`, `index-legacy.html`, `app.js`, `styles.css`, `toast.js/css`, `locales/{en,zh}.json`, `vendor/...` | ~14,350 |

## 5. Key Data Flows

### 5.1 Startup (`main.go`)

```
CONFIG_PATH (or data/config.json)
   -> os.MkdirAll(data dir)
   -> config.Init(path)          // reads or creates defaults
   -> logger.Init(config.GetLogLevel())   // LOG_LEVEL env wins
   -> config.SetPassword(ADMIN_PASSWORD)  // env override if set
   -> pool.GetPool()             // process-wide singleton
   -> proxy.NewHandler()         // wires pool + background goroutines
   -> http.Server{Addr: Host:Port, WriteTimeout: 0, ...}
   -> srv.ListenAndServe()
```

`proxy.NewHandler()` also: applies the outbound proxy, restores persisted stats, loads the prompt-cache (`data/prompt_cache.json`), and starts the `backgroundRefresh`, `backgroundStatsSaver`, and expired-response purge goroutines.

### 5.2 A public API request (`proxy/handler.go`)

```
client request
  -> ServeHTTP (handler.go:362)   // single router, CORS + OPTIONS handling
  -> (public API) RequireApiKey (proxy/auth.go:55)   // fail-closed multi-key check
  -> endpoint handler, e.g. handleClaudeMessages
  -> translator.ClaudeToKiro / OpenAIToKiro        // build KiroPayload + prompt hygiene
  -> ensureValidToken (handler.go:2139)            // refresh if within tokenRefreshSkewSeconds=120
  -> pool.GetNextForModel / GetNextExcluding       // weighted RR + exclusions
  -> kiro.CallKiroAPI (kiro.go:292)                // ordered endpoint fallback, regionalized URLs
       -> parseEventStream (kiro.go:417)           // AWS event-stream binary -> text/tool/reasoning/usage
  -> account_failover classification               // quota/429, overage/402, suspension, auth -> react
  -> translator.KiroToClaudeResponse / KiroToOpenAIResponse   // back to client format
  -> promptCache.Update (on success path)          // record fingerprints
```

Upstream endpoints tried in order (`kiroEndpoints`, `kiro.go:32`): Kiro IDE (`q.us-east-1.amazonaws.com/generateAssistantResponse`), CodeWhisperer, Amazon Q.

### 5.3 Admin request (`proxy/handler.go`)

```
/admin/api/* request
  -> ServeHTTP -> handleAdminAPI (handler.go:2183)
  -> X-Admin-Password header OR admin_password cookie
  -> constant-time compare vs config password
  -> ~50 nested sub-routes (accounts CRUD/batch/refresh/test/models, auth flows, settings,
       stats, stats/reset, logs, generate-machine-id, thinking, endpoint, proxy,
       prompt-filter, version, export, api-keys CRUD + reset-usage)
```

### 5.4 Prompt-cache accounting (`proxy/cache_tracker.go`)

The cache tracker is **accounting-only**: it estimates Anthropic-style `cache_creation_input_tokens` / `cache_read_input_tokens` from SHA-256-fingerprinted prompt prefixes. It does NOT cache responses.

```
Compute(accountID, profile)   // global store (C1: cross-account sharing); accountID ignored
  -> for each breakpoint: lookup fingerprint, refresh TTL on hit, MoveToFront (LRU)
  -> clamp cache_read by config ratio; rescale against real upstream totals
  -> atomic counters: hits++ / misses++
Update(accountID, profile)    // success path only
  -> putLocked (insert/refresh) -> evictOverflowLocked (O(1) pop-back LRU)
persisted: data/prompt_cache.json (30s debounced save loop, load on startup)
exposed: Stats() -> /v1/stats "cache" object (hits/misses/evictions/expirations/entries/capacity)
```

TTL default 5 min (`defaultPromptCacheTTL`); min cacheable 1024 tokens (4096 for opus). Capacity is configurable (`config.PromptCacheMaxEntries`, default 131072, floor 256). See the [cache plans](superpowers/plans/) for the design history.

### 5.5 OpenAI Responses (`proxy/responses_*.go`)

```
POST /v1/responses
  -> responses_handler.handleOpenAIResponses
  -> responses_history.expandPreviousResponseHistory   // chain, depth cap 64
  -> responses_input.parseResponsesInput               // normalize flexible input field
  -> dispatch stream/non-stream via the normal translator+kiro path
  -> responses_store persists under data/responses/    // atomic tmp+rename, 30-day TTL, owner-key-ID scoped
```

## 6. Endpoint Inventory (condensed)

**Public API** (API-key gated when multi-key mode is on):
- `POST /v1/messages` (+ `/messages`, `/anthropic/v1/messages`) — Claude
- `POST /v1/messages/count_tokens`
- `POST /v1/chat/completions` (+ `/chat/completions`) — OpenAI Chat
- `POST /v1/responses` (+ `/responses`) — OpenAI Responses
- `GET /v1/models` — model list (cached from upstream)
- `GET /v1/stats` — usage stats (incl. cache metrics)
- `POST /api/event_logging/batch` — telemetry sink

**Admin/Web** (admin-gated): `GET /admin` (SPA), `/admin/*` static files, `/admin/api/*` (~50 sub-routes), plus `GET /health`. `GET /` redirects (302) to `/admin`.

See [System Architecture](system-architecture.md) for diagrams and [Project Overview & PDR](project-overview-pdr.md) for the full functional-requirement list.

## 7. Configuration Surface (high level)

- Single JSON file, default `data/config.json`, overridable via `CONFIG_PATH` (`config/config.go`).
- Defaults: port `8080`, host `0.0.0.0`, admin password `changeme`, `RequireApiKey=false`.
- Migrations: legacy single `ApiKey` -> first entry in `ApiKeys`; per-account legacy `allowOverage` -> `OverageStatus="ENABLED"`.
- Access pattern: getters/updaters under a `sync.RWMutex` (see [Code Standards](code-standards.md)).

## 8. Where to Go Next

| If you want to... | Read |
|--------------------|------|
| Understand the request lifecycle end to end | [System Architecture](system-architecture.md) |
| Know the rules before editing code | [Code Standards](code-standards.md) |
| Add an endpoint / auth provider / client format | [Design Guidelines](design-guidelines.md) |
| Deploy or operate the service | [Deployment Guide](deployment-guide.md) |
| See what's planned | [Project Roadmap](project-roadmap.md) |
| Read the original design/TDD plans | [`docs/superpowers/plans/`](superpowers/plans/), [`docs/superpowers/specs/`](superpowers/specs/) |
