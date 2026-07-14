# Phase 01 — Define thinking passthrough contract

## Status

Implemented. Contract + tests-first landed; Go verification gates pending (no toolchain locally).

## Context

Kiro-Go currently resolves model-suffix and Anthropic thinking inputs into a boolean. When enabled, translators prepend one fixed directive:

```xml
<thinking_mode>enabled</thinking_mode>
<max_thinking_length>200000</max_thinking_length>
```

This loses the client's manual budget and reasoning-effort selection. Kiro-Go's current upstream `InferenceConfig` has no verified native JSON fields for Anthropic `thinking`, `budget_tokens`, or effort, so this feature provides **semantic passthrough through Kiro-compatible system directives**, not byte-for-byte JSON passthrough.

Relevant code:

- [`proxy/translator.go`](../../proxy/translator.go)
- [`proxy/handler.go`](../../proxy/handler.go)
- [`proxy/responses_types.go`](../../proxy/responses_types.go)
- [`proxy/responses_handler.go`](../../proxy/responses_handler.go)
- [`proxy/kiro.go`](../../proxy/kiro.go)

## Requirements

### Supported client inputs

Normalize these request shapes into one request-scoped value:

- Claude Messages:
  - `thinking.type`: `enabled`, `adaptive`, or `disabled`
  - `thinking.budget_tokens` for manual thinking
  - `output_config.effort`: `low`, `medium`, `high`, `xhigh`, or `max`
- OpenAI Chat Completions:
  - `reasoning_effort`: `low`, `medium`, `high`, `xhigh`, or `max`
- OpenAI Responses:
  - `reasoning.effort`: `low`, `medium`, `high`, `xhigh`, or `max`
- Existing model suffix, default `-thinking`, as a compatibility fallback.

### Normalized request-scoped representation

Replace the translator-facing boolean with one small value object. The implementation may refine names, but it must carry enough information to distinguish:

```go
type ThinkingDirective struct {
    Enabled      bool
    Explicit     bool
    Mode         string
    BudgetTokens int
    Effort       string
    Source       string
}
```

Required semantics:

- `Enabled` determines whether Kiro thinking directives are generated.
- `Explicit` distinguishes client intent from suffix fallback.
- `Mode` distinguishes manual `enabled`, `adaptive`, and disabled/no-thinking behavior.
- `BudgetTokens` preserves a valid manual budget exactly.
- `Effort` preserves a valid soft effort signal.
- `Source` is optional implementation metadata for deterministic resolution and tests; it must not create mutable global state.

Do not add a service, dependency, database migration, or model capability registry for this feature.

### Toggle and precedence

The persisted toggle is OFF by default.

When passthrough is OFF:

1. Preserve current behavior exactly.
2. The model suffix and supported Anthropic thinking request continue to resolve to a boolean.
3. Any enabled result receives the fixed `ThinkingModePrompt` with `max_thinking_length` set to `200000`.
4. Client effort remains ignored.
5. Existing validation and response formatting remain unchanged.

When passthrough is ON:

1. Explicit client configuration takes precedence over the model suffix.
2. Explicit `thinking.type=disabled` disables generated thinking directives even when the model has the thinking suffix.
3. `thinking.type=enabled` with a valid `budget_tokens` generates manual Kiro directives and preserves that integer exactly.
4. `thinking.type=adaptive` generates adaptive mode; a valid Claude `output_config.effort` is emitted as the effort directive.
5. OpenAI `reasoning_effort` and Responses `reasoning.effort` enable adaptive thinking and preserve the effort value.
6. A concrete manual budget takes precedence over an effort-derived soft setting when both are present.
7. If no explicit client thinking or effort configuration exists, the model suffix remains the fallback and retains the current fixed `200000` budget behavior.
8. Without explicit client configuration or a matching suffix, do not generate thinking directives.

### Generated Kiro directives

Manual thinking:

```xml
<thinking_mode>enabled</thinking_mode>
<max_thinking_length>2048</max_thinking_length>
```

The numeric value must be the client's validated `budget_tokens`, not a mapped effort estimate.

Adaptive effort:

```xml
<thinking_mode>adaptive</thinking_mode>
<thinking_effort>medium</thinking_effort>
```

Suffix-only fallback and passthrough OFF retain the existing fixed directive.

The generated block must be prepended once to the filtered system prompt. It must not overwrite user system text or duplicate itself across translation and token-count paths.

### Validation

- Retain existing Anthropic manual-thinking validation:
  - `budget_tokens` is required for `enabled`.
  - Minimum budget is `1024`.
  - Budget must be less than `max_tokens`.
  - `adaptive` and `disabled` reject `budget_tokens`.
- Accept effort values only from the supported enum.
- Reject an explicitly supplied invalid effort with the protocol's existing invalid-request response shape; do not silently downgrade it.
- An omitted effort is not an error.
- Do not invent a manual budget from effort.
- Preserve existing `thinking.display` behavior and response formatting.

### Semantic limitation

Document that Anthropic effort can influence overall response-token spending and tool behavior in the native API. Kiro-Go can preserve the requested semantic signal only through the Kiro-compatible system directive available in this codebase; equivalent upstream enforcement is not guaranteed.

## Files to modify

This phase defines contracts that implementation in later phases applies to:

- [`proxy/translator.go`](../../proxy/translator.go)
- [`proxy/handler.go`](../../proxy/handler.go)
- [`proxy/responses_types.go`](../../proxy/responses_types.go)
- [`proxy/responses_handler.go`](../../proxy/responses_handler.go)

## Tests to define first

Add table-driven tests that lock down:

- OFF mode's fixed `200000` directive.
- ON mode manual budget preservation.
- ON mode adaptive effort preservation for all five accepted effort values.
- Explicit disabled overriding the suffix.
- Manual budget taking precedence over effort.
- Suffix-only fallback.
- No-input/no-suffix behavior.
- Invalid effort rejection for each supported API shape.
- No duplicate generated directive.

## Acceptance criteria

- One deterministic resolution contract covers Claude Messages, OpenAI Chat Completions, and OpenAI Responses.
- OFF behavior is byte-for-byte compatible at the generated prompt level.
- ON precedence is explicit and testable.
- The plan does not claim unsupported raw Kiro JSON passthrough.
- No request-specific thinking state is stored globally.

## Risks and rollback

The primary risk is changing current compatibility when the toggle is absent or false. Isolate the legacy branch and protect it with regression tests. Rollback consists of disabling the toggle; no data migration is required.
