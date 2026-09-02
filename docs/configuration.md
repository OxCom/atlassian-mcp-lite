# Configuration

Atlassian MCP lite reads its settings from one private env file, or from plain
environment variables. This page explains every setting, what the server does
with it, and where to find the value on your Atlassian site.

## The config file

```text
~/.config/atlassian-mcp-lite/env
```

That is the conventional location; any path works. Point `ATLAS_ENV_FILE` at it
and the server reads it at startup. The format is one `KEY=VALUE` per line,
blank lines and `#` comments ignored, an optional `export ` prefix, and values
optionally wrapped in single or double quotes. Nothing is interpolated.

A minimal, complete file:

```bash
ATLAS_BASE_URL=https://your-domain.atlassian.net
ATLAS_EMAIL=you@example.com
ATLAS_TOKEN=paste-your-api-token-here
```

That gives you the read tools of Jira and Confluence and nothing that can
change your site.

### Permissions: the file must be `0600`

The file holds an API token that carries the full authority of its account, so
the server refuses to start when anyone but the owner could read it. On Linux
and macOS the mode must be exactly `0600` (owner read and write) or `0400`
(owner read only), with no setuid, setgid or sticky bit. Anything else is a startup error that names the fix:

```text
atlassian-mcp-lite: configuration: ATLAS_ENV_FILE: /home/you/.config/atlassian-mcp-lite/env has permissions 0644, but it holds an API token and must be readable by its owner only; run: chmod 600 '/home/you/.config/atlassian-mcp-lite/env'
```

Windows does not express permissions as Unix mode bits, so the check is skipped
there. Keep the file inside your own profile directory.

### Precedence

A variable set in the process environment overrides the same variable in the
file, even when it is set to the empty string. That lets a container or a launch
script flip one switch without editing the file: `-e ATLAS_JIRA_WRITE=false`
wins over `ATLAS_JIRA_WRITE=true` in the file.

If `ATLAS_ENV_FILE` is unset, only the process environment is read. Docker's
`--env-file` flag is such a case: Docker reads the file on the host and injects
plain variables, so the server never sees the file and cannot check its mode.
Prefer mounting the file and setting `ATLAS_ENV_FILE`, as the README shows, so
the permission check runs.

## Settings

### Connection

| Variable | Required | Rules |
|---|---|---|
| `ATLAS_BASE_URL` | yes | Site origin only: `https://your-domain.atlassian.net`. No path, query, fragment or embedded credentials. `https` is required except for loopback hosts. A trailing `/` is trimmed. |
| `ATLAS_EMAIL` | yes | The email of the Atlassian account the token belongs to. A bare address: no display name, no colon. |
| `ATLAS_TOKEN` | yes | An Atlassian API token. No whitespace, no control characters. Never logged, never echoed in an error. |

**Where to find `ATLAS_BASE_URL`.** It is the address you use in the browser
for Jira or Confluence, up to and including `.atlassian.net`. Drop everything
after it: `https://acme.atlassian.net/jira/software/...` becomes
`https://acme.atlassian.net`. The same origin serves both products.

**Where to find `ATLAS_EMAIL`.** The address you sign in to Atlassian with. It
is shown under your avatar → *Account settings* → *Email*. Use the account that
owns the token; for a shared setup, create a dedicated service account with
only the project and space permissions you need.

**How to create `ATLAS_TOKEN`.**

1. Sign in and open <https://id.atlassian.com/manage-profile/security/api-tokens>.
2. Click **Create API token** (or **Create API token with scopes** — scopes
   apply to OAuth and are ignored for the Basic authentication this server
   uses, so either kind works).
3. Give it a label such as `atlassian-mcp-lite` and an expiry.
4. Click **Create**, then **Copy**. The token is shown once. Paste it into the
   config file yourself; do not paste it into a chat with an assistant.
5. If it ever leaks, revoke it on the same page and create a new one.

Atlassian's own guide: [Manage API tokens for your Atlassian account](https://support.atlassian.com/atlassian-account/docs/manage-api-tokens-for-your-atlassian-account/).

### Capabilities

One switch per product and action class. Read is on by default; write and
destructive are off.

| Variable | Default | Enables |
|---|---|---|
| `ATLAS_JIRA_READ` | `true` | `jira_search`, `jira_get` |
| `ATLAS_JIRA_WRITE` | `false` | `jira_comment`; `jira_update` for assignee, fix versions, epic, parent |
| `ATLAS_JIRA_DESTRUCTIVE` | `false` | `jira_transition`; `jira_update` for summary and description |
| `ATLAS_CONFLUENCE_READ` | `true` | `confluence_search`, `confluence_get_page` |
| `ATLAS_CONFLUENCE_WRITE` | `false` | `confluence_create_page`, `confluence_comment` |
| `ATLAS_CONFLUENCE_DESTRUCTIVE` | `false` | `confluence_update_page` |

The three classes:

- **read** returns data and changes nothing.
- **write** is additive and reversible: comment, assign, set a field, create a
  page.
- **destructive** overwrites or moves state that is hard to recover: summary,
  description, status transition, replacing a page body.

Booleans accept `1/true/yes/on` and `0/false/no/off`, case-insensitive. Any
other value is a startup error: a typo such as `ture` must not quietly disable a
capability you believe is on, or enable one you believe is off.

A tool is registered when at least one of its classes is enabled. A tool that is
not registered does not exist: it is absent from `tools/list` and unknown to the
dispatcher. `jira_update` builds its input schema from the enabled classes, so
with destructive off the `summary` and `description` properties are not in the
schema at all. If every class of every product is off, the server exits with
`no tools enabled`.

The variable names are derived from the product names, so a future product
`foo` would read `ATLAS_FOO_READ`, `ATLAS_FOO_WRITE` and `ATLAS_FOO_DESTRUCTIVE`
with the same defaults.

### Write allowlists

By default writes may go to every Jira project and every Confluence space the
account can reach. These narrow that.

| Variable | Default | Rules |
|---|---|---|
| `ATLAS_WRITE_PROJECTS` | unset (all projects) | Comma-separated Jira project keys, e.g. `PROJ,OPS`. Letters, digits and underscore. |
| `ATLAS_WRITE_SPACES` | unset (all spaces) | Comma-separated Confluence space keys, e.g. `ENG,~jdoe`. A leading `~` marks a personal space. |

A non-empty list is strict. A write or destructive call aimed at anything not
listed is refused before any request is made, and `jira_update` also refuses to
move an issue into or out of an unlisted project. Matching is case-insensitive.
Reads are never restricted. A value that is set but yields no keys, such as `,`,
is a startup error rather than "allow everything".

**Where to find a Jira project key.** It is the prefix of every issue key in
the project: `PROJ` in `PROJ-123`. It is also shown in Jira under *Projects* →
*View all projects*, in the **Key** column.

**Where to find a Confluence space key.** Open the space; the key is the
segment after `/spaces/` in the URL, `ENG` in
`https://acme.atlassian.net/wiki/spaces/ENG/overview`. It is also listed under
*Spaces* → *View all spaces*. Personal spaces have keys starting with `~`.

### Result limits

| Variable | Default | Rules |
|---|---|---|
| `ATLAS_LIMIT_DEFAULT` | `20` | Result count used when a call omits `limit`. At least 1, at most `ATLAS_LIMIT_MAX`. |
| `ATLAS_LIMIT_MAX` | `50` | Hard cap on `limit`. At least 1, at most 1000. |

A `limit` above the cap is clamped, and a truncated result says so using each
API's own completion signal. There is no paging; narrow the query instead. A
value that is set but not an integer is an error, not a fallback.

### Logging

| Variable | Default | Rules |
|---|---|---|
| `ATLAS_LOG` | `info` | `info` or `debug`. Anything else fails at startup. |

Logs go to stderr; stdout carries the MCP protocol. The token and the Basic
credential derived from it are redacted wherever they might appear. Successful
response bodies are never logged; failing ones are at `debug`.

### Field ids

| Variable | Default | Rules |
|---|---|---|
| `ATLAS_EPIC_FIELD_ID` | `customfield_10014` | The id of the Epic Link custom field on your site. A field id such as `customfield_10014`. |

Jira has no field named `epic`: company-managed projects store the link in a
custom field whose id differs between sites. The tools accept the logical name
`epic` in `fields` and in `jira_update`, and translate it to this id.

**How to find your Epic Link field id.**

1. Open `https://your-domain.atlassian.net/rest/api/3/field` in a browser
   while signed in. It returns every field as JSON.
2. Search the page for `"name":"Epic Link"` and read the `id` next to it.
3. If there is no such field, your projects are team-managed and use `parent`
   instead of an epic link; leave the default.

Alternatively, in Jira: ⚙ *Settings* → *Issues* → *Custom fields*, find *Epic
Link*, open its ⋯ menu → *View field information*; the id is the number at the
end of the URL, prefixed with `customfield_`.

## Fields returned by the read tools

### Defaults

| Tool | Default fields |
|---|---|
| `jira_search` | `summary`, `status`, `updated`, `assignee`, `reporter` |
| `jira_get` | the same plus `description`, rendered as markdown |
| `confluence_get_page` | `id`, `title`, `spaceId`, `version`, `body` |

The Jira defaults are the triage set: what the issue is, where it stands, when
it last moved, who owns it and who raised it. The description is left out of
search because it dominates the payload and, on the search endpoint, arrives in
a format this server does not convert.

### Choosing fields

`jira_search`, `jira_get` and `confluence_get_page` take a `fields` array (`confluence_search` takes only `cql` and `limit`):

| `fields` | Meaning |
|---|---|
| omitted | the default set |
| `["summary","status"]` | exactly those, replacing the default |
| `["+description","+labels"]` | the default plus those |
| `["-updated"]` | the default minus that |
| `["*all"]` | every field Jira has for the issue |
| `["*navigable"]` | every field Jira shows in issue navigator columns |

Bare and prefixed names may not be mixed in one call. Names are
case-insensitive for adding and removing. The Confluence page tool has a fixed
vocabulary (the five defaults) and does not accept `*all`.

Two things to know about `*all`:

- On `jira_search`, rich-text fields such as `description` and `environment`
  come back as Jira's raw document JSON, not markdown, because the search
  endpoint only speaks that format. Use `jira_get` for a readable description.
- The response can be large. Combine it with a small `limit`.

**Where to find a field name.** Standard names are lower camel case:
`summary`, `status`, `assignee`, `reporter`, `priority`, `labels`,
`fixVersions`, `components`, `issuetype`, `parent`, `created`, `updated`,
`duedate`, `resolution`. Custom fields are `customfield_<number>`. The complete
list for your site is at `https://your-domain.atlassian.net/rest/api/3/field`,
one object per field with its `id` and display `name`. Atlassian's reference:
[Jira Cloud REST API — Fields](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-fields/).

A field the caller asked for that Jira did not return for any issue is listed
under `unavailable_fields` in the result, because Jira silently ignores names it
does not know or the account cannot see.

## Validation

Every setting is validated at startup by `internal/core/config.go`; the env
file by `internal/core/envfile.go`. If this page and those files disagree, the
files are right.
