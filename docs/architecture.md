# Architecture

`gitlab-reviewer` is layered so that the frontends only see small
interfaces: `gitlabx.Service` (GitLab API), `checkout.Manager` (repo on
disk), and `review.Reviewer` (AI backend). `internal/gitlabx` is the only
package that imports the GitLab client; `internal/review/claudecli` is the
only package that knows the `claude` binary exists.

There are two frontends over the same core: the TUI (`internal/tui`,
Bubble Tea, the root command) and the browser GUI (`internal/webui`, a
loopback-only HTTP server behind `gitlab-reviewer gui`). Both drive the
review pipeline through `internal/review/runner` (checkout → prompt →
concurrent agent passes → merge → stored record) and publish through
`internal/review/publisher` (position resolution, draft/immediate posting,
note fallback), so a review started in one frontend can be reopened in the
other.

What a scan checks is decided by **review agents**
(`internal/review/agents`): six built-ins mirroring the finding categories
plus user (`~/.config/gitlab-reviewer/agents/`) and repo
(`.gitlab-reviewer/agents/`) definitions, merged into a catalog with
repo > user > builtin shadowing. Each frontend shows a per-scan agent
picker; repo definitions are fetched over the GitLab API at the MR head
SHA (cached per project + sha in `agents.RemoteCache`), so they are
toggleable before any checkout exists. The runner turns the selection into
one reviewer invocation per agent per diff chunk, run under a concurrency
cap, and stamps each finding with the agent that produced it.

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

    subgraph WEBUI["internal/webui (browser GUI)"]
        HANDLERS[HTTP handlers + templates]
        SSE[review run registry + SSE]
    end

    CONFIG[internal/config<br/>koanf: flags > env > file > defaults]
    GITLABX[internal/gitlabx<br/>client-go wrapper]
    POSITION[internal/gitlabx/position<br/>diff parsing + position mapping]
    CHECKOUT[internal/checkout<br/>clone / path / root → worktree]
    REVIEW[internal/review<br/>Reviewer interface, prompt, schema]
    AGENTS[internal/review/agents<br/>builtin + user/repo agent catalog]
    RUNNER[internal/review/runner<br/>agent fan-out + persistence]
    PUBLISHER[internal/review/publisher<br/>posting: inline / draft / fallback]
    CLAUDECLI[internal/review/claudecli<br/>claude -p subprocess]
    SECRET[internal/secret<br/>token redaction]

    GITLAB[(GitLab API)]
    REPO[(git repositories)]
    CLAUDE[[claude CLI<br/>Anthropic API / Bedrock]]

    CONFIG --> TUI
    CONFIG --> WEBUI
    MRLIST --> GITLABX
    MRDETAIL --> GITLABX
    HANDLERS --> GITLABX
    REVIEWRUN --> RUNNER
    SSE --> RUNNER
    RUNNER --> CHECKOUT
    RUNNER --> REVIEW
    RUNNER --> AGENTS
    PUBLISH --> PUBLISHER
    HANDLERS --> PUBLISHER
    PUBLISHER --> POSITION
    PUBLISHER --> GITLABX
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
    T->>U: agent picker (builtin + custom agents)
    U->>T: choose agents, enter
    T->>G: GetMergeRequest (DiffRefs) + ListDiffs
    T->>C: Ensure(MR)
    C->>C: clone/fetch cache, worktree at head SHA
    C-->>T: worktree path
    loop one pass per selected agent × diff chunk (concurrent, capped)
        T->>R: Review(request + agent prompt) — bounded diff on stdin
        activate R
        R-->>T: stream: init / tool use / retries (agent-prefixed progress)
        R-->>T: ReviewResult (validated structured_output)
        deactivate R
    end
    T->>T: merge results, stamp findings with their agent
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

config → MR list → MR detail (diffs + existing discussions) → agent
selection (picker / `--agents` / config) → worktree at head SHA → prompt
(metadata + custom instructions + bounded diff + agent focus) → `claude -p`
with read-only tools in the worktree (one pass per selected agent per diff
chunk, run concurrently, results merged with agent attribution) →
schema-validated findings → user curation → position mapping against parsed
diff hunks → inline discussions or draft review (with note fallback) on the
MR.
