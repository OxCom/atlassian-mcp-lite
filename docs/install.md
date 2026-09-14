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
> - [ ] write — create issues, comment, add a fix version, assign an issue, move issues between statuses; additive and reversible. A workflow move can be one-way and can fire notifications and automation
> - [ ] destructive — change the parent, change summary and description

> What may the assistant do in **Confluence**?
>
> - [x] read — search and view pages; changes nothing
> - [ ] write — create pages, add comments
> - [ ] destructive — replace a page body

For every product the user keeps read on, ask:

> Restrict what the assistant may **read** in **Jira** to specific projects?
> Enter project keys separated by commas (for example `DEV,PLATFORM,INFRA`), or
> leave empty to allow every project the account can reach.

> Restrict what the assistant may **read** in **Confluence** to specific spaces?
> Enter space keys separated by commas (for example `ENG,ARCHITECTURE`), or
> leave empty to allow every space.

If the user picks write or destructive for a product, ask one follow-up:

> Restrict **Jira** writes to specific projects? Enter project keys separated by
> commas (for example `PROJ,OPS`), or leave empty to allow every project the
> account can reach.

> Restrict **Confluence** writes to specific spaces? Enter space keys separated
> by commas (for example `ENG,~jdoe`), or leave empty to allow every space.

The read lists and the write lists are independent: answering one says nothing
about the other. A read list is enforced by the server on every read path — the
allowlist is ANDed onto whatever JQL or CQL is sent, and a single issue or page
is checked against the project or space it lives in *now*, not its key.

An allowlist also covers the other end of a link: a `parent` given
to `jira_update` must be in a listed project, and a `parent_id` given to
`confluence_create_page` must be in the space the call names.

Explain in one line that the token has the full authority of its account, that
on a shared site the allowlists are the only thing stopping an unwanted edit or
an unwanted disclosure elsewhere, and that the account's own Atlassian
permissions remain the primary boundary — a dedicated service account limited
to what is needed is still recommended.

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

Location, unless the user asks for another. Ask which OS the user is on rather
than assuming; the rest of this page writes the Linux path:

| OS | Config file |
|---|---|
| Linux | `~/.config/atlassian-mcp-lite/env` |
| macOS | `~/.config/atlassian-mcp-lite/env` |
| Windows | `%LOCALAPPDATA%\atlassian-mcp-lite\env` |

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

# Read allowlists. Unset or empty means every project / space the account can
# reach. Independent of the write lists below.
#ATLAS_READ_PROJECTS=DEV,PLATFORM,INFRA
#ATLAS_READ_SPACES=ENG,ARCHITECTURE

# Write allowlists. Unset means every project / space the account can reach.
#ATLAS_WRITE_PROJECTS=PROJ,OPS
#ATLAS_WRITE_SPACES=ENG,~jdoe
EOF
chmod 600 ~/.config/atlassian-mcp-lite/env
```

On **Windows** there is no `chmod`, and the server cannot check the file for
you — neither the mode check nor the owner check runs there. Write the file and
restrict it explicitly, then relay the result to the user:

```powershell
$Dir = "$env:LOCALAPPDATA\atlassian-mcp-lite"
New-Item -ItemType Directory -Force $Dir | Out-Null
$Env = "$Dir\env"

# Same contents as the Unix file above.
Set-Content -Path $Env -Encoding ascii -Value @'
ATLAS_BASE_URL=https://your-domain.atlassian.net
ATLAS_EMAIL=you@example.com
ATLAS_TOKEN=REPLACE_WITH_YOUR_API_TOKEN
'@

# Break inheritance, then grant the current user alone.
icacls $Env /inheritance:r /grant:r "$($env:USERNAME):(R,W)"
```

The server prints a startup warning on Windows naming both skipped checks and
suggesting that `icacls` line. Relay the warning; do not present a clean start
as proof the file is private.

Fill in the answers from Steps 1–3:

- a product not selected in Step 1 → `ATLAS_<PRODUCT>_READ=false`;
- each class selected in Step 2 → `ATLAS_<PRODUCT>_<CLASS>=true`;
- a non-empty read allowlist → uncomment and fill `ATLAS_READ_PROJECTS` /
  `ATLAS_READ_SPACES`;
- a non-empty write allowlist → uncomment and fill `ATLAS_WRITE_PROJECTS` /
  `ATLAS_WRITE_SPACES`.

Then tell the user, verbatim:

> Open `~/.config/atlassian-mcp-lite/env` in your editor and replace
> `REPLACE_WITH_YOUR_API_TOKEN` with the token you created. Keep the file mode
> `0600`; the server refuses to start otherwise. Keep the key names uppercase as
> written, and do not set the same key twice in the file — a duplicate is an
> error, not last-one-wins.

## Step 5 — Get the server: container image or native binary

Ask one single-select question before doing anything here:

> How should the server run?
>
> - ( ) Container image (recommended) — needs Docker; the image is minimal,
>       read-only, unprivileged, and upgrades are a `docker pull`.
> - ( ) Native binary — no Docker on the machine, or a client that cannot
>       launch containers. One static file, no runtime dependencies.

Both paths end at the same server. The difference is only what the MCP client
launches, and Step 6 has a config block for each.

### Option A — container image

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

### Option B — native binary

Every tagged release attaches a static, dependency-free binary per platform
plus one `SHA256SUMS` covering all of them. Browse them at
<https://github.com/OxCom/atlassian-mcp-lite/releases/latest>.

| Platform | Asset |
|---|---|
| Linux, Intel/AMD 64-bit | `atlassian-mcp-lite_linux_amd64` |
| Linux, ARM 64-bit | `atlassian-mcp-lite_linux_arm64` |
| macOS, Apple silicon | `atlassian-mcp-lite_darwin_arm64` |
| macOS, Intel | `atlassian-mcp-lite_darwin_amd64` |
| Windows, Intel/AMD 64-bit | `atlassian-mcp-lite_windows_amd64.exe` |
| checksums | `SHA256SUMS` |

Ask the user which platform they are on rather than guessing, then download,
**verify the checksum**, and install. Verification is not optional: the binary
is what holds the token at runtime.

Linux and macOS:

```bash
VER=vX.Y.Z          # the release tag
ASSET=atlassian-mcp-lite_linux_amd64      # from the table above
BASE=https://github.com/OxCom/atlassian-mcp-lite/releases/download/$VER

curl -fLO "$BASE/$ASSET"
curl -fLO "$BASE/SHA256SUMS"
sha256sum --ignore-missing -c SHA256SUMS   # macOS: shasum -a 256 --ignore-missing -c SHA256SUMS

mkdir -p ~/.local/bin
install -m 0755 "$ASSET" ~/.local/bin/atlassian-mcp-lite
```

On macOS the binary is unsigned and unnotarised, so Gatekeeper quarantines a
download from a browser. `curl` does not set the quarantine attribute, which is
why the commands above use it. If the user downloaded through a browser
instead, clear it explicitly:

```bash
xattr -d com.apple.quarantine ~/.local/bin/atlassian-mcp-lite
```

Windows (PowerShell):

```powershell
$Ver   = "vX.Y.Z"
$Asset = "atlassian-mcp-lite_windows_amd64.exe"
$Base  = "https://github.com/OxCom/atlassian-mcp-lite/releases/download/$Ver"

New-Item -ItemType Directory -Force "$env:LOCALAPPDATA\atlassian-mcp-lite" | Out-Null
Invoke-WebRequest "$Base/$Asset" -OutFile "$env:LOCALAPPDATA\atlassian-mcp-lite\atlassian-mcp-lite.exe"
Invoke-WebRequest "$Base/SHA256SUMS" -OutFile "$env:TEMP\SHA256SUMS"

# Compare the hash against the line for this asset in SHA256SUMS.
(Get-FileHash "$env:LOCALAPPDATA\atlassian-mcp-lite\atlassian-mcp-lite.exe" -Algorithm SHA256).Hash.ToLower()
Select-String -Path "$env:TEMP\SHA256SUMS" -Pattern $Asset
```

The two values must match. SmartScreen may warn on first run because the
executable is unsigned; the checksum is what establishes provenance here, not
the absence of a warning.

The binary reads its configuration from the environment, so the MCP client must
pass `ATLAS_ENV_FILE` — Step 6 does that. It opens no file other than that one
and speaks MCP on stdin and stdout.

### Option B, variant — the self-updating binary

Every release carries two sets of binaries. The table above is the default set,
which cannot modify itself. The second set is compiled with an updater built in:

| | Default | Self-updating variant |
|---|---|---|
| Asset name | `atlassian-mcp-lite_<goos>_<goarch>` | `atlassian-mcp-lite-selfupdate_<goos>_<goarch>` |
| Windows | `..._windows_amd64.exe` | `...-selfupdate_windows_amd64.exe` |
| Extra tool | none | `self_update` |
| How it is upgraded | download and verify a new asset | either that, or by calling `self_update` |

Only the variant can update itself. The default binary contains no updater code
at all, so there is nothing in it to enable. **The container image cannot
self-update either**, by design: a swap inside a container is undone by the next
redeploy. Images are upgraded with `docker pull`, as Option A describes.

Ask one multi-select question, in the same style as the action classes in
Step 2, and **do not pre-select it**:

> This is the optional extra capability. Leave it unchecked unless you
> specifically want it.
>
> - [ ] self-update — let the assistant replace this server's own binary with
>       the latest signed release

If the user leaves it unchecked — the recommended answer — install the default
asset from the table above and skip the rest of this section.

If the user checks it, say plainly what it means before installing anything: the
assistant decides what to call from the text in front of it, which includes issue
and page text written by anyone who can edit it on the site, so binary
replacement becomes an action reachable from that text. What bounds it is that
the tool is absent unless it is both this build and switched on, and that it
takes no arguments at all — no URL, no version, no path — so there is nothing in
the call for injected text to steer.

Then install the variant asset and add one line to the config file:

```bash
ATLAS_SELFUPDATE=true
```

Unset or `false`, the tool is not registered and the variant behaves exactly
like the default binary. `docs/configuration.md` covers the setting.

**Verify the download against both files.** The variant release publishes
`SHA256SUMS` and an `ed25519` detached signature `SHA256SUMS.sig` over it; check
the signature first, then the checksum, because a checksum file nobody signed
proves only that the download matches whatever was published:

```bash
VER=vX.Y.Z          # the release tag
ASSET=atlassian-mcp-lite-selfupdate_linux_amd64      # variant, from the table above
BASE=https://github.com/OxCom/atlassian-mcp-lite/releases/download/$VER

curl -fLO "$BASE/$ASSET"
curl -fLO "$BASE/SHA256SUMS"
curl -fLO "$BASE/SHA256SUMS.sig"

# Signature over the bytes of SHA256SUMS, checked with the project's release
# public key. That key is not a download: it is the ed25519 public key carried
# in the project's source, in internal/selfupdate/pubkey.go, which is the point
# — a key fetched from the same place as the release would prove nothing. To
# check by hand, wrap those raw 32 bytes in the DER header openssl expects:
{ printf '\x30\x2a\x30\x05\x06\x03\x2b\x65\x70\x03\x21\x00'
  echo 'aVWaPg3gb+dyQ2kUAmR2a470m0bG0CX/FcohYH6tnbw=' | base64 -d
} | openssl pkey -pubin -inform DER -out release-pubkey.pem

openssl pkeyutl -verify -pubin -inkey release-pubkey.pem \
  -rawin -in SHA256SUMS -sigfile SHA256SUMS.sig

sha256sum --ignore-missing -c SHA256SUMS   # macOS: shasum -a 256 --ignore-missing -c SHA256SUMS

mkdir -p ~/.local/bin
install -m 0755 "$ASSET" ~/.local/bin/atlassian-mcp-lite
```

The binary performs the same two checks on every update it makes: the signature
over `SHA256SUMS` is verified with a public key compiled into it, then the
downloaded asset is hashed and compared to the line for its own asset name. An
update that fails either check is refused, and nothing on disk changes.

**After a successful update, restart the server.** The tool stages the new
binary and reports `restart_required`; it does not restart itself, because the
MCP session is stateful and exiting would drop a session nobody asked to end.
The running process keeps serving the old code until the MCP client launches it
again. The previous binary is kept next to the new one as
`<binary>.<version>.bak` — for example
`~/.local/bin/atlassian-mcp-lite.v0.2.1.bak` — so a build that does not run on
this machine can be put back by moving that file over the installed one. Delete
it once the new version has proved itself.

If the running version is already the latest, nothing is downloaded and the tool
reports that it is current.

### Building from source instead

Only needed to run an unreleased commit, or where both `ghcr.io` and the
release assets are blocked:

```bash
git clone https://github.com/OxCom/atlassian-mcp-lite
cd atlassian-mcp-lite
make image          # produces the image atlassian-mcp-lite:local
make dist           # produces every release binary in ./dist, with SHA256SUMS
```

Both need only Docker on the host. Behind a corporate proxy see
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

**If Step 5 chose the native binary**, the client launches the binary directly
and passes the config path in the environment. There is no bind mount and no
uid to match — the process already runs as the user:

```json
{
  "mcpServers": {
    "atlassian": {
      "command": "/home/YOUR_USER/.local/bin/atlassian-mcp-lite",
      "env": {
        "ATLAS_ENV_FILE": "/home/YOUR_USER/.config/atlassian-mcp-lite/env"
      }
    }
  }
}
```

On Windows use the installed path and forward slashes or doubled backslashes,
both of which JSON accepts:

```json
{
  "mcpServers": {
    "atlassian": {
      "command": "C:/Users/YOUR_USER/AppData/Local/atlassian-mcp-lite/atlassian-mcp-lite.exe",
      "env": {
        "ATLAS_ENV_FILE": "C:/Users/YOUR_USER/AppData/Local/atlassian-mcp-lite/env"
      }
    }
  }
}
```

`command` must be an absolute path. A bare name works only if the directory is
on the `PATH` of the process that launches the client, which for a desktop app
is often not the shell's `PATH`.

Do not put `ATLAS_TOKEN` in this `env` block. The MCP client config is a
world-readable file in most clients, and it is not the config file whose
permissions the server checks.

Where that goes depends on the client. Every one of these takes the same
`mcpServers` object; only the file differs.

| Client | Location |
|---|---|
| Claude Code | `claude mcp add-json atlassian '<the "atlassian" object above>'`, or `.mcp.json` in the project root for a shared checkout, or `~/.claude.json` for every project |
| Claude Desktop — macOS | `~/Library/Application Support/Claude/claude_desktop_config.json` |
| Claude Desktop — Windows | `%APPDATA%\Claude\claude_desktop_config.json` |
| Claude Desktop — Linux | `~/.config/Claude/claude_desktop_config.json` |
| Cursor | `.cursor/mcp.json` in the project, or `~/.cursor/mcp.json` globally |
| VS Code / GitHub Copilot | `.vscode/mcp.json` in the workspace, or the user `mcp.json` via **MCP: Open User Configuration**. Copilot agent mode is stdio-only, which both options above are |
| Windsurf | `~/.codeium/windsurf/mcp_config.json` |
| Zed | `settings.json`, under `context_servers` rather than `mcpServers` |
| Other | the client's MCP server list; the shape above is the common one |

Add the entry, do not replace the file: these files usually hold other servers
already. Restart the client afterwards — none of them reload MCP config while
running.

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

If Step 5 chose the native binary, run that instead — same check, no mount:

```bash
ATLAS_ENV_FILE=~/.config/atlassian-mcp-lite/env \
  ~/.local/bin/atlassian-mcp-lite </dev/null
```

```powershell
$env:ATLAS_ENV_FILE = "$env:LOCALAPPDATA\atlassian-mcp-lite\env"
& "$env:LOCALAPPDATA\atlassian-mcp-lite\atlassian-mcp-lite.exe" < $null
```

Add the CA arguments from Step 6 here too if the network intercepts TLS. For
the binary the equivalent is `SSL_CERT_FILE` in the environment; on Windows and
macOS the Go runtime reads the system trust store, so a CA installed there is
already trusted and nothing extra is needed.

To see the tool list the server actually advertises — the fastest check that
the capability settings came out as intended — run it under the MCP inspector,
which needs only Node:

```bash
npx @modelcontextprotocol/inspector ~/.local/bin/atlassian-mcp-lite
```

Set `ATLAS_ENV_FILE` in the environment first. With the defaults the inspector
lists exactly four tools: `jira_search`, `jira_get`, `confluence_search`,
`confluence_get_page`.

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

## Step 8 — When it does not work

Read the client's MCP log first. An MCP server that fails at startup usually
shows in the client as "server disconnected" with nothing else; the reason is
always on the server's stderr, which the client captures.

| Client | Log |
|---|---|
| Claude Code | `claude mcp list` shows connection state; run the same command by hand (Step 7) to see stderr |
| Claude Desktop — macOS | `tail -f ~/Library/Logs/Claude/mcp*.log` |
| Claude Desktop — Windows | `Get-Content -Wait "$env:APPDATA\Claude\logs\mcp*.log"` |
| Claude Desktop — Linux | `tail -f ~/.config/Claude/logs/mcp*.log` |
| Cursor | Output panel, **MCP Logs** channel |
| VS Code / Copilot | Output panel, **MCP** channel |

Then match the message:

| Symptom | Cause and fix |
|---|---|
| `config file ... must be mode 0600 or 0400` | The error names the exact `chmod`. On Windows this check does not run at all — see the warning in Step 4. |
| `config file ... is a symbolic link` | Write the real file in the user's own home directory. The server refuses a link because its target's owner, not the user, would decide what is read. |
| `config file ... is owned by uid N` | With Docker, the `--user` argument does not match the file's owner. Use `id -u`/`id -g`. |
| `ATLAS_TOKEN must be at least 16 characters` | The token was truncated on the way into the file, or the placeholder is still there. |
| `no capabilities are enabled` | Every class of every product is off. At minimum leave one `ATLAS_<PRODUCT>_READ` unset or `true`. |
| `401` on the first read | Wrong email, wrong or revoked token, or a token created for a different Atlassian account. Tokens are per account, not per site. |
| `403` on a read that should work | The account lacks permission on that project or space. This is Atlassian's answer, not the server's — Basic auth carries the full authority of the account and nothing less. |
| `x509: certificate signed by unknown authority` | TLS interception. See "Networks that intercept TLS" above. |
| `blocked destination address` | `ATLAS_BASE_URL` resolves to a private, loopback or link-local address. That is refused deliberately; point it at the real site. |
| Tool missing from the client's list | Its action class is off. A disabled tool is absent from `tools/list` rather than failing when called — check `ATLAS_<PRODUCT>_<CLASS>` and restart the client. |
| `result exceeds 1 MiB` | Ask for fewer fields or a smaller `limit`. The result is refused rather than truncated, because a cut JSON document cannot be read safely. |
| Container exits immediately, no output | `-i` is missing from the `docker run` arguments, so the server sees EOF on stdin at once. |

## Upgrading and removing

**Upgrade — image:** `docker pull` the new tag and update the tag in the client
config if it is pinned. **Upgrade — binary:** download the new asset, verify its
checksum, and overwrite the installed file. Nothing else changes: the config
file format is stable and is not touched by an upgrade.

**Upgrade — self-updating variant:** the same download-and-verify path still
works, and is the only path for the default binary and the image. With
`ATLAS_SELFUPDATE=true` the assistant can instead call `self_update`,
which verifies the release signature and checksum, stages the new binary, keeps
the old one as `<binary>.<version>.bak`, and asks for a restart. The server runs
the old code until the MCP client relaunches it.

**Remove:** delete the server entry from the client config, delete the config
file (it holds the token), then `docker rmi ghcr.io/oxcom/atlassian-mcp-lite`
or delete the binary. Finally **revoke the API token** at
<https://id.atlassian.com/manage-profile/security/api-tokens> — deleting the
file does not invalidate it.
