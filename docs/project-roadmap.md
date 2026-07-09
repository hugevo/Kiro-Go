# Kiro-Go — Project Roadmap

> **Purpose:** State where Kiro-Go is today and where it's heading, grounded in code evidence and the existing design plans.
> **Conventions:** Each item is marked **Done**, **In progress**, or **Proposed**, with the evidence that justifies the status. "Proposed" means the idea fits the architecture but has no committed plan yet.
> **Companion docs:** [Project Overview & PDR](project-overview-pdr.md), [Codebase Summary](codebase-summary.md), the design plans under [`docs/superpowers/`](superpowers/).

## 1. Current State (v1.1.2)

Kiro-Go is a working, deployable reverse proxy. The capabilities below are evidenced in the v1.1.2 codebase.

### 1.1 API surface — Done

- Anthropic `/v1/messages` (with aliases) and OpenAI `/v1/chat/completions` — `proxy/translator.go`.
- OpenAI Responses `/v1/responses` with `previous_response_id` chaining — `proxy/responses_handler.go`, `responses_history.go`, `responses_store.go`.
- `/v1/models` (cached), `/v1/messages/count_tokens`, `/v1/stats`, `/api/event_logging/batch` — `proxy/handler.go`.
- SSE streaming with `WriteTimeout=0` so streams aren't killed — `main.go`.

### 1.2 Account pool & failover — Done

- Weighted round-robin + model-aware routing — `pool/account.go`.
- Pre-dispatch token refresh (`tokenRefreshSkewSeconds=120`), retry cap (`maxAccountRetryAttempts=3`), error classification and reaction (disable/cooldown/overage/retry) — `proxy/handler.go`, `proxy/account_failover.go`.
- Cross-region profile probing (`us-east-1`, `eu-central-1`) with 24h unsupported-profile cooldown, URL regionalization — `proxy/kiro_api.go`.

### 1.3 Translation & payload safety — Done

- Bidirectional client-format <-> Kiro translation, system-prompt priming, image/tool extraction, tool-name sanitization, history normalization — `proxy/translator.go`.
- 900 KB payload cap dropping oldest history (`truncatePayloadToLimit`) — `proxy/translator.go`.
- Prompt-filter pipeline: Claude Code system-prompt filter, env-noise strip, boundary-marker strip, custom regex — `proxy/translator.go`.

### 1.4 Prompt-cache accounting — Done (with active hardening)

- In-memory SHA-256 LRU tracker, cross-account sharing, disk persistence (`data/prompt_cache.json`), `/v1/stats` metrics — `proxy/cache_tracker.go`.
- This area is the focus of the active design plans (see §2); treat the tracker as correct-but-being-hardened.

### 1.5 Authentication & SSO — Done

- Five credential paths: Builder ID, IAM Identity Center, hosted Kiro sign-in (Google/GitHub/enterprise IdPs), SSO Token paste, credential JSON paste — `auth/`.
- SSRF-hardened external IdP validation, PKCE everywhere, anti-CSRF state — `auth/kiro_sso.go`.

### 1.6 Admin & operations — Done

- Self-contained admin SPA (Accounts/Settings/API/Logs, CN/EN) — `web/`.
- ~50 admin API routes — `proxy/handler.go` `handleAdminAPI`, `proxy/admin_apikeys.go`.
- Hot-reconfigurable outbound HTTP/SOCKS5 proxy — `auth/http_client.go`.
- Thinking mode via `-thinking` suffix or top-level `thinking` block — `proxy/translator.go`.
- Leveled logger with `LOG_LEVEL` env override — `logger/logger.go`.

### 1.7 Deployment — Done

- Multi-stage Dockerfile, multi-arch images (linux/amd64, linux/arm64) to GHCR via GHA — `Dockerfile`, `.github/workflows/docker.yml`.
- Docker Compose with loopback-only SSO callback publish — `docker-compose.yml`.

## 2. Near-Term (from active design plans)

These have committed TDD plans under `docs/superpowers/plans/`. Status reflects whether the plan's changes are merged.

### 2.1 Prompt-cache improvements (C1/C2/C3) — In progress

Source: [`docs/superpowers/plans/2026-06-27-cache-improvements.md`](superpowers/plans/2026-06-27-cache-improvements.md).

- **C1 — Cross-account cache sharing** (drop per-account isolation; all pooled accounts share one store). *Rationale:* accounts are in the same upstream Anthropic org, so upstream cache is shared; making the fingerprint store global corrects the accounting so hit rate rises from ~(1/N) x 70% to ~70%.
- **C2 — Configurable cache-read cap** (`config.PromptCacheMaxRatio`, default 0.85) so "continue"-heavy workloads where >85% of input is genuinely cached aren't under-reported.
- **C3 — Disk persistence** (load `data/prompt_cache.json` on startup, debounced 30s write) so restart doesn't lose entries.

> Evidence check: the v1.1.2 tracker already has a global store, a configurable ratio, disk load/save, and the persistence loop is wired in `proxy.NewHandler()`. Treat C1/C2/C3 as **Done in v1.1.2** unless a re-baseline says otherwise; the plan documents remain the authoritative spec for the behavior.

### 2.2 Cache capacity, O(1) LRU, and metrics — In progress

Source: [`docs/superpowers/plans/2026-07-02-cache-capacity-lru-metrics.md`](superpowers/plans/2026-07-02-cache-capacity-lru-metrics.md).

- Raise the LRU bound from 4096 to **131072** (`config.PromptCacheMaxEntries`, floor 256) to stop prefix churn under load.
- Replace O(n log n) sort-under-lock eviction with **O(1) `container/list` LRU** (pointer entries + recency list, `putLocked` / `evictOverflowLocked`).
- Add **atomic counters** (`hits`/`misses`/`evictions`/`expirations`) and a `Stats()` method surfaced on `/v1/stats` under a `cache` object.
- Churn regression guards: at cap 131072 a 5000-prefix workload does not churn; at cap 4096 it does.

> Evidence check: v1.1.2 already ships `container/list`-based pointer entries, `putLocked`/`evictOverflowLocked`, `Stats()`, and `/v1/stats` cache metrics. Mark **Done in v1.1.2**. (The plan's "two constructors" refinement is the implementation shape.)

### 2.3 external_idp credential-JSON adapter — In progress

Source: [`docs/superpowers/plans/2026-06-27-credential-json-external-idp-adapter.md`](superpowers/plans/2026-06-27-credential-json-external-idp-adapter.md).

- Make the "Add credential JSON" import accept and persist `external_idp` (Azure AD / Microsoft 365) accounts from any JSON shape.
- Read `tokenEndpoint`/`issuerUrl`/`scopes` from the pasted JSON, validate the IdP endpoint against the allow-list (SSRF guard), route refresh through the existing `refreshExternalIdpToken`.
- Must succeed at one live token refresh before persisting (regression test `TestApiImportCredentialsRejectsWhenRefreshFails`).

> Evidence check: `auth/kiro_sso.go` exposes `ValidateExternalIdpEndpoint`; `auth/oidc.go` has `refreshExternalIdpToken`. The adapter is wired; mark **Done in v1.1.2** pending a re-baseline against the plan's test list.

## 3. Medium-Term (proposed, architecture-ready)

Items that fit the existing boundaries and have clear homes, but no committed plan yet.

### 3.1 Dispatch health-awareness (Part A) — Proposed

The cache-improvements plan explicitly defers a "dispatch" track: health-aware scoring, circuit breaker, session affinity, auto-recovery, and weight-as-probability selection. This lives in `pool/account.go` and `proxy/account_failover.go`. A separate plan is TBD (per the cache plan's "Out of scope" note).

### 3.2 More client formats / providers — Proposed

The translator is the extension point (`proxy/translator.go`). Plausible additions: Google Gemini-format clients, additional OpenAI sub-formats, or richer tool-use normalization. See [Design Guidelines §5](design-guidelines.md#5-adding-a-new-client-format).

### 3.3 Observability — Proposed

- A `/health` payload richer than the current liveness check (pool depth, recent failure rate, per-account freshness).
- Optional Prometheus/OpenMetrics export from the existing atomic counters without adding a dependency (a lightweight handler, not a client library).

### 3.4 Admin panel polish — Proposed

- Replace `web/index-legacy.html` (176 KB legacy fallback) usage over time as the modern `index.html` SPA proves out.
- More granular per-key and per-account dashboards driven by the existing stats.

### 3.5 Testing & CI — Proposed

- Run `go test ./...` in CI (today CI builds/publishes images only — `.github/workflows/docker.yml`).
- Gated `go vet ./...` on PRs.
- Document the known pre-existing translator image-test failures so they stop causing false alarms.

## 4. Long-Term (directional, not committed)

- **Pluggable upstream back-ends:** generalize the data-plane client beyond the Kiro/CodeWhisperer/Amazon Q trio so additional model providers can be pooled behind the same translator.
- **Optional external state store:** today everything is JSON under `data/`. A large/HA deployment might want an optional external store, but this would be a significant scope change against NFR-02 (single binary, single file).
- **Formal API stability policy:** versioned public API surface and a deprecation cadence as adoption grows.

## 5. Explicitly Out of Scope (re-stated)

- **Response caching.** The cache tracker is accounting-only. This is a hard constraint, not a gap — see [PDR §6.2](project-overview-pdr.md#62-out-of-scope).
- **A database.** All state is JSON files under `data/`.
- **Official affiliation** with Amazon/AWS/Kiro (see the project Disclaimer).

## 6. How to Move an Item Forward

1. **Proposed -> plan:** write a TDD plan under `docs/superpowers/plans/` (and a design spec under `specs/`) following the existing format (Goal / Architecture / Global Constraints / file-by-file tasks with RED-GREEN steps).
2. **Plan -> code:** implement via the RED-GREEN loop; run `go test ./...`, `go vet ./...`, `go build ./...`; commit per task with conventional-commit messages.
3. **Code -> Done:** update this roadmap's evidence line and the relevant [PDR](project-overview-pdr.md) requirement status.
