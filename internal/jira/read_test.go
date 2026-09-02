package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"issues":[{"key":"PROJ-1","fields":{"summary":"S","status":{"name":"Open"}}}]}`)
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
	// The default set is documented in docs/configuration.md; keep both in step.
	want := []string{"summary", "status", "updated", "assignee", "reporter"}
	for _, f := range want {
		if !containsField(joined, f) {
			t.Errorf("default fields missing %q: %v", f, joined)
		}
	}
	if len(joined) != len(want) {
		t.Errorf("default fields = %v, want exactly %v", joined, want)
	}
	if containsField(joined, "description") {
		t.Error("description must NOT be in jira_search defaults")
	}
}

func TestSearchLimitCappedAtMax(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"issues":[]}`)
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
		_, _ = io.WriteString(w, `{"issues":[{"key":"A","fields":{}}]}`)
	})
	out := call(t, m, "jira_search", map[string]any{"jql": "x", "limit": 1})
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "truncated") {
		t.Errorf("result must state possible truncation: %s", raw)
	}
}

func TestGetUsesV2AndConvertsDescriptionToMarkdown(t *testing.T) {
	var gotFields string
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/api/2/issue/") {
			t.Errorf("jira_get must use v2 so description is wiki markup, got %s", r.URL.Path)
		}
		gotFields = r.URL.Query().Get("fields")
		_, _ = io.WriteString(w, `{"key":"PROJ-1","fields":{"summary":"S","description":"h3. Objective\n\nDo *the* thing"}}`)
	})
	out := call(t, m, "jira_get", map[string]any{"key": "PROJ-1"})
	// The default set is documented in docs/configuration.md; keep both in step.
	if want := "summary,status,updated,assignee,reporter,description"; gotFields != want {
		t.Errorf("default fields = %q, want %q", gotFields, want)
	}
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
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"issues":[],"isLast":true}`)
	})
	call(t, m, "jira_search", map[string]any{"jql": "x", "fields": []string{"+epic"}})

	fields, _ := body["fields"].([]any)
	names := make([]string, 0, len(fields))
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
		_, _ = io.WriteString(w, `{"key":"PROJ-1","fields":{"customfield_10014":"PROJ-9"}}`)
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
		_, _ = io.WriteString(w, `{"issues":[{"key":"A","fields":{}}],"isLast":true}`)
	})
	out := call(t, m, "jira_search", map[string]any{"jql": "x", "limit": 1})
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "truncated") {
		t.Errorf("isLast=true must not be reported as truncated: %s", raw)
	}
}

func TestSearchNextPageTokenMeansTruncated(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"issues":[{"key":"A","fields":{}}],"isLast":false,"nextPageToken":"tok"}`)
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
		_, _ = io.WriteString(w, `{"key":"PROJ-1","fields":{}}`)
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

// declFor fails loudly when a tool is missing, so a rename cannot make a test
// pass by never running its body.
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

func callErr(t *testing.T, m core.Module, name string, args map[string]any) error {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	_, err = declFor(t, m, name).Handle(context.Background(), raw)
	if err == nil {
		t.Fatalf("%s: expected an error", name)
	}
	return err
}

// The most common runtime path: Jira says no. The upstream message has to
// reach the model, and *core.APIError has to survive the handler.
func TestUpstreamFailurePropagatesAsAPIError(t *testing.T) {
	for _, c := range []struct {
		name   string
		tool   string
		args   map[string]any
		status int
		body   string
		want   string
	}{
		{"search 400", "jira_search", map[string]any{"jql": "bad ~ syntax"}, http.StatusBadRequest,
			`{"errorMessages":["Field 'nope' does not exist"]}`, "does not exist"},
		{"get 404", "jira_get", map[string]any{"key": "PROJ-1"}, http.StatusNotFound,
			`{"errorMessages":["Issue does not exist"]}`, "does not exist"},
		{"get 500", "jira_get", map[string]any{"key": "PROJ-1"}, http.StatusInternalServerError,
			`upstream exploded`, "upstream exploded"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, c.body)
			})
			err := callErr(t, m, c.tool, c.args)

			var apiErr *core.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error %v is not a *core.APIError", err)
			}
			if apiErr.Status != c.status {
				t.Errorf("status = %d, want %d", apiErr.Status, c.status)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not carry the upstream message %q", err, c.want)
			}
		})
	}
}

// The flattening rules, asserted on output rather than on the request. Every
// one of these shapes appears in real Jira responses.
func TestFlattenFieldShapes(t *testing.T) {
	cases := []struct {
		name  string
		field string
		raw   string
		want  any
	}{
		{"named object with key and name", "status", `{"name":"Open","id":"1"}`, "Open"},
		{"named object with both", "resolution", `{"key":"done","name":"Done"}`, "done (Done)"},
		{"person by display name", "assignee", `{"displayName":"Ada L","accountId":"5b1"}`, "Ada L"},
		// displayName is absent whenever the site hides it, which is common.
		// Returning null here would report an assigned issue as unassigned.
		{"person by account id only", "assignee", `{"accountId":"5b10a2"}`, "5b10a2"},
		{"named list", "fixVersions", `[{"name":"1.0"},{"name":"1.1"}]`, []string{"1.0", "1.1"}},
		{"empty named list", "fixVersions", `[]`, []string{}},
		// parent carries no name: the summary lives under fields.summary.
		{"parent keeps its summary", "parent", `{"key":"PROJ-9","fields":{"summary":"The epic"}}`, "PROJ-9 (The epic)"},
		{"parent without summary", "parent", `{"key":"PROJ-9"}`, "PROJ-9"},
		{"ordinary custom field", "customfield_20001", `"plain"`, "plain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"key":"PROJ-1","fields":{"`+c.field+`":`+c.raw+`}}`)
			})
			out := call(t, m, "jira_get", map[string]any{"key": "PROJ-1", "fields": []string{c.field}}).(map[string]any)
			got, ok := out[c.field]
			if !ok {
				t.Fatalf("field %q absent from %v", c.field, out)
			}
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Errorf("%s = %v (%T), want %v", c.field, got, got, c.want)
			}
		})
	}
}

// An explicit null means the field exists and has no value. Skipping it would
// make it indistinguishable from a field Jira never sent, which is what an
// unknown field name produces.
func TestFlattenKeepsExplicitNullsDistinctFromAbsentFields(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"key":"PROJ-1","fields":{"assignee":null,"status":{"name":"Open"}}}`)
	})
	out := call(t, m, "jira_get", map[string]any{"key": "PROJ-1", "fields": []string{"assignee", "status"}}).(map[string]any)

	v, ok := out["assignee"]
	if !ok {
		t.Fatal("an explicitly null field must be present in the output")
	}
	if v != nil {
		t.Errorf("assignee = %v, want null", v)
	}
}

// A shape this code cannot read is still data. Null would assert the field is
// empty; passing the raw value through keeps it.
func TestFlattenPassesUnreadableShapesThrough(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"key":"PROJ-1","fields":{"status":["unexpected","array"]}}`)
	})
	out := call(t, m, "jira_get", map[string]any{"key": "PROJ-1", "fields": []string{"status"}}).(map[string]any)
	got := fmt.Sprint(out["status"])
	if !strings.Contains(got, "unexpected") {
		t.Errorf("status = %v, want the raw value preserved", out["status"])
	}
}

// Jira silently ignores a field name it does not know or the caller cannot
// see, so the result is quietly short. The caller has to be told which name.
func TestUnavailableFieldsAreReported(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"key":"PROJ-1","fields":{"summary":"S"}}`)
	})
	out := call(t, m, "jira_get", map[string]any{"key": "PROJ-1", "fields": []string{"summary", "nosuchfield"}}).(map[string]any)
	missing, ok := out["unavailable_fields"].([]string)
	if !ok {
		t.Fatalf("unavailable_fields absent from %v", out)
	}
	if len(missing) != 1 || missing[0] != "nosuchfield" {
		t.Errorf("unavailable_fields = %v, want [nosuchfield]", missing)
	}
}

// "*all" expands server-side and never comes back under its own name, so it
// must not be reported as a field Jira failed to return.
func TestStarSelectorsAreNotReportedUnavailable(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = io.WriteString(w, `{"issues":[{"key":"PROJ-1","fields":{"summary":"S"}}],"isLast":true}`)
			return
		}
		_, _ = io.WriteString(w, `{"key":"PROJ-1","fields":{"summary":"S"}}`)
	})
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"jira_search", map[string]any{"jql": "x", "fields": []string{"*all"}}},
		{"jira_get", map[string]any{"key": "PROJ-1", "fields": []string{"*navigable"}}},
	} {
		out := call(t, m, tc.tool, tc.args).(map[string]any)
		if missing, ok := out["unavailable_fields"]; ok {
			t.Errorf("%s: unavailable_fields = %v, want none for a star selector", tc.tool, missing)
		}
	}
}

// Search runs on v3, which returns rich text as ADF. Shipping a raw ADF blob
// for the same field jira_get returns as markdown is worse than refusing.
func TestSearchRefusesRichTextFields(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a rich-text field request must not reach the network")
	})
	for _, f := range []string{"description", "environment"} {
		err := callErr(t, m, "jira_search", map[string]any{"jql": "project = X", "fields": []string{"+" + f}})
		if !strings.Contains(err.Error(), "jira_get") {
			t.Errorf("error for %q = %v, want it to point at jira_get", f, err)
		}
	}
}

// The one free-form string on this path must be bounded, like every other
// input in the repository.
func TestSearchBoundsJQLLength(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("an oversized JQL query must not reach the network")
	})
	err := callErr(t, m, "jira_search", map[string]any{"jql": strings.Repeat("a", maxJQLLen+1)})
	if !strings.Contains(err.Error(), "limit is") {
		t.Errorf("error = %v, want it to state the limit", err)
	}
}

// Two truncation shapes the count fallback must not get wrong.
func TestSearchTruncationSignalsArePinned(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"empty result with no signals", `{"issues":[]}`, false},
		{"isLast false means more exist", `{"issues":[{"key":"A","fields":{}}],"isLast":false}`, true},
		{"isLast true is complete", `{"issues":[{"key":"A","fields":{}}],"isLast":true}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, c.body)
			})
			out := call(t, m, "jira_search", map[string]any{"jql": "project = X"}).(map[string]any)
			if _, ok := out["truncated"]; ok != c.want {
				t.Errorf("truncated present = %v, want %v", ok, c.want)
			}
		})
	}
}

// A response omitting `key` would otherwise produce an issue with an empty
// key, when the handler already holds the validated, canonical one.
func TestGetFallsBackToTheValidatedKey(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"fields":{"summary":"S"}}`)
	})
	out := call(t, m, "jira_get", map[string]any{"key": "PROJ-7", "fields": []string{"summary"}}).(map[string]any)
	if out["key"] != "PROJ-7" {
		t.Errorf("key = %v, want the validated PROJ-7", out["key"])
	}
}

// New() exists to report the domain before configuration is loaded. Its
// handlers must refuse rather than panic on a nil client — and they cannot be
// nil, because core.Registry.Register panics on a nil Handle.
func TestDeclarationOnlyModuleHandlersRefuseRatherThanPanic(t *testing.T) {
	m := New()
	for _, d := range m.Tools() {
		if d.Handle == nil {
			t.Fatalf("tool %q has a nil Handle; core.Registry.Register panics on that", d.Name)
		}
	}
	for _, name := range []string{"jira_search", "jira_get"} {
		err := callErr(t, m, name, map[string]any{"jql": "project = X", "key": "PROJ-1"})
		if !strings.Contains(err.Error(), "NewWith") {
			t.Errorf("%s error = %v, want it to name the correct constructor", name, err)
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
