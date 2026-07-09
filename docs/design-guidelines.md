# Kiro-Go — Design Guidelines for Contributors

> **Purpose:** Tell you *where* a given change belongs and *how* to land it cleanly. This is the "where to put things" companion to [Code Standards](code-standards.md) (the "how to write things") and [System Architecture](system-architecture.md) (the "how it fits").
> **Style:** Each section gives the rule, the place in the code, and a worked sketch. There is a [Do Not](#9-do-not-do-this) section at the end.

## 1. Package Boundaries

The boundaries are load-bearing. Respect them.

| Package | Owns | May depend on | Must not do |
|---------|------|---------------|-------------|
| `config` | JSON store + getters/updaters + migrations + API-key management | stdlib only | Request handling, account selection, HTTP clients |
| `pool` | Account selection + per-account runtime state | `config` (reads `Account` snapshots) | Credential storage; reaching into `config` internals |
| `proxy` | HTTP request lifecycle: routing, translation, upstream calls, failover, cache accounting, stats, admin handlers | `config`, `pool`, `auth`, `logger` | Persisting credentials; owning the HTTP client (that's `auth`) |
| `auth` | Credential flows + global hot-swappable HTTP client | `config`, `logger` | Routing, translation |
| `logger` | Leveled logging | stdlib only | Anything else |

**Principles:**

- **Config owns storage.** If data must survive restart, it goes through `config` (field + getter + updater + `Save()`), or into a dedicated data file managed by `proxy` (like `data/prompt_cache.json`, `data/responses/`).
- **Pool owns selection.** New selection logic (health-aware scoring, circuit breaker, session affinity) belongs in `pool/account.go`, fed by events from `proxy/account_failover.go`.
- **Proxy owns the request lifecycle.** A new endpoint, a new stat, a new translator branch — all in `proxy`.
- **Auth owns credentials.** A new sign-in flow, a new token-refresh branch, a new HTTP-client behavior — all in `auth`.

## 2. Adding a New Endpoint

**Public API endpoint** (client-facing, e.g. a new `/v1/...` route):

1. **Route it** in `proxy/handler.go` `ServeHTTP` (the single router, `handler.go:362`). Add the path match alongside the existing public routes.
2. **Authenticate** by routing through the existing `RequireApiKey` gate (`proxy/auth.go:55`) — don't roll your own. It is fail-closed when multi-key mode is on.
3. **Implement the handler** as a method on `*Handler` in `proxy/handler.go` (or a focused file in `proxy/` if the handler is large).
4. **Translate** to/from Kiro using `proxy/translator.go` (see [§5](#5-adding-a-new-client-format)).
5. **Dispatch** through the pool + failover path (`ensureValidToken` -> `GetNextForModel` -> `kiro.CallKiroAPI`) so the new endpoint inherits retry, failover, and stats for free.
6. **Stats:** update the atomic counters (`totalRequests`, `successRequests`, `failedRequests`, `totalTokens`) so `/v1/stats` stays accurate.
7. **Test** with `httptest` in `proxy/<name>_test.go`; add a table-driven case.

**Admin endpoint** (operator-facing, admin-gated):

1. **Route it** inside `handleAdminAPI` (`handler.go:2183`) — it inherits the `X-Admin-Password` / `admin_password` cookie constant-time gate. Do **not** add admin-gated behavior on a public route.
2. If it's API-key CRUD, the home is `proxy/admin_apikeys.go`.
3. Mirror the new capability in the admin SPA (`web/app.js`) and add i18n strings to both `web/locales/en.json` and `web/locales/zh.json`.

## 3. Adding a New Auth Provider

Every credential flow follows the **Start / Poll** (or Start / Complete) async pattern so the admin panel can drive it. Reference implementations: `auth/builderid.go` (device-code), `auth/iam_sso.go` (auth-code + PKCE), `auth/kiro_sso.go` (hosted PKCE, ~906 lines).

1. **Add a file in `auth/`** (e.g. `auth/<provider>.go`). Define the Start step (register client / build auth URL / begin a transient loopback listener) and the Poll/Complete step (exchange code for tokens).
2. **Pick the `AuthMethod`.** Canonical values are snake_case: `idc`, `social`, `external_idp`. If you need a new one, also teach `auth/oidc.go` `RefreshToken` how to refresh it.
3. **Refresh:** implement the refresh path in `auth/oidc.go` dispatch. Mind the response casing: `external_idp` is snake_case; `social` is camelCase; `idc` is the OIDC default.
4. **Security defaults:**
   - PKCE (S256) for any auth-code flow.
   - Anti-CSRF `state`.
   - If you accept a user-supplied IdP endpoint, validate it through `validateExternalIdpEndpoint` (https, non-IP, allow-listed host suffix). Do not weaken `allowedExternalIdpIssuerSuffixes`.
5. **Admin wiring:** expose Start/Poll under `/admin/api/auth/<provider>/...` inside `handleAdminAPI`. On success, persist via `config` (an account entry), not by stashing secrets in memory only.
6. **Loopback listener:** if you need a browser callback, reuse the fixed port 3128 pattern and document the bind nuance (see [Deployment Guide §7.4](deployment-guide.md#74-enterprise-sso-callback-exposure-port-3128)).
7. **Test** the validator and the state/CSRF handling; mock the HTTP exchange with `httptest.NewServer`.

> Existing import paths (not flows): **SSO Token paste** (`auth/sso_token.go` `ImportFromSsoToken`) and **credential JSON paste** (admin handler + the `external_idp` adapter). New paste-imports should validate-before-persist (must succeed at one live refresh before saving the account).

## 4. Adding a New Upstream Behavior (selection / failover)

Failover logic lives in `proxy/account_failover.go`; selection lives in `pool/account.go`.

- **New error class:** add a classifier in `account_failover.go` that maps an upstream signal to a reaction (disable / soft-cooldown / overage refresh / auth-retry / give-up). Keep the retry cap (`maxAccountRetryAttempts=3`) consistent across the dispatch loops in `handler.go` (there are four: `handler.go:897,1467,1664,2045`).
- **New selection criterion:** add it to the skip-conditions in `GetNextExcluding` / `GetNextForModel` (cooldown, near-expiry, quota-blocked, model-not-served). Selection must remain O(accounts) and lock-light.
- **Record the outcome** via `RecordSuccess` / `RecordError` / `MarkOverLimit` / `UpdateToken` / `UpdateStats` so the pool's view of each account stays current.

Health-aware scoring, circuit breaker, and session affinity are **proposed** medium-term work (see [Roadmap §3.1](project-roadmap.md#31-dispatch-health-awareness-part-a---proposed)); they belong here when implemented.

## 5. Adding a New Client Format

All client-format <-> Kiro translation lives in **`proxy/translator.go`** (~66 KB). The pattern is bidirectional: an `XToKiro` builder and a `KiroToX...` response converter.

1. **Add `XToKiro`** that builds a `KiroPayload`: prime the system prompt, apply prompt filters, extract images/tools, sanitize tool names, normalize history, enforce the 900 KB cap via `truncatePayloadToLimit`.
2. **Add `KiroToXResponse`** (and a streaming variant if needed) that consumes the event-stream callbacks from `parseEventStream` (`kiro.go:417`).
3. **Model mapping:** teach `MapModel` / `ParseModelAndThinking` the new format's model names and any alias/normalization rules (see how `gpt-4o -> claude-sonnet-4.5`, `claude-X-Y -> claude-X.Y`, and the `-thinking` suffix are handled).
4. **Route** the new endpoint in `ServeHTTP` and reuse `RequireApiKey`, the pool dispatch, and the atomic stats (see [§2](#2-adding-a-new-endpoint)).
5. **Cache accounting:** if the format has explicit cache breakpoints, feed them through `flattenClaudeCacheBlocks` / `buildConversationID` and the `promptCacheTracker` so cache tokens are estimated consistently.
6. **Test** translation both directions with table-driven cases in `proxy/translator_test.go`.

## 6. Adding a Config Field

Adding a tunable is a fixed checklist (see [Code Standards §3](code-standards.md#3-configuration-access-patterns)). In `config/config.go`:

1. **Field** on the `Config` struct with a `json:"camelCase,omitempty"` tag.
2. **Default constant** if there's a sensible default (`defaultPromptCacheMaxEntries`, etc.).
3. **Getter** (read lock, returns the default when unset/invalid).
4. **Updater** (write lock + `Save()`).
5. **Admin wiring** (a route in `handleAdminAPI` + SPA control) if operators should change it at runtime.
6. **Migration** if it replaces a legacy field (see the existing migrations: single `ApiKey` -> `ApiKeys[0]`; per-account `allowOverage` -> `OverageStatus="ENABLED"`).
7. **Env-override only if it must apply at startup** — then apply it once in `main.go` (like `ADMIN_PASSWORD`, `LOG_LEVEL`). Do not scatter `os.Getenv` through handlers.

## 7. Extending the Prompt-Filter Pipeline

`applyPromptFilters` (in `proxy/translator.go`) runs configured `PromptFilterRule`s against the outgoing prompt.

- **Add a new filter as a small, composable function** (e.g. `stripFooNoise`), next to the existing ones (`isClaudeCodeSystemPrompt`, `stripEnvNoiseLines`, `stripBoundaryMarkers`). Do **not** branch an existing function.
- Keep filters **idempotent** and, where possible, **order-independent**.
- Document the rule's intent and add a translator test case.
- Manage rules through the admin panel (**Settings -> Prompt Filter**) so they persist in config as `PromptFilterRule`s.

## 8. Testing Requirements

- **TDD is the documented workflow.** The `docs/superpowers/plans/` are RED-GREEN: write the failing test, watch it fail, implement, watch it pass, commit per task. Follow that loop when implementing a plan.
- **File suffix** `_test.go` in the same package (white-box where existing tests are).
- **Table-driven** where it pays off; scenario tests for end-to-end behavior.
- **Stdlib only** (`testing`, `httptest`). No external test framework (one-dependency policy).
- **Temp dirs** (`t.TempDir()`) for any file-backed test (config, cache, responses).
- **Before merging:** `go test ./...`, `go vet ./...`, `go build ./...` all clean.
- **Known baseline:** two pre-existing translator image-test failures are acceptable; confirm you're seeing those and not a new failure you introduced.

## 9. Do Not Do This

- **Do not add an external Go dependency casually.** The module has exactly one (`github.com/google/uuid`). New deps need strong justification and a callout in review. Do not add a test framework that pulls transitively.
- **Do not read or mutate `config` struct fields directly from outside `config`.** Use getters/updaters. Do not hold the config lock across a long operation or an HTTP call.
- **Do not default API auth to "allow".** `RequireApiKey` is fail-closed when multi-key mode is on; keep it that way. Never add a public route that should be admin-gated.
- **Do not log secrets.** No tokens, passwords, API keys, or refresh materials. API keys are masked in the admin SPA; cleartext is emitted once on creation.
- **Do not weaken the SSRF guard.** External IdP endpoints must stay https + non-IP + allow-listed suffix. Do not broaden `allowedExternalIdpIssuerSuffixes` without a security review.
- **Do not break SSE.** `http.Server.WriteTimeout=0` is intentional. Do not wrap responses in a buffering middleware or set a write timeout that kills long streams.
- **Do not drop the 900 KB payload cap** or change `truncatePayloadToLimit` to trim from anywhere but the oldest history.
- **Do not make the cache tracker cache responses.** It is accounting-only. Adding response caching is explicitly out of scope ([PDR §6.2](project-overview-pdr.md#62-out-of-scope)).
- **Do not spawn unbounded goroutines.** Background work is a fixed set started in `NewHandler()`. Per-request goroutines must not outlive the request without being joined or having a clear lifecycle.
- **Do not bypass the pool.** Every upstream call should go through selection + `ensureValidToken` + failover so retry, token freshness, and stats remain consistent. Calling `kiro.CallKiroAPI` with a hand-picked account outside the dispatch loop is a smell.
- **Do not change the module's `go 1.21` directive casually.** The Docker builder uses 1.23, but `go 1.21` is the compatibility floor.
- **Do not skip i18n.** Any user-facing admin string goes into both `web/locales/en.json` and `web/locales/zh.json`.
- **Do not add a build step to `web/`.** The admin SPA is vanilla JS served statically. Keep it framework-free.

## 10. Landing a Change (checklist)

- [ ] Change lives in the right package per [§1](#1-package-boundaries).
- [ ] No new external dependency (or it's justified).
- [ ] Config changes use getter + updater + `Save()` ([§6](#6-adding-a-config-field)).
- [ ] Auth changes keep PKCE + state + the SSRF guard ([§3](#3-adding-a-new-auth-provider)).
- [ ] New endpoints route through `RequireApiKey` / `handleAdminAPI` ([§2](#2-adding-a-new-endpoint)).
- [ ] Stats and failover are inherited, not re-implemented ([§4](#4-adding-a-new-upstream-behavior-selection--failover)).
- [ ] Tests added (table-driven; stdlib only); `go test ./...` green ([§8](#8-testing-requirements)).
- [ ] `go vet ./...` and `go build ./...` clean.
- [ ] No secrets logged; admin strings added to both locales.
- [ ] Docs updated if behavior changed: [PDR](project-overview-pdr.md) requirement status, [Roadmap](project-roadmap.md) evidence line, this file if a boundary moved.
