# Publishing

Nothing leaves your machine until you publish. This page covers how
accepted findings and manual comments become comments on the MR, and how
to shape what they look like.

## Draft vs immediate

`publish.mode` (default `draft`) picks between two flows; either can be
toggled per run — `m` on the TUI publish screen, or the mode selector in
the GUI.

- **`draft`** — each accepted finding becomes a GitLab draft note (a
  pending review). Publish them all in one action (`P` in the TUI), or
  leave them pending and finish in the GitLab web UI. Requires
  GitLab ≥ 16.x.
- **`immediate`** — each comment is posted as a live discussion as it is
  accepted for publishing.

## Positioning and the note fallback

Accepted findings are anchored to diff lines as positioned inline
discussions. Position resolution handles renames, context lines, and
removed lines (the rules are in
[ADR-0004](https://github.com/RobertYoung/gitlab-reviewer-cli/blob/main/docs/adr/0004-comment-positioning.md)).

When a finding cannot be anchored to any hunk — or GitLab rejects the
position at publish time — and `publish.fallback_to_note` is on (the
default), the comment is posted as a general MR note carrying the
file/line reference and a blob permalink at the head commit, and marked as
fallen-back in the UI. Findings never silently disappear. With the
fallback off, such findings stay pending with an error instead.

A finding with no file at all (e.g. an MR-level observation) is posted as
a deliberate MR-level note — that is not a fallback.

## Auto-publish

With `publish.auto_comment: true`, findings at or above
`publish.auto_min_severity` (default `major`) are accepted automatically;
weaker findings still wait for your decision on the findings screen.

**Frontend difference:** the TUI publishes the qualifying findings
immediately when the findings screen opens; the GUI pre-accepts them but
still waits for you to click publish.

```yaml
publish:
  auto_comment: true
  auto_min_severity: critical   # only auto-publish the most severe
```

## Publish floor: `publish.min_severity`

`publish.min_severity` (default `info`, i.e. everything publishes) is a
hard floor: findings below it are **never posted to GitLab**, from any
frontend or headless mode. They stay visible on the findings screens,
marked `below-threshold`, so triage still sees the full picture — accepting
one changes nothing, the publisher skips it regardless.

This is different from auto-publish: `auto_min_severity` decides what gets
accepted *for you*, `min_severity` decides what is allowed out at all.

```yaml
publish:
  min_severity: minor       # keep info-level nits off the MR
  auto_comment: true
  auto_min_severity: major  # auto-accept the strong ones
```

## Severity gate

The `gate` section ties the review outcome to approvals. With
`gate.min_severity` set, findings at or above it are **blocking** while
they have not been rejected in triage (manual comments never block); the
MR's newest stored review is what counts.

```yaml
gate:
  min_severity: major
  approvals: block   # off | warn | block
```

`gate.approvals` controls approving *from this tool* while blocking
findings remain:

- **`warn`** (default) — the TUI asks for a confirming second press of
  `a`; the GUI shows the warning on the MR page and relabels the button
  "Approve anyway".
- **`block`** — both frontends refuse the approval until the findings are
  rejected in triage or a re-review comes back clean.
- **`off`** — approvals ignore the gate.

In [headless mode](Headless-Mode.md#output-and-exit-codes) the same gate
drives the exit code, so CI can fail the job. The gate is advisory from
GitLab's perspective — it restricts this tool, not the GitLab UI or API —
so treat it as a team convention, not an enforcement boundary.

## Comment layout: `publish.template`

`publish.template` is a Go
[text/template](https://pkg.go.dev/text/template) that controls how each
comment body is built. The default layout is

```
**[{{.severity}} · {{.category}}] {{.title}}**

{{.body}}
```

which renders as `**[major · design] Title**` followed by the body. If you
would rather your comments read like something you typed yourself, drop
the badge:

```yaml
publish:
  template: "{{.body}}"                    # body only, no header at all
  # template: "{{.title}} — {{.body}}"
  # template: "**{{.severity}}** ({{.agent}}): {{.body}}"
```

Available fields: `{{.severity}}`, `{{.category}}`, `{{.agent}}`,
`{{.title}}`, `{{.body}}`, `{{.file}}`. Unknown fields fail at config
validation, not at publish time. Severity and category are still shown on
the findings screens either way, so nothing is lost by omitting them from
the published comment.

Two things are appended after the templated body:

- **Suggestion blocks** (GitLab ```` ```suggestion ```` syntax), when the
  finding proposes a concrete replacement and is anchored to a new-side
  line.
- The **attribution footer**, when `publish.attribution: true` — a small
  marker that the comment was AI-suggested.

To also change the *tone* of the comment text itself, add guidance via
`review.instructions`, e.g. `"Write comment bodies in first person, as a
colleague would phrase them."`

## Manual comments

Comments you write yourself (`c`/`C` in the TUI, click-to-comment in the
GUI) post **verbatim**: no template, no attribution footer. They go
through the same publish pipeline — publish them on their own, or run a
review and curate them together with the findings.
