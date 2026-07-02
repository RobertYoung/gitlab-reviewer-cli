# ADR-0002: Bubble Tea for the TUI

## Status

Accepted

## Context

The TUI needs list/detail navigation, a scrollable syntax-highlighted diff
viewer, text editing, and — critically — streaming progress from reviews
that run for minutes without blocking input.

## Decision

Use Bubble Tea v2 with Bubbles (components) and Lipgloss (styling), plus
chroma for syntax highlighting. The Elm-style update loop makes long-running
asynchronous work natural: reviews run in goroutines and feed progress
messages back into the update loop, so the UI never blocks.

Screens are implemented behind an internal `Screen` interface on a screen
stack owned by the root model, keeping each screen independently testable
and insulating the app from framework-version churn.

## Consequences

- De-facto standard stack: components (viewport, table, textarea, spinner)
  and community knowledge come for free.
- Bubble Tea v2 only became stable in mid-2026; the ecosystem (e.g. teatest)
  is still catching up, so rendered-frame tests are kept coarse and most TUI
  coverage is pure `Update` unit tests.
