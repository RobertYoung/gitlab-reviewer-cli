# ADR-0004: Diff position mapping with note fallback

## Status

Accepted

## Context

GitLab inline discussions require a position: `base_sha`/`head_sha`/
`start_sha` plus old/new path and old/new line. Getting these wrong yields
400 errors or comments anchored to the wrong line. Edge cases: renamed
files, comments on unchanged (context) lines, moved hunks, and findings on
lines outside any hunk.

## Decision

- SHAs come verbatim from the MR's `diff_refs`; never recomputed locally.
- Each MR file diff is parsed into per-file line tables mapping new-line and
  old-line numbers to added/removed/context classifications.
- Resolution rules: added line → `new_line` only; removed line → `old_line`
  only; context line → **both** `old_line` and `new_line` (GitLab requires
  both on unchanged lines). Paths always come from the diff entry itself, so
  renames are handled even if the model reports only one path.
- A finding that cannot be anchored to any hunk falls back to a general MR
  note carrying the file/line reference and a blob permalink at the head
  SHA (`publish.fallback_to_note`, default on).
- If GitLab still rejects a position at publish time, the comment is
  re-posted through the same fallback and marked in the UI, rather than
  failing the publish.

## Consequences

- Findings never silently disappear: worst case they land as a general note.
- The resolver is pure and heavily table-tested against fixture diffs, which
  is where the real-world risk concentrates.
