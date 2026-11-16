# ADR-0009: Aggregation agent for duplicate findings

## Status

Accepted

## Context

Running several review agents (ADR-0007) means the same defect is often reported more than once, in different words, under different categories, or anchored a line or two apart. The runner deduplicated with a heuristic in `internal/review/dedupe` (same file and line, at least 60% word overlap), which misses paraphrases and different anchors and cannot combine complementary evidence. Reviewers ended up curating and publishing duplicate comments.

## Decision

The shared runner gains an **aggregation phase** after every review pass has finished and before the result is stored, so the TUI, GUI and headless runs all receive the same consolidated findings.

- **Contract.** `review.Aggregator` takes the findings as a JSON array with stable per-call ids (`s1`, `s2`, ...) and returns groups shaped by `review.AggregateSchema`: member ids, an anchor member, a `certain` flag, a title, a body and a merge rationale. The claude backend implements it; the runner uses it when `review.aggregate` is on (the default) and the backend provides one.
- **Deterministic assembly.** The model only decides grouping and wording. `dedupe.Aggregate` rejects any answer that omits, invents or doubly assigns an id or picks an anchor outside its group. A merged finding takes its location, category and suggestion from the anchor source, its severity is the highest any source reported, and the sources plus the rationale are kept in `Finding.Sources` and `Finding.MergeNote` (new optional JSON fields, so stored records need no version bump). Singletons and groups marked uncertain are left exactly as the agents wrote them. Differing suggestions are surfaced in the merge note rather than resolved.
- **Curated findings win.** Findings carried forward by an incremental review are never rewritten. A group with one curated member keeps that finding verbatim and records the fresh duplicates as its sources. A group with several curated members is not merged, and a warning names them for human resolution. Manual comments bypass aggregation.
- **Bounded.** One call sees at most 150 findings. Larger sets are aggregated in location-sorted batches, then one reconciliation call over the batches' canonical findings catches duplicates split across batches; if that set still exceeds the limit the phase fails. With `review.max_budget_usd` set the phase gets one slice of the total (the passes now split the rest) capped at what the passes left unspent, and it runs under `review.timeout`.
- **Fallback.** Any failure other than user cancellation (timeout, claude error, malformed or inconsistent output, budget) keeps every source finding and runs the existing `dedupe.Findings` heuristic with an explicit warning. Cancellation cancels the run.
- **Sandbox.** The aggregation session runs with `--tools ""` and an explicit deny list, no MCP configuration or web and shell grants even when the review session has them, and an empty temporary directory as its cwd. Finding text is untrusted and is wrapped in delimiters that JSON encoding cannot close.

## Consequences

- Each review with two or more findings costs one more claude call (a few cents with a small model), and with a budget cap each pass gets a slightly smaller slice.
- Wording of merged findings comes from the model, so it can drift from the sources; the sources are always one click away and every location and fix is still a reported one.
- The aggregation uses `review.model`. A separate, cheaper aggregation model is a possible follow-up if cost matters.
- Persona or voice styling (#106) is a presentation concern applied to findings, not an aggregation input; the prompt tells the agent to ignore tone when grouping.
- The publisher's check against comments already on the MR is unchanged and still runs at publish time.
