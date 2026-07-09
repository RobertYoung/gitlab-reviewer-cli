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

## Appearance

The GUI follows your OS light/dark preference and remembers an explicit
choice made with the sun/moon toggle in the top bar. Syntax highlighting
switches with the theme.

## Keyboard shortcuts

Press `?` on any page for the full list. The diff view has `]`/`[` for
next/previous hunk and `n`/`p` for next/previous file; the findings page
has `j`/`k` to move between findings, `a` accept, `x` reject, `e` edit,
and `A` accept-all. `⌘`/`ctrl`+`enter` submits comment and chat forms,
`esc` closes the inline comment form.

## The screens

The GUI mirrors the TUI screen for screen:

- **MR list** — filter by state, author, target branch, and free-text
  search via the filter bar (filters are URL parameters, so views are
  bookmarkable). MRs that already have a stored review carry a *reviewed*
  badge linking to their past reviews. Each MR links to its GitLab page.
  A status column fills in lazily after the list renders: the head
  pipeline's outcome (linking to the pipeline in GitLab) and the MR's
  unresolved discussion-thread count — amber while threads are open, a
  muted check once all are resolved.
- **MR detail** — title, branches, approval status with
  approve/unapprove, the description rendered as markdown, the commit
  list, and a needs-rebase warning when the branch is behind its target.
  With a [severity gate](Publishing.md#severity-gate) configured, blocking
  findings show a warning banner; `warn` relabels the button "Approve
  anyway" and `block` disables it (the server refuses direct POSTs too).
- **Diff view** — syntax-highlighted, soft-wrapped diffs in unified or
  side-by-side layout (default from `ui.diff_view`, switchable via the
  sidebar links), with the changed span inside modified line pairs
  emphasised. A persistent file-explorer tree shows collapsible folders
  with comment/finding counts, and existing MR discussions render inline
  where they were made. Each file header has a *viewed* checkbox that
  collapses the file and ticks it off in the tree (tracked per head
  commit, so a new push resets it); clicking a header folds the file.
  The latest review's findings appear inline on their lines and can be
  accepted or rejected in place.
- **Review form** — the *Run AI review* button (on the overview and the
  diff sidebar) has an agents selector matching the TUI's picker, with a
  per-agent model dropdown next to each; the selection and model picks
  per project are remembered across both frontends. See
  [Review Agents](Review-Agents.md#per-agent-model). Once the MR has a
  stored review, runs default to an incremental re-review (only the
  changes since the last reviewed commit, previous findings and their
  curation states carried forward); a *full re-review* checkbox forces
  scanning the entire diff.
- **Review progress** — the run log streams live over server-sent events,
  with a per-agent status strip (running / done with finding count /
  failed) above it; the page jumps to the findings when the run
  completes. A cancel button stops the run.
- **Findings** — accept, reject, edit, and publish without page reloads;
  a sticky bar tracks accepted/rejected/pending counts next to the
  publish button, and chips filter by state or severity. *View in diff*
  reopens the same findings inline on the diff.
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

## Auto-publish

With `publish.auto_comment` enabled, the GUI publishes qualifying findings
straight after the run, like the TUI. In draft mode the notes are created
as a pending review and the run page offers a one-click *Publish review
now*.

## Differences from the TUI

- The GUI can **delete** a pending manual comment; the TUI cannot (it can
  only publish or let it ride into a review).
- The GUI shows review findings inline on the diff, tracks viewed files,
  emphasises word-level changes, and badges reviewed MRs on the list —
  TUI counterparts are tracked in the feature matrix.
- Browser-native affordances apply: text selection, in-page search, soft
  wrapping.

## Lifecycle notes

Stored review results and run logs survive restarts (they live in the
shared state directory). Pending manual comments and running-review state
are held in memory and die with the process — publish or finish before
stopping the server. Viewed-file ticks live in the browser's local
storage.
