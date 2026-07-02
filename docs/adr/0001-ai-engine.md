# ADR-0001: Shell out to the Claude Code CLI

## Status

Accepted

## Context

The reviewer needs full-repository context, multi-step exploration (read
files, grep, follow references), and support for both the Anthropic API and
AWS Bedrock. Building that agent loop ourselves against the raw SDKs would
duplicate what Claude Code already does well.

## Decision

Invoke the `claude` CLI in headless mode (`claude -p`) as a subprocess:

- `--output-format stream-json` for live progress events in the TUI.
- `--json-schema` so findings arrive as validated structured output.
- Read-only tools (`Read`, `Grep`, `Glob`), `--permission-mode dontAsk`, and
  `--strict-mcp-config` so runs are non-interactive and side-effect free.
- Bedrock via Claude Code's native `CLAUDE_CODE_USE_BEDROCK=1` plus AWS
  region/profile environment.

The `claude` binary is a hard runtime dependency, checked at startup with a
friendly install pointer. The invocation sits behind a small
`review.Reviewer` interface so a direct Anthropic/Bedrock SDK backend can be
added later without touching the TUI.

## Consequences

- We inherit Claude Code's agent loop, auth (API key, OAuth/subscription,
  Bedrock), and model updates for free.
- We take on subprocess lifecycle management and output-drift risk,
  mitigated by a version gate, schema validation, and golden-file tests of
  recorded transcripts.
