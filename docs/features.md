# Features by frontend

Both frontends — the TUI (`gitlab-reviewer`) and the browser GUI
(`gitlab-reviewer gui`) — sit over the same core packages: reviews run
through the same pipeline, results land in the same stores, and a review
started in one frontend can be reopened in the other. (The non-interactive
`gitlab-reviewer review` command drives the same pipeline too — see
[Headless mode](wiki/Headless-Mode.md) — but as a one-shot command it has
no screens to track here.) This page tracks
which user-facing features each frontend offers. Anything marked missing
on one side is a parity gap; anything marked n/a only makes sense in the
other modality (keyboard-driven vs browser-native).

## Browsing

| Feature | TUI | GUI | Notes |
|---|:-:|:-:|---|
| Instance picker (multiple `gitlab.instances`) | ✅ | ✅ | GUI serves each instance under its own URL path |
| Group/project scope picker when nothing is configured | ✅ | ✅ | |
| MR list across configured projects/groups | ✅ | ✅ | |
| Filter by state, search text, author, target branch | ✅ | ✅ | TUI: `s`/`/`/`a`/`t`; GUI: filter bar / URL params |
| Pagination | ✅ | ✅ | TUI loads more as you scroll; GUI pages |
| Open MR in browser | ✅ | ✅ | TUI: `o`; GUI: "open in GitLab" link |

## MR detail

| Feature | TUI | GUI | Notes |
|---|:-:|:-:|---|
| Title, branches, author, state, draft badge | ✅ | ✅ | GUI: MR ref, author, and branches link to their GitLab pages |
| Description and commit list | ✅ | ✅ | TUI: `d` toggles the overview over the diff; GUI renders the description as markdown, also collapsible atop the diff view |
| Needs-rebase / conflict warning | ✅ | ✅ | |
| Approval status (who approved, approvals outstanding) | ✅ | ✅ | |
| Approve / unapprove | ✅ | ✅ | Approval is pinned to the head commit that was on screen; TUI: `a` |

## Diff

| Feature | TUI | GUI | Notes |
|---|:-:|:-:|---|
| Unified diff with syntax highlighting | ✅ | ✅ | |
| Split (side-by-side) layout | ✅ | ✅ | TUI: `v` toggles; GUI: layout links in the sidebar; `ui.diff_view` sets the default for both |
| File explorer: collapsible directory tree, status glyphs, comment counts | ✅ | ✅ | TUI: `e` shows/hides, `h`/`l` fold; GUI: sticky sidebar, folders fold in place |
| Existing GitLab discussions shown inline | ✅ | ✅ | |
| Hunk-to-hunk navigation | ✅ | n/a | TUI `]`/`[`; the browser scrolls |
| Soft wrapping, text selection, in-page search | n/a | ✅ | Browser-native |

## Commenting

| Feature | TUI | GUI | Notes |
|---|:-:|:-:|---|
| Manual line-anchored comments | ✅ | ✅ | TUI: line cursor + `c`; GUI: click `+` on a line (both layouts) |
| Manual MR-level comments | ✅ | ✅ | TUI: `C` |
| Delete a pending comment | ❌ | ✅ | Parity gap: the TUI can only publish pending comments or reject them during findings triage |
| Publish pending comments on their own | ✅ | ✅ | TUI: `P` |
| Pending comments ride along with a review run | ✅ | ✅ | |

## Chat

| Feature | TUI | GUI | Notes |
|---|:-:|:-:|---|
| Chat with Claude about the whole MR | ✅ | ✅ | TUI: `T`; GUI: "Chat with Claude" on the MR overview / diff sidebar |
| Chat about a single diff line | ✅ | ✅ | TUI: line cursor + `t`; GUI: `+` on a line → "Ask Claude" (typed text becomes the first message) |
| Multi-turn conversation with full repo context | ✅ | ✅ | Runs in a checkout of the MR head; the backend session resumes across turns |
| Progress streamed while the reply is written | ✅ | ✅ | GUI streams over server-sent events |
| Cancel the reply being written | ✅ | ✅ | TUI: `esc`; GUI: cancel button. The conversation continues |
| Ephemeral: nothing posted to GitLab | ✅ | ✅ | The checkout is released when the chat ends; the conversation itself is not stored, though a raw debug transcript (`chat-*.jsonl`) lands in the state dir like review transcripts do |

## Review

| Feature | TUI | GUI | Notes |
|---|:-:|:-:|---|
| Run AI review (multi-pass for large MRs) | ✅ | ✅ | Same runner |
| Agent selection before each scan | ✅ | ✅ | TUI: picker on `r`; GUI: checkboxes on the review form. Last selection remembered per project |
| Custom review agents (user + repo `.md` definitions) | ✅ | ✅ | Shared catalog; repo > user > builtin on name collision |
| Live progress log | ✅ | ✅ | GUI streams over server-sent events; lines prefixed per agent |
| Cancel a running review | ✅ | ✅ | |
| Findings triage: accept / reject / edit / accept-all | ✅ | ✅ | |
| Publish immediately or as a draft review | ✅ | ✅ | |
| Auto-publish with `publish.auto_comment` | ✅ | ❌ | Parity gap: the TUI publishes qualifying findings on entering the findings screen; the GUI pre-accepts them but still requires a manual publish |
| Position fallback to a general note | ✅ | ✅ | Shared publisher |
| Past reviews: reopen findings with curation state | ✅ | ✅ | Shared result store — cross-frontend |
| View a run's progress log later | ✅ | ✅ | |

## Modality-specific

| Feature | TUI | GUI | Notes |
|---|:-:|:-:|---|
| Full keyboard driving + `?` help screen | ✅ | n/a | GUI is form/link-driven; `⌘`/`ctrl`+`enter` submits comment forms, `esc` cancels |
| Session token auth, loopback-only server | n/a | ✅ | GUI security model |

When adding a feature to one frontend, add it to the other (or record it
here as `n/a` with the reason) and update this table.
