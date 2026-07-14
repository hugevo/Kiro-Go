# Phase 02 — Implement configuration and request mapping

## Status

Implemented. Config toggle, admin API, protocol-native fields, single-resolve directive, translator migration, and token/cache consistency landed; Go verification gates pending (no toolchain locally).

## Dependencies

- Phase 01 precedence and directive formats are authoritative.
- No upstream Kiro JSON reasoning field should be added unless separately verified against the actual Kiro contract.

## Context links

- [`config/config.go`](../../config/config.go)
- [`config/config_test.go`](../../config/config_test.go)
- [`proxy/handler.go`](../../proxy/handler.go)
- [`proxy/handler_test.go`](../../proxy/handler_test.go)
- [`proxy/translator.go`](../../proxy/translator.go)
- [`proxy/translator_test.go`](../../proxy/translator_test.go)
- [`proxy/responses_types.go`](../../proxy/responses_types.go)
- [`proxy/responses_handler.go`](../../proxy/responses_handler.go)
- [`proxy/responses_handler_test.go`](../../proxy/responses_handler_test.go)
- [`proxy/kiro.go`](../../proxy/kiro.go)

## Implementation steps

### 1. Persist the toggle with a safe default

Extend the existing persisted and runtime thinking configuration rather than introducing a new subsystem:

```go
ThinkingPassthrough bool `json:"thinkingPassthrough,omitempty"`
```

Expose the same value on `ThinkingConfig`, and extend `UpdateThinkingConfig` to accept and persist it under the existing mutex discipline.

Compatibility requirements:

- Missing JSON field decodes to `false`.
- Existing configuration files require no migration.
- Existing suffix and response-format defaults remain unchanged.
- Use the spelling `passthrough` consistently in new exported identifiers, JSON, UI IDs, tests, and docs.

Add config tests for absent/default false, true persistence, false persistence, and no regression to existing thinking settings.

### 2. Extend the existing admin API

Update `apiGetThinkingConfig` and `apiUpdateThinkingConfig` in [`proxy/handler.go`](../../proxy/handler.go):

- GET `/thinking` returns `thinkingPassthrough` alongside `suffix`, `openaiFormat`, and `claudeFormat`.
- POST `/thinking` accepts the boolean and calls the extended config updater.
- An omitted field remains false under the current request-decoding contract.
- Do not add another route.

Add handler tests for GET and POST payloads, persistence, and default compatibility.

### 3. Parse protocol-native fields

Extend request types without changing unrelated response contracts:

- `ClaudeRequest` gains `output_config` with an optional `effort`.
- `OpenAIRequest` gains optional `reasoning_effort`.
- `ResponsesRequest` gains optional `reasoning` with optional `effort`.

Use typed nested structs rather than unstructured maps so malformed and invalid values can be validated consistently.

Keep fields optional to preserve compatibility with existing clients.

### 4. Normalize request intent once

Create focused resolver/helper functions that combine:

- toggle state,
- model suffix result,
- Claude thinking configuration,
- Claude effort,
- OpenAI effort,
- Responses effort.

The handlers should resolve the normalized directive once per request and pass it through all downstream operations. Do not independently resolve precedence in each translator.

Required handler behavior:

- Strip the configured model suffix exactly as today.
- With passthrough OFF, use the legacy resolver behavior.
- With passthrough ON, use Phase 01 precedence.
- Validate explicit effort before sending an upstream request.
- Return existing protocol-specific invalid-request responses on validation failure.

### 5. Migrate translator contracts

Replace boolean translator parameters:

```go
func ClaudeToKiro(req *ClaudeRequest, directive ThinkingDirective) *KiroPayload
func OpenAIToKiro(req *OpenAIRequest, directive ThinkingDirective) *KiroPayload
```

A pointer or equivalent immutable value is acceptable if it matches project conventions. Update all production callers and tests together.

Refactor system-prompt construction to render from the normalized directive:

- no directive when disabled,
- fixed legacy directive for OFF/suffix fallback,
- exact manual budget directive,
- adaptive effort directive.

Preserve prompt-filter ordering and current user/system message conversion. Centralize rendering so Claude and OpenAI paths cannot drift.

### 6. Carry Responses reasoning through conversion

In [`proxy/responses_handler.go`](../../proxy/responses_handler.go):

- Preserve `reasoning.effort` while converting a Responses request to the internal OpenAI representation, or normalize it directly before conversion.
- Apply the same suffix stripping and precedence rules as Chat Completions.
- Ensure streaming and non-streaming Responses paths use the same resolved directive.

### 7. Keep token accounting and cache profiles consistent

Claude count-tokens and any effective-request/cache-profile derivation must use the same normalized directive as the actual inference request.

Verify:

- Generated thinking directives are included exactly once in token accounting when they would be sent upstream.
- Explicit disabled removes the generated block when passthrough is ON.
- Cache/profile keys cannot alias requests whose generated thinking directives differ by mode, budget, or effort.
- Existing OFF cache behavior remains compatible.

Do not broaden this phase into unrelated cache redesign.

## Test matrix

Implement focused table-driven coverage before or alongside each code change.

### Config and admin API

- Missing toggle defaults to false.
- Toggle round-trips false and true.
- Existing config fields remain intact.
- GET/POST expose the boolean.

### Claude Messages

- OFF + suffix produces fixed `200000` directive.
- OFF + `thinking.enabled` produces fixed `200000` directive.
- OFF ignores `output_config.effort`.
- ON + manual enabled preserves budgets at minimum and representative larger values.
- ON + adaptive plus each valid effort preserves the effort.
- ON + disabled overrides suffix.
- ON + manual budget plus effort uses manual budget.
- ON + no explicit fields uses suffix fallback.
- Invalid effort returns a client error without an upstream call.

### OpenAI Chat Completions

- OFF ignores `reasoning_effort`.
- ON preserves every accepted effort as adaptive thinking.
- ON retains suffix fallback when effort is omitted.
- Invalid effort returns the established OpenAI-compatible error.
- Streaming and non-streaming payloads produce identical Kiro directives.

### OpenAI Responses

- `reasoning.effort` survives request conversion.
- OFF ignores effort.
- ON preserves valid effort.
- Invalid effort is rejected.
- Streaming and non-streaming paths agree.

### Translation and accounting

- Existing non-thinking payload snapshots remain unchanged.
- User system prompts remain after the generated directive.
- Directives are not duplicated.
- Count-tokens uses the inference-equivalent directive.
- Cache/profile behavior differentiates distinct manual budgets and efforts where applicable.

## Files to modify

Expected files:

- [`config/config.go`](../../config/config.go)
- [`config/config_test.go`](../../config/config_test.go)
- [`proxy/handler.go`](../../proxy/handler.go)
- [`proxy/handler_test.go`](../../proxy/handler_test.go)
- [`proxy/translator.go`](../../proxy/translator.go)
- [`proxy/translator_test.go`](../../proxy/translator_test.go)
- [`proxy/responses_types.go`](../../proxy/responses_types.go)
- [`proxy/responses_handler.go`](../../proxy/responses_handler.go)
- [`proxy/responses_handler_test.go`](../../proxy/responses_handler_test.go)
- Other existing translator/tool-result tests only where required by the signature migration.

## Validation

Run the narrowest tests after each logical change, then the package and repository gates:

```bash
go test ./config
go test ./proxy
go test ./...
go vet ./...
go build ./...
```

The current planning environment did not have `go` available on `PATH`; implementation must run these checks in a Go-enabled environment and report any skipped gate explicitly.

## Acceptance criteria

- Persisted false/default preserves existing behavior.
- All three client request shapes reach one normalized translation contract.
- Manual budgets are preserved exactly when ON.
- Valid efforts are preserved without conversion to invented token budgets.
- Explicit disabled overrides suffix only when ON.
- Translators, token accounting, and cache/profile handling agree on the generated directive.
- No unsupported field is added to Kiro `InferenceConfig`.

## Risks and rollback

- Translator signature migration touches many tests; update callers mechanically, then use focused translation tests to detect semantic drift.
- A mismatch between inference and count-token resolution can cause inaccurate counts or cache reuse; resolve once per request where possible.
- Runtime rollback is the existing setting endpoint/UI toggle. Code rollback requires no config migration because unknown/absent boolean values remain safe.
