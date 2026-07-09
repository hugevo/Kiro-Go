# Kiro-Go — Project Overview & Product Development Requirements

> **Document type:** Project overview + Product Development Requirements (PDR)
> **Version:** 1.1.2  | **Module:** `kiro-go` (Go 1.21, Docker build on 1.23)
> **Repo:** https://github.com/Quorinex/Kiro-Go
> **Status:** Living document. Update when scope or requirements change.

This document is the entry point to the `docs/` set. It states what Kiro-Go is, who it is for, what it must do, and how success is measured. The deeper "how" lives in the companion docs:

- [Codebase Summary](codebase-summary.md) — size, stack, package map, data flows.
- [System Architecture](system-architecture.md) — components, sequence diagrams, failover, SSO.
- [Code Standards](code-standards.md) — conventions contributors must follow.
- [Design Guidelines](design-guidelines.md) — where to add endpoints, providers, formats.
- [Deployment Guide](deployment-guide.md) — Docker, ports, volumes, security.
- [Project Roadmap](project-roadmap.md) — current state and direction.

---

## 1. Problem Statement

Kiro / AWS Q Developer / CodeWhisperer accounts expose a capable model backend (Anthropic Claude family, reached through AWS event-stream endpoints), but the native protocol is **not** the OpenAI or Anthropic public API. As a result:

- Standard OpenAI / Anthropic clients, SDKs, and tooling cannot talk to a Kiro account directly.
- A single account's quota, rate limits, and regional availability are quickly exhausted under sustained use.
- Token lifecycle (OAuth refresh, region probing, overage switches, suspension detection) is fiddly and account-specific, so operators managing several accounts end up with brittle, copy-pasted glue.

**Kiro-Go solves this** by acting as a local reverse proxy that (a) translates OpenAI / Anthropic requests into Kiro's internal payload format, (b) pools many upstream accounts behind one endpoint with weighted round-robin selection and automatic failover, and (c) hides all credential/region/overage bookkeeping behind a web admin panel and a clean HTTP API.

## 2. Goals

| # | Goal |
|---|------|
| G1 | Present a single local HTTP server that is byte-compatible with the Anthropic `/v1/messages` and OpenAI `/v1/chat/completions` (+ `/v1/responses`) APIs. |
| G2 | Aggregate N upstream accounts into one pool with transparent failover and per-account model-aware routing. |
| G3 | Automate credential lifecycle: token refresh, cross-region profile probing, overage opt-in, suspension/quota handling. |
| G4 | Provide a self-contained admin panel for accounts, keys, proxy, prompt filters, logs, and stats — no external database required. |
| G5 | Be deployable in one command (Docker Compose) with a single JSON config file and a `/app/data` volume for persistence. |
| G6 | Keep the dependency surface minimal (one external Go module: `github.com/google/uuid`) and ship as a static binary. |

## 3. Target Users

| User | What they want from Kiro-Go |
|------|------------------------------|
| **Power user / developer** | Point an existing Claude/OpenAI client at a local URL and get streaming completions, with zero protocol glue. |
| **Operator of a small pool** | Manage several Kiro accounts in one place, see usage and failures, rotate keys, survive quota blips automatically. |
| **Self-hoster behind a restricted network** | Route upstream traffic through an HTTP or SOCKS5 proxy that can be reconfigured without a restart. |
| **Enterprise SSO user** | Sign in with Microsoft 365 / Entra ID (Azure AD) or IAM Identity Center instead of a personal Builder ID. |
| **Tooling integrator** | A stable, documented HTTP surface (admin API ~50 endpoints, public API) to script against. |

## 4. Functional Requirements

Requirements are numbered `FR-xx`. Each maps to code evidence (file/function) where the behavior is implemented.

### 4.1 API surface (translation)

| ID | Requirement | Evidence |
|----|-------------|----------|
| FR-01 | Accept Anthropic-format Claude requests at `/v1/messages` (alias paths `/messages`, `/anthropic/v1/messages`) and stream or buffer responses. | `proxy/handler.go` `ServeHTTP`; `proxy/translator.go` `ClaudeToKiro` / `KiroToClaudeResponse` |
| FR-02 | Accept OpenAI Chat Completions at `/v1/chat/completions` (alias `/chat/completions`). | `proxy/translator.go` `OpenAIToKiro` / `KiroToOpenAIResponse[WithReasoning]` |
| FR-03 | Accept OpenAI Responses API at `/v1/responses` (alias `/responses`), including `previous_response_id` chaining. | `proxy/responses_handler.go`, `responses_history.go`, `responses_store.go` |
| FR-04 | Resolve model aliases (`gpt-4o` -> `claude-sonnet-4.5`; `claude-X-Y` -> `claude-X.Y` normalization; `-thinking` suffix stripping). | `proxy/translator.go` `MapModel` / `ParseModelAndThinking` |
| FR-05 | Provide `/v1/models` (cached from upstream) and `/v1/messages/count_tokens`. | `proxy/handler.go` |
| FR-06 | Stream responses via Server-Sent Events; never kill long streams. | `main.go` (`WriteTimeout=0`), `proxy/kiro.go` `parseEventStream` |
| FR-07 | Expose `/v1/stats` (requests, success/failure, tokens, prompt-cache hits/misses). | `proxy/handler.go` `handleStats`; `proxy/cache_tracker.go` `Stats()` |

### 4.2 Account pool & failover

| ID | Requirement | Evidence |
|----|-------------|----------|
| FR-08 | Maintain a process-wide account pool with weighted round-robin selection. | `pool/account.go` `AccountPool`, `GetNextExcluding` |
| FR-09 | Route requests to accounts that actually serve the requested model (model-aware selection). | `pool/account.go` `GetNextForModel` (per-account model-ID set) |
| FR-10 | Skip accounts that are in cooldown, near token expiry, or quota-blocked; refresh tokens pre-dispatch. | `proxy/handler.go` `ensureValidToken`; `tokenRefreshSkewSeconds=120` |
| FR-11 | Classify upstream errors and react: disable on quota/429, refresh overage on 402, soft-cooldown on suspension/profile-unavailable, retry on auth. Cap retries at `maxAccountRetryAttempts=3`. | `proxy/account_failover.go` |
| FR-12 | Auto-resolve the upstream profile ARN with cross-region probing and a 24h cooldown for unsupported profiles. | `proxy/kiro_api.go` `ResolveProfileArn`; `defaultKiroProfileRegions=["us-east-1","eu-central-1"]` |

### 4.3 Translation & payload safety

| ID | Requirement | Evidence |
|----|-------------|----------|
| FR-13 | Prime system prompts, extract images and tools, sanitize tool names, and normalize history before sending upstream. | `proxy/translator.go` `ClaudeToKiro` / `OpenAIToKiro` |
| FR-14 | Apply a configurable prompt-filter pipeline (Claude Code system-prompt filter, env-noise line strip, boundary-marker strip, custom regex). | `proxy/translator.go` `applyPromptFilters`, `isClaudeCodeSystemPrompt`, `stripEnvNoiseLines`, `stripBoundaryMarkers` |
| FR-15 | Enforce a payload size cap (`maxPayloadBytes = 900 KB`) by dropping the oldest history entries until under the limit. | `proxy/translator.go` `truncatePayloadToLimit` |
| FR-16 | Estimate tokens with a character-class heuristic for cache accounting and stats. | `proxy/token_estimator.go` |

### 4.4 Prompt-cache accounting

| ID | Requirement | Evidence |
|----|-------------|----------|
| FR-17 | Maintain an in-memory SHA-256-fingerprinted prompt-cache tracker (LRU) that mirrors upstream Anthropic caching to estimate `cache_creation_input_tokens` / `cache_read_input_tokens`. | `proxy/cache_tracker.go` `promptCacheTracker`, `Compute`, `Update` |
| FR-18 | Share cache entries across accounts (single global store), persist to `data/prompt_cache.json`, and rescale estimates against real upstream totals. | `proxy/cache_tracker.go`; persistence loop in `proxy/handler.go` |
| FR-19 | Expose cache stats (hits/misses/evictions/expirations) on `/v1/stats`. | `proxy/cache_tracker.go` `Stats()` |

### 4.5 Authentication & SSO

| ID | Requirement | Evidence |
|----|-------------|----------|
| FR-20 | Support AWS Builder ID device-code flow. | `auth/builderid.go` |
| FR-21 | Support IAM Identity Center / Enterprise AWS SSO (authorization-code + PKCE). | `auth/iam_sso.go` |
| FR-22 | Support hosted Kiro sign-in federating Google, GitHub, and enterprise IdPs (Microsoft 365 / Entra ID / Azure AD) behind one PKCE flow. | `auth/kiro_sso.go` |
| FR-23 | Support raw AWS SSO bearer-token import and credential-JSON paste import. | `auth/sso_token.go` `ImportFromSsoToken`; admin import handlers |
| FR-24 | Dispatch token refresh by `AuthMethod` (external_idp / social / idc). | `auth/oidc.go` `RefreshToken` |
| FR-25 | Harden external-IdP endpoints against SSRF (https, non-IP, allow-listed host suffixes). | `auth/kiro_sso.go` `validateExternalIdpEndpoint`, `allowedExternalIdpIssuerSuffixes` |

### 4.6 Admin panel & configuration

| ID | Requirement | Evidence |
|----|-------------|----------|
| FR-26 | Serve a self-contained admin SPA at `/admin` with tabs for Accounts, Settings, API docs, and Logs, localized CN/EN. | `web/` (index.html, app.js, styles.css, locales/) |
| FR-27 | Gate admin endpoints with `X-Admin-Password` header or `admin_password` cookie using a constant-time compare. | `proxy/handler.go` `handleAdminAPI` |
| FR-28 | Provide ~50 admin API sub-routes: account CRUD + batch + refresh/test/models, all auth flows, settings, stats, logs, api-key CRUD. | `proxy/handler.go` `handleAdminAPI`; `proxy/admin_apikeys.go` |
| FR-29 | Optional multi-API-key mode with per-key quota checks; fail closed when enabled and no key matches. | `proxy/auth.go` `RequireApiKey`; `config/apikeys.go` |
| FR-30 | JSON-backed config with `sync.RWMutex`, getters/updaters, and legacy migrations (single ApiKey -> first ApiKeys entry; per-account overage -> `OverageStatus="ENABLED"`). | `config/config.go`, `config/apikeys.go` |

### 4.7 Operations

| ID | Requirement | Evidence |
|----|-------------|----------|
| FR-31 | Support a hot-reconfigurable outbound HTTP/SOCKS5 proxy (no restart). | `auth/http_client.go` `InitHttpClient`; admin proxy handlers |
| FR-32 | Support "thinking mode" via `-thinking` model suffix or a top-level `thinking` config block. | `proxy/translator.go` `ParseModelAndThinking`; admin `/thinking` |
| FR-33 | Choose upstream endpoints with fallback across Kiro IDE, CodeWhisperer, and Amazon Q. | `proxy/kiro.go` `kiroEndpoints`, `CallKiroAPI` |
| FR-34 | Toggle and persist the upstream AWS Q Overages switch. | `proxy/kiro_overage.go` |
| FR-35 | Ship a leveled logger (DEBUG/INFO/WARN/ERROR) with `LOG_LEVEL` env override. | `logger/logger.go`; `main.go` |

## 5. Non-Functional Requirements

| ID | Category | Requirement |
|----|----------|-------------|
| NFR-01 | **Dependencies** | Exactly one external Go module (`github.com/google/uuid v1.6.0`). Stdlib-first. |
| NFR-02 | **Footprint** | Single static binary (CGO disabled in the Docker build), single config JSON, single data volume. |
| NFR-03 | **Concurrency** | Account pool is a process-wide singleton; runtime stats use atomic counters; hot paths avoid lock contention where possible. |
| NFR-04 | **Streaming** | `http.Server.WriteTimeout=0` so SSE streams are never server-killed; `ReadHeaderTimeout=30s`, `ReadTimeout=60s`, `IdleTimeout=120s` still apply. |
| NFR-05 | **Resilience** | Failover, token refresh skew, retry caps, and 24h unsupported-profile cooldown reduce sensitivity to any single upstream account blip. |
| NFR-06 | **Security default** | Default admin password is `changeme` and MUST be changed before production exposure (env or admin panel). External-IdP endpoints are SSRF-validated. |
| NFR-07 | **Auth posture** | API auth is fail-closed when multi-key mode is enabled. Admin gate uses constant-time password compare. |
| NFR-08 | **Observability** | Request log ring buffer, leveled logs, and `/v1/stats` give runtime visibility without external metrics infrastructure. |
| NFR-09 | **Portability** | Multi-arch Docker image (linux/amd64, linux/arm64); builds from source on Go 1.21+. |
| NFR-10 | **i18n** | Admin panel supports English and Simplified Chinese. |

## 6. Scope

### 6.1 In scope

- Translating OpenAI / Anthropic client traffic to Kiro upstreams and back.
- Multi-account pooling, failover, and credential lifecycle automation.
- Web admin panel + admin HTTP API for all runtime configuration.
- Prompt-cache accounting (estimation only — no response caching).
- Prompt sanitization pipeline.
- OpenAI Responses persistence (`data/responses/`, 30-day TTL).
- Docker / Compose / source deployment and CI multi-arch image publishing.

### 6.2 Out of scope

- **Response caching.** The cache tracker is accounting-only (it reports `cache_*` tokens; it never returns a cached response). See [cache-improvements plan](superpowers/plans/2026-06-27-cache-improvements.md) Global Constraints.
- **A database.** All state is JSON files under `data/`. There is no SQL store.
- **A standalone client UI.** The admin panel is for operators; end users keep their existing Claude/OpenAI client.
- **Modifying upstream Kiro/AWS behavior.** Kiro-Go is a translator and pooler, not a patched upstream.
- **Official affiliation.** Kiro-Go is unaffiliated with Amazon, AWS, or Kiro (see the project Disclaimer).

## 7. Success Metrics

These are the observable signals that the requirements are met in practice. Numbers are targets/expectations, not hard SLOs.

| Metric | Target | Where observed |
|--------|--------|----------------|
| API compatibility | Standard Claude/OpenAI clients connect with no protocol errors. | Client-side; `/v1/messages`, `/v1/chat/completions`, `/v1/responses` |
| Pool availability | A single disabled account does not fail a request (failover within `maxAccountRetryAttempts=3`). | `proxy/handler.go` retry loops; `/v1/stats` failedRequests |
| Token freshness | No request dispatched with an expired token. | `ensureValidToken`; `tokenRefreshSkewSeconds=120` |
| Cache accounting accuracy | Reported `cache_read_input_tokens` tracks real upstream cache reads within the configured ratio; cross-account sharing gives ~pool-wide hit rate rather than ~1/N. | `proxy/cache_tracker.go`; `/v1/stats` cache object |
| Payload safety | No upstream 413/body-too-large caused by oversized prompts (oldest history dropped at 900 KB). | `truncatePayloadToLimit` |
| Admin operability | All ~50 admin routes reachable from the SPA; changes persist across restart. | `data/config.json`, `data/prompt_cache.json`, `data/responses/` |
| Deployment friction | One `docker-compose up -d` brings a working server; data survives recreate via the `./data` volume. | `docker-compose.yml` |
| Dependency hygiene | `go.mod` stays at one external require; `go vet ./...` clean. | `go.mod`; CI |

## 8. Assumptions & Constraints

- All pooled accounts are assumed to be within the same upstream Anthropic org context (this is what justifies cross-account prompt-cache sharing in [FR-18]).
- The operator is responsible for lawfully obtaining and using the upstream accounts; Kiro-Go is educational/research tooling (see Disclaimer).
- The Enterprise SSO (Microsoft 365) callback reaches the host browser via `localhost:3128`; in container/remote deployments the callback URL can be pasted manually via `FeedCallbackURL`.
- The SSO loopback listener is transient (~10-minute login window) and anti-CSRF state-matched, but on shared Docker networks port 3128 may be reachable by peer containers during that window (operators on untrusted networks should isolate the network or pin the bind interface).
