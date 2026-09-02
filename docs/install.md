# Guided install

This page is written for an AI coding assistant (Claude Code, Cursor, Codex,
or similar) that a person has asked to install Atlassian MCP lite. A person can
follow it too; the steps are the same. The assistant's job is to ask a few
structured questions, write one config file with the right permissions,
register the server in the MCP client, and say where the config lives.

The user typically starts with:

```text
Install Atlassian MCP lite from https://github.com/OxCom/atlassian-mcp-lite
and follow docs/install.md exactly.
```

## Rules for the assistant

- **Never ask for, accept, or store the API token.** The user creates it and
  pastes it into the config file themselves, in their own editor. If the user
  pastes a token into the chat, tell them to revoke it and create a new one.
- **Ask with visual prompts.** Use the client's structured question feature
  (in Claude Code, `AskUserQuestion` with `multiSelect: true`) so the choices
  render as checkbox lists, not as free text. Fall back to a numbered list only
  if the client has no such feature.
- **Recommend the safe default.** Read on, write and destructive off. Do not
  pre-select write or destructive.
- **Do not skip the permission step.** The server refuses a config file whose
  mode is not exactly `0600` (or `0400`). It also refuses a symbolic link and a
  file owned by anyone but the user the server runs as, so write the real file
  in the user's own home directory rather than linking to one elsewhere.
- **On Windows, tell the user to restrict the file themselves.** Neither the
  permission check nor the owner check runs there — access is an ACL, not Unix
  mode bits — so the server logs a warning at startup naming both skipped checks
  and suggesting an `icacls` command. Relay that command rather than treating a
  clean startup as proof the file is private.
- **Do not run the server against the real site to "test" it** until the user
  has filled in the token. A startup check with placeholders is fine, but read
  what it proves: it validates the config file's mode, ownership and required
  keys, and the placeholder token below is long enough to clear the minimum
  length, so the check exits quietly rather than reporting an error. A quiet
  exit therefore says the file is well formed, not that the credentials work.
  Only a real call to Atlassian tests those.

## Step 1 — Which products?

Ask one multi-select question:

> Which Atlassian products should this server expose?
>
> - [x] Jira
> - [x] Confluence

Both are pre-selected. A product left unselected gets every class turned off:
`ATLAS_<PRODUCT>_READ=false`.

## Step 2 — Which actions per product?

For every product selected, ask one multi-select question. Show all three
classes with their meaning; pre-select **read only**:

> What may the assistant do in **Jira**?
>
> - [x] read — search and view issues; changes nothing
> - [ ] write — comment and add a fix version; additive and reversible
> - [ ] destructive — reassign, change the epic or parent, change summary and description, move issues between statuses

> What may the assistant do in **Confluence**?
>
> - [x] read — search and view pages; changes nothing
> - [ ] write — create pages, add comments
> - [ ] destructive — replace a page body

If the user picks write or destructive for a product, ask one follow-up:

> Restrict **Jira** writes to specific projects? Enter project keys separated by
> commas (for example `PROJ,OPS`), or leave empty to allow every project the
> account can reach.

> Restrict **Confluence** writes to specific spaces? Enter space keys separated
> by commas (for example `ENG,~jdoe`), or leave empty to allow every space.

An allowlist also covers the other end of a link: an `epic` or `parent` given
to `jira_update` must be in a listed project, and a `parent_id` given to
`confluence_create_page` must be in the space the call names.

Explain in one line that the token has the full authority of its account and
that on a shared site the allowlist is the only thing stopping an unwanted edit
elsewhere.

## Step 3 — Connection details

Ask, as plain questions:

1. **Site URL** — "What is your Atlassian site URL? It looks like
   `https://your-domain.atlassian.net`. Only the origin is needed."
2. **Account email** — "Which account email will the server use? Prefer a
   dedicated service account for shared use."

Do not ask for the token. Tell the user how to create one:

> Create an API token at
> <https://id.atlassian.com/manage-profile/security/api-tokens>
> (**Create API token**, give it a label and an expiry, **Copy**). You will
> paste it into the config file yourself in the next step. The server requires
> at least 16 characters, which every real Atlassian token exceeds — a short
> value means something was truncated on the way in.

## Step 4 — Write the config file

Location, unless the user asks for another:

```text
~/.config/atlassian-mcp-lite/env
```

Write it with a placeholder token, then lock it down:

```bash
mkdir -p ~/.config/atlassian-mcp-lite
cat > ~/.config/atlassian-mcp-lite/env <<'EOF'
# Atlassian MCP lite — see https://github.com/OxCom/atlassian-mcp-lite/blob/master/docs/configuration.md
ATLAS_BASE_URL=https://your-domain.atlassian.net
ATLAS_EMAIL=you@example.com
ATLAS_TOKEN=REPLACE_WITH_YOUR_API_TOKEN

# Capabilities. Read is on by default; write and destructive are off.
# Only the lines that differ from the default need to be here.
ATLAS_JIRA_WRITE=false
ATLAS_JIRA_DESTRUCTIVE=false
ATLAS_CONFLUENCE_WRITE=false
ATLAS_CONFLUENCE_DESTRUCTIVE=false

# Write allowlists. Unset means every project / space the account can reach.
#ATLAS_WRITE_PROJECTS=PROJ,OPS
#ATLAS_WRITE_SPACES=ENG,~jdoe
EOF
chmod 600 ~/.config/atlassian-mcp-lite/env
```

Fill in the answers from Steps 1–3:

- a product not selected in Step 1 → `ATLAS_<PRODUCT>_READ=false`;
- each class selected in Step 2 → `ATLAS_<PRODUCT>_<CLASS>=true`;
- a non-empty allowlist → uncomment and fill `ATLAS_WRITE_PROJECTS` /
  `ATLAS_WRITE_SPACES`.

Then tell the user, verbatim:

> Open `~/.config/atlassian-mcp-lite/env` in your editor and replace
> `REPLACE_WITH_YOUR_API_TOKEN` with the token you created. Keep the file mode
> `0600`; the server refuses to start otherwise. Keep the key names uppercase as
> written, and do not set the same key twice in the file — a duplicate is an
> error, not last-one-wins.

## Step 5 — Pull the image

The released image is published to the GitHub Container Registry:

```bash
docker pull ghcr.io/oxcom/atlassian-mcp-lite:latest
```

Prefer a version tag over `latest` so an upgrade is a deliberate act rather
than something that happens on the next `docker pull`. The available tags are
listed at
<https://github.com/OxCom/atlassian-mcp-lite/pkgs/container/atlassian-mcp-lite>;
ask the user which they want, and default to the newest `vX.Y.Z` shown there:

```bash
docker pull ghcr.io/oxcom/atlassian-mcp-lite:vX.Y.Z
```

The registry path must be lowercase — `oxcom`, not `OxCom` — even though the
GitHub owner is capitalised. No login is needed for a public package. If the
pull fails with `denied`, the client is sending stale credentials for
`ghcr.io`; `docker logout ghcr.io` and retry.

Steps 6 and 7 below write `:latest`; substitute the tag chosen here.

**Building from source instead** is only needed to run an unreleased commit, or
where pulling from `ghcr.io` is blocked:

```bash
git clone https://github.com/OxCom/atlassian-mcp-lite
cd atlassian-mcp-lite
make image          # produces atlassian-mcp-lite:local
```

`make image` needs only Docker on the host. Behind a corporate proxy see
`docs/development.md`.

## Step 6 — Register the server in the MCP client

Add this server entry to the client's MCP configuration. Substitute the user's
home directory for `/home/YOUR_USER` and their uid and gid (`id -u`, `id -g`)
for `1000:1000`; the container must run as the file's owner because the file is
readable by its owner only. The last argument is the image tag from Step 5 —
`ghcr.io/oxcom/atlassian-mcp-lite:vX.Y.Z`, or `atlassian-mcp-lite:local` if it
was built from source.

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

Where that goes depends on the client:

| Client | Location |
|---|---|
| Claude Code | `claude mcp add-json atlassian '<the "atlassian" object above>'` or `.mcp.json` in the project |
| Claude Desktop | `claude_desktop_config.json` in the app's config directory |
| Cursor | `.cursor/mcp.json` in the project or `~/.cursor/mcp.json` |
| Other | the client's MCP server list; the shape above is the common one |

### Networks that intercept TLS

The image carries the public root certificates only. On a corporate network
that terminates and re-signs TLS, every request to Atlassian fails with:

```text
tls: failed to verify certificate: x509: certificate signed by unknown authority
```

The fix is to mount a trust bundle that contains **both** the public roots and
the company CA, and to name it through `SSL_CERT_FILE`, which Go reads directly
— two extra arguments in the entry above:

```json
        "-v", "/etc/ssl/certs/ca-certificates.crt:/etc/ssl/corp/bundle.crt:ro",
        "-e", "SSL_CERT_FILE=/etc/ssl/corp/bundle.crt",
```

On a Linux host whose CA is already installed system-wide,
`/etc/ssl/certs/ca-certificates.crt` is that bundle. Point `SSL_CERT_FILE` at
the company CA **alone** and it replaces the trust store instead of extending
it, so every host the proxy does not re-sign then fails to verify. Elsewhere,
concatenate the company CA onto a copy of the public roots and mount that.

This is separate from the build-time CA in Step 5 and in
`docs/development.md`: that one lets `go mod download` reach the module proxy,
and does nothing for the running container.

## Step 7 — Verify and hand over

Run the server once by hand so a configuration error is seen now, not inside
the client:

```bash
docker run --rm -i --user "$(id -u):$(id -g)" \
  -v ~/.config/atlassian-mcp-lite/env:/config/env:ro \
  -e ATLAS_ENV_FILE=/config/env \
  ghcr.io/oxcom/atlassian-mcp-lite:latest </dev/null
```

Add the CA arguments from Step 6 here too if the network intercepts TLS.

This exits quietly on EOF whether the token is the placeholder or a real one:
the placeholder clears the minimum token length, and nothing in startup calls
Atlassian. So a quiet exit checks the file, not the credentials. What it does
catch: a permission error prints the exact `chmod` to run; an error about
ownership means the uid passed to `--user` does not own the mounted file, and
`$(id -u):$(id -g)` above is the uid that does; a duplicate or lowercase key is
named in the error.

Credentials are proven only by the first read through the client — a wrong
token or email returns `401`, an unreachable or misspelled site fails to
resolve, and a trust-store problem gives the `x509` error above.

Finish with this note to the user, adjusted to what was chosen:

> **Installed.** Configuration lives in `~/.config/atlassian-mcp-lite/env`
> (mode 0600). Edit that file to enable or disable products, action classes,
> or the project and space allowlists; every setting is explained in
> <https://github.com/OxCom/atlassian-mcp-lite/blob/master/docs/configuration.md>.
> Restart the MCP client after changing it. Currently enabled: *Jira read,
> Confluence read* (list what was selected).
