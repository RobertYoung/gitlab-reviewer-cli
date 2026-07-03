# MCP servers

`review.mcp_servers` grants the review session
[MCP](https://modelcontextprotocol.io) servers — for reference material the
reviewer should consult live rather than guess at. The motivating example:
a repo managing AWS IAM roles, where agents cross-check policies against
the AWS documentation MCP server:

```yaml
projects:
  mygroup/iam-roles:
    review:
      mcp_servers:
        aws-documentation:
          command: uvx
          args: [awslabs.aws-documentation-mcp-server@latest]
          env:
            AWS_DOCUMENTATION_PARTITION: aws
          tools: [search_documentation, read_documentation, recommend]
```

**Read [Security Model](Security-Model.md) before adding a server.**
Reviews process untrusted MR content, and the sandbox's network denial is
what makes prompt injection harmless; an MCP server with network access
reopens that channel to wherever the server can reach.

## Definition schema

Entries mirror Claude Code's `.mcp.json`. Server names must match
`^[a-zA-Z0-9_-]+$`.

| Field | Type | Meaning |
|---|---|---|
| `type` | string | `stdio`, `http`, or `sse`; inferred when omitted (`stdio` if `command`, `http` if `url`) |
| `command` | string | executable for a local stdio server |
| `args` | list | arguments for `command` |
| `env` | map | environment for the server process; keys starting `GITLAB` are **rejected** at validation |
| `url` | string | endpoint of a remote (`http`/`sse`) server |
| `headers` | map | request headers for a remote server (e.g. auth); **redacted** from `config show` |
| `tools` | list | optional: narrow the grant to these named tools; omitting it allows all of the server's tools |

Exactly one of `command` or `url` must be set. There is no flag or
environment form — the grant lives in your settings file only, globally or
per project; a per-project section (as above) keeps it scoped to the repos
that need it.

```yaml
# A remote server
review:
  mcp_servers:
    internal-docs:
      type: http
      url: https://docs-mcp.example.com/mcp
      headers:
        Authorization: Bearer ...
      tools: [search, read_page]
```

## Security posture

Every granted server is a conscious relaxation of the review sandbox: it
runs inside the review subprocess with whatever reach *it* has, so the
exfiltration analysis is only as good as the server's egress. Prefer
servers with narrow, well-known egress (the AWS documentation server only
talks to AWS's own documentation endpoints), keep grants per-project, and
pair them with an OS/proxy-level egress allowlist if you need defence in
depth.

What still holds when MCP servers are granted:

- `--strict-mcp-config` remains in force: only your explicit grant loads.
  Ambient `.mcp.json` files — including one shipped in the reviewed repo —
  are never picked up, and repo agent definitions cannot grant servers.
- `WebFetch`/`WebSearch` and write/exec tools stay denied.
- The GitLab token never enters the subprocess; `GITLAB*` env keys in
  server definitions are rejected at validation, and remote-server
  `headers` are redacted from `config show`.

In MCP-enabled runs the read-only guarantee is enforced through permission
rules (an extended deny list plus `dontAsk` denying everything not
read-only or granted) rather than the claude CLI's `--tools` allowlist,
whose semantics would strip MCP tools — see
[ADR-0008](https://github.com/RobertYoung/gitlab-reviewer-cli/blob/main/docs/adr/0008-mcp-servers.md)
for the details.
