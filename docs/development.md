# Development

Every tool runs in a pinned container; nothing but Docker is needed on the
host. Versions live in `compose.dev.yaml` and the `Makefile` is a thin wrapper
over it.

```bash
make check    # security, lint, test — what CI runs on a branch
make test     # go test ./... -race -covermode=atomic
make lint     # golangci-lint
make fmt      # gofmt + goimports, applied
make security # gitleaks, trufflehog, govulncheck, gosec
make vet      # go vet
make tidy     # fail if go.mod or go.sum would change
make cover    # per-function coverage report
make build    # binary into ./bin
make image    # production container image
```

`make image` is the build command to use for the image. It passes
`compose.yaml`, `compose.dev.yaml` and, when it exists, `compose.override.yaml`.
A bare `docker compose build` works only when you have no override file: Compose
auto-loads the override for the default `compose.yaml`, the override names
dev-only services, and the project fails to validate.

`compose.yaml` also runs a quick manual smoke test:

```bash
ATLAS_ENV_FILE=~/.config/atlassian-mcp-lite/env docker compose run --rm atlassian-mcp-lite
```

For day-to-day development, skip the container:

```bash
go build ./cmd/atlassian-mcp-lite
ATLAS_ENV_FILE=~/.config/atlassian-mcp-lite/env ./atlassian-mcp-lite
```

## Layout

Four packages plus `cmd/atlassian-mcp-lite`:

- `internal/core` — MCP lifecycle, configuration and the env file loader, module
  registry and capability gating, HTTP client, field selection, logging with
  credential masking, SSRF guards.
- `internal/markup` — markdown to wiki markup for writes; wiki markup and
  rendered HTML to markdown for reads.
- `internal/jira`, `internal/confluence` — declare tools only. Neither may import
  the MCP SDK, read the environment, build an HTTP client, or log.

Tools are declared, not registered. `core.Registry.Enabled` drops any tool whose
action classes are all disabled for its domain, so a disabled tool does not
exist at runtime. `jira_update` spans write and destructive and builds its input
schema from the enabled capabilities: `fixVersion` is the only write-class
property, because it uses Jira's `add` verb, while `assignee`, `epic`,
`parent`, `summary` and `description` each replace an existing value and are
destructive.

`core.NewServer` wraps every successful result as
`{"notice": core.UntrustedNotice, "untrusted_content": ..., "notice_end":
core.UntrustedNotice}`, advertises `core.ServerInstructions` in the initialize
response, and refuses a
wrapped result above `maxResultBytes` (1 MiB) — with an error asking for a
narrower request — rather than truncating it.

`cmd/atlassian-mcp-lite/main.go` is a stdio server: stdout carries the MCP
protocol, all logging goes to stderr. It registers declaration-only modules to
discover the domain names, loads the env file if `ATLAS_ENV_FILE` is set,
loads config, then re-registers functional modules wired to the config and
client.

`core.LoadEnvFile` returns three values — `(func(string) string, string, error)`
— where the middle one is an operator-facing warning, empty when every check
ran. It is returned rather than logged because `internal/core`'s env loader
holds no logger, and `main` writes it with `bootLog.Error` so it is visible at
the default log level. Today it is non-empty only on Windows, where both the
file-permission and the file-owner check are skipped and the operator has to
restrict the file themselves; a single predicate drives the skip and the
warning, so the two cannot disagree about what did not run.

## Tests

Tests use the standard library `testing` package only — no assertion library.
Fakes are `net/http/httptest` servers, and no test contacts a real Atlassian
host. This keeps the dependency budget at four modules.

Two lists are asserted exactly and documented in `docs/configuration.md`:
the `jira_search` default field set and the capability defaults. Change the
code, the test and the doc together.

## Continuous integration

`.github/workflows/verify-and-release.yml` (workflow "Verify and release") runs
five stages, each gated on the whole of the one before: **security → lint → test
→ build → release**. A stage starts only when every job of the previous stage
has passed, so a leaked secret stops the run before anything compiles.

Inside a stage the jobs are independent and run in parallel:

| Stage | Parallel jobs |
|---|---|
| security | gitleaks, trufflehog, govulncheck, gosec, `go mod tidy -diff` |
| lint | golangci-lint, formatting, `go vet` |
| test | race detector + coverage |
| build | one job per target (tags only) |
| release | container image, GitHub release (tags only) |

One failing scanner therefore does not hide what the others would have found.
Adding a job to a stage means adding it to the `needs:` list of every job in
the next stage.

- Branches and pull requests run security, lint and test, then stop.
- `build` and `release` run on `v*` tags only. `build` cross-compiles
  linux/amd64, linux/arm64, darwin/arm64 and windows/amd64 with checksums;
  `release` publishes the binaries and pushes the image to `ghcr.io`.
- The security stage checks out with full history because gitleaks scans
  history, not just the tip. `govulncheck` covers dependency vulnerabilities
  reachable from this code; `gosec` covers our own code.
- `-race` needs CGO, so `CGO_ENABLED=1` is set in the test job only; every
  build path is CGO-free.
- **Every action is pinned to a commit SHA and every container image to a
  digest**, with the human-readable version in a trailing comment. A tag is
  mutable: whoever can move `v7` can run their own code in a workflow that
  holds the release token. `.github/dependabot.yml` watches the
  `github-actions`, `docker` and `docker-compose` ecosystems weekly and opens
  the pull requests that move those pins forward, so pinning does not mean
  going stale. When you add a step, pin it the same way — resolve the tag to
  its SHA and keep the version in the comment.
- The GitHub release is created with `gh release create "$GITHUB_REF_NAME"
  dist/* --generate-notes --verify-tag`, using the job's own token rather than
  a third-party publishing action. `--verify-tag` refuses a ref that is not an
  existing tag.

`gosec` is disabled inside `.golangci.yml` and runs as its own stage, so a lint
failure and a security failure never look alike.

## Behind a corporate proxy

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
CA is additionally guarded with `if [ -s ... ]`. The CA is never installed: it
is concatenated with the build image's roots into a temporary bundle named
through `SSL_CERT_FILE` and deleted in the same layer, and the runtime trust
store is copied from a pristine image, so an office-network build cannot ship
an interception CA. A machine outside the office needs no override file at all.

One caveat, in both directions. Compose auto-loads `compose.override.yaml` for
the default `compose.yaml`, so once you create the override, a bare `docker
compose build` picks it up — and fails, because the override also names
dev-only services that `compose.yaml` does not define. The Makefile passes
`-f`, which disables auto-loading, so it adds the override explicitly: use
`make image`. Conversely, `docker compose -f compose.yaml ...` by hand skips
your proxy settings, because the explicit `-f` suppresses the auto-load.

Turning `GOSUMDB` off trades checksum verification for the ability to build
behind the proxy. Prefer leaving it on and setting `GOPRIVATE` for internal
modules if Artifactory can reach `sum.golang.org`.

## Adding a product module

Create `internal/<name>`, implement `core.Module`, and register it in
`cmd/atlassian-mcp-lite/main.go`. Capability variables
`ATLAS_<NAME>_READ|WRITE|DESTRUCTIVE` are derived from the domain name, so
`internal/core` needs no change; `READ` defaults to on, the other two to off. A
module must not import the MCP SDK, read the environment, build an HTTP client,
or log — core owns all of that, so masking, gating and allowlisting cannot be
forgotten in a module.

Document the new domain's tools and defaults in `docs/configuration.md` and add
it to the product list in `docs/install.md`.
