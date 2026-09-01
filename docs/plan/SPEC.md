# atlassian-mcp-lite — Specification

Status: agreed 2026-09-01. Decisions reached by structured interview; each was chosen by the
repository owner, not defaulted.

## Purpose

A minimal Model Context Protocol server exposing a small, fixed set of Jira and Confluence
operations, with the advertised tool and parameter surface controlled by configuration.

Built rather than adopted. `sooperset/mcp-atlassian` v0.23.1 was audited and rejected on the
surface-ratio argument: 98 tools and 149 transitive dependencies to serve 10 operations, with a
30-advisory history in which at least six fixes were incomplete. Atlassian's own Rovo MCP server is not
always an option: organisations can disable API-token connections to it, in which case it answers
"You don't have permission to connect via API token" and only OAuth remains.

## Global constraints

- Go, module path `github.com/OxCom/atlassian-mcp-lite`. `go.mod` declares **go 1.27** — the
  current stable release. All builds and tests run in `golang:1.27-trixie`, so the host toolchain
  version is irrelevant.
- MCP SDK: `github.com/modelcontextprotocol/go-sdk` v1.7.0 (official; supports spec 2026-07-28).
- HTTP via stdlib `net/http` only. No third-party HTTP, retry, or logging library.
- Markup parsing uses real parsers, not regex: `github.com/yuin/goldmark` (zero dependencies) for
  markdown, `golang.org/x/net/html` for HTML. Hand-rolled regex parsing of markdown or HTML is
  rejected — it is the classic source of silent corruption.
- Dependency budget: `go-sdk` (which itself pulls 11 modules), `goldmark`, `golang.org/x/net`, and
  `github.com/google/jsonschema-go` — the schema package `go-sdk` v1.7.0 itself requires, pinned to
  the same v0.4.3, and a direct `require` only because `internal/core` imports it directly (Go has
  no other way to name an imported module). It adds nothing to the 16-module total.
  Any addition beyond these requires changing this line first.
  Unmaintained convenience libraries are rejected on principle: `html-to-markdown` (last release
  2024-05) and `blackfriday-confluence` (2022-01, built on archived blackfriday) were both
  considered and declined.
- Transport: stdio only. No SSE, no streamable HTTP. Every critical advisory in the audited
  alternative was in its HTTP transport.
- Runtime: Docker Compose. Container is `--read-only`, non-root, `cap_drop: ALL`,
  `no-new-privileges`, no volume mounts.
- Logs go to **stderr only**. stdout is the MCP protocol channel; anything written there corrupts
  the session.
- Credential masking is unconditional at every log level. It is not a level or a flag.

## Architecture

Three packages. `core` owns everything generic; product modules own only endpoint knowledge.

```
cmd/atlassian-mcp-lite/main.go   wiring: load config, register modules, serve
internal/core/                   config, registry, client, fields, log
internal/jira/                   Module: 5 tools
internal/confluence/             Module: 5 tools
```

Modules register at compile time. Go plugins (`buildmode=plugin`) are excluded: they require CGO,
break the static binary and the scratch image, and demand exact toolchain matching.

The `domain` concept is generic. A module declares its domain string; core derives
`ATLAS_<DOMAIN>_READ`, `_WRITE`, `_DESTRUCTIVE` from it. Adding a module requires no core change.

A module must not import the MCP SDK, read the environment, construct an HTTP client, or write a
log line. All four live in core so a future module cannot forget them. A module bypassing
`core.Client` is the one thing a reviewer should reject outright.

## Tools

Ten tools. Action class in brackets.

| Tool | Class | Endpoint |
|---|---|---|
| `jira_search(jql, fields?, limit?)` | read | `POST /rest/api/3/search/jql` |
| `jira_get(key, fields?)` | read | `GET /rest/api/2/issue/{key}` |
| `jira_update(key, assignee?, fixVersion?, epic?, parent?, summary?, description?)` | write + destructive | `PUT /rest/api/2/issue/{key}` |
| `jira_transition(key, status)` | destructive | `GET`, `POST /rest/api/3/issue/{key}/transitions` |
| `jira_comment(key, body)` | write | `POST /rest/api/2/issue/{key}/comment` |
| `confluence_search(cql, limit?)` | read | `GET /wiki/rest/api/search` |
| `confluence_get_page(id, fields?)` | read | `GET /wiki/api/v2/pages/{id}` |
| `confluence_create_page(space, title, body, parent_id?)` | write | `POST /wiki/api/v2/pages` |
| `confluence_update_page(id, title?, body)` | destructive | `PUT /wiki/api/v2/pages/{id}` |
| `confluence_comment(page_id, body)` | write | `POST /wiki/api/v2/footer-comments` |

Action classes: `read` returns data; `write` is additive and reversible; `destructive` overwrites
or moves state that is hard to recover.

Confluence search uses the **v1** endpoint deliberately: the v2 API has no CQL search path
(verified against the v2 OpenAPI document, 151 paths, none matching `search`).

## Gating

Six booleans: `{jira, confluence} × {read, write, destructive}`.

Enforced by **absence**. At startup core walks each module's declarations, drops any whose action
classes are all disabled, and registers only the survivors. A disabled tool is not in `tools/list`
and is unknown to the dispatcher, because it was never registered. There is no filter to bypass.

A tool may span more than one class. `ToolDecl.Actions` lists every class it touches and the tool
registers when **any** of them is enabled — otherwise `jira_update`, which is both write and
destructive, would vanish whenever only one of the two was on.

**Registration must use the generic `mcp.AddTool`, never `(*mcp.Server).AddTool`.** The latter is
the SDK's low-level API and performs no input validation: its handler receives raw arguments, so a
property absent from the schema would still reach the handler and the gating below would be
decorative. The generic form resolves `tool.InputSchema` and validates every call against it before
the handler runs. Verified in go-sdk v1.7.0: `ToolHandler`'s documentation states that no input
validation is performed, and `setSchema` honours a preset `InputSchema` rather than replacing it
with one inferred from the Go type.

This is a direct response to GHSA-3r68 in the audited alternative, where `ENABLED_TOOLS` filtered
a fully-registered surface and a path around the filter was found.

`jira_update` spans two classes, declaring `Actions: []Action{ActionWrite, ActionDestructive}`. Its
input schema is assembled at startup from the enabled classes:

- `destructive=false` → schema properties are `key, assignee, fixVersion, epic, parent`
- `destructive=true` → the above plus `summary, description`

The schema **must** set `additionalProperties: false`. The SDK validates input against the schema
before the handler runs, but JSON Schema permits unknown properties by default — without this,
`description` would pass validation and unmarshal into the struct while absent from the advertised
schema. That is the bypass this design exists to prevent.

If `write=false` and `destructive=true`, `jira_update` registers with only `key`, `summary` and
`description`. If both are false it is not registered at all. This case has a registry-level test,
not merely a schema-level one: asserting on `Schema(caps)` alone cannot detect a tool the registry
dropped before the schema was built.

## Field selection

Read tools accept `fields`. Semantics:

- omitted → the tool's default set
- bare names (`["summary","status"]`) → exactly those, replacing the default
- `+name` → default plus that field
- `-name` → default minus that field
- mixing bare and prefixed names in one call → error, ambiguous intent

Defaults:

- `jira_search`: `key, summary, status, issuetype, assignee, fixVersions, parent, epic, updated`
- `jira_get`: the above plus `description`

`epic` is a **logical** field name. Jira has no field called `epic`: classic projects store the
link in a site-specific custom field named by `ATLAS_EPIC_FIELD_ID`. Every field list is translated
to upstream names before the request, and the custom field id is renamed back to `epic` in the
response, so the id never appears in a tool's input or output.
- `confluence_get_page`: `id, title, spaceId, version, body`

Rationale, measured on a representative epic: the full v3 JSON for one issue is ~1370 tokens;
the triage set is ~60. A 20-issue search lands near 800 tokens instead of tens of thousands. `description` is excluded from
search defaults because it is the largest field and is rarely wanted while scanning.

## Body formats — markdown at the tool boundary

Tools speak **markdown**. Atlassian speaks wiki markup, ADF, or XHTML storage. Neither Jira nor
Confluence accepts markdown over REST — verified: the Confluence v2 OpenAPI document contains zero
occurrences of "markdown", and its write representations are `["storage", "atlas_doc_format",
"wiki"]`; Jira accepts ADF on v3 and wiki markup on v2. So the server converts.

`internal/markup` owns all conversion. Three directions, each using a real parser where one
exists:

| Direction | Used by | Implementation |
|---|---|---|
| markdown → wiki | every write | `goldmark` parses to AST; a custom renderer walks it |
| wiki → markdown | Jira reads | line-oriented; wiki markup is line-based, so this stays hand-written |
| HTML → markdown | Confluence reads | `golang.org/x/net/html` tokeniser walk |

Writes therefore need **one** converter, because both products accept `representation: "wiki"` —
verified: `PageBodyWrite`, `CommentBodyWrite` and `BlogPostBodyWrite` all accept it, and Jira v2
takes a plain wiki-markup string. Atlassian performs wiki → storage / ADF server-side. **No ADF
or XHTML generator is written or vendored.**

Confluence reads request `body-format=view` rather than `storage`. Both are available, but `view`
is HTML with macros already rendered by Atlassian, so converting it needs no macro handling.
`body-format=wiki` does not exist for reads and returns HTTP 400 (verified).

### Supported markdown subset

Headings, bold, italic, inline code, fenced code blocks (with language), unordered and ordered
lists including nesting, links, tables, blockquotes, horizontal rules.

Anything outside the subset **passes through as literal text**. The converter never guesses and
never silently drops content: an unrecognised construct arriving intact and slightly wrong is
recoverable, whereas a dropped paragraph is not. Every construct has a round-trip test.

## Write allowlist

`ATLAS_WRITE_PROJECTS`, `ATLAS_WRITE_SPACES`.

- unset → unrestricted
- empty string → unrestricted (identical to unset; there is no third state)
- non-empty → strict allowlist; any other target is refused before the request is made

Reads are never restricted. Unrestricted is the default by project decision, against the
security recommendation on record: where issues are writable by other people and the MCP client
approves tool calls automatically, an injected instruction can drive a write to any project the
credential reaches. The shipped `.env.example` carries a commented-out allowlist so enabling it
is uncommenting a line.

## Logging

`ATLAS_LOG` ∈ `info` (default), `debug`.

- `info`: failures only, including the upstream error text, which is where Atlassian's useful
  diagnostics live.
- `debug`: adds method, path, status, duration and body sizes for every call.
- Response bodies are logged **only** for non-2xx responses. Successful bodies are business data
  and never logged.
- `Authorization`, `Cookie`, `Set-Cookie`, `Proxy-Authorization` are masked at all levels.
  Masking keeps the first and last 4 characters of the credential.
- The auth **scheme** is preserved only for `Authorization` and `Proxy-Authorization`, where
  knowing `Basic` from `Bearer` is useful and not secret. Cookies are masked whole: a cookie value
  reads `name=value; Path=/`, so cutting at the first space would leave the credential segment in
  the clear.
- Header masking alone is insufficient. The logger is constructed with the configured token and
  redacts it from **every** message, including the upstream error bodies that are deliberately
  logged. A secret shorter than 8 characters is not redacted, because it would match ordinary
  words.

## Configuration

One env file, mode 0600, outside any git repository. Compose reads it via `env_file`.

```
ATLAS_BASE_URL=https://your-domain.atlassian.net
ATLAS_EMAIL=you@example.com
ATLAS_TOKEN=

ATLAS_JIRA_READ=true
ATLAS_JIRA_WRITE=true
ATLAS_JIRA_DESTRUCTIVE=false
ATLAS_CONFLUENCE_READ=true
ATLAS_CONFLUENCE_WRITE=true
ATLAS_CONFLUENCE_DESTRUCTIVE=false

#ATLAS_WRITE_PROJECTS=PROJ
#ATLAS_WRITE_SPACES=DOCS

ATLAS_LOG=info
ATLAS_LIMIT_DEFAULT=20
ATLAS_LIMIT_MAX=50
```

Auth is HTTP Basic: `base64(email:token)`. API token scopes do not apply — Atlassian enforces
scopes only for OAuth, not Basic auth — so the credential carries the full authority of the
account that created it. Least privilege must therefore come from a dedicated service account
with reduced project permissions, not from the token.

## Error handling

Any response outside 200–299 is an `APIError` carrying the upstream message, because Atlassian's
own error text is the useful part of a failure. Checking only `>= 400` would let an unfollowed 3xx
be decoded as a successful result.

On 429 the `Retry-After` header is parsed — both the delay-seconds and HTTP-date forms — and
surfaced on the error, so a caller has a backoff signal rather than only a status code. An
unparseable value yields zero rather than failing the call.

Context cancellation propagates: every request is built with `http.NewRequestWithContext`, and a
cancelled context returns an error wrapping `context.Canceled`.

Malformed JSON in a 2xx body is an error, never a partially populated result.

## Paging

None. `limit` defaults to 20, hard cap 50. A result set exceeding the cap is truncated and the
tool result says so, instructing the caller to narrow the JQL or CQL. Cursors are deliberately
not implemented.

## Internal lookups

Callers pass human-meaningful values; the module resolves them. The behaviours below were
verified against a live Jira Cloud site during design:

- **Epic.** Classic (company-managed) projects — `style: classic`, `simplified: false` — expose
  both `parent` and an Epic Link custom field in `GET /rest/api/3/issue/{key}/editmeta`. The
  `epic` parameter maps to that custom field; `parent` maps to `parent`. Team-managed projects
  use `parent` for both.
- **fixVersion.** Name → id via `GET /rest/api/3/project/{key}/versions`.
- **assignee.** Email or display name → accountId via `GET /rest/api/3/user/search`.
- **transition.** Status name → transition id via `GET /rest/api/3/issue/{key}/transitions`.
  Transition ids are workflow-specific and must never be hardcoded.

Custom field ids are **not** hardcoded as shared constants. The Epic Link id differs between
Jira sites, so it is read from `ATLAS_EPIC_FIELD_ID`, defaulting to `customfield_10014` because
that is the most common value on Jira Cloud. Operators on other sites override it.

## Out of scope

Attachments in any form. No tool takes a filesystem path, and no tool reads local files. Twelve
of the thirty advisories in the audited alternative are arbitrary local file read through an
attachment path parameter; the class is absent here by construction rather than disabled by
configuration.

Also out: deletes, bulk operations, Confluence inline comments, blog posts, sprints, boards,
worklogs, Bitbucket, JSM.

## Residual risk, accepted

Removing the file-path parameter does not remove the exfiltration chain. The MCP client itself has
filesystem access: the agent can read a file with its own tools and paste the contents into
`jira_comment`. The container sandboxes this server, not the client driving it. Mitigations belong outside this
repository: a client-side deny rule covering the MCP client's own credential files, a service
account with reduced permissions, and requiring confirmation on destructive tools.

**Egress is not restricted by this project.** An earlier draft claimed the container limited
outbound traffic to the Atlassian host, and leaned on that to argue SSRF was unreachable. That was
wrong: `read_only`, `cap_drop` and `no-new-privileges` restrict the filesystem and privileges, not
network destinations, and nothing in the shipped Compose file or the documented `docker run`
invocation constrains where the process may connect.

What actually makes SSRF unreachable is narrower and still solid: the base URL is a configuration
constant validated at startup, never derived from tool input, and no tool accepts a URL. There is no
code path by which model output chooses a destination host.

Operators who want defence in depth on top of that must supply it themselves — a Docker network
with an egress proxy, or host firewall rules. The README documents this as an operator control
rather than a delivered feature.

What the container does buy: containment of any local-file-read bug in this codebase, since there
are no mounts and no writable filesystem.
