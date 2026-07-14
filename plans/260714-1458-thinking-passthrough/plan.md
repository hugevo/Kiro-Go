---
title: "Thinking passthrough feature toggle"
description: "Add an opt-in mode that preserves client thinking mode, budget, and effort semantics when translating requests to Kiro while retaining the current fixed override by default."
status: implemented
priority: P2
branch: "main"
tags: [feature, backend, api, frontend]
blockedBy: []
blocks: []
created: 2026-07-14
createdBy: "ck:plan"
source: skill
---

> Implementation status (2026-07-14): all three phases implemented and code-reviewed.
> Go build/test/vet gates were NOT run locally because the Go toolchain is not installed
> on this machine; they must be run in a Go-enabled environment before merge. Run
> `gofmt -w proxy/translator.go proxy/responses_types.go` first to clear struct-alignment drift.

# Thinking passthrough feature toggle

## Overview

Add `thinkingPassthrough`, default `false`, to the existing Thinking Mode settings. OFF keeps today's behavior: a suffix or supported Claude `thinking` request enables the fixed `ThinkingModePrompt` with `max_thinking_length=200000`, and client effort fields remain ignored. ON preserves client intent by normalizing Claude/OpenAI thinking fields and rendering equivalent Kiro prompt directives instead of replacing them with the fixed prompt.

This is semantic passthrough, not raw JSON forwarding: Kiro's current request schema has no native thinking fields in `InferenceConfig`, so the proxy must translate the accepted client fields to Kiro-supported system tags.

## Scope

- Claude Messages: `thinking.type`, `thinking.budget_tokens`, `thinking.display`, and `output_config.effort`.
- OpenAI Chat Completions: `reasoning_effort`.
- OpenAI Responses: `reasoning.effort`.
- Existing model suffix remains a fallback trigger.
- Admin API/UI toggle, English/Chinese docs, and regression coverage.
- No new dependency, endpoint, persistence store, or upstream JSON field.

## Key Decisions

- Toggle defaults OFF for config-file and API backward compatibility.
- ON uses one request-scoped normalized directive; no mutable process-global request state.
- Explicit client mode/effort takes precedence over suffix fallback when ON.
- Manual `budget_tokens` is preserved exactly after existing validation; effort is preserved as `low|medium|high|xhigh|max`.
- Accepted effort values are `low`, `medium`, `high`, `xhigh`, and `max` across the supported client shapes; explicitly supplied unsupported values return protocol-compatible HTTP 400 only when passthrough is ON.
- `thinking.display` remains response-format behavior and is not sent upstream as a reasoning-control tag.
- Token estimation and prompt-cache profiling use the same rendered directive as the upstream payload.

## Phases

| Phase | Name | Status |
| --- | --- | --- |
| 1 | [Define passthrough contract](./phase-01-define-passthrough-contract.md) | Implemented (pending Go verification) |
| 2 | [Implement config and request mapping](./phase-02-implement-config-and-request-mapping.md) | Implemented (pending Go verification) |
| 3 | [Add UI docs and regression validation](./phase-03-add-ui-docs-and-regression-validation.md) | Implemented (pending Go verification) |

## Dependencies

- No cross-plan dependencies detected.
- Kiro reasoning control continues to use prompt directives because `proxy.InferenceConfig` currently exposes only `maxTokens`, `temperature`, and `topP`.
- External contracts: [Anthropic effort](https://platform.claude.com/docs/en/build-with-claude/effort), [Anthropic extended thinking](https://platform.claude.com/docs/en/build-with-claude/extended-thinking), and [OpenAI reasoning](https://developers.openai.com/api/docs/guides/reasoning).

## Acceptance Criteria

- [ ] Fresh/legacy config loads with passthrough OFF and produces byte-equivalent legacy thinking directives.
- [ ] ON preserves Claude manual budgets and adaptive/disabled mode plus valid effort.
- [ ] ON accepts OpenAI Chat and Responses effort fields and converts them to the normalized Kiro directive.
- [ ] Explicit client settings beat the model suffix; suffix still works when the client supplies no setting.
- [ ] Count-token estimation, cache profiles, streaming, and non-streaming use one resolved directive.
- [ ] Admin UI can load/save the toggle in both current and legacy pages with English/Chinese labels.
- [ ] Focused tests, full tests, vet, and build pass without weakening existing validation.

## Open Questions

None. Raw JSON passthrough is intentionally excluded because the current Kiro payload contract has no matching fields; semantic translation is the compatible implementation.
