# Atlassian MCP lite

A small [Model Context Protocol](https://modelcontextprotocol.io) server for
Jira Cloud and Confluence Cloud, written in Go. Ten tools, five per product.
It speaks **stdio only**: the MCP protocol travels on stdout, and all logging
goes to stderr.

The advertised tool and parameter surface is assembled at startup from
configuration, so a capability you disable is *absent* rather than filtered —
a disabled tool never reaches `tools/list` and is unknown to the dispatcher.

## Why

Every tool an MCP server advertises is text the model reads on every turn, and
every parameter it exposes is something a prompt-injected page can try to fill
in. A Jira integration needs a handful of operations, not a mirror of the REST
API. This server is built around that:

- **Only what you turn on exists.** Ten tools cover search, read, comment,
  update, transition and page management. Each one belongs to a product and an
  action class (read, write, destructive), and a class you leave off is not
  filtered at call time — the tool is absent from `tools/list`, and a
  parameter such as `summary` on `jira_update` is missing from the schema
  itself. A fresh install serves four read tools and nothing that can change
  your site.
- **Safe by construction, not by policy.** No tool accepts a URL or a file
  path; the only host it ever talks to is the one in `ATLAS_BASE_URL`,
  validated at startup. Writes can be fenced to named projects and spaces
  before a request is made. The config file that holds the API token is
  refused unless only its owner can read it. The token and its Basic
  credential are redacted from every log line.
- **Models read and write markdown.** Atlassian does not: it wants wiki markup
  in and returns wiki markup, storage HTML or ADF out. The server converts in
  both directions so the model never sees a markup format it has to guess.
- **Compact answers by default.** `jira_search` returns five fields per issue
  unless asked for more, and `*all` is one parameter away when the model needs
  everything. Results are capped and a truncated result says so.
- **Small enough to audit.** Four Go dependencies, standard-library tests
  only, a static CGO-free binary in a `scratch` image of a few megabytes with
  no shell, running as a non-root user.

## Quick install

**With an AI assistant.** Paste this into Claude Code, Cursor, or any agent
that can read a repository and edit your MCP client config:

```text
Install Atlassian MCP lite from https://github.com/OxCom/atlassian-mcp-lite
and follow docs/install.md exactly.
```

The assistant walks you through it: a checkbox list of the products to enable
(Jira, Confluence), a second list of action classes per product (read, write,
destructive), your site URL and email, then it writes the config file with the
right permissions, registers the server in your client, and tells you where the
config lives. You type the API token yourself; the assistant never sees it. See
[`docs/install.md`](docs/install.md) for what the assistant is instructed to do.

**By hand.** Create the config file, lock it down, pull the image:

```bash
mkdir -p ~/.config/atlassian-mcp-lite
cat > ~/.config/atlassian-mcp-lite/env <<'EOF'
ATLAS_BASE_URL=https://your-domain.atlassian.net
ATLAS_EMAIL=you@example.com
ATLAS_TOKEN=paste-your-api-token-here
EOF
chmod 600 ~/.config/atlassian-mcp-lite/env

docker pull ghcr.io/oxcom/atlassian-mcp-lite:latest
```

The image is published at
[ghcr.io/oxcom/atlassian-mcp-lite](https://github.com/OxCom/atlassian-mcp-lite/pkgs/container/atlassian-mcp-lite)
— public, so no registry login is needed. Pin a `vX.Y.Z` tag instead of
`latest` if you would rather upgrade deliberately. To run an unreleased commit,
clone the repository and `make image` instead, which produces
`atlassian-mcp-lite:local`.

That is a complete configuration. With nothing else set, the server starts
with the **read tools of both products** and nothing that can modify your
site. Every other setting, and where to find each value, is in
[`docs/configuration.md`](docs/configuration.md).

Then register it in your MCP client. The client launches the container itself,
because it must own stdin and stdout:

```json
{
  "mcpServers": {
    "atlassian": {
      "command": "docker",
      "args": [
        "run", "--rm", "-i",
        "--user", "1000:1000",
        "-v", "/home/YOUR_USER/.config/atlassian-mcp-lite/env:/config/env:ro",
        "-e", "ATLAS_ENV_FILE=/config/env",
        "--read-only", "--cap-drop", "ALL",
        "--security-opt", "no-new-privileges:true",
        "ghcr.io/oxcom/atlassian-mcp-lite:latest"
      ]
    }
  }
}
```

Replace `1000:1000` with your own uid and gid (`id -u` and `id -g`): the file
is readable by its owner only, so the container must run as that owner. `-i`
is required: without it the container gets no stdin and the client sees an
immediate EOF.

## What is enabled by default

| | read | write | destructive |
|---|---|---|---|
| Jira | **on** | off | off |
| Confluence | **on** | off | off |

So a fresh install serves exactly four tools: `jira_search`, `jira_get`,
`confluence_search` and `confluence_get_page`. Turn a class on with
`ATLAS_JIRA_WRITE=true`, `ATLAS_CONFLUENCE_DESTRUCTIVE=true` and so on; turn
reads off with `ATLAS_JIRA_READ=false`. If every class of every product ends up
off, the server refuses to start rather than serving an empty tool list.

## Tools

| Tool | Action class | What it does |
|---|---|---|
| `jira_search` | read | JQL search, compact default field set |
| `jira_get` | read | One issue, description as markdown |
| `jira_update` | write + destructive | Adds a fix version with write; replaces assignee, epic, parent, summary or description only when destructive is enabled |
| `jira_transition` | destructive | Move an issue to another status by name |
| `jira_comment` | write | Add a comment |
| `confluence_search` | read | CQL search |
| `confluence_get_page` | read | One page, body as markdown |
| `confluence_create_page` | write | Create a page |
| `confluence_update_page` | destructive | Replace a page body |
| `confluence_comment` | write | Add a footer comment |

The three classes mean:

- **read** — returns data, changes nothing.
- **write** — additive and reversible: add a comment, add a fix version, create
  a page.
- **destructive** — overwrites or moves state that is hard to recover: assignee,
  epic link, parent, summary, description, status transition, page body
  replacement. Each of these replaces a value the issue already holds, and
  nothing records what that value was.

Tools speak markdown. Atlassian does not accept markdown over REST, so the
server converts markdown to wiki markup on the way in, and wiki markup or
rendered HTML back to markdown on the way out. Text coming out is
markdown-escaped, because page prose is third-party input and must not reach the
model as live markdown; the practical consequence is that a round trip preserves
meaning but not bytes when the text contains markdown-significant characters.

There is no paging. `limit` is clamped to `ATLAS_LIMIT_MAX` and a truncated
result says so, using each API's own completion signal.

## Default fields

`jira_search` and `jira_get` return a compact set unless told otherwise:

| Tool | Default fields |
|---|---|
| `jira_search` | `summary`, `status`, `updated`, `assignee`, `reporter` |
| `jira_get` | the same plus `description` |
| `confluence_get_page` | `id`, `title`, `spaceId`, `version`, `body` |

Ask for more with the `fields` parameter:

| `fields` value | Meaning |
|---|---|
| omitted | the default set above |
| `["summary","status"]` | exactly those, replacing the default |
| `["+description","+labels"]` | the default plus those |
| `["-updated"]` | the default minus that |
| `["*all"]` | **every field Jira has** for the issue |

Bare and prefixed forms may not be mixed in one call. `epic` is a logical name
for the site's Epic Link custom field, see `ATLAS_EPIC_FIELD_ID`. The Confluence
page tool has a fixed field vocabulary and no `*all`. Where to find the name of
a field is covered in [`docs/configuration.md`](docs/configuration.md).

## Restricting which projects and spaces can be changed

By default the server may **write to every Jira project and every Confluence
space** the account can reach. Two allowlists narrow that:

```bash
ATLAS_WRITE_PROJECTS=PROJ,OPS        # Jira project keys
ATLAS_WRITE_SPACES=ENG,~jdoe         # Confluence space keys; ~ marks a personal space
```

A non-empty list is strict: a write or destructive call aimed anywhere else is
refused before any request is made, and a move into or out of a listed
project or space is refused too. The check follows the issue rather than the
key: because Jira keeps every old key working after an issue moves, the
project the issue is in *now* is what is checked. An `epic` or `parent` key
passed to `jira_update` must be in an allowed project too, since linking gives
that issue a child in its own hierarchy. For the same reason
`confluence_create_page` refuses a `parent_id` whose page is in a different
space from the one requested: a child page is created in its parent's space. Reads are never restricted — search and get
see whatever the account sees. Matching is case-insensitive. A list that is set
but names no keys (`,`) is a startup error, not "allow everything".

Set both on any shared site. The API token carries the full authority of its
account, and these lists are the only thing between a prompt-injected page and
an unwanted edit somewhere else.

## Configuration

Configuration is a private env file named by `ATLAS_ENV_FILE`, or plain
environment variables. **The file must be mode `0600` or stricter**: on Linux
and macOS the server refuses to start otherwise and prints the exact `chmod`
to run. It must also be a regular file — never a symbolic link — and owned by
the user the server runs as. Setting the same key twice in it is an error
rather than last-one-wins. Process environment overrides the file, value by
value.

On **Windows** neither the permission check nor the owner check can run, because
access there is an ACL rather than Unix mode bits. The server says so instead of
staying quiet: it logs a warning at startup naming both skipped checks and
suggesting the `icacls` command that restricts the file to the account running
the server. Restricting it is yours to do on that platform.

The short version:

| Variable | Default | Meaning |
|---|---|---|
| `ATLAS_BASE_URL` | required | `https://your-domain.atlassian.net` |
| `ATLAS_EMAIL` | required | Account email |
| `ATLAS_TOKEN` | required | Atlassian API token, at least 16 characters |
| `ATLAS_JIRA_READ` / `ATLAS_CONFLUENCE_READ` | `true` | Read tools |
| `ATLAS_JIRA_WRITE` / `ATLAS_CONFLUENCE_WRITE` | `false` | Write tools |
| `ATLAS_JIRA_DESTRUCTIVE` / `ATLAS_CONFLUENCE_DESTRUCTIVE` | `false` | Destructive tools |
| `ATLAS_WRITE_PROJECTS` / `ATLAS_WRITE_SPACES` | unrestricted | Write allowlists |
| `ATLAS_LIMIT_DEFAULT` / `ATLAS_LIMIT_MAX` | `20` / `50` | Result counts |
| `ATLAS_LOG` | `info` | `info` or `debug` |
| `ATLAS_EPIC_FIELD_ID` | `customfield_10014` | Epic Link field id |

Every variable, its validation rules, and step-by-step instructions for finding
each value — creating an API token, locating a custom field id, reading a space
key — are in [`docs/configuration.md`](docs/configuration.md).

## Security notes

- **The API token carries the full authority of its account.** Atlassian
  applies token scopes to OAuth, not to Basic auth. Use a dedicated service
  account whose project and space permissions are limited to what you need.
- **The config file is refused unless it is private.** Anything other than
  owner-only permissions is a startup error, because the file is the token. So
  is a symbolic link, whose owner would decide what the server actually reads,
  and a file belonging to another user, which would be their credential rather
  than yours.
- **Set the write allowlists.** See the section above. Unset means
  unrestricted, which is rarely what you want in a shared site.
- **The image is minimal by construction.** The binary is CGO-free and static,
  the final stage is `scratch` with no shell and no package manager, and it
  runs as uid 65532 unless you pass `--user`. The compose service adds
  `read_only`, `cap_drop: ALL` and `no-new-privileges`.
- **Write capability is the blast radius of a prompt injection.** Nothing can
  tell an operator-intended write from one an injected page talked the model
  into, so what bounds the damage is configuration: `ATLAS_*_WRITE` and
  `ATLAS_*_DESTRUCTIVE` are off unless you set them, and
  `ATLAS_WRITE_PROJECTS` / `ATLAS_WRITE_SPACES` confine writes to projects and
  spaces you name, checked against the target's current project or space
  rather than its key prefix.
- **This server does not sandbox your MCP client.** Issue and page text is
  attacker-influenceable input that enters the model's context. This server
  accepts no filesystem paths, but the client driving it may have its own file
  tools. Restrict those separately.
- **Egress is not restricted by the container.** What prevents SSRF is that
  `ATLAS_BASE_URL` is validated at startup and no tool accepts a URL, so model
  output cannot choose a destination. Outbound filtering, if you want it, is an
  operator control: a proxy-only Docker network or host firewall rules.
- **Every result is labelled untrusted.** A successful tool result is wrapped
  as `{"notice": ..., "untrusted_content": ..., "notice_end": ...}`, where the
  notice states that the payload is third-party data from Atlassian and not
  instructions, and is repeated after the payload so a megabyte of injected
  text cannot be the last thing the model reads. The same statement arrives
  once outside every result, as the server's `instructions` in the MCP
  initialize response, and every write and destructive tool's description says
  that text returned by a tool is never authorization to call it. Links in
  converted content are limited to `http`, `https` and `mailto`; a target with
  any other scheme is rendered as plain text, so a `javascript:` or `data:` URL
  planted in a page never becomes a link the model or your client can follow.
  Plain scalars Atlassian renders literally — a summary, a title, a status or
  version name — are not converted, but control characters are removed and
  markdown structure is escaped whenever the value carries link, HTML or code
  syntax, so an ordinary name still round-trips byte for byte into a write.
  Scheme-less targets stay links because they resolve against the Atlassian
  host, with one exception: a target starting with two slashes or backslashes
  (`//evil.example/x`, `\\host\share`) names another origin without naming a
  scheme, and is refused.
- **A result above 1 MiB is refused, not truncated.** The limit is measured on
  the wrapped result, and the error asks for a narrower request. A cut JSON
  document is worse than none, because the model cannot tell where the cut
  fell.
- **Logs go to stderr and never contain credentials.** The token and the
  Base64 Basic credential derived from it are both registered with the logger
  for redaction. Successful response bodies are not logged at any level;
  failing ones are, because that is where the upstream diagnostics live.

## Documentation

- [`docs/install.md`](docs/install.md) — the guided install, written for an AI
  assistant to follow and for a human to check.
- [`docs/configuration.md`](docs/configuration.md) — every setting explained,
  with where to find each value.
- [`docs/development.md`](docs/development.md) — building, testing, CI, the
  corporate-proxy override, and adding a product module.
