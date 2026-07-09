# Kiro-Go — Codebase Structure & Conventions

> **Audience:** Contributors editing Go or web assets in this repo.
> **Goal:** Keep the codebase idiomatic, minimal-dependency, and consistent with the patterns already in place.
> **Companion docs:** [Design Guidelines](design-guidelines.md) (where to add things), [System Architecture](system-architecture.md) (how things fit), [Codebase Summary](codebase-summary.md) (the map).

This document records the conventions the existing code already follows. Follow them unless you have a concrete reason not to — and if you do, say so in the PR description.

## 1. Package Layout

The repo is a single Go module (`kiro-go`) with a small number of focused packages. Each package owns one concern.

| Package | Owns | Does NOT do |
|---------|------|-------------|
| `main` | Process bootstrap and server wiring | Business logic (delegates to `proxy`) |
| `config` | The JSON config store + getters/updaters + migrations + API-key management | Request handling, account selection |
| `pool` | Account selection (weighted RR, model-aware) and per-account runtime state | Credential storage (reads `config.Account` snapshots) |
| `proxy` | The HTTP request lifecycle: routing, translation, upstream calls, failover, caching accounting, stats, admin handlers | Persisting credentials (that's `config`) |
| `auth` | Credential flows (Start/Poll) and the global hot-swappable HTTP client | Routing or translation |
| `logger` | Leveled logging on stdlib `log` | Anything else |
| `web/` | The admin SPA (not Go) | Backend behavior |

**Rule:** respect the boundaries. `config` owns storage; `pool` owns selection; `proxy` owns the request lifecycle; `auth` owns credentials. Crossing a boundary (e.g., `pool` reaching into `config` internals) is a smell. See [Design Guidelines §1](design-guidelines.md#1-package-boundaries) for the rationale.

### 1.1 File naming inside `proxy/`

`proxy/` is large (~15,500 LOC across 16 non-test files) and splits by *subsystem*, not by type:

- `handler.go` — the `Handler`, the single router (`ServeHTTP`), all endpoint handlers, all runtime stats, the nested admin router.
- `auth.go` — API-key gate (`RequireApiKey`).
- `account_failover.go` — upstream error classification and reaction.
- `translator.go` — bidirectional client-format <-> Kiro translation + prompt hygiene.
- `cache_tracker.go` — prompt-cache accounting (LRU, metrics, disk persistence).
- `token_estimator.go` — character-class token heuristic.
- `kiro.go` — streaming data-plane client + event-stream parser.
- `kiro_api.go` — REST/control-plane client (profile ARN, regions, usage).
- `kiro_headers.go`, `kiro_overage.go` — header construction and overage switch.
- `responses_*.go` — OpenAI Responses subsystem (handler/types/store/history/input).
- `admin_apikeys.go` — admin CRUD handlers for API keys.

When adding a new subsystem, prefer a new file in the right package over ballooning an existing one.

## 2. Naming Conventions

- **Packages:** short, lowercase, single word (`proxy`, `pool`, `config`, `auth`, `logger`). No underscores, no `pkg` suffix.
- **Exported identifiers:** `CamelCase`. Types are nouns (`Handler`, `AccountPool`, `promptCacheTracker` is intentionally unexported — only its behavior is exposed via methods).
- **Constants:** descriptive names, often with units in the name when it matters (`tokenRefreshSkewSeconds`, `defaultPromptCacheTTL`, `maxPayloadBytes`). Defined at package scope near where they're used.
- **Acronyms:** the codebase uses mixed practice (`URL`, `ARN`, `ID`, `API`) — match the surrounding file.
- **Files:** `snake_case.go` for subsystem files (`account_failover.go`, `kiro_headers.go`); `_test.go` suffix for tests.

## 3. Configuration Access Patterns

All runtime configuration lives in one JSON file (`data/config.json` by default; overridable via `CONFIG_PATH`), guarded by a `sync.RWMutex` in `config/config.go`.

**Rules:**

1. **Never read struct fields directly from outside `config`.** Use a getter (`config.GetPort()`, `config.GetLogLevel()`, `config.GetPromptCacheMaxEntries()`).
2. **Never mutate struct fields directly.** Use an updater (`config.UpdatePromptCacheMaxRatio(ratio)`, `config.SetPassword(...)`) — updaters take the write lock and `Save()`.
3. **Getters take the read lock; updaters take the write lock.** Do not hold either across a long operation.
4. **Adding a config field = field + JSON tag + getter + updater (+ default constant).** Example shape from the cache plans:

```go
// field (config/config.go)
PromptCacheMaxRatio float64 `json:"promptCacheMaxRatio,omitempty"`

// getter (read lock, sensible default)
func GetPromptCacheMaxRatio() float64 {
    cfgLock.RLock()
    defer cfgLock.RUnlock()
    if cfg == nil || cfg.PromptCacheMaxRatio <= 0 || cfg.PromptCacheMaxRatio > 1 {
        return 0.85
    }
    return cfg.PromptCacheMaxRatio
}

// updater (write lock + Save)
func UpdatePromptCacheMaxRatio(ratio float64) error {
    cfgLock.Lock()
    defer cfgLock.Unlock()
    cfg.PromptCacheMaxRatio = ratio
    return Save()
}
```

5. **Env overrides are applied once at startup in `main.go`** (`ADMIN_PASSWORD`, `LOG_LEVEL`) or read live where documented (`LOG_LEVEL` via `logger`). Do not sprinkle env reads through the handlers.

See [Design Guidelines §6](design-guidelines.md#6-adding-a-config-field) for the full checklist.

## 4. Error Handling

- **Idiomatic Go:** return `error` as the last value; callers check it. `log.Fatalf` is reserved for unrecoverable startup failures in `main` (e.g., config load failure).
- **Upstream errors are classified, not bubbled.** `proxy/account_failover.go` maps HTTP status / error shape to a reaction:
  - `429` / quota -> disable account
  - `402` / overage -> refresh the overage switch (`kiro_overage.go`)
  - suspension / profile-unavailable -> soft cooldown
  - auth -> retry
  - retry cap is `maxAccountRetryAttempts = 3` (`account_failover.go:10`)
- **Do not panic across goroutine boundaries.** Background goroutines started in `proxy.NewHandler()` (refresh, stats saver, response purge) must not crash the process on a transient error.
- **Fail closed, not open.** See §7.

## 5. Logging

Use the package-level leveled logger (`logger/logger.go`), not `fmt.Println` or raw `log`:

```go
logger.Debugf("[HTTP] %s %s from %s", r.Method, path, r.RemoteAddr)
logger.Infof("Kiro-Go starting on http://%s (log level: %s)", addr, ...)
logger.Warnf(...)
logger.Errorf(...)
logger.Fatalf(...)   // startup only
```

- Levels: `DEBUG < INFO < WARN < ERROR`. `DEBUG`/`INFO` go to stdout; `WARN`/`ERROR` go to stderr.
- The level is held in an `atomic.Int32` and is overridable at runtime: `LOG_LEVEL` env beats config.
- Tag log lines with a short context prefix (`[HTTP]`, `[CACHE]`, etc.) to make grep effective.

## 6. Testing Conventions

- **File suffix:** `_test.go` in the same package (e.g., `proxy/cache_tracker_test.go`, `proxy/cache_tracker_hardening_test.go`). Tests are white-box where the existing tests are white-box.
- **Table-driven where it pays off.** Many translator/cache tests are table-driven; some are scenario-style (`TestPromptCacheCrossAccountSharing`).
- **TDD is the documented workflow.** The `docs/superpowers/plans/` are RED-GREEN plans: write the failing test first (`go test ./... -run <name> -v`), watch it fail, implement, watch it pass, then commit. Follow that loop when implementing a plan.
- **No external test framework.** Stdlib `testing` + `httptest` only (consistent with the one-dependency policy).
- **Temp dirs for file-backed tests.** Use `t.TempDir()` for config/cache files. The cache disk-persistence test (`TestPromptCacheDiskPersistence`) is the reference pattern.
- **Run the whole suite before merging:** `go test ./...`, then `go vet ./...`, then `go build ./...`.

> **Known baseline:** the cache-capacity plan notes that "only the two pre-existing translator image-test failures noted in the branch history are acceptable" as pre-existing failures. Don't introduce new ones; if you see those two, confirm they're the known pair before assuming you broke something.

## 7. Authentication & Security Defaults

- **API-key gate is fail-closed.** When multi-key mode is enabled (`RequireApiKey=true`), `proxy/auth.go` `RequireApiKey` (around `proxy/auth.go:55`) does a constant-time-style match with per-key quota checks (`config.FindApiKeyByValue`, `config.ApiKeyOverLimit`); legacy single-key mode is a fallback; if nothing matches, the request is rejected. Never default to "allow" when the feature is on.
- **Admin gate is constant-time.** `handleAdminAPI` (`proxy/handler.go:2183`) compares the `X-Admin-Password` header or `admin_password` cookie against the configured password using a constant-time compare. The default password is `changeme` and **must** be changed before production exposure.
- **External IdP endpoints are SSRF-validated.** `auth/kiro_sso.go` `validateExternalIdpEndpoint` requires https, a non-IP host, and a host suffix in `allowedExternalIdpIssuerSuffixes` (`.microsoftonline.com`, `.microsoftonline.us`, `.microsoftonline.cn`). Don't weaken this; see [Deployment Guide §Security](deployment-guide.md#security-hardening).
- **No secrets in logs.** Don't log tokens, passwords, API keys, or refresh materials. The admin SPA masks API keys (`MaskApiKey`) and emits cleartext only once, on creation.

## 8. Payload Safety

- **900 KB upstream cap.** `proxy/translator.go` `truncatePayloadToLimit` enforces `maxPayloadBytes = 900 KB` by dropping the **oldest** history entries until the body is under the limit. Preserve this — it prevents upstream body-too-large failures on long conversations.
- **Order matters.** Drop oldest first (keeps the most recent context). Don't trim from the middle.
- **Prompt hygiene is layered.** Before sending upstream, the translator runs: system-prompt priming, prompt-filter application, Claude-Code system-prompt detection (`isClaudeCodeSystemPrompt`), env-noise line stripping (`stripEnvNoiseLines`), boundary-marker stripping (`stripBoundaryMarkers`), Kiro history sanitization, polluted tool-call text stripping, and tool-result narration. Each is a small, composable function — extend by adding a function, not by editing a monolith.

## 9. Prompt-Filter Pipeline

`applyPromptFilters` (in `proxy/translator.go`) runs configured `PromptFilterRule`s against the outgoing prompt. Rules are managed via the admin panel and stored in config. When extending:

- Add a *new* filter function rather than branching an existing one.
- Keep filters idempotent (running twice must equal running once) and order-independent where possible.
- Document the rule's intent and provide a test case in the translator test suite.

## 10. Dependency Policy

- **Stdlib-first, minimal external deps.** The module currently has exactly one external require: `github.com/google/uuid v1.6.0`. Adding a dependency requires a strong justification and should be called out in review.
- **No test-only frameworks** that pull transitive dependencies. Use `testing` + `httptest`.
- **`go.mod` stays at `go 1.21`.** The Docker builder uses `golang:1.23-alpine`, but the module directive is the compatibility floor — don't bump it casually.

## 11. Concurrency Patterns

- **Process-wide singletons are intentional.** `pool.GetPool()` returns one `AccountPool`; `proxy.NewHandler()` starts a fixed set of background goroutines. Don't spawn per-request goroutines that outlive the request without joining them.
- **Runtime stats use `sync/atomic`** (e.g., `atomic.LoadInt64(&h.totalRequests)`). Don't wrap them in a mutex for reads.
- **The cache tracker uses one mutex** (`promptCacheTracker.mu`) guarding the entries map **and** the LRU list (`container/list`). All map/list mutation happens under that lock. If you touch either, hold the lock.
- **Hot-swappable HTTP client.** `auth/http_client.go` keeps a global `*http.Client` behind an atomic pointer with a per-proxy cache; this is what makes "proxy change takes effect immediately without restart" work. Reconfigure through `InitHttpClient`, not by reassigning globals.

## 12. Web/Admin Conventions

- The SPA is **vanilla JS** statically served from `web/`. The heavy file is `web/app.js` (~169 KB). Keep it framework-free; don't introduce a build step.
- **i18n is mandatory.** User-facing strings go through the locale files (`web/locales/en.json`, `web/locales/zh.json`). Add a string to both.
- **Admin actions hit `/admin/api/*`.** Don't add a new top-level public route for something that should be admin-gated.
- **Copy-to-clipboard on docs.** The API tab documents endpoints with copy buttons; keep example bodies accurate when you change request shapes.

## 13. Quick Reference — Load-Bearing Constants

| Constant | Value | Where | Why it matters |
|----------|-------|-------|----------------|
| `maxAccountRetryAttempts` | 3 | `proxy/account_failover.go:10` | Caps per-request failover fan-out |
| `tokenRefreshSkewSeconds` | 120 | `proxy/handler.go:22` | Pre-emptive token refresh window |
| `maxPayloadBytes` | 900 KB | `proxy/translator.go` | Upstream body-size guard |
| `defaultPromptCacheTTL` | 5 min | `proxy/cache_tracker.go:18` | Prompt-cache accounting TTL |
| `defaultPromptCacheMaxEntries` | 131072 | `config/config.go` | LRU bound (floor 256) |
| `defaultKiroProfileRegions` | `["us-east-1","eu-central-1"]` | `proxy/kiro_api.go:95` | Cross-region profile probing order |
| `http.Server.WriteTimeout` | 0 | `main.go` | Intentional — SSE streams must not be killed |
