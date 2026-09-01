# Review log

One section per task, appended at that task's Step 5 review gate. Same shape as
`REVIEW-01-findings.md`: severity, where, what, disposition, and which engine found it.

Record agreement explicitly — a defect two or three engines found independently is the strongest
signal this process produces, and it is worth knowing which lens caught what.

A task whose review found nothing blocking gets one line saying so. That is a valid outcome; a
finding invented to fill the table is worse than an empty section.

## Review 01 — the plan itself, before any code

See `REVIEW-01-findings.md`. 39 raw findings, 27 after deduplication, 5 blocking.

---

<!-- Task sections go below, newest last. -->
## Task 1 — module bootstrap and configuration

Three engines, three lenses, run 2026-09-01 against the Task 1 diff (`go.mod`,
`internal/core/config.go`, `internal/core/config_test.go`).

| Engine | Lens | Raw findings |
|---|---|---|
| Fable | completeness against spec, untested behaviour, hostile config | 7 |
| Haiku | mechanical consistency: signatures, types, imports, helpers | 3 |
| Codex | Go correctness, `net/url` semantics, test validity | 2 |

No engine found a blocking defect in the implementation. The one finding filed as blocking is
disputed on CI grounds, below.

### Important

| # | Finding | Found by | Disposition |
|---|---|---|---|
| T1-I1 | `validateBaseURL` accepts a trailing bare `?` or `#`. `url.Parse("https://h?")` leaves `RawQuery` empty and sets `ForceQuery`; `url.Parse("https://h#")` leaves `Fragment` empty and records nothing at all, so the guard on `RawQuery`/`Fragment` misses both. The retained delimiter would turn a later appended path into a query or a fragment. | Codex | **Fixed.** Verified with a `net/url` probe under go 1.27: `?` → `ForceQuery=true, RawQuery=""`; `#` → `Fragment=""`, no flag. `u.ForceQuery` is now rejected, and the bare `#` is caught by a raw-string check before parsing, since no parsed field carries it. Cases `"bare query"`, `"bare frag"`, `"fragment"` added to `TestLoadRejectsUnsafeBaseURLs`. |
| T1-I2 | A non-empty allowlist that yields no keys fails open: `ATLAS_WRITE_PROJECTS=","` is configuration expressing intent to restrict, but `splitList` returns nothing and `AllowProject` then allows everything. SPEC's write-allowlist section defines three states, and "non-empty → strict" is one of them. | Fable | **Fixed.** `Load` rejects a value that is non-empty after trimming but produces no keys, for both `ATLAS_WRITE_PROJECTS` and `ATLAS_WRITE_SPACES`. A whitespace-only value stays equivalent to unset, which keeps the spec's two unrestricted states intact. Test `TestAllowlistWithNoUsableKeysIsRejected` pins both halves. |
| T1-I3 | `AllowSpace` and `ATLAS_WRITE_SPACES` — both named in the Interfaces block — had no test at all. A crossed assignment reading `ATLAS_WRITE_PROJECTS` into `WriteSpaces` would have passed the whole suite. | Fable | **Fixed.** `TestSpaceAllowlist` mirrors the project tests; `TestProjectAndSpaceAllowlistsAreIndependent` sets both to different values and asserts neither leaks into the other. |
| T1-I4 | `ATLAS_LOG` is a closed enum in SPEC (`info`, `debug`), but `Load` stored any value verbatim, pushing an undefined level into whichever later task consumes it. | Fable | **Fixed.** The value is lowercased and validated at load; anything outside the enum is an error. `TestLogLevel` covers the default, both members, case and whitespace normalisation, and rejection. |
| T1-I5 | `TestLoadRequiresBaseURLEmailToken` omits email and token together, so it passes even if `Load` checks only one of the three. | Codex | **Fixed.** `TestLoadRequiresEachCredentialIndependently` omits exactly one of the three per case, and also asserts a whitespace-only value is rejected — trimming means whitespace must be equivalent to absent. |

### Minor

| # | Finding | Found by | Disposition |
|---|---|---|---|
| T1-M1 | The limit machinery had no test: no custom values, no `LimitDefault > LimitMax` clamp, no strict-parse fallback. | Fable | **Fixed.** `TestLimitParsingAndClamp` covers custom values, `"20x"`, `"0"`, `"-5"`, `"abc"`, whitespace, and `DEFAULT=40, MAX=30 → 30/30`. |
| T1-M2 | The capability-flag synonyms (`1`, `yes`, `on`, case and whitespace variants) and the garbage-means-false behaviour were pinned by nothing. | Fable | **Fixed.** `TestCapabilityFlagSynonyms` covers the accepted set and asserts a typo such as `ture` disables the capability rather than enabling it. |
| T1-M3 | `intOrDefault` uses `strconv.Atoi` where the plan's template used `fmt.Sscanf`. | Fable, Haiku | **Kept, deliberate.** `Sscanf("%d")` parses `"20x"` as 20; a malformed limit is a misconfiguration, not a 20. `TestLimitParsingAndClamp` pins the strict behaviour, which is what T1-M1 said was missing. |
| T1-M4 | Loop variable named `required` where the plan template said `missing`. | Haiku | **Kept.** The loop asserts a requirement; the name says so. No functional difference. |
| T1-M5 | `Caps.Any()` is defined but unused in Task 1. | Fable (as a non-finding) | **Kept and tested.** It is part of the plan's own Task 1 code and Task 5 consumes it; `TestCapsAny` stops it rotting in the meantime. |

### Disputed

| # | Finding | Found by | Disposition |
|---|---|---|---|
| T1-D1 | Filed as **blocking**: Step 3's `go get github.com/modelcontextprotocol/go-sdk@v1.7.0` was not run, so there is no `go.sum` and no SDK require. | Haiku (Fable filed the same observation as minor, with the same reasoning as here) | **Not fixed; the plan step is wrong for this task.** Nothing in Task 1 imports the SDK, and `go mod tidy` removes a require with no importer — so `make tidy` (`go mod tidy -diff`) and the CI lint chain would fail on the very commit the finding asks for. The dependency is added by the first task that imports it (Task 6, `cmd/atlassian-mcp-lite/main.go`), which is also where its `go.sum` entries become verifiable. Task 1 commits `go.mod` alone. |

### Findings outside this task's Files list

Reported, not changed, per the plan's rule 4. Both break `make check` today, independently of any
code in this task:

| Where | What |
|---|---|
| `compose.dev.yaml` `gosec` service | The image tag `securego/gosec:2.29.0` does not exist. The newest tag published on Docker Hub is `2.24.6`, and the repository does not use a `v` prefix. `make gosec`, `make security` and `make check` fail at image pull. |
| `compose.dev.yaml` `test` service | `command` passes `-race`, but `x-go` sets `CGO_ENABLED: "0"`, and `-race` requires cgo. Setting `CGO_ENABLED=1` then fails differently: `golang:1.27-alpine` ships no C compiler (`cgo: C compiler "gcc" not found`). `make test` cannot pass as configured — it needs `CGO_ENABLED=1` *and* `build-base` in the image, or the image switched to the Debian-based `golang:1.27`. |

### Verification after fixes

`go test ./internal/core/` — 16 tests, all pass; `go tool cover -func` reports 98.6% of statements
in the package. `go vet ./...` clean. `golangci-lint run` — 0 issues. `go mod tidy -diff` clean.
`-race` was not run, for the `compose.dev.yaml` reason above.

Serena's Go language server could not start in this session (`gopls` absent from its `PATH`), so
`get_diagnostics_for_file` was unavailable and the edits used plain text tools. The container
`vet` + `golangci-lint` + test runs stand in for it.

### Post-review hardening — validation of every provided value (2026-09-01)

Requested after the gate: validate what the operator supplies rather than accepting it and failing
at first use. `Load` is the trust boundary, so every value is checked there, against an allowlist
where the value later reaches a URL path, a query string or a JSON field name.

| Value | Rule | Rationale |
|---|---|---|
| `ATLAS_EMAIL` | single bare mailbox via `net/mail.ParseAddress`, no display name, no colon, no control characters | It is the user half of the Basic credential; a colon is the separator and CR/LF would corrupt the header. |
| `ATLAS_TOKEN` | no control characters, no whitespace; errors never quote the value | A malformed token fails at load instead of producing an unexplained 401, and the diagnostic must not become the leak the masking rule exists to prevent. |
| `ATLAS_<DOMAIN>_READ/WRITE/DESTRUCTIVE` | `true/1/yes/on`, `false/0/no/off`, or unset; anything else is an error | `ture` previously read as false, silently disabling a capability the operator believes is on. Unset still means off. |
| `ATLAS_LIMIT_DEFAULT`, `ATLAS_LIMIT_MAX` | unset → 20/50; set → positive integer, max ≤ 1000, default ≤ max | `"20x"` silently becoming 20 hides a typo in a bound on response size. A default above the max is a contradiction, so it is an error rather than a silent clamp. |
| `ATLAS_EPIC_FIELD_ID` | `^[A-Za-z][A-Za-z0-9_]*$` | It becomes a JSON object key and a query-string value. |
| `ATLAS_WRITE_PROJECTS`, `ATLAS_WRITE_SPACES` keys | `^~?[A-Za-z0-9_]+$` | Keys reach URL paths and JQL/CQL. The tilde admits Confluence personal spaces. An entry that could never match a real key would also fail closed silently. |
| `domains` argument | `^[a-z][a-z0-9_]*$`, no duplicates | Each becomes part of an environment variable name, and a duplicate would silently overwrite the first registration. |

Two behaviour changes from the plan's Task 1 template, both from silent fallback to a load error:
the capability flags (`isTrue` → `parseBool`) and the limits (`intOrDefault` → `positiveInt`, plus
the removal of the `LimitDefault > LimitMax` clamp). The `Config` shape, `Load` signature and the
`AllowProject`/`AllowSpace` methods are unchanged, so the Interfaces block still holds; the plan's
later tasks read `cfg.LimitDefault`/`cfg.LimitMax` and never call the two removed helpers.

Verification: 21 tests pass, 97.0% of statements covered, `go vet` clean, `golangci-lint` 0 issues,
`go mod tidy -diff` clean.

---

## Task 2 — logging and credential masking

Files: `internal/core/log.go`, `internal/core/log_test.go`. 16 raw findings across three engines.
Two blocking, both real, both verified before acting.

### Blocking

| # | Finding | Found by | Disposition |
|---|---|---|---|
| B1 | `Mask` returns its input **unchanged** when the value already has the shape of its own mask. `Mask("abcd*efgh") == "abcd*efgh"`, so a secret of that shape is logged verbatim. | Codex | **Fixed and verified.** Reproduced before acting. `Mask` now falls back to a fully opaque value when the masked form equals the input. Test: `TestMaskNeverReturnsItsInputUnchanged`. |
| B2 | `NewLogger`/`MaskHeaders` have no production call site, so nothing is masked in the running server. | Codex | **Not a defect of this task.** Task 6 wires the server and Task 3 the client; there is no `main` yet. Recorded so it is not lost — if Task 6 lands without passing `cfg.Token` and `BasicCredential(...)` to `NewLogger`, that is a blocking finding at *that* gate. |

### Important

| # | Finding | Found by | Disposition |
|---|---|---|---|
| I1 | `Mask` slices bytes, so multi-byte UTF-8 is split into invalid text and a short non-ASCII value is treated as long enough to reveal its ends. | Fable **and** Codex | **Fixed.** Operates on runes throughout, including the redaction threshold. Test: `TestMaskHandlesMultiByteRunes`. |
| I2 | Redaction used sequential `strings.ReplaceAll`, so order decided the outcome: a secret that is a prefix of another destroys the longer match and leaves its tail visible. Replaced text was also rescanned. | Fable **and** Codex | **Fixed.** Secrets are sorted longest-first and applied in a single left-to-right pass that never rescans a replacement. Tests: `TestLoggerRedactsLongestSecretFirst`, `TestLoggerDoesNotRedactItsOwnReplacements`. |
| I3 | `minRedactableSecret` (8) and `Mask` interacted badly: a 9-character secret was redacted to a form showing 8 of its 9 characters. | Fable | **Fixed.** `Mask` now reveals ends only when the value is at least `maskKeep*revealRatio` (16) characters. Test: `TestMaskRevealsEndsOnlyWhenEnoughIsHidden` covers 8, 9, 12, 15, 16. |
| I4 | Only the raw token could be redacted, but the client authenticates with `base64(email:token)`. An upstream echo of the encoded form would pass through untouched — precisely the leak redaction exists to stop. | Fable | **Fixed.** Added `BasicCredential(email, token)`; the wiring passes both it and the token to `NewLogger`. Test: `TestBasicCredentialIsRedactable`. |
| I5 | The Authorization test's leak sentinel `c2VjcmV0dmFsdWU` does not occur in the credential under test, so the test passed against a completely unchanged header. | Codex | **Fixed and verified.** Confirmed the sentinel is absent from the encoding. The test now asserts the whole credential is gone and that the result equals the exact expected form. |
| I6 | The concurrency test proves nothing without `-race`: removing the mutex can still yield 100 well-formed lines depending on scheduling. | Codex | **Fixed.** Replaced `bytes.Buffer` with a writer that counts overlapping `Write` calls and yields inside the window, so the test fails on an unsynchronised logger regardless of `-race`. |
| I7 | `Errorf(body)` with a `%` in an upstream JSON body corrupts the diagnostics the message exists to carry. | Fable | **Fixed.** Added non-formatting `Error`/`Debug`. Test: `TestPlainVariantsDoNotInterpretFormatVerbs`. |

### Minor

| # | Finding | Found by | Disposition |
|---|---|---|---|
| M1 | Nil writer panics on first write. | Fable | **Fixed.** Falls back to `io.Discard`. |
| M2 | `MaskHeaders(nil)` and a non-canonical map key (`h["authorization"]`) were untested. | Fable **and** Codex | **Fixed.** Both covered by `TestMaskHeadersNilAndNonCanonicalKeys`. |
| M3 | The `Logger` doc comment claimed stdout "must never be written to" as though enforced, when the constructor accepts any writer. | Fable | **Fixed.** Reworded to place the obligation on the caller. |

Haiku's consistency pass returned **no findings**: no duplicate identifiers across the package, no
unused imports, signatures matching the declared interface, and no helper collisions with
`config_test.go`.

### What the three engines each caught alone

Codex found both blockers, and both were invisible to inspection of the plan or of the code's
intent — one needed an actual evaluation of `Mask` on a crafted input, the other needed checking
whether a base64 sentinel really occurs in a base64 string. Fable found the whole class of gaps
around what the *next* task will need (the Basic credential, the format-verb hazard) and the
threshold interaction. Haiku confirmed the package still coheres. Each lens paid for itself.

Result: 98.1% statement coverage, `go vet` clean, `golangci-lint` 0 issues, `gosec` 0 issues over
605 lines, Serena diagnostics clean.

### Closing the two Task 1 findings filed outside its Files list

Both are now fixed, in the session that implemented Task 2:

- **`securego/gosec:2.29.0` does not exist.** Confirmed: Docker Hub's newest tag is `2.24.6`, from
  February, against a v2.29.0 release in August — the image lags the project by half a year. Rather
  than pinning to a stale image, `gosec` is now installed with `go install` inside the Go image, in
  both `compose.dev.yaml` and CI. It therefore shares the module cache and the TLS trust
  configuration of every other Go tool.
- **`-race` versus `CGO_ENABLED=0` on Alpine.** Fixed by taking the reviewer's second option: the
  dev images are now `golang:1.27-trixie` and `golangci/golangci-lint:v2.13.2`. `-race` is back as
  the default for `make test`, with `CGO_ENABLED=1` scoped to that service alone so every build path
  stays CGO-free. The interim `make test-race` target and its second image are gone.

The reviewer was right to report rather than fix these: both were outside Task 1's Files list, and
both were real. Reporting them cost one line each and saved rediscovering them.

### A third bug, found by running the tools rather than reading them

`make security` stopped at the first failing scan, so `gosec` never ran at all — make aborts a
prerequisite chain on the first non-zero exit. The target now runs every scan and fails at the end.
A security sweep that hides its remaining findings is worse than a slow one.

This one was invisible to all three reviewers because it only appears when the suite is executed.

## Task 3 — HTTP client

Files: `internal/core/client.go`, `internal/core/client_test.go`.

Haiku's consistency pass returned **no blocking or important findings**, and confirmed two things
worth having on record: no duplicate identifiers across the six package files, and the `Do` signature
matches every call site the plan's Tasks 9–13 make. Its three minor notes were deliberate
improvements on the plan's sketch (helper named `newTestClient`, returning two values instead of
three because `t.Cleanup` owns the server, and a four-argument `NewLogger` that redacts the encoded
credential as well as the raw token).

### Important

| # | Finding | Found by | Disposition |
|---|---|---|---|
| I1 | `Do` built its target as `c.base + path` with no check on `path`. **A path with no leading slash changes the host**: `"https://x.atlassian.net" + "rest/api"` parses with host `x.atlassian.netrest`. The SSRF argument therefore rested entirely on caller discipline in Tasks 9–13, and one missing character defeated it silently. | Fable | **Fixed and verified.** Reproduced with `urlparse` before acting. `Do` now rejects any path not matching `^/[^?#]*$` before touching the network, which also closes the `?`-in-path collision with the `query` argument. Tests: `TestDoRejectsPathsThatCouldChangeTheDestination`, `TestDoAcceptsOrdinaryPaths`. |
| I2 | With `out == nil` an oversized 2xx body was silently truncated and reported as success, while the same body with `out != nil` was an error — so a response succeeded or failed depending on the caller's interest in it. The "drain so the connection can be reused" comment was also false, since bytes past the cap stayed unread. | Fable | **Fixed.** The cap is applied on both paths. Test: `TestOversizedResponseFailsRegardlessOfOut`. |
| I3 | The 8 KiB error-body cap had no test, though it is the stated reason the cap exists. | Fable | **Fixed.** `TestErrorBodyIsTruncatedToTheCap` serves a 32 KiB HTML page and asserts both the message and the log stay bounded. |
| I4 | The 30-second timeout was an untestable package constant, and because `http.Client.Timeout` yields a bare `context.DeadlineExceeded`, Tasks 9–13 could not tell this client's timeout from their own caller's deadline expiring. | Fable | **Fixed.** The timeout and the body cap are now `Client` fields defaulted in `NewClient`, and the deadline is applied with `context.WithTimeoutCause` so expiry wraps the exported `ErrRequestTimeout`. Test: `TestClientTimeoutIsDistinguishableFromCallerCancellation`. |

### Minor

| # | Finding | Found by | Disposition |
|---|---|---|---|
| M1 | A comment claimed the code "unwraps" so `errors.Is` reaches `context.Canceled`, when it wraps and `url.Error`'s own `Unwrap` is what preserves the chain. | Fable | **Fixed.** Comment corrected; the mechanism is now stated accurately. |
| M2 | `Retry-After` was parsed only on 429, but Atlassian also sends it with 503. | Fable | **Fixed.** Parsed on any non-2xx. Test: `TestRetryAfterParsedOnServiceUnavailable`. |
| M3 | A 2xx with a non-JSON content type failed as `invalid character '<'`, naming nothing about the network — the realistic cause being an intercepting proxy's login page, which this project has already hit once for real. | Fable | **Fixed, then relaxed.** The first attempt rejected any non-JSON content type outright and broke four passing tests by rejecting valid JSON that `httptest` had served as `text/plain`. That was too strict: the content type now explains a decode *failure* and never rejects a success. Tests: `TestNonJSONBodyNamesTheContentType`, `TestOddContentTypeStillDecodesValidJSON`. |
| M4 | No correlation id captured, so a logged failure could not be matched to an Atlassian server-side trace. | Fable | **Fixed.** `APIError.TraceID` reads the first of four Atlassian trace headers and the error log line carries it. Test: `TestTraceIDIsCapturedAndLogged`. |
| M5 | A 200 with a literal `null` body was an untested silent no-op. | Fable | **Fixed by pinning the behaviour**: `out` is left at its existing value, like the empty-body case. Test: `TestNullBodyLeavesOutAtZeroValue`. |
| M6 | The comment claimed a body exactly at the cap succeeds, but only the over-limit case was tested. | Fable | **Fixed.** Testable now that the cap is a field. Test: `TestResponseExactlyAtTheCapSucceeds`. |
| M7 | Every test used a debug logger, so the spec's "info: failures only" contract was unverified for the client's own emissions. | Fable | **Fixed.** `TestInfoLevelLogsFailuresOnly` asserts silence on success and output on failure. |

### Caught before the gate, without a reviewer

- **`Version` would have collided.** Declared here for the `User-Agent`, and the plan's Task 6
  declared `const Version = "v0.1.0"` in `server.go` — same package, duplicate declaration, a
  guaranteed compile failure the moment Task 6 landed. The plan's Task 6 no longer declares it.
- **An unused function.** `errIsTimeout` was written speculatively and lint flagged it. Deleted
  rather than kept for later. Ironically the need for it reappeared as I4, and the answer was a
  different and better mechanism.
- 20 unchecked writes in test handlers, and one gofmt violation, both from lint.

Result: 97.3% statement coverage, `go vet` clean, `golangci-lint` 0 issues, `gosec` 0 issues over
969 lines, Serena diagnostics clean.

### Codex's pass on the same task

Arrived last and found the leak the other two missed.

| # | Finding | Disposition |
|---|---|---|
| C1 | **The credential could escape through the returned error.** The logger redacted upstream text, but `APIError.Message` kept it raw — and that message travels back to the caller and into an MCP tool result. A body echoing the token would have leaked without ever being logged. `TestCredentialsNeverReachTheLog` ignored the returned error and could not see it. | **Fixed.** `Logger.Redact` is now exported and `apiError` redacts before storing the message. Redaction could not stay the logger's private concern. Test: `TestCredentialsNeverReachTheReturnedError` asserts absence from both the message and `err.Error()`. |
| C2 | `json.Unmarshal` writes into `out` as it goes and can fail partway, leaving the caller a struct that is neither the old value nor the new one — violating the spec's "never a partially populated result". | **Fixed.** `decode` unmarshals into a `reflect.New` scratch value and assigns to `out` only on complete success. |
| C3 | The malformed-JSON test asserted only that an error came back. Its payload failed before assigning anything, so it passed against an implementation that does mutate `out`. | **Fixed.** The fixture is now `{"key":"decoded","other":` — valid field first, then the fault — with `out` prepopulated and asserted unchanged. |
| C4 | `strconv.Atoi` accepted `+30`, which RFC 9110 delay-seconds does not permit, and multiplying an arbitrary integer by `time.Second` overflows to a negative duration. | **Fixed.** Digits-only regex, `ParseInt`, and saturation at 24 hours. A value beyond int64 saturates rather than returning zero: the server is still asking for a long wait, and discarding that is worse than capping it. Test covers `+30`, `30.5`, `1e3`, int64 max and beyond. |
| C5 | `TraceID` was copied from a response header into a log line with no validation or length cap, so a hostile header could add megabytes to one entry and walk past the error-body cap. | **Fixed.** Charset regex and a 200-byte cap; anything else is discarded rather than sanitised. Test: `TestTraceIDIsBoundedAndValidated`. |
| C6 | The error-body read error was discarded, so a truncated response became an `APIError` with partial diagnostics and no indication. | **Fixed.** Read one byte past the cap to detect truncation, and both the read failure and the truncation are named in the message. |
| C7 | The exact-cap test payload was 15 bytes, not 16, so it exercised cap-1 and would not have caught an off-by-one rejection at the boundary. | **Fixed.** The fixture is measured and the test fails if it is not exactly the intended size. |
| C8 | The transport-error test assumed nothing listens on `127.0.0.1:1` — environment-dependent — and did not prove the wrap preserves `*url.Error`. | **Fixed.** Uses a started-then-closed `httptest` server for a deterministic refusal, and asserts `errors.As` reaches `*url.Error`. |
| C9 | The ordering test sampled eight runs and relied on randomised map iteration to expose an unsorted implementation, which it can miss. | **Fixed.** Asserts the one exact lexicographically ordered string, in a single request. |
| C10 | `hits` was written by the handler goroutine and read by the test goroutine with no synchronisation — a real data race, and `-race` is now the default. | **Fixed.** `atomic.Int32`. |
| C11 | The HTTP-date test depended on completing within a 10-second tolerance, so a stalled worker could fail a correct parser. | **Fixed.** One-hour offset with a loose assertion. |

### Two of my own expectations were wrong, and the tests said so

- I expected a `Retry-After` beyond int64 to yield no hint. Saturating is better, per C4.
- I expected a `null` body to be a silent no-op, which stopped being true once C2's scratch-decode
  landed: `null` decodes *successfully* into a zero value, which would then be assigned and wipe the
  caller's field. Resolved by treating `null` like an empty body — "no content" and "the zero value"
  are different answers, and only the caller knows which its own zero value means.

### gitleaks found a real thing, on my own test fixture

Adding the strengthened Authorization test introduced a base64 literal, and `make gitleaks` failed on
it at commit `092331b`. Synthetic value, true positive on entropy. Fixed at the root — the test now
computes it with `BasicCredential` — plus one `.gitleaksignore` fingerprint, because the commit is
already in history and gitleaks scans history. Recorded as a workaround note in the vault, since a
scanner suppression is exactly what that register is for.

Result after all three reviews: 96.6% statement coverage, `go vet` clean, `golangci-lint` 0 issues,
`gosec` 0 issues over 1057 lines, gitleaks and trufflehog clean, `govulncheck` clean, `make check`
green end to end.

## SSRF dial guard and Task 4 (field selection)

Files: `internal/core/ssrf.go`, `ssrf_test.go`, `internal/core/fields.go`, `fields_test.go`.
Reviewed by Codex; 9 findings, 5 important, all fixed.

### Important

| # | Finding | Disposition |
|---|---|---|
| S1 | **The loopback exemption bypassed every address check, not just the loopback one.** `if g.allowLocal \|\| addrIsGloballyRoutable(addr)` meant a base URL of `http://localhost` accepted a name resolving to `169.254.169.254` — the exemption becoming a bypass of the control it sits inside. And `validateBaseURL` permits loopback HTTP, so it was reachable in production, not only in tests. | **Fixed.** A new `permits` method admits loopback and nothing else under the exemption. Test: `TestLoopbackExemptionDoesNotAdmitOtherInternalAddresses`. |
| S2 | **IPv6 zone identifiers walked past every prefix denial.** `netip.Prefix` strips zones, so `Prefix.Contains` returns false for a zoned address. | **Fixed and verified empirically before acting.** Ran the case in `golang:1.27-trixie`: `64:ff9b::a00:1` → `Contains=true`, but `64:ff9b::a00:1%eth0` → `Contains=false`, `IsPrivate=false`, `IsGlobalUnicast=true`, so it would have been **accepted**. Any zoned address is now rejected outright — no globally routable destination needs a zone. `IsPrivate` turned out to be zone-independent, so only the prefix checks were affected. Test: `TestZonedAddressesAreRejectedOutright`. |
| S3 | The prefix list was incomplete: `64:ff9b:1::/48` (RFC 8215 local-use NAT64, which reaches private IPv4) and `0.0.0.0/8` beyond `0.0.0.0` itself were both accepted. | **Fixed.** Added those plus `192.88.99.0/24` (RFC 7526 6to4 relay anycast). Test: `TestTunnelPrefixesAreRejectedUnzoned` covers every tunnel and special-purpose prefix in the list. |
| F1 | **`ResolveFields` did not validate the defaults on the omitted-request path** — the common case — so a malformed default reached the query string unchecked, contradicting the comment two lines above it. | **Fixed.** Defaults are validated once, before branching. Test: `TestResolveFieldsValidatesTheDefaultsOnEveryPath` covers all three paths. |
| F2 | `maxFieldCount` bounded only the deduplicated output, so an arbitrarily long list of duplicates was parsed and grouped first. | **Fixed.** `maxRequestEntries` bounds the raw request before anything is allocated. Test: `TestResolveFieldsBoundsTheRawRequest`. |

### Minor

| # | Finding | Disposition |
|---|---|---|
| F3 | A bare `"+"` or `"-"` was silently discarded, so a request of just `{"+"}` returned the defaults — and my own test enshrined that. | **Fixed, test corrected.** A prefix with no name is malformed rather than empty, and is now rejected. Blank entries are still skipped, which is the difference between noise and a malformed instruction. |
| F4 | The regex admitted any `*name`, though only `*all` and `*navigable` exist. Such a name passes local validation and fails at Atlassian, moving the error away from its cause. | **Fixed.** Star selectors are an explicit allowlist; the pattern no longer admits a leading star. Test: `TestResolveFieldsStarSelectorsAreAnAllowlist`. |
| S4 | No test dialled through `dialContext`, so `resolve` passing would not have caught a re-resolution at dial time; and nothing protected TLS hostname verification. | **Fixed.** `TestDialContextConnectsToTheResolvedAddress` asserts exactly one lookup and connection to that address. `TestTLSVerifiesTheHostnameNotThePinnedAddress` points a URL host of `example.atlassian.net` at a loopback TLS server whose certificate does not cover it, and asserts the certificate error — which can only happen if the hostname, not the pinned IP, is what gets verified. |
| S5 | `TestNewClientInstallsTheGuard` would have passed against an ordinary unguarded transport. | **Fixed, and the fix found something.** The strengthened version changed `allowedHost` mid-test and still succeeded — because the pooled connection was reused and `DialContext` never ran. The guard is a **connection-establishment** control, not a per-request one. That is correct, since the host is fixed at construction, but the test now clears the idle pool so it exercises a real dial. Without that it was asserting nothing. |

### Also of note

`validateFieldName` now measures length in runes rather than bytes, so a short
non-ASCII name is rejected by the pattern rather than by a byte-length limit reporting a misleading
count.

Result: 96.5% statement coverage, `go vet` clean, `golangci-lint` 0 issues, `gosec` 0 issues over
1513 lines, gitleaks and trufflehog clean, `govulncheck` clean.

## Task 5: module registry and capability gating

Files: `internal/core/registry.go`, `registry_test.go`, `go.mod`, `go.sum`.
Reviewed by three engines in parallel — fable (completeness), haiku (consistency), codex via a relay
agent (correctness). 11 findings, 2 of them raised independently by two engines.

### Where the engines agreed

| # | Finding | Engines | Disposition |
|---|---|---|---|
| R1 | `github.com/google/jsonschema-go v0.4.3` was added as a direct `require` while the dependency budget in `SPEC.md` and the plan's Global Constraints named only `go-sdk`, `goldmark` and `x/net`. | haiku (blocking), fable (important) | **Fixed in the docs, not the code.** Verified against the SDK's own module file rather than assumed: `go-sdk` v1.7.0 requires exactly `jsonschema-go v0.4.3`, so the pin is identical and Task 6 will not churn `go.mod`. A package imported directly must be a direct `require` — Go offers no other spelling — so the budget line was the thing that was wrong. Both lines now name it and say why. |
| R2 | The Task 5 Interfaces block declares `ToolDecl{... Action Action ...}` (singular) while SPEC's multi-class gating and the plan's own Step 1 and Step 3 code use `Actions []Action`. | fable, codex | **Fixed.** The Interfaces block is the stale artifact; the code is right. Corrected, because Task 6's author reads that block and not this diff. |

### Fixed in the code

| # | Finding | Engine | Disposition |
|---|---|---|---|
| R3 | **A nil `Handle` survived gating and would detonate at first dispatch**, after the server had already advertised the tool; a nil `Schema` panicked at startup without naming the offender. | fable (important) | **Fixed.** `Register` now rejects a nil `Schema`, a nil `Handle`, an empty tool name and an empty domain, each panic naming domain and tool. These are compile-time wiring mistakes in this repository's own code, so failing loudly at registration is strictly better than degrading at runtime. Tests: `TestRegisterRejectsUnservableDeclarations`, `TestRegisterRejectsEmptyDomain`. |
| R4 | No duplicate detection: two modules could claim one domain (and `Domains()` would derive the config key twice), and two tools could share a name, reaching `mcp.AddTool` as a silent last-wins collision in Task 6. | fable (minor) | **Fixed** in the same `Register` check, and the tool-name check is registry-wide rather than per module, because the MCP dispatch key is global. Tests: `TestRegisterRejectsDuplicateDomain`, `TestRegisterRejectsToolNameTakenByAnotherModule`. |
| R5 | An empty `Actions` slice is correctly denied but untested, so a refactor to "default allow when nothing is declared" would pass the suite. | fable (minor) | **Fixed.** `TestEmptyActionsRegisterNothing`. |
| R6 | `TestSchemaBuiltFromCapsOmitsDestructiveProperty` guarded `off[0]` with a length check but indexed `on[0]` without one, so a regression to zero registered tools would panic instead of failing cleanly. | codex relay (the relay agent's own finding, not codex's) | **Fixed.** Same guard on both paths. |

### Rejected, with reasons

| # | Finding | Engine | Why not |
|---|---|---|---|
| R7 | Clamp the caps passed to `Schema` down to the tool's declared action classes, so a module's `Schema` func cannot advertise a property for a class the tool never declared. | fable (minor) | **Rejected — it contradicts the specified behaviour.** The plan's own `TestSchemaBuiltFromCapsOmitsDestructiveProperty` registers a tool declaring only `ActionWrite` and requires its schema to gain the `description` property when the *domain* has `Destructive: true`. Clamping would make that test fail. The declared classes decide **registration**; the domain's capabilities decide the **schema**. Recorded rather than silently dropped, because the defence-in-depth argument is sound in the abstract and only the plan's test settles it. |
| R8 | The Interfaces block says Task 5 consumes `core.Logger`, but nothing in `registry.go` takes one, and `actionNames` has no production caller. | fable (important) | **No change.** `actionNames` is called in Task 6's `server.go` (`log.Debugf("registered %s (%s/%s)", ...)`), which is where the logger enters. The helper is covered by a test here so the `unused` linter stays quiet in the interval. |
| R9 | Missing `go.sum` entries for the new module; CI's `go mod tidy -diff` will fail. | codex (blocking) | **False.** `go.sum` exists with both `jsonschema-go v0.4.3` and `go-cmp v0.7.0` hashes; codex inferred its absence from the diff, which carried only `go.mod` and the two new files. It withdrew the finding when corrected. `make tidy` is clean. The real residue is procedural: `go.sum` is untracked and must be committed alongside `go.mod`. |
| R10 | Nil `*Registry` receiver; concurrent `Register`/`Enabled`. | fable (out of scope, self-flagged) | **No change,** and agreed: the registry is constructed once in `main.go` and registration is single-threaded startup wiring. A mutex would advertise a concurrency guarantee that does not exist. |

### Verified against library source rather than taken on trust

`AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}` really does reject unknown
properties. `jsonschema-go@v0.4.3/jsonschema/validate.go:500` tests
`schema.AdditionalProperties.Not != nil && reflect.ValueOf(*schema.AdditionalProperties.Not).IsZero()`
and `schema.go:147` builds `falseSchema()` in exactly that shape — so the construct is the library's
own canonical false schema and takes the fast path. `Resolve(&jsonschema.ResolveOptions{})` is
equivalent to `Resolve(nil)` in this version.

Result: 96.9% statement coverage, `go vet` clean, `golangci-lint` 0 issues, `gosec` 0 issues over
1664 lines, gitleaks, trufflehog and `govulncheck` clean, `go mod tidy -diff` clean.

## Task 6: MCP server wiring

Files: `internal/core/server.go`, `server_test.go`, `go.mod`, `go.sum`, `docs/plan/main.go.deferred`.
Reviewed by fable (completeness), haiku (consistency) and codex via a relay agent (correctness).
14 findings; haiku found nothing, and the other two agreed on four things independently.

### Decided before the review: `main.go` moves to Task 13

Task 6 as planned created `cmd/atlassian-mcp-lite/main.go`, which imports `internal/jira` and
`internal/confluence` — packages that do not exist until Tasks 9 and 12. Landing it here was measured,
not guessed: `golangci-lint` reported `typecheck: 2` and `go test ./...` failed to build the test
binary, repo-wide, and would have stayed that way for seven tasks, costing every intervening task the
Step 4 signal that this review gate depends on. Put to the user, who chose to defer. The file is
parked verbatim at `docs/plan/main.go.deferred` and Task 13 now carries it. Its content is unchanged
apart from R2 below.

### Where the engines agreed

| # | Finding | Engines | Disposition |
|---|---|---|---|
| R1 | **The "unmarshalable result is a protocol error" design does not exist on the wire.** go-sdk v1.7.0 converts every handler error that is not a `*jsonrpc.Error` into `CallToolResult{IsError: true}`, so the marshal-failure branch is indistinguishable from an ordinary tool error — and `TestUnmarshalableResultIsAProtocolError` passed only through the `!res.IsError` half of a disjunction, with its tool-name assertion guarded by an `err != nil` branch that never ran. | fable (important), codex (important, both halves) | **Fixed, by correcting the code's belief rather than fighting the SDK.** Verified at `mcp/server.go:383` in the v1.7.0 source. A marshal failure keeps the session usable and stays attributable, which is what was actually wanted; returning a `*jsonrpc.Error` would have bought nothing. Comments corrected, and the test renamed to `TestUnmarshalableResultIsAnAttributedToolError` now asserts the branch that really happens: `IsError`, the tool name in the text, the failure in the log. |
| R2 | **The deferred `main.go` gave the logger only the raw token, not the Base64 Basic credential**, so an upstream body echoing the `Authorization` header would leak a reusable credential. | codex (blocking) | **Fixed.** `log.go:196-204` documents `BasicCredential` with the instruction "Pass this to NewLogger alongside the raw token", so this was a violated contract, not an inference. `main.go.deferred` now passes both. |
| R3 | `mcp.AddTool` panics rather than erroring on a malformed schema, so `NewServer`'s `error` return was structurally dead while its one real failure mode escaped as a panic. | fable (minor), codex (important, plus the relay agent's note that the return is literally unreachable) | **Fixed.** Confirmed at `mcp/server.go:278-299`: a nil schema, a nil `*jsonschema.Schema`, or any root type other than `"object"` panics. `NewServer` now recovers and returns it as the promised error. Test: `TestMalformedSchemaBecomesAnError`. |
| R4 | The comment claiming a protocol error would abort the session is wrong; in this SDK it fails one call. | fable (minor), codex (minor) | **Fixed,** and the premise was worse than the consequence: on the handler path the SDK produces no protocol error at all. |

### Found by one engine, fixed

| # | Finding | Engine | Disposition |
|---|---|---|---|
| R5 | **`err.Error()` reached the MCP client unredacted.** The log copy was masked; the identical text went into `TextContent` untouched, and `Logger.Redact` — exported at `log.go:253` precisely for this — was unused by `server.go`. | fable (important) | **Fixed.** Every client-visible error text now goes through `toolError`, which redacts. Test: `TestToolErrorTextIsRedacted` asserts that neither the raw token nor the Basic credential appears in the result or the log. |
| R6 | **A module panic was unrecovered**, and the SDK does not recover on that path, so one poisoned call would kill the stdio session and every other tool with it. | fable (important) | **Fixed.** The handler wrapper recovers, logs, and returns an error result naming the tool. Test: `TestPanickingHandlerDoesNotKillTheSession` proves the next call still succeeds. |
| R7 | **The comment "the validated bytes pass straight through to the module" is false.** The SDK unmarshals arguments into `map[string]any`, applies defaults and re-marshals, so every JSON number round-trips through `float64`. | relay agent's own finding, not codex's | **Fixed after empirical proof, not source-reading alone.** `mcp/tool.go:75-142` re-marshals whenever the input is an object, which for our schemas is always. Ran it: `9007199254740993` reaches the handler as `9007199254740992`, and key order changes. The comment now states this, with the rule it implies — module schemas carry Atlassian IDs as strings, never as JSON numbers — and `TestArgumentsAreRemarshalledAndLoseIntegerPrecision` fails loudly if the SDK ever starts preserving them. |
| R8 | No test that the context reaching `Handle` is the live request context; nothing pinned the shape of a nil result. | fable (minor) | **Fixed.** `TestHandlerObservesClientCancellation` blocks a handler and cancels client-side, asserting the handler sees `context.Canceled`. `TestNilResultEncodesAsJSONNull` pins `null` rather than leaving it to be changed by accident. |
| R9 | `Serve`'s doc comment named only the disconnect exit, not the `ctx.Err()` one that `main.go`'s `errors.Is(err, context.Canceled)` filter exists for. | fable (minor) | **Fixed.** Both exits documented. |

### Rejected or recorded

| # | Finding | Engine | Why not |
|---|---|---|---|
| R10 | `StdioTransport` decodes each message with an uncapped `json.Decoder`, so an oversized call could exhaust memory before validation. Fix: switch to `mcp.IOTransport`. | codex (important) | **Real, but the fix is wrong and the severity is too high.** The relay agent checked: `IOTransport.Connect` calls the same `newIOConn` and inherits the identical unbounded decoder, and an `io.LimitReader` caps the stream rather than each message. Stdin here is the local MCP client process that launched us, not a network peer, so this is hardening against a compromised local client. Recorded as an accepted risk for Task 14 rather than patched blind. |
| R11 | Bump `golang.org/x/sys`. | nobody — `make security` found it | **Fixed anyway.** `govulncheck` reported GO-2026-5024 in `x/sys@v0.41.0`, pulled indirectly by the SDK and not reachable from our code. Bumped to v0.44.0: an MVS version bump, no new module, and the scan is now completely clean rather than clean-with-a-footnote. |

### Provenance, which nearly went unnoticed

Codex claimed to have read go-sdk v1.7.0. It cannot have: the host module cache holds only v1.6.0.
The relay agent caught this and re-derived every finding from v1.6.0, where they also hold — but the
pinned version had still not been read by anyone. Every claim above was therefore re-verified against
the real v1.7.0 source inside `golang:1.27-trixie`, where the module actually is, and R7 was settled
by running the case rather than reading it. A reviewer's confident citation is a lead, not a fact,
and this time the citation was to the wrong version of the library.

Result: 96.9% statement coverage (`Serve` alone at 0%, untestable without hijacking stdio),
`go vet` clean, `golangci-lint` 0 issues, `gosec` 0 issues over 1816 lines, gitleaks, trufflehog and
`govulncheck` all clean, `go mod tidy -diff` clean.

## Task 7: markdown to wiki markup, via goldmark

Files: `internal/markup/to_wiki.go`, `to_wiki_test.go`, `go.mod` (goldmark v1.8.5).
Reviewed by fable (completeness), haiku (consistency) and codex via a relay agent (correctness).
19 findings. haiku found nothing; fable and codex agreed on six defects independently, which is the
strongest signal this log has recorded so far — and every one of them was a content-loss bug in code
the plan supplied verbatim.

Both reviewers verified their claims by running inputs rather than reading, and so did I before
fixing: all ten reported failures reproduced exactly.

### Content loss — the plan's renderer dropped material silently

| # | Finding | Engines | Disposition |
|---|---|---|---|
| M1 | **An image lost its URL.** `![alt](https://e.com/i.png)` rendered as `alt` — `*ast.Image` was unhandled, so the default branch recursed into the alt text and discarded the destination. | fable (blocking), codex (important) | **Fixed.** Images render as `!url!`, or `!url\|alt=…!` when alt text exists. Test: `TestToWikiImageKeepsTheURL`. |
| M2 | **Any block whose content lives in `Lines()` vanished when nested.** `> ```code``` ` produced `bq. ` and nothing else; a fenced block inside a list item produced `* item`. Such nodes have no inline children, so `writeInline` walked nothing. | fable (blocking), codex (important, both the blockquote and the list-item half) | **Fixed.** Blockquote and list-item children are dispatched by kind: paragraphs inline, everything else through `writeBlock`. A quote containing anything but paragraphs now uses `{quote}`, because `bq. ` covers exactly one line. Tests: `TestToWikiBlockquoteWithBlocksUsesQuoteMacro`, `TestToWikiListItemKeepsNestedCodeBlock`. |
| M3 | **`HTMLBlock.ClosureLine` was dropped**, so `<div>\nx\n</div>` lost its closing tag. goldmark stores the closing line separately from `Lines()`. | fable (important), codex (important) | **Fixed,** and confirmed against `ast/block.go`, where goldmark's own `Text()` appends the closure explicitly. Test: `TestToWikiHTMLBlockKeepsItsClosingTag`. |
| M4 | Two paragraphs in one list item were concatenated — `para1para2` — and a list inside a blockquote flattened to `bq. onetwo`. | fable (important), codex (important, via the tight/loose distinction: goldmark rewrites `Paragraph` to `TextBlock` only for tight lists) | **Fixed.** Successive paragraphs in an item are separated by `\\`, wiki's forced line break. Test: `TestToWikiListItemParagraphsAreSeparated`. |
| M5 | An empty table cell emitted `||`, which wiki reads as a header delimiter, shifting every following cell. | fable (important) | **Fixed.** An empty body cell emits a single space. Test: `TestToWikiEmptyTableCellIsNotAHeaderDelimiter`. |

### Injection — user text could forge wiki structure

| # | Finding | Engines | Disposition |
|---|---|---|---|
| M6 | **No escaping anywhere.** Text containing `{code}` opened a macro that swallows the rest of the issue; `[x\|y]` became a live link; a `\|` in a cell forged a column; a `}}` inside a code span broke out of monospace. The text is user-controlled and arrives from a model. | fable (blocking), codex's relay agent (important; codex itself missed it entirely) | **Fixed.** `escapeWiki` backslash-escapes `\ { } [ ] \| * _ + ^ ~ !` on every text segment. `-` and `?` are deliberately excluded: they only act doubled or paired, the failure is cosmetic rather than structural, and escaping every hyphen in prose is a worse trade. Test: `TestToWikiEscapesStructuralCharacters` plus the table and code-span cases. |
| M7 | A code block whose body contains `{code}` closes the macro early, dragging the rest of the document inside it. | fable (important) | **Fixed.** `safeCodeBody` breaks the inner terminator to `{code\}`; a wiki code macro has no escape sequence, so one visible backslash in a rare body beats losing the document's structure. `FromWiki` undoes it. Test: `TestToWikiCodeBodyCannotCloseTheMacro`. |
| M8 | `Link.Destination` keeps markdown's backslash escapes (goldmark resolves them only in its own HTML renderer), and a `\|` in a URL splits alias from target. | codex (important) | **Fixed.** Destinations are markdown-unescaped, then `\|` and `]` are percent-encoded — which keeps the URL working, unlike a backslash inside a link target. Test: `TestToWikiPercentEncodesPipeInURL`. |
| M9 | An email autolink lost its scheme: `AutoLink.URL()` prepends one only when the source had one, so `<a@b.com>` became `[a@b.com]`, a page link rather than an address. | codex (important) | **Fixed.** Test in `TestToWikiUnsupportedInlinePassesThroughAsText`. |
| M10 | goldmark leaves markdown's own `\*` escapes in the text segment, so naive wiki escaping would double them. | fable (minor) | **Fixed** as part of M6: `unescapeMarkdown` runs before `escapeWiki`. Test: `TestToWikiDoesNotDoubleMarkdownEscapes`. |

### Weak tests, fixed

| # | Finding | Engine | Disposition |
|---|---|---|---|
| M11 | `TestToWikiRawHTMLIsNotDropped` accepted either case's sentinel, so a converter returning the constant `"x"` passed both iterations. | codex (minor) | **Fixed.** Each case asserts its own exact output. |
| M12 | `TestToWikiUnsupportedInlinePassesThroughAsText` claimed to exercise the unsupported-node fallback, but with only the table extension enabled `~~gone~~` never becomes a node — it stays ordinary text. | codex (minor) | **Fixed.** The test now also covers autolinks, which this parser really does build. |

### Rejected or recorded

| # | Finding | Engine | Why not |
|---|---|---|---|
| M13 | Unbounded recursion on deeply nested lists could exhaust the stack. | codex (important) | **Recorded, not fixed.** The relay agent's downgrade is right: nesting depth is bounded by source indentation, so depth is at most input size / 2, and Go grows goroutine stacks to 1 GB. It pairs with the uncapped stdin decode recorded as R10 in Task 6 — both are "a hostile local client sends an enormous document", and both belong to that same Task 14 hardening note. |
| M14 | An ordered list's start number is discarded (`3.` renumbers from 1); a table row wider than its header loses cells. | fable (minor) | **Documented and pinned, not changed.** Wiki markup has no start-offset syntax, and the ragged-row truncation happens in the markdown parser before this package sees it — GitHub renders it the same way. `TestToWikiDocumentedLimitations` locks both in so a future change is deliberate. |
| M15 | A soft line break emits `\n`, so soft-wrapped paragraphs gain breaks Jira will render. | fable (minor) | **No change.** Markdown says a soft break is a space; Jira says a newline is a break. Preserving the author's visible line structure is the more useful of two defensible readings, and nothing is lost either way. |
| M16 | `strings.HasSuffix(b.String(), "\n")` inside the list loop is quadratic. | codex (asked as a leading question by me; codex declined to answer) | **False, and the relay agent said so with the receipt:** `strings.Builder.String()` is `unsafe.String(...)`, O(1) and copy-free, and `HasSuffix` compares one byte. The call was removed anyway — the rewritten list rendering builds each item in its own buffer, which is clearer regardless. |

### Provenance, again

Codex again claimed to have read the pinned version while actually reading the host module cache,
having been told not to. This time the relay agent md5-summed `ast/block.go`, `ast/inline.go` and
`parser/list.go` in both locations and found them byte-identical, so the claims stood. Two reviews
running, two provenance failures: the instruction is not enough on its own, and the verification step
is what makes the review trustworthy.

Result after fixes: 48 tests in `internal/markup`, 90.7% statement coverage, `go vet` clean,
`golangci-lint` 0 issues, `gosec` 0 issues over 2533 lines, gitleaks, trufflehog and `govulncheck`
clean, `go mod tidy -diff` clean.

## Task 8: wiki and HTML to markdown

Files: `internal/markup/from_wiki.go`, `from_html.go`, their tests, `go.mod` (`golang.org/x/net` v0.58.0).
Reviewed by fable (completeness), haiku (consistency) and codex via a relay agent (correctness).
33 findings across the three, 21 of them acted on. Every one I fixed was reproduced first.

This is the heaviest review in the log, and the reason is worth stating plainly: `FromWiki` and
`FromHTML` read whatever Atlassian returns, so their input is other people's page content arriving in
a model's context. Both converters shipped from the plan without escaping anything.

### Blocking — real Atlassian input, corrupted

| # | Finding | Engine | Disposition |
|---|---|---|---|
| W1 | **The parameterised code fence did not match.** `{code:title=Foo.java\|borderStyle=solid}` — the common form in real Jira — fell through as prose, its body was emphasis-converted, and then the *closing* `{code}` matched and opened a fence that swallowed the rest of the document. | fable | **Fixed.** The pattern accepts any parameter tail, and `fenceLanguage` emits a language only when the first parameter actually is one rather than an attribute. Test: `TestFromWikiCodeFenceWithParameters`. |
| W2 | **Mixed list markers were not understood.** Wiki writes a bullet inside an ordered list as `#*`, which `ToWiki` itself emits, and neither the all-`#` nor the all-`*` pattern matched it — so the line passed through as literal text and the round trip was broken for a construct this package produces. | fable, codex | **Fixed.** One pattern for both kinds: the last marker character picks the kind, its length the depth. Test: `TestFromWikiMixedNestedListMarkers`. |
| W3 | **Table rows were split on escaped pipes.** `ToWiki` escapes a pipe in cell text precisely so it is not a boundary; `FromWiki` split on it anyway, forging a column and misaligning the row against its separator. | fable, codex (who also caught the link half) | **Fixed.** `splitCells` honours backslash escapes *and* `[label\|target]` spans, and a literal pipe is re-escaped on output. Tests: `TestFromWikiEscapedPipeStaysInsideItsCell`, `TestFromWikiLinkInsideTableCellIsNotSplit`. |
| W4 | **HTML table cells emitted pipes and newlines verbatim.** A `<br>` or a nested paragraph — routine in Confluence — ended the markdown row, dropping everything after it out of the table. | fable | **Fixed.** `tableCell` escapes unescaped pipes and folds newlines to spaces. Test: `TestFromHTMLTableCellCannotBreakTheRow`. |
| W5 | **`FromHTML` returned raw HTML — script bodies included — whenever the parser errored.** Not theoretical: x/net/html recovers its own >512-open-element panic as an error, so a deeply nested page reaches that branch and hands the model exactly the content `dropContent` exists to remove. | codex (rated important; the relay agent raised it to blocking, correctly) | **Fixed.** The fallback tokenizes for visible text, skipping script, style and noscript, and escapes what survives. Verified with 600 nested `<div>`: the script body is gone, the text remains. Test: `TestFromHTMLParseFailureStillDropsScripts`. |

### Injection and content loss

| # | Finding | Engine | Disposition |
|---|---|---|---|
| W6 | **Untrusted page text went into markdown unescaped**, so prose containing `*not bold*` or a backtick became live markdown — the mirror of the Task 7 gap, on the read path. | fable, codex | **Fixed.** `escapeMarkdown` on every text node outside `pre`/`code`, where escaping would corrupt the code instead. Test: `TestFromHTMLEscapesMarkdownMetacharacters`. |
| W7 | **The `\x00` placeholder scheme could be forged by the input.** Text containing `\x00h0\x00` had a held value substituted into it, swapping someone else's content in. | codex, probe-confirmed by the relay agent | **Fixed.** NUL is stripped before any placeholder exists; it is not text an Atlassian field should carry. Test: `TestFromWikiInputCannotForgeAPlaceholder`. |
| W8 | `<img>` was dropped entirely — it has no children, so the default walk emitted nothing, losing even the alt text. Confluence uses `img` for every attachment and emoticon. | fable, codex | **Fixed:** `![alt](src)`. Test: `TestFromHTMLImageIsNotDropped`. |
| W9 | `<blockquote>` lost its quote semantics, so the model could not tell quoted — often third-party — text from the page's own words. | fable, codex | **Fixed:** per-line `> `. Test: `TestFromHTMLBlockquoteKeepsItsQuoteMarker`. |
| W10 | `{noformat}` bodies were inline-converted, rewriting pasted logs and stack traces. | fable | **Fixed:** same literal handling as `{code}`. Test: `TestFromWikiNoformatBodyIsLiteral`. |
| W11 | `{quote}` — which `ToWiki` emits for any quote holding more than paragraphs — was left as literal text, breaking the round trip. | fable, codex | **Fixed.** Test: `TestFromWikiQuoteBlock`. |
| W12 | A `<li>` with two blocks leaked its later blocks out of the list, splitting it. Confluence wraps `li` content in `p`, so this is the normal case, not an edge one. | fable | **Fixed:** continuation indent. Test: `TestFromHTMLMultiBlockListItemStaysInTheList`. |
| W13 | **Emphasis with inner whitespace did not render.** `<strong>bold </strong>` emitted `**bold **`, and CommonMark does not close emphasis on a marker preceded by space — so the reader sees asterisks. Source indentation also leaked through as literal leading spaces, which markdown reads as an indented code block. | codex, probe-confirmed | **Fixed.** Whitespace is collapsed as a browser would, and `writeEmphasis` moves spaces outside the markers. Tests: `TestFromHTMLEmphasisSpacingRenders`, `TestFromHTMLCollapsesSourceWhitespace`. |
| W14 | A `<pre>` body containing a fence closed the block early; the Confluence code macro's language was dropped. | fable | **Fixed.** The fence is longer than the longest backtick run inside it, and the language is read from `data-syntaxhighlighter-params` or a `language-` class. Tests: `TestFromHTMLPreBodyCannotCloseTheFence`, `TestFromHTMLCodeLanguageIsKept`. |
| W15 | `colspan` was ignored, shifting later cells under the wrong heading; definition lists fused term and definition; link targets with spaces or parentheses broke. | fable | **All fixed,** with a test each. |

### Rejected or recorded

| # | Finding | Engine | Why not |
|---|---|---|---|
| W16 | An unclosed `{code}` is silently closed at EOF, which contradicts the doc comment's pass-through promise. | codex | **Kept, comment left as the thing to reconcile later.** Jira renders an unterminated `{code}` by treating the remainder as code; emitting the closing fence produces well-formed markdown with the same meaning. Leaking an unterminated fence would be worse for the reader. |
| W17 | The placeholder restore was quadratic. | codex | **Fixed anyway**, though the sizes here never made it matter: one indexed pass, repeated only while nesting remains. |
| W18 | `{panel}` and `{color}` pass through as literal noise. | fable (minor, self-flagged as spec-compliant) | **No change.** Pass-through is the stated contract, and unwrapping them is a product decision rather than a defect. |
| W19 | Round-trip and HTML tests use substring assertions where exact output would be stronger. | codex | **Partly done.** The new tests assert exact output where the shape is fixed; the older substring ones were left, since rewriting passing assertions is not what this gate is for. |

### The process failure worth recording

I built codex's review bundle as a **snapshot of the files before the fix pass**, then applied
fable's fixes while codex was still reading. Seven of its fifteen findings were already fixed on
disk. The relay agent caught it by comparing line counts — 392 on disk against 195 in the snapshot —
and re-checked every finding against the live files rather than reporting the stale set. Without that
step I would have "fixed" seven things twice and trusted a review of code that no longer existed.
The rule this earns: a review bundle is only valid until the first fix lands, so either freeze the
tree until every reviewer returns, or hand each reviewer a commit-ish snapshot and re-run the ones
whose ground moved.

Codex also misjudged its one genuinely blocking finding as important, and the relay agent's own
probe is what raised it. Three reviews now, three times the relay layer added value beyond the
model's own output.

Result: 68 tests in `internal/markup`, 92.7% statement coverage, `go vet` clean, `golangci-lint`
0 issues, `gosec` 0 issues over 2945 lines, gitleaks, trufflehog and `govulncheck` clean,
`go mod tidy -diff` clean.

## Task 12: Confluence read tools

Files: `internal/confluence/{module.go,read.go,read_test.go}`.
Built by a delegated agent in parallel with Task 9, then reviewed by fable (completeness), haiku
(consistency) and codex via a relay agent (correctness). 13 findings; 11 acted on.

The implementing agent returned its own critique alongside the code, and six of its seven leads were
confirmed by the reviewers — worth noting because it means the cheapest review in this project is
asking the builder what it is unsure about.

### Fixed

| # | Finding | Engine | Disposition |
|---|---|---|---|
| C1 | **A non-content search result emitted an empty `id` and `type`.** CQL matches spaces, users and attachments, and those results carry no `content` object, so a `type=space` search produced `{"id":"","title":"A Space","type":""}` — an id the model would then hand back to `confluence_get_page`. | fable (important, probed) | **Fixed.** Empty keys are omitted entirely and `entityType` says what the row actually is. Test: `TestSearchNonContentResultOmitsEmptyKeys`. |
| C2 | **An unknown field name was silently dropped.** `fields: ["space"]` — the natural typo for `spaceId` — returned a short result with no hint, because `ResolveFields` validates the grammar and the local filter discarded anything it did not recognise. Jira gets away without this because its field list goes to the API, which errors; this endpoint takes no field parameter. | fable (important, probed) | **Fixed.** `knownFields` rejects the name and lists the valid set. Test: `TestGetPageRejectsUnknownField`. |
| C3 | **Search titles carried highlight markers.** The v1 endpoint wraps the top-level `title` in `@@@hl@@@…@@@endhl@@@` whenever the CQL has a text match, and the clean title lives at `content.title`, which the decode struct omitted. | fable (important) | **Fixed.** `content.title` is preferred, with the markers stripped from the fallback. Test: `TestSearchTitlePrefersContentAndStripsHighlights`. |
| C4 | **Truncation signalling was wrong in both directions.** With `_links.next` set and no `totalSize`, the message read "1 of 1 matches returned" — claiming everything arrived while flagging truncation. With neither signal and a full page, nothing was reported at all. | codex (important) and fable, both probed | **Fixed, on the relay agent's reasoning rather than codex's.** Codex justified it from the plan, which actually says the opposite; the relay agent found the better argument in the repo — the sibling Jira module already decodes `IsLast *bool` precisely to tell absent from false, and falls back to counting. Confluence now uses `TotalSize *int` and the same three-signal ladder, and declines to state a total it was never given. Tests: `TestSearchTruncationSignals` (four shapes), `TestSearchFullPageWithoutSignalsIsReportedTruncated`. |
| C5 | **A missing or null `version` silently became 0** — exactly the value that would then be handed to `confluence_update_page`'s optimistic lock. | codex (important, premise unproven, probe confirmed the behaviour) | **Fixed, and the defensive half taken regardless of the premise.** The relay agent could not confirm codex's claim that `include-version` defaults to false, and said so rather than asserting it. But the probe showed both `version:null` and an absent version yield `0` with no error, so `Version` is now a pointer, a missing or non-positive version is an error, and `include-version=true` is sent explicitly instead of assumed. Tests: `TestGetPageRequiresAUsableVersion`, `TestGetPageRequestsVersionExplicitly`. |
| C6 | No test covered a non-2xx from either endpoint, so `*core.APIError` propagation through the handlers — the most common runtime path there is — was unexercised. Nor was the default limit, nor the truncation message. | fable (important), and the builder's own lead | **Fixed.** `TestUpstreamFailurePropagatesAsAPIError` asserts `errors.As` and the status for both tools; `TestSearchOmittedLimitSendsDefault` pins the default. |
| C7 | The three rejection tests asserted only `err != nil` plus a `t.Error` in the handler, so any pre-request failure passed them — a broken `validPageID` with a JSON error instead would have been green. | fable (minor), builder's lead | **Fixed** for the new tests, which assert the error text. |
| C8 | `Size` and `Status` were decoded and never used. | haiku, codex's relay agent (codex itself missed both) | **Fixed.** `Size` is gone; `Status` is now emitted when present, since it distinguishes a draft from a current page. |
| C9 | The CQL string was unbounded — the only user-controlled string in the repository with no cap. | (mirrors fable's Jira finding) | **Fixed.** `maxCQLLen` of 4096, checked before the network. Test: `TestSearchBoundsCQLLength`. |

### Recorded, not changed

| # | Finding | Engine | Why |
|---|---|---|---|
| C10 | `version` is forced into the output even for `fields:["title"]` or `["-version"]`, which deviates from SPEC's "bare names replace the default set entirely". | fable (minor) | **Kept.** The update path needs the version for its optimistic lock, and a caller who removed it would otherwise have to make a second call to write safely. This is a deliberate deviation, and it is stated in the code where the forcing happens. |
| C11 | `internal/core/client.go`'s `upstreamMessage` decodes Jira's `map[string]string` error shape, so Confluence v2's `{"errors":[{...}]}` array reaches the model as a raw JSON blob with the useful `title` buried. | fable (minor, outside Task 12's files) | **Real, deferred to a core change** rather than patched from a module review. Recorded here so it is not lost. |

### A false blocking finding I caused myself

Codex's only blocking finding was that the tool calls the wrong endpoint — and it was wrong, because
**my reviewer prompt asserted the endpoint as a premise** ("v1 /rest/api/content/search with CQL").
Codex adopted the premise instead of checking the code. `SPEC.md:75` and the plan both specify
`/wiki/rest/api/search`, and the plan states why: the v2 API has no CQL search path. The relay agent
caught it, traced it to my prompt, and refuted it with both sources.

The rule this earns, alongside the freeze rule from Task 8: **a reviewer prompt states what to check,
never what is true.** A confident premise in the prompt comes back as a confident finding.

The relay agent also noted that the flagged truncation code is byte-identical to the plan's own
snippet, so the plan needs the same fix or Task 13 will reintroduce it.

Result: 44 tests in `internal/confluence`, 90.5% statement coverage (from 82.9%), `go vet` clean,
`golangci-lint` clean for this package.

## Task 9: Jira module scaffold and read tools

Files: `internal/jira/{module.go,read.go,read_test.go}`, plus one fix in `internal/core/client.go`.
Built by a delegated agent in parallel with Task 12, then reviewed by fable (completeness), haiku
(consistency) and codex via a relay agent (correctness). 16 findings; 13 acted on.

### The plan contradicted itself, and the builder noticed

The plan's stub block declares `jira_update` with `Actions{ActionWrite}`, while the plan's own
`TestModuleDeclaresExpectedToolsAndActions` requires `{ActionWrite, ActionDestructive}` and Task 10's
prose calls it "the tool that spans two action classes". The implementing agent followed the test and
said so; haiku independently reached the same conclusion. Left as the builder set it, and it matters:
with only `ActionWrite` declared, the tool disappears entirely on a site that enables destructive but
not write.

### Fixed

| # | Finding | Engine | Disposition |
|---|---|---|---|
| J1 | **Lint failed on every file in the package** — 18 issues: goimports grouping, `func projectOf is unused`, `[0-9]+` for `\d+`, a `"fields"` goconst, twelve unchecked `io.WriteString`/`Decode` returns, one prealloc. CI is a strict security → lint → test chain, so the diff could not pass a branch build. | fable (blocking) | **Fixed.** My instruction to the builders not to run `make lint` is what let this through — the reason was sound (the sibling package was half-written and would have confused the output) but the cost was a blocking finding in both packages. Next time: lint the one package, not the repo. `projectOf` was deleted rather than carried unused, with a comment telling Task 10 to bring it back alongside its caller. |
| J2 | **Three field states were collapsed into two.** A helper that could not parse a shape returned `nil`, and `flatten` wrote that into the map, so `"assignee":null` was emitted for an issue that *is* assigned. Separately, a genuinely null field was skipped entirely, making "unset" indistinguishable from "Jira never sent this field" — which is what an unknown field name produces. | fable (important, probed) and codex (important), from opposite directions | **Fixed by reconciling them rather than applying either.** The two reviewers wanted opposite things — one wanted null not to mean "absent", the other wanted absent not to mean "null" — and both are right about the same underlying flaw. All three states are now distinct: present-and-null emits `null`, present-and-understood emits the scalar, present-but-unreadable passes the raw JSON through, because a shape this code cannot read is still data. Tests: `TestFlattenKeepsExplicitNullsDistinctFromAbsentFields`, `TestFlattenPassesUnreadableShapesThrough`. |
| J3 | **The parent lost its summary.** Jira's parent object carries no `name`: the key is in `key` and the readable half in `fields.summary`, so treating it as a generic named object rendered `"parent":"PROJ-9"` and forced a second call to learn what the parent was. | fable (important, probed) | **Fixed.** A dedicated `parentValue` renders `PROJ-9 (The epic)`. Test in `TestFlattenFieldShapes`. |
| J4 | **A user with no `displayName` came back as null.** The name is hidden whenever the site's privacy settings say so, which is common, and `AccountID` was decoded and never used. | fable, codex (both) | **Fixed.** The account id is the fallback. An assigned issue must never read as unassigned. |
| J5 | **`+description` on `jira_search` shipped a raw ADF blob.** Search runs on v3, where rich text is ADF — a format this project deliberately does not parse — while `jira_get` runs on v2 and returns the same field as markdown. The same field name produced two different formats. | fable (important, probed) and codex's relay agent (which codex itself missed) | **Fixed by refusing rather than converting.** `jira_search` rejects `description` and `environment` with a message pointing at `jira_get`. Parsing ADF is out of scope by the dependency budget, and silently shipping it defeats the token-budget rationale field selection exists for. Test: `TestSearchRefusesRichTextFields`. |
| J6 | **Jira silently ignores a field name it does not know or the caller cannot see**, so the result was quietly short with no way to tell which name was wrong. | codex (important) | **Fixed, with the relay agent's cheaper design.** Codex wanted validation against Jira's field metadata, which costs an extra round trip on every read. The result now echoes `unavailable_fields` instead — same information, no extra call. Test: `TestUnavailableFieldsAreReported`. |
| J7 | No test exercised a non-2xx response, so `*core.APIError` propagation — the most common runtime path there is — was unexercised; and `namedValue`, `personValue` and `namedList` had no output assertions anywhere. | fable and codey both, plus the builder's own lead | **Fixed.** `TestUpstreamFailurePropagatesAsAPIError` covers 400/404/500 across both tools; `TestFlattenFieldShapes` is table-driven over nine real shapes. Coverage went 69.3% → 89.0%. |
| J8 | The JQL string was unbounded — the only user-controlled string in the repository with no cap. | fable (important, probed at 5 MiB) | **Fixed.** `maxJQLLen` of 4096, checked before the network. |
| J9 | **`New()`'s handlers were bound to a nil client and would panic if called.** The plan documents `New()` as declaration-only "with nil handlers". | codex (important) | **Fixed, but not the way codex said.** Its fix — set every `Handle` to nil — would make `Register(jira.New())` panic at startup, because `core.Registry.Register` rejects a nil `Handle` by design. So the plan's own Interfaces text is what conflicts with the registry. The handlers now refuse with a message naming `NewWith`. Test: `TestDeclarationOnlyModuleHandlersRefuseRatherThanPanic`. |
| J10 | `handleGet` trusted the response's `key` over the validated one, so a response omitting it produced an issue with an empty key. | fable (minor) | **Fixed:** falls back to the validated, canonical key. |
| J11 | Two tests looped `m.Tools()` with a `continue` guard, so a rename would make them pass by never running their bodies. | codex's relay agent | **Fixed.** A `declFor` helper `t.Fatal`s on a missing tool. |
| J12 | The truncation count fallback had no pin tests, so a future `clampLimit` change could break it silently. | fable (minor, claim refuted at runtime, confirmed as a test gap) | **Fixed.** `TestSearchTruncationSignalsArePinned` covers the three shapes. |
| J13 | **`internal/core/client.go` only understood Jira's error shape.** Confluence v2 sends `{"errors":[{...}]}` — an array, not an object — which failed the whole unmarshal and dropped the caller into the raw-body fallback, so every Confluence v2 failure reached the model as a JSON blob with the useful sentence buried inside it. | fable, on the Task 12 review | **Fixed in core**, since both modules share it: `errorDetails` reads both shapes. Test: `TestDoSurfacesConfluenceV2ErrorArray`. |

### Recorded, not changed

| # | Finding | Engine | Why |
|---|---|---|---|
| J14 | `clampLimit` is now byte-identical in both product modules. | haiku | **Left duplicated for now, deliberately.** Limit policy belongs to core, which already owns `LimitDefault` and `LimitMax`, so hoisting it is right — but doing it during a review of two packages written in parallel would mean changing core, jira and confluence in one unreviewed sweep. It is the first thing Task 10 should do. |
| J15 | Codex rated the unknown-field-name behaviour "important" and prescribed a metadata round trip. | codex | **Downgraded** by the relay agent's reasoning, and solved more cheaply — see J6. |
| J16 | Line numbers in codex's report were off by one. | codex's relay agent | **Not a finding, a process artifact.** A `make fmt` import regroup landed on disk while codex was reading. The relay agent detected the shift, remapped every anchor, and said so. The freeze rule from Task 8 held for the file bodies; it did not cover formatting, which I ran mid-review. |

Result: 39 tests in `internal/jira`, 89.0% coverage (from 69.3%), and `make check` green end to end
across all four packages — `golangci-lint` 0 issues, `gosec` 0 issues over 3851 lines, gitleaks,
trufflehog and `govulncheck` clean, `go mod tidy -diff` clean.

## Task 10: Jira lookups and jira_update

Files: `internal/jira/{module.go,lookup.go,update.go,read.go,lookup_test.go,update_test.go}`, plus
`internal/core/config.go`. Built by a delegated agent in parallel with Task 13, then reviewed by
fable (completeness), haiku (consistency) and codex via a relay agent (correctness).

This is the first tool in the project that **writes to a customer's Jira**, and the review was told to
weigh findings by what a wrong write costs. That framing earned its keep: three of the findings were
silent data corruption, not errors.

### Data loss, found and fixed

| # | Finding | Engine | Disposition |
|---|---|---|---|
| U1 | **`fixVersion` replaced the whole array.** A value under `fields` is a SET in Jira's edit API, so setting one fix version silently discarded every version already on the issue — reachable with only the *write* capability, whose SPEC definition is "additive and reversible". | fable (blocking) and codex's relay agent (which codex itself missed) | **Fixed with the better of the two proposed remedies.** Reclassifying the field as destructive would have made the label honest but the tool useless for its purpose. Jira accepts an `update` member alongside `fields`, and `{"update":{"fixVersions":[{"add":{"id":…}}]}}` is genuinely additive — so the behaviour now matches the declared class instead of the label matching the behaviour. Test asserts `fields.fixVersions` is *absent* and the `add` verb is used. |
| U2 | **A whitespace-only description erased the real one.** `"   "` passes the non-empty gate, `markup.ToWiki` renders it to `""`, and the PUT sent `"description": ""` — wiping the customer's description while reporting `updated: description`. | fable (important, probe-confirmed) | **Fixed.** A description that renders to nothing is refused, because clearing a field is not something this tool offers. Test: `TestUpdateRefusesADescriptionThatRendersEmpty`. |
| U3 | **A case-variant version resolved to the wrong id.** Jira permits `beta` and `Beta` to coexist; `EqualFold` returned the first match, so an exact request for `Beta` wrote `beta`'s id. | fable (important, probe-confirmed) | **Fixed.** Exact name wins; a case-insensitive match is accepted only when unique, and two folded matches is an error naming the collision. |
| U4 | **The write allowlist could be bypassed by a moved issue.** Jira keeps every past key working after a move, resolving it to the issue's current home with no redirect a client can detect — so `ATLAS_WRITE_PROJECTS=OLD` authorised writes into whatever project the issue now lives in. | codex (blocking) and fable (important), independently | **Fixed.** When an allowlist is in force, the handler resolves the issue's current project and re-checks. `core.Config` gained `RestrictsProjects()`/`RestrictsSpaces()` so the extra round trip happens only where it can change the answer — an unrestricted deployment pays nothing. Tests cover the moved-issue refusal and the no-allowlist no-GET case. The relay agent verified the API claim against Atlassian's own documentation rather than accepting it: the docs state a moved issue's old key resolves with no redirect returned. |

### Where I judged a reviewer's fix too strict

| # | Finding | Disposition |
|---|---|---|
| U5 | **A single fuzzy user match was accepted without checking it.** Jira's user search is a substring match, so one result for "and" can be "Alexander" — and the exact-email tiebreak only ran in the multi-result branch, skipping the case where exactness matters most. | **Fixed, but not as prescribed.** Codex wanted an exact email or display-name match required outright. Implementing that broke two existing tests, and the tests were right: on a site where GDPR privacy settings hide `emailAddress`, a search *by* email returns a user with no email field, so requiring an exact email in the response makes an emailed assignee unresolvable — breaking the tool's documented primary input. The landed rule is layered: exact email wins; else a unique exact display name; else a single result is accepted **only for an email-shaped query**, because a full address is not a plausible accidental substring of someone else's name. A single fuzzy *name* match is refused. Four tests pin the ladder. |

### Adjudications the review was asked to make

The implementing agent raised three design questions rather than guessing, and fable ruled on each:

- **No field can be cleared** — keep the decision, but it was documented nowhere the model could see. A model asked to "unassign PROJ-1" would send `assignee: ""` and get a misleading "nothing to set". The tool description now states that omitted and empty fields are left unchanged and that the tool cannot clear a field, and the error says so too.
- **`fixVersion` replacing the array** — not a documentation problem, a real gap. See U1.
- **The handler's capability re-check** — correct defence in depth, kept. `Handle` takes raw JSON and is reachable without SDK validation (the package's own tests do exactly that), the whole gating design exists because a filter in front of a full surface was bypassed in the advisory this project was measured against, and the check is fail-closed and free.

### Minor, fixed

`maxSummaryLen` counted bytes against a limit Atlassian states in characters, so a 200-character CJK
summary was refused at 600 bytes — `boundedRunes` now measures runes for that limit while byte bounds
stay where the point is payload size. `validKey` had no length bound, so a megabyte of "A" satisfied
the regex and reached a URL path. The summary is trimmed and documented as plain text, since Jira
renders it literally and markdown would appear as punctuation in an issue title. Two weak tests were
strengthened: one asserted only that `AdditionalProperties` was non-nil, and none pinned the payload's
field count, so an implementation sending `assignee: null` alongside a summary update would have
passed everything.

### Recorded, not changed

`logicalEpic` lives in `read.go` while the other field constants moved to `module.go`. haiku rated
this **blocking**; it is not a defect at all — a package-level identifier used from a second file in
the same package is ordinary Go, and the package compiles and passes. Noted here because the
mislabelling is worth remembering: severity inflation costs the gate credibility, and the next
reviewer prompt said so explicitly.

Result: 62 tests in `internal/jira`, 91.9% coverage, `golangci-lint` 0 issues for the package,
`internal/core` still green at 96.0%.

## Task 13: Confluence write tools

Files: `internal/confluence/{module.go,write.go,write_test.go,read.go}`, plus `internal/core/config.go`.
Built by a delegated agent in parallel with Task 10, then reviewed by fable (completeness), haiku
(consistency) and codex via a relay agent (correctness).

The builder found a blocking defect in the plan before any reviewer saw the code: the plan's update
path never requests `include-version` and decodes the version into a non-pointer struct, so a missing
version becomes `0` and the PUT sends `"number": 1` — the exact silent-zero the optimistic lock exists
to prevent. It also dropped the plan's `body-format=view` from the update's preliminary GET, which
fetched the largest object the API returns only to discard it.

### The lost update, and why it needed a decision rather than a patch

Both fable and codex arrived at the same finding from different directions: **the optimistic lock did
not protect the cycle a model actually performs.** `confluence_get_page` surfaces `version`
"because confluence_update_page needs it for optimistic locking", but the update tool accepted no
version — it refetched the current one itself and PUT current+1. The lock therefore covered only the
microseconds inside the handler. An edit made between the model *reading* the page and *calling* the
tool was silently overwritten.

Codex rated it blocking; the relay agent downgraded it to important with the right reasoning — the
behaviour matches `SPEC.md:78` and the plan's own reference implementation, so it is a challenge to
the specified design, not a deviation from it, and that belongs to the owner rather than to a
reviewer.

**Landed:** `version` is now an **optional** argument. Supplied, it is the version the caller read and
a mismatch is refused with both numbers named; omitted, the previous behaviour stands. That honours
the read tool's promise without changing the spec'd required signature. Three tests: stale refusal,
current acceptance, malformed rejection.

### Also fixed

| # | Finding | Engine | Disposition |
|---|---|---|---|
| P1 | **Length bounds counted bytes against a limit Atlassian states in characters**, so a 100-character CJK title (300 bytes) was refused locally with an error the server would never have produced — and the code comment claimed the bound "turns a server-side 400 into a local error", when it was inventing one. | fable and codex, independently | **Fixed** for the title on both create and update. Byte bounds stay where the point is payload size. |
| P2 | **A PUT response without a version handed the model `version: 0`.** The read path guards this with a pointer precisely so a missing version cannot become zero; the write path did not. | fable and codex, independently | **Fixed:** pointer plus a fallback to the number the server must have accepted. |
| P3 | **The riskiest configuration had the least-exercised code.** Every allowlist test asserted a refusal, so the branch that lets a write *through* in restricted mode — the one that runs in production on a locked-down site — was never executed. | fable (elevated from the builder's own note) | **Fixed:** `TestRestrictedModeAllowsAWriteToAnAllowlistedSpace` covers all three tools, and `TestSpaceKeyForFailsClosed` covers the three fail-closed branches that are the backstop. |
| P4 | **Two of three subtests in the upstream-failure test were vacuous.** The all-403 fake made the *preliminary* GET fail, so create never reached its POST and update never reached its PUT; both would have passed against a handler that swallowed its real write error. | codex (minor, scoped exactly right — the relay agent instrumented the handlers to confirm which requests actually arrived) | **Fixed.** The lookups now succeed and only the write itself 403s, with an assertion that the write request was actually made. |
| P5 | A comment claimed `spaceIDFor` "only accepts a space whose key matches this one exactly" while the code folds case. | codex's relay agent | **Comment fixed, code left alone** — the relay agent checked whether this was an allowlist bypass and it is not: `core.allowed` folds too, so the two checks agree. A false claim about a security property is worth correcting even when the behaviour is right. |
| P6 | `RestrictsSpaces()` existed and was unused; `write.go` inlined `len(m.cfg.WriteSpaces) > 0` while the Jira module used the accessor for the identical decision. `commentDecl` mixed a constant and a literal in one schema. | codex's relay agent, haiku | **Both fixed.** |

### Recorded as accepted risk

**The comment tool has a space-move TOCTOU.** The allowlist is checked by resolving the page's space,
but the footer-comment POST carries only `pageId`, so a page moved to a disallowed space between the
check and the post still receives the comment. Codex rated it important and proposed binding the post
atomically to an allowed space — the relay agent checked Atlassian's v2 OpenAPI spec and found
`CreateFooterCommentModel` has no space or version field to bind to, so the fix cannot be built on
this API; codex's own text concedes a preflight GET "only narrows the race". Its remaining proposal
amounts to removing the comment tool whenever an allowlist is set. Recorded rather than done: the
window is sub-second, the same race exists on the update path, and dropping a tool in restricted mode
is a product decision.

### Cross-module duplication, now resolved in core

Three reviews in a row flagged the same thing: `clampLimit` was byte-identical in both modules, and
the length-bound helper existed twice under two names (`bound` and `boundedField`). `internal/core`
now owns `Config.ClampLimit`, `BoundBytes` and `BoundRunes`, and both modules call them. This was
deferred once already, correctly — doing it mid-review would have meant an unreviewed sweep across
three packages — and done here, once both gates had closed.

### Premise checking earned its place again

The design rests entirely on `representation: "wiki"` being accepted by the v2 API. Rather than take
it from the plan, the relay agent verified it against Atlassian's machine-readable v2 spec:
`PageBodyWrite` and `CommentBodyWrite` both enumerate `["storage", "atlas_doc_format", "wiki"]`, and
`PageUpdateRequest` requires exactly the five members the code sends, with `version.number`
documented as "the current version number plus one". All correct — but this is the round where that
check was cheapest to skip and would have been most expensive to get wrong.

Result: 65 tests in `internal/confluence`, 92.9% statement coverage, and `make check` green end to
end — `golangci-lint` 0 issues, `gosec` 0 issues over 4687 lines, gitleaks, trufflehog and
`govulncheck` clean, `go mod tidy -diff` clean.

## Task 11: jira_comment and jira_transition

Files: `internal/jira/{write.go,write_test.go,module.go,read.go,update.go}`, plus one fix in
`internal/markup/to_wiki.go`. Reviewed by fable (completeness), haiku (consistency) and codex via a
relay agent (correctness). haiku found nothing; the other two converged on the same defect from
opposite directions.

With these two tools all ten exist.

### The resolver picked the wrong transition, and a transition is not undoable

`jira_transition` accepts an id, a transition name, or a target status name. The landed code tried
them as ranked tiers, most specific first — which reads as obviously right and is wrong.

Both reviewers proved it with the same shape of workflow, independently:

- fable: transitions `{1 "Dup"→A, 2 "Dup"→B, 3 "Go"→Dup}` with `status: "Dup"`. The name tier
  matched **ambiguously**, so the switch fell through to the target tier and silently executed
  transition 3 — a path the caller never named.
- codex's relay agent: id 11 named "Done" targeting **Closed**, id 22 named "Resolve" targeting
  **Done**. Asking for `status: "Done"` moved the issue to Closed. No error, no warning.

The second is the one that matters. The parameter is called `status`, SPEC frames the lookup as
"status name → transition id", and the tool answered by firing a transition into a different status
— triggering whatever automation and notifications that workflow carries, irreversibly.

**Fixed** by abandoning ranked tiers: every category is collected, matches are deduplicated by
transition, and the move happens only when the whole set names exactly one. Anything else is an
ambiguity the caller settles with an id, and the error lists every candidate as `name -> target
(id N)`. Five tests now cover it, including both reviewers' workflows.

codex rated this blocking; the relay agent downgraded it to important and was right — the code
compiles, runs, and violates no stated constraint, because SPEC never specifies resolution rules.
Notably it also refused to treat "each accepted only when unique" as evidence, because that phrasing
came from **my review prompt**, not the spec. That is the premises rule working in the direction it
was written for.

### Test quality, and where the weak test came from

| # | Finding | Engine | Disposition |
|---|---|---|---|
| T1 | `TestTransitionResolvesStatusNameToID` gave every transition the same name as its target status, so it passed whether resolution worked by name, by target, or by only one of them — and could never surface the collision above. | codex | **Fixed.** The fixture now keeps names and targets distinct, and the relay agent's extra observation is addressed too: *every* fixture in the file had that flaw, so resolution by target status alone was untested anywhere. It has its own test now. Worth recording that this fixture is **copied verbatim from the plan** — the implementer followed instructions, and the defect is the plan's. |
| T2 | The transition case in the upstream-error test returned 403 for the initial GET, so the POST was never reached and the test would pass against a handler that swallowed its write error. | codex | **Fixed** the same way the identical flaw was fixed in Task 13: the lookup succeeds, only the write 403s, and the test asserts the write was actually attempted. Two rounds, two packages, same mistake — a fake that fails everything tests less than it appears to. |

### Adjudicated, no change

- **`authorizeWrite` duplicates the allowlist logic still inlined in `handleUpdate`.** Real, and it is
  a security check, so drift is the risk. But folding it here would mean rewriting `update.go`, which
  is outside this task's file list, and `handleUpdate` additionally needs the verified project back
  for its version lookup. Recorded as a follow-up: change `authorizeWrite` to return
  `(project string, err error)` and have `handleUpdate` consume it.
- **The "renders to nothing" guard on the comment body is unreachable.** fable probed every plausible
  candidate — link reference definitions, HTML comments, footnotes, bare entities, a lone backslash —
  and `markup.ToWiki` returns `""` only for whitespace-only input, which `TrimSpace` already rejects.
  Kept as a one-line defence against future converter changes; its comment was corrected, since it
  claimed HTML comments render to nothing and they do not.
- **Byte bound on the comment body versus Jira's 32,767-character limit.** fable ruled bytes correct
  here, and the reasoning is better than the convention: markdown→wiki conversion changes length, so
  no local check can mirror the real limit anyway. The bound exists to keep an unbounded payload off
  the wire, Jira's own 400 handles the rest, and `BoundRunes` stays for values sent unconverted.

### A finding for a different task, fixed anyway

fable noticed, while probing comment bodies, that `internal/markup/to_wiki.go` emitted HTML blocks as
**raw unescaped source** — so a comment body containing `<div>{code}...{code}</div>` posted a live
macro, straight past the escaper that Task 7's review installed as the converter's injection
boundary. Confirmed: `<div>{code}malicious{code}</div>` came out byte-identical.

Fixed in `to_wiki.go`: an HTML block is content, not markup this converter produced, so it goes
through `escapeWiki` like any other text. Angle brackets mean nothing to wiki, so ordinary HTML is
untouched — the existing pass-through tests still assert byte-identical output. Inline raw HTML was
already safe, because the text between tags is a Text node and was already escaped.

Result: 86 tests in `internal/jira`, `golangci-lint` 0 issues.

## Task 14: packaging and documentation

Files: `Dockerfile`, `compose.yaml`, `.dockerignore`, `README.md`, `CLAUDE.md`,
`compose.override.yaml.example`, and `cmd/atlassian-mcp-lite/main.go` (landed separately, see below).
Reviewed by fable (completeness and accuracy) and haiku (toolchain consistency), with codex's
correctness pass running against the corrected files.

### main.go, and a lint rule that was right

`main.go` was deferred out of Task 6 and again out of Task 13, so that the two product packages could
be built in parallel without a `cmd/` that imported both. It landed here, unchanged in substance from
the version parked in the plan — and `golangci-lint` immediately caught something the plan's code had
carried all along: `os.Exit` after `defer stop()`, so the signal handler is never released.

Harmless in a process that is exiting, but it is a lie in the shape of correct code, and the fix is
the standard one: `main` now does nothing but turn an error into an exit status, and everything else
lives in `run() error` where defers actually run. The two failure paths were then verified in the
real container: no configuration gives `configuration: ATLAS_BASE_URL is required`, and valid
credentials with no capability give `no tools enabled: set at least one of ATLAS_<DOMAIN>_...`.

### The corporate CA shipped in the production image

**The finding of this round, and I raised it as a lead before dispatching** — the Dockerfile appended
the corporate CA to `/etc/ssl/certs/ca-certificates.crt` in the build stage, and the final stage
copied that same file. fable confirmed it empirically rather than by reading: it built with the
secret and found the runtime bundle had grown from 150 to 151 certificates, matching the Crytek CA by
SHA-256 fingerprint.

So a container built on the office network trusted a TLS-interception CA for its Atlassian
connection — and the Dockerfile's own comment claimed the opposite, that neither the CA nor the token
"is written into a layer".

**Fixed** by never mutating the system bundle: the CA is concatenated with the build image's roots
into `/tmp/bundle.crt`, named through `SSL_CERT_FILE`, and deleted inside the same `RUN`; the runtime
roots are copied from a pristine `golang:1.27-trixie` rather than from the build stage, so no future
edit to the build can reach them either. Verified after the fix: exactly 150 certificates, corp CA
absent by fingerprint.

fable also surfaced the reason this was intermittent — **BuildKit does not key its layer cache on
secret contents**, so whether the CA landed in an image depended on which cached `go mod download`
layer was reused. That is also why `make image` appeared to work earlier and then failed once the
Dockerfile changed: the successful builds had been served from cache. The real cause was a
machine-local `compose.override.yaml` predating the production service, so no CA secret was passed at
all. The tracked template already had the block; the local copy did not.

### The documented build command did not work

fable found that `docker compose build` — the command the README gave — **fails outright** on any
machine with a `compose.override.yaml`, because Compose auto-loads that file for the default
`compose.yaml` and it names dev-only services. Worse, the README stated the caveat backwards: it
claimed a hand invocation without `-f` silently skips proxy settings, when in fact without `-f` the
override auto-loads and errors, and it is the explicit `-f compose.yaml` that skips it.

Fixed: `make image` is now the documented command, and the caveat is stated in both directions. The
same inverted claim in `compose.override.yaml.example` was corrected too, along with a trap that cost
real time here — Compose fails the *entire* project when a secret names a file that does not exist,
so a machine without `~/.netrc` must delete that secret rather than leave it declared.

### The gap that could not be closed

Every remaining blocking finding from both reviewers traces to one missing file: **`.env.example`
cannot be written**, because a permission rule denies `.env*`. It is not a code problem and I did not
route around it — writing the content under another name and renaming it would defeat a control the
user put there deliberately.

What was done instead: the README's Running section now inlines a minimal environment file rather
than telling the reader to copy a template, and it says explicitly that at least one capability must
be enabled — without which the server exits 1, which the previous three-variable instruction would
have produced. `compose.yaml`'s comment no longer names the file. Still outstanding, and recorded in
`CLAUDE.md` so a later session does not assume the packaging is complete: the file itself, and
`internal/core/env_example_test.go`, the test that guards it against drifting from what `Load` reads.

### Documentation accuracy

fable checked the README's environment table against `internal/core/config.go` variable by variable
and found it exact — all ten fixed variables, the six derived capability flags, every default, every
validation rule. The tool table was verified against a live `tools/list` on the built image with all
capabilities enabled, and each action class matched its `Actions` declaration. A full MCP handshake
was confirmed under the README's own hardened `docker run` invocation.

Three claims were wrong and are fixed: `CLAUDE.md` said `.env.example` had landed, said the
dependency budget was three modules when SPEC and `go.mod` name four, and the README omitted that
allowlist matching is case-insensitive and that a `~` prefix is permitted.

Result: image builds at 9.6 MiB, `make check` green — `golangci-lint` 0 issues, `gosec` 0 issues over
4997 lines, gitleaks, trufflehog and `govulncheck` clean, coverage core 96.4%, confluence 92.9%,
jira 92.3%, markup 92.7%.

### Task 14, codex pass: a release job nobody had ever run

The correctness pass went last, against the already-corrected files, and found the one defect the
other two could not: **the release job cannot push its image.** `ghcr.io/${{ github.repository }}`
yields `ghcr.io/OxCom/atlassian-mcp-lite`, and a Docker repository component must be lowercase, so
the reference is rejected before anything is pushed. Fixed with `${GITHUB_REPOSITORY,,}`. This is a
tag-gated job that has never executed — exactly the code a review is for.

It also found that **`-X main.version` set nothing**. Package `main` has no such symbol; the version
lives in `internal/core`, and it was a `const`, which the linker cannot set either. The linker
ignores an unmatched `-X` in silence, so every release binary would have reported `0.1.0` forever.
`Version` is now a `var`, the flag names its real import path, and the Dockerfile takes a `VERSION`
build arg that the release job passes — verified by building with `-X …Version=v9.9.9` and finding
the string in the binary.

Two smaller things it caught, both leftovers from fixing the earlier findings: `compose.yaml`'s own
header still advertised the `docker compose build` command the README had just been corrected away
from, and both README and `CLAUDE.md` claimed the Dockerfile guards *both* secrets with `if [ -s ... ]`
when only the CA is processed at all. Fixed.

**Two false blockers, and how they were caught.** Codex reported that `/out` and `dist/` are never
created, so `go build -o` into them fails. The relay agent tested it — `go build -o` does create the
parent directory — and discarded both. That is the premises rule doing its job in the direction it
was written for: a confident claim about tool behaviour, refuted by running the tool.

**Recorded, not fixed.** Codex wants `golang:1.27-trixie` pinned by digest and every GitHub Action
pinned to a commit SHA. Both are right for a project that publishes releases, and neither is a
one-line change: digests need re-pinning on every base image update, and action SHAs cannot be looked
up from this environment. What was fixed is the half that costs nothing — the base image is now a
named `base` stage resolved once, so the toolchain and the runtime trust roots can no longer come
from two different images in a single build. Also recorded: the release publishes only linux/amd64
though the matrix cross-compiles arm64, so the container and the binaries disagree about which
platforms are supported.

The relay agent additionally answered two lens items codex skipped silently: the scratch image needs
no `/tmp`, tzdata or `/etc/passwd` (no code path calls `os.CreateTemp`, `time.LoadLocation`,
`user.Current` or `os.UserHomeDir`), so `read_only: true` holds; and the README's account of what
happens with no capability enabled matches `main.go`. Reporting "I checked this and it is fine" is
worth as much as a finding, and codex's silence there was indistinguishable from not having looked.

Final state: image 10.1 MB, runtime trust store exactly the 150 pristine roots, `make check` green —
`golangci-lint` 0 issues, `gosec` 0 issues over 5003 lines, gitleaks, trufflehog and `govulncheck`
clean.
