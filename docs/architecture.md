# Architecture

`gitlab-reviewer` is layered so that the TUI only sees small interfaces:
`gitlabx.Service` (GitLab API), `checkout.Manager` (repo on disk), and
`review.Reviewer` (AI backend). `internal/gitlabx` is the only package that
imports the GitLab client; `internal/review/claudecli` is the only package
that knows the `claude` binary exists.

## Component diagram

```mermaid
graph TD
    subgraph TUI["internal/tui (Bubble Tea)"]
        MRLIST[mrlist screen]
        MRDETAIL[mrdetail screen]
        REVIEWRUN[reviewrun screen]
        FINDINGS[findings screen]
        PUBLISH[publish screen]
    end

    CONFIG[internal/config<br/>koanf: flags > env > file > defaults]
    GITLABX[internal/gitlabx<br/>client-go wrapper]
    POSITION[internal/gitlabx/position<br/>diff parsing + position mapping]
    CHECKOUT[internal/checkout<br/>clone / path / root → worktree]
    REVIEW[internal/review<br/>Reviewer interface, prompt, schema]
    CLAUDECLI[internal/review/claudecli<br/>claude -p subprocess]
    SECRET[internal/secret<br/>token redaction]

    GITLAB[(GitLab API)]
    REPO[(git repositories)]
    CLAUDE[[claude CLI<br/>Anthropic API / Bedrock]]

    CONFIG --> TUI
    MRLIST --> GITLABX
    MRDETAIL --> GITLABX
    REVIEWRUN --> CHECKOUT
    REVIEWRUN --> REVIEW
    FINDINGS --> POSITION
    PUBLISH --> GITLABX
    REVIEW --> CLAUDECLI
    GITLABX --> GITLAB
    CHECKOUT --> REPO
    CLAUDECLI --> CLAUDE
    SECRET -.redacts.- GITLABX
    SECRET -.redacts.- CLAUDECLI
```

## Review flow

```mermaid
sequenceDiagram
    actor U as Engineer
    participant T as TUI
    participant G as gitlabx (GitLab API)
    participant C as checkout
    participant R as claudecli (claude -p)
    participant P as position resolver

    U->>T: select MR, press r
    T->>G: GetMergeRequest (DiffRefs) + ListDiffs
    T->>C: Ensure(MR)
    C->>C: clone/fetch cache, worktree at head SHA
    C-->>T: worktree path
    T->>R: Review(request) — bounded diff on stdin
    activate R
    R-->>T: stream: init / tool use / retries (progress)
    R-->>T: ReviewResult (validated structured_output)
    deactivate R
    T->>U: findings list (edit / accept / reject)
    U->>T: accept findings, publish
    T->>P: Resolve(finding, parsed diffs, DiffRefs)
    alt position resolved
        T->>G: create inline discussion / draft note
    else no valid position
        T->>G: create general MR note (fallback)
    end
    opt draft mode
        T->>G: PublishAllDraftNotes
    end
    G-->>U: comments visible on the MR
```

## Data flow summary

config → MR list → MR detail (diffs + existing discussions) → worktree at
head SHA → prompt (metadata + custom instructions + bounded diff) → `claude
-p` with read-only tools in the worktree (one pass per diff chunk for large
MRs, results merged) → schema-validated findings → user curation → position
mapping against parsed diff hunks → inline discussions or draft review
(with note fallback) on the MR.
