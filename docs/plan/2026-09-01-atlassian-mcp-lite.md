# atlassian-mcp-lite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A stdio MCP server exposing 10 Jira and Confluence tools, whose advertised tool and parameter surface is assembled at startup from configuration.

**Architecture:** Four packages. `internal/core` owns the MCP server lifecycle, config, HTTP client, field selection, logging and masking. `internal/markup` converts between markdown and Atlassian's formats. `internal/jira` and `internal/confluence` are modules that only *declare* tools — core decides which get registered, based on six domain×action booleans. Disabled tools are never registered, so they are absent rather than filtered.

**Tech Stack:** Go 1.27 · `github.com/modelcontextprotocol/go-sdk` v1.7.0 · stdlib `net/http` · `github.com/google/jsonschema-go/jsonschema` (transitive via the SDK) · Docker Compose

**Spec:** `docs/plan/SPEC.md` — read it before starting. Every task's requirements implicitly include the Global Constraints below.

## Global Constraints

- Module path: `github.com/OxCom/atlassian-mcp-lite`. `go.mod` declares **go 1.27**. Every build,
  test and lint runs inside a container, so the host toolchain version does not matter — do not
  lower the directive to match a host install.
- Dependencies are exactly: `github.com/modelcontextprotocol/go-sdk` v1.7.0, `github.com/yuin/goldmark` v1.8.5, `golang.org/x/net` v0.58.0, plus `github.com/google/jsonschema-go` v0.4.3 — the exact version go-sdk v1.7.0 requires, direct in `go.mod` only because `internal/core` imports it directly (Task 5). Nothing else may be added without changing this line first.
- HTTP through stdlib `net/http` only. No retry, logging, or HTTP helper libraries.
- Never hand-roll a markdown or HTML parser with regex. Use goldmark and `x/net/html` respectively.
- stdio transport only. Never add SSE or streamable HTTP.
- All log output to **stderr**. Writing to stdout corrupts the MCP protocol stream.
- Credential masking is unconditional — never gated on a log level or a flag.
- No tool may accept a filesystem path. No code in this repository opens a local file other than config.
- Tools speak **markdown**. All conversion lives in `internal/markup`; no module converts markup itself.
- All writes send `representation: "wiki"`. Never generate ADF or XHTML storage.
- The markdown converter passes unrecognised syntax through as literal text. Never drop content.
- Every generated JSON schema sets `additionalProperties: false`.
- **Every Atlassian identifier is a string in a tool schema, never a JSON number.** Established in
  Task 6 and verified against go-sdk v1.7.0: the SDK unmarshals a call's arguments into a
  `map[string]any`, applies schema defaults and re-marshals before the handler sees them, so every
  JSON number round-trips through `float64`. Measured: `9007199254740993` arrives as
  `9007199254740992`. Confluence page IDs are already in that range. `internal/core/server_test.go`
  has a test that fails if the SDK ever stops doing this.
- Modules must not import the MCP SDK, read `os.Getenv`, build an `http.Client`, or log directly.
- Tests use `net/http/httptest`. No test may contact a real Atlassian host.


## Multi-agent execution

14 tasks, 6 steps each. Every task ends with a three-engine review gate (Step 5) before its commit
(Step 6), so no task reaches `main` on one model's judgement.

### Dependency order

Tasks are ordered by dependency, not by preference. What can run in parallel:

```
Wave 1  Task 1 (config)  ──┬─→ Task 2 (logging)  ─→ Task 3 (client)
                           └─→ Task 4 (fields)         │
                                                       │
Wave 2  Task 7 (md→wiki)  ─→ Task 8 (wiki/html→md)     │   ← independent of 1–6
                                                       │
Wave 3  Task 5 (registry) ─→ Task 6 (server) ←─────────┘
Wave 4  Task 9  (jira read)      ─→ Task 10 (jira update) ─→ Task 11 (jira write)
        Task 12 (confluence read) ─→ Task 13 (confluence write)
Wave 5  Task 14 (packaging and docs)
```

- Tasks 7 and 8 depend on nothing in core and can be built first, or concurrently by a second
  agent. They are the largest self-contained chunk.
- Tasks 2 and 4 both depend only on Task 1 and are independent of each other.
- Tasks 9–11 and 12–13 are two independent chains once Task 6 lands. Two agents can run them
  concurrently; they share no files.
- `go build ./...` does not succeed until Task 13, because `main.go` from Task 6 references both
  modules. Run per-package tests until then. This is expected.

### Rules for an agent executing one task

1. **Read `docs/plan/SPEC.md` first.** The plan argues from the spec; where they disagree, the spec
   is the requirement and the disagreement is a finding worth reporting.
2. **Read your task's "Interfaces" block.** It names exactly what earlier tasks give you and what
   later tasks will call. You will not have read those tasks, and you do not need to.
3. **Follow the steps in order.** Write the failing test, watch it fail, implement, watch it pass.
   A test that passes before the implementation exists is testing nothing.
4. **Do not touch files outside your task's Files list.** If you believe another file must change,
   that is a finding to report, not a change to make.
5. **Do not skip Step 5.** The review gate is the point of this structure.
6. **Report what you actually did.** If a step failed, or a library's API differs from what the plan
   assumed, say so plainly with the evidence. The plan's "Known adjustment points" exist because
   some of this was expected.

### What earned this structure

Review 01 (`REVIEW-01-findings.md`) ran the same three engines against the plan before any code was
written. It found 5 blocking defects in 39 findings, including one — the SDK's low-level `AddTool`
performing no input validation — that made the entire capability-gating design decorative. All three
engines independently found the same two blockers, and each found blockers the others missed. No
single reviewer, including the plan's own author, found them.

That is the argument for the per-task gate: the cost of three reviews is small against one silently
broken security boundary.

---

### Task 1: Module bootstrap and configuration

**Files:**
- Create: `go.mod`
- Create: `internal/core/config.go`
- Test: `internal/core/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `core.Config` struct with fields `BaseURL, Email, Token string`, `Domains map[string]Caps`, `WriteProjects, WriteSpaces []string`, `LogLevel string`, `LimitDefault, LimitMax int`, `EpicFieldID string`. `core.Caps` struct with `Read, Write, Destructive bool`. `core.Load(getenv func(string) string, domains []string) (Config, error)`. `Config.AllowProject(key string) bool`, `Config.AllowSpace(key string) bool`.

- [x] **Step 1: Write the failing test**

```go
package core

import "testing"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadRequiresBaseURLEmailToken(t *testing.T) {
	_, err := Load(env(map[string]string{"ATLAS_BASE_URL": "https://x.atlassian.net"}), []string{"jira"})
	if err == nil {
		t.Fatal("expected error when ATLAS_EMAIL and ATLAS_TOKEN are missing")
	}
}

func TestLoadDerivesDomainCaps(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"ATLAS_BASE_URL":             "https://x.atlassian.net",
		"ATLAS_EMAIL":                "a@b.c",
		"ATLAS_TOKEN":                "t",
		"ATLAS_JIRA_READ":            "true",
		"ATLAS_JIRA_WRITE":           "true",
		"ATLAS_JIRA_DESTRUCTIVE":     "false",
		"ATLAS_CONFLUENCE_READ":      "true",
	}), []string{"jira", "confluence"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Domains["jira"]; !got.Read || !got.Write || got.Destructive {
		t.Errorf("jira caps = %+v, want read+write only", got)
	}
	if got := cfg.Domains["confluence"]; !got.Read || got.Write || got.Destructive {
		t.Errorf("confluence caps = %+v, want read only (unset means false)", got)
	}
}

func TestAllowlistUnsetAndEmptyAreUnrestricted(t *testing.T) {
	for name, m := range map[string]map[string]string{
		"unset": {},
		"empty": {"ATLAS_WRITE_PROJECTS": ""},
	} {
		m["ATLAS_BASE_URL"] = "https://x.atlassian.net"
		m["ATLAS_EMAIL"] = "a@b.c"
		m["ATLAS_TOKEN"] = "t"
		cfg, err := Load(env(m), []string{"jira"})
		if err != nil {
			t.Fatalf("%s: Load: %v", name, err)
		}
		if !cfg.AllowProject("ANYTHING") {
			t.Errorf("%s: want unrestricted", name)
		}
	}
}

func TestAllowlistNonEmptyIsStrict(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"ATLAS_BASE_URL":       "https://x.atlassian.net",
		"ATLAS_EMAIL":          "a@b.c",
		"ATLAS_TOKEN":          "t",
		"ATLAS_WRITE_PROJECTS": "PROJ, CEM",
	}), []string{"jira"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AllowProject("PROJ") {
		t.Error("PROJ should be allowed")
	}
	if !cfg.AllowProject("cem") {
		t.Error("matching must be case-insensitive and whitespace-trimmed")
	}
	if cfg.AllowProject("HR") {
		t.Error("HR must be refused")
	}
}

func TestLimitDefaults(t *testing.T) {
	cfg, _ := Load(env(map[string]string{
		"ATLAS_BASE_URL": "https://x.atlassian.net",
		"ATLAS_EMAIL":    "a@b.c",
		"ATLAS_TOKEN":    "t",
	}), []string{"jira"})
	if cfg.LimitDefault != 20 || cfg.LimitMax != 50 {
		t.Errorf("limits = %d/%d, want 20/50", cfg.LimitDefault, cfg.LimitMax)
	}
	if cfg.EpicFieldID != "customfield_10014" {
		t.Errorf("EpicFieldID = %q, want customfield_10014", cfg.EpicFieldID)
	}
}

func TestLoadRejectsUnsafeBaseURLs(t *testing.T) {
	for name, raw := range map[string]string{
		"plain http":       "http://example.atlassian.net",
		"credentials":      "https://u:p@example.atlassian.net",
		"has path":         "https://example.atlassian.net/wiki",
		"has query":        "https://example.atlassian.net?a=b",
		"unsupported":      "ftp://example.atlassian.net",
		"no host":          "https://",
	} {
		_, err := Load(env(map[string]string{
			"ATLAS_BASE_URL": raw,
			"ATLAS_EMAIL":    "a@b.c",
			"ATLAS_TOKEN":    "t",
		}), []string{"jira"})
		if err == nil {
			t.Errorf("%s (%q) must be rejected", name, raw)
		}
	}
}

func TestLoadAllowsHTTPForLoopbackSoTestsCanRun(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8080", "http://localhost:9000", "http://[::1]:9000"} {
		if _, err := Load(env(map[string]string{
			"ATLAS_BASE_URL": raw,
			"ATLAS_EMAIL":    "a@b.c",
			"ATLAS_TOKEN":    "t",
		}), []string{"jira"}); err != nil {
			t.Errorf("%q must be accepted: %v", raw, err)
		}
	}
}

func TestBaseURLTrailingSlashStripped(t *testing.T) {
	cfg, _ := Load(env(map[string]string{
		"ATLAS_BASE_URL": "https://x.atlassian.net/",
		"ATLAS_EMAIL":    "a@b.c",
		"ATLAS_TOKEN":    "t",
	}), []string{"jira"})
	if cfg.BaseURL != "https://x.atlassian.net" {
		t.Errorf("BaseURL = %q, want no trailing slash", cfg.BaseURL)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd /var/www/external/atlassian-mcp-lite && go test ./internal/core/ -run TestLoad -v`
Expected: FAIL — the package does not compile, `undefined: Load`.

- [x] **Step 3: Write minimal implementation**

First `go.mod`:

Run these in the container, not on the host. The host toolchain may be older than the
`go 1.27` directive, and the project's rule is that Go runs in a pinned image:

```bash
cd /var/www/external/atlassian-mcp-lite
GO="docker run --rm -v $PWD:/src -w /src golang:1.27-trixie go"
$GO mod init github.com/OxCom/atlassian-mcp-lite
$GO mod edit -go=1.27
$GO get github.com/modelcontextprotocol/go-sdk@v1.7.0
```

`go mod init` writes whatever the image's toolchain reports, so `mod edit -go=1.27` pins the
directive explicitly rather than leaving it to the image tag.

Then `internal/core/config.go`:

```go
// Package core provides the generic MCP provider: configuration, tool
// registration and gating, HTTP access, field selection and logging.
// Product modules declare tools; core decides what is registered.
package core

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Caps is the set of action classes enabled for one domain.
type Caps struct {
	Read        bool
	Write       bool
	Destructive bool
}

// Any reports whether the domain has any capability at all.
func (c Caps) Any() bool { return c.Read || c.Write || c.Destructive }

// Config is the fully resolved server configuration.
type Config struct {
	BaseURL string
	Email   string
	Token   string

	Domains map[string]Caps

	WriteProjects []string
	WriteSpaces   []string

	LogLevel     string
	LimitDefault int
	LimitMax     int

	// EpicFieldID is site-specific: the Epic Link custom field id differs
	// between Jira sites, so it is configurable rather than a constant.
	EpicFieldID string
}

// Load resolves configuration for the given domains. Capability env vars are
// derived from each domain name, so registering a new module needs no change
// here.
func Load(getenv func(string) string, domains []string) (Config, error) {
	cfg := Config{
		BaseURL:      strings.TrimRight(strings.TrimSpace(getenv("ATLAS_BASE_URL")), "/"),
		Email:        strings.TrimSpace(getenv("ATLAS_EMAIL")),
		Token:        strings.TrimSpace(getenv("ATLAS_TOKEN")),
		Domains:      make(map[string]Caps, len(domains)),
		LogLevel:     orDefault(getenv("ATLAS_LOG"), "info"),
		LimitDefault: intOrDefault(getenv("ATLAS_LIMIT_DEFAULT"), 20),
		LimitMax:     intOrDefault(getenv("ATLAS_LIMIT_MAX"), 50),
		EpicFieldID:  orDefault(getenv("ATLAS_EPIC_FIELD_ID"), "customfield_10014"),
	}

	for _, missing := range []struct {
		name, val string
	}{
		{"ATLAS_BASE_URL", cfg.BaseURL},
		{"ATLAS_EMAIL", cfg.Email},
		{"ATLAS_TOKEN", cfg.Token},
	} {
		if missing.val == "" {
			return Config{}, fmt.Errorf("%s is required", missing.name)
		}
	}

	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return Config{}, fmt.Errorf("ATLAS_BASE_URL: %w", err)
	}

	for _, d := range domains {
		prefix := "ATLAS_" + strings.ToUpper(d) + "_"
		cfg.Domains[d] = Caps{
			Read:        isTrue(getenv(prefix + "READ")),
			Write:       isTrue(getenv(prefix + "WRITE")),
			Destructive: isTrue(getenv(prefix + "DESTRUCTIVE")),
		}
	}

	cfg.WriteProjects = splitList(getenv("ATLAS_WRITE_PROJECTS"))
	cfg.WriteSpaces = splitList(getenv("ATLAS_WRITE_SPACES"))

	if cfg.LimitDefault > cfg.LimitMax {
		cfg.LimitDefault = cfg.LimitMax
	}
	return cfg, nil
}

// validateBaseURL rejects a base URL that would leak credentials or break path
// concatenation. Failing here beats failing on the first request, and an http
// URL would send Basic credentials in clear text.
//
// Loopback hosts may use http so tests can point at an httptest server.
func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a valid URL: %w", err)
	}
	if u.Host == "" {
		return fmt.Errorf("no host in %q", raw)
	}
	if u.User != nil {
		return errors.New("must not contain credentials in the URL")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must be an origin only, with no query or fragment")
	}
	if u.Path != "" {
		return fmt.Errorf("must be an origin only, but has path %q", u.Path)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopback(u.Hostname()) {
			return nil
		}
		return errors.New("http is only allowed for loopback hosts; Basic credentials must not travel in clear text")
	default:
		return fmt.Errorf("scheme %q is not supported; use https", u.Scheme)
	}
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// AllowProject reports whether writes to key are permitted. An unset or empty
// allowlist means unrestricted; a non-empty one is strict.
func (c Config) AllowProject(key string) bool { return allowed(c.WriteProjects, key) }

// AllowSpace reports whether writes to a Confluence space key are permitted.
func (c Config) AllowSpace(key string) bool { return allowed(c.WriteSpaces, key) }

func allowed(list []string, key string) bool {
	if len(list) == 0 {
		return true
	}
	for _, e := range list {
		if strings.EqualFold(e, strings.TrimSpace(key)) {
			return true
		}
	}
	return false
}

func isTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func orDefault(s, def string) string {
	if s = strings.TrimSpace(s); s == "" {
		return def
	}
	return s
}

func intOrDefault(s string, def int) int {
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err != nil || n <= 0 {
		return def
	}
	return n
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -v`
Expected: PASS — all six tests.

- [x] **Step 5: Three-engine review gate**

Do not commit yet. Dispatch three independent reviews of *this task's diff only*, in parallel, one
per engine. Each gets the same diff and the task's own "Interfaces" block, and a different lens.

```
Agent(model="fable",  prompt=<completeness lens>)   # spec coverage, missing tests, error paths
Agent(model="haiku",  prompt=<consistency lens>)    # signatures, types, imports, helper clashes
Agent(subagent_type="claude", prompt=<relay>)       # wraps mcp__codex__codex: Go and API correctness
```

Give every reviewer this preamble:

> Review the diff below against `docs/plan/SPEC.md` and this task's Interfaces block. Report findings
> only; do not rewrite. For each: SEVERITY (blocking | important | minor), WHERE (file:line), WHAT
> (one or two sentences), FIX (the concrete change). No praise, no summary. If you find nothing real,
> say so rather than padding.

Then the lens, one per engine:

- **fable** — completeness: does this task implement every spec requirement it claims? What
  behaviour has no test? What happens on a 4xx, a 5xx, a cancelled context, an empty result?
- **haiku** — consistency: do the signatures, types, field names and imports match what earlier
  tasks defined and later tasks will call? Any helper defined twice in one package? Any import
  declared but unused, which fails to compile in Go?
- **codex** — correctness: will this compile? Is the SDK, goldmark, `x/net/html` and Atlassian REST
  usage right? Does each test actually test what it claims, or would it pass regardless of the
  implementation?

Then:

1. Triage every finding. Blocking and important ones get fixed before the commit. Minor ones are
   fixed or recorded with a reason.
2. **Verify any claim about a library's behaviour against that library's source** before acting on
   it. Review 01 found a blocking defect that only source-reading could confirm, and one false
   positive that only source-reading could refute. A reviewer's confident claim is a lead, not a
   fact.
3. Re-run `go test ./...` for the packages this task touches.
4. Append the findings and their dispositions to `docs/plan/REVIEW-LOG.md`, in the same table shape
   as `REVIEW-01-findings.md`, noting which engine found what and where they agreed. Agreement
   across engines is the strongest signal available.

The gate is the review, never a rubber stamp: "three reviews ran and found nothing blocking" is a
complete and valid outcome, recorded in one line.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/core/config.go internal/core/config_test.go
git commit -m "feat(core): configuration with derived domain capabilities and write allowlists"
```

---

### Task 2: Logging and credential masking

**Files:**
- Create: `internal/core/log.go`
- Test: `internal/core/log_test.go`

**Interfaces:**
- Consumes: `core.Config.LogLevel` from Task 1.
- Produces: `core.Logger` with `Debugf(format string, args ...any)`, `Errorf(format string, args ...any)`, `Enabled(level string) bool`. Constructor `core.NewLogger(level string, w io.Writer) *Logger`. `core.Mask(s string) string`, `core.MaskHeaders(h http.Header) map[string]string`.

- [ ] **Step 1: Write the failing test**

```go
package core

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestMaskKeepsEndsHidesMiddle(t *testing.T) {
	got := Mask("TOKENsecretmiddlepart0123")
	if strings.Contains(got, "secretmiddlepart") {
		t.Fatalf("Mask leaked the middle: %q", got)
	}
	if !strings.HasPrefix(got, "TOKE") || !strings.HasSuffix(got, "0123") {
		t.Errorf("Mask = %q, want first and last 4 preserved", got)
	}
}

func TestMaskShortValueFullyHidden(t *testing.T) {
	if got := Mask("abcd"); strings.Contains(got, "abcd") {
		t.Errorf("Mask(%q) = %q, short values must be fully hidden", "abcd", got)
	}
}

func TestMaskEmpty(t *testing.T) {
	if got := Mask(""); got != "" {
		t.Errorf("Mask(\"\") = %q, want empty", got)
	}
}

func TestMaskHeadersPreservesSchemeHidesCredential(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Basic YW5kcmlpQGNyeXRlay5jb206c2VjcmV0dmFsdWU=")
	h.Set("Cookie", "session=abcdefghijklmnop")
	h.Set("Accept", "application/json")

	got := MaskHeaders(h)
	if !strings.HasPrefix(got["Authorization"], "Basic ") {
		t.Errorf("Authorization = %q, want scheme preserved", got["Authorization"])
	}
	if strings.Contains(got["Authorization"], "c2VjcmV0dmFsdWU") {
		t.Errorf("Authorization leaked credential: %q", got["Authorization"])
	}
	if strings.Contains(got["Cookie"], "abcdefghijklmnop") {
		t.Errorf("Cookie leaked: %q", got["Cookie"])
	}
	if got["Accept"] != "application/json" {
		t.Errorf("Accept = %q, non-sensitive headers must pass through", got["Accept"])
	}
}

// A cookie value containing a space must not be split like an auth scheme:
// that would leave the credential segment in the clear.
func TestMaskHeadersCookieWithSpacesFullyMasked(t *testing.T) {
	h := http.Header{}
	h.Set("Cookie", "session=supersecretvalue; Path=/; HttpOnly")
	h.Set("Set-Cookie", "tenant.session=anothersecretvalue; Secure")

	got := MaskHeaders(h)
	for _, k := range []string{"Cookie", "Set-Cookie"} {
		for _, leak := range []string{"supersecretvalue", "anothersecretvalue", "session="} {
			if strings.Contains(got[k], leak) {
				t.Errorf("%s leaked %q: %q", k, leak, got[k])
			}
		}
	}
}

// Secrets echoed inside an arbitrary log message must be redacted too, not just
// those in headers we chose to mask.
func TestLoggerRedactsConfiguredSecrets(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("debug", &buf, "supersecrettoken", "")
	l.Errorf("upstream said: %s", `{"errorMessages":["bad token supersecrettoken"]}`)
	if strings.Contains(buf.String(), "supersecrettoken") {
		t.Fatalf("secret leaked into log: %q", buf.String())
	}
}

func TestLoggerInfoSuppressesDebug(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("info", &buf)
	l.Debugf("this must not appear")
	l.Errorf("this must appear")
	out := buf.String()
	if strings.Contains(out, "must not appear") {
		t.Error("debug output leaked at info level")
	}
	if !strings.Contains(out, "must appear") {
		t.Error("error output missing at info level")
	}
}

func TestLoggerDebugEmitsBoth(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("debug", &buf)
	l.Debugf("dbg")
	l.Errorf("err")
	out := buf.String()
	if !strings.Contains(out, "dbg") || !strings.Contains(out, "err") {
		t.Errorf("debug level output = %q, want both lines", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run 'TestMask|TestLogger' -v`
Expected: FAIL — `undefined: Mask`, `undefined: NewLogger`.

- [ ] **Step 3: Write minimal implementation**

```go
package core

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// sensitiveHeaders are masked at every log level, unconditionally.
var sensitiveHeaders = map[string]bool{
	"Authorization":       true,
	"Cookie":              true,
	"Set-Cookie":          true,
	"Proxy-Authorization": true,
}

// Mask hides the middle of a secret, keeping at most the first and last four
// characters so a value can be recognised without being usable.
func Mask(s string) string {
	const keep = 4
	if s == "" {
		return ""
	}
	if len(s) <= keep*2 {
		return strings.Repeat("*", len(s))
	}
	return s[:keep] + strings.Repeat("*", len(s)-keep*2) + s[len(s)-keep:]
}

// MaskHeaders returns a loggable copy of h with credentials masked. The auth
// scheme is preserved because knowing Basic vs Bearer is diagnostically useful
// and is not secret.
func MaskHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		ck := http.CanonicalHeaderKey(k)
		if !sensitiveHeaders[ck] {
			out[k] = strings.Join(vs, ", ")
			continue
		}
		masked := make([]string, 0, len(vs))
		for _, v := range vs {
			// A scheme is preserved only for the authorization headers, where
			// "Basic" vs "Bearer" is useful and not secret. Cookies look like
			// "name=value; Path=/", so cutting at the first space would leak
			// the entire credential segment.
			if ck == "Authorization" || ck == "Proxy-Authorization" {
				if scheme, cred, ok := strings.Cut(v, " "); ok {
					masked = append(masked, scheme+" "+Mask(cred))
					continue
				}
			}
			masked = append(masked, Mask(v))
		}
		out[k] = strings.Join(masked, ", ")
	}
	return out
}

// Logger writes to stderr only. stdout carries the MCP protocol stream and
// must never be written to.
type Logger struct {
	mu      sync.Mutex
	w       io.Writer
	debug   bool
	secrets []string
}

// NewLogger returns a Logger at the given level ("info" or "debug").
//
// Any secrets given are redacted from every message at every level. Masking
// headers is not sufficient on its own: an upstream error body can echo a
// credential back, and that body is logged deliberately because it carries the
// useful diagnostics.
func NewLogger(level string, w io.Writer, secrets ...string) *Logger {
	kept := make([]string, 0, len(secrets))
	for _, sec := range secrets {
		// A very short secret would redact ordinary words.
		if len(sec) >= 8 {
			kept = append(kept, sec)
		}
	}
	return &Logger{
		w:       w,
		debug:   strings.EqualFold(strings.TrimSpace(level), "debug"),
		secrets: kept,
	}
}

// Enabled reports whether the named level would produce output.
func (l *Logger) Enabled(level string) bool {
	if strings.EqualFold(level, "debug") {
		return l.debug
	}
	return true
}

// Debugf logs only when the level is debug.
func (l *Logger) Debugf(format string, args ...any) {
	if l.debug {
		l.emit("DEBUG", format, args...)
	}
}

// Errorf always logs.
func (l *Logger) Errorf(format string, args ...any) { l.emit("ERROR", format, args...) }

func (l *Logger) emit(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	for _, sec := range l.secrets {
		msg = strings.ReplaceAll(msg, sec, Mask(sec))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "%s %s\n", level, msg)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -v`
Expected: PASS — Task 1 and Task 2 tests.

- [ ] **Step 5: Three-engine review gate**

Do not commit yet. Dispatch three independent reviews of *this task's diff only*, in parallel, one
per engine. Each gets the same diff and the task's own "Interfaces" block, and a different lens.

```
Agent(model="fable",  prompt=<completeness lens>)   # spec coverage, missing tests, error paths
Agent(model="haiku",  prompt=<consistency lens>)    # signatures, types, imports, helper clashes
Agent(subagent_type="claude", prompt=<relay>)       # wraps mcp__codex__codex: Go and API correctness
```

Give every reviewer this preamble:

> Review the diff below against `docs/plan/SPEC.md` and this task's Interfaces block. Report findings
> only; do not rewrite. For each: SEVERITY (blocking | important | minor), WHERE (file:line), WHAT
> (one or two sentences), FIX (the concrete change). No praise, no summary. If you find nothing real,
> say so rather than padding.

Then the lens, one per engine:

- **fable** — completeness: does this task implement every spec requirement it claims? What
  behaviour has no test? What happens on a 4xx, a 5xx, a cancelled context, an empty result?
- **haiku** — consistency: do the signatures, types, field names and imports match what earlier
  tasks defined and later tasks will call? Any helper defined twice in one package? Any import
  declared but unused, which fails to compile in Go?
- **codex** — correctness: will this compile? Is the SDK, goldmark, `x/net/html` and Atlassian REST
  usage right? Does each test actually test what it claims, or would it pass regardless of the
  implementation?

Then:

1. Triage every finding. Blocking and important ones get fixed before the commit. Minor ones are
   fixed or recorded with a reason.
2. **Verify any claim about a library's behaviour against that library's source** before acting on
   it. Review 01 found a blocking defect that only source-reading could confirm, and one false
   positive that only source-reading could refute. A reviewer's confident claim is a lead, not a
   fact.
3. Re-run `go test ./...` for the packages this task touches.
4. Append the findings and their dispositions to `docs/plan/REVIEW-LOG.md`, in the same table shape
   as `REVIEW-01-findings.md`, noting which engine found what and where they agreed. Agreement
   across engines is the strongest signal available.

The gate is the review, never a rubber stamp: "three reviews ran and found nothing blocking" is a
complete and valid outcome, recorded in one line.

- [ ] **Step 6: Commit**

```bash
git add internal/core/log.go internal/core/log_test.go
git commit -m "feat(core): stderr logger with unconditional credential masking"
```

---

### Task 3: HTTP client

**Files:**
- Create: `internal/core/client.go`
- Test: `internal/core/client_test.go`

**Interfaces:**
- Consumes: `core.Config` (Task 1), `core.Logger` (Task 2).
- Produces: `core.Client` with `Do(ctx context.Context, method, path string, query url.Values, body any, out any) error`. Constructor `core.NewClient(cfg Config, log *Logger) *Client`. Error type `core.APIError` with fields `Status int`, `Method, Path string`, `Message string`, implementing `error`.

The `path` argument is always a server-controlled constant with url-escaped segments interpolated by the caller. The base URL comes from config and is never taken from tool input — this is what makes SSRF unreachable without a DNS-pinning adapter.

- [ ] **Step 1: Write the failing test**

```go
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc) (*Client, *bytes.Buffer, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	var logs bytes.Buffer
	cfg := Config{BaseURL: srv.URL, Email: "a@b.c", Token: "test-token-value-1234"}
	return NewClient(cfg, NewLogger("debug", &logs)), &logs, srv
}

func TestDoSendsBasicAuthAndDecodes(t *testing.T) {
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "a@b.c" || pass != "test-token-value-1234" {
			t.Errorf("basic auth = %q/%q ok=%v", user, pass, ok)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		io.WriteString(w, `{"key":"PROJ-123"}`)
	})

	var out struct{ Key string }
	if err := c.Do(context.Background(), http.MethodGet, "/rest/api/3/issue/PROJ-123", nil, nil, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out.Key != "PROJ-123" {
		t.Errorf("Key = %q", out.Key)
	}
}

func TestDoSendsJSONBodyAndQuery(t *testing.T) {
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("maxResults"); got != "20" {
			t.Errorf("maxResults = %q", got)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		var got map[string]any
		json.NewDecoder(r.Body).Decode(&got)
		if got["jql"] != "project = PROJ" {
			t.Errorf("body jql = %v", got["jql"])
		}
		w.WriteHeader(http.StatusNoContent)
	})

	q := url.Values{"maxResults": {"20"}}
	body := map[string]any{"jql": "project = PROJ"}
	if err := c.Do(context.Background(), http.MethodPost, "/rest/api/3/search/jql", q, body, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
}

func TestDoReturnsAPIErrorWithUpstreamMessage(t *testing.T) {
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"errorMessages":["Field 'customfield_10014' cannot be set"],"errors":{}}`)
	})

	err := c.Do(context.Background(), http.MethodPut, "/rest/api/2/issue/PROJ-123", nil, map[string]any{}, nil)
	if err == nil {
		t.Fatal("expected error on 400")
	}
	var apiErr *APIError
	if !errorsAs(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.Status != 400 {
		t.Errorf("Status = %d", apiErr.Status)
	}
	if !strings.Contains(apiErr.Message, "customfield_10014") {
		t.Errorf("Message = %q, want upstream text preserved", apiErr.Message)
	}
}

func TestErrorBodyLoggedButSuccessBodyNot(t *testing.T) {
	c, logs, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"errorMessages":["nope-secret-detail"]}`)
			return
		}
		io.WriteString(w, `{"confidential":"business-data-value"}`)
	})

	var out any
	c.Do(context.Background(), http.MethodGet, "/ok", nil, nil, &out)
	c.Do(context.Background(), http.MethodGet, "/fail", nil, nil, &out)

	got := logs.String()
	if strings.Contains(got, "business-data-value") {
		t.Error("successful response body must never be logged")
	}
	if !strings.Contains(got, "nope-secret-detail") {
		t.Error("error response body should be logged for diagnosis")
	}
}

func TestTokenNeverAppearsInLogs(t *testing.T) {
	c, logs, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"errorMessages":["boom"]}`)
	})
	var out any
	c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out)
	if strings.Contains(logs.String(), "test-token-value-1234") {
		t.Fatal("token leaked into logs")
	}
}

func TestDoTreatsRedirectAsError(t *testing.T) {
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		// A 3xx that is not followed must not be decoded as a success.
		w.Header().Set("Location", "https://elsewhere.example/")
		w.WriteHeader(http.StatusMovedPermanently)
	})
	var out any
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out); err == nil {
		t.Fatal("a 301 must be an error")
	}
}

func TestDoUnauthorizedIsAPIError(t *testing.T) {
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "Client must be authenticated to access this resource.")
	})
	var out any
	err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out)
	var apiErr *APIError
	if !errorsAs(err, &apiErr) || apiErr.Status != 401 {
		t.Fatalf("err = %v, want *APIError with status 401", err)
	}
}

func TestDoRateLimitedSurfacesRetryAfter(t *testing.T) {
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"errorMessages":["rate limit exceeded"]}`)
	})
	var out any
	err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out)
	var apiErr *APIError
	if !errorsAs(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", apiErr.RetryAfter)
	}
}

func TestDoCancelledContextAborts(t *testing.T) {
	release := make(chan struct{})
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
		io.WriteString(w, `{}`)
	})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out any
	err := c.Do(ctx, http.MethodGet, "/x", nil, nil, &out)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestDoMalformedSuccessBodyIsAnError(t *testing.T) {
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"key": "truncated`)
	})
	var out struct{ Key string }
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out); err == nil {
		t.Fatal("invalid JSON on a 200 must be an error, not a partial result")
	}
}

func TestErrorBodyEchoingTheTokenIsRedacted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"errorMessages":["bad credential test-token-value-1234"]}`)
	}))
	t.Cleanup(srv.Close)

	var logs bytes.Buffer
	cfg := Config{BaseURL: srv.URL, Email: "u@example.com", Token: "test-token-value-1234"}
	c := NewClient(cfg, NewLogger("debug", &logs, cfg.Token))

	var out any
	c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out)
	if strings.Contains(logs.String(), "test-token-value-1234") {
		t.Fatalf("token echoed by upstream leaked into logs: %q", logs.String())
	}
}

// errorsAs is a tiny shim so the test reads clearly; use errors.As directly in
// implementation code.
func errorsAs(err error, target **APIError) bool {
	for err != nil {
		if e, ok := err.(*APIError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run 'TestDo|TestError|TestToken' -v`
Expected: FAIL — `undefined: NewClient`, `undefined: APIError`.

- [ ] **Step 3: Write minimal implementation**

```go
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxErrorBody caps how much of a failing response we read, so a large error
// page cannot flood the log.
const maxErrorBody = 8 << 10

// APIError is a non-2xx response from Atlassian, carrying the upstream message
// because that text is the useful part of a Jira or Confluence failure.
type APIError struct {
	Status  int
	Method  string
	Path    string
	Message string
	// RetryAfter is the parsed Retry-After header on a 429, so a caller has a
	// backoff signal instead of only a status code. Zero when absent.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Message)
}

// Client is the only way a module may reach Atlassian. The base URL is fixed
// at construction from configuration and is never derived from tool input.
type Client struct {
	base  string
	email string
	token string
	log   *Logger
	http  *http.Client
}

// NewClient builds a Client bound to cfg.BaseURL.
func NewClient(cfg Config, log *Logger) *Client {
	return &Client{
		base:  strings.TrimRight(cfg.BaseURL, "/"),
		email: cfg.Email,
		token: cfg.Token,
		log:   log,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Do performs a request against path, decoding a 2xx JSON body into out when
// out is non-nil. body, when non-nil, is marshalled as JSON.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reader io.Reader
	var sentBytes int
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		sentBytes = len(buf)
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.SetBasicAuth(c.email, c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	start := time.Now()
	res, err := c.http.Do(req)
	if err != nil {
		c.log.Errorf("%s %s: transport error after %s: %v", method, path, time.Since(start).Round(time.Millisecond), err)
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer res.Body.Close()

	// Anything outside 2xx is a failure. Checking only >= 400 would let an
	// unfollowed 3xx be decoded as a successful result.
	if res.StatusCode < 200 || res.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
		msg := upstreamMessage(raw)
		apiErr := &APIError{Status: res.StatusCode, Method: method, Path: path, Message: msg}
		if res.StatusCode == http.StatusTooManyRequests {
			apiErr.RetryAfter = parseRetryAfter(res.Header.Get("Retry-After"))
			if apiErr.RetryAfter > 0 {
				apiErr.Message = fmt.Sprintf("%s (retry after %s)", msg, apiErr.RetryAfter)
			}
		}
		// Error bodies are logged: this is where Atlassian's diagnostics live.
		// The logger redacts configured secrets, so an echoed credential in the
		// upstream text does not reach the log.
		c.log.Errorf("%s %s -> %d in %s: %s", method, path, res.StatusCode,
			time.Since(start).Round(time.Millisecond), apiErr.Message)
		return apiErr
	}

	var recvBytes int
	if out != nil {
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("%s %s: read body: %w", method, path, err)
		}
		recvBytes = len(raw)
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("%s %s: decode body: %w", method, path, err)
			}
		}
	} else {
		recvBytes, _ = discard(res.Body)
	}

	// Successful bodies are business data and are never logged — only shapes.
	c.log.Debugf("%s %s -> %d in %s (sent %dB, recv %dB) headers=%v",
		method, path, res.StatusCode, time.Since(start).Round(time.Millisecond),
		sentBytes, recvBytes, MaskHeaders(req.Header))
	return nil
}

// parseRetryAfter handles both forms the header may take: delay-seconds, or an
// HTTP date. An unparseable value yields zero rather than an error, because a
// missing backoff hint must not fail the call.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func discard(r io.Reader) (int, error) {
	n, err := io.Copy(io.Discard, r)
	return int(n), err
}

// upstreamMessage extracts a human-readable message from an Atlassian error
// body, falling back to the raw text.
func upstreamMessage(raw []byte) string {
	var jiraShape struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
		Message       string            `json:"message"`
	}
	if err := json.Unmarshal(raw, &jiraShape); err == nil {
		var parts []string
		parts = append(parts, jiraShape.ErrorMessages...)
		for k, v := range jiraShape.Errors {
			parts = append(parts, k+": "+v)
		}
		if jiraShape.Message != "" {
			parts = append(parts, jiraShape.Message)
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
	}
	if s := strings.TrimSpace(string(raw)); s != "" {
		return s
	}
	return "(empty error body)"
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -v`
Expected: PASS — all tests from Tasks 1–3.

- [ ] **Step 5: Three-engine review gate**

Do not commit yet. Dispatch three independent reviews of *this task's diff only*, in parallel, one
per engine. Each gets the same diff and the task's own "Interfaces" block, and a different lens.

```
Agent(model="fable",  prompt=<completeness lens>)   # spec coverage, missing tests, error paths
Agent(model="haiku",  prompt=<consistency lens>)    # signatures, types, imports, helper clashes
Agent(subagent_type="claude", prompt=<relay>)       # wraps mcp__codex__codex: Go and API correctness
```

Give every reviewer this preamble:

> Review the diff below against `docs/plan/SPEC.md` and this task's Interfaces block. Report findings
> only; do not rewrite. For each: SEVERITY (blocking | important | minor), WHERE (file:line), WHAT
> (one or two sentences), FIX (the concrete change). No praise, no summary. If you find nothing real,
> say so rather than padding.

Then the lens, one per engine:

- **fable** — completeness: does this task implement every spec requirement it claims? What
  behaviour has no test? What happens on a 4xx, a 5xx, a cancelled context, an empty result?
- **haiku** — consistency: do the signatures, types, field names and imports match what earlier
  tasks defined and later tasks will call? Any helper defined twice in one package? Any import
  declared but unused, which fails to compile in Go?
- **codex** — correctness: will this compile? Is the SDK, goldmark, `x/net/html` and Atlassian REST
  usage right? Does each test actually test what it claims, or would it pass regardless of the
  implementation?

Then:

1. Triage every finding. Blocking and important ones get fixed before the commit. Minor ones are
   fixed or recorded with a reason.
2. **Verify any claim about a library's behaviour against that library's source** before acting on
   it. Review 01 found a blocking defect that only source-reading could confirm, and one false
   positive that only source-reading could refute. A reviewer's confident claim is a lead, not a
   fact.
3. Re-run `go test ./...` for the packages this task touches.
4. Append the findings and their dispositions to `docs/plan/REVIEW-LOG.md`, in the same table shape
   as `REVIEW-01-findings.md`, noting which engine found what and where they agreed. Agreement
   across engines is the strongest signal available.

The gate is the review, never a rubber stamp: "three reviews ran and found nothing blocking" is a
complete and valid outcome, recorded in one line.

- [ ] **Step 6: Commit**

```bash
git add internal/core/client.go internal/core/client_test.go
git commit -m "feat(core): HTTP client with fixed base URL, upstream error text, no success-body logging"
```

---

### Task 4: Field selection engine

**Files:**
- Create: `internal/core/fields.go`
- Test: `internal/core/fields_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `core.ResolveFields(defaults []string, requested []string) ([]string, error)`.

- [ ] **Step 1: Write the failing test**

```go
package core

import (
	"reflect"
	"sort"
	"testing"
)

var testDefaults = []string{"key", "summary", "status"}

func TestResolveFieldsOmittedReturnsDefaults(t *testing.T) {
	got, err := ResolveFields(testDefaults, nil)
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	if !reflect.DeepEqual(got, testDefaults) {
		t.Errorf("got %v, want %v", got, testDefaults)
	}
}

func TestResolveFieldsBareReplaces(t *testing.T) {
	got, err := ResolveFields(testDefaults, []string{"summary", "assignee"})
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	want := []string{"summary", "assignee"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want exactly %v", got, want)
	}
}

func TestResolveFieldsPlusAdds(t *testing.T) {
	got, err := ResolveFields(testDefaults, []string{"+description"})
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	want := []string{"description", "key", "status", "summary"}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestResolveFieldsMinusRemoves(t *testing.T) {
	got, err := ResolveFields(testDefaults, []string{"-status"})
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	want := []string{"key", "summary"}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestResolveFieldsCombinesPlusAndMinus(t *testing.T) {
	got, err := ResolveFields(testDefaults, []string{"+description", "-status"})
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	want := []string{"description", "key", "summary"}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestResolveFieldsRejectsMixedForms(t *testing.T) {
	if _, err := ResolveFields(testDefaults, []string{"summary", "+description"}); err == nil {
		t.Fatal("mixing bare and prefixed names must be an error")
	}
}

func TestResolveFieldsRejectsEmptyResult(t *testing.T) {
	if _, err := ResolveFields(testDefaults, []string{"-key", "-summary", "-status"}); err == nil {
		t.Fatal("removing every field must be an error, not an empty request")
	}
}

func TestResolveFieldsIgnoresDuplicatesAndBlanks(t *testing.T) {
	got, err := ResolveFields(testDefaults, []string{"+description", "+description", " "})
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("got %v, want 4 unique fields", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run TestResolveFields -v`
Expected: FAIL — `undefined: ResolveFields`.

- [ ] **Step 3: Write minimal implementation**

```go
package core

import (
	"errors"
	"fmt"
	"strings"
)

// ResolveFields applies the field-selection grammar:
//
//	nil / empty  -> defaults
//	"name"       -> replaces the default set entirely
//	"+name"      -> added to the default set
//	"-name"      -> removed from the default set
//
// Bare and prefixed forms may not be mixed: the intent is ambiguous, so it is
// rejected rather than guessed.
func ResolveFields(defaults, requested []string) ([]string, error) {
	var bare, add, remove []string
	for _, r := range requested {
		r = strings.TrimSpace(r)
		switch {
		case r == "" || r == "+" || r == "-":
			continue
		case strings.HasPrefix(r, "+"):
			add = append(add, strings.TrimSpace(r[1:]))
		case strings.HasPrefix(r, "-"):
			remove = append(remove, strings.TrimSpace(r[1:]))
		default:
			bare = append(bare, r)
		}
	}

	if len(bare) > 0 && (len(add) > 0 || len(remove) > 0) {
		return nil, errors.New(`fields: cannot mix bare names with "+" or "-" prefixes; ` +
			`use bare names to replace the default set, or prefixes to adjust it`)
	}

	if len(bare) > 0 {
		out := dedupe(bare)
		if len(out) == 0 {
			return nil, errors.New("fields: resolved to an empty set")
		}
		return out, nil
	}

	if len(add) == 0 && len(remove) == 0 {
		return dedupe(defaults), nil
	}

	drop := make(map[string]bool, len(remove))
	for _, r := range remove {
		drop[strings.ToLower(r)] = true
	}

	var out []string
	for _, d := range defaults {
		if !drop[strings.ToLower(d)] {
			out = append(out, d)
		}
	}
	out = append(out, add...)
	out = dedupe(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("fields: every default field was removed, leaving nothing to return")
	}
	return out, nil
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		k := strings.ToLower(s)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -v`
Expected: PASS.

- [ ] **Step 5: Three-engine review gate**

Do not commit yet. Dispatch three independent reviews of *this task's diff only*, in parallel, one
per engine. Each gets the same diff and the task's own "Interfaces" block, and a different lens.

```
Agent(model="fable",  prompt=<completeness lens>)   # spec coverage, missing tests, error paths
Agent(model="haiku",  prompt=<consistency lens>)    # signatures, types, imports, helper clashes
Agent(subagent_type="claude", prompt=<relay>)       # wraps mcp__codex__codex: Go and API correctness
```

Give every reviewer this preamble:

> Review the diff below against `docs/plan/SPEC.md` and this task's Interfaces block. Report findings
> only; do not rewrite. For each: SEVERITY (blocking | important | minor), WHERE (file:line), WHAT
> (one or two sentences), FIX (the concrete change). No praise, no summary. If you find nothing real,
> say so rather than padding.

Then the lens, one per engine:

- **fable** — completeness: does this task implement every spec requirement it claims? What
  behaviour has no test? What happens on a 4xx, a 5xx, a cancelled context, an empty result?
- **haiku** — consistency: do the signatures, types, field names and imports match what earlier
  tasks defined and later tasks will call? Any helper defined twice in one package? Any import
  declared but unused, which fails to compile in Go?
- **codex** — correctness: will this compile? Is the SDK, goldmark, `x/net/html` and Atlassian REST
  usage right? Does each test actually test what it claims, or would it pass regardless of the
  implementation?

Then:

1. Triage every finding. Blocking and important ones get fixed before the commit. Minor ones are
   fixed or recorded with a reason.
2. **Verify any claim about a library's behaviour against that library's source** before acting on
   it. Review 01 found a blocking defect that only source-reading could confirm, and one false
   positive that only source-reading could refute. A reviewer's confident claim is a lead, not a
   fact.
3. Re-run `go test ./...` for the packages this task touches.
4. Append the findings and their dispositions to `docs/plan/REVIEW-LOG.md`, in the same table shape
   as `REVIEW-01-findings.md`, noting which engine found what and where they agreed. Agreement
   across engines is the strongest signal available.

The gate is the review, never a rubber stamp: "three reviews ran and found nothing blocking" is a
complete and valid outcome, recorded in one line.

- [ ] **Step 6: Commit**

```bash
git add internal/core/fields.go internal/core/fields_test.go
git commit -m "feat(core): field selection with replace, +add and -remove semantics"
```

---

### Task 5: Module registry and capability gating

**Files:**
- Create: `internal/core/registry.go`
- Test: `internal/core/registry_test.go`

**Interfaces:**
- Consumes: `core.Config`, `core.Caps` (Task 1); `core.Logger` (Task 2).
- Produces: `core.Action` (`ActionRead`, `ActionWrite`, `ActionDestructive`), `core.ToolDecl{Name string; Actions []Action; Description string; Schema func(Caps) *jsonschema.Schema; Handle func(context.Context, json.RawMessage) (any, error)}`, `core.Module` interface (`Domain() string`, `Tools() []ToolDecl`), `core.Registry` with `Register(Module)`, `Domains() []string`, `Enabled(cfg Config) []Registered`, and `core.Registered{Decl ToolDecl; Domain string; Schema *jsonschema.Schema}`.
- Also produces `core.ObjectSchema(props map[string]*jsonschema.Schema, required []string) *jsonschema.Schema`, which always sets `AdditionalProperties` to a false schema.

- [x] **Step 1: Write the failing test**

```go
package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

type fakeModule struct {
	domain string
	tools  []ToolDecl
}

func (f fakeModule) Domain() string     { return f.domain }
func (f fakeModule) Tools() []ToolDecl  { return f.tools }

func decl(name string, actions ...Action) ToolDecl {
	return ToolDecl{
		Name:        name,
		Actions:     actions,
		Description: name,
		Schema: func(c Caps) *jsonschema.Schema {
			props := map[string]*jsonschema.Schema{"key": {Type: "string"}}
			if c.Destructive {
				props["description"] = &jsonschema.Schema{Type: "string"}
			}
			return ObjectSchema(props, []string{"key"})
		},
		Handle: func(context.Context, json.RawMessage) (any, error) { return nil, nil },
	}
}

func capsCfg(c Caps) Config {
	return Config{Domains: map[string]Caps{"fake": c}}
}

func TestEnabledDropsDisabledActions(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{
		decl("fake_read", ActionRead),
		decl("fake_write", ActionWrite),
		decl("fake_nuke", ActionDestructive),
	}})

	got := r.Enabled(capsCfg(Caps{Read: true, Write: true}))
	names := map[string]bool{}
	for _, g := range got {
		names[g.Decl.Name] = true
	}
	if !names["fake_read"] || !names["fake_write"] {
		t.Errorf("enabled = %v, want read and write present", names)
	}
	if names["fake_nuke"] {
		t.Error("destructive tool registered while destructive=false")
	}
}

func TestEnabledEmptyWhenDomainFullyDisabled(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{decl("fake_read", ActionRead)}})
	if got := r.Enabled(capsCfg(Caps{})); len(got) != 0 {
		t.Errorf("enabled = %d tools, want 0", len(got))
	}
}

func TestSchemaBuiltFromCapsOmitsDestructiveProperty(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{decl("fake_update", ActionWrite)}})

	off := r.Enabled(capsCfg(Caps{Write: true}))
	if len(off) != 1 {
		t.Fatalf("got %d tools", len(off))
	}
	if _, ok := off[0].Schema.Properties["description"]; ok {
		t.Error("description must be absent from the schema when destructive=false")
	}

	on := r.Enabled(capsCfg(Caps{Write: true, Destructive: true}))
	if _, ok := on[0].Schema.Properties["description"]; !ok {
		t.Error("description must be present when destructive=true")
	}
}

func TestObjectSchemaRejectsUnknownProperties(t *testing.T) {
	s := ObjectSchema(map[string]*jsonschema.Schema{"key": {Type: "string"}}, []string{"key"})

	// Asserting AdditionalProperties is non-nil is not enough: a permissive
	// schema such as {} is also non-nil and accepts everything. Resolve the
	// schema and validate real documents against it.
	resolved, err := s.Resolve(&jsonschema.ResolveOptions{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := resolved.Validate(map[string]any{"key": "PROJ-1"}); err != nil {
		t.Errorf("a valid object must pass: %v", err)
	}
	if err := resolved.Validate(map[string]any{"key": "PROJ-1", "description": "x"}); err == nil {
		t.Fatal("an unknown property must fail validation; capability gating depends on it")
	}
}

// A tool spanning write and destructive must survive when only one of them is
// enabled. Testing Schema(caps) directly cannot catch this: the registry drops
// the tool before the schema is ever built.
func TestEnabledKeepsMultiClassToolWhenOnlyOneClassIsOn(t *testing.T) {
	multi := ToolDecl{
		Name:    "fake_update",
		Actions: []Action{ActionWrite, ActionDestructive},
		Schema: func(c Caps) *jsonschema.Schema {
			props := map[string]*jsonschema.Schema{"key": {Type: "string"}}
			if c.Write {
				props["assignee"] = &jsonschema.Schema{Type: "string"}
			}
			if c.Destructive {
				props["description"] = &jsonschema.Schema{Type: "string"}
			}
			return ObjectSchema(props, []string{"key"})
		},
		Handle: func(context.Context, json.RawMessage) (any, error) { return nil, nil },
	}
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{multi}})

	destructiveOnly := r.Enabled(capsCfg(Caps{Destructive: true}))
	if len(destructiveOnly) != 1 {
		t.Fatalf("destructive-only: registered %d tools, want 1", len(destructiveOnly))
	}
	props := destructiveOnly[0].Schema.Properties
	if _, ok := props["description"]; !ok {
		t.Error("destructive-only: description must be present")
	}
	if _, ok := props["assignee"]; ok {
		t.Error("destructive-only: assignee must be absent")
	}

	writeOnly := r.Enabled(capsCfg(Caps{Write: true}))
	if len(writeOnly) != 1 {
		t.Fatalf("write-only: registered %d tools, want 1", len(writeOnly))
	}
	if _, ok := writeOnly[0].Schema.Properties["description"]; ok {
		t.Error("write-only: description must be absent")
	}

	if got := r.Enabled(capsCfg(Caps{Read: true})); len(got) != 0 {
		t.Errorf("read-only: registered %d tools, want 0", len(got))
	}
}

func TestDomainsListedForConfigDerivation(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "jira"})
	r.Register(fakeModule{domain: "confluence"})
	got := r.Domains()
	if len(got) != 2 || got[0] != "jira" || got[1] != "confluence" {
		t.Errorf("Domains() = %v, want registration order", got)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run 'TestEnabled|TestSchema|TestObjectSchema|TestDomains' -v`
Expected: FAIL — `undefined: Registry`, `undefined: ObjectSchema`.

- [x] **Step 3: Write minimal implementation**

```go
package core

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// Action is a tool's capability class.
type Action int

const (
	// ActionRead returns data and changes nothing.
	ActionRead Action = iota
	// ActionWrite is additive and reversible.
	ActionWrite
	// ActionDestructive overwrites or moves state that is hard to recover.
	ActionDestructive
)

// String renders an Action for logs and errors.
func (a Action) String() string {
	switch a {
	case ActionRead:
		return "read"
	case ActionWrite:
		return "write"
	case ActionDestructive:
		return "destructive"
	}
	return "unknown"
}

func (a Action) allowedBy(c Caps) bool {
	switch a {
	case ActionRead:
		return c.Read
	case ActionWrite:
		return c.Write
	case ActionDestructive:
		return c.Destructive
	}
	return false
}

// enabled reports whether any of the tool's action classes is permitted.
func (d ToolDecl) enabled(c Caps) bool {
	for _, a := range d.Actions {
		if a.allowedBy(c) {
			return true
		}
	}
	return false
}

// actionNames renders the declared classes for logging.
func (d ToolDecl) actionNames() string {
	names := make([]string, 0, len(d.Actions))
	for _, a := range d.Actions {
		names = append(names, a.String())
	}
	return strings.Join(names, "+")
}

// ToolDecl is a tool a module offers. It is a declaration, not a registration:
// core decides whether it reaches the MCP server.
type ToolDecl struct {
	Name string
	// Actions is every class this tool spans. The tool is registered when at
	// least one of them is enabled, and Schema then advertises only the
	// properties those enabled classes permit. A tool spanning write and
	// destructive must list both, or it disappears entirely when only one is
	// enabled.
	Actions     []Action
	Description string
	// Schema builds the input schema from the domain's enabled capabilities,
	// so a tool spanning two action classes can advertise only the properties
	// its enabled classes permit.
	Schema func(Caps) *jsonschema.Schema
	// Handle receives the raw validated arguments.
	Handle func(context.Context, json.RawMessage) (any, error)
}

// Module is a product integration. A Module must not import the MCP SDK, read
// the environment, build an HTTP client, or log.
type Module interface {
	Domain() string
	Tools() []ToolDecl
}

// Registered is a tool that survived gating, with its schema already built.
type Registered struct {
	Domain string
	Decl   ToolDecl
	Schema *jsonschema.Schema
}

// Registry holds modules in registration order.
type Registry struct {
	modules []Module
}

// Register adds a module. Call before Domains or Enabled.
func (r *Registry) Register(m Module) { r.modules = append(r.modules, m) }

// Domains returns the registered domain names, used to derive config keys.
func (r *Registry) Domains() []string {
	out := make([]string, 0, len(r.modules))
	for _, m := range r.modules {
		out = append(out, m.Domain())
	}
	return out
}

// Enabled returns only the tools whose action class is enabled for their
// domain, with schemas built from that domain's capabilities. A tool that is
// not returned here is never registered with the MCP server, so it is absent
// from tools/list and unknown to the dispatcher.
func (r *Registry) Enabled(cfg Config) []Registered {
	var out []Registered
	for _, m := range r.modules {
		caps := cfg.Domains[m.Domain()]
		if !caps.Any() {
			continue
		}
		for _, d := range m.Tools() {
			if !d.enabled(caps) {
				continue
			}
			out = append(out, Registered{Domain: m.Domain(), Decl: d, Schema: d.Schema(caps)})
		}
	}
	return out
}

// ObjectSchema builds an object schema that rejects unknown properties.
//
// AdditionalProperties is mandatory here. JSON Schema accepts unknown
// properties by default, so a property omitted from Properties would still
// validate and still unmarshal into the handler's struct — defeating the
// capability gating entirely.
func ObjectSchema(props map[string]*jsonschema.Schema, required []string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:                 "object",
		Properties:           props,
		Required:             required,
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
	}
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -v`
Expected: PASS. If `AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}` does not marshal to `false` or an always-failing schema, check `jsonschema.Schema` for a boolean-schema helper and use that instead; the requirement is that unknown properties are rejected, not a specific representation.

- [x] **Step 5: Three-engine review gate**

Do not commit yet. Dispatch three independent reviews of *this task's diff only*, in parallel, one
per engine. Each gets the same diff and the task's own "Interfaces" block, and a different lens.

```
Agent(model="fable",  prompt=<completeness lens>)   # spec coverage, missing tests, error paths
Agent(model="haiku",  prompt=<consistency lens>)    # signatures, types, imports, helper clashes
Agent(subagent_type="claude", prompt=<relay>)       # wraps mcp__codex__codex: Go and API correctness
```

Give every reviewer this preamble:

> Review the diff below against `docs/plan/SPEC.md` and this task's Interfaces block. Report findings
> only; do not rewrite. For each: SEVERITY (blocking | important | minor), WHERE (file:line), WHAT
> (one or two sentences), FIX (the concrete change). No praise, no summary. If you find nothing real,
> say so rather than padding.

Then the lens, one per engine:

- **fable** — completeness: does this task implement every spec requirement it claims? What
  behaviour has no test? What happens on a 4xx, a 5xx, a cancelled context, an empty result?
- **haiku** — consistency: do the signatures, types, field names and imports match what earlier
  tasks defined and later tasks will call? Any helper defined twice in one package? Any import
  declared but unused, which fails to compile in Go?
- **codex** — correctness: will this compile? Is the SDK, goldmark, `x/net/html` and Atlassian REST
  usage right? Does each test actually test what it claims, or would it pass regardless of the
  implementation?

Then:

1. Triage every finding. Blocking and important ones get fixed before the commit. Minor ones are
   fixed or recorded with a reason.
2. **Verify any claim about a library's behaviour against that library's source** before acting on
   it. Review 01 found a blocking defect that only source-reading could confirm, and one false
   positive that only source-reading could refute. A reviewer's confident claim is a lead, not a
   fact.
3. Re-run `go test ./...` for the packages this task touches.
4. Append the findings and their dispositions to `docs/plan/REVIEW-LOG.md`, in the same table shape
   as `REVIEW-01-findings.md`, noting which engine found what and where they agreed. Agreement
   across engines is the strongest signal available.

The gate is the review, never a rubber stamp: "three reviews ran and found nothing blocking" is a
complete and valid outcome, recorded in one line.

- [ ] **Step 6: Commit**

```bash
git add internal/core/registry.go internal/core/registry_test.go
git commit -m "feat(core): module registry gating tools by domain and action class"
```

---

### Task 6: MCP server wiring

**Files:**
- Create: `internal/core/server.go`
- Test: `internal/core/server_test.go`
- **`cmd/atlassian-mcp-lite/main.go` moved to Task 13** (decided 2026-09-01, with the user). It
  imports `internal/jira` and `internal/confluence`, which do not exist until Tasks 9 and 12, so
  landing it here turns `make lint`, `make test` and `make build` red repo-wide — measured:
  `typecheck: 2` from golangci-lint, and a build failure for the test binary — and keeps them red
  for seven tasks, costing every intervening task its Step 4 verification signal. Its content is
  unchanged; only the task it lands in moved. See Task 13, Step 3.

**Interfaces:**
- Consumes: `core.Registry`, `core.Config`, `core.Logger`, `core.Registered` (Tasks 1–5).
- Produces: `core.NewServer(cfg Config, reg *Registry, log *Logger) (*mcp.Server, int, error)` returning the server and the count of registered tools. `core.Serve(ctx context.Context, s *mcp.Server) error`.

- [x] **Step 1: Write the failing test**

```go
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNewServerRegistersOnlyEnabledTools(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{
		decl("fake_read", ActionRead),
		decl("fake_nuke", ActionDestructive),
	}})

	cfg := Config{Domains: map[string]Caps{"fake": {Read: true}}}
	var logs bytes.Buffer
	_, n, err := NewServer(cfg, r, NewLogger("debug", &logs))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if n != 1 {
		t.Errorf("registered %d tools, want 1", n)
	}
}

func TestServerRoundTripsAToolCall(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{{
		Name:        "fake_echo",
		Actions:      []Action{ActionRead},
		Description: "echo",
		Schema: func(Caps) *jsonschema.Schema {
			return ObjectSchema(map[string]*jsonschema.Schema{"key": {Type: "string"}}, []string{"key"})
		},
		Handle: func(_ context.Context, raw json.RawMessage) (any, error) {
			var in struct{ Key string }
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, err
			}
			return map[string]string{"echoed": in.Key}, nil
		},
	}}})

	cfg := Config{Domains: map[string]Caps{"fake": {Read: true}}}
	var logs bytes.Buffer
	srv, _, err := NewServer(cfg, r, NewLogger("debug", &logs))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	sess, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer sess.Close()

	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "fake_echo" {
		t.Fatalf("tools = %+v", tools.Tools)
	}

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "fake_echo",
		Arguments: map[string]any{"key": "PROJ-123"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %+v", res.Content)
	}
	// Assert the payload. Without this the test passes for any non-error
	// result and proves nothing about the round trip.
	if len(res.Content) == 0 {
		t.Fatal("no content returned")
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] is %T, want *mcp.TextContent", res.Content[0])
	}
	if !strings.Contains(text.Text, "PROJ-123") {
		t.Errorf("returned text = %q, want the echoed key", text.Text)
	}
}

func TestUnknownPropertyIsRejected(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{decl("fake_update", ActionWrite)}})
	cfg := Config{Domains: map[string]Caps{"fake": {Write: true}}}
	var logs bytes.Buffer
	srv, _, err := NewServer(cfg, r, NewLogger("info", &logs))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	srv.Connect(ctx, st, nil)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	sess, _ := client.Connect(ctx, ct, nil)
	defer sess.Close()

	// description is absent from the schema because destructive=false. It must
	// be rejected, not silently accepted and applied.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "fake_update",
		Arguments: map[string]any{"key": "PROJ-123", "description": "overwritten"},
	})
	if err == nil && !res.IsError {
		t.Fatal("unknown property must be rejected; capability gating is bypassed otherwise")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run 'TestNewServer|TestServerRoundTrips|TestUnknownProperty' -v`
Expected: FAIL — `undefined: NewServer`.

- [x] **Step 3: Write minimal implementation**

`internal/core/server.go`:

```go
package core

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version lives in client.go (Task 3), where the User-Agent needs it. Declaring
// it again here would be a duplicate declaration in the same package.

// NewServer builds an MCP server holding exactly the tools enabled by cfg. It
// returns the server and the number of tools registered.
func NewServer(cfg Config, reg *Registry, log *Logger) (*mcp.Server, int, error) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "atlassian-mcp-lite", Version: Version}, nil)

	enabled := reg.Enabled(cfg)
	for _, r := range enabled {
		r := r
		tool := &mcp.Tool{
			Name:        r.Decl.Name,
			Description: r.Decl.Description,
			InputSchema: r.Schema,
		}

		// The generic mcp.AddTool is mandatory here, not a convenience.
		// (*Server).AddTool is the low-level API and performs NO input
		// validation — its handler receives raw arguments — so a property
		// absent from our capability-built schema would still reach the
		// handler and defeat the gating entirely. The generic form resolves
		// tool.InputSchema and validates every call against it before the
		// handler runs. In is json.RawMessage so the validated bytes pass
		// straight through to the module; Out is any so no output schema is
		// inferred.
		handler := func(ctx context.Context, _ *mcp.CallToolRequest, in json.RawMessage) (*mcp.CallToolResult, any, error) {
			out, err := r.Decl.Handle(ctx, in)
			if err != nil {
				log.Errorf("%s: %v", r.Decl.Name, err)
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
				}, nil, nil
			}
			raw, mErr := json.Marshal(out)
			if mErr != nil {
				return nil, nil, fmt.Errorf("%s: marshal result: %w", r.Decl.Name, mErr)
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}},
			}, nil, nil
		}
		mcp.AddTool(srv, tool, handler)
		log.Debugf("registered %s (%s/%s)", r.Decl.Name, r.Domain, r.Decl.actionNames())
	}
	return srv, len(enabled), nil
}

// Serve runs the server over stdio until the client disconnects.
func Serve(ctx context.Context, s *mcp.Server) error {
	return s.Run(ctx, &mcp.StdioTransport{})
}
```

`cmd/atlassian-mcp-lite/main.go`:

```go
// Command atlassian-mcp-lite is a minimal MCP server for Jira and Confluence.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/OxCom/atlassian-mcp-lite/internal/confluence"
	"github.com/OxCom/atlassian-mcp-lite/internal/core"
	"github.com/OxCom/atlassian-mcp-lite/internal/jira"
)

func main() {
	// Logs go to stderr: stdout carries the MCP protocol stream.
	bootLog := core.NewLogger(os.Getenv("ATLAS_LOG"), os.Stderr)

	reg := &core.Registry{}
	// Adding a module is one line here plus its package. Capability env vars
	// are derived from the domain name, so core needs no change.
	reg.Register(jira.New())
	reg.Register(confluence.New())

	cfg, err := core.Load(os.Getenv, reg.Domains())
	if err != nil {
		bootLog.Errorf("configuration: %v", err)
		os.Exit(1)
	}

	// The token is given to the logger so it is redacted from every message,
	// including upstream error bodies that echo it back.
	log := core.NewLogger(cfg.LogLevel, os.Stderr, cfg.Token)
	client := core.NewClient(cfg, log)

	reg = &core.Registry{}
	reg.Register(jira.NewWith(cfg, client))
	reg.Register(confluence.NewWith(cfg, client))

	srv, n, err := core.NewServer(cfg, reg, log)
	if err != nil {
		log.Errorf("build server: %v", err)
		os.Exit(1)
	}
	if n == 0 {
		log.Errorf("no tools enabled: check ATLAS_<DOMAIN>_{READ,WRITE,DESTRUCTIVE}")
		os.Exit(1)
	}
	log.Debugf("serving %d tools over stdio", n)

	// As PID 1 in a scratch container, ignoring SIGTERM means docker stop
	// hangs until the kill timeout on every shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := core.Serve(ctx, srv); err != nil && !errors.Is(err, context.Canceled) {
		log.Errorf("serve: %v", err)
		os.Exit(1)
	}
}
```

Note on the two-phase registry: `core.Load` needs the domain list before a client exists, and modules need the client to build handlers. `New()` returns a declaration-only module (domain and tool names, no working handlers); `NewWith(cfg, client)` returns the functional one. Tasks 7–11 implement both constructors.

- [x] **Step 4: Run tests to verify they pass**

Run: `make lint && make test`
Expected: PASS, 0 lint issues. With `main.go` deferred to Task 13 there is no uncompilable package, so the whole toolchain stays green from here on. If `mcp.NewInMemoryTransports` or the generic `mcp.AddTool` differ from the signatures above, check the pinned SDK's godoc and adjust. Do **not** substitute `(*mcp.Server).AddTool` to make it compile: that is the non-validating path and it silently defeats the capability gating.

- [x] **Step 5: Three-engine review gate**

Do not commit yet. Dispatch three independent reviews of *this task's diff only*, in parallel, one
per engine. Each gets the same diff and the task's own "Interfaces" block, and a different lens.

```
Agent(model="fable",  prompt=<completeness lens>)   # spec coverage, missing tests, error paths
Agent(model="haiku",  prompt=<consistency lens>)    # signatures, types, imports, helper clashes
Agent(subagent_type="claude", prompt=<relay>)       # wraps mcp__codex__codex: Go and API correctness
```

Give every reviewer this preamble:

> Review the diff below against `docs/plan/SPEC.md` and this task's Interfaces block. Report findings
> only; do not rewrite. For each: SEVERITY (blocking | important | minor), WHERE (file:line), WHAT
> (one or two sentences), FIX (the concrete change). No praise, no summary. If you find nothing real,
> say so rather than padding.

Then the lens, one per engine:

- **fable** — completeness: does this task implement every spec requirement it claims? What
  behaviour has no test? What happens on a 4xx, a 5xx, a cancelled context, an empty result?
- **haiku** — consistency: do the signatures, types, field names and imports match what earlier
  tasks defined and later tasks will call? Any helper defined twice in one package? Any import
  declared but unused, which fails to compile in Go?
- **codex** — correctness: will this compile? Is the SDK, goldmark, `x/net/html` and Atlassian REST
  usage right? Does each test actually test what it claims, or would it pass regardless of the
  implementation?

Then:

1. Triage every finding. Blocking and important ones get fixed before the commit. Minor ones are
   fixed or recorded with a reason.
2. **Verify any claim about a library's behaviour against that library's source** before acting on
   it. Review 01 found a blocking defect that only source-reading could confirm, and one false
   positive that only source-reading could refute. A reviewer's confident claim is a lead, not a
   fact.
3. Re-run `go test ./...` for the packages this task touches.
4. Append the findings and their dispositions to `docs/plan/REVIEW-LOG.md`, in the same table shape
   as `REVIEW-01-findings.md`, noting which engine found what and where they agreed. Agreement
   across engines is the strongest signal available.

The gate is the review, never a rubber stamp: "three reviews ran and found nothing blocking" is a
complete and valid outcome, recorded in one line.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/core/server.go internal/core/server_test.go
git commit -m "feat(core): MCP server wiring over stdio with schema-validated dispatch"
```

---

### Task 7: Markdown to wiki markup, via goldmark

**Files:**
- Create: `internal/markup/to_wiki.go`
- Test: `internal/markup/to_wiki_test.go`
- Modify: `go.mod` (add goldmark)

**Interfaces:**
- Consumes: nothing.
- Produces: `markup.ToWiki(md string) string`.

Both products accept `representation: "wiki"`, so this one function serves every write in both modules. goldmark parses markdown into an AST; a renderer walks it. No regex parsing.

- [x] **Step 1: Write the failing test**

```go
package markup

import "testing"

func TestToWikiHeadings(t *testing.T) {
	for in, want := range map[string]string{
		"# One":      "h1. One",
		"## Two":     "h2. Two",
		"###### Six": "h6. Six",
	} {
		if got := ToWiki(in); got != want {
			t.Errorf("ToWiki(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToWikiInlineEmphasis(t *testing.T) {
	for in, want := range map[string]string{
		"**bold**":            "*bold*",
		"*italic*":            "_italic_",
		"_italic_":            "_italic_",
		"`code`":              "{{code}}",
		"**bold** and `code`": "*bold* and {{code}}",
	} {
		if got := ToWiki(in); got != want {
			t.Errorf("ToWiki(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToWikiLink(t *testing.T) {
	if got, want := ToWiki("see [the docs](https://example.com/x)"), "see [the docs|https://example.com/x]"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWikiFencedCodeKeepsLanguage(t *testing.T) {
	if got, want := ToWiki("```go\nfmt.Println(1)\n```"), "{code:go}\nfmt.Println(1)\n{code}"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWikiFencedCodeWithoutLanguage(t *testing.T) {
	if got, want := ToWiki("```\nplain\n```"), "{code}\nplain\n{code}"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWikiCodeContentNotTransformed(t *testing.T) {
	in := "```\n# not a heading\n**not bold**\n```"
	want := "{code}\n# not a heading\n**not bold**\n{code}"
	if got := ToWiki(in); got != want {
		t.Errorf("code contents must be literal: got %q, want %q", got, want)
	}
}

func TestToWikiLists(t *testing.T) {
	if got, want := ToWiki("- one\n- two\n  - nested"), "* one\n* two\n** nested"; got != want {
		t.Errorf("unordered: got %q, want %q", got, want)
	}
	if got, want := ToWiki("1. first\n2. second"), "# first\n# second"; got != want {
		t.Errorf("ordered: got %q, want %q", got, want)
	}
}

// Wiki markup encodes list nesting as the full marker ancestry, so a list of a
// different type nested inside another must not repeat the outer marker.
func TestToWikiMixedNestedListMarkers(t *testing.T) {
	if got, want := ToWiki("1. outer\n   - inner"), "# outer\n#* inner"; got != want {
		t.Errorf("unordered in ordered: got %q, want %q", got, want)
	}
	if got, want := ToWiki("- outer\n   1. inner"), "* outer\n*# inner"; got != want {
		t.Errorf("ordered in unordered: got %q, want %q", got, want)
	}
}

func TestToWikiRawHTMLIsNotDropped(t *testing.T) {
	// The contract is never to lose content, even for unsupported syntax.
	for _, in := range []string{"before <span>x</span> after", "<div>block content</div>"} {
		got := ToWiki(in)
		if !contains(got, "content") && !contains(got, "x") {
			t.Errorf("ToWiki(%q) = %q, dropped the content", in, got)
		}
	}
}

func TestToWikiBlockquoteAndRule(t *testing.T) {
	if got, want := ToWiki("> quoted"), "bq. quoted"; got != want {
		t.Errorf("blockquote: got %q, want %q", got, want)
	}
	if got, want := ToWiki("---"), "----"; got != want {
		t.Errorf("rule: got %q, want %q", got, want)
	}
}

func TestToWikiTable(t *testing.T) {
	if got, want := ToWiki("| A | B |\n|---|---|\n| 1 | 2 |"), "||A||B||\n|1|2|"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWikiParagraphsSeparated(t *testing.T) {
	if got, want := ToWiki("one\n\ntwo"), "one\n\ntwo"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWikiUnsupportedInlinePassesThroughAsText(t *testing.T) {
	// Strikethrough is not in the subset. Its text must survive.
	got := ToWiki("~~gone~~ but kept")
	if !contains(got, "gone") || !contains(got, "kept") {
		t.Errorf("content dropped: %q", got)
	}
}

func TestToWikiEmpty(t *testing.T) {
	if got := ToWiki(""); got != "" {
		t.Errorf("ToWiki(\"\") = %q", got)
	}
}

func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/markup/ -v`
Expected: FAIL — package does not exist.

- [x] **Step 3: Write minimal implementation**

```bash
cd /var/www/external/atlassian-mcp-lite
docker run --rm -v "$PWD:/src" -w /src \
  golang:1.27-trixie go get github.com/yuin/goldmark@v1.8.5
```

```go
// Package markup converts between markdown, which the tools speak, and the
// formats Atlassian accepts. Neither Jira nor Confluence accepts markdown over
// REST, so every write is converted to wiki markup, which both products accept
// and expand server-side.
//
// Markdown is parsed with goldmark rather than regex: regex markdown parsing
// silently corrupts nested and escaped constructs. Unsupported nodes render
// their text content rather than being dropped.
package markup

import (
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

var mdParser = goldmark.New(goldmark.WithExtensions(extension.Table))

// ToWiki converts a markdown document to Atlassian wiki markup.
func ToWiki(md string) string {
	if strings.TrimSpace(md) == "" {
		return ""
	}
	src := []byte(md)
	doc := mdParser.Parser().Parse(text.NewReader(src))

	var b strings.Builder
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		writeBlock(&b, n, src, "")
		if n.NextSibling() != nil {
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// writeBlock renders one block node. listPrefix carries the accumulated list
// markers of the ancestors, because Atlassian wiki markup encodes nesting as
// the full ancestry: an unordered list inside an ordered one is "#*", not "**".
func writeBlock(b *strings.Builder, n ast.Node, src []byte, listPrefix string) {
	switch node := n.(type) {
	case *ast.Heading:
		b.WriteString("h" + strconv.Itoa(node.Level) + ". ")
		writeInline(b, node, src)
		b.WriteString("\n")

	case *ast.Paragraph:
		writeInline(b, node, src)
		b.WriteString("\n")

	case *ast.TextBlock:
		writeInline(b, node, src)

	case *ast.Blockquote:
		for c := node.FirstChild(); c != nil; c = c.NextSibling() {
			b.WriteString("bq. ")
			writeInline(b, c, src)
			b.WriteString("\n")
		}

	case *ast.ThematicBreak:
		b.WriteString("----\n")

	case *ast.FencedCodeBlock:
		lang := string(node.Language(src))
		if lang != "" {
			b.WriteString("{code:" + lang + "}\n")
		} else {
			b.WriteString("{code}\n")
		}
		b.WriteString(rawLines(node, src))
		b.WriteString("{code}\n")

	case *ast.CodeBlock:
		b.WriteString("{code}\n")
		b.WriteString(rawLines(node, src))
		b.WriteString("{code}\n")

	case *ast.List:
		marker := "*"
		if node.IsOrdered() {
			marker = "#"
		}
		prefix := listPrefix + marker
		for item := node.FirstChild(); item != nil; item = item.NextSibling() {
			b.WriteString(prefix + " ")
			for c := item.FirstChild(); c != nil; c = c.NextSibling() {
				if sub, ok := c.(*ast.List); ok {
					b.WriteString("\n")
					writeBlock(b, sub, src, prefix)
					continue
				}
				writeInline(b, c, src)
			}
			if !strings.HasSuffix(b.String(), "\n") {
				b.WriteString("\n")
			}
		}

	case *extast.Table:
		for row := node.FirstChild(); row != nil; row = row.NextSibling() {
			_, header := row.(*extast.TableHeader)
			sep := "|"
			if header {
				sep = "||"
			}
			b.WriteString(sep)
			for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
				writeInline(b, cell, src)
				if cell.NextSibling() != nil || header {
					b.WriteString(sep)
				} else {
					b.WriteString("|")
				}
			}
			b.WriteString("\n")
		}

	case *ast.HTMLBlock:
		// Not in the subset. Emit the literal source rather than dropping it:
		// an HTMLBlock has no inline children, so recursing would lose the
		// whole block.
		b.WriteString(rawLines(node, src))

	default:
		// Unknown block: emit its text so nothing is lost.
		writeInline(b, n, src)
		b.WriteString("\n")
	}
}

func writeInline(b *strings.Builder, n ast.Node, src []byte) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch node := c.(type) {
		case *ast.Text:
			b.Write(node.Segment.Value(src))
			if node.SoftLineBreak() || node.HardLineBreak() {
				b.WriteString("\n")
			}
		case *ast.Emphasis:
			if node.Level == 2 {
				b.WriteString("*")
				writeInline(b, node, src)
				b.WriteString("*")
			} else {
				b.WriteString("_")
				writeInline(b, node, src)
				b.WriteString("_")
			}
		case *ast.CodeSpan:
			b.WriteString("{{")
			writeInline(b, node, src)
			b.WriteString("}}")
		case *ast.Link:
			b.WriteString("[")
			writeInline(b, node, src)
			b.WriteString("|" + string(node.Destination) + "]")
		case *ast.AutoLink:
			b.WriteString("[" + string(node.URL(src)) + "]")
		case *ast.RawHTML:
			// Not in the subset, but the contract is never to drop content, so
			// the literal source is emitted.
			for i := 0; i < node.Segments.Len(); i++ {
				b.Write(node.Segments.At(i).Value(src))
			}
		default:
			// Unknown inline: recurse so its text survives.
			writeInline(b, c, src)
		}
	}
}

// rawLines returns a block node's literal source lines.
func rawLines(n ast.Node, src []byte) string {
	var b strings.Builder
	l := n.Lines()
	for i := 0; i < l.Len(); i++ {
		seg := l.At(i)
		b.Write(seg.Value(src))
	}
	return b.String()
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/markup/ -v`
Expected: PASS. Two likely adjustments: goldmark's table extension node types live in `extension/ast`, and list items wrap their content in `ast.TextBlock`. If a test fails on a stray newline, fix the renderer's newline handling — do not change the expected wiki output, which is what Atlassian requires.

- [x] **Step 5: Three-engine review gate**

Do not commit yet. Dispatch three independent reviews of *this task's diff only*, in parallel, one
per engine. Each gets the same diff and the task's own "Interfaces" block, and a different lens.

```
Agent(model="fable",  prompt=<completeness lens>)   # spec coverage, missing tests, error paths
Agent(model="haiku",  prompt=<consistency lens>)    # signatures, types, imports, helper clashes
Agent(subagent_type="claude", prompt=<relay>)       # wraps mcp__codex__codex: Go and API correctness
```

Give every reviewer this preamble:

> Review the diff below against `docs/plan/SPEC.md` and this task's Interfaces block. Report findings
> only; do not rewrite. For each: SEVERITY (blocking | important | minor), WHERE (file:line), WHAT
> (one or two sentences), FIX (the concrete change). No praise, no summary. If you find nothing real,
> say so rather than padding.

Then the lens, one per engine:

- **fable** — completeness: does this task implement every spec requirement it claims? What
  behaviour has no test? What happens on a 4xx, a 5xx, a cancelled context, an empty result?
- **haiku** — consistency: do the signatures, types, field names and imports match what earlier
  tasks defined and later tasks will call? Any helper defined twice in one package? Any import
  declared but unused, which fails to compile in Go?
- **codex** — correctness: will this compile? Is the SDK, goldmark, `x/net/html` and Atlassian REST
  usage right? Does each test actually test what it claims, or would it pass regardless of the
  implementation?

Then:

1. Triage every finding. Blocking and important ones get fixed before the commit. Minor ones are
   fixed or recorded with a reason.
2. **Verify any claim about a library's behaviour against that library's source** before acting on
   it. Review 01 found a blocking defect that only source-reading could confirm, and one false
   positive that only source-reading could refute. A reviewer's confident claim is a lead, not a
   fact.
3. Re-run `go test ./...` for the packages this task touches.
4. Append the findings and their dispositions to `docs/plan/REVIEW-LOG.md`, in the same table shape
   as `REVIEW-01-findings.md`, noting which engine found what and where they agreed. Agreement
   across engines is the strongest signal available.

The gate is the review, never a rubber stamp: "three reviews ran and found nothing blocking" is a
complete and valid outcome, recorded in one line.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/markup/to_wiki.go internal/markup/to_wiki_test.go
git commit -m "feat(markup): markdown to wiki markup via goldmark AST"
```

---

### Task 8: Wiki and HTML to markdown

**Files:**
- Create: `internal/markup/from_wiki.go`
- Create: `internal/markup/from_html.go`
- Test: `internal/markup/from_wiki_test.go`
- Test: `internal/markup/from_html_test.go`
- Modify: `go.mod` (add `golang.org/x/net`)

**Interfaces:**
- Consumes: `markup.ToWiki` (Task 7) for round-trip tests.
- Produces: `markup.FromWiki(wiki string) string`, `markup.FromHTML(h string) string`.

`FromWiki` serves Jira reads, where v2 returns wiki markup. Wiki markup is line-oriented, so a line scanner is the right tool and no parser library exists for it. `FromHTML` serves Confluence reads, which request `body-format=view` — HTML with macros already expanded by Atlassian, parsed with `x/net/html`.

- [x] **Step 1: Write the failing test**

`from_wiki_test.go`:

```go
package markup

import "testing"

func TestFromWikiHeading(t *testing.T) {
	if got, want := FromWiki("h3. Objective"), "### Objective"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFromWikiInline(t *testing.T) {
	for in, want := range map[string]string{
		"*bold*":   "**bold**",
		"_italic_": "*italic*",
		"{{code}}": "`code`",
		"[the docs|https://example.com/x]": "[the docs](https://example.com/x)",
	} {
		if got := FromWiki(in); got != want {
			t.Errorf("FromWiki(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFromWikiCodeBlock(t *testing.T) {
	if got, want := FromWiki("{code:go}\nfmt.Println(1)\n{code}"), "```go\nfmt.Println(1)\n```"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFromWikiCodeContentNotTransformed(t *testing.T) {
	in := "{code}\n*not bold*\n{code}"
	want := "```\n*not bold*\n```"
	if got := FromWiki(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFromWikiLists(t *testing.T) {
	if got, want := FromWiki("* one\n** nested"), "- one\n  - nested"; got != want {
		t.Errorf("unordered: got %q, want %q", got, want)
	}
	if got, want := FromWiki("# first\n# second"), "1. first\n1. second"; got != want {
		t.Errorf("ordered: got %q, want %q", got, want)
	}
}

func TestFromWikiRule(t *testing.T) {
	if got, want := FromWiki("----"), "---"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFromWikiTable(t *testing.T) {
	in := "||A||B||\n|1|2|"
	want := "| A | B |\n|---|---|\n| 1 | 2 |"
	if got := FromWiki(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFromWikiProtectsCodeAndLinks(t *testing.T) {
	// Emphasis conversion must not reach inside a code span or a URL.
	if got, want := FromWiki("{{*x*}}"), "`*x*`"; got != want {
		t.Errorf("code span: got %q, want %q", got, want)
	}
	if got, want := FromWiki("[t|https://e.com/a_b_c]"), "[t](https://e.com/a_b_c)"; got != want {
		t.Errorf("link with underscores: got %q, want %q", got, want)
	}
}

func TestRoundTripStability(t *testing.T) {
	// Every construct the spec lists as supported must survive md -> wiki -> md.
	for _, md := range []string{
		"# One",
		"## Two",
		"### Objective",
		"#### Four",
		"##### Five",
		"###### Six",
		"a **bold** word",
		"an *italic* word",
		"some `code` inline",
		"see [docs](https://example.com/a)",
		"- one\n- two",
		"1. first\n1. second",
		"- outer\n  - inner",
		"| A | B |\n|---|---|\n| 1 | 2 |",
		"```go\nx := 1\n```",
		"---",
	} {
		if got := FromWiki(ToWiki(md)); got != md {
			t.Errorf("round trip changed document:\n in:  %q\n out: %q", md, got)
		}
	}
}
```

`from_html_test.go`:

```go
package markup

import (
	"strings"
	"testing"
)

func TestFromHTMLHeadingAndEmphasis(t *testing.T) {
	got := FromHTML("<h2>Title</h2><p>Some <strong>bold</strong> and <em>italic</em>.</p>")
	for _, want := range []string{"## Title", "**bold**", "*italic*"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestFromHTMLListAndLink(t *testing.T) {
	got := FromHTML(`<ul><li>one</li><li>two</li></ul><p><a href="https://example.com">link</a></p>`)
	for _, want := range []string{"- one", "- two", "[link](https://example.com)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestFromHTMLOrderedList(t *testing.T) {
	got := FromHTML("<ol><li>first</li><li>second</li></ol>")
	if !strings.Contains(got, "1. first") || !strings.Contains(got, "2. second") {
		t.Errorf("ordered list wrong: %q", got)
	}
}

func TestFromHTMLCodeBlock(t *testing.T) {
	got := FromHTML("<pre><code>x := 1</code></pre>")
	if !strings.Contains(got, "```") || !strings.Contains(got, "x := 1") {
		t.Errorf("code block missing: %q", got)
	}
}

func TestFromHTMLTableIsValidMarkdown(t *testing.T) {
	got := FromHTML("<table><thead><tr><th>A</th><th>B</th></tr></thead><tbody><tr><td>1</td><td>2</td></tr></tbody></table>")
	for _, want := range []string{"| A | B |", "|---|---|", "| 1 | 2 |"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestFromHTMLHeaderlessTableStillGetsSeparator(t *testing.T) {
	// Markdown has no headerless table form, so the first row becomes the header.
	got := FromHTML("<table><tr><td>1</td><td>2</td></tr><tr><td>3</td><td>4</td></tr></table>")
	if !strings.Contains(got, "|---|---|") {
		t.Errorf("separator missing, output is not a valid table: %q", got)
	}
}

func TestFromHTMLUnknownTagsStrippedTextKept(t *testing.T) {
	got := FromHTML(`<div class="macro"><span>kept text</span></div>`)
	if !strings.Contains(got, "kept text") {
		t.Errorf("text must survive: %q", got)
	}
	if strings.Contains(got, "<") || strings.Contains(got, "class=") {
		t.Errorf("markup must not survive: %q", got)
	}
}

func TestFromHTMLDecodesEntities(t *testing.T) {
	got := FromHTML("<p>a &amp; b &lt; c &quot;d&quot;</p>")
	for _, want := range []string{"a & b", "< c", `"d"`} {
		if !strings.Contains(got, want) {
			t.Errorf("entity not decoded (%q): %q", want, got)
		}
	}
}

func TestFromHTMLScriptContentDropped(t *testing.T) {
	got := FromHTML("<p>before</p><script>alert('x')</script><p>after</p>")
	if strings.Contains(got, "alert") {
		t.Errorf("script content must be dropped: %q", got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("surrounding text must survive: %q", got)
	}
}

func TestFromHTMLEmpty(t *testing.T) {
	if got := FromHTML(""); got != "" {
		t.Errorf("FromHTML(\"\") = %q", got)
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/markup/ -run 'FromWiki|FromHTML|RoundTrip' -v`
Expected: FAIL — `undefined: FromWiki`, `undefined: FromHTML`.

- [x] **Step 3: Write minimal implementation**

```bash
docker run --rm -v "$PWD:/src" -w /src \
  golang:1.27-trixie go get golang.org/x/net@v0.58.0
```

`internal/markup/from_wiki.go`:

```go
package markup

import (
	"regexp"
	"strconv"
	"strings"
)

// Wiki markup is line-oriented, so a line scanner is appropriate here. This is
// not markdown parsing — there is no nesting to get wrong at block level.
var (
	reWikiHeading = regexp.MustCompile(`^h([1-6])\.\s+(.*)$`)
	reWikiFence   = regexp.MustCompile(`^\{code(?::([A-Za-z0-9+#._-]*))?\}$`)
	reWikiBold    = regexp.MustCompile(`\*([^*\n]+)\*`)
	reWikiItalic  = regexp.MustCompile(`_([^_\n]+)_`)
	reWikiCode    = regexp.MustCompile(`\{\{([^}\n]+)\}\}`)
	reWikiLink    = regexp.MustCompile(`\[([^\]|\n]+)\|([^\]\n]+)\]`)
	reWikiUL      = regexp.MustCompile(`^(\*+)\s+(.*)$`)
	reWikiOL      = regexp.MustCompile(`^(#+)\s+(.*)$`)
	reWikiQuote   = regexp.MustCompile(`^bq\.\s+(.*)$`)
	reWikiHeadRow = regexp.MustCompile(`^\s*\|\|.*\|\|\s*$`)
	reWikiBodyRow = regexp.MustCompile(`^\s*\|.*\|\s*$`)
)

// wikiTableRow splits a wiki table row on its separator and renders a markdown
// row. header also emits the separator line markdown requires.
func wikiTableRow(line string, header bool) []string {
	sep := "|"
	if header {
		sep = "||"
	}
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, sep)
	t = strings.TrimSuffix(t, sep)
	raw := strings.Split(t, sep)
	cells := make([]string, 0, len(raw))
	for _, c := range raw {
		cells = append(cells, inlineFromWiki(strings.TrimSpace(c)))
	}
	row := "| " + strings.Join(cells, " | ") + " |"
	if !header {
		return []string{row}
	}
	dashes := make([]string, len(cells))
	for i := range dashes {
		dashes[i] = "---"
	}
	return []string{row, "|" + strings.Join(dashes, "|") + "|"}
}

// FromWiki converts Atlassian wiki markup to markdown. Over the supported
// subset it is the inverse of ToWiki; anything else is passed through.
func FromWiki(wiki string) string {
	lines := strings.Split(wiki, "\n")
	out := make([]string, 0, len(lines))
	inCode := false

	for _, line := range lines {
		if m := reWikiFence.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			if inCode {
				out, inCode = append(out, "```"), false
			} else {
				out, inCode = append(out, "```"+m[1]), true
			}
			continue
		}
		if inCode {
			out = append(out, line)
			continue
		}
		if strings.TrimSpace(line) == "----" {
			out = append(out, "---")
			continue
		}
		// Tables: a "||" row is the header and also emits the markdown
		// separator; a "|" row is a body row. Checked before emphasis handling
		// so cell pipes are not mistaken for markup.
		if reWikiHeadRow.MatchString(line) {
			out = append(out, wikiTableRow(line, true)...)
			continue
		}
		if reWikiBodyRow.MatchString(line) {
			out = append(out, wikiTableRow(line, false)...)
			continue
		}
		if m := reWikiHeading.FindStringSubmatch(line); m != nil {
			out = append(out, strings.Repeat("#", int(m[1][0]-'0'))+" "+inlineFromWiki(m[2]))
			continue
		}
		if m := reWikiQuote.FindStringSubmatch(line); m != nil {
			out = append(out, "> "+inlineFromWiki(m[1]))
			continue
		}
		if m := reWikiOL.FindStringSubmatch(line); m != nil {
			out = append(out, strings.Repeat("  ", len(m[1])-1)+"1. "+inlineFromWiki(m[2]))
			continue
		}
		if m := reWikiUL.FindStringSubmatch(line); m != nil {
			out = append(out, strings.Repeat("  ", len(m[1])-1)+"- "+inlineFromWiki(m[2]))
			continue
		}
		out = append(out, inlineFromWiki(line))
	}
	if inCode {
		out = append(out, "```")
	}
	return strings.Join(out, "\n")
}

// inlineFromWiki converts span-level markup. Code spans and links are replaced
// with placeholders first: applying the emphasis regexes over them would
// rewrite markup inside {{...}} and mangle URLs containing * or _.
func inlineFromWiki(s string) string {
	var held []string
	protect := func(re *regexp.Regexp, render func([]string) string) {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			held = append(held, render(re.FindStringSubmatch(m)))
			return "\x00h" + strconv.Itoa(len(held)-1) + "\x00"
		})
	}
	protect(reWikiCode, func(g []string) string { return "`" + g[1] + "`" })
	protect(reWikiLink, func(g []string) string { return "[" + g[1] + "](" + g[2] + ")" })

	s = reWikiBold.ReplaceAllString(s, "**$1**")
	s = reWikiItalic.ReplaceAllString(s, "*$1*")

	for i, v := range held {
		s = strings.ReplaceAll(s, "\x00h"+strconv.Itoa(i)+"\x00", v)
	}
	return s
}
```

`internal/markup/from_html.go`:

```go
package markup

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// dropContent are elements whose text must never reach the model.
var dropContent = map[string]bool{"script": true, "style": true, "noscript": true}

// FromHTML converts Confluence rendered HTML (body-format=view) to markdown.
//
// view is requested rather than storage because Atlassian has already expanded
// macros, so no macro handling is needed. Parsing uses x/net/html: regex over
// HTML mishandles nesting and attribute edge cases.
func FromHTML(in string) string {
	if strings.TrimSpace(in) == "" {
		return ""
	}
	doc, err := html.Parse(strings.NewReader(in))
	if err != nil {
		// Parse rarely fails, but never lose the content: fall back to text.
		return strings.TrimSpace(in)
	}
	var b strings.Builder
	render(&b, doc, 0)
	return collapseBlankLines(strings.TrimSpace(b.String()))
}

func render(b *strings.Builder, n *html.Node, listDepth int) {
	switch n.Type {
	case html.TextNode:
		// Confluence view HTML emits non-breaking spaces liberally.
		b.WriteString(strings.ReplaceAll(n.Data, "\u00a0", " "))
		return
	case html.ElementNode:
		if dropContent[n.Data] {
			return
		}
	}

	switch n.Data {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		level, _ := strconv.Atoi(n.Data[1:])
		b.WriteString("\n" + strings.Repeat("#", level) + " ")
		renderChildren(b, n, listDepth)
		b.WriteString("\n")
		return
	case "p", "div":
		b.WriteString("\n")
		renderChildren(b, n, listDepth)
		b.WriteString("\n")
		return
	case "br":
		b.WriteString("\n")
		return
	case "hr":
		b.WriteString("\n---\n")
		return
	case "strong", "b":
		b.WriteString("**")
		renderChildren(b, n, listDepth)
		b.WriteString("**")
		return
	case "em", "i":
		b.WriteString("*")
		renderChildren(b, n, listDepth)
		b.WriteString("*")
		return
	case "code":
		if n.Parent != nil && n.Parent.Data == "pre" {
			renderChildren(b, n, listDepth)
			return
		}
		b.WriteString("`")
		renderChildren(b, n, listDepth)
		b.WriteString("`")
		return
	case "pre":
		b.WriteString("\n```\n")
		var inner strings.Builder
		renderChildren(&inner, n, listDepth)
		b.WriteString(strings.Trim(inner.String(), "\n"))
		b.WriteString("\n```\n")
		return
	case "a":
		var inner strings.Builder
		renderChildren(&inner, n, listDepth)
		b.WriteString("[" + strings.TrimSpace(inner.String()) + "](" + attr(n, "href") + ")")
		return
	case "ul", "ol":
		b.WriteString("\n")
		i := 1
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode || c.Data != "li" {
				continue
			}
			b.WriteString(strings.Repeat("  ", listDepth))
			if n.Data == "ol" {
				b.WriteString(strconv.Itoa(i) + ". ")
			} else {
				b.WriteString("- ")
			}
			var inner strings.Builder
			renderChildren(&inner, c, listDepth+1)
			b.WriteString(strings.TrimSpace(inner.String()))
			b.WriteString("\n")
			i++
		}
		return
	case "table":
		// Handled whole rather than per row, because a markdown table needs a
		// separator line after the header, and a row alone cannot know whether
		// it is the header.
		rows := collectRows(n, listDepth)
		if len(rows) == 0 {
			return
		}
		b.WriteString("\n")
		b.WriteString("| " + strings.Join(rows[0].cells, " | ") + " |\n")
		dashes := make([]string, len(rows[0].cells))
		for i := range dashes {
			dashes[i] = "---"
		}
		b.WriteString("|" + strings.Join(dashes, "|") + "|\n")
		for _, r := range rows[1:] {
			b.WriteString("| " + strings.Join(r.cells, " | ") + " |\n")
		}
		return
	}

	renderChildren(b, n, listDepth)
}

type htmlRow struct {
	cells  []string
	header bool
}

// collectRows walks a table for tr elements at any depth, so thead and tbody
// wrappers do not hide rows. When no th is present the first row is used as the
// header, because markdown has no headerless table form.
func collectRows(table *html.Node, listDepth int) []htmlRow {
	var rows []htmlRow
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.Data == "tr" {
				var row htmlRow
				for cell := c.FirstChild; cell != nil; cell = cell.NextSibling {
					if cell.Type != html.ElementNode || (cell.Data != "td" && cell.Data != "th") {
						continue
					}
					if cell.Data == "th" {
						row.header = true
					}
					var inner strings.Builder
					renderChildren(&inner, cell, listDepth)
					row.cells = append(row.cells, strings.TrimSpace(inner.String()))
				}
				if len(row.cells) > 0 {
					rows = append(rows, row)
				}
				continue
			}
			walk(c)
		}
	}
	walk(table)
	return rows
}

func renderChildren(b *strings.Builder, n *html.Node, listDepth int) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		render(b, c, listDepth)
	}
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/markup/ -v`
Expected: PASS. `TestRoundTripStability` is load-bearing — if a construct fails to round-trip, fix the converter rather than weakening the test, unless the construct is genuinely outside the documented subset. The header-row separator in `TestFromHTMLTable` is not emitted; add it only if a real Confluence page needs it.

- [x] **Step 5: Three-engine review gate**

Do not commit yet. Dispatch three independent reviews of *this task's diff only*, in parallel, one
per engine. Each gets the same diff and the task's own "Interfaces" block, and a different lens.

```
Agent(model="fable",  prompt=<completeness lens>)   # spec coverage, missing tests, error paths
Agent(model="haiku",  prompt=<consistency lens>)    # signatures, types, imports, helper clashes
Agent(subagent_type="claude", prompt=<relay>)       # wraps mcp__codex__codex: Go and API correctness
```

Give every reviewer this preamble:

> Review the diff below against `docs/plan/SPEC.md` and this task's Interfaces block. Report findings
> only; do not rewrite. For each: SEVERITY (blocking | important | minor), WHERE (file:line), WHAT
> (one or two sentences), FIX (the concrete change). No praise, no summary. If you find nothing real,
> say so rather than padding.

Then the lens, one per engine:

- **fable** — completeness: does this task implement every spec requirement it claims? What
  behaviour has no test? What happens on a 4xx, a 5xx, a cancelled context, an empty result?
- **haiku** — consistency: do the signatures, types, field names and imports match what earlier
  tasks defined and later tasks will call? Any helper defined twice in one package? Any import
  declared but unused, which fails to compile in Go?
- **codex** — correctness: will this compile? Is the SDK, goldmark, `x/net/html` and Atlassian REST
  usage right? Does each test actually test what it claims, or would it pass regardless of the
  implementation?

Then:

1. Triage every finding. Blocking and important ones get fixed before the commit. Minor ones are
   fixed or recorded with a reason.
2. **Verify any claim about a library's behaviour against that library's source** before acting on
   it. Review 01 found a blocking defect that only source-reading could confirm, and one false
   positive that only source-reading could refute. A reviewer's confident claim is a lead, not a
   fact.
3. Re-run `go test ./...` for the packages this task touches.
4. Append the findings and their dispositions to `docs/plan/REVIEW-LOG.md`, in the same table shape
   as `REVIEW-01-findings.md`, noting which engine found what and where they agreed. Agreement
   across engines is the strongest signal available.

The gate is the review, never a rubber stamp: "three reviews ran and found nothing blocking" is a
complete and valid outcome, recorded in one line.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/markup/from_wiki.go internal/markup/from_html.go internal/markup/from_wiki_test.go internal/markup/from_html_test.go
git commit -m "feat(markup): wiki and rendered-HTML to markdown for reads"
```

---

### Task 9: Jira module scaffold and read tools

**Files:**
- Create: `internal/jira/module.go`
- Create: `internal/jira/read.go`
- Test: `internal/jira/read_test.go`

**Interfaces:**
- Consumes: `core.Config`, `core.Client`, `core.ObjectSchema`, `core.ResolveFields`, `core.ToolDecl`, `core.Module`, `core.ActionRead` (Tasks 1–5); `markup.FromWiki` (Task 8).
- Produces: `jira.New() core.Module` (declaration-only, for domain discovery before a client exists), `jira.NewWith(cfg core.Config, c *core.Client) core.Module`, and the constant `jira.Domain = "jira"`.

Two constructors exist because `core.Load` needs the domain list before configuration is resolved, and handlers need a configured client. `New()` returns a module whose `Tools()` yields declarations with nil handlers — it is only ever asked for `Domain()`.

- [x] **Step 1: Write the failing test**

```go
package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
)

// newTestModule wires a module against a fake Atlassian.
func newTestModule(t *testing.T, h http.HandlerFunc) core.Module {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := core.Config{
		BaseURL:      srv.URL,
		Email:        "u@example.com",
		Token:        "test-token-value-1234",
		Domains:      map[string]core.Caps{Domain: {Read: true, Write: true, Destructive: true}},
		LimitDefault: 20,
		LimitMax:     50,
		EpicFieldID:  "customfield_10014",
	}
	var logs bytes.Buffer
	return NewWith(cfg, core.NewClient(cfg, core.NewLogger("debug", &logs)))
}

// call invokes a declared tool by name with the given arguments.
func call(t *testing.T, m core.Module, name string, args map[string]any) any {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	for _, d := range m.Tools() {
		if d.Name != name {
			continue
		}
		out, err := d.Handle(context.Background(), raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	t.Fatalf("tool %q not declared", name)
	return nil
}

func TestNewDeclaresDomainWithoutClient(t *testing.T) {
	if got := New().Domain(); got != "jira" {
		t.Errorf("Domain() = %q, want jira", got)
	}
}

func TestModuleDeclaresExpectedToolsAndActions(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {})
	want := map[string][]core.Action{
		"jira_search": {core.ActionRead},
		"jira_get":    {core.ActionRead},
		// jira_update spans both classes; see updateDecl.
		"jira_update":     {core.ActionWrite, core.ActionDestructive},
		"jira_transition": {core.ActionDestructive},
		"jira_comment":    {core.ActionWrite},
	}
	got := map[string][]core.Action{}
	for _, d := range m.Tools() {
		got[d.Name] = d.Actions
	}
	if len(got) != len(want) {
		t.Fatalf("declared %d tools, want %d: %v", len(got), len(want), got)
	}
	for name, actions := range want {
		if len(got[name]) != len(actions) {
			t.Errorf("%s actions = %v, want %v", name, got[name], actions)
			continue
		}
		for i := range actions {
			if got[name][i] != actions[i] {
				t.Errorf("%s actions = %v, want %v", name, got[name], actions)
				break
			}
		}
	}
}

func TestSearchSendsJQLAndDefaultFields(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/3/search/jql" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"issues":[{"key":"PROJ-1","fields":{"summary":"S","status":{"name":"Open"}}}]}`)
	})

	call(t, m, "jira_search", map[string]any{"jql": "project = PROJ"})

	if body["jql"] != "project = PROJ" {
		t.Errorf("jql = %v", body["jql"])
	}
	if body["maxResults"] != float64(20) {
		t.Errorf("maxResults = %v, want the configured default 20", body["maxResults"])
	}
	fields, _ := body["fields"].([]any)
	joined := make([]string, 0, len(fields))
	for _, f := range fields {
		joined = append(joined, f.(string))
	}
	for _, want := range []string{"summary", "status", "assignee", "fixVersions", "parent", "updated"} {
		if !containsField(joined, want) {
			t.Errorf("default fields missing %q: %v", want, joined)
		}
	}
	if containsField(joined, "description") {
		t.Error("description must NOT be in jira_search defaults")
	}
}

func TestSearchLimitCappedAtMax(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"issues":[]}`)
	})
	call(t, m, "jira_search", map[string]any{"jql": "x", "limit": 5000})
	if body["maxResults"] != float64(50) {
		t.Errorf("maxResults = %v, want capped at 50", body["maxResults"])
	}
}

func TestSearchTruncationFallsBackToCountWhenNoSignal(t *testing.T) {
	// Neither isLast nor nextPageToken present: a full page is reported as
	// possibly truncated rather than silently treated as complete.
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"issues":[{"key":"A","fields":{}}]}`)
	})
	out := call(t, m, "jira_search", map[string]any{"jql": "x", "limit": 1})
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "truncated") {
		t.Errorf("result must state possible truncation: %s", raw)
	}
}

func TestGetUsesV2AndConvertsDescriptionToMarkdown(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/api/2/issue/") {
			t.Errorf("jira_get must use v2 so description is wiki markup, got %s", r.URL.Path)
		}
		io.WriteString(w, `{"key":"PROJ-1","fields":{"summary":"S","description":"h3. Objective\n\nDo *the* thing"}}`)
	})
	out := call(t, m, "jira_get", map[string]any{"key": "PROJ-1"})
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "### Objective") {
		t.Errorf("description not converted to markdown: %s", raw)
	}
	if !strings.Contains(string(raw), "**the**") {
		t.Errorf("inline emphasis not converted: %s", raw)
	}
}

// "epic" is a logical name. It must be translated to the site-specific custom
// field on the way out and back again on the way in, or the caller sees a field
// Jira does not have.
func TestSearchTranslatesLogicalEpicField(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"issues":[],"isLast":true}`)
	})
	call(t, m, "jira_search", map[string]any{"jql": "x"})

	fields, _ := body["fields"].([]any)
	var names []string
	for _, f := range fields {
		names = append(names, f.(string))
	}
	if containsField(names, "epic") {
		t.Error("the literal string \"epic\" must not be sent to Jira")
	}
	if !containsField(names, "customfield_10014") {
		t.Errorf("epic must be translated to the configured custom field: %v", names)
	}
}

func TestGetRenamesEpicCustomFieldBackToLogicalName(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"key":"PROJ-1","fields":{"customfield_10014":"PROJ-9"}}`)
	})
	out := call(t, m, "jira_get", map[string]any{"key": "PROJ-1"})
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), `"epic":"PROJ-9"`) {
		t.Errorf("custom field not renamed to epic: %s", raw)
	}
	if strings.Contains(string(raw), "customfield_10014") {
		t.Errorf("raw custom field id leaked to the caller: %s", raw)
	}
}

// A complete result set whose size happens to equal the limit is not truncated.
func TestSearchExactlyAtLimitButCompleteIsNotTruncated(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"issues":[{"key":"A","fields":{}}],"isLast":true}`)
	})
	out := call(t, m, "jira_search", map[string]any{"jql": "x", "limit": 1})
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "truncated") {
		t.Errorf("isLast=true must not be reported as truncated: %s", raw)
	}
}

func TestSearchNextPageTokenMeansTruncated(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"issues":[{"key":"A","fields":{}}],"isLast":false,"nextPageToken":"tok"}`)
	})
	out := call(t, m, "jira_search", map[string]any{"jql": "x", "limit": 10})
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "truncated") {
		t.Errorf("a next page token must be reported: %s", raw)
	}
}

func TestGetFieldsPlusPrefixExtendsDefaults(t *testing.T) {
	var gotFields string
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		gotFields = r.URL.Query().Get("fields")
		io.WriteString(w, `{"key":"PROJ-1","fields":{}}`)
	})
	call(t, m, "jira_get", map[string]any{"key": "PROJ-1", "fields": []string{"+labels"}})
	if !strings.Contains(gotFields, "labels") || !strings.Contains(gotFields, "summary") {
		t.Errorf("fields = %q, want defaults plus labels", gotFields)
	}
}

func TestGetRejectsMixedFieldForms(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made when field selection is invalid")
	})
	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1", "fields": []string{"summary", "+labels"}})
	for _, d := range m.Tools() {
		if d.Name != "jira_get" {
			continue
		}
		if _, err := d.Handle(context.Background(), raw); err == nil {
			t.Fatal("mixing bare and prefixed field names must error")
		}
	}
}

func TestGetRejectsMalformedIssueKey(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made for a malformed key")
	})
	raw, _ := json.Marshal(map[string]any{"key": "../../../etc/passwd"})
	for _, d := range m.Tools() {
		if d.Name != "jira_get" {
			continue
		}
		if _, err := d.Handle(context.Background(), raw); err == nil {
			t.Fatal("malformed issue key must be rejected before any request")
		}
	}
}

func containsField(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/jira/ -v`
Expected: FAIL — package does not exist, `undefined: New`, `undefined: NewWith`, `undefined: Domain`.

- [x] **Step 3: Write minimal implementation**

`internal/jira/module.go`:

```go
// Package jira declares the Jira tools. It must not import the MCP SDK, read
// the environment, build an HTTP client, or log: those belong to core, so that
// masking, gating and allowlisting cannot be forgotten here.
package jira

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
)

// Domain is the config and gating key for this module.
const Domain = "jira"

// reIssueKey is the full set of characters Jira permits in an issue key. It is
// enforced before any key reaches a URL path.
var reIssueKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[0-9]+$`)

type module struct {
	cfg    core.Config
	client *core.Client
}

// New returns a declaration-only module, used to discover the domain name
// before configuration is loaded. Its handlers are not wired.
func New() core.Module { return module{} }

// NewWith returns a functional module.
func NewWith(cfg core.Config, c *core.Client) core.Module {
	return module{cfg: cfg, client: c}
}

func (m module) Domain() string { return Domain }

// Tools declares every tool this module offers. core decides which are
// registered, based on the domain's enabled action classes.
func (m module) Tools() []core.ToolDecl {
	return []core.ToolDecl{
		m.searchDecl(),
		m.getDecl(),
		m.updateDecl(),
		m.transitionDecl(),
		m.commentDecl(),
	}
}

// validKey guards every path interpolation. A key is never trusted from input.
func validKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if !reIssueKey.MatchString(key) {
		return "", fmt.Errorf("invalid issue key %q: expected the form PROJ-123", key)
	}
	return strings.ToUpper(key), nil
}

// projectOf returns the project key portion of an issue key.
//
// NOTE (added after the Task 9 review gate): this function was deleted from the
// landed Task 9 code, because golangci-lint's `unused` fails the build on a
// function with no caller and CI is a strict security -> lint -> test chain.
// Whichever task first needs it must add it back together with its caller —
// Task 10's write-allowlist check is the first.
func projectOf(issueKey string) string {
	if i := strings.IndexByte(issueKey, '-'); i > 0 {
		return issueKey[:i]
	}
	return issueKey
}

// clampLimit applies the configured default and hard cap.
func (m module) clampLimit(requested int) int {
	if requested <= 0 {
		return m.cfg.LimitDefault
	}
	if requested > m.cfg.LimitMax {
		return m.cfg.LimitMax
	}
	return requested
}
```

`internal/jira/read.go`:

```go
package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
	"github.com/OxCom/atlassian-mcp-lite/internal/markup"
	"github.com/google/jsonschema-go/jsonschema"
)

// searchDefaults is the triage set: enough to decide what an issue is and who
// owns it, without the description, which dominates the payload.
//
// "epic" here is a LOGICAL name. Jira has no field called epic — classic
// projects store the link in a site-specific custom field — so every field list
// is translated before the request and translated back in the response.
var searchDefaults = []string{
	"summary", "status", "issuetype", "assignee", "fixVersions", "parent", "epic", "updated",
}

// logicalEpic is the name callers use for the Epic Link field.
const logicalEpic = "epic"

// toUpstreamFields translates logical field names to what Jira expects.
func (m module) toUpstreamFields(fields []string) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if strings.EqualFold(f, logicalEpic) {
			out = append(out, m.cfg.EpicFieldID)
			continue
		}
		out = append(out, f)
	}
	return out
}

// getDefaults adds the description, which is normally wanted for a single issue.
var getDefaults = append(append([]string{}, searchDefaults...), "description")

func fieldsProperty() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "array",
		Items:       &jsonschema.Schema{Type: "string"},
		Description: `Fields to return. Omit for the default set. Bare names replace the default set entirely; "+name" adds to it; "-name" removes from it. Do not mix bare and prefixed names.`,
	}
}

func (m module) searchDecl() core.ToolDecl {
	return core.ToolDecl{
		Name:        "jira_search",
		Actions:     []core.Action{core.ActionRead},
		Description: "Search Jira issues with JQL. Returns a compact field set by default.",
		Schema: func(core.Caps) *jsonschema.Schema {
			return core.ObjectSchema(map[string]*jsonschema.Schema{
				"jql":    {Type: "string", Description: "A JQL query."},
				"fields": fieldsProperty(),
				"limit":  {Type: "integer", Description: "Maximum issues to return."},
			}, []string{"jql"})
		},
		Handle: m.handleSearch,
	}
}

type searchArgs struct {
	JQL    string   `json:"jql"`
	Fields []string `json:"fields"`
	Limit  int      `json:"limit"`
}

func (m module) handleSearch(ctx context.Context, raw json.RawMessage) (any, error) {
	var in searchArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("jira_search: %w", err)
	}
	if strings.TrimSpace(in.JQL) == "" {
		return nil, fmt.Errorf("jira_search: jql is required")
	}
	fields, err := core.ResolveFields(searchDefaults, in.Fields)
	if err != nil {
		return nil, fmt.Errorf("jira_search: %w", err)
	}
	limit := m.clampLimit(in.Limit)

	// JQL travels as a JSON body value, never concatenated into a URL.
	body := map[string]any{
		"jql":        in.JQL,
		"maxResults": limit,
		"fields":     m.toUpstreamFields(fields),
	}

	var res struct {
		Issues []struct {
			Key    string                     `json:"key"`
			Fields map[string]json.RawMessage `json:"fields"`
		} `json:"issues"`
		// isLast and nextPageToken are the API's own completion signals.
		// Counting returned issues cannot distinguish a complete result set of
		// exactly maxResults from a truncated one.
		IsLast        *bool  `json:"isLast"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := m.client.Do(ctx, http.MethodPost, "/rest/api/3/search/jql", nil, body, &res); err != nil {
		return nil, err
	}

	issues := make([]map[string]any, 0, len(res.Issues))
	for _, i := range res.Issues {
		issues = append(issues, m.flatten(i.Key, i.Fields))
	}

	out := map[string]any{"issues": issues, "returned": len(issues)}
	if m.moreAvailable(res.IsLast, res.NextPageToken, len(issues), limit) {
		out["truncated"] = fmt.Sprintf(
			"more results exist beyond limit %d; narrow the JQL or raise limit (max %d). paging is not supported",
			limit, m.cfg.LimitMax)
	}
	return out, nil
}

// moreAvailable decides whether results were cut short. The API's explicit
// signals win; the count heuristic is only a fallback for responses that carry
// neither, and a full page with no other signal is reported as possibly
// truncated rather than silently complete.
func (m module) moreAvailable(isLast *bool, nextPageToken string, returned, limit int) bool {
	if nextPageToken != "" {
		return true
	}
	if isLast != nil {
		return !*isLast
	}
	return returned >= limit
}

func (m module) getDecl() core.ToolDecl {
	return core.ToolDecl{
		Name:        "jira_get",
		Actions:     []core.Action{core.ActionRead},
		Description: "Get one Jira issue. The description is returned as markdown.",
		Schema: func(core.Caps) *jsonschema.Schema {
			return core.ObjectSchema(map[string]*jsonschema.Schema{
				"key":    {Type: "string", Description: "Issue key, e.g. PROJ-123."},
				"fields": fieldsProperty(),
			}, []string{"key"})
		},
		Handle: m.handleGet,
	}
}

type getArgs struct {
	Key    string   `json:"key"`
	Fields []string `json:"fields"`
}

func (m module) handleGet(ctx context.Context, raw json.RawMessage) (any, error) {
	var in getArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("jira_get: %w", err)
	}
	key, err := validKey(in.Key)
	if err != nil {
		return nil, fmt.Errorf("jira_get: %w", err)
	}
	fields, err := core.ResolveFields(getDefaults, in.Fields)
	if err != nil {
		return nil, fmt.Errorf("jira_get: %w", err)
	}

	// v2 returns rich-text fields as wiki markup, which markup.FromWiki turns
	// into markdown. v3 would return ADF, which we deliberately do not parse.
	q := url.Values{"fields": {strings.Join(m.toUpstreamFields(fields), ",")}}
	var res struct {
		Key    string                     `json:"key"`
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := m.client.Do(ctx, http.MethodGet, "/rest/api/2/issue/"+url.PathEscape(key), q, nil, &res); err != nil {
		return nil, err
	}
	return m.flatten(res.Key, res.Fields), nil
}

// flatten reduces Jira's nested field objects to scalars a model can read,
// converts wiki-markup bodies to markdown, and renames the site-specific Epic
// Link custom field back to the logical name callers used.
func (m module) flatten(key string, fields map[string]json.RawMessage) map[string]any {
	out := map[string]any{"key": key}
	for name, raw := range fields {
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		if name == m.cfg.EpicFieldID {
			name = logicalEpic
		}
		switch name {
		case "description", "environment":
			var s string
			if json.Unmarshal(raw, &s) == nil {
				out[name] = markup.FromWiki(s)
				continue
			}
			out[name] = json.RawMessage(raw)
		case "status", "issuetype", "priority", "resolution", "parent":
			out[name] = namedValue(raw)
		case "assignee", "reporter", "creator":
			out[name] = personValue(raw)
		case "fixVersions", "components", "versions":
			out[name] = namedList(raw)
		default:
			var v any
			if json.Unmarshal(raw, &v) == nil {
				out[name] = v
			}
		}
	}
	return out
}

func namedValue(raw json.RawMessage) any {
	var v struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	}
	if json.Unmarshal(raw, &v) == nil {
		if v.Key != "" && v.Name != "" {
			return v.Key + " (" + v.Name + ")"
		}
		if v.Name != "" {
			return v.Name
		}
		if v.Key != "" {
			return v.Key
		}
	}
	return nil
}

func personValue(raw json.RawMessage) any {
	var v struct {
		DisplayName string `json:"displayName"`
		AccountID   string `json:"accountId"`
	}
	if json.Unmarshal(raw, &v) == nil && v.DisplayName != "" {
		return v.DisplayName
	}
	return nil
}

func namedList(raw json.RawMessage) any {
	var vs []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &vs) != nil {
		return nil
	}
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		if v.Name != "" {
			out = append(out, v.Name)
		}
	}
	return out
}
```

Tasks 10 and 11 add `updateDecl`, `transitionDecl` and `commentDecl`. To keep this task compiling on its own, add temporary stubs at the bottom of `module.go` and delete them in Task 10:

```go
// Temporary stubs so the package compiles before Tasks 10 and 11. Delete each
// as its real implementation lands.
func (m module) updateDecl() core.ToolDecl {
	// Write AND Destructive: this tool spans both classes, and a tool that
	// lists only one vanishes when only the other is enabled. Corrected after
	// the Task 9 review gate — the stub block originally disagreed with this
	// file's own TestModuleDeclaresExpectedToolsAndActions.
	return core.ToolDecl{Name: "jira_update", Actions: []core.Action{core.ActionWrite, core.ActionDestructive}, Description: "stub",
		Schema: func(core.Caps) *jsonschema.Schema { return core.ObjectSchema(nil, nil) },
		Handle: func(context.Context, json.RawMessage) (any, error) { return nil, fmt.Errorf("not implemented") }}
}
func (m module) transitionDecl() core.ToolDecl {
	return core.ToolDecl{Name: "jira_transition", Actions: []core.Action{core.ActionDestructive}, Description: "stub",
		Schema: func(core.Caps) *jsonschema.Schema { return core.ObjectSchema(nil, nil) },
		Handle: func(context.Context, json.RawMessage) (any, error) { return nil, fmt.Errorf("not implemented") }}
}
func (m module) commentDecl() core.ToolDecl {
	return core.ToolDecl{Name: "jira_comment", Actions: []core.Action{core.ActionWrite}, Description: "stub",
		Schema: func(core.Caps) *jsonschema.Schema { return core.ObjectSchema(nil, nil) },
		Handle: func(context.Context, json.RawMessage) (any, error) { return nil, fmt.Errorf("not implemented") }}
}
```

The stub file needs `"context"`, `"encoding/json"` and the jsonschema import added to `module.go`.

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/jira/ -v`
Expected: PASS — all ten tests.

- [x] **Step 5: Three-engine review gate**

Do not commit yet. Dispatch three independent reviews of *this task's diff only*, in parallel, one
per engine. Each gets the same diff and the task's own "Interfaces" block, and a different lens.

```
Agent(model="fable",  prompt=<completeness lens>)   # spec coverage, missing tests, error paths
Agent(model="haiku",  prompt=<consistency lens>)    # signatures, types, imports, helper clashes
Agent(subagent_type="claude", prompt=<relay>)       # wraps mcp__codex__codex: Go and API correctness
```

Give every reviewer this preamble:

> Review the diff below against `docs/plan/SPEC.md` and this task's Interfaces block. Report findings
> only; do not rewrite. For each: SEVERITY (blocking | important | minor), WHERE (file:line), WHAT
> (one or two sentences), FIX (the concrete change). No praise, no summary. If you find nothing real,
> say so rather than padding.

Then the lens, one per engine:

- **fable** — completeness: does this task implement every spec requirement it claims? What
  behaviour has no test? What happens on a 4xx, a 5xx, a cancelled context, an empty result?
- **haiku** — consistency: do the signatures, types, field names and imports match what earlier
  tasks defined and later tasks will call? Any helper defined twice in one package? Any import
  declared but unused, which fails to compile in Go?
- **codex** — correctness: will this compile? Is the SDK, goldmark, `x/net/html` and Atlassian REST
  usage right? Does each test actually test what it claims, or would it pass regardless of the
  implementation?

Then:

1. Triage every finding. Blocking and important ones get fixed before the commit. Minor ones are
   fixed or recorded with a reason.
2. **Verify any claim about a library's behaviour against that library's source** before acting on
   it. Review 01 found a blocking defect that only source-reading could confirm, and one false
   positive that only source-reading could refute. A reviewer's confident claim is a lead, not a
   fact.
3. Re-run `go test ./...` for the packages this task touches.
4. Append the findings and their dispositions to `docs/plan/REVIEW-LOG.md`, in the same table shape
   as `REVIEW-01-findings.md`, noting which engine found what and where they agreed. Agreement
   across engines is the strongest signal available.

The gate is the review, never a rubber stamp: "three reviews ran and found nothing blocking" is a
complete and valid outcome, recorded in one line.

- [ ] **Step 6: Commit**

```bash
git add internal/jira/
git commit -m "feat(jira): module scaffold with jira_search and jira_get"
```

---

### Task 10: Jira lookups and jira_update

**Files:**
- Create: `internal/jira/lookup.go`
- Create: `internal/jira/update.go`
- Modify: `internal/jira/module.go` — delete the `updateDecl` stub
- Test: `internal/jira/lookup_test.go`
- Test: `internal/jira/update_test.go`

**Interfaces:**
- Consumes: everything from Task 9; `markup.ToWiki` (Task 7); `core.Config.AllowProject`, `core.Config.EpicFieldID` (Task 1).
- Produces: `(module).accountIDFor(ctx, query string) (string, error)`, `(module).versionIDFor(ctx, projectKey, name string) (string, error)`, and the real `updateDecl`.

**Scope limit, deliberate.** `epic` always writes the field named by
`ATLAS_EPIC_FIELD_ID`, which is correct for classic (company-managed) projects.
Team-managed projects have no Epic Link field and use `parent` instead — callers
there pass `parent`, not `epic`. Detecting the project style per call would mean
an `editmeta` round trip on every update, which was judged not worth it. A
team-managed project receiving `epic` gets a clear 400 from Jira carrying the
field name, which the error path surfaces verbatim.

`jira_update` is the tool that spans two action classes. Its schema is built from `core.Caps`: `summary` and `description` appear only when `Destructive` is true.

- [x] **Step 1: Write the failing test**

`lookup_test.go`:

```go
package jira

import (
	"context"
	"io"
	"net/http"
	"testing"
)

func TestAccountIDForResolvesEmail(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/user/search" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if q := r.URL.Query().Get("query"); q != "u@example.com" {
			t.Errorf("query = %q", q)
		}
		io.WriteString(w, `[{"accountId":"aid-1","displayName":"A User"}]`)
	}).(module)

	got, err := m.accountIDFor(context.Background(), "u@example.com")
	if err != nil {
		t.Fatalf("accountIDFor: %v", err)
	}
	if got != "aid-1" {
		t.Errorf("accountId = %q", got)
	}
}

func TestAccountIDForNoMatchIsAnError(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[]`)
	}).(module)
	if _, err := m.accountIDFor(context.Background(), "nobody@example.com"); err == nil {
		t.Fatal("an unresolvable assignee must be an error, not a silent no-op")
	}
}

func TestAccountIDForAmbiguousIsAnError(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"accountId":"a","displayName":"A"},{"accountId":"b","displayName":"B"}]`)
	}).(module)
	if _, err := m.accountIDFor(context.Background(), "A"); err == nil {
		t.Fatal("an ambiguous assignee must be an error, not a guess")
	}
}

func TestVersionIDForMatchesByNameCaseInsensitively(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/project/PROJ/versions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		io.WriteString(w, `[{"id":"111","name":"1.2.x"},{"id":"222","name":"Backlog"}]`)
	}).(module)

	got, err := m.versionIDFor(context.Background(), "PROJ", "backlog")
	if err != nil {
		t.Fatalf("versionIDFor: %v", err)
	}
	if got != "222" {
		t.Errorf("id = %q, want 222", got)
	}
}

func TestVersionIDForUnknownNameListsAvailable(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"id":"111","name":"1.2.x"}]`)
	}).(module)
	_, err := m.versionIDFor(context.Background(), "PROJ", "nope")
	if err == nil {
		t.Fatal("unknown version must error")
	}
	if !contains(err.Error(), "1.2.x") {
		t.Errorf("error should list available versions, got %q", err)
	}
}

func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
```

`update_test.go`:

```go
package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
)

func declFor(t *testing.T, m core.Module, name string) core.ToolDecl {
	t.Helper()
	for _, d := range m.Tools() {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("tool %q not declared", name)
	return core.ToolDecl{}
}

func TestUpdateSchemaOmitsDestructivePropsWhenDisabled(t *testing.T) {
	d := declFor(t, newTestModule(t, func(http.ResponseWriter, *http.Request) {}), "jira_update")

	off := d.Schema(core.Caps{Write: true})
	for _, prop := range []string{"summary", "description"} {
		if _, ok := off.Properties[prop]; ok {
			t.Errorf("%q must be absent when destructive=false", prop)
		}
	}
	for _, prop := range []string{"assignee", "fixVersion", "epic", "parent"} {
		if _, ok := off.Properties[prop]; !ok {
			t.Errorf("%q must be present when write=true", prop)
		}
	}

	on := d.Schema(core.Caps{Write: true, Destructive: true})
	for _, prop := range []string{"summary", "description"} {
		if _, ok := on.Properties[prop]; !ok {
			t.Errorf("%q must be present when destructive=true", prop)
		}
	}
}

func TestUpdateSchemaWriteOnlyPropsAbsentWhenWriteDisabled(t *testing.T) {
	d := declFor(t, newTestModule(t, func(http.ResponseWriter, *http.Request) {}), "jira_update")
	s := d.Schema(core.Caps{Destructive: true})
	if _, ok := s.Properties["assignee"]; ok {
		t.Error("assignee must be absent when write=false")
	}
	if _, ok := s.Properties["description"]; !ok {
		t.Error("description must be present when destructive=true")
	}
}

func TestUpdateSchemaForbidsAdditionalProperties(t *testing.T) {
	d := declFor(t, newTestModule(t, func(http.ResponseWriter, *http.Request) {}), "jira_update")
	if d.Schema(core.Caps{Write: true}).AdditionalProperties == nil {
		t.Fatal("AdditionalProperties must be set, or an omitted property is still accepted")
	}
}

func TestUpdateConvertsDescriptionToWikiAndUsesV2(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || !strings.HasPrefix(r.URL.Path, "/rest/api/2/issue/") {
			t.Errorf("request = %s %s, want PUT on v2", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusNoContent)
	})

	call(t, m, "jira_update", map[string]any{
		"key":         "PROJ-1",
		"description": "### Objective\n\nDo **the** thing",
	})

	fields, _ := body["fields"].(map[string]any)
	desc, _ := fields["description"].(string)
	if !strings.Contains(desc, "h3. Objective") {
		t.Errorf("description not converted to wiki markup: %q", desc)
	}
	if !strings.Contains(desc, "*the*") {
		t.Errorf("bold not converted: %q", desc)
	}
}

func TestUpdateResolvesAssigneeAndFixVersion(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/user/search":
			io.WriteString(w, `[{"accountId":"aid-9","displayName":"A User"}]`)
		case r.URL.Path == "/rest/api/3/project/PROJ/versions":
			io.WriteString(w, `[{"id":"777","name":"1.2.x"}]`)
		default:
			json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusNoContent)
		}
	})

	call(t, m, "jira_update", map[string]any{
		"key":        "PROJ-1",
		"assignee":   "u@example.com",
		"fixVersion": "1.2.x",
	})

	fields, _ := body["fields"].(map[string]any)
	assignee, _ := fields["assignee"].(map[string]any)
	if assignee["accountId"] != "aid-9" {
		t.Errorf("assignee = %v, want resolved accountId", fields["assignee"])
	}
	versions, _ := fields["fixVersions"].([]any)
	if len(versions) != 1 {
		t.Fatalf("fixVersions = %v", fields["fixVersions"])
	}
	if v, _ := versions[0].(map[string]any); v["id"] != "777" {
		t.Errorf("fixVersions[0] = %v, want resolved id", versions[0])
	}
}

func TestUpdateEpicUsesConfiguredCustomField(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusNoContent)
	})
	call(t, m, "jira_update", map[string]any{"key": "PROJ-1", "epic": "PROJ-9"})

	fields, _ := body["fields"].(map[string]any)
	if fields["customfield_10014"] != "PROJ-9" {
		t.Errorf("epic must map to the configured custom field, got %v", fields)
	}
}

func TestUpdateParentUsesParentField(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusNoContent)
	})
	call(t, m, "jira_update", map[string]any{"key": "PROJ-1", "parent": "PROJ-2"})

	fields, _ := body["fields"].(map[string]any)
	parent, _ := fields["parent"].(map[string]any)
	if parent["key"] != "PROJ-2" {
		t.Errorf("parent = %v", fields["parent"])
	}
}

func TestUpdateRefusedByAllowlist(t *testing.T) {
	srvCalled := false
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		srvCalled = true
		w.WriteHeader(http.StatusNoContent)
	}).(module)

	base.cfg.WriteProjects = []string{"OTHER"}
	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1", "parent": "PROJ-2"})
	if _, err := declFor(t, base, "jira_update").Handle(context.Background(), raw); err == nil {
		t.Fatal("write outside the allowlist must be refused")
	}
	if srvCalled {
		t.Error("refusal must happen before any request is made")
	}
}

func TestUpdateWithNoFieldsIsAnError(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made when there is nothing to set")
	})
	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1"})
	if _, err := declFor(t, m, "jira_update").Handle(context.Background(), raw); err == nil {
		t.Fatal("an update with no fields must error")
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/jira/ -run 'Update|AccountID|Version' -v`
Expected: FAIL — `undefined: accountIDFor`, and the stub `jira_update` returns "not implemented".

- [x] **Step 3: Write minimal implementation**

Delete the three stub functions added in Task 9 from `module.go`, then re-add stubs for `transitionDecl` and `commentDecl` only (Task 11 removes those).

`internal/jira/lookup.go`:

```go
package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// accountIDFor resolves an email or display name to an accountId. An
// unresolvable or ambiguous query is an error: silently assigning the wrong
// person is worse than failing.
func (m module) accountIDFor(ctx context.Context, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("assignee is empty")
	}
	var users []struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
		Email       string `json:"emailAddress"`
	}
	q := url.Values{"query": {query}, "maxResults": {"5"}}
	if err := m.client.Do(ctx, http.MethodGet, "/rest/api/3/user/search", q, nil, &users); err != nil {
		return "", err
	}
	switch len(users) {
	case 0:
		return "", fmt.Errorf("no Atlassian user matches %q", query)
	case 1:
		return users[0].AccountID, nil
	default:
		names := make([]string, 0, len(users))
		for _, u := range users {
			names = append(names, u.DisplayName)
		}
		return "", fmt.Errorf("%q matches %d users (%s); use an exact email address",
			query, len(users), strings.Join(names, ", "))
	}
}

// versionIDFor resolves a version name to its id within a project.
func (m module) versionIDFor(ctx context.Context, projectKey, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("fixVersion is empty")
	}
	var versions []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	path := "/rest/api/3/project/" + url.PathEscape(projectKey) + "/versions"
	if err := m.client.Do(ctx, http.MethodGet, path, nil, nil, &versions); err != nil {
		return "", err
	}
	available := make([]string, 0, len(versions))
	for _, v := range versions {
		if strings.EqualFold(v.Name, name) {
			return v.ID, nil
		}
		available = append(available, v.Name)
	}
	return "", fmt.Errorf("no version named %q in %s; available: %s",
		name, projectKey, strings.Join(available, ", "))
}
```

`internal/jira/update.go`:

```go
package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
	"github.com/OxCom/atlassian-mcp-lite/internal/markup"
	"github.com/google/jsonschema-go/jsonschema"
)

func (m module) updateDecl() core.ToolDecl {
	return core.ToolDecl{
		Name: "jira_update",
		// Spans two classes: the additive fields are write; summary and
		// description are destructive. Declaring both means the tool still
		// registers when only one class is enabled, carrying just that
		// class's properties.
		Actions: []core.Action{core.ActionWrite, core.ActionDestructive},
		Description: "Update fields on a Jira issue. Bodies are written in markdown. " +
			"Which fields are accepted depends on the server's enabled capabilities.",
		Schema: func(c core.Caps) *jsonschema.Schema {
			props := map[string]*jsonschema.Schema{
				"key": {Type: "string", Description: "Issue key, e.g. PROJ-123."},
			}
			// Additive, reversible fields.
			if c.Write {
				props["assignee"] = &jsonschema.Schema{Type: "string",
					Description: "Assignee email address, or an exact display name."}
				props["fixVersion"] = &jsonschema.Schema{Type: "string",
					Description: "Fix version name, resolved to its id."}
				props["epic"] = &jsonschema.Schema{Type: "string",
					Description: "Epic issue key to link this issue to."}
				props["parent"] = &jsonschema.Schema{Type: "string",
					Description: "Parent issue key."}
			}
			// Overwrites that are hard to recover.
			if c.Destructive {
				props["summary"] = &jsonschema.Schema{Type: "string",
					Description: "Replaces the summary."}
				props["description"] = &jsonschema.Schema{Type: "string",
					Description: "Replaces the description. Markdown."}
			}
			return core.ObjectSchema(props, []string{"key"})
		},
		Handle: m.handleUpdate,
	}
}

type updateArgs struct {
	Key         string `json:"key"`
	Assignee    string `json:"assignee"`
	FixVersion  string `json:"fixVersion"`
	Epic        string `json:"epic"`
	Parent      string `json:"parent"`
	Summary     string `json:"summary"`
	Description string `json:"description"`
}

func (m module) handleUpdate(ctx context.Context, raw json.RawMessage) (any, error) {
	var in updateArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("jira_update: %w", err)
	}
	key, err := validKey(in.Key)
	if err != nil {
		return nil, fmt.Errorf("jira_update: %w", err)
	}
	project := projectOf(key)
	if !m.cfg.AllowProject(project) {
		return nil, fmt.Errorf("jira_update: writes to project %s are not permitted by ATLAS_WRITE_PROJECTS", project)
	}

	fields := map[string]any{}
	applied := make([]string, 0, 6)

	if in.Assignee != "" {
		id, err := m.accountIDFor(ctx, in.Assignee)
		if err != nil {
			return nil, fmt.Errorf("jira_update: assignee: %w", err)
		}
		fields["assignee"] = map[string]any{"accountId": id}
		applied = append(applied, "assignee")
	}
	if in.FixVersion != "" {
		id, err := m.versionIDFor(ctx, project, in.FixVersion)
		if err != nil {
			return nil, fmt.Errorf("jira_update: fixVersion: %w", err)
		}
		fields["fixVersions"] = []any{map[string]any{"id": id}}
		applied = append(applied, "fixVersion")
	}
	if in.Epic != "" {
		epic, err := validKey(in.Epic)
		if err != nil {
			return nil, fmt.Errorf("jira_update: epic: %w", err)
		}
		// Classic projects use an Epic Link custom field whose id is
		// site-specific; team-managed projects use parent. The id comes from
		// configuration so it is never a shared hardcoded constant.
		fields[m.cfg.EpicFieldID] = epic
		applied = append(applied, "epic")
	}
	if in.Parent != "" {
		parent, err := validKey(in.Parent)
		if err != nil {
			return nil, fmt.Errorf("jira_update: parent: %w", err)
		}
		fields["parent"] = map[string]any{"key": parent}
		applied = append(applied, "parent")
	}
	if in.Summary != "" {
		fields["summary"] = in.Summary
		applied = append(applied, "summary")
	}
	if in.Description != "" {
		// v2 accepts wiki markup as a plain string; v3 would demand ADF.
		fields["description"] = markup.ToWiki(in.Description)
		applied = append(applied, "description")
	}

	if len(fields) == 0 {
		return nil, fmt.Errorf("jira_update: nothing to set; supply at least one field")
	}

	body := map[string]any{"fields": fields}
	path := "/rest/api/2/issue/" + url.PathEscape(key)
	if err := m.client.Do(ctx, http.MethodPut, path, nil, body, nil); err != nil {
		return nil, err
	}
	return map[string]any{"key": key, "updated": strings.Join(applied, ", ")}, nil
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/jira/ -v`
Expected: PASS — Task 9 and Task 10 tests.

- [x] **Step 5: Three-engine review gate**

Do not commit yet. Dispatch three independent reviews of *this task's diff only*, in parallel, one
per engine. Each gets the same diff and the task's own "Interfaces" block, and a different lens.

```
Agent(model="fable",  prompt=<completeness lens>)   # spec coverage, missing tests, error paths
Agent(model="haiku",  prompt=<consistency lens>)    # signatures, types, imports, helper clashes
Agent(subagent_type="claude", prompt=<relay>)       # wraps mcp__codex__codex: Go and API correctness
```

Give every reviewer this preamble:

> Review the diff below against `docs/plan/SPEC.md` and this task's Interfaces block. Report findings
> only; do not rewrite. For each: SEVERITY (blocking | important | minor), WHERE (file:line), WHAT
> (one or two sentences), FIX (the concrete change). No praise, no summary. If you find nothing real,
> say so rather than padding.

Then the lens, one per engine:

- **fable** — completeness: does this task implement every spec requirement it claims? What
  behaviour has no test? What happens on a 4xx, a 5xx, a cancelled context, an empty result?
- **haiku** — consistency: do the signatures, types, field names and imports match what earlier
  tasks defined and later tasks will call? Any helper defined twice in one package? Any import
  declared but unused, which fails to compile in Go?
- **codex** — correctness: will this compile? Is the SDK, goldmark, `x/net/html` and Atlassian REST
  usage right? Does each test actually test what it claims, or would it pass regardless of the
  implementation?

Then:

1. Triage every finding. Blocking and important ones get fixed before the commit. Minor ones are
   fixed or recorded with a reason.
2. **Verify any claim about a library's behaviour against that library's source** before acting on
   it. Review 01 found a blocking defect that only source-reading could confirm, and one false
   positive that only source-reading could refute. A reviewer's confident claim is a lead, not a
   fact.
3. Re-run `go test ./...` for the packages this task touches.
4. Append the findings and their dispositions to `docs/plan/REVIEW-LOG.md`, in the same table shape
   as `REVIEW-01-findings.md`, noting which engine found what and where they agreed. Agreement
   across engines is the strongest signal available.

The gate is the review, never a rubber stamp: "three reviews ran and found nothing blocking" is a
complete and valid outcome, recorded in one line.

- [ ] **Step 6: Commit**

```bash
git add internal/jira/
git commit -m "feat(jira): jira_update with capability-built schema and name-to-id lookups"
```

---

### Task 11: jira_comment and jira_transition

**Files:**
- Create: `internal/jira/write.go`
- Modify: `internal/jira/module.go` — delete the remaining `transitionDecl` and `commentDecl` stubs
- Test: `internal/jira/write_test.go`

**Interfaces:**
- Consumes: everything from Tasks 9–10.
- Produces: the real `commentDecl` and `transitionDecl`.

`jira_transition` is `ActionDestructive` because a workflow move is not trivially reversible: transitions can be one-way and can fire side effects such as notifications and automation rules.

- [ ] **Step 1: Write the failing test**

```go
package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCommentConvertsMarkdownToWiki(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/2/issue/PROJ-1/comment" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"id":"10001"}`)
	})

	call(t, m, "jira_comment", map[string]any{"key": "PROJ-1", "body": "See `x` and **y**"})

	got, _ := body["body"].(string)
	if !strings.Contains(got, "{{x}}") || !strings.Contains(got, "*y*") {
		t.Errorf("body not converted to wiki markup: %q", got)
	}
}

func TestCommentRequiresNonEmptyBody(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made for an empty comment")
	})
	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1", "body": "   "})
	if _, err := declFor(t, m, "jira_comment").Handle(context.Background(), raw); err == nil {
		t.Fatal("an empty comment body must error")
	}
}

func TestCommentRefusedByAllowlist(t *testing.T) {
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("refusal must happen before any request")
	}).(module)
	base.cfg.WriteProjects = []string{"OTHER"}

	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1", "body": "hi"})
	if _, err := declFor(t, base, "jira_comment").Handle(context.Background(), raw); err == nil {
		t.Fatal("comment outside the allowlist must be refused")
	}
}

func TestTransitionResolvesStatusNameToID(t *testing.T) {
	var posted map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path != "/rest/api/3/issue/PROJ-1/transitions" {
				t.Errorf("path = %s", r.URL.Path)
			}
			io.WriteString(w, `{"transitions":[{"id":"51","name":"Done","to":{"name":"Done"}},{"id":"171","name":"On Hold","to":{"name":"On Hold"}}]}`)
		case http.MethodPost:
			json.NewDecoder(r.Body).Decode(&posted)
			w.WriteHeader(http.StatusNoContent)
		}
	})

	call(t, m, "jira_transition", map[string]any{"key": "PROJ-1", "status": "done"})

	tr, _ := posted["transition"].(map[string]any)
	if tr["id"] != "51" {
		t.Errorf("transition = %v, want id 51 resolved from the name", posted["transition"])
	}
}

func TestTransitionUnknownStatusListsAvailable(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"transitions":[{"id":"51","name":"Done","to":{"name":"Done"}}]}`)
	})
	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1", "status": "Nope"})
	_, err := declFor(t, m, "jira_transition").Handle(context.Background(), raw)
	if err == nil {
		t.Fatal("unknown status must error")
	}
	if !strings.Contains(err.Error(), "Done") {
		t.Errorf("error must list available transitions, got %q", err)
	}
}

func TestTransitionNeverHardcodesIDs(t *testing.T) {
	// The transitions endpoint must always be consulted; ids are workflow-specific.
	consulted := false
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			consulted = true
			io.WriteString(w, `{"transitions":[{"id":"999","name":"Done","to":{"name":"Done"}}]}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	call(t, m, "jira_transition", map[string]any{"key": "PROJ-1", "status": "Done"})
	if !consulted {
		t.Fatal("transitions endpoint must be consulted rather than assuming an id")
	}
}

func TestTransitionAcceptsNumericIDDirectly(t *testing.T) {
	var posted map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"transitions":[{"id":"51","name":"Done","to":{"name":"Done"}}]}`)
			return
		}
		json.NewDecoder(r.Body).Decode(&posted)
		w.WriteHeader(http.StatusNoContent)
	})
	call(t, m, "jira_transition", map[string]any{"key": "PROJ-1", "status": "51"})
	tr, _ := posted["transition"].(map[string]any)
	if tr["id"] != "51" {
		t.Errorf("transition = %v", posted["transition"])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/jira/ -run 'Comment|Transition' -v`
Expected: FAIL — the stubs return "not implemented".

- [ ] **Step 3: Write minimal implementation**

Delete the `transitionDecl` and `commentDecl` stubs from `module.go`, then create `internal/jira/write.go`:

```go
package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
	"github.com/OxCom/atlassian-mcp-lite/internal/markup"
	"github.com/google/jsonschema-go/jsonschema"
)

func (m module) commentDecl() core.ToolDecl {
	return core.ToolDecl{
		Name:        "jira_comment",
		Actions:     []core.Action{core.ActionWrite},
		Description: "Add a comment to a Jira issue. The body is written in markdown.",
		Schema: func(core.Caps) *jsonschema.Schema {
			return core.ObjectSchema(map[string]*jsonschema.Schema{
				"key":  {Type: "string", Description: "Issue key, e.g. PROJ-123."},
				"body": {Type: "string", Description: "Comment body. Markdown."},
			}, []string{"key", "body"})
		},
		Handle: m.handleComment,
	}
}

type commentArgs struct {
	Key  string `json:"key"`
	Body string `json:"body"`
}

func (m module) handleComment(ctx context.Context, raw json.RawMessage) (any, error) {
	var in commentArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("jira_comment: %w", err)
	}
	key, err := validKey(in.Key)
	if err != nil {
		return nil, fmt.Errorf("jira_comment: %w", err)
	}
	if strings.TrimSpace(in.Body) == "" {
		return nil, fmt.Errorf("jira_comment: body is empty")
	}
	if project := projectOf(key); !m.cfg.AllowProject(project) {
		return nil, fmt.Errorf("jira_comment: writes to project %s are not permitted by ATLAS_WRITE_PROJECTS", project)
	}

	body := map[string]any{"body": markup.ToWiki(in.Body)}
	var res struct {
		ID string `json:"id"`
	}
	path := "/rest/api/2/issue/" + url.PathEscape(key) + "/comment"
	if err := m.client.Do(ctx, http.MethodPost, path, nil, body, &res); err != nil {
		return nil, err
	}
	return map[string]any{"key": key, "commentId": res.ID}, nil
}

func (m module) transitionDecl() core.ToolDecl {
	return core.ToolDecl{
		Name:   "jira_transition",
		Actions: []core.Action{core.ActionDestructive},
		Description: "Move a Jira issue to another status. Destructive: a workflow move can be " +
			"one-way and can trigger notifications and automation.",
		Schema: func(core.Caps) *jsonschema.Schema {
			return core.ObjectSchema(map[string]*jsonschema.Schema{
				"key": {Type: "string", Description: "Issue key, e.g. PROJ-123."},
				"status": {Type: "string",
					Description: "Target status name, or a numeric transition id."},
			}, []string{"key", "status"})
		},
		Handle: m.handleTransition,
	}
}

type transitionArgs struct {
	Key    string `json:"key"`
	Status string `json:"status"`
}

func (m module) handleTransition(ctx context.Context, raw json.RawMessage) (any, error) {
	var in transitionArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("jira_transition: %w", err)
	}
	key, err := validKey(in.Key)
	if err != nil {
		return nil, fmt.Errorf("jira_transition: %w", err)
	}
	want := strings.TrimSpace(in.Status)
	if want == "" {
		return nil, fmt.Errorf("jira_transition: status is empty")
	}
	if project := projectOf(key); !m.cfg.AllowProject(project) {
		return nil, fmt.Errorf("jira_transition: writes to project %s are not permitted by ATLAS_WRITE_PROJECTS", project)
	}

	// Transition ids are workflow-specific and are always looked up, never
	// assumed.
	var list struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	path := "/rest/api/3/issue/" + url.PathEscape(key) + "/transitions"
	if err := m.client.Do(ctx, http.MethodGet, path, nil, nil, &list); err != nil {
		return nil, err
	}

	var id, resolved string
	available := make([]string, 0, len(list.Transitions))
	for _, t := range list.Transitions {
		available = append(available, fmt.Sprintf("%s (id %s)", t.To.Name, t.ID))
		if strings.EqualFold(t.Name, want) || strings.EqualFold(t.To.Name, want) || t.ID == want {
			id, resolved = t.ID, t.To.Name
			break
		}
	}
	if id == "" {
		return nil, fmt.Errorf("jira_transition: %s has no transition to %q; available: %s",
			key, want, strings.Join(available, ", "))
	}

	body := map[string]any{"transition": map[string]any{"id": id}}
	if err := m.client.Do(ctx, http.MethodPost, path, nil, body, nil); err != nil {
		return nil, err
	}
	return map[string]any{"key": key, "status": resolved, "transitionId": id}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/jira/ -v`
Expected: PASS — all Jira tests.

- [ ] **Step 5: Three-engine review gate**

Do not commit yet. Dispatch three independent reviews of *this task's diff only*, in parallel, one
per engine. Each gets the same diff and the task's own "Interfaces" block, and a different lens.

```
Agent(model="fable",  prompt=<completeness lens>)   # spec coverage, missing tests, error paths
Agent(model="haiku",  prompt=<consistency lens>)    # signatures, types, imports, helper clashes
Agent(subagent_type="claude", prompt=<relay>)       # wraps mcp__codex__codex: Go and API correctness
```

Give every reviewer this preamble:

> Review the diff below against `docs/plan/SPEC.md` and this task's Interfaces block. Report findings
> only; do not rewrite. For each: SEVERITY (blocking | important | minor), WHERE (file:line), WHAT
> (one or two sentences), FIX (the concrete change). No praise, no summary. If you find nothing real,
> say so rather than padding.

Then the lens, one per engine:

- **fable** — completeness: does this task implement every spec requirement it claims? What
  behaviour has no test? What happens on a 4xx, a 5xx, a cancelled context, an empty result?
- **haiku** — consistency: do the signatures, types, field names and imports match what earlier
  tasks defined and later tasks will call? Any helper defined twice in one package? Any import
  declared but unused, which fails to compile in Go?
- **codex** — correctness: will this compile? Is the SDK, goldmark, `x/net/html` and Atlassian REST
  usage right? Does each test actually test what it claims, or would it pass regardless of the
  implementation?

Then:

1. Triage every finding. Blocking and important ones get fixed before the commit. Minor ones are
   fixed or recorded with a reason.
2. **Verify any claim about a library's behaviour against that library's source** before acting on
   it. Review 01 found a blocking defect that only source-reading could confirm, and one false
   positive that only source-reading could refute. A reviewer's confident claim is a lead, not a
   fact.
3. Re-run `go test ./...` for the packages this task touches.
4. Append the findings and their dispositions to `docs/plan/REVIEW-LOG.md`, in the same table shape
   as `REVIEW-01-findings.md`, noting which engine found what and where they agreed. Agreement
   across engines is the strongest signal available.

The gate is the review, never a rubber stamp: "three reviews ran and found nothing blocking" is a
complete and valid outcome, recorded in one line.

- [ ] **Step 6: Commit**

```bash
git add internal/jira/
git commit -m "feat(jira): jira_comment and jira_transition with looked-up transition ids"
```

---

### Task 12: Confluence read tools

**Files:**
- Create: `internal/confluence/module.go`
- Create: `internal/confluence/read.go`
- Test: `internal/confluence/read_test.go`

**Interfaces:**
- Consumes: `core` (Tasks 1–5), `markup.FromHTML` (Task 8).
- Produces: `confluence.New() core.Module`, `confluence.NewWith(cfg core.Config, c *core.Client) core.Module`, `confluence.Domain = "confluence"`.

Two API versions are used deliberately. Search is v1 (`/wiki/rest/api/search`) because the v2 API has no CQL search path. Pages are v2. Page bodies are requested as `body-format=view`, which is HTML with macros already expanded by Atlassian.

- [x] **Step 1: Write the failing test**

```go
package confluence

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
)

func newTestModule(t *testing.T, h http.HandlerFunc) core.Module {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := core.Config{
		BaseURL:      srv.URL,
		Email:        "u@example.com",
		Token:        "test-token-value-1234",
		Domains:      map[string]core.Caps{Domain: {Read: true, Write: true, Destructive: true}},
		LimitDefault: 20,
		LimitMax:     50,
	}
	var logs bytes.Buffer
	return NewWith(cfg, core.NewClient(cfg, core.NewLogger("debug", &logs)))
}

func call(t *testing.T, m core.Module, name string, args map[string]any) any {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	for _, d := range m.Tools() {
		if d.Name != name {
			continue
		}
		out, err := d.Handle(context.Background(), raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	t.Fatalf("tool %q not declared", name)
	return nil
}

func declFor(t *testing.T, m core.Module, name string) core.ToolDecl {
	t.Helper()
	for _, d := range m.Tools() {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("tool %q not declared", name)
	return core.ToolDecl{}
}

func TestModuleDeclaresExpectedToolsAndActions(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {})
	want := map[string]core.Action{
		"confluence_search":      core.ActionRead,
		"confluence_get_page":    core.ActionRead,
		"confluence_create_page": core.ActionWrite,
		"confluence_update_page": core.ActionDestructive,
		"confluence_comment":     core.ActionWrite,
	}
	got := map[string]core.Action{}
	for _, d := range m.Tools() {
		if len(d.Actions) != 1 {
			t.Errorf("%s declares %d actions; every Confluence tool spans exactly one", d.Name, len(d.Actions))
			continue
		}
		got[d.Name] = d.Actions[0]
	}
	if len(got) != len(want) {
		t.Fatalf("declared %d tools, want %d: %v", len(got), len(want), got)
	}
	for name, action := range want {
		if got[name] != action {
			t.Errorf("%s action = %v, want %v", name, got[name], action)
		}
	}
}

func TestSearchUsesV1CQLEndpoint(t *testing.T) {
	var gotPath, gotCQL string
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotCQL = r.URL.Path, r.URL.Query().Get("cql")
		io.WriteString(w, `{"results":[{"content":{"id":"123","type":"page"},"title":"T"}],"size":1}`)
	})

	call(t, m, "confluence_search", map[string]any{"cql": "type=page and space=DOCS"})

	// v2 has no CQL search path, so v1 is correct and must not be "fixed".
	if gotPath != "/wiki/rest/api/search" {
		t.Errorf("path = %q, want the v1 search endpoint", gotPath)
	}
	if gotCQL != "type=page and space=DOCS" {
		t.Errorf("cql = %q", gotCQL)
	}
}

func TestSearchLimitCapped(t *testing.T) {
	var gotLimit string
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		gotLimit = r.URL.Query().Get("limit")
		io.WriteString(w, `{"results":[]}`)
	})
	call(t, m, "confluence_search", map[string]any{"cql": "type=page", "limit": 900})
	if gotLimit != "50" {
		t.Errorf("limit = %q, want capped at 50", gotLimit)
	}
}

func TestGetPageRequestsViewFormatAndConvertsToMarkdown(t *testing.T) {
	var gotFormat string
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wiki/api/v2/pages/123" {
			t.Errorf("path = %q", r.URL.Path)
		}
		gotFormat = r.URL.Query().Get("body-format")
		io.WriteString(w, `{"id":"123","title":"T","spaceId":"9","version":{"number":4},"body":{"view":{"value":"<h2>Title</h2><p>Some <strong>bold</strong>.</p>"}}}`)
	})

	out := call(t, m, "confluence_get_page", map[string]any{"id": "123"})

	if gotFormat != "view" {
		t.Errorf("body-format = %q, want view (macros already expanded by Atlassian)", gotFormat)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "## Title") || !strings.Contains(string(raw), "**bold**") {
		t.Errorf("body not converted to markdown: %s", raw)
	}
}

func TestGetPageReturnsVersionForLaterUpdate(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"123","title":"T","version":{"number":7},"body":{"view":{"value":"<p>x</p>"}}}`)
	})
	out := call(t, m, "confluence_get_page", map[string]any{"id": "123"})
	raw, _ := json.Marshal(out)
	// confluence_update_page needs the current version number, so a read must surface it.
	if !strings.Contains(string(raw), `"version":7`) {
		t.Errorf("version not surfaced: %s", raw)
	}
}

func TestGetPageFieldSelection(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"123","title":"T","spaceId":"9","version":{"number":4},"body":{"view":{"value":"<p>x</p>"}}}`)
	}

	// Bare names replace the default set.
	out := call(t, newTestModule(t, handler), "confluence_get_page",
		map[string]any{"id": "123", "fields": []string{"title"}})
	got, _ := out.(map[string]any)
	if _, ok := got["body"]; ok {
		t.Errorf("body must be absent when only title was requested: %v", got)
	}
	if got["title"] != "T" {
		t.Errorf("title missing: %v", got)
	}
	// version is always surfaced: confluence_update_page needs it.
	if _, ok := got["version"]; !ok {
		t.Errorf("version must always be present: %v", got)
	}

	// "-" removes from the default set.
	out = call(t, newTestModule(t, handler), "confluence_get_page",
		map[string]any{"id": "123", "fields": []string{"-body"}})
	got, _ = out.(map[string]any)
	if _, ok := got["body"]; ok {
		t.Errorf("body must be removed: %v", got)
	}
	if _, ok := got["title"]; !ok {
		t.Errorf("title must remain: %v", got)
	}
}

func TestGetPageRejectsMixedFieldForms(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made when field selection is invalid")
	})
	raw, _ := json.Marshal(map[string]any{"id": "123", "fields": []string{"title", "+body"}})
	if _, err := declFor(t, m, "confluence_get_page").Handle(context.Background(), raw); err == nil {
		t.Fatal("mixing bare and prefixed field names must error")
	}
}

func TestGetPageRejectsNonNumericID(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made for a malformed id")
	})
	raw, _ := json.Marshal(map[string]any{"id": "../../secrets"})
	if _, err := declFor(t, m, "confluence_get_page").Handle(context.Background(), raw); err == nil {
		t.Fatal("a non-numeric page id must be rejected before any request")
	}
}

func TestSearchRequiresCQL(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made without a query")
	})
	raw, _ := json.Marshal(map[string]any{"cql": "  "})
	if _, err := declFor(t, m, "confluence_search").Handle(context.Background(), raw); err == nil {
		t.Fatal("an empty CQL query must error")
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/confluence/ -v`
Expected: FAIL — package does not exist.

> **Amended after the Task 12 review gate (2026-09-01).** The truncation block below is defective as
> written: with `_links.next` set and no `totalSize` it reports "N of N matches returned", claiming
> everything arrived while flagging truncation, and with neither signal present a full page is not
> flagged at all. The landed code uses `TotalSize *int` plus a three-signal ladder — see
> `internal/confluence/read.go`'s `truncationNote`, which mirrors the Jira module's `IsLast *bool`
> for the same reason. Task 13 must not copy the snippet below verbatim. Two further amendments:
> `version` is decoded as a pointer and a missing or non-positive version is an error, and
> `include-version=true` is sent explicitly.

- [x] **Step 3: Write minimal implementation**

`internal/confluence/module.go`:

```go
// Package confluence declares the Confluence tools. Like the jira package it
// must not import the MCP SDK, read the environment, build an HTTP client, or
// log — core owns all of that.
package confluence

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
)

// Domain is the config and gating key for this module.
const Domain = "confluence"

// rePageID matches Confluence's numeric content ids. Enforced before any id
// reaches a URL path.
var rePageID = regexp.MustCompile(`^[0-9]+$`)

type module struct {
	cfg    core.Config
	client *core.Client
}

// New returns a declaration-only module for domain discovery.
func New() core.Module { return module{} }

// NewWith returns a functional module.
func NewWith(cfg core.Config, c *core.Client) core.Module {
	return module{cfg: cfg, client: c}
}

func (m module) Domain() string { return Domain }

func (m module) Tools() []core.ToolDecl {
	return []core.ToolDecl{
		m.searchDecl(),
		m.getPageDecl(),
		m.createPageDecl(),
		m.updatePageDecl(),
		m.commentDecl(),
	}
}

func validPageID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if !rePageID.MatchString(id) {
		return "", fmt.Errorf("invalid page id %q: expected a numeric id", id)
	}
	return id, nil
}

func (m module) clampLimit(requested int) int {
	if requested <= 0 {
		return m.cfg.LimitDefault
	}
	if requested > m.cfg.LimitMax {
		return m.cfg.LimitMax
	}
	return requested
}
```

`internal/confluence/read.go`:

```go
package confluence

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
	"github.com/OxCom/atlassian-mcp-lite/internal/markup"
	"github.com/google/jsonschema-go/jsonschema"
)

func (m module) searchDecl() core.ToolDecl {
	return core.ToolDecl{
		Name:        "confluence_search",
		Actions:     []core.Action{core.ActionRead},
		Description: "Search Confluence content with CQL.",
		Schema: func(core.Caps) *jsonschema.Schema {
			return core.ObjectSchema(map[string]*jsonschema.Schema{
				"cql":   {Type: "string", Description: `A CQL query, e.g. type=page and space=DOCS.`},
				"limit": {Type: "integer", Description: "Maximum results to return."},
			}, []string{"cql"})
		},
		Handle: m.handleSearch,
	}
}

type searchArgs struct {
	CQL   string `json:"cql"`
	Limit int    `json:"limit"`
}

func (m module) handleSearch(ctx context.Context, raw json.RawMessage) (any, error) {
	var in searchArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("confluence_search: %w", err)
	}
	if strings.TrimSpace(in.CQL) == "" {
		return nil, fmt.Errorf("confluence_search: cql is required")
	}
	limit := m.clampLimit(in.Limit)

	var res struct {
		Results []struct {
			Title   string `json:"title"`
			Content struct {
				ID     string `json:"id"`
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"content"`
		} `json:"results"`
		Size int `json:"size"`
		// totalSize is the full match count; _links.next is present only when
		// another page exists. Either is a better truncation signal than
		// counting results against the limit.
		TotalSize int `json:"totalSize"`
		Links     struct {
			Next string `json:"next"`
		} `json:"_links"`
	}
	// The v2 API has no CQL search path, so search stays on v1.
	q := url.Values{"cql": {in.CQL}, "limit": {strconv.Itoa(limit)}}
	if err := m.client.Do(ctx, http.MethodGet, "/wiki/rest/api/search", q, nil, &res); err != nil {
		return nil, err
	}

	hits := make([]map[string]any, 0, len(res.Results))
	for _, r := range res.Results {
		hits = append(hits, map[string]any{
			"id":    r.Content.ID,
			"type":  r.Content.Type,
			"title": r.Title,
		})
	}
	out := map[string]any{"results": hits, "returned": len(hits)}
	if res.Links.Next != "" || res.TotalSize > len(hits) {
		out["truncated"] = fmt.Sprintf(
			"%d of %d matches returned at limit %d; narrow the CQL or raise limit (max %d). paging is not supported",
			len(hits), max(res.TotalSize, len(hits)), limit, m.cfg.LimitMax)
	}
	return out, nil
}

// pageDefaults is the default field set for confluence_get_page. Same grammar
// as the Jira read tools: bare names replace, "+" adds, "-" removes.
var pageDefaults = []string{"id", "title", "spaceId", "version", "body"}

func fieldsProperty() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "array",
		Items:       &jsonschema.Schema{Type: "string"},
		Description: `Fields to return. Omit for the default set. Bare names replace the default set entirely; "+name" adds to it; "-name" removes from it. Do not mix bare and prefixed names.`,
	}
}

func (m module) getPageDecl() core.ToolDecl {
	return core.ToolDecl{
		Name:        "confluence_get_page",
		Actions:     []core.Action{core.ActionRead},
		Description: "Get a Confluence page. The body is returned as markdown.",
		Schema: func(core.Caps) *jsonschema.Schema {
			return core.ObjectSchema(map[string]*jsonschema.Schema{
				"id":     {Type: "string", Description: "Numeric page id."},
				"fields": fieldsProperty(),
			}, []string{"id"})
		},
		Handle: m.handleGetPage,
	}
}

type getPageArgs struct {
	ID     string   `json:"id"`
	Fields []string `json:"fields"`
}

func (m module) handleGetPage(ctx context.Context, raw json.RawMessage) (any, error) {
	var in getPageArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("confluence_get_page: %w", err)
	}
	id, err := validPageID(in.ID)
	if err != nil {
		return nil, fmt.Errorf("confluence_get_page: %w", err)
	}
	fields, err := core.ResolveFields(pageDefaults, in.Fields)
	if err != nil {
		return nil, fmt.Errorf("confluence_get_page: %w", err)
	}

	// view is requested rather than storage: Atlassian has already expanded
	// macros, so markup.FromHTML needs no macro handling. body-format=wiki
	// does not exist for reads and returns HTTP 400.
	q := url.Values{"body-format": {"view"}}
	var res struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		SpaceID string `json:"spaceId"`
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
		Body struct {
			View struct {
				Value string `json:"value"`
			} `json:"view"`
		} `json:"body"`
	}
	if err := m.client.Do(ctx, http.MethodGet, "/wiki/api/v2/pages/"+url.PathEscape(id), q, nil, &res); err != nil {
		return nil, err
	}

	// The v2 endpoint has no field-selection parameter, so filtering happens
	// here. The saving is in what reaches the model's context, which is the
	// cost that matters.
	all := map[string]any{
		"id":      res.ID,
		"title":   res.Title,
		"spaceId": res.SpaceID,
		"version": res.Version.Number,
		"body":    markup.FromHTML(res.Body.View.Value),
	}
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		if v, ok := all[f]; ok {
			out[f] = v
		}
	}
	// version is what confluence_update_page needs for optimistic locking, so
	// it is always surfaced even if the caller removed it.
	if _, ok := out["version"]; !ok {
		out["version"] = res.Version.Number
	}
	return out, nil
}
```

Add temporary stubs for `createPageDecl`, `updatePageDecl` and `commentDecl` to `module.go`, mirroring Task 9's pattern, and delete them in Task 13.

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/confluence/ -v`
Expected: PASS.

- [x] **Step 5: Three-engine review gate**

Do not commit yet. Dispatch three independent reviews of *this task's diff only*, in parallel, one
per engine. Each gets the same diff and the task's own "Interfaces" block, and a different lens.

```
Agent(model="fable",  prompt=<completeness lens>)   # spec coverage, missing tests, error paths
Agent(model="haiku",  prompt=<consistency lens>)    # signatures, types, imports, helper clashes
Agent(subagent_type="claude", prompt=<relay>)       # wraps mcp__codex__codex: Go and API correctness
```

Give every reviewer this preamble:

> Review the diff below against `docs/plan/SPEC.md` and this task's Interfaces block. Report findings
> only; do not rewrite. For each: SEVERITY (blocking | important | minor), WHERE (file:line), WHAT
> (one or two sentences), FIX (the concrete change). No praise, no summary. If you find nothing real,
> say so rather than padding.

Then the lens, one per engine:

- **fable** — completeness: does this task implement every spec requirement it claims? What
  behaviour has no test? What happens on a 4xx, a 5xx, a cancelled context, an empty result?
- **haiku** — consistency: do the signatures, types, field names and imports match what earlier
  tasks defined and later tasks will call? Any helper defined twice in one package? Any import
  declared but unused, which fails to compile in Go?
- **codex** — correctness: will this compile? Is the SDK, goldmark, `x/net/html` and Atlassian REST
  usage right? Does each test actually test what it claims, or would it pass regardless of the
  implementation?

Then:

1. Triage every finding. Blocking and important ones get fixed before the commit. Minor ones are
   fixed or recorded with a reason.
2. **Verify any claim about a library's behaviour against that library's source** before acting on
   it. Review 01 found a blocking defect that only source-reading could confirm, and one false
   positive that only source-reading could refute. A reviewer's confident claim is a lead, not a
   fact.
3. Re-run `go test ./...` for the packages this task touches.
4. Append the findings and their dispositions to `docs/plan/REVIEW-LOG.md`, in the same table shape
   as `REVIEW-01-findings.md`, noting which engine found what and where they agreed. Agreement
   across engines is the strongest signal available.

The gate is the review, never a rubber stamp: "three reviews ran and found nothing blocking" is a
complete and valid outcome, recorded in one line.

- [ ] **Step 6: Commit**

```bash
git add internal/confluence/
git commit -m "feat(confluence): module scaffold with CQL search and page read"
```

---

### Task 13: Confluence write tools

**Files:**
- Create: `internal/confluence/write.go`
- Modify: `internal/confluence/module.go` — delete the three stubs
- Test: `internal/confluence/write_test.go`

**Interfaces:**
- Consumes: Task 12; `markup.ToWiki` (Task 7); `core.Config.AllowSpace` (Task 1).
- Produces: `createPageDecl`, `updatePageDecl`, `commentDecl`.

All three send `representation: "wiki"`, which Atlassian expands to storage format server-side. `confluence_update_page` is destructive: it replaces the body and requires the current version number, which Confluence uses for optimistic locking.

- [x] **Step 1: Write the failing test**

```go
package confluence

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCreatePageSendsWikiRepresentation(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		// The v2 create endpoint needs a numeric spaceId, so the space key is
		// resolved first. Both calls land on this handler.
		if r.URL.Path == "/wiki/api/v2/spaces" {
			io.WriteString(w, `{"results":[{"id":"9","key":"DOCS"}]}`)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/wiki/api/v2/pages" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"id":"555","title":"New"}`)
	})

	call(t, m, "confluence_create_page", map[string]any{
		"space": "DOCS",
		"title": "New",
		"body":  "## Heading\n\nSome **bold** text",
	})

	if body["spaceId"] != "9" {
		t.Errorf("spaceId = %v, want the key resolved to a numeric id", body["spaceId"])
	}
	b, _ := body["body"].(map[string]any)
	if b["representation"] != "wiki" {
		t.Errorf("representation = %v, want wiki (no XHTML generator exists here)", b["representation"])
	}
	value, _ := b["value"].(string)
	if !strings.Contains(value, "h2. Heading") || !strings.Contains(value, "*bold*") {
		t.Errorf("body not converted to wiki markup: %q", value)
	}
}

func TestCreatePageRefusedByAllowlist(t *testing.T) {
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("refusal must happen before any request")
	}).(module)
	base.cfg.WriteSpaces = []string{"OTHER"}

	raw, _ := json.Marshal(map[string]any{"space": "DOCS", "title": "T", "body": "x"})
	if _, err := declFor(t, base, "confluence_create_page").Handle(context.Background(), raw); err == nil {
		t.Fatal("create outside the space allowlist must be refused")
	}
}

func TestUpdatePageFetchesVersionAndIncrements(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			io.WriteString(w, `{"id":"123","title":"Old","spaceId":"9","version":{"number":7},"body":{"view":{"value":"<p>old</p>"}}}`)
		case http.MethodPut:
			if r.URL.Path != "/wiki/api/v2/pages/123" {
				t.Errorf("path = %q", r.URL.Path)
			}
			json.NewDecoder(r.Body).Decode(&body)
			io.WriteString(w, `{"id":"123","version":{"number":8}}`)
		}
	})

	call(t, m, "confluence_update_page", map[string]any{"id": "123", "body": "new text"})

	version, _ := body["version"].(map[string]any)
	if version["number"] != float64(8) {
		t.Errorf("version.number = %v, want current+1 for optimistic locking", version["number"])
	}
	if body["title"] != "Old" {
		t.Errorf("title = %v; omitting title must preserve the existing one, not blank it", body["title"])
	}
}

func TestUpdatePageKeepsSuppliedTitle(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"id":"123","title":"Old","version":{"number":1},"body":{"view":{"value":""}}}`)
			return
		}
		json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"id":"123","version":{"number":2}}`)
	})
	call(t, m, "confluence_update_page", map[string]any{"id": "123", "title": "Renamed", "body": "x"})
	if body["title"] != "Renamed" {
		t.Errorf("title = %v, want Renamed", body["title"])
	}
}

func TestUpdatePageRequiresBody(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made without a body")
	})
	raw, _ := json.Marshal(map[string]any{"id": "123", "body": "  "})
	if _, err := declFor(t, m, "confluence_update_page").Handle(context.Background(), raw); err == nil {
		t.Fatal("an empty page body must error rather than blanking the page")
	}
}

// A comment is a write, so the space allowlist must apply to it. Without this
// the allowlist would cover page creation and replacement but not commenting.
func TestCommentRefusedByAllowlist(t *testing.T) {
	posted := false
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posted = true
		}
		switch {
		case r.URL.Path == "/wiki/api/v2/pages/123":
			io.WriteString(w, `{"id":"123","spaceId":"9","title":"T","version":{"number":1},"body":{"view":{"value":""}}}`)
		case r.URL.Path == "/wiki/api/v2/spaces/9":
			io.WriteString(w, `{"id":"9","key":"OTHERSPACE"}`)
		default:
			io.WriteString(w, `{"id":"9001"}`)
		}
	}).(module)
	base.cfg.WriteSpaces = []string{"DOCS"}

	raw, _ := json.Marshal(map[string]any{"page_id": "123", "body": "hi"})
	if _, err := declFor(t, base, "confluence_comment").Handle(context.Background(), raw); err == nil {
		t.Fatal("comment outside the space allowlist must be refused")
	}
	if posted {
		t.Error("refusal must happen before the comment is posted")
	}
}

func TestSpaceIDForRequiresAnExactKeyMatch(t *testing.T) {
	// Server-side filtering is not trusted: a broad response must not place a
	// page in a space the allowlist never approved.
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wiki/api/v2/spaces" {
			io.WriteString(w, `{"results":[{"id":"77","key":"SOMETHINGELSE"}]}`)
			return
		}
		t.Error("create must not be attempted when the space key does not match")
	})
	raw, _ := json.Marshal(map[string]any{"space": "DOCS", "title": "T", "body": "x"})
	if _, err := declFor(t, m, "confluence_create_page").Handle(context.Background(), raw); err == nil {
		t.Fatal("a non-matching space key must be an error")
	}
}

func TestCommentPostsFooterComment(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wiki/api/v2/footer-comments" {
			t.Errorf("path = %q, want footer-comments", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"id":"9001"}`)
	})

	call(t, m, "confluence_comment", map[string]any{"page_id": "123", "body": "a `note`"})

	if body["pageId"] != "123" {
		t.Errorf("pageId = %v", body["pageId"])
	}
	b, _ := body["body"].(map[string]any)
	if b["representation"] != "wiki" {
		t.Errorf("representation = %v, want wiki", b["representation"])
	}
	if v, _ := b["value"].(string); !strings.Contains(v, "{{note}}") {
		t.Errorf("body not converted: %q", v)
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/confluence/ -run 'Create|Update|Comment' -v`
Expected: FAIL — the stubs return "not implemented".

- [x] **Step 3: Write minimal implementation**

Delete the three stubs from `module.go`, then create `internal/confluence/write.go`:

```go
package confluence

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
	"github.com/OxCom/atlassian-mcp-lite/internal/markup"
	"github.com/google/jsonschema-go/jsonschema"
)

// wikiBody builds a v2 body object. Every write uses the wiki representation:
// Atlassian expands it to storage format server-side, so no XHTML generator
// exists in this codebase.
func wikiBody(md string) map[string]any {
	return map[string]any{"representation": "wiki", "value": markup.ToWiki(md)}
}

func (m module) createPageDecl() core.ToolDecl {
	return core.ToolDecl{
		Name:        "confluence_create_page",
		Actions:     []core.Action{core.ActionWrite},
		Description: "Create a Confluence page. The body is written in markdown.",
		Schema: func(core.Caps) *jsonschema.Schema {
			return core.ObjectSchema(map[string]*jsonschema.Schema{
				"space":     {Type: "string", Description: "Space key, e.g. DOCS."},
				"title":     {Type: "string", Description: "Page title."},
				"body":      {Type: "string", Description: "Page body. Markdown."},
				"parent_id": {Type: "string", Description: "Optional parent page id."},
			}, []string{"space", "title", "body"})
		},
		Handle: m.handleCreatePage,
	}
}

type createPageArgs struct {
	Space    string `json:"space"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	ParentID string `json:"parent_id"`
}

func (m module) handleCreatePage(ctx context.Context, raw json.RawMessage) (any, error) {
	var in createPageArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("confluence_create_page: %w", err)
	}
	space := strings.TrimSpace(in.Space)
	if space == "" || strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Body) == "" {
		return nil, fmt.Errorf("confluence_create_page: space, title and body are all required")
	}
	if !m.cfg.AllowSpace(space) {
		return nil, fmt.Errorf("confluence_create_page: writes to space %s are not permitted by ATLAS_WRITE_SPACES", space)
	}

	spaceID, err := m.spaceIDFor(ctx, space)
	if err != nil {
		return nil, fmt.Errorf("confluence_create_page: %w", err)
	}

	body := map[string]any{
		"spaceId": spaceID,
		"status":  "current",
		"title":   in.Title,
		"body":    wikiBody(in.Body),
	}
	if in.ParentID != "" {
		pid, err := validPageID(in.ParentID)
		if err != nil {
			return nil, fmt.Errorf("confluence_create_page: parent_id: %w", err)
		}
		body["parentId"] = pid
	}

	var res struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := m.client.Do(ctx, http.MethodPost, "/wiki/api/v2/pages", nil, body, &res); err != nil {
		return nil, err
	}
	return map[string]any{"id": res.ID, "title": res.Title, "space": space}, nil
}

func (m module) updatePageDecl() core.ToolDecl {
	return core.ToolDecl{
		Name:   "confluence_update_page",
		Actions: []core.Action{core.ActionDestructive},
		Description: "Replace a Confluence page's body. Destructive: the previous body is " +
			"superseded, though Confluence keeps it in page history.",
		Schema: func(core.Caps) *jsonschema.Schema {
			return core.ObjectSchema(map[string]*jsonschema.Schema{
				"id":    {Type: "string", Description: "Numeric page id."},
				"title": {Type: "string", Description: "Optional new title. Omit to keep the current one."},
				"body":  {Type: "string", Description: "Replacement body. Markdown."},
			}, []string{"id", "body"})
		},
		Handle: m.handleUpdatePage,
	}
}

type updatePageArgs struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (m module) handleUpdatePage(ctx context.Context, raw json.RawMessage) (any, error) {
	var in updatePageArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("confluence_update_page: %w", err)
	}
	id, err := validPageID(in.ID)
	if err != nil {
		return nil, fmt.Errorf("confluence_update_page: %w", err)
	}
	if strings.TrimSpace(in.Body) == "" {
		return nil, fmt.Errorf("confluence_update_page: body is empty; refusing to blank the page")
	}

	// Confluence uses the version number for optimistic locking, and an
	// omitted title blanks it, so the current state is always fetched first.
	var current struct {
		Title   string `json:"title"`
		SpaceID string `json:"spaceId"`
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
	}
	path := "/wiki/api/v2/pages/" + url.PathEscape(id)
	if err := m.client.Do(ctx, http.MethodGet, path, url.Values{"body-format": {"view"}}, nil, &current); err != nil {
		return nil, err
	}

	if len(m.cfg.WriteSpaces) > 0 {
		key, err := m.spaceKeyFor(ctx, current.SpaceID)
		if err != nil {
			return nil, fmt.Errorf("confluence_update_page: %w", err)
		}
		if !m.cfg.AllowSpace(key) {
			return nil, fmt.Errorf("confluence_update_page: writes to space %s are not permitted by ATLAS_WRITE_SPACES", key)
		}
	}

	title := current.Title
	if strings.TrimSpace(in.Title) != "" {
		title = in.Title
	}

	body := map[string]any{
		"id":      id,
		"status":  "current",
		"title":   title,
		"body":    wikiBody(in.Body),
		"version": map[string]any{"number": current.Version.Number + 1},
	}
	var res struct {
		ID      string `json:"id"`
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
	}
	if err := m.client.Do(ctx, http.MethodPut, path, nil, body, &res); err != nil {
		return nil, err
	}
	return map[string]any{"id": res.ID, "title": title, "version": res.Version.Number}, nil
}

func (m module) commentDecl() core.ToolDecl {
	return core.ToolDecl{
		Name:        "confluence_comment",
		Actions:     []core.Action{core.ActionWrite},
		Description: "Add a footer comment to a Confluence page. The body is written in markdown.",
		Schema: func(core.Caps) *jsonschema.Schema {
			return core.ObjectSchema(map[string]*jsonschema.Schema{
				"page_id": {Type: "string", Description: "Numeric page id."},
				"body":    {Type: "string", Description: "Comment body. Markdown."},
			}, []string{"page_id", "body"})
		},
		Handle: m.handleComment,
	}
}

type commentArgs struct {
	PageID string `json:"page_id"`
	Body   string `json:"body"`
}

func (m module) handleComment(ctx context.Context, raw json.RawMessage) (any, error) {
	var in commentArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("confluence_comment: %w", err)
	}
	id, err := validPageID(in.PageID)
	if err != nil {
		return nil, fmt.Errorf("confluence_comment: %w", err)
	}
	if strings.TrimSpace(in.Body) == "" {
		return nil, fmt.Errorf("confluence_comment: body is empty")
	}

	// A comment is a write. Without this check the space allowlist would apply
	// to page creation and replacement but not to commenting, which is a hole
	// wide enough to exfiltrate through.
	if len(m.cfg.WriteSpaces) > 0 {
		key, err := m.spaceKeyForPage(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("confluence_comment: %w", err)
		}
		if !m.cfg.AllowSpace(key) {
			return nil, fmt.Errorf("confluence_comment: writes to space %s are not permitted by ATLAS_WRITE_SPACES", key)
		}
	}

	body := map[string]any{"pageId": id, "body": wikiBody(in.Body)}
	var res struct {
		ID string `json:"id"`
	}
	if err := m.client.Do(ctx, http.MethodPost, "/wiki/api/v2/footer-comments", nil, body, &res); err != nil {
		return nil, err
	}
	return map[string]any{"pageId": id, "commentId": res.ID}, nil
}

// spaceIDFor resolves a space key to the numeric id the v2 API requires.
//
// The returned key is compared explicitly rather than trusting server-side
// filtering: taking results[0] on faith would let an unexpectedly broad
// response place a page in a different space than the one that passed the
// allowlist check.
func (m module) spaceIDFor(ctx context.Context, key string) (string, error) {
	var res struct {
		Results []struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"results"`
	}
	q := url.Values{"keys": {key}, "limit": {"25"}}
	if err := m.client.Do(ctx, http.MethodGet, "/wiki/api/v2/spaces", q, nil, &res); err != nil {
		return "", err
	}
	var found string
	for _, r := range res.Results {
		if strings.EqualFold(r.Key, key) {
			if found != "" {
				return "", fmt.Errorf("space key %q matched more than one space", key)
			}
			found = r.ID
		}
	}
	if found == "" {
		return "", fmt.Errorf("no space with key %q is visible to this account", key)
	}
	return found, nil
}

// spaceKeyForPage resolves the space key that owns a page, for allowlist checks.
func (m module) spaceKeyForPage(ctx context.Context, pageID string) (string, error) {
	var page struct {
		SpaceID string `json:"spaceId"`
	}
	if err := m.client.Do(ctx, http.MethodGet, "/wiki/api/v2/pages/"+url.PathEscape(pageID), nil, nil, &page); err != nil {
		return "", err
	}
	return m.spaceKeyFor(ctx, page.SpaceID)
}

// spaceKeyFor resolves a numeric space id back to its key, for allowlist checks.
func (m module) spaceKeyFor(ctx context.Context, id string) (string, error) {
	var res struct {
		Key string `json:"key"`
	}
	if err := m.client.Do(ctx, http.MethodGet, "/wiki/api/v2/spaces/"+url.PathEscape(id), nil, nil, &res); err != nil {
		return "", err
	}
	return res.Key, nil
}
```


#### Also in this task: `cmd/atlassian-mcp-lite/main.go`

Moved here from Task 6 (decided 2026-09-01) because it imports `internal/jira` and
`internal/confluence`, which only exist once this task lands. Content is exactly as Task 6 specified
it — the boot logger, the two-phase registry, `core.Load`, the redacting logger, `core.NewClient`,
`core.NewServer`, the zero-tools guard, and `signal.NotifyContext` for `SIGTERM` — and the file is
kept verbatim at `docs/plan/main.go.deferred` so nothing has to be reconstructed from memory. After
adding it, `go build ./...` succeeds for the first time.

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./... -v`
Expected: PASS across all packages. `go build ./...` now succeeds, because `main.go` from Task 6 can resolve both modules' real constructors.

- [x] **Step 5: Three-engine review gate**

Do not commit yet. Dispatch three independent reviews of *this task's diff only*, in parallel, one
per engine. Each gets the same diff and the task's own "Interfaces" block, and a different lens.

```
Agent(model="fable",  prompt=<completeness lens>)   # spec coverage, missing tests, error paths
Agent(model="haiku",  prompt=<consistency lens>)    # signatures, types, imports, helper clashes
Agent(subagent_type="claude", prompt=<relay>)       # wraps mcp__codex__codex: Go and API correctness
```

Give every reviewer this preamble:

> Review the diff below against `docs/plan/SPEC.md` and this task's Interfaces block. Report findings
> only; do not rewrite. For each: SEVERITY (blocking | important | minor), WHERE (file:line), WHAT
> (one or two sentences), FIX (the concrete change). No praise, no summary. If you find nothing real,
> say so rather than padding.

Then the lens, one per engine:

- **fable** — completeness: does this task implement every spec requirement it claims? What
  behaviour has no test? What happens on a 4xx, a 5xx, a cancelled context, an empty result?
- **haiku** — consistency: do the signatures, types, field names and imports match what earlier
  tasks defined and later tasks will call? Any helper defined twice in one package? Any import
  declared but unused, which fails to compile in Go?
- **codex** — correctness: will this compile? Is the SDK, goldmark, `x/net/html` and Atlassian REST
  usage right? Does each test actually test what it claims, or would it pass regardless of the
  implementation?

Then:

1. Triage every finding. Blocking and important ones get fixed before the commit. Minor ones are
   fixed or recorded with a reason.
2. **Verify any claim about a library's behaviour against that library's source** before acting on
   it. Review 01 found a blocking defect that only source-reading could confirm, and one false
   positive that only source-reading could refute. A reviewer's confident claim is a lead, not a
   fact.
3. Re-run `go test ./...` for the packages this task touches.
4. Append the findings and their dispositions to `docs/plan/REVIEW-LOG.md`, in the same table shape
   as `REVIEW-01-findings.md`, noting which engine found what and where they agreed. Agreement
   across engines is the strongest signal available.

The gate is the review, never a rubber stamp: "three reviews ran and found nothing blocking" is a
complete and valid outcome, recorded in one line.

- [ ] **Step 6: Commit**

```bash
git add internal/confluence/
git commit -m "feat(confluence): page create, update and comment via wiki representation"
```

---

### Task 14: Packaging, configuration surface and documentation

**Files:**
- Create: `Dockerfile`
- Create: `compose.yaml`
- Create: `.env.example`
- Create: `README.md`
- Already present, do not recreate: `compose.dev.yaml`, `compose.override.yaml.example`, `Makefile`, `.golangci.yml`, `.github/workflows/ci.yml`, `.gitignore`
- Modify: `CLAUDE.md` — replace the "no source code" description with real build commands and architecture
- Test: `internal/core/env_example_test.go`

**Interfaces:**
- Consumes: every environment variable read in Task 1.
- Produces: no Go API. Produces the operator-facing surface.

The one automated check here guards a real failure mode: `.env.example` drifting from the variables the code actually reads, so an operator sets a name that is silently ignored.

- [ ] **Step 1: Write the failing test**

```go
package core

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every ATLAS_ variable named in .env.example must be one the code reads, and
// every variable the code reads must appear in .env.example. Drift here is
// silent: an operator sets a name that does nothing.
func TestEnvExampleMatchesCodeVariables(t *testing.T) {
	raw, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}

	documented := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		if name, _, ok := strings.Cut(line, "="); ok {
			if name = strings.TrimSpace(name); strings.HasPrefix(name, "ATLAS_") {
				documented[name] = true
			}
		}
	}

	// Domain variables are derived, so they are checked for the two shipped modules.
	read := map[string]bool{
		"ATLAS_BASE_URL": true, "ATLAS_EMAIL": true, "ATLAS_TOKEN": true,
		"ATLAS_LOG": true, "ATLAS_LIMIT_DEFAULT": true, "ATLAS_LIMIT_MAX": true,
		"ATLAS_EPIC_FIELD_ID":  true,
		"ATLAS_WRITE_PROJECTS": true, "ATLAS_WRITE_SPACES": true,
	}
	for _, d := range []string{"JIRA", "CONFLUENCE"} {
		for _, a := range []string{"READ", "WRITE", "DESTRUCTIVE"} {
			read["ATLAS_"+d+"_"+a] = true
		}
	}

	for name := range read {
		if !documented[name] {
			t.Errorf("%s is read by the code but missing from .env.example", name)
		}
	}
	for name := range documented {
		if !read[name] {
			t.Errorf("%s appears in .env.example but nothing reads it", name)
		}
	}
}

// The example must never ship a real-looking credential.
func TestEnvExampleShipsNoToken(t *testing.T) {
	raw, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	if regexp.MustCompile(`(?m)^ATLAS_TOKEN=\S`).Match(raw) {
		t.Error("ATLAS_TOKEN must be left empty in .env.example")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run TestEnvExample -v`
Expected: FAIL — `.env.example` does not exist.

- [ ] **Step 3: Write the files**

`.env.example`:

```bash
# Copy to a private location, chmod 0600, and point compose at it:
#   mkdir -p ~/.config/atlassian-mcp-lite
#   cp .env.example ~/.config/atlassian-mcp-lite/env
#   chmod 600 ~/.config/atlassian-mcp-lite/env
# Never commit the filled-in copy.

# --- Connection ---
ATLAS_BASE_URL=https://your-domain.atlassian.net
ATLAS_EMAIL=you@example.com
# Create at https://id.atlassian.com/manage-profile/security/api-tokens
# Note: API token scopes do not apply to Basic auth. This credential carries the
# full authority of the account that created it. Prefer a dedicated service
# account whose project permissions are limited to what you actually need.
ATLAS_TOKEN=

# --- Capabilities ---
# read        returns data
# write       additive and reversible: comment, assign, set fields, create page
# destructive overwrites or moves state: summary, description, transition,
#             page body replacement
ATLAS_JIRA_READ=true
ATLAS_JIRA_WRITE=true
ATLAS_JIRA_DESTRUCTIVE=false
ATLAS_CONFLUENCE_READ=true
ATLAS_CONFLUENCE_WRITE=true
ATLAS_CONFLUENCE_DESTRUCTIVE=false

# --- Write allowlist (recommended) ---
# Unset or empty means unrestricted. A non-empty value is a strict allowlist:
# any other target is refused before a request is made. Reads are never
# restricted.
#ATLAS_WRITE_PROJECTS=PROJ,OPS
#ATLAS_WRITE_SPACES=DOCS

# --- Behaviour ---
# info logs failures with the upstream error text; debug adds method, path,
# status, duration and body sizes. Successful response bodies are never logged.
ATLAS_LOG=info
ATLAS_LIMIT_DEFAULT=20
ATLAS_LIMIT_MAX=50

# The Epic Link custom field id differs between Jira sites. Find yours with:
#   GET /rest/api/3/issue/{anyIssueKey}/editmeta
# and look for the field named "Epic Link". Team-managed projects use `parent`
# instead and can ignore this.
ATLAS_EPIC_FIELD_ID=customfield_10014
```

`Dockerfile`:

```dockerfile
# syntax=docker/dockerfile:1
# The syntax directive is required for RUN --mount=type=secret below.

# Build the static binary.
FROM golang:1.27-trixie AS build

# Module source. Defaults are the public ones, so a plain `docker build` needs
# no arguments. compose.override.yaml supplies Artifactory values on a
# corporate network.
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ARG GOPRIVATE=
ENV GOPROXY=${GOPROXY} GOSUMDB=${GOSUMDB} GOPRIVATE=${GOPRIVATE}

WORKDIR /src
COPY go.mod go.sum ./

# Both secrets are optional. When none is passed the mounts are empty, the
# guards do nothing, and the build behaves exactly as it does outside the
# office. Secrets are used rather than build args so neither the CA nor the
# Artifactory token is written into a layer or shown by `docker history`.
RUN --mount=type=secret,id=corp_ca,target=/run/secrets/corp_ca \
    --mount=type=secret,id=netrc,target=/root/.netrc \
    set -eu; \
    if [ -s /run/secrets/corp_ca ]; then \
        cat /run/secrets/corp_ca >> /etc/ssl/certs/ca-certificates.crt; \
    fi; \
    go mod download

COPY . .
# CGO off and a stripped binary so the final image can be scratch.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/atlassian-mcp-lite ./cmd/atlassian-mcp-lite

# Final image: no shell, no package manager, no libc.
FROM scratch
# TLS roots, needed to verify the Atlassian certificate.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/atlassian-mcp-lite /atlassian-mcp-lite
# Non-root. scratch has no /etc/passwd, so the id is given numerically.
USER 65532:65532
ENTRYPOINT ["/atlassian-mcp-lite"]
```

`compose.yaml`:

```yaml
services:
  atlassian-mcp-lite:
    build:
      context: .
      # Defaults are public. compose.override.yaml overrides these and adds the
      # `secrets:` list on a corporate network; nothing here needs to change.
      args:
        GOPROXY: ${GOPROXY:-https://proxy.golang.org,direct}
        GOSUMDB: ${GOSUMDB:-sum.golang.org}
    image: atlassian-mcp-lite:local
    # stdio transport: the MCP client owns this process's stdin and stdout.
    stdin_open: true
    tty: false
    # Points at the private env file by default; override with ATLAS_ENV_FILE.
    env_file:
      - ${ATLAS_ENV_FILE:-${HOME}/.config/atlassian-mcp-lite/env}
    read_only: true
    cap_drop:
      - ALL
    security_opt:
      - no-new-privileges:true
    # No volumes. This server never reads a local file, and no tool accepts a
    # filesystem path, so there is nothing to mount.
    #
    # Egress is NOT restricted here. read_only, cap_drop and no-new-privileges
    # constrain the filesystem and privileges, not network destinations. SSRF is
    # prevented instead by the base URL being a validated configuration
    # constant that tool input cannot influence. Operators wanting outbound
    # filtering must add a proxy network or host firewall rules themselves.
```

Compose is for building and for a quick manual smoke test. MCP clients launch
the container directly, because the client must own stdin and stdout:

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

`README.md`:

````markdown
# atlassian-mcp-lite

A small MCP server for Jira and Confluence Cloud. Ten tools, not ninety-eight.
The advertised tool and parameter surface is assembled at startup from
configuration, so a capability you disable is absent rather than filtered.

## Why

Existing Atlassian MCP servers expose large tool surfaces to serve a handful of
real operations. This one exposes five Jira tools and five Confluence tools,
accepts no filesystem paths anywhere, and speaks only stdio.

## Tools

| Tool | Class | What it does |
|---|---|---|
| `jira_search` | read | JQL search, compact default field set |
| `jira_get` | read | One issue, description as markdown |
| `jira_update` | write / destructive | Assignee, fix version, epic, parent; summary and description when destructive is enabled |
| `jira_transition` | destructive | Move to another status by name |
| `jira_comment` | write | Add a comment |
| `confluence_search` | read | CQL search |
| `confluence_get_page` | read | One page, body as markdown |
| `confluence_create_page` | write | Create a page |
| `confluence_update_page` | destructive | Replace a page body |
| `confluence_comment` | write | Add a footer comment |

Tools speak markdown. Atlassian does not accept markdown over REST, so the
server converts to wiki markup on the way in and back to markdown on the way
out.

## Capabilities

Six switches, one per domain and action class:

```
ATLAS_JIRA_READ / _WRITE / _DESTRUCTIVE
ATLAS_CONFLUENCE_READ / _WRITE / _DESTRUCTIVE
```

A disabled tool is never registered with the MCP server, so it is absent from
`tools/list` and unknown to the dispatcher. `jira_update` spans two classes: its
input schema is built at startup, so with `destructive=false` the `summary` and
`description` properties do not exist, and unknown properties are rejected.

## Field selection

Read tools take a `fields` array:

| Value | Meaning |
|---|---|
| omitted | the tool's default set |
| `["summary","status"]` | exactly those, replacing the default |
| `["+description"]` | the default plus `description` |
| `["-updated"]` | the default minus `updated` |

Bare and prefixed forms may not be mixed.

## Running

```bash
mkdir -p ~/.config/atlassian-mcp-lite
cp .env.example ~/.config/atlassian-mcp-lite/env
chmod 600 ~/.config/atlassian-mcp-lite/env
# fill in ATLAS_BASE_URL, ATLAS_EMAIL, ATLAS_TOKEN
docker compose build
```

Then register it with your MCP client using the `docker run` invocation above.

For development, skip the container:

```bash
go build ./cmd/atlassian-mcp-lite
set -a && . ~/.config/atlassian-mcp-lite/env && set +a
./atlassian-mcp-lite
```

## Security notes

- **The API token carries the full authority of its account.** Atlassian applies
  token scopes to OAuth only, not to Basic auth. Use a dedicated service account
  with limited project permissions.
- **Set the write allowlist.** `ATLAS_WRITE_PROJECTS` and `ATLAS_WRITE_SPACES`
  refuse writes to anything not listed, before a request is made. Unset means
  unrestricted.
- **This server does not sandbox your MCP client.** Issue and page text is
  attacker-influencable input that enters the model's context. This server
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
- **Logs go to stderr and never contain credentials.** Successful response
  bodies are not logged at any level; failing ones are, because that is where
  the upstream diagnostics live.

## Development

Every tool runs in a pinned container; nothing but Docker is needed on the host.

```bash
make check    # security, lint, test — what CI runs on a branch
make test
make lint
make image
```

### Behind a corporate proxy

If your network intercepts TLS or your Go modules come from JFrog Artifactory:

```bash
cp compose.override.yaml.example compose.override.yaml
# edit: set the Artifactory URL and the path to your company CA
```

The override is gitignored. It supplies `GOPROXY`, the CA certificate and your
Artifactory credentials to the dev containers, and passes the CA and credentials
to the image build as BuildKit secrets so they never enter a layer. Everything
in it is optional, and a machine outside the office needs no override file at
all — the defaults are the public Go proxy and checksum database.

One caveat worth knowing: Compose auto-loads an override only for the default
`compose.yaml`. The Makefile passes `-f`, which disables that, so it adds the
override explicitly. Invoking `docker compose` by hand without `-f
compose.override.yaml` will silently skip your proxy settings.

Adding a product module: create `internal/<name>`, implement `core.Module`, and
register it in `cmd/atlassian-mcp-lite/main.go`. Capability variables
`ATLAS_<NAME>_READ|WRITE|DESTRUCTIVE` are derived from the domain name, so core
needs no change.

## License

MIT.
````

`CLAUDE.md` — replace the "Repository State" and "Intended Scope" sections with:

```markdown
## Repository State

A working Go MCP server for Jira and Confluence Cloud. Build with `go build
./cmd/atlassian-mcp-lite`, test with `go test ./...`, vet with `go vet ./...`.
The container is built with `docker compose build`.

## Architecture

Four packages:

- `internal/core` — MCP lifecycle, configuration, module registry and gating,
  HTTP client, field selection, logging and masking.
- `internal/markup` — markdown to wiki markup for writes; wiki and rendered
  HTML to markdown for reads.
- `internal/jira`, `internal/confluence` — declare tools; they never import the
  MCP SDK, read the environment, build an HTTP client, or log.

Tools are declared, not registered. `core.Registry.Enabled` drops any tool whose
action class is disabled for its domain, so a disabled tool does not exist at
runtime. `jira_update` builds its input schema from the enabled capabilities.

The design decisions and their reasoning are in `docs/plan/SPEC.md`; the
task-by-task build is `docs/plan/2026-09-01-atlassian-mcp-lite.md`.
```

- [ ] **Step 4: Run the full suite**

Run:
```bash
go vet ./...
go test ./... -v
go build ./...
docker compose build
```
Expected: vet clean, all tests PASS, binary builds, image builds.

- [ ] **Step 5: Three-engine review gate**

Do not commit yet. Dispatch three independent reviews of *this task's diff only*, in parallel, one
per engine. Each gets the same diff and the task's own "Interfaces" block, and a different lens.

```
Agent(model="fable",  prompt=<completeness lens>)   # spec coverage, missing tests, error paths
Agent(model="haiku",  prompt=<consistency lens>)    # signatures, types, imports, helper clashes
Agent(subagent_type="claude", prompt=<relay>)       # wraps mcp__codex__codex: Go and API correctness
```

Give every reviewer this preamble:

> Review the diff below against `docs/plan/SPEC.md` and this task's Interfaces block. Report findings
> only; do not rewrite. For each: SEVERITY (blocking | important | minor), WHERE (file:line), WHAT
> (one or two sentences), FIX (the concrete change). No praise, no summary. If you find nothing real,
> say so rather than padding.

Then the lens, one per engine:

- **fable** — completeness: does this task implement every spec requirement it claims? What
  behaviour has no test? What happens on a 4xx, a 5xx, a cancelled context, an empty result?
- **haiku** — consistency: do the signatures, types, field names and imports match what earlier
  tasks defined and later tasks will call? Any helper defined twice in one package? Any import
  declared but unused, which fails to compile in Go?
- **codex** — correctness: will this compile? Is the SDK, goldmark, `x/net/html` and Atlassian REST
  usage right? Does each test actually test what it claims, or would it pass regardless of the
  implementation?

Then:

1. Triage every finding. Blocking and important ones get fixed before the commit. Minor ones are
   fixed or recorded with a reason.
2. **Verify any claim about a library's behaviour against that library's source** before acting on
   it. Review 01 found a blocking defect that only source-reading could confirm, and one false
   positive that only source-reading could refute. A reviewer's confident claim is a lead, not a
   fact.
3. Re-run `go test ./...` for the packages this task touches.
4. Append the findings and their dispositions to `docs/plan/REVIEW-LOG.md`, in the same table shape
   as `REVIEW-01-findings.md`, noting which engine found what and where they agreed. Agreement
   across engines is the strongest signal available.

The gate is the review, never a rubber stamp: "three reviews ran and found nothing blocking" is a
complete and valid outcome, recorded in one line.

- [ ] **Step 6: Commit**

```bash
git add Dockerfile compose.yaml .env.example README.md CLAUDE.md internal/core/env_example_test.go
git commit -m "feat: packaging, configuration surface and documentation"
```

---

## Self-review notes

Revised after Review 01. The original version of this section claimed every spec section mapped to a
task and named only two untested requirements. **That was wrong**, and the record is in
`REVIEW-01-findings.md`: five spec requirements had no implementing step, and one spec claim
(egress restriction) was never delivered by the packaging at all. Treat the paragraph below as a
current statement of known gaps, not a clean bill of health.

### Spec coverage

Every spec section now maps to a task: architecture and gating to Tasks 5–6; the ten tools to Tasks
9–13; field selection to Task 4 and used by Tasks 9 and 12; body formats and the markdown subset to
Tasks 7–8; the write allowlist to Tasks 1, 10, 11 and 13; logging, masking and secret redaction to
Tasks 2–3; error handling to Task 3; configuration and URL validation to Tasks 1 and 14; paging to
Tasks 9 and 12; internal lookups to Task 10.

### Requirements with no automated test

Enforced by review only. A reviewer should reject a change that violates either.

1. *A module must not import the MCP SDK, read the environment, build an HTTP client, or log.* A
   test walking the import graphs of `internal/jira` and `internal/confluence` would automate this
   in about twenty lines. Worth adding if a third module is ever written.
2. *No code opens a local file other than config.* The only `os.ReadFile` in the codebase is the
   `.env.example` drift test in Task 14.

### Known scope limits, deliberate

- **Team-managed Jira projects.** `epic` always writes `ATLAS_EPIC_FIELD_ID`, correct for classic
  projects. Team-managed projects have no Epic Link field and use `parent`. Detecting the style
  would mean an `editmeta` round trip per update; the trade was judged not worth it, and Jira
  returns a clear 400 naming the field, which the error path surfaces verbatim.
- **Egress is not restricted.** See the spec's Residual risk section. SSRF is prevented by the base
  URL being a validated config constant that tool input cannot reach, not by network policy.
- **No paging.** `limit` is capped and truncation is reported using each API's own completion signal.
- **Confluence field selection filters client-side**, because the v2 page endpoint has no field
  parameter. The saving is in context, not bandwidth.

### Deliberate ordering choice

Tasks 9 and 12 leave temporary stubs so each package compiles and its own tests run in isolation;
Tasks 10, 11 and 13 delete them. `go build ./...` does not succeed until Task 13, because `main.go`
from Task 6 references both modules' real constructors. Run per-package tests until then.

### Known adjustment points

Places where the plan states an intent that a library may express differently. In each case the
expected output is what Atlassian or the MCP spec requires — adjust the call, not the expectation.
Report any adjustment in the task's review-gate log.

- goldmark's table extension node types, list-item wrapping, and `ast.RawHTML.Segments` (Task 7).
- `mcp.NewInMemoryTransports`, and the exact generic parameters `mcp.AddTool` infers (Tasks 5–6).
- `jsonschema.Schema.Resolve` / `Resolved.Validate` signatures used by the strengthened
  `ObjectSchema` test (Task 5).
- Jira enhanced-search response fields `isLast` and `nextPageToken`, and the Confluence v1 search
  `totalSize` and `_links.next` (Tasks 9 and 12). If a field is absent, the count fallback applies.

### One asymmetry to keep in mind

Jira reads and writes both use **v2**, so descriptions and comments are wiki markup rather than ADF.
Confluence writes use **v2** with `representation: "wiki"`; Confluence reads use **v2** with
`body-format=view`; Confluence search uses **v1**, because the v2 API has no CQL path. Every one of
these version choices is deliberate and justified in the spec. Do not consolidate them.
