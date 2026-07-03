# GUI guide

`gitlab-reviewer gui` serves the same review workflow as a local web app.
It shares every core component with the TUI — reviews run through the same
pipeline, results land in the same stores, and a review started in one
frontend can be reopened in the other. The
[feature matrix](https://github.com/RobertYoung/gitlab-reviewer-cli/blob/main/docs/features.md)
tracks the few deliberate differences.

## Launching

```sh
gitlab-reviewer gui                # random free port, opens your browser
gitlab-reviewer gui --port 8080    # fixed port
gitlab-reviewer gui --no-browser   # just print the URL
```

The server binds to `127.0.0.1` only and prints a launch URL containing a
per-session random token. Opening the URL once exchanges the token for a
strict same-site, http-only session cookie and strips it from the address
bar; requests without the cookie get a 403. Other local processes cannot
drive your review session. See
[Security Model](Security-Model.md#the-browser-guis-session-security).

With several `gitlab.instances` configured, the GUI starts on an instance
picker and serves each instance under its own URL path — no `--instance`
flag needed.

## The screens

The GUI mirrors the TUI screen for screen:

- **MR list** — filter by state, author, target branch, and free-text
  search via the filter bar (filters are URL parameters, so views are
  bookmarkable). Each MR links to its GitLab page.
- **MR detail** — title, branches, approval status with
  approve/unapprove, the description rendered as markdown, the commit
  list, and a needs-rebase warning when the branch is behind its target.
- **Diff view** — syntax-highlighted, soft-wrapped diffs in unified or
  side-by-side layout (default from `ui.diff_view`, switchable via the
  sidebar links), a persistent file-explorer tree with collapsible
  folders, and existing MR discussions rendered inline where they were
  made.
- **Review form** — the *Run AI review* button has an agents selector
  (checkboxes) matching the TUI's picker; the last selection per project
  is remembered across both frontends.
- **Review progress** — the run log streams live over server-sent events;
  the page jumps to the findings when the run completes. A cancel button
  stops the run.
- **Findings** — accept, reject, edit, and publish, same as the TUI.
- **Past reviews** — reopen any stored review with its curation state.

## Commenting and chat

- **Click-to-comment**: the `+` button on any diff line opens an inline
  comment form (both layouts). `⌘`/`ctrl`+`enter` submits, `esc` closes.
  Pending comments can be deleted before publishing, published on their
  own, or left to ride along with the next review run.
- **General comments**: an MR-level comment form on the detail page.
- **Chat**: "Chat with Claude" on the MR overview (or the diff sidebar)
  opens a conversation about the whole MR; the `+` menu on any diff line
  offers "Ask Claude" alongside "Add comment" — whatever you typed becomes
  the first message of a chat anchored to that line. Replies stream their
  progress live; `⌘`/`ctrl`+`enter` sends. The conversation is multi-turn
  and continues until you end the chat.

## Differences from the TUI

- The GUI can **delete** a pending manual comment; the TUI cannot (it can
  only publish or let it ride into a review).
- With `publish.auto_comment` enabled, the TUI publishes qualifying
  findings automatically; the GUI marks them accepted but waits for you to
  click publish.
- Hunk-to-hunk jump keys don't exist — the browser scrolls; in exchange
  you get text selection, in-page search, and soft wrapping.

## Lifecycle notes

Stored review results and run logs survive restarts (they live in the
shared state directory). Pending manual comments and running-review state
are held in memory and die with the process — publish or finish before
stopping the server.
