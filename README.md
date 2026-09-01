# atlassian-mcp-lite

A small [Model Context Protocol](https://modelcontextprotocol.io) server for
Jira Cloud and Confluence Cloud, written in Go. Ten tools, five per product.
It speaks **stdio only**: the MCP protocol travels on stdout, and all logging
goes to stderr.

The advertised tool and parameter surface is assembled at startup from
configuration, so a capability you disable is *absent* rather than filtered —
a disabled tool never reaches `tools/list` and is unknown to the dispatcher.

## Why

Existing Atlassian MCP servers expose large tool surfaces to serve a handful of
real operations. This one exposes five Jira tools and five Confluence tools,
accepts no filesystem paths anywhere, and talks to exactly one Atlassian site,
fixed by configuration.

## Tools

| Tool | Action class | What it does |
|---|---|---|
| `jira_search` | read | JQL search, compact default field set |
| `jira_get` | read | One issue, description as markdown |
| `jira_update` | write + destructive | Assignee, fix version, epic, parent; summary and description only when destructive is enabled |
| `jira_transition` | destructive | Move an issue to another status by name |
| `jira_comment` | write | Add a comment |
| `confluence_search` | read | CQL search |
| `confluence_get_page` | read | One page, body as markdown |
| `confluence_create_page` | write | Create a page |
| `confluence_update_page` | destructive | Replace a page body |
| `confluence_comment` | write | Add a footer comment |

Tools speak markdown. Atlassian does not accept markdown over REST, so the
server converts markdown to wiki markup on the way in, and wiki markup or
rendered HTML back to markdown on the way out.

There is no paging. `limit` is clamped to `ATLAS_LIMIT_MAX` and a truncated
result says so, using each API's own completion signal.

## Capabilities

Six switches, one per domain and action class:

```
ATLAS_JIRA_READ / ATLAS_JIRA_WRITE / ATLAS_JIRA_DESTRUCTIVE
ATLAS_CONFLUENCE_READ / ATLAS_CONFLUENCE_WRITE / ATLAS_CONFLUENCE_DESTRUCTIVE
```

The three classes mean:

- **read** — returns data, changes nothing.
- **write** — additive and reversible: comment, assign, set a field, create a page.
- **destructive** — overwrites or moves state that is hard to recover: summary,
  description, status transition, page body replacement.

A tool is registered when at least one of its declared classes is enabled. A
domain with no capability at all contributes no tools. If nothing is enabled
anywhere, the server refuses to start rather than serving an empty tool list.

`jira_update` spans two classes: its input schema is built at startup, so with
`ATLAS_JIRA_DESTRUCTIVE=false` the `summary` and `description` properties do
not exist in the schema, and every tool schema rejects unknown properties.

## Configuration

Configuration is environment variables only — no config file, no command-line
flags. Put them in a private env file, keep it out of the repository, and point
`ATLAS_ENV_FILE` at it.

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `ATLAS_BASE_URL` | yes | — | Site origin, e.g. `https://your-domain.atlassian.net`. Origin only: no path, query or fragment, no embedded credentials. `https` is required except for loopback hosts. A trailing `/` is trimmed. |
| `ATLAS_EMAIL` | yes | — | Atlassian account email; the user half of the HTTP Basic credential. Must be a bare address with no display name and no colon. |
| `ATLAS_TOKEN` | yes | — | Atlassian API token. Must contain no whitespace and no control characters. Never logged, never echoed in an error. |
| `ATLAS_JIRA_READ` | no | `false` | Enable the Jira read tools. |
| `ATLAS_JIRA_WRITE` | no | `false` | Enable the Jira write tools. |
| `ATLAS_JIRA_DESTRUCTIVE` | no | `false` | Enable the Jira destructive tools and `jira_update`'s destructive fields. |
| `ATLAS_CONFLUENCE_READ` | no | `false` | Enable the Confluence read tools. |
| `ATLAS_CONFLUENCE_WRITE` | no | `false` | Enable the Confluence write tools. |
| `ATLAS_CONFLUENCE_DESTRUCTIVE` | no | `false` | Enable `confluence_update_page`. |
| `ATLAS_WRITE_PROJECTS` | no | unset (unrestricted) | Comma-separated Jira project keys. Non-empty means a strict allowlist for writes; reads are never restricted. Matched case-insensitively. A value that is set but yields no keys is a startup error, not "allow everything". |
| `ATLAS_WRITE_SPACES` | no | unset (unrestricted) | Comma-separated Confluence space keys, same rules. A leading `~` is permitted, for personal spaces. |
| `ATLAS_LOG` | no | `info` | `info` or `debug`. Any other value fails at startup. |
| `ATLAS_LIMIT_DEFAULT` | no | `20` | Result count used when a call omits `limit`. Must be >= 1 and <= `ATLAS_LIMIT_MAX`. |
| `ATLAS_LIMIT_MAX` | no | `50` | Hard cap on `limit`. Must be >= 1 and <= 1000. |
| `ATLAS_EPIC_FIELD_ID` | no | `customfield_10014` | The Epic Link custom field id for your site. |

Notes that matter in practice:

- The six capability variables are **derived from the domain names**, not
  hard-coded: a module registered as `foo` reads `ATLAS_FOO_READ`,
  `ATLAS_FOO_WRITE` and `ATLAS_FOO_DESTRUCTIVE`. Today the domains are `jira`
  and `confluence`.
- Booleans accept `1/true/yes/on` and `0/false/no/off`, case-insensitive.
  Unset is false. **Anything else is a startup error** — a typo such as `ture`
  must not quietly disable a capability the operator believes is on.
- An allowlist that is set but contains no keys (`,` or ` , `) is an error, not
  a silent "allow everything". Allowlists never restrict reads.
- Numeric variables that are set but unparsable are errors, not fallbacks:
  `20x` does not silently become `20`.

Every one of these is validated at startup by `internal/core/config.go`, which
is the single source of truth. If this table and that file disagree, the file
is right.

## Field selection

Read tools take a `fields` array:

| Value | Meaning |
|---|---|
| omitted | the tool's default set |
| `["summary","status"]` | exactly those, replacing the default |
| `["+description"]` | the default plus `description` |
| `["-updated"]` | the default minus `updated` |

Bare and prefixed forms may not be mixed in one call.

## Running

```bash
mkdir -p ~/.config/atlassian-mcp-lite
cat > ~/.config/atlassian-mcp-lite/env <<'EOF'
ATLAS_BASE_URL=https://your-domain.atlassian.net
ATLAS_EMAIL=you@example.com
ATLAS_TOKEN=

# At least one capability must be enabled, or the server exits with
# "no tools enabled". Everything is off by default.
ATLAS_JIRA_READ=true
ATLAS_CONFLUENCE_READ=true
EOF
chmod 600 ~/.config/atlassian-mcp-lite/env

make image
```

`make image` is the build command to use. It passes `compose.yaml`,
`compose.dev.yaml` and — when it exists — `compose.override.yaml`, which is
what the corporate-proxy setup below relies on. Plain `docker compose build`
works only when you have no `compose.override.yaml`: Compose auto-loads that
file for the default `compose.yaml`, and it defines dev-only services, so the
project fails to validate.

`compose.yaml` exists to build the image and to run a quick manual smoke test:

```bash
ATLAS_ENV_FILE=~/.config/atlassian-mcp-lite/env docker compose run --rm atlassian-mcp-lite
```

That is not how you use it day to day. An MCP client launches the container
itself, because the client must own stdin and stdout:

```json
{
  "mcpServers": {
    "atlassian": {
      "command": "docker",
      "args": [
        "run", "--rm", "-i",
        "--env-file", "/home/YOUR_USER/.config/atlassian-mcp-lite/env",
        "--read-only", "--cap-drop", "ALL",
        "--security-opt", "no-new-privileges:true",
        "atlassian-mcp-lite:local"
      ]
    }
  }
}
```

`-i` is required: without it the container gets no stdin and the client sees an
immediate EOF.

For development, skip the container:

```bash
go build ./cmd/atlassian-mcp-lite
set -a && . ~/.config/atlassian-mcp-lite/env && set +a
./atlassian-mcp-lite
```

## Security notes

- **The API token carries the full authority of its account.** Atlassian
  applies token scopes to OAuth, not to Basic auth. Use a dedicated service
  account whose project and space permissions are limited to what you need.
- **Set the write allowlist.** `ATLAS_WRITE_PROJECTS` and `ATLAS_WRITE_SPACES`
  refuse writes to anything not listed, before a request is made. Unset means
  unrestricted, which is rarely what you want in a shared site.
- **The image is minimal by construction.** The binary is CGO-free and static,
  the final stage is `scratch` with no shell and no package manager, and it
  runs as uid 65532. The compose service adds `read_only`, `cap_drop: ALL` and
  `no-new-privileges`.
- **This server does not sandbox your MCP client.** Issue and page text is
  attacker-influenceable input that enters the model's context. This server
  accepts no filesystem paths, but the client driving it may have its own file
  tools. Restrict those separately.
- **Egress is not restricted by the container.** `read_only`, `cap_drop` and
  `no-new-privileges` constrain the filesystem and privileges, not network
  destinations. What prevents SSRF is that `ATLAS_BASE_URL` is validated at
  startup and no tool accepts a URL, so model output cannot choose a
  destination. If you want outbound filtering as well, put the container on a
  Docker network whose only route out is a proxy that allows your Atlassian
  host, or apply host firewall rules. That is an operator control, not
  something this project delivers.
- **Logs go to stderr and never contain credentials.** The token and the
  Base64 Basic credential derived from it are both registered with the logger
  for redaction, because an upstream error body can echo either back.
  Successful response bodies are not logged at any level; failing ones are,
  because that is where the upstream diagnostics live.

## Development

Every tool runs in a pinned container; nothing but Docker is needed on the
host. Versions live in `compose.dev.yaml` and the `Makefile` is a thin wrapper
over it.

```bash
make check    # security, lint, test — what CI runs on a branch
make test     # go test ./... -race -covermode=atomic
make lint     # golangci-lint
make security # gitleaks, trufflehog, govulncheck, gosec
make build    # binary into ./bin
make image    # production container image
```

Tests use the standard library `testing` package only — no assertion library.
Fakes are `net/http/httptest` servers, and no test contacts a real Atlassian
host.

CI (`.github/workflows/ci.yml`) is a strict chain: security → lint → test →
build → release. Branches and pull requests stop after test; `build` and
`release` run on `v*` tags only.

### Behind a corporate proxy

If your network intercepts TLS or your Go modules come from JFrog Artifactory:

```bash
cp compose.override.yaml.example compose.override.yaml
# edit: set the Artifactory URL and the path to your company CA
```

The override is gitignored. It supplies `GOPROXY`, the CA certificate and your
Artifactory credentials to the dev containers, and passes the CA and
credentials to the image build as **BuildKit secrets**, so neither is written
into a layer or visible in `docker history`. Both mounts are optional — a plain
`docker build` with no secrets behaves exactly as it does off-network — and the
CA is additionally guarded with `if [ -s ... ]`, because it is the one the build
actively processes. The CA is never installed: it is concatenated with the build
image's roots into a temporary bundle named through `SSL_CERT_FILE` and deleted
in the same layer, and the runtime trust store is copied from a pristine image,
so an office-network build cannot ship an interception CA. Everything in the override is optional and a machine
outside the office needs no override file at all — the defaults are the public
Go proxy and checksum database.

One caveat worth knowing, in both directions. Compose auto-loads
`compose.override.yaml` for the default `compose.yaml`, so once you create the
override, a bare `docker compose build` picks it up — and fails, because the
override also names dev-only services that `compose.yaml` does not define. The
Makefile passes `-f`, which disables auto-loading, so it adds the override
explicitly: use `make image`. Conversely, invoking `docker compose -f
compose.yaml ...` by hand DOES skip your proxy settings, because the explicit
`-f` suppresses the auto-load.

### Adding a product module

Create `internal/<name>`, implement `core.Module`, and register it in
`cmd/atlassian-mcp-lite/main.go`. Capability variables
`ATLAS_<NAME>_READ|WRITE|DESTRUCTIVE` are derived from the domain name, so
`internal/core` needs no change. A module must not import the MCP SDK, read the
environment, build an HTTP client, or log — core owns all of that, so masking,
gating and allowlisting cannot be forgotten in a module.

## Documentation

`docs/plan/SPEC.md` is the agreed specification and
`docs/plan/2026-09-01-atlassian-mcp-lite.md` the task-by-task build.

## License

MIT. See `LICENSE`.
