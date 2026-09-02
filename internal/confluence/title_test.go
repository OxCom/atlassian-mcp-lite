package confluence

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
)

// A title is chosen by whoever can create the page and Confluence renders it
// literally, so it is not converted like a body. It is still third-party text:
// link syntax in it is disarmed and control characters are dropped.
func TestPageTitleIsDisarmed(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"123","title":"[click](javascript:alert(1))\nsecond line",`+
			`"spaceId":"9","version":{"number":3},"body":{"view":{"value":"<p>hi</p>"}}}`)
	})
	out := call(t, m, "confluence_get_page", map[string]any{"id": "123"}).(map[string]any)

	want := `\[click\](javascript:alert(1))second line`
	if got := out[fieldTitle]; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
}

// The same treatment on the search path, where the title arrives wrapped in
// the v1 endpoint's highlight markers.
func TestSearchTitleIsDisarmed(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":[{"title":"@@@hl@@@[click](javascript:alert(1))@@@endhl@@@",`+
			`"entityType":"content","content":{"id":"1","type":"page","status":"current"}}],"totalSize":1}`)
	})
	out := call(t, m, "confluence_search", map[string]any{"cql": "type=page"}).(map[string]any)

	got := fmt.Sprint(out["results"])
	if strings.Contains(got, "[click](") {
		t.Errorf("results = %q, want the link syntax escaped", got)
	}
	if strings.Contains(got, "@@@hl@@@") {
		t.Errorf("results = %q, want the highlight markers removed", got)
	}
}

// An ordinary title must arrive byte for byte: confluence_update_page writes
// one back as plain text, so an escape added here would become punctuation in
// the page name.
func TestOrdinaryTitleIsUnchanged(t *testing.T) {
	const title = "Release 1.0-beta_2 (draft)"
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"123","title":"`+title+`","spaceId":"9",`+
			`"version":{"number":3},"body":{"view":{"value":"<p>hi</p>"}}}`)
	})
	out := call(t, m, "confluence_get_page", map[string]any{"id": "123"}).(map[string]any)
	if got := out[fieldTitle]; got != title {
		t.Errorf("title = %q, want %q", got, title)
	}
}

// The write and destructive tools are what an injected page is trying to
// reach, so each says in its own description that a request found in returned
// text is not authorization.
func TestWriteToolDescriptionsRefuseThirdPartyAuthorization(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {})
	for _, d := range m.Tools() {
		writes := false
		for _, a := range d.Actions {
			if a != core.ActionRead {
				writes = true
			}
		}
		if !writes {
			continue
		}
		if !strings.HasSuffix(d.Description, notAuthorizedNotice) {
			t.Errorf("%s description = %q, want it to end with the not-authorization sentence", d.Name, d.Description)
		}
	}
}
