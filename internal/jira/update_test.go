package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
)

func TestUpdateSchemaOmitsDestructivePropsWhenDisabled(t *testing.T) {
	d := declFor(t, newTestModule(t, func(http.ResponseWriter, *http.Request) {}), "jira_update")

	// Everything that replaces an existing value is destructive: summary and
	// description, but also assignee, epic and parent, which are SETs.
	off := d.Schema(core.Caps{Write: true})
	for _, prop := range []string{"summary", "description", "assignee", "epic", "parent"} {
		if _, ok := off.Properties[prop]; ok {
			t.Errorf("%q must be absent when destructive=false", prop)
		}
	}
	if _, ok := off.Properties["fixVersion"]; !ok {
		t.Error("fixVersion must be present when write=true")
	}

	on := d.Schema(core.Caps{Write: true, Destructive: true})
	for _, prop := range []string{"summary", "description", "assignee", "epic", "parent"} {
		if _, ok := on.Properties[prop]; !ok {
			t.Errorf("%q must be present when destructive=true", prop)
		}
	}
}

func TestUpdateSchemaWriteOnlyPropsAbsentWhenWriteDisabled(t *testing.T) {
	d := declFor(t, newTestModule(t, func(http.ResponseWriter, *http.Request) {}), "jira_update")
	s := d.Schema(core.Caps{Destructive: true})
	if _, ok := s.Properties["fixVersion"]; ok {
		t.Error("fixVersion must be absent when write=false")
	}
	if _, ok := s.Properties["description"]; !ok {
		t.Error("description must be present when destructive=true")
	}
}

// key is the one property that must survive every capability combination:
// without it there is nothing to address.
func TestUpdateSchemaAlwaysCarriesKeyAsRequiredString(t *testing.T) {
	d := declFor(t, newTestModule(t, func(http.ResponseWriter, *http.Request) {}), "jira_update")
	for _, c := range []core.Caps{{Write: true}, {Destructive: true}, {Write: true, Destructive: true}} {
		s := d.Schema(c)
		key, ok := s.Properties["key"]
		if !ok {
			t.Fatalf("caps %+v: key must always be present", c)
		}
		if key.Type != "string" {
			t.Errorf("caps %+v: key type = %q, want string", c, key.Type)
		}
		if len(s.Required) != 1 || s.Required[0] != "key" {
			t.Errorf("caps %+v: required = %v, want [key]", c, s.Required)
		}
	}
}

// Every Atlassian identifier crosses the wire as a string. The SDK re-marshals
// arguments through map[string]any, so a JSON number loses precision above
// 2^53 and an id would silently change.
func TestUpdateSchemaDeclaresNoNumericProperties(t *testing.T) {
	d := declFor(t, newTestModule(t, func(http.ResponseWriter, *http.Request) {}), "jira_update")
	for name, prop := range d.Schema(core.Caps{Write: true, Destructive: true}).Properties {
		if prop.Type == "number" || prop.Type == "integer" {
			t.Errorf("property %q is %s; identifiers must be strings", name, prop.Type)
		}
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
		_ = json.NewDecoder(r.Body).Decode(&body)
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
		switch r.URL.Path {
		case "/rest/api/3/user/search":
			_, _ = io.WriteString(w, `[{"accountId":"aid-9","displayName":"A User"}]`)
		case "/rest/api/3/project/PROJ/versions":
			_, _ = io.WriteString(w, `[{"id":"777","name":"1.2.x"}]`)
		default:
			_ = json.NewDecoder(r.Body).Decode(&body)
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
	// fixVersions must NOT appear under `fields`: a value there is a SET and
	// replaces the whole array, silently dropping every version already on the
	// issue — data loss reachable with only the write capability.
	if _, ok := fields["fixVersions"]; ok {
		t.Error("fixVersions under `fields` replaces the array; it must use the add verb")
	}
	update, _ := body["update"].(map[string]any)
	ops, _ := update["fixVersions"].([]any)
	if len(ops) != 1 {
		t.Fatalf("update.fixVersions = %v, want one add operation", update["fixVersions"])
	}
	op, _ := ops[0].(map[string]any)
	add, _ := op["add"].(map[string]any)
	if add["id"] != "777" {
		t.Errorf("add = %v, want the resolved version id", op)
	}
}

// A whitespace-only description passes the non-empty check, renders to nothing,
// and would erase the issue's real description while reporting success.
func TestUpdateRefusesADescriptionThatRendersEmpty(t *testing.T) {
	wrote := false
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		wrote = true
		w.WriteHeader(http.StatusNoContent)
	})
	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1", "description": "   \n  "})
	if _, err := declFor(t, m, "jira_update").Handle(context.Background(), raw); err == nil {
		t.Fatal("a description that renders to nothing must be refused, not written as empty")
	}
	if wrote {
		t.Error("nothing may be written for a description that renders to nothing")
	}
}

// Only the fields the caller named may appear in the payload. Without this a
// change that sent defaults — assignee: null, fixVersions: [] — would pass
// every other test here while clearing fields nobody mentioned.
func TestUpdateSendsOnlyTheNamedFields(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusNoContent)
	})
	call(t, m, "jira_update", map[string]any{"key": "PROJ-1", "summary": "just this"})

	fields, _ := body["fields"].(map[string]any)
	if len(fields) != 1 {
		t.Fatalf("fields = %v, want exactly one entry", fields)
	}
	if fields["summary"] != "just this" {
		t.Errorf("summary = %v", fields["summary"])
	}
	if _, ok := body["update"]; ok {
		t.Errorf("update = %v, want it absent when no multi-value field was named", body["update"])
	}
}

// A summary is rendered literally by Jira, so it is sent as plain text with
// surrounding whitespace removed rather than converted.
func TestUpdateSummaryIsTrimmedPlainText(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusNoContent)
	})
	call(t, m, "jira_update", map[string]any{"key": "PROJ-1", "summary": "  Fix the thing  "})
	fields, _ := body["fields"].(map[string]any)
	if fields["summary"] != "Fix the thing" {
		t.Errorf("summary = %q, want it trimmed", fields["summary"])
	}
}

// Jira's summary limit is 255 characters, not bytes, so a CJK summary well
// inside the limit must not be refused here.
func TestUpdateSummaryLimitCountsCharacters(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	// 200 characters, 600 bytes.
	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1", "summary": strings.Repeat("課", 200)})
	if _, err := declFor(t, m, "jira_update").Handle(context.Background(), raw); err != nil {
		t.Errorf("a 200-character summary is within Jira's limit: %v", err)
	}
	raw, _ = json.Marshal(map[string]any{"key": "PROJ-1", "summary": strings.Repeat("課", maxSummaryLen+1)})
	if _, err := declFor(t, m, "jira_update").Handle(context.Background(), raw); err == nil {
		t.Error("a summary past the character limit must be refused")
	}
}

// A lookup that fails must abort the whole update: a partial write that set
// some fields and dropped the one that could not resolve is worse than no
// write, because the caller is told nothing about the gap.
func TestUpdateUnresolvableAssigneeWritesNothing(t *testing.T) {
	wrote := false
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/3/user/search" {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		wrote = true
		w.WriteHeader(http.StatusNoContent)
	})
	if err := callUpdateErr(t, m, map[string]any{
		"key": "PROJ-1", "assignee": "ghost@example.com", "summary": "new",
	}); err == nil {
		t.Fatal("an unresolvable assignee must fail the update")
	}
	if wrote {
		t.Error("no issue may be modified when a lookup failed")
	}
}

func TestUpdateEpicUsesConfiguredCustomField(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
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
		_ = json.NewDecoder(r.Body).Decode(&body)
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
	base := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
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

// The allowlist is matched on the canonical, upper-cased project, so a
// lower-case key cannot slip past an allowlist entry that names it.
// The allowlist is matched on the canonical, upper-cased project, so a
// lower-case key cannot slip past an allowlist entry that names it.
//
// With an allowlist in force the handler also verifies where the issue
// actually lives, so the fake has to answer that lookup.
func TestUpdateAllowlistMatchesCanonicalisedKey(t *testing.T) {
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"fields":{"project":{"key":"PROJ"}}}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}).(module)
	base.cfg.WriteProjects = []string{"PROJ"}
	raw, _ := json.Marshal(map[string]any{"key": "proj-1", "parent": "PROJ-2"})
	if _, err := declFor(t, base, "jira_update").Handle(context.Background(), raw); err != nil {
		t.Fatalf("an allowlisted project must be writable whatever the case: %v", err)
	}
}

// Jira keeps every past key working after an issue moves, resolving it to the
// issue's current home with no redirect a client can see. So the key's prefix
// is not proof of the project, and an allowlisted old prefix must not authorise
// a write into a project the operator never allowed.
func TestUpdateRefusesIssueMovedOutOfAnAllowlistedProject(t *testing.T) {
	var wrote bool
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// The old key still resolves — to an issue that now lives in SECRET.
			_, _ = io.WriteString(w, `{"fields":{"project":{"key":"SECRET"}}}`)
			return
		}
		wrote = true
		w.WriteHeader(http.StatusNoContent)
	}).(module)
	base.cfg.WriteProjects = []string{"PROJ"}

	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1", "summary": "x"})
	_, err := declFor(t, base, "jira_update").Handle(context.Background(), raw)
	if err == nil {
		t.Fatal("a moved issue outside the allowlist must be refused")
	}
	// Quoted, like every other third-party string in this package: the project
	// key comes back from Jira, so a newline or a control character in it must
	// not reach the log or the model raw.
	if !strings.Contains(err.Error(), `"SECRET"`) {
		t.Errorf("error = %v, want it to name the project the issue actually lives in, quoted", err)
	}
	if wrote {
		t.Error("nothing may be written once the allowlist check fails")
	}
}

// The verification costs a round trip, so it must not happen at all when no
// allowlist is configured — an unrestricted deployment should not pay for a
// check it can never fail.
func TestUpdateSkipsProjectVerificationWhenUnrestricted(t *testing.T) {
	var gets int
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets++
		}
		w.WriteHeader(http.StatusNoContent)
	}).(module)
	base.cfg.WriteProjects = nil

	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1", "summary": "x"})
	if _, err := declFor(t, base, "jira_update").Handle(context.Background(), raw); err != nil {
		t.Fatalf("jira_update: %v", err)
	}
	if gets != 0 {
		t.Errorf("made %d GETs, want none when no allowlist is configured", gets)
	}
}

func TestUpdateWithNoFieldsIsAnError(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made when there is nothing to set")
	})
	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1"})
	if _, err := declFor(t, m, "jira_update").Handle(context.Background(), raw); err == nil {
		t.Fatal("an update with no fields must error")
	}
}

func TestUpdateRejectsMalformedKeys(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for a malformed key")
	})
	for name, args := range map[string]map[string]any{
		"issue":  {"key": "../../secret", "summary": "x"},
		"epic":   {"key": "PROJ-1", "epic": "not a key"},
		"parent": {"key": "PROJ-1", "parent": "PROJ-1/../x"},
	} {
		if err := callUpdateErr(t, m, args); err == nil {
			t.Errorf("%s: a malformed key must be refused", name)
		}
	}
}

// Free-form strings are bounded, as maxJQLLen bounds the one on the read path.
func TestUpdateBoundsFreeFormStrings(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for an over-long field")
	})
	cases := map[string]map[string]any{
		"summary":     {"key": "PROJ-1", "summary": strings.Repeat("s", maxSummaryLen+1)},
		"description": {"key": "PROJ-1", "description": strings.Repeat("d", maxBodyLen+1)},
		"fixVersion":  {"key": "PROJ-1", "fixVersion": strings.Repeat("v", maxNameLen+1)},
		"assignee":    {"key": "PROJ-1", "assignee": strings.Repeat("a", maxNameLen+1)},
	}
	for name, args := range cases {
		if err := callUpdateErr(t, m, args); err == nil {
			t.Errorf("%s: an unbounded string must be refused", name)
		}
	}
}

// The schema hides properties a capability does not allow, and the SDK
// validates against it — but the handler is the boundary that actually
// performs the write, so it re-checks rather than trusting the caller.
func TestUpdateHandlerRefusesFieldsTheCapsForbid(t *testing.T) {
	base := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for a forbidden field")
	}).(module)

	base.cfg.Domains = map[string]core.Caps{Domain: {Read: true, Write: true}}
	if err := callUpdateErr(t, base, map[string]any{"key": "PROJ-1", "summary": "x"}); err == nil {
		t.Error("summary must be refused when destructive is disabled")
	}

	base.cfg.Domains = map[string]core.Caps{Domain: {Read: true, Destructive: true}}
	if err := callUpdateErr(t, base, map[string]any{"key": "PROJ-1", "fixVersion": "1.0"}); err == nil {
		t.Error("fixVersion must be refused when write is disabled")
	}
}

func TestUpdateReportsWhatItSet(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	out, _ := call(t, m, "jira_update", map[string]any{
		"key": "proj-1", "summary": "S", "parent": "PROJ-2",
	}).(map[string]any)
	if out["key"] != "PROJ-1" {
		t.Errorf("key = %v, want the canonicalised PROJ-1", out["key"])
	}
	updated, _ := out["updated"].(string)
	for _, want := range []string{"summary", "parent"} {
		if !strings.Contains(updated, want) {
			t.Errorf("updated = %q, want it to mention %q", updated, want)
		}
	}
}

func TestUpdateUpstreamFailurePropagates(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"errorMessages":["Field 'customfield_10014' cannot be set"]}`)
	})
	err := callUpdateErr(t, m, map[string]any{"key": "PROJ-1", "epic": "PROJ-9"})
	if err == nil {
		t.Fatal("a 400 must reach the caller")
	}
	if !strings.Contains(err.Error(), "customfield_10014") {
		t.Errorf("upstream diagnostics must survive verbatim, got %q", err)
	}
}

func TestUpdateWithoutClientIsAnError(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1", "summary": "x"})
	if _, err := declFor(t, New(), "jira_update").Handle(context.Background(), raw); err == nil {
		t.Fatal("a declaration-only module must refuse to handle a write")
	}
}

// callUpdateErr invokes jira_update and returns its error, if any.
func callUpdateErr(t *testing.T, m core.Module, args map[string]any) error {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	_, err = declFor(t, m, "jira_update").Handle(context.Background(), raw)
	return err
}

// assignee, epic and parent are SET operations: each replaces whatever value
// the issue already holds, which is exactly what the destructive class exists
// to gate. Only fixVersion is additive, so it alone stays under write.
func TestUpdateSetFieldsRequireDestructive(t *testing.T) {
	base := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for a field the capability forbids")
	}).(module)
	base.cfg.Domains = map[string]core.Caps{Domain: {Read: true, Write: true}}

	for field, value := range map[string]string{"assignee": "u@example.com", "epic": "PROJ-9", "parent": "PROJ-2"} {
		err := callUpdateErr(t, base, map[string]any{"key": "PROJ-1", field: value})
		if err == nil {
			t.Errorf("%s must be refused when destructive is disabled", field)
			continue
		}
		if !strings.Contains(err.Error(), "destructive") {
			t.Errorf("%s: error = %v, want it to name the destructive capability", field, err)
		}
	}
}

func TestUpdateFixVersionNeedsOnlyWrite(t *testing.T) {
	var put bool
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/project/PROJ/versions":
			_, _ = io.WriteString(w, `[{"id":"777","name":"1.2.x"}]`)
		case r.Method == http.MethodPut:
			put = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}).(module)
	base.cfg.Domains = map[string]core.Caps{Domain: {Read: true, Write: true}}

	if err := callUpdateErr(t, base, map[string]any{"key": "PROJ-1", "fixVersion": "1.2.x"}); err != nil {
		t.Fatalf("fixVersion is additive and must work with write alone: %v", err)
	}
	if !put {
		t.Error("the update was never sent")
	}
}

// Linking an issue to an epic or parent writes into that other issue's
// hierarchy too, so the link target is held to the same allowlist as the
// issue being updated: prefix check first, then where the issue really lives.
func TestUpdateLinkTargetsOutsideAllowlistAreRefused(t *testing.T) {
	for _, field := range []string{"parent", "epic"} {
		var (
			mu   sync.Mutex
			seen = map[string]int{}
		)
		base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen[r.Method+" "+r.URL.Path]++
			mu.Unlock()
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, `{"fields":{"project":{"key":"SANDBOX"}}}`)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}).(module)
		base.cfg.WriteProjects = []string{"SANDBOX"}

		err := callUpdateErr(t, base, map[string]any{"key": "SANDBOX-1", field: "PROD-7"})
		if err == nil {
			t.Errorf("%s outside the allowlist must be refused", field)
			continue
		}
		for _, want := range []string{field, `"PROD-7"`, "PROD"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error = %v, want it to mention %q", field, err, want)
			}
		}

		mu.Lock()
		counts := make(map[string]int, len(seen))
		for k, v := range seen {
			counts[k] = v
		}
		mu.Unlock()

		// The counter must have seen something, or every zero below would pass
		// on a fake that records nothing.
		if counts[http.MethodGet+" /rest/api/2/issue/SANDBOX-1"] == 0 {
			t.Errorf("%s: the issue being updated was never looked up, so the request counts prove nothing: %v", field, counts)
		}

		// Counted by path, not merely "no PUT was sent": the prefix check is
		// meant to settle the refusal on its own, so the refused key must never
		// be probed either. A GET against it would tell the caller whether
		// PROD-7 exists, which is exactly the information an allowlist outside
		// SANDBOX is there to withhold.
		for path, n := range counts {
			if strings.Contains(path, "PROD-7") {
				t.Errorf("%s: %s was requested %d times; a refused project must never be probed", field, path, n)
			}
		}
		if n := counts[http.MethodPut+" /rest/api/2/issue/SANDBOX-1"]; n != 0 {
			t.Errorf("%s: nothing may be written once the allowlist check fails, got %d PUTs", field, n)
		}
	}
}

// The link target's prefix is no more proof of its project than the target
// issue's is: a key that once belonged to SANDBOX may now resolve to PROD.
func TestUpdateLinkTargetMovedOutOfAllowlistIsRefused(t *testing.T) {
	for _, field := range []string{"parent", "epic"} {
		var put bool
		base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				if strings.HasSuffix(r.URL.Path, "/SANDBOX-9") {
					_, _ = io.WriteString(w, `{"fields":{"project":{"key":"PROD"}}}`)
					return
				}
				_, _ = io.WriteString(w, `{"fields":{"project":{"key":"SANDBOX"}}}`)
				return
			}
			put = true
			w.WriteHeader(http.StatusNoContent)
		}).(module)
		base.cfg.WriteProjects = []string{"SANDBOX"}

		err := callUpdateErr(t, base, map[string]any{"key": "SANDBOX-1", field: "SANDBOX-9"})
		if err == nil {
			t.Errorf("%s: a link target that moved out of the allowlist must be refused", field)
			continue
		}
		// The project key is Jira-supplied, so it is quoted like every other
		// third-party string in this package.
		if !strings.Contains(err.Error(), `"PROD"`) || !strings.Contains(err.Error(), field) {
			t.Errorf("%s: error = %v, want it to name the field and quote the project the issue lives in", field, err)
		}
		if put {
			t.Errorf("%s: nothing may be written once the allowlist check fails", field)
		}
	}
}

// Without an allowlist there is nothing to verify, so the link target must not
// cost an extra round trip either.
func TestUpdateLinkTargetSkipsVerificationWhenUnrestricted(t *testing.T) {
	var gets int
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets++
		}
		w.WriteHeader(http.StatusNoContent)
	}).(module)
	base.cfg.WriteProjects = nil
	if err := callUpdateErr(t, base, map[string]any{"key": "PROJ-1", "parent": "OTHER-2", "epic": "THIRD-3"}); err != nil {
		t.Fatalf("jira_update: %v", err)
	}
	if gets != 0 {
		t.Errorf("made %d GETs, want none when no allowlist is configured", gets)
	}
}
