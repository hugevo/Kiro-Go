---
title: "Stabilize GPT-5.6 hidden-reasoning streams"
description: "Suppress GPT-5.6 hidden-reasoning placeholders and preserve delta chunks without changing existing Claude snapshot normalization."
status: implemented (pending Go verification)
priority: P1
branch: "main"
tags: [bugfix, backend, streaming, gpt-5.6]
created: 2026-07-14
createdBy: "claude-code"
---

# Stabilize GPT-5.6 hidden-reasoning streams

## Context

Kiro documents GPT-5.6 Sol, Terra, and Luna as using hidden chain-of-thought: clients should see final output only. When the proxy forces thinking through the `-thinking` suffix, GPT-5.6 can emit repeated `...` reasoning placeholders. The shared stream normalizer also assumes snapshot-like chunks and may remove valid repeated or overlapping delta text, breaking Markdown fences and joining words incorrectly.

## Scope

1. Make the upstream event-stream parser model-aware using the destination `modelId` already present in `KiroPayload`.
2. For destination models matching `gpt-5.6` (bare name and tier variants):
   - Treat `assistantResponseEvent.content` as independent delta text and forward it unchanged.
   - Suppress `reasoningContentEvent` output because GPT-5.6 reasoning is explicitly hidden; this removes standalone `...` placeholders without filtering legitimate ellipses from final answers.
3. Preserve the existing snapshot normalization and visible reasoning behavior for all other models.
4. Do not change thinking request resolution, effort forwarding, model aliases, response formats, or UI code.

## Files

- Modify `proxy/kiro.go`.
- Extend `proxy/kiro_test.go`.

## Implementation

- Add a small stream policy derived from the payload destination model.
- Pass that policy/model into `parseEventStream` from `CallKiroAPI`.
- Keep `normalizeChunk` for existing snapshot-style models.
- Bypass normalization for GPT-5.6 assistant deltas.
- Drop GPT-5.6 reasoning events at the parser boundary so Claude Messages, OpenAI Chat Completions, and Responses behave consistently.

## Regression tests

- GPT-5.6 preserves repeated identical deltas, including paired Markdown fences such as two distinct ``` chunks.
- GPT-5.6 preserves adjacent chunks whose boundary happens to overlap, preventing missing letters or malformed words.
- GPT-5.6 suppresses repeated `reasoningContentEvent` values such as `...` while preserving final-answer ellipses carried by `assistantResponseEvent`.
- A non-GPT model retains cumulative snapshot deduplication and visible reasoning behavior.

## Validation

- Run targeted proxy tests covering event parsing and normalization.
- Run broader `go test ./proxy` and `go test ./...` if the Go toolchain is available.
- If Go remains unavailable on this machine, report the exact unrun gates and retain tests for execution in CI/a Go-enabled environment.

## Acceptance criteria

- GPT-5.6 final Markdown streams without dropped repeated fences or boundary characters.
- Standalone hidden-reasoning `...` placeholders are not exposed to clients.
- Legitimate `...` in GPT-5.6 final answer content remains untouched.
- Existing Claude model stream behavior and tests remain unchanged.
