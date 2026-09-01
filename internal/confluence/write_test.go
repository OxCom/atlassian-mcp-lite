package confluence

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

func TestCreatePageSendsWikiRepresentation(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		// The v2 create endpoint needs a numeric spaceId, so the space key is
		// resolved first. Both calls land on this handler.
		if r.URL.Path == "/wiki/api/v2/spaces" {
			_, _ = io.WriteString(w, `{"results":[{"id":"9","key":"DOCS"}]}`)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/wiki/api/v2/pages" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"id":"555","title":"New"}`)
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

func TestCreatePageSendsParentID(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wiki/api/v2/spaces" {
			_, _ = io.WriteString(w, `{"results":[{"id":"9","key":"DOCS"}]}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"id":"555","title":"New"}`)
	})
	call(t, m, "confluence_create_page", map[string]any{
		"space": "DOCS", "title": "New", "body": "x", "parent_id": "42",
	})
	if body["parentId"] != "42" {
		t.Errorf("parentId = %v, want 42", body["parentId"])
	}
}

// A parent id reaches no URL path, but it does reach a request body, and every
// other id in this package is validated before it leaves the process.
func TestCreatePageRejectsNonNumericParentID(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wiki/api/v2/spaces" {
			_, _ = io.WriteString(w, `{"results":[{"id":"9","key":"DOCS"}]}`)
			return
		}
		t.Error("create must not be attempted with a malformed parent id")
	})
	err := callErr(t, m, "confluence_create_page", map[string]any{
		"space": "DOCS", "title": "T", "body": "x", "parent_id": "../9",
	})
	if !strings.Contains(err.Error(), "parent_id") {
		t.Errorf("error = %v, want it to name parent_id", err)
	}
}

func TestCreatePageRefusedByAllowlist(t *testing.T) {
	base := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("refusal must happen before any request")
	}).(module)
	base.cfg.WriteSpaces = []string{"OTHER"}

	raw, err := json.Marshal(map[string]any{"space": "DOCS", "title": "T", "body": "x"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	if _, err := declFor(t, base, "confluence_create_page").Handle(context.Background(), raw); err == nil {
		t.Fatal("create outside the space allowlist must be refused")
	}
}

func TestUpdatePageFetchesVersionAndIncrements(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"id":"123","title":"Old","spaceId":"9","version":{"number":7}}`)
		case http.MethodPut:
			if r.URL.Path != "/wiki/api/v2/pages/123" {
				t.Errorf("path = %q", r.URL.Path)
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, _ = io.WriteString(w, `{"id":"123","version":{"number":8}}`)
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

// The version number is the optimistic lock. It is requested explicitly rather
// than hoped for as a default, exactly as confluence_get_page does.
func TestUpdatePageRequestsVersionExplicitly(t *testing.T) {
	var got string
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			got = r.URL.Query().Get("include-version")
			_, _ = io.WriteString(w, `{"id":"123","title":"Old","version":{"number":1}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"123","version":{"number":2}}`)
	})
	call(t, m, "confluence_update_page", map[string]any{"id": "123", "body": "x"})
	if got != "true" {
		t.Errorf("include-version = %q, want true", got)
	}
}

// Sending version 1 for a page whose version the server never reported would
// either clobber a concurrent edit or fail with an opaque conflict. Neither is
// acceptable, so a missing version is an error before the PUT.
func TestUpdatePageRefusesWithoutAUsableVersion(t *testing.T) {
	for _, resp := range []string{
		`{"id":"123","title":"Old"}`,
		`{"id":"123","title":"Old","version":null}`,
		`{"id":"123","title":"Old","version":{"number":0}}`,
	} {
		m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("no %s must be sent without a usable version", r.Method)
			}
			_, _ = io.WriteString(w, resp)
		})
		err := callErr(t, m, "confluence_update_page", map[string]any{"id": "123", "body": "x"})
		if !strings.Contains(err.Error(), "version") {
			t.Errorf("response %s: error = %v, want it to name the version", resp, err)
		}
	}
}

func TestUpdatePageKeepsSuppliedTitle(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"id":"123","title":"Old","version":{"number":1}}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"id":"123","version":{"number":2}}`)
	})
	call(t, m, "confluence_update_page", map[string]any{"id": "123", "title": "Renamed", "body": "x"})
	if body["title"] != "Renamed" {
		t.Errorf("title = %v, want Renamed", body["title"])
	}
}

func TestUpdatePageRequiresBody(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made without a body")
	})
	raw, err := json.Marshal(map[string]any{"id": "123", "body": "  "})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	if _, err := declFor(t, m, "confluence_update_page").Handle(context.Background(), raw); err == nil {
		t.Fatal("an empty page body must error rather than blanking the page")
	}
}

func TestUpdatePageRefusedByAllowlist(t *testing.T) {
	put := false
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			put = true
		}
		switch r.URL.Path {
		case "/wiki/api/v2/pages/123":
			_, _ = io.WriteString(w, `{"id":"123","title":"T","spaceId":"9","version":{"number":1}}`)
		case "/wiki/api/v2/spaces/9":
			_, _ = io.WriteString(w, `{"id":"9","key":"OTHERSPACE"}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}).(module)
	base.cfg.WriteSpaces = []string{"DOCS"}

	if err := callErr(t, base, "confluence_update_page",
		map[string]any{"id": "123", "body": "x"}); !strings.Contains(err.Error(), "OTHERSPACE") {
		t.Errorf("error = %v, want it to name the refused space", err)
	}
	if put {
		t.Error("refusal must happen before the page is replaced")
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
		switch r.URL.Path {
		case "/wiki/api/v2/pages/123":
			_, _ = io.WriteString(w, `{"id":"123","spaceId":"9","title":"T","version":{"number":1}}`)
		case "/wiki/api/v2/spaces/9":
			_, _ = io.WriteString(w, `{"id":"9","key":"OTHERSPACE"}`)
		default:
			_, _ = io.WriteString(w, `{"id":"9001"}`)
		}
	}).(module)
	base.cfg.WriteSpaces = []string{"DOCS"}

	raw, err := json.Marshal(map[string]any{"page_id": "123", "body": "hi"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
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
			_, _ = io.WriteString(w, `{"results":[{"id":"77","key":"SOMETHINGELSE"}]}`)
			return
		}
		t.Error("create must not be attempted when the space key does not match")
	})
	raw, err := json.Marshal(map[string]any{"space": "DOCS", "title": "T", "body": "x"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	if _, err := declFor(t, m, "confluence_create_page").Handle(context.Background(), raw); err == nil {
		t.Fatal("a non-matching space key must be an error")
	}
}

// Two spaces cannot both be "the" space for a create, and picking either would
// be a silent guess about where the page lands.
func TestSpaceIDForRejectsAmbiguousMatch(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wiki/api/v2/spaces" {
			_, _ = io.WriteString(w, `{"results":[{"id":"1","key":"DOCS"},{"id":"2","key":"docs"}]}`)
			return
		}
		t.Error("create must not be attempted for an ambiguous space key")
	})
	err := callErr(t, m, "confluence_create_page", map[string]any{"space": "DOCS", "title": "T", "body": "x"})
	if !strings.Contains(err.Error(), "more than one") {
		t.Errorf("error = %v, want it to report the ambiguity", err)
	}
}

func TestCommentPostsFooterComment(t *testing.T) {
	var body map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wiki/api/v2/footer-comments" {
			t.Errorf("path = %q, want footer-comments", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"id":"9001"}`)
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

// Every free-form string a caller controls is bounded, as maxCQLLen bounds the
// one on the read path. An unbounded title or body is a memory and rate-limit
// amplifier that costs nothing to close.
func TestWriteToolsBoundFreeFormStrings(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("an oversized string must not reach the network")
	})
	long := strings.Repeat("a", maxBodyLen+1)
	longTitle := strings.Repeat("a", maxTitleLen+1)

	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"create body", "confluence_create_page", map[string]any{"space": "DOCS", "title": "T", "body": long}},
		{"create title", "confluence_create_page", map[string]any{"space": "DOCS", "title": longTitle, "body": "x"}},
		{"create space", "confluence_create_page", map[string]any{"space": strings.Repeat("S", maxSpaceKeyLen+1), "title": "T", "body": "x"}},
		{"update body", "confluence_update_page", map[string]any{"id": "123", "body": long}},
		{"update title", "confluence_update_page", map[string]any{"id": "123", "title": longTitle, "body": "x"}},
		{"comment body", "confluence_comment", map[string]any{"page_id": "123", "body": long}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := callErr(t, m, c.tool, c.args); !strings.Contains(err.Error(), "limit is") {
				t.Errorf("error = %v, want it to state the limit", err)
			}
		})
	}
}

// Atlassian saying no is the most common runtime path, and the caller needs
// *core.APIError to survive each write handler.
// Atlassian saying no is the most common runtime path, and the caller needs
// *core.APIError to survive each write handler.
//
// The lookup GETs each handler makes first must succeed, or the 403 is raised
// by the preliminary request and the write itself is never reached — which is
// how this test previously passed without exercising the create POST or the
// update PUT at all.
func TestWriteUpstreamFailurePropagatesAsAPIError(t *testing.T) {
	for _, c := range []struct {
		name       string
		tool       string
		args       map[string]any
		wantMethod string
		wantPath   string
	}{
		{"create", "confluence_create_page", map[string]any{"space": "DOCS", "title": "T", "body": "x"},
			http.MethodPost, "/wiki/api/v2/pages"},
		{"update", "confluence_update_page", map[string]any{"id": "123", "body": "x"},
			http.MethodPut, "/wiki/api/v2/pages/123"},
		{"comment", "confluence_comment", map[string]any{"page_id": "123", "body": "x"},
			http.MethodPost, "/wiki/api/v2/footer-comments"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var reached bool
			m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					switch {
					case strings.HasPrefix(r.URL.Path, "/wiki/api/v2/spaces"):
						_, _ = io.WriteString(w, `{"results":[{"id":"9","key":"DOCS"}]}`)
					default:
						_, _ = io.WriteString(w, `{"id":"123","title":"T","spaceId":"9","version":{"number":3}}`)
					}
					return
				}
				if r.Method == c.wantMethod && r.URL.Path == c.wantPath {
					reached = true
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"message":"no permission"}`)
			})
			err := callErr(t, m, c.tool, c.args)
			if !reached {
				t.Fatalf("the %s %s was never made; this test would pass on a handler that never writes", c.wantMethod, c.wantPath)
			}
			var apiErr *core.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error %v is not a *core.APIError", err)
			}
			if apiErr.Status != http.StatusForbidden {
				t.Errorf("status = %d, want 403", apiErr.Status)
			}
			if !strings.Contains(err.Error(), "no permission") {
				t.Errorf("error %q does not carry the upstream message", err)
			}
		})
	}
}

func TestWriteToolsRejectMalformedPageID(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for a malformed id")
	})
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"confluence_update_page", map[string]any{"id": "../../secrets", "body": "x"}},
		{"confluence_comment", map[string]any{"page_id": "1 2", "body": "x"}},
	} {
		if err := callErr(t, m, c.tool, c.args); !strings.Contains(err.Error(), "page id") {
			t.Errorf("%s: error = %v, want it to name the id", c.tool, err)
		}
	}
}

// The schema keys are written as constants, so a mistake there would advertise
// a property no handler reads and would be invisible to every other test:
// ObjectSchema rejects unknown properties, so the tool would simply refuse
// every call. The names are pinned literally here on purpose.
func TestWriteToolSchemasAdvertiseTheDocumentedProperties(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {})
	want := map[string]struct {
		props    []string
		required []string
	}{
		"confluence_create_page": {[]string{"space", "title", "body", "parent_id"}, []string{"space", "title", "body"}},
		"confluence_update_page": {[]string{"id", "title", "body", "version"}, []string{"id", "body"}},
		"confluence_comment":     {[]string{"page_id", "body"}, []string{"page_id", "body"}},
	}
	for name, w := range want {
		schema := declFor(t, m, name).Schema(core.Caps{Write: true, Destructive: true})
		if len(schema.Properties) != len(w.props) {
			t.Errorf("%s advertises %d properties, want %d", name, len(schema.Properties), len(w.props))
		}
		for _, p := range w.props {
			if schema.Properties[p] == nil {
				t.Errorf("%s: property %q is not advertised", name, p)
			}
		}
		if strings.Join(schema.Required, ",") != strings.Join(w.required, ",") {
			t.Errorf("%s required = %v, want %v", name, schema.Required, w.required)
		}
	}
}

// Without a caller-supplied version the lock covers only the microseconds
// between this handler's own GET and its PUT, so an edit made between the
// caller READING the page and calling this tool is silently overwritten. The
// version argument moves the check back to the read the caller actually used.
func TestUpdatePageRefusesWhenTheBasedOnVersionIsStale(t *testing.T) {
	var wrote bool
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// Someone else has already moved the page on to version 8.
			_, _ = io.WriteString(w, `{"id":"123","title":"T","spaceId":"9","version":{"number":8}}`)
			return
		}
		wrote = true
		w.WriteHeader(http.StatusOK)
	})

	err := callErr(t, m, "confluence_update_page", map[string]any{
		"id": "123", "body": "x", "version": "7",
	})
	if !strings.Contains(err.Error(), "version 8") {
		t.Errorf("error = %v, want it to name the version the page is actually at", err)
	}
	if wrote {
		t.Error("a stale update must not reach the network")
	}
}

// The supplied version being current is the ordinary case, and it must write.
func TestUpdatePageAcceptsACurrentBasedOnVersion(t *testing.T) {
	var sent map[string]any
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"id":"123","title":"T","spaceId":"9","version":{"number":7}}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&sent)
		_, _ = io.WriteString(w, `{"id":"123","version":{"number":8}}`)
	})
	out := call(t, m, "confluence_update_page", map[string]any{
		"id": "123", "body": "x", "version": "7",
	}).(map[string]any)

	version, _ := sent["version"].(map[string]any)
	if version["number"] != float64(8) {
		t.Errorf("sent version = %v, want the current one plus one", sent["version"])
	}
	if out["version"] != 8 {
		t.Errorf("returned version = %v, want 8", out["version"])
	}
}

func TestUpdatePageRejectsAMalformedVersion(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a malformed version must not reach the network")
	})
	for _, v := range []string{"abc", "0", "-1"} {
		if err := callErr(t, m, "confluence_update_page", map[string]any{"id": "123", "body": "x", "version": v}); !strings.Contains(err.Error(), "positive number") {
			t.Errorf("version %q: error = %v", v, err)
		}
	}
}

// A PUT response without a version would otherwise hand the model a confident
// zero. The read path guards this with a pointer; so must the write path.
func TestUpdatePageFallsBackWhenTheResponseOmitsTheVersion(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"id":"123","title":"T","spaceId":"9","version":{"number":3}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"123"}`)
	})
	out := call(t, m, "confluence_update_page", map[string]any{"id": "123", "body": "x"}).(map[string]any)
	if out["version"] != 4 {
		t.Errorf("version = %v, want the 4 the server must have accepted, never 0", out["version"])
	}
}

// Every allowlist test asserts a refusal, which leaves the branch that lets a
// write THROUGH — the one that runs in the riskiest configuration — unproven.
func TestRestrictedModeAllowsAWriteToAnAllowlistedSpace(t *testing.T) {
	for _, c := range []struct {
		name string
		tool string
		args map[string]any
	}{
		{"create", "confluence_create_page", map[string]any{"space": "DOCS", "title": "T", "body": "x"}},
		{"update", "confluence_update_page", map[string]any{"id": "123", "body": "x"}},
		{"comment", "confluence_comment", map[string]any{"page_id": "123", "body": "x"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var wrote bool
			base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					switch {
					case strings.HasPrefix(r.URL.Path, "/wiki/api/v2/spaces/"):
						_, _ = io.WriteString(w, `{"id":"9","key":"DOCS"}`)
					case strings.HasPrefix(r.URL.Path, "/wiki/api/v2/spaces"):
						_, _ = io.WriteString(w, `{"results":[{"id":"9","key":"DOCS"}]}`)
					default:
						_, _ = io.WriteString(w, `{"id":"123","title":"T","spaceId":"9","version":{"number":3}}`)
					}
					return
				}
				wrote = true
				_, _ = io.WriteString(w, `{"id":"123","title":"T","version":{"number":4}}`)
			}).(module)
			base.cfg.WriteSpaces = []string{"DOCS"}

			raw, _ := json.Marshal(c.args)
			if _, err := declFor(t, base, c.tool).Handle(context.Background(), raw); err != nil {
				t.Fatalf("a write to an allowlisted space must succeed: %v", err)
			}
			if !wrote {
				t.Error("the write never reached the network")
			}
		})
	}
}

// spaceKeyFor is the security backstop for update and comment in restricted
// mode: if it ever failed open, an unallowlisted space would be writable.
func TestSpaceKeyForFailsClosed(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"empty space id", `{"id":"123","title":"T","spaceId":"","version":{"number":3}}`},
		{"non-numeric space id", `{"id":"123","title":"T","spaceId":"abc","version":{"number":3}}`},
		{"empty key in space response", `{"id":"123","title":"T","spaceId":"9","version":{"number":3}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var wrote bool
			base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if strings.HasPrefix(r.URL.Path, "/wiki/api/v2/spaces/") {
						// The empty-key case: a space that reports no key.
						_, _ = io.WriteString(w, `{"id":"9","key":""}`)
						return
					}
					_, _ = io.WriteString(w, c.body)
					return
				}
				wrote = true
				w.WriteHeader(http.StatusOK)
			}).(module)
			base.cfg.WriteSpaces = []string{"DOCS"}

			raw, _ := json.Marshal(map[string]any{"id": "123", "body": "x"})
			if _, err := declFor(t, base, "confluence_update_page").Handle(context.Background(), raw); err == nil {
				t.Fatal("an unresolvable space must refuse the write, not permit it")
			}
			if wrote {
				t.Error("nothing may be written when the space cannot be resolved")
			}
		})
	}
}

// Confluence states its title limit in characters. Measuring bytes refuses a
// valid CJK title at well under the real limit.
func TestTitleLimitCountsCharacters(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if strings.HasPrefix(r.URL.Path, "/wiki/api/v2/spaces") {
				_, _ = io.WriteString(w, `{"results":[{"id":"9","key":"DOCS"}]}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":"123","title":"T","spaceId":"9","version":{"number":3}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"123","title":"T","version":{"number":4}}`)
	})
	// 200 characters, 600 bytes: inside Confluence's limit.
	call(t, m, "confluence_create_page", map[string]any{
		"space": "DOCS", "title": strings.Repeat("課", 200), "body": "x",
	})
	if err := callErr(t, m, "confluence_create_page", map[string]any{
		"space": "DOCS", "title": strings.Repeat("課", maxTitleLen+1), "body": "x",
	}); !strings.Contains(err.Error(), "characters") {
		t.Errorf("error = %v, want a character-based limit message", err)
	}
}
