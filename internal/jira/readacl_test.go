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

// newReadRestrictedModule wires a module whose read allowlist names projects.
func newReadRestrictedModule(t *testing.T, h http.HandlerFunc, readProjects ...string) core.Module {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := core.Config{
		BaseURL:      srv.URL,
		Email:        "u@example.com",
		Token:        "test-token-value-1234",
		Domains:      map[string]core.Caps{Domain: {Read: true, Write: true, Destructive: true}},
		ReadProjects: readProjects,
		LimitDefault: 20,
		LimitMax:     50,
		EpicFieldID:  "customfield_10014",
	}
	var logs bytes.Buffer
	return NewWith(cfg, core.NewClient(cfg, core.NewLogger("debug", &logs)))
}

func TestApplyReadProjectFilter(t *testing.T) {
	cases := []struct {
		name     string
		jql      string
		projects []string
		want     string
	}{
		{
			name:     "unrestricted leaves the query untouched",
			jql:      "status = Open",
			projects: nil,
			want:     "status = Open",
		},
		{
			name:     "the allowlist is ANDed on",
			jql:      "status = Open",
			projects: []string{"DEV", "PLATFORM"},
			want:     `(status = Open) AND project IN ("DEV", "PLATFORM")`,
		},
		{
			name:     "an OR naming a forbidden project cannot escape",
			jql:      "project = SECRET OR status = Open",
			projects: []string{"DEV", "PLATFORM"},
			want:     `(project = SECRET OR status = Open) AND project IN ("DEV", "PLATFORM")`,
		},
		{
			name:     "nested JQL stays nested inside the restriction",
			jql:      "(project = SECRET AND status = Open) OR (labels = x OR project = OTHER)",
			projects: []string{"DEV"},
			want:     `((project = SECRET AND status = Open) OR (labels = x OR project = OTHER)) AND project IN ("DEV")`,
		},
		{
			name:     "an existing project clause is restricted anyway",
			jql:      "project IN (DEV, SECRET)",
			projects: []string{"DEV"},
			want:     `(project IN (DEV, SECRET)) AND project IN ("DEV")`,
		},
		{
			name:     "a sort clause survives",
			jql:      "status = Open ORDER BY created DESC",
			projects: []string{"DEV"},
			want:     `(status = Open) AND project IN ("DEV") ORDER BY created DESC`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := applyReadProjectFilter(c.jql, c.projects)
			if err != nil {
				t.Fatalf("applyReadProjectFilter: %v", err)
			}
			if got != c.want {
				t.Errorf("applyReadProjectFilter = %q, want %q", got, c.want)
			}
		})
	}
}

// An unbalanced JQL is the one shape the wrapping parentheses cannot restrict,
// so the search is refused rather than sent. Without this,
// `status = Open) OR (status != Open` would compose into a disjunction whose
// first half has no project restriction, because AND binds tighter than OR.
func TestSearchRefusesJQLItCannotRestrict(t *testing.T) {
	for _, jql := range []string{
		"status = Open) OR (status != Open",
		"(status = Open",
		`summary ~ "unterminated`,
	} {
		sent := false
		m := newReadRestrictedModule(t, func(w http.ResponseWriter, r *http.Request) {
			sent = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"issues":[],"isLast":true}`)
		}, "DEV")
		if err := callErr(t, m, "jira_search", map[string]any{"jql": jql}); err == nil {
			t.Errorf("jira_search(%q) = nil error, want refusal", jql)
		}
		if sent {
			t.Errorf("jira_search(%q) reached Jira; it must be refused before any request", jql)
		}
	}
}

// The same queries are sent untouched with no allowlist in force: the refusal
// exists to protect the restriction, not to police JQL.
func TestSearchSendsUnbalancedJQLWhenUnrestricted(t *testing.T) {
	var sent string
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in struct {
			JQL string `json:"jql"`
		}
		_ = json.Unmarshal(body, &in)
		sent = in.JQL
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"issues":[],"isLast":true}`)
	})
	const jql = "status = Open) OR (status != Open"
	call(t, m, "jira_search", map[string]any{"jql": jql})
	if sent != jql {
		t.Errorf("JQL sent = %q, want it unchanged", sent)
	}
}

// A Confluence personal space key passes core's key pattern but is not a Jira
// project key, so it must never end up in a JQL security clause.
func TestApplyReadProjectFilterRejectsNonProjectKey(t *testing.T) {
	for _, key := range []string{"~alice", "1DEV", `DEV"`} {
		if _, err := applyReadProjectFilter("status = Open", []string{key}); err == nil {
			t.Errorf("applyReadProjectFilter with %q = nil error, want refusal", key)
		}
	}
}

func TestSearchSendsRestrictedJQL(t *testing.T) {
	for _, c := range []struct {
		name string
		jql  string
		want string
	}{
		{
			name: "plain condition",
			jql:  "status = Open",
			want: `(status = Open) AND project IN ("DEV", "PLATFORM")`,
		},
		{
			name: "attempted bypass with OR",
			jql:  "project = SECRET OR status = Open",
			want: `(project = SECRET OR status = Open) AND project IN ("DEV", "PLATFORM")`,
		},
		{
			name: "attempted bypass with a nested expression and a sort",
			jql:  "(project = SECRET) OR (status = Open) ORDER BY created",
			want: `((project = SECRET) OR (status = Open)) AND project IN ("DEV", "PLATFORM") ORDER BY created`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			var sent string
			m := newReadRestrictedModule(t, func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				var in struct {
					JQL string `json:"jql"`
				}
				if err := json.Unmarshal(body, &in); err != nil {
					t.Fatalf("unmarshal body: %v", err)
				}
				sent = in.JQL
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"issues":[],"isLast":true}`)
			}, "DEV", "PLATFORM")
			call(t, m, "jira_search", map[string]any{"jql": c.jql})
			if sent != c.want {
				t.Errorf("JQL sent = %q, want %q", sent, c.want)
			}
		})
	}
}

// The injected clause narrows the query; it does not decide what may be shown.
// JQL's `project` field resolves a value by key, by NAME or by id, so
// `project IN ("DEV")` also matches a project merely named DEV — and the
// allowlist is a list of keys. Every returned issue is re-checked against the
// project it reports, the way jira_get checks the one it fetched.
func TestSearchDropsIssuesOutsideTheReadAllowlist(t *testing.T) {
	var sentFields string
	m := newReadRestrictedModule(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var in struct {
			Fields []string `json:"fields"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		sentFields = strings.Join(in.Fields, ",")
		w.Header().Set("Content-Type", "application/json")
		// SECRET-1 came back because the project SECRET is *named* DEV.
		_, _ = io.WriteString(w, `{"issues":[
			{"key":"DEV-1","fields":{"summary":"ok","project":{"key":"DEV"}}},
			{"key":"SECRET-1","fields":{"summary":"leaked","project":{"key":"SECRET"}}},
			{"key":"NOPROJ-1","fields":{"summary":"unknown"}}
		],"isLast":true}`)
	}, "DEV")

	out, ok := call(t, m, "jira_search", map[string]any{"jql": "status = Open"}).(map[string]any)
	if !ok {
		t.Fatal("jira_search did not return a map")
	}
	if !strings.Contains(sentFields, fieldProject) {
		t.Errorf("fields sent = %q, want the project the check needs", sentFields)
	}
	issues, ok := out["issues"].([]map[string]any)
	if !ok {
		t.Fatalf("issues = %T, want []map[string]any", out["issues"])
	}
	if len(issues) != 1 {
		t.Fatalf("returned %d issues, want only the allowlisted one: %v", len(issues), issues)
	}
	if issues[0][fieldKey] != "DEV-1" {
		t.Errorf("kept %v, want DEV-1", issues[0][fieldKey])
	}
	// Fetched for the check alone, so it is not part of the answer.
	if _, present := issues[0][fieldProject]; present {
		t.Errorf("result carries %q, which the caller did not ask for", fieldProject)
	}
}

// Off the allowlist nothing extra is fetched and nothing is dropped: the
// re-check must cost nothing in the unrestricted deployment.
func TestSearchAsksForNoExtraFieldWhenUnrestricted(t *testing.T) {
	var sentFields string
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in struct {
			Fields []string `json:"fields"`
		}
		_ = json.Unmarshal(body, &in)
		sentFields = strings.Join(in.Fields, ",")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"issues":[{"key":"SECRET-1","fields":{"summary":"fine"}}],"isLast":true}`)
	})

	out, ok := call(t, m, "jira_search", map[string]any{"jql": "status = Open"}).(map[string]any)
	if !ok {
		t.Fatal("jira_search did not return a map")
	}
	if strings.Contains(sentFields, fieldProject) {
		t.Errorf("fields sent = %q, want no project fetched when unrestricted", sentFields)
	}
	if issues, _ := out["issues"].([]map[string]any); len(issues) != 1 {
		t.Errorf("returned %d issues, want 1", len(issues))
	}
}

func TestSearchLeavesJQLAloneWhenUnrestricted(t *testing.T) {
	var sent string
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			JQL string `json:"jql"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &in)
		sent = in.JQL
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"issues":[],"isLast":true}`)
	})
	call(t, m, "jira_search", map[string]any{"jql": "project = SECRET"})
	if sent != "project = SECRET" {
		t.Errorf("JQL sent = %q, want it unchanged", sent)
	}
}

// issueHandler serves an issue in one response, recording every path and query
// it was asked for. The project travels with the content, which is what the
// authorization decision is made on.
func issueHandler(t *testing.T, project, summary string, requests *[]string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		*requests = append(*requests, r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"key":"DEV-123","fields":{"summary":"`+summary+
			`","project":{"key":"`+project+`"}}}`)
	}
}

func TestGetEnforcesCurrentProjectNotKeyPrefix(t *testing.T) {
	const secretSummary = "quarterly layoffs"
	cases := []struct {
		name    string
		key     string
		project string
		allowed bool
	}{
		{name: "key prefix and current project agree", key: "DEV-123", project: "DEV", allowed: true},
		{name: "another allowed project", key: "PLATFORM-123", project: "PLATFORM", allowed: true},
		// The moved-issue case: DEV-123 is still a working alias for an issue
		// that now lives in SECRET, so a prefix check would have allowed it.
		{name: "moved out of an allowed project", key: "DEV-123", project: "SECRET", allowed: false},
		{name: "key prefix is not allowlisted either", key: "SECRET-1", project: "SECRET", allowed: false},
		// The reverse move: a key from a forbidden project whose issue now
		// lives in an allowed one is readable, because where it lives is what
		// decides.
		{name: "moved into an allowed project", key: "SECRET-1", project: "DEV", allowed: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var requests []string
			m := newReadRestrictedModule(t, issueHandler(t, c.project, secretSummary, &requests), "DEV", "PLATFORM")
			if c.allowed {
				out := call(t, m, "jira_get", map[string]any{"key": c.key})
				got, ok := out.(map[string]any)
				if !ok {
					t.Fatalf("jira_get returned %T, want a map", out)
				}
				if got[fieldSummary] != secretSummary {
					t.Errorf("summary = %v, want %q", got[fieldSummary], secretSummary)
				}
				return
			}
			err := callErr(t, m, "jira_get", map[string]any{"key": c.key})
			if !strings.Contains(err.Error(), "access denied") || !strings.Contains(err.Error(), "ATLAS_READ_PROJECTS") {
				t.Errorf("error = %q, want an access-denied message naming ATLAS_READ_PROJECTS", err)
			}
			// The refusal must carry none of the issue, and the project it
			// turned out to live in is not the caller's to learn either.
			if strings.Contains(err.Error(), secretSummary) {
				t.Errorf("error %q leaks issue data", err)
			}
			// The project the issue turned out to live in is not the caller's
			// to learn — except where the caller's own key already named it.
			if !strings.Contains(c.key, c.project) && strings.Contains(err.Error(), c.project) {
				t.Errorf("error %q discloses the project the issue lives in", err)
			}
			// One request, which is what makes the decision race-free: the
			// project checked and the content refused came from the same
			// response, so no move in between can be missed.
			if len(requests) != 1 {
				t.Errorf("requests = %v, want exactly one", requests)
			}
		})
	}
}

// The project is fetched for the check alone and is not part of the answer,
// unless the caller asked for it.
func TestGetReturnsTheProjectOnlyWhenAsked(t *testing.T) {
	var requests []string
	m := newReadRestrictedModule(t, issueHandler(t, "DEV", "s", &requests), "DEV")

	out, ok := call(t, m, "jira_get", map[string]any{"key": "DEV-123"}).(map[string]any)
	if !ok {
		t.Fatal("jira_get did not return a map")
	}
	if _, present := out[fieldProject]; present {
		t.Errorf("result carries %q, which the caller did not ask for", fieldProject)
	}
	if !strings.Contains(requests[0], "project") {
		t.Errorf("request %q does not fetch the project the check needs", requests[0])
	}

	out, ok = call(t, m, "jira_get", map[string]any{"key": "DEV-123", "fields": []string{"+project"}}).(map[string]any)
	if !ok {
		t.Fatal("jira_get did not return a map")
	}
	if _, present := out[fieldProject]; !present {
		t.Errorf("result omits %q, which the caller asked for", fieldProject)
	}
}

func TestGetAsksForNoExtraFieldWhenUnrestricted(t *testing.T) {
	var requests []string
	m := newTestModule(t, issueHandler(t, "SECRET", "body", &requests))
	call(t, m, "jira_get", map[string]any{"key": "DEV-123"})
	if len(requests) != 1 || strings.Contains(requests[0], "project") {
		t.Errorf("requests = %v, want one fetch with no allowlist field added", requests)
	}
}

// An issue whose project cannot be resolved fails closed.
func TestGetRefusesWhenProjectCannotBeResolved(t *testing.T) {
	m := newReadRestrictedModule(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"key":"DEV-1","fields":{}}`)
	}, "DEV")
	if err := callErr(t, m, "jira_get", map[string]any{"key": "DEV-1"}); err == nil {
		t.Error("expected a refusal when the project is unknown")
	}
}

// A permitted issue may link to issues in projects the allowlist excludes.
// Their keys survive — the caller needs those to ask jira_get, which decides
// for itself — but their text does not.
func TestGetDropsTextOfLinkedIssuesUnderAReadAllowlist(t *testing.T) {
	const forbiddenSummary = "acquisition of Initech"
	issue := `{"key":"DEV-1","fields":{
		"summary":"own summary",
		"project":{"key":"DEV"},
		"parent":{"key":"SECRET-9","fields":{"summary":"` + forbiddenSummary + `"}},
		"issuelinks":[{"type":{"name":"blocks"},"inwardIssue":{"key":"SECRET-8","fields":{"summary":"` + forbiddenSummary + `"}}}],
		"subtasks":[{"key":"SECRET-7","fields":{"summary":"` + forbiddenSummary + `"}}]}}`
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, issue)
	}

	m := newReadRestrictedModule(t, handler, "DEV")
	out := call(t, m, "jira_get", map[string]any{"key": "DEV-1", "fields": []string{"*all"}})
	rendered, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if strings.Contains(string(rendered), forbiddenSummary) {
		t.Errorf("result carries the summary of a linked issue outside the read allowlist: %s", rendered)
	}
	for _, key := range []string{"SECRET-9", "SECRET-8", "SECRET-7"} {
		if !strings.Contains(string(rendered), key) {
			t.Errorf("result dropped the key %q; only the linked issues' text should go", key)
		}
	}
	if !strings.Contains(string(rendered), "own summary") {
		t.Error("result dropped the summary of the issue the caller actually asked for")
	}

	// Unrestricted, the same response keeps everything: the scrub exists to
	// serve the allowlist, not to trim results in general.
	unrestricted := newTestModule(t, handler)
	rendered, err = json.Marshal(call(t, unrestricted, "jira_get", map[string]any{"key": "DEV-1", "fields": []string{"*all"}}))
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if !strings.Contains(string(rendered), forbiddenSummary) {
		t.Errorf("with no allowlist the linked summaries must survive: %s", rendered)
	}
}

// A write the write allowlist permits must not become a way to read workflow
// metadata about a project the read allowlist excludes.
func TestTransitionWithholdsCandidatesOutsideTheReadAllowlist(t *testing.T) {
	const transitionName = "Escalate to legal"
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Query().Get("fields") == "project":
			_, _ = io.WriteString(w, `{"fields":{"project":{"key":"SECRET"}}}`)
		case strings.HasSuffix(r.URL.Path, "/transitions"):
			_, _ = io.WriteString(w, `{"transitions":[{"id":"5","name":"`+transitionName+`","to":{"name":"Legal"}}]}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}
	m := newReadRestrictedModule(t, handler, "DEV")
	err := callErr(t, m, "jira_transition", map[string]any{"key": "SECRET-1", "status": "Done"})
	if strings.Contains(err.Error(), transitionName) {
		t.Errorf("error %q discloses workflow metadata of a project outside the read allowlist", err)
	}
}

// How many versions of a project exist whose names differ from the caller's
// only by case is project metadata, so the ambiguous-match refusal follows the
// same read gate as the "no such version" one below it. The caller still
// learns its own value was not exact, which is what it needs to fix the call.
func TestVersionAmbiguityWithholdsTheCountOutsideTheReadAllowlist(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"id":"111","name":"Release"},{"id":"222","name":"RELEASE"}]`)
	}

	m := newReadRestrictedModule(t, handler, "DEV").(module)
	_, err := m.versionIDFor(context.Background(), "SECRET", "release")
	if err == nil {
		t.Fatal("an ambiguous version must error")
	}
	if strings.Contains(err.Error(), "2 versions") {
		t.Errorf("error = %v, must not count versions in a project outside the read allowlist", err)
	}
	if !strings.Contains(err.Error(), "exact") {
		t.Errorf("error = %v, want it to say an exact name is needed", err)
	}

	// Inside the allowlist the count is the caller's own to see.
	m = newReadRestrictedModule(t, handler, "DEV").(module)
	if _, err = m.versionIDFor(context.Background(), "DEV", "release"); err == nil {
		t.Fatal("an ambiguous version must error")
	} else if !strings.Contains(err.Error(), "2 versions") {
		t.Errorf("error = %v, want the count for a readable project", err)
	}
}
