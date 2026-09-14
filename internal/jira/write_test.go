package jira

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
)

func TestCommentConvertsMarkdownToWiki(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/2/issue/PROJ-1/comment" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"id":"10001"}`)
	})

	out, _ := call(t, m, "jira_comment", map[string]any{"key": "proj-1", "body": "See `x` and **y**"}).(map[string]any)

	got, _ := body["body"].(string)
	if !strings.Contains(got, "{{x}}") || !strings.Contains(got, "*y*") {
		t.Errorf("body not converted to wiki markup: %q", got)
	}
	if out["key"] != "PROJ-1" {
		t.Errorf("key = %v, want the canonicalised PROJ-1", out["key"])
	}
	if out["commentId"] != "10001" {
		t.Errorf("commentId = %v, want the id the server returned", out["commentId"])
	}
}

func TestCommentRequiresNonEmptyBody(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for an empty comment")
	})
	for name, body := range map[string]string{
		"empty":      "",
		"whitespace": "   ",
		// Not ASCII space, and not caught by a naive == "" check.
		"non-breaking space": " ",
	} {
		raw, _ := json.Marshal(map[string]any{"key": "PROJ-1", "body": body})
		if _, err := declFor(t, m, "jira_comment").Handle(context.Background(), raw); err == nil {
			t.Errorf("%s: an empty comment body must error", name)
		}
	}
}

func TestCommentBoundsBody(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for an over-long comment")
	})
	if err := callErr(t, m, "jira_comment", map[string]any{
		"key": "PROJ-1", "body": strings.Repeat("b", maxBodyLen+1),
	}); err == nil {
		t.Error("an unbounded comment body must be refused")
	}
}

func TestCommentRefusedByAllowlist(t *testing.T) {
	base := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("refusal must happen before any request")
	}).(module)
	base.cfg.WriteProjects = []string{"OTHER"}

	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1", "body": "hi"})
	if _, err := declFor(t, base, "jira_comment").Handle(context.Background(), raw); err == nil {
		t.Fatal("comment outside the allowlist must be refused")
	}
}

// Jira keeps every past key working after an issue moves, so an allowlisted
// prefix is not proof of the project a comment would actually land in.
func TestCommentRefusesIssueMovedOutOfAnAllowlistedProject(t *testing.T) {
	var wrote bool
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"fields":{"project":{"key":"SECRET"}}}`)
			return
		}
		wrote = true
		_, _ = io.WriteString(w, `{"id":"1"}`)
	}).(module)
	base.cfg.WriteProjects = []string{"PROJ"}

	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1", "body": "hi"})
	_, err := declFor(t, base, "jira_comment").Handle(context.Background(), raw)
	if err == nil {
		t.Fatal("a moved issue outside the allowlist must be refused")
	}
	if !strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error = %v, want it to name the project the issue actually lives in", err)
	}
	if wrote {
		t.Error("nothing may be written once the allowlist check fails")
	}
}

func TestCommentSkipsProjectVerificationWhenUnrestricted(t *testing.T) {
	var gets int
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets++
		}
		_, _ = io.WriteString(w, `{"id":"1"}`)
	}).(module)
	base.cfg.WriteProjects = nil

	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1", "body": "hi"})
	if _, err := declFor(t, base, "jira_comment").Handle(context.Background(), raw); err != nil {
		t.Fatalf("jira_comment: %v", err)
	}
	if gets != 0 {
		t.Errorf("made %d GETs, want none when no allowlist is configured", gets)
	}
}

// The plan's fixture gave every transition the same name as its target status,
// so it passed whether the code resolved by name, by target, or by only one of
// them — and it could not surface the collision the resolver now guards. These
// fixtures keep names and targets distinct on purpose.
func TestTransitionResolvesStatusNameToID(t *testing.T) {
	var posted map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path != "/rest/api/3/issue/PROJ-1/transitions" {
				t.Errorf("path = %s", r.URL.Path)
			}
			_, _ = io.WriteString(w, `{"transitions":[
				{"id":"51","name":"Finish work","to":{"name":"Done"}},
				{"id":"171","name":"Pause","to":{"name":"On Hold"}}]}`)
		case http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&posted)
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// Resolved by TARGET STATUS: no transition is named "done".
	out, _ := call(t, m, "jira_transition", map[string]any{"key": "PROJ-1", "status": "done"}).(map[string]any)

	tr, _ := posted["transition"].(map[string]any)
	if tr["id"] != "51" {
		t.Errorf("transition = %v, want id 51 resolved from the target status", posted["transition"])
	}
	if out["status"] != "Done" || out["transitionId"] != "51" {
		t.Errorf("result = %v, want the status and id it moved to", out)
	}
}

// Resolved by TRANSITION NAME, where the name matches nothing else.
func TestTransitionResolvesTransitionNameToID(t *testing.T) {
	var posted map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"transitions":[
				{"id":"51","name":"Finish work","to":{"name":"Done"}},
				{"id":"171","name":"Pause","to":{"name":"On Hold"}}]}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&posted)
		w.WriteHeader(http.StatusNoContent)
	})
	call(t, m, "jira_transition", map[string]any{"key": "PROJ-1", "status": "Finish work"})
	tr, _ := posted["transition"].(map[string]any)
	if tr["id"] != "51" {
		t.Errorf("transition = %v, want id 51 resolved from the transition name", posted["transition"])
	}
}

// The measured bug: a transition NAMED "Done" that moves an issue to Closed,
// alongside one that TARGETS Done. A priority rule picked the name and moved
// the issue to the wrong status silently. It must refuse instead.
func TestTransitionRefusesWhenNameAndTargetDisagree(t *testing.T) {
	var posted bool
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"transitions":[
				{"id":"11","name":"Done","to":{"name":"Closed"}},
				{"id":"22","name":"Resolve","to":{"name":"Done"}}]}`)
			return
		}
		posted = true
		w.WriteHeader(http.StatusNoContent)
	})

	err := callErr(t, m, "jira_transition", map[string]any{"key": "PROJ-1", "status": "Done"})
	if posted {
		t.Fatal("an ambiguous transition must not be executed; a transition is not undoable")
	}
	for _, want := range []string{"matches 2 transitions", "id 11", "id 22"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to contain %q so the caller can pick", err, want)
		}
	}
}

// Two transitions sharing a name is ambiguous too, and must not fall through
// to some third transition that happens to match on target.
func TestTransitionRefusesDuplicateNames(t *testing.T) {
	var posted bool
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"transitions":[
				{"id":"1","name":"Dup","to":{"name":"A"}},
				{"id":"2","name":"Dup","to":{"name":"B"}},
				{"id":"3","name":"Go","to":{"name":"Dup"}}]}`)
			return
		}
		posted = true
		w.WriteHeader(http.StatusNoContent)
	})

	if err := callErr(t, m, "jira_transition", map[string]any{"key": "PROJ-1", "status": "Dup"}); !strings.Contains(err.Error(), "matches 3 transitions") {
		t.Errorf("error = %v, want every match counted", err)
	}
	if posted {
		t.Fatal("an ambiguous transition must not be executed")
	}
}

// An id is exact, so it stays unambiguous even when it also equals nothing
// else — but it must still be the same transition the id names.
func TestTransitionByIDIsExact(t *testing.T) {
	var posted map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"transitions":[
				{"id":"11","name":"Done","to":{"name":"Closed"}},
				{"id":"22","name":"Resolve","to":{"name":"Done"}}]}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&posted)
		w.WriteHeader(http.StatusNoContent)
	})
	call(t, m, "jira_transition", map[string]any{"key": "PROJ-1", "status": "22"})
	tr, _ := posted["transition"].(map[string]any)
	if tr["id"] != "22" {
		t.Errorf("transition = %v, want the id the caller named", posted["transition"])
	}
}

func TestTransitionUnknownStatusListsAvailable(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"transitions":[{"id":"51","name":"Done","to":{"name":"Done"}}]}`)
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

// An issue whose workflow offers nothing from its current status must say so,
// rather than reporting an empty list of candidates.
func TestTransitionWithNoAvailableTransitions(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"transitions":[]}`)
	})
	err := callErr(t, m, "jira_transition", map[string]any{"key": "PROJ-1", "status": "Done"})
	if !strings.Contains(err.Error(), "no transitions") {
		t.Errorf("error = %v, want it to say the issue offers no transitions", err)
	}
}

func TestTransitionNeverHardcodesIDs(t *testing.T) {
	// The transitions endpoint must always be consulted; ids are workflow-specific.
	consulted := false
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			consulted = true
			_, _ = io.WriteString(w, `{"transitions":[{"id":"999","name":"Done","to":{"name":"Done"}}]}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	out, _ := call(t, m, "jira_transition", map[string]any{"key": "PROJ-1", "status": "Done"}).(map[string]any)
	if !consulted {
		t.Fatal("transitions endpoint must be consulted rather than assuming an id")
	}
	if out["transitionId"] != "999" {
		t.Errorf("transitionId = %v, want the id the workflow actually reported", out["transitionId"])
	}
}

func TestTransitionAcceptsNumericIDDirectly(t *testing.T) {
	var posted map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"transitions":[{"id":"51","name":"Done","to":{"name":"Done"}}]}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&posted)
		w.WriteHeader(http.StatusNoContent)
	})
	call(t, m, "jira_transition", map[string]any{"key": "PROJ-1", "status": "51"})
	tr, _ := posted["transition"].(map[string]any)
	if tr["id"] != "51" {
		t.Errorf("transition = %v", posted["transition"])
	}
}

// The id in a transition body is a string. Declaring it as a JSON number would
// lose precision above 2^53, and Jira's own responses type it as a string.
func TestTransitionSendsIDAsString(t *testing.T) {
	var raw map[string]json.RawMessage
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"transitions":[{"id":"51","name":"Done","to":{"name":"Done"}}]}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&raw)
		w.WriteHeader(http.StatusNoContent)
	})
	call(t, m, "jira_transition", map[string]any{"key": "PROJ-1", "status": "Done"})
	if got := string(raw["transition"]); !strings.Contains(got, `"id":"51"`) {
		t.Errorf("transition body = %s, want the id as a JSON string", got)
	}
}

func TestTransitionRequiresNonEmptyStatus(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for an empty status")
	})
	if err := callErr(t, m, "jira_transition", map[string]any{"key": "PROJ-1", "status": "  "}); err == nil {
		t.Error("an empty status must error")
	}
	if err := callErr(t, m, "jira_transition", map[string]any{
		"key": "PROJ-1", "status": strings.Repeat("s", maxNameLen+1),
	}); err == nil {
		t.Error("an unbounded status must be refused")
	}
}

func TestWriteToolsRejectMalformedKeys(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for a malformed key")
	})
	if err := callErr(t, m, "jira_comment", map[string]any{"key": "../../secret", "body": "hi"}); err == nil {
		t.Error("jira_comment: a malformed key must be refused")
	}
	if err := callErr(t, m, "jira_transition", map[string]any{"key": "../../secret", "status": "Done"}); err == nil {
		t.Error("jira_transition: a malformed key must be refused")
	}
}

func TestTransitionRefusedByAllowlist(t *testing.T) {
	base := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("refusal must happen before any request")
	}).(module)
	base.cfg.WriteProjects = []string{"OTHER"}

	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1", "status": "Done"})
	if _, err := declFor(t, base, "jira_transition").Handle(context.Background(), raw); err == nil {
		t.Fatal("a transition outside the allowlist must be refused")
	}
}

func TestTransitionRefusesIssueMovedOutOfAnAllowlistedProject(t *testing.T) {
	var moved bool
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/transitions") {
			if r.Method == http.MethodPost {
				moved = true
			}
			_, _ = io.WriteString(w, `{"transitions":[{"id":"51","name":"Done","to":{"name":"Done"}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"fields":{"project":{"key":"SECRET"}}}`)
	}).(module)
	base.cfg.WriteProjects = []string{"PROJ"}

	raw, _ := json.Marshal(map[string]any{"key": "PROJ-1", "status": "Done"})
	_, err := declFor(t, base, "jira_transition").Handle(context.Background(), raw)
	if err == nil {
		t.Fatal("a moved issue outside the allowlist must be refused")
	}
	if !strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error = %v, want it to name the project the issue actually lives in", err)
	}
	if moved {
		t.Error("nothing may be transitioned once the allowlist check fails")
	}
}

// The schema hides a tool whose class is disabled and the SDK validates against
// it, but the handler is where the write happens, so it re-checks.
func TestWriteHandlersRecheckCapabilities(t *testing.T) {
	base := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made when the capability is disabled")
	}).(module)

	base.cfg.Domains = map[string]core.Caps{Domain: {Read: true, Destructive: true}}
	if err := callErr(t, base, "jira_comment", map[string]any{"key": "PROJ-1", "body": "hi"}); err == nil {
		t.Error("jira_comment must be refused when write is disabled")
	}

	base.cfg.Domains = map[string]core.Caps{Domain: {Read: true, Write: true}}
	if err := callErr(t, base, "jira_transition", map[string]any{"key": "PROJ-1", "status": "Done"}); err == nil {
		t.Error("jira_transition must be refused when destructive is disabled")
	}
}

// A declaration-only module has no client. Calling it must say so rather than
// panic on a nil dereference.
func TestWriteHandlersGuardAgainstNilClient(t *testing.T) {
	m := New()
	for name, args := range map[string]map[string]any{
		"jira_comment":    {"key": "PROJ-1", "body": "hi"},
		"jira_transition": {"key": "PROJ-1", "status": "Done"},
		"jira_create":     {"project": "PROJ", "issuetype": "Task", "summary": "s"},
	} {
		err := callErr(t, m, name, args)
		if !strings.Contains(err.Error(), "NewWith") {
			t.Errorf("%s error = %v, want it to name NewWith", name, err)
		}
	}
}

// Jira answers a transition POST with 204 and no body; a success must not
// depend on decoding one.
func TestTransitionAcceptsEmptyResponseBody(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"transitions":[{"id":"51","name":"Done","to":{"name":"Done"}}]}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if out := call(t, m, "jira_transition", map[string]any{"key": "PROJ-1", "status": "Done"}); out == nil {
		t.Fatal("a 204 must still produce a result")
	}
}

// An upstream refusal has to reach the model, not be flattened into a generic
// failure.
// An upstream refusal has to reach the model, not be flattened into a generic
// failure.
//
// The lookups each handler makes first must SUCCEED, or the refusal is raised
// by a preliminary GET and the write itself is never exercised — which is how
// the transition case previously passed without testing its POST at all.
func TestWriteToolsPropagateUpstreamErrors(t *testing.T) {
	for _, c := range []struct {
		name       string
		tool       string
		args       map[string]any
		wantMethod string
	}{
		{"comment", "jira_comment", map[string]any{"key": "PROJ-1", "body": "hi"}, http.MethodPost},
		{"transition", "jira_transition", map[string]any{"key": "PROJ-1", "status": "Done"}, http.MethodPost},
		{"create", "jira_create", map[string]any{"project": "PROJ", "issuetype": "Task", "summary": "s"}, http.MethodPost},
	} {
		t.Run(c.name, func(t *testing.T) {
			var reached bool
			m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = io.WriteString(w, `{"transitions":[{"id":"51","name":"Finish","to":{"name":"Done"}}]}`)
					return
				}
				if r.Method == c.wantMethod {
					reached = true
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"errorMessages":["You do not have permission"]}`)
			})
			err := callErr(t, m, c.tool, c.args)
			if !reached {
				t.Fatalf("the %s was never made; this test would pass on a handler that never writes", c.wantMethod)
			}
			if !strings.Contains(err.Error(), "You do not have permission") {
				t.Errorf("error = %v, want the upstream message", err)
			}
			var apiErr *core.APIError
			if !errors.As(err, &apiErr) {
				t.Errorf("error %v is not a *core.APIError", err)
			}
		})
	}
}

// A deployment that has turned read off has said its caller may not see Jira's
// data. A transition error must not become the way to list a workflow anyway.
func TestTransitionErrorsHideNamesWithoutRead(t *testing.T) {
	base := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"transitions":[{"id":"51","name":"Done","to":{"name":"Closed"}},{"id":"52","name":"Finish","to":{"name":"Done"}}]}`)
	}).(module)
	base.cfg.Domains = map[string]core.Caps{Domain: {Destructive: true}}

	err := callErr(t, base, "jira_transition", map[string]any{"key": "PROJ-1", "status": "Nope"})
	for _, leak := range []string{"Closed", "Finish"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error = %v, must not enumerate transitions when read is disabled", err)
		}
	}
	for _, want := range []string{"2 available", "use an exact transition name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to carry %q", err, want)
		}
	}

	err = callErr(t, base, "jira_transition", map[string]any{"key": "PROJ-1", "status": "Done"})
	for _, leak := range []string{"Closed", "Finish"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("ambiguity error = %v, must not enumerate transitions when read is disabled", err)
		}
	}
	if !strings.Contains(err.Error(), "2 transitions") {
		t.Errorf("ambiguity error = %v, want the count", err)
	}
}

// With read on the names are shown, but as quoted third-party data.
func TestTransitionErrorsQuoteNames(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"transitions":[{"id":"51","name":"Done","to":{"name":"Closed"}}]}`)
	})
	err := callErr(t, m, "jira_transition", map[string]any{"key": "PROJ-1", "status": "Nope"})
	if !strings.Contains(err.Error(), `"Done" -> "Closed" (id 51)`) {
		t.Errorf("error = %v, want the transition rendered with quoted names", err)
	}
}

func TestCreateSendsProjectIssueTypeSummaryAndWikiDescription(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/2/issue" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"id":"10001","key":"PROJ-7"}`)
	})

	out, _ := call(t, m, "jira_create", map[string]any{
		"project": "PROJ", "issuetype": "Task", "summary": "Fix the thing",
		"description": "See `x` and **y**",
	}).(map[string]any)

	fields, _ := body["fields"].(map[string]any)
	project, _ := fields["project"].(map[string]any)
	if project["key"] != "PROJ" {
		t.Errorf("fields.project.key = %v, want PROJ", project["key"])
	}
	issuetype, _ := fields["issuetype"].(map[string]any)
	if issuetype["name"] != "Task" {
		t.Errorf("fields.issuetype.name = %v, want Task", issuetype["name"])
	}
	if fields["summary"] != "Fix the thing" {
		t.Errorf("fields.summary = %v", fields["summary"])
	}
	desc, _ := fields["description"].(string)
	if !strings.Contains(desc, "{{x}}") || !strings.Contains(desc, "*y*") {
		t.Errorf("description not converted to wiki markup: %q", desc)
	}
	if out["key"] != "PROJ-7" || out["id"] != "10001" {
		t.Errorf("result = %v, want the key and id the server returned", out)
	}
}

func TestCreateOmitsDescriptionWhenNotSupplied(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"id":"1","key":"PROJ-1"}`)
	})

	call(t, m, "jira_create", map[string]any{"project": "PROJ", "issuetype": "Bug", "summary": "s"})

	fields, _ := body["fields"].(map[string]any)
	if _, ok := fields["description"]; ok {
		t.Errorf("description must be absent when the caller supplied none, got %v", fields["description"])
	}
}

func TestCreateRequiresWriteCapability(t *testing.T) {
	base := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request may be made when the write class is disabled")
	}).(module)
	base.cfg.Domains = map[string]core.Caps{Domain: {Read: true}}

	if err := callErr(t, base, "jira_create", map[string]any{
		"project": "PROJ", "issuetype": "Task", "summary": "s",
	}); err == nil {
		t.Error("creating without the write capability must be refused")
	}
}

func TestCreateRefusedByAllowlist(t *testing.T) {
	base := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("refusal must happen before any request")
	}).(module)
	base.cfg.WriteProjects = []string{"OTHER"}

	err := callErr(t, base, "jira_create", map[string]any{
		"project": "PROJ", "issuetype": "Task", "summary": "s",
	})
	if !strings.Contains(err.Error(), "PROJ") {
		t.Errorf("error = %v, want it to name the project the caller supplied", err)
	}
	if strings.Contains(err.Error(), "OTHER") {
		t.Errorf("error = %v, must not disclose the allowlist's contents", err)
	}
}

func TestCreateProceedsForAnAllowlistedProject(t *testing.T) {
	base := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"1","key":"PROJ-1"}`)
	}).(module)
	base.cfg.WriteProjects = []string{"PROJ"}

	raw, _ := json.Marshal(map[string]any{"project": "PROJ", "issuetype": "Task", "summary": "s"})
	if _, err := declFor(t, base, "jira_create").Handle(context.Background(), raw); err != nil {
		t.Fatalf("jira_create: %v", err)
	}
}

func TestCreateRejectsInvalidProjectKey(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for an invalid project key")
	})
	for name, project := range map[string]string{
		"personal space": "~PERSONAL",
		"leading digit":  "1ABC",
		"empty":          "",
		"whitespace":     "   ",
		"punctuation":    "PR-OJ",
	} {
		if err := callErr(t, m, "jira_create", map[string]any{
			"project": project, "issuetype": "Task", "summary": "s",
		}); err == nil {
			t.Errorf("%s: an invalid project key must be refused", name)
		}
	}
}

func TestCreateRequiresIssueTypeAndSummary(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made when a required field is blank")
	})
	for name, args := range map[string]map[string]any{
		"missing issuetype": {"project": "PROJ", "summary": "s"},
		"blank issuetype":   {"project": "PROJ", "issuetype": "  ", "summary": "s"},
		"missing summary":   {"project": "PROJ", "issuetype": "Task"},
		"blank summary":     {"project": "PROJ", "issuetype": "Task", "summary": " "},
	} {
		if err := callErr(t, m, "jira_create", args); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestCreateBoundsSummaryAndDescription(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for an over-long field")
	})
	for name, args := range map[string]map[string]any{
		"summary": {"project": "PROJ", "issuetype": "Task",
			"summary": strings.Repeat("s", maxSummaryLen+1)},
		"description": {"project": "PROJ", "issuetype": "Task", "summary": "s",
			"description": strings.Repeat("d", maxBodyLen+1)},
		"issuetype": {"project": "PROJ", "summary": "s",
			"issuetype": strings.Repeat("t", maxIssueTypeLen+1)},
		// Trimming before measuring would let any of these through: the value
		// that arrives is unbounded whatever survives the trim.
		"padded summary": {"project": "PROJ", "issuetype": "Task",
			"summary": strings.Repeat(" ", maxSummaryLen+1) + "s"},
		"padded issuetype": {"project": "PROJ", "summary": "s",
			"issuetype": strings.Repeat(" ", maxIssueTypeLen+1) + "Task"},
		"padded description": {"project": "PROJ", "issuetype": "Task", "summary": "s",
			"description": strings.Repeat(" ", maxBodyLen+1) + "d"},
	} {
		if err := callErr(t, m, "jira_create", args); err == nil {
			t.Errorf("%s: an unbounded value must be refused", name)
		}
	}
}

// reProjectKey accepts a key of any length, so the bound is what keeps a
// megabyte of "A" out of the request body and out of the refusal that echoes
// the caller's own value back.
func TestCreateBoundsProjectKey(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for an over-long project key")
	})
	oversize := strings.Repeat("A", maxKeyLen+1)
	for name, project := range map[string]string{
		"valid shape":   oversize,
		"invalid shape": oversize + "!",
		"padded":        strings.Repeat(" ", maxKeyLen+1) + "PROJ",
	} {
		err := callErr(t, m, "jira_create", map[string]any{
			"project": project, "issuetype": "Task", "summary": "s",
		})
		if err == nil {
			t.Fatalf("%s: an unbounded project key must be refused", name)
		}
		if strings.Contains(err.Error(), oversize) {
			t.Errorf("%s: the refusal repeats the whole over-long value back", name)
		}
	}
}

// The allowlist folds case, so a lowercase key it permits must not then reach
// Jira in a form Jira may not resolve. validKey upper-cases for the same
// reason.
func TestCreateUpperCasesProjectKey(t *testing.T) {
	var sent string
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Fields struct {
				Project struct {
					Key string `json:"key"`
				} `json:"project"`
			} `json:"fields"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		sent = body.Fields.Project.Key
		_, _ = io.WriteString(w, `{"id":"1","key":"PROJ-1"}`)
	}).(module)
	base.cfg.WriteProjects = []string{"PROJ"}

	call(t, base, "jira_create", map[string]any{
		"project": "proj", "issuetype": "Task", "summary": "s",
	})
	if sent != "PROJ" {
		t.Errorf("project key sent = %q, want %q", sent, "PROJ")
	}
}

// A whitespace-only description passes the non-empty check and renders to
// nothing; creating the issue anyway would silently drop the description the
// caller asked for.
func TestCreateRefusesDescriptionThatRendersToNothing(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for a description that renders empty")
	})
	if err := callErr(t, m, "jira_create", map[string]any{
		"project": "PROJ", "issuetype": "Task", "summary": "s",
		"description": "   \n  ",
	}); err == nil {
		t.Error("a description that renders to nothing must be refused")
	}
}

// summary is plain text Jira renders literally, so it is sent exactly as the
// caller wrote it. Escaping it on the write direction would store backslashes
// in Jira, which is why markup.SafeText belongs on the read direction only.
func TestCreateSendsSummaryUnescaped(t *testing.T) {
	const summary = "[link](http://x) and `code`"
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"id":"1","key":"PROJ-1"}`)
	})

	call(t, m, "jira_create", map[string]any{
		"project": "PROJ", "issuetype": "Task", "summary": summary,
	})

	fields, _ := body["fields"].(map[string]any)
	if fields["summary"] != summary {
		t.Errorf("fields.summary = %q, want the caller's text verbatim", fields["summary"])
	}
}

func TestCreateScrubsReturnedKey(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		// U+200B between the key's halves: the key is third-party text like
		// any other scalar a result carries.
		_, _ = io.WriteString(w, "{\"id\":\"1\",\"key\":\"PROJ-\u200b1\"}")
	})

	out, _ := call(t, m, "jira_create", map[string]any{
		"project": "PROJ", "issuetype": "Task", "summary": "s",
	}).(map[string]any)

	if out["key"] != "PROJ-1" {
		t.Errorf("key = %q, want the invisible character stripped", out["key"])
	}
}
