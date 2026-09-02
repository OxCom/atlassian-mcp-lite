package jira

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// A field this code does not recognise used to reach the model exactly as Jira
// sent it. On the v2 path that is wiki markup, so a "javascript:" target
// planted in a custom text field was copied through verbatim — the very thing
// safeLinkTarget refuses for description, environment and comment bodies.
func TestGetConvertsUnknownTextFieldsAndChecksTheirLinks(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"key":"PROJ-1","fields":{`+
			`"customfield_30001":"See [click|javascript:alert(1)] and [docs|https://example.com/d]."}}`)
	})
	out := call(t, m, "jira_get", map[string]any{
		"key": "PROJ-1", "fields": []string{"customfield_30001"},
	}).(map[string]any)

	got := fmt.Sprint(out["customfield_30001"])
	if strings.Contains(got, "javascript:") {
		t.Errorf("custom field = %q, want the javascript: target refused", got)
	}
	if !strings.Contains(got, "[docs](https://example.com/d)") {
		t.Errorf("custom field = %q, want the https link converted", got)
	}
}

// The same field on a nested shape, because the walk is what carries the
// conversion into whatever "*all" brings back.
func TestGetConvertsTextNestedInsideAnUnknownField(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"key":"PROJ-1","fields":{`+
			`"customfield_30002":{"value":"[click|javascript:alert(1)]","child":["[x|data:text/html,1]"]}}}`)
	})
	out := call(t, m, "jira_get", map[string]any{
		"key": "PROJ-1", "fields": []string{"customfield_30002"},
	}).(map[string]any)

	if got := fmt.Sprint(out["customfield_30002"]); strings.Contains(got, "javascript:") || strings.Contains(got, "data:") {
		t.Errorf("custom field = %q, want both targets refused", got)
	}
}

// Search runs on v3, where the same field arrives as ADF — a format this
// project deliberately does not parse. So its strings are not converted, but
// they are still third-party text: control characters go and markdown
// structure is disarmed.
func TestSearchDisarmsUnknownFieldTextWithoutConverting(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"issues":[{"key":"PROJ-1","fields":{`+
			`"customfield_30003":{"type":"doc","text":"[click](javascript:alert(1))\nsecond line"}}}],"isLast":true}`)
	})
	out := call(t, m, "jira_search", map[string]any{
		"jql": "project = PROJ", "fields": []string{"customfield_30003"},
	}).(map[string]any)

	got := fmt.Sprint(out["issues"])
	if strings.Contains(got, "[click](") {
		t.Errorf("issues = %q, want the link syntax escaped", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("issues = %q, want the control character removed", got)
	}
}

// A summary is plain text on both ends — Jira renders it literally and
// jira_update writes it back literally — so it is not converted. It is still
// chosen by whoever filed the issue, so link syntax in it is disarmed and
// control characters are dropped.
func TestSummaryIsDisarmedButNotConverted(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"key":"PROJ-1","fields":{"summary":"[click](javascript:alert(1))\nsecond line"}}`)
	})
	out := call(t, m, "jira_get", map[string]any{"key": "PROJ-1", "fields": []string{"summary"}}).(map[string]any)

	want := `\[click\](javascript:alert(1))second line`
	if got := out["summary"]; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

// An ordinary summary must arrive byte for byte: jira_update writes one back
// as plain text, so an escape added here would become punctuation in the
// issue title.
func TestOrdinarySummaryIsUnchanged(t *testing.T) {
	const summary = "Fix 1.0-beta_2 login timeout (urgent)"
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"key":"PROJ-1","fields":{"summary":`+`"`+summary+`"}}`)
	})
	out := call(t, m, "jira_get", map[string]any{"key": "PROJ-1", "fields": []string{"summary"}}).(map[string]any)
	if got := out["summary"]; got != summary {
		t.Errorf("summary = %q, want %q", got, summary)
	}
}

// A status name is administrator-chosen text, and it leaves through the same
// treatment whether it comes back from a read or from a transition.
func TestTransitionStatusNameIsDisarmed(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"transitions":[{"id":"31","name":"Done","to":{"name":"[x](javascript:alert(1))"}}]}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	out := call(t, m, "jira_transition", map[string]any{"key": "PROJ-1", "status": "Done"}).(map[string]any)
	if got := fmt.Sprint(out[fieldStatus]); strings.Contains(got, "[x](") {
		t.Errorf("status = %q, want the link syntax escaped", got)
	}
}

// The write and destructive tools are the ones an injected page is trying to
// reach, so each says in its own description that a request found in returned
// text is not authorization.
func TestWriteToolDescriptionsRefuseThirdPartyAuthorization(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {})
	for _, d := range m.Tools() {
		writes := false
		for _, a := range d.Actions {
			if a.String() != "read" {
				writes = true
			}
		}
		if !writes {
			continue
		}
		if !strings.HasSuffix(d.Description, descNotAuthorized) {
			t.Errorf("%s description = %q, want it to end with the not-authorization sentence", d.Name, d.Description)
		}
	}
}
