# Phase 03 — Add UI, documentation, and regression validation

## Status

Implemented. Modern + legacy UI toggle, app.js load/save, en/zh locales (file + inline), README/README_CN landed; locale JSON validated. Go + browser regression gates pending (no toolchain locally).

## Dependencies

- Phase 02 exposes `thinkingPassthrough` through the existing `/thinking` settings API.
- Backend tests establish the authoritative OFF/ON semantics.

## Context links

- [`web/index.html`](../../web/index.html)
- [`web/index-legacy.html`](../../web/index-legacy.html)
- [`web/app.js`](../../web/app.js)
- [`web/locales/en.json`](../../web/locales/en.json)
- [`web/locales/zh.json`](../../web/locales/zh.json)
- [`README.md`](../../README.md)
- [`README_CN.md`](../../README_CN.md)

## Implementation steps

### 1. Add the toggle to the modern settings UI

Place a checkbox/switch in the existing Thinking Mode card in [`web/index.html`](../../web/index.html). Do not create a separate settings section.

Requirements:

- Default unchecked when the API field is missing or false.
- Clearly state that OFF preserves Kiro-Go's fixed compatibility behavior.
- Clearly state that ON uses client-provided thinking budget/effort semantics when present.
- Keep suffix and response-format controls available because suffix fallback and output formatting remain supported.
- Follow existing accessibility patterns for labels, descriptions, focus, and keyboard operation.

Update [`web/app.js`](../../web/app.js):

- `loadThinkingConfig` sets the checked state from `d.thinkingPassthrough === true`.
- `saveThinkingConfig` includes the boolean in the existing POST body.
- Existing fallback values remain unchanged.

### 2. Keep the legacy UI functionally consistent

Add the same setting and load/save behavior to [`web/index-legacy.html`](../../web/index-legacy.html), following its inline-script and styling conventions.

Visual parity is not required, but both UIs must expose the same setting, default, and explanatory meaning.

### 3. Add localized strings

Update:

- [`web/locales/en.json`](../../web/locales/en.json)
- [`web/locales/zh.json`](../../web/locales/zh.json)

Add strings for:

- Toggle label.
- OFF/ON behavior hint.
- Optional concise limitation text if the existing card supports it.

Keep terminology consistent with existing Thinking Mode translations. Validate both JSON files after editing.

### 4. Document user-visible behavior

Update the Thinking Mode sections in [`README.md`](../../README.md) and [`README_CN.md`](../../README_CN.md).

Document:

- The setting is OFF by default.
- OFF preserves the existing fixed directive with `max_thinking_length=200000`.
- ON accepts:
  - Claude `thinking.type` and manual `budget_tokens`.
  - Claude `output_config.effort`.
  - OpenAI Chat Completions `reasoning_effort`.
  - OpenAI Responses `reasoning.effort`.
- Accepted effort values: `low`, `medium`, `high`, `xhigh`, and `max`.
- Explicit Claude `disabled` overrides a thinking suffix when ON.
- Manual budget takes precedence over effort.
- The suffix remains a fallback when explicit client settings are absent.
- This is semantic passthrough via Kiro-compatible system directives, not raw JSON forwarding to a verified native Kiro reasoning field.
- Native Anthropic effort can affect broader generation/tool behavior; equivalent Kiro enforcement is not guaranteed.

Include compact request examples for Claude, OpenAI Chat, and OpenAI Responses. Do not include secrets or environment-specific credentials.

### 5. Complete regression validation

Run focused tests first, then full checks:

```bash
go test ./config
go test ./proxy
go test ./...
go vet ./...
go build ./...
```

Also validate frontend assets at minimum by:

- Parsing `web/locales/en.json` and `web/locales/zh.json` as JSON.
- Loading both modern and legacy settings pages if the project's preview/runtime is available.
- Confirming load/save round-trip for false and true.
- Confirming no browser console errors from missing element IDs or localization keys.

If no automated browser test framework exists, perform a minimal manual smoke test rather than adding a new dependency solely for this setting.

## End-to-end regression scenarios

1. Existing config with no toggle field starts OFF and behaves exactly as before.
2. Saving OFF and restarting preserves OFF.
3. Saving ON and restarting preserves ON.
4. Claude manual budget is visible in the generated Kiro system directive exactly.
5. Claude adaptive effort is visible in the generated Kiro directive.
6. Claude explicit disabled plus `-thinking` generates no directive when ON.
7. OpenAI Chat effort is preserved in streaming and non-streaming requests.
8. OpenAI Responses effort is preserved in streaming and non-streaming requests.
9. Invalid effort is rejected before any Kiro upstream call.
10. Suffix-only requests remain compatible in both toggle states.
11. Count-tokens and actual inference use matching thinking directives.
12. Existing response thinking formats remain unchanged.

## Expected files to modify

- [`web/index.html`](../../web/index.html)
- [`web/index-legacy.html`](../../web/index-legacy.html)
- [`web/app.js`](../../web/app.js)
- [`web/locales/en.json`](../../web/locales/en.json)
- [`web/locales/zh.json`](../../web/locales/zh.json)
- [`README.md`](../../README.md)
- [`README_CN.md`](../../README_CN.md)
- Backend test files from Phase 02 as required by final regression fixes.

## Acceptance criteria

- Both admin UIs expose and persist the same boolean setting.
- Missing/false renders as OFF.
- English and Chinese documentation accurately match tested backend behavior.
- All focused and full Go checks pass in a Go-enabled environment.
- Locale JSON parses and both UI variants load without new console errors.
- No unrelated dependency or settings redesign is introduced.

## Risks and rollback

The main UI risk is modern/legacy divergence. Treat the backend field as the single source of truth and include both pages in the smoke checklist. Runtime rollback is switching the setting OFF; documentation rollback is independent and no stored data conversion is needed.
