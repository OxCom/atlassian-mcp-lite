package confluence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
		_, _ = io.WriteString(w, `{"results":[{"content":{"id":"123","type":"page"},"title":"T"}],"size":1}`)
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
		_, _ = io.WriteString(w, `{"results":[]}`)
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
		_, _ = io.WriteString(w, `{"id":"123","title":"T","spaceId":"9","version":{"number":4},"body":{"view":{"value":"<h2>Title</h2><p>Some <strong>bold</strong>.</p>"}}}`)
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
		_, _ = io.WriteString(w, `{"id":"123","title":"T","version":{"number":7},"body":{"view":{"value":"<p>x</p>"}}}`)
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
		_, _ = io.WriteString(w, `{"id":"123","title":"T","spaceId":"9","version":{"number":4},"body":{"view":{"value":"<p>x</p>"}}}`)
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

// callErr runs a tool expecting failure, and returns the error.
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

// The most common runtime path of all: Atlassian says no. The model needs the
// upstream message, and the caller needs *core.APIError to survive the handler.
func TestUpstreamFailurePropagatesAsAPIError(t *testing.T) {
	for _, c := range []struct {
		name   string
		tool   string
		args   map[string]any
		status int
		body   string
	}{
		{"search 400", "confluence_search", map[string]any{"cql": "type=page"}, http.StatusBadRequest,
			`{"message":"Could not parse cql"}`},
		{"get_page 404", "confluence_get_page", map[string]any{"id": "123"}, http.StatusNotFound,
			`{"message":"No page with id 123"}`},
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
			if !strings.Contains(err.Error(), "cql") && !strings.Contains(err.Error(), "id 123") {
				t.Errorf("error %q does not carry the upstream message", err)
			}
		})
	}
}

// An omitted limit must send the configured default, not zero.
func TestSearchOmittedLimitSendsDefault(t *testing.T) {
	var got string
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("limit")
		_, _ = io.WriteString(w, `{"results":[],"totalSize":0}`)
	})
	call(t, m, "confluence_search", map[string]any{"cql": "type=page"})
	if got != "20" {
		t.Errorf("limit = %q, want the configured default 20", got)
	}
}

// Three truncation shapes, because the wrong answer in either direction
// misleads the model: claiming everything arrived when it did not, or claiming
// a total the response never gave.
func TestSearchTruncationSignals(t *testing.T) {
	hit := `{"title":"T","content":{"id":"1","type":"page"}}`
	cases := []struct {
		name       string
		body       string
		wantNote   bool
		wantAbsent string
	}{
		{"totalSize larger than returned", `{"results":[` + hit + `],"totalSize":100}`, true, ""},
		{"next link with no total", `{"results":[` + hit + `],"_links":{"next":"/x"}}`, true, "of 1 matches"},
		{"complete small result", `{"results":[` + hit + `],"totalSize":1}`, false, ""},
		{"no signals at all", `{"results":[` + hit + `]}`, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, c.body)
			})
			out := call(t, m, "confluence_search", map[string]any{"cql": "type=page"}).(map[string]any)
			note, ok := out["truncated"].(string)
			if ok != c.wantNote {
				t.Fatalf("truncated present = %v, want %v (note %q)", ok, c.wantNote, note)
			}
			if c.wantAbsent != "" && strings.Contains(note, c.wantAbsent) {
				t.Errorf("note %q must not claim a total it was never given", note)
			}
		})
	}
}

// A full page with no pagination signal at all cannot be distinguished from a
// truncated one by counting, so it is reported as possibly truncated.
func TestSearchFullPageWithoutSignalsIsReportedTruncated(t *testing.T) {
	hits := make([]string, 5)
	for i := range hits {
		hits[i] = `{"title":"T","content":{"id":"1","type":"page"}}`
	}
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":[`+strings.Join(hits, ",")+`]}`)
	})
	out := call(t, m, "confluence_search", map[string]any{"cql": "type=page", "limit": 5}).(map[string]any)
	if _, ok := out["truncated"]; !ok {
		t.Errorf("a full page with no signals must be reported as possibly truncated: %v", out)
	}
}

// CQL matches spaces and users too, and those results carry no content object.
// Emitting empty id and type invites the model to call get_page with "".
func TestSearchNonContentResultOmitsEmptyKeys(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":[{"title":"A Space","entityType":"space"}],"totalSize":1}`)
	})
	out := call(t, m, "confluence_search", map[string]any{"cql": "type=space"}).(map[string]any)
	row := out["results"].([]map[string]any)[0]
	if _, ok := row["id"]; ok {
		t.Errorf("a space result must not carry an id key: %v", row)
	}
	if row["entityType"] != "space" {
		t.Errorf("entityType = %v, want space so the model knows what this row is", row["entityType"])
	}
	if row["title"] != "A Space" {
		t.Errorf("title = %v", row["title"])
	}
}

// The v1 search endpoint wraps the top-level title in highlight markers when
// the CQL contains a text match. They are not part of the page's name.
func TestSearchTitlePrefersContentAndStripsHighlights(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":[
			{"title":"@@@hl@@@Release@@@endhl@@@ notes","content":{"id":"1","type":"page","title":"Release notes"}},
			{"title":"@@@hl@@@Only@@@endhl@@@ top","entityType":"space"}
		],"totalSize":2}`)
	})
	out := call(t, m, "confluence_search", map[string]any{"cql": "text ~ release"}).(map[string]any)
	rows := out["results"].([]map[string]any)
	if got := rows[0]["title"]; got != "Release notes" {
		t.Errorf("title = %v, want the content object's clean title", got)
	}
	if got := rows[1]["title"]; got != "Only top" {
		t.Errorf("title = %v, want the markers stripped", got)
	}
}

// An unknown field name is a typo, and the caller has to be told. Filtering it
// out silently returns a short result with no explanation.
func TestGetPageRejectsUnknownField(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request must be made for an unknown field")
	})
	for _, f := range []string{"space", "banana"} {
		err := callErr(t, m, "confluence_get_page", map[string]any{"id": "123", "fields": []string{f}})
		if !strings.Contains(err.Error(), "unknown field") {
			t.Errorf("error for %q = %v, want it to name the problem", f, err)
		}
		if !strings.Contains(err.Error(), "spaceId") {
			t.Errorf("error for %q = %v, want it to list the valid fields", f, err)
		}
	}
}

// version 0 is exactly what would be handed to the update tool's optimistic
// lock, so a response without a usable version has to fail here.
func TestGetPageRequiresAUsableVersion(t *testing.T) {
	for _, body := range []string{
		`{"id":"123","title":"T"}`,
		`{"id":"123","title":"T","version":null}`,
		`{"id":"123","title":"T","version":{"number":0}}`,
	} {
		m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		})
		err := callErr(t, m, "confluence_get_page", map[string]any{"id": "123"})
		if !strings.Contains(err.Error(), "version") {
			t.Errorf("body %s: error = %v, want it to name the version", body, err)
		}
	}
}

// The version number is what the update path locks on, so the request has to
// ask for it rather than hope it is a default.
func TestGetPageRequestsVersionExplicitly(t *testing.T) {
	var got string
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("include-version")
		_, _ = io.WriteString(w, `{"id":"123","title":"T","version":{"number":3}}`)
	})
	call(t, m, "confluence_get_page", map[string]any{"id": "123"})
	if got != "true" {
		t.Errorf("include-version = %q, want true", got)
	}
}

// The one free-form string on this path must be bounded, like every other
// input in the repository.
func TestSearchBoundsCQLLength(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("an oversized CQL query must not reach the network")
	})
	err := callErr(t, m, "confluence_search", map[string]any{"cql": strings.Repeat("a", maxCQLLen+1)})
	if !strings.Contains(err.Error(), "limit is") {
		t.Errorf("error = %v, want it to state the limit", err)
	}
}
