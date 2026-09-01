# Review 01 — findings and dispositions

Three independent reviews of `SPEC.md` and `2026-09-01-atlassian-mcp-lite.md`, run 2026-09-01 on
three engines with different lenses. 39 raw findings, deduplicated to 27 below.

| Engine | Lens | Raw findings |
|---|---|---|
| Fable | completeness against spec, missing work, error paths | 13 |
| Haiku | mechanical consistency: signatures, types, imports, helpers | 7 |
| Codex | Go correctness, SDK and library API usage, test validity | 19 |

Independent agreement is noted per finding: a defect all three found separately is the strongest
signal in this table.

## Blocking

| # | Finding | Found by | Disposition |
|---|---|---|---|
| B1 | `Server.AddTool` performs **no input validation** — it is the low-level API and passes raw arguments straight to the handler. The entire capability-gating design assumed the SDK would reject properties absent from the schema. It would not: `description` would reach `jira_update` with `destructive=false`. | Codex | **Fixed.** Verified in v1.7.0 source: `ToolHandler`'s doc states "no input validation is performed"; the generic `mcp.AddTool` wraps the handler with `applySchema` before invoking it, and `setSchema` honours a preset `InputSchema` (`if *sfield == nil` guards the reflection path). Task 6 now uses `mcp.AddTool[json.RawMessage, any]`. |
| B2 | `jira_update` is declared with a single `ActionWrite`, so `Registry.Enabled` drops it entirely when `write=false, destructive=true` — the spec's own headline gating case. The Task 10 test hid this by calling `d.Schema(caps)` directly instead of going through the registry. | **all three** | **Fixed.** `ToolDecl.Action` replaced by `ToolDecl.EnabledBy func(Caps) bool`, plus a registry-level test asserting the destructive-only registration. |
| B3 | `confluence_get_page` has no `fields` parameter; `core.ResolveFields` is never called in the confluence package, contradicting the spec's field-selection contract. | **all three** | **Fixed.** `fields` added with Confluence defaults and the same grammar tests as Jira. |
| B4 | `confluence_comment` never checks `ATLAS_WRITE_SPACES`. A caller could comment on any page the credential reaches, bypassing the allowlist. | Codex | **Fixed.** Resolves the page's space key and checks `AllowSpace` before the POST, with a test asserting no request is made on refusal. |
| B5 | `MaskHeaders` leaks the first segment of any `Cookie`/`Set-Cookie` value containing a space: `session=secret; Path=/` is treated as `scheme=session=secret;`. The test used a cookie without spaces and missed it. | Codex | **Fixed.** Scheme preservation now applies only to `Authorization` and `Proxy-Authorization`; cookies are masked whole, across all header values. |

## Important

| # | Finding | Found by | Disposition |
|---|---|---|---|
| I1 | The logical `epic` field is never mapped to `cfg.EpicFieldID` on **read**, so `fields: ["+epic"]` would send the literal string `epic`, which is not a Jira field. `epic` is also absent from the spec's stated default sets. | Fable, Codex | **Fixed.** Logical-to-upstream field translation added on request, and the reverse on response, with tests. |
| I2 | Task 10's "Produces" block promises `epicFieldIsParent()` (the classic vs team-managed detection the spec describes). No step implements it, so a team-managed project would receive a 400 on `epic`. | Fable, Haiku | **Fixed by scoping down.** The promise is removed and the limitation documented: `epic` writes the configured custom field, which is correct for classic projects. Team-managed projects use `parent`. An `editmeta` probe per call was rejected as an extra round trip on every update. |
| I3 | `Client.Do` treats only `>= 400` as failure, so an unfollowed 3xx would be decoded as success. | Codex | **Fixed.** Anything outside 200–299 is an `APIError`. |
| I4 | `ATLAS_BASE_URL` is never parsed. A malformed URL fails only on first use, and an `http://` URL would send Basic credentials in clear text. | Codex | **Fixed.** Parsed at load: HTTPS and a non-empty host required, no userinfo, query or fragment. Loopback hosts may use `http` so `httptest` works. |
| I5 | Truncation is reported when `len(results) >= limit`, which is false for a complete result set of exactly `limit` items. `total` is decoded but unused. | Fable, Codex | **Fixed.** Uses the APIs' own completion signals, with a test for the exactly-at-limit-but-complete case. |
| I6 | No client tests for 401, 429, context cancellation, or malformed JSON on a 2xx. `Retry-After` is not surfaced on 429, so a caller gets no backoff signal. | Fable | **Fixed.** Tests added; `APIError` carries `RetryAfter`. |
| I7 | Credential masking covers only explicitly logged headers. An upstream error body echoing a token would be logged verbatim, breaking the "unconditional at every level" guarantee. | Codex | **Fixed.** The logger is constructed with the secrets and redacts them from every emitted message, including upstream error text. Test asserts the exact token and the Basic value are absent. |
| I8 | Nested list markers repeat only the current list's marker, so a list nested inside a different list type produces `##` instead of Atlassian's ancestry-aware `#*`. | Codex | **Fixed.** The marker prefix is carried through recursion; mixed-nesting tests added. |
| I9 | `FromWiki` has no table handling at all, though tables are in the round-trip subset. The "load-bearing" round-trip test omitted tables, blockquotes, ordered lists and nesting. | Fable, Codex | **Fixed.** Table parsing added; the round-trip test now covers every construct the spec lists. |
| I10 | `inlineFromWiki` applies regexes sequentially, so emphasis conversion corrupts content inside inline-code spans and link destinations. | Codex | **Fixed.** Code spans and links are tokenised out before emphasis conversion, matching what `ToWiki` already does for images. |
| I11 | `FromHTML` emits table rows with no header-separator row, so the output is not a valid markdown table. The test only counted pipe-delimited lines, and the plan deferred correctness. | Codex | **Fixed.** A separator is emitted, synthesised when the table has no `<th>`. |
| I12 | `RawHTML` and `HTMLBlock` nodes are dropped, and an `HTMLBlock` has no inline children, so its whole source can disappear — violating the stated "never silently drop content" contract. | Codex | **Fixed.** Literal source is emitted for unsupported raw and block nodes. |
| I13 | `spaceIDFor` trusts the first `/spaces?keys=` result without checking the returned key matches, so an unexpectedly broad response could create a page in the wrong space after the input passed the allowlist. | Codex | **Fixed.** Exact case-insensitive key match required; zero or multiple matches is an error. |
| I14 | The spec claims egress is limited to the Atlassian host and leans on it ("SSRF becomes unreachable"), but neither `compose.yaml` nor the documented `docker run` restricts the network at all. | Fable, Codex | **Fixed by correcting the claim.** Container flags do not restrict destinations. The spec no longer claims egress restriction as delivered; it is documented as an operator-supplied control with concrete instructions, and the SSRF argument now rests on the base URL being a config constant. |
| I15 | The spec's tool table pins `jira_get` to v3 while the plan implements and tests v2, contradicting the spec's own body-format section. | Fable | **Fixed.** Spec table corrected to v2, which is what makes the wiki-to-markdown path work. |
| I16 | `confluence_create_page` gained a `parent_id` parameter absent from the agreed spec signature. | Fable | **Fixed.** Added to the spec, since a page needs a parent to land anywhere but the space root. |
| I17 | No SIGTERM or SIGINT handling. As PID 1 in a scratch container, `docker stop` would hang until the kill timeout on every shutdown. | Fable | **Fixed.** `signal.NotifyContext` in `main`. |
| I18 | `TestServerRoundTripsAToolCall` never asserts the echoed value, so it passes for any non-error result. `TestObjectSchemaForbidsAdditionalProperties` only checks non-nil, so a permissive `{}` schema would pass. | Codex | **Fixed.** Both now assert real behaviour; the schema test resolves and validates an object carrying an unknown field. |

## Minor, and deliberately not changed

| # | Finding | Found by | Disposition |
|---|---|---|---|
| M1 | `strings.ReplaceAll(n.Data, " ", " ")` looks like a no-op. | Fable | **False positive.** Hexdump shows `c2 a0` — a genuine U+00A0. Rewritten as `" "` with a comment, because a literal non-breaking space in source is a maintenance hazard. |
| M2 | Test helpers `contains` / `containsStr` / `declFor` / `call` are duplicated across packages. | Haiku | **Accepted.** Go test helpers are package-scoped; sharing them would mean an exported test-only package. Duplication is the conventional cost. `containsStr` renamed to `containsField` so the two names in package `jira` describe what they do. |
| M3 | Em dashes appear inside Go comments. | — | **Accepted.** Go source is UTF-8; comments may contain any character. |

## What this review changed about the plan's own claims

The plan's Self-review notes asserted that every spec section mapped to a task and named only two
untested requirements. That was wrong: B2, B3, B4, I1 and I2 were spec requirements with no
implementing step, and I14 was a spec claim the packaging never delivered. The Self-review section
has been rewritten to say so.

The most valuable single finding was B1, because it was invisible to inspection of the plan alone —
it required reading the SDK source to discover that the API the plan called does not do what the
plan assumed. Nothing in the plan's own logic was wrong; the environment was.
