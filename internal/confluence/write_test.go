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

// The version-mismatch refusal reports the page's current version number, so
// it must not be reachable for a page in a space the allowlist denies: it
// would confirm the page exists and report how often it has been edited, and
// it differs from the allowlist refusal, which makes the pair an oracle.
// confluence_get_page checks the space before the version for this reason;
// confluence_update_page did not until 2026-09-04.
func TestUpdatePageChecksTheSpaceBeforeTheVersion(t *testing.T) {
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wiki/api/v2/pages/123":
			_, _ = io.WriteString(w, `{"id":"123","title":"T","spaceId":"9","version":{"number":57}}`)
		case "/wiki/api/v2/spaces/9":
			_, _ = io.WriteString(w, `{"id":"9","key":"SECRET"}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}).(module)
	base.cfg.WriteSpaces = []string{"DOCS"}

	err := callErr(t, base, "confluence_update_page",
		map[string]any{"id": "123", "body": "x", "version": "1"})
	if strings.Contains(err.Error(), "57") {
		t.Errorf("error = %v, want no disclosure of the version of a page in a denied space", err)
	}
	if !strings.Contains(err.Error(), "ATLAS_WRITE_SPACES") {
		t.Errorf("error = %v, want the allowlist refusal", err)
	}
}

// A page whose owning space cannot be resolved must not have that space's
// numeric id handed back: the caller supplied a page id and nothing else, and
// the id names a space it may be allowed neither to read nor to write.
func TestSpaceKeyForDoesNotDiscloseTheSpaceID(t *testing.T) {
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/wiki/api/v2/pages/123":
			_, _ = io.WriteString(w, `{"id":"123","title":"T","spaceId":"9876543","version":{"number":3}}`)
		case strings.HasPrefix(r.URL.Path, "/wiki/api/v2/spaces/"):
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"message":"rate limited"}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}).(module)
	base.cfg.WriteSpaces = []string{"DOCS"}

	err := callErr(t, base, "confluence_update_page",
		map[string]any{"id": "123", "body": "x"})
	if strings.Contains(err.Error(), "9876543") {
		t.Errorf("error = %v, want no disclosure of the owning space id", err)
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error = %v, want the upstream status kept so a throttle is diagnosable", err)
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

// The registry withholds a tool whose action class is disabled, so in normal
// operation a disabled handler is never reached. The handler is still where
// the write happens, so it re-checks the capability rather than trusting its
// caller — a future dispatcher bug must not turn a read-only deployment into
// a writable one.
func TestWriteHandlersRecheckCapabilityAtRuntime(t *testing.T) {
	for _, c := range []struct {
		name string
		tool string
		args map[string]any
		caps core.Caps
		want string
	}{
		{"create without write", "confluence_create_page",
			map[string]any{"space": "DOCS", "title": "T", "body": "x"},
			core.Caps{Read: true, Destructive: true}, "write"},
		{"comment without write", "confluence_comment",
			map[string]any{"page_id": "123", "body": "x"},
			core.Caps{Read: true, Destructive: true}, "write"},
		{"update without destructive", "confluence_update_page",
			map[string]any{"id": "123", "body": "x"},
			core.Caps{Read: true, Write: true}, "destructive"},
	} {
		t.Run(c.name, func(t *testing.T) {
			base := newTestModule(t, func(http.ResponseWriter, *http.Request) {
				t.Error("a handler whose capability is off must make no request at all")
			}).(module)
			base.cfg.Domains[Domain] = c.caps

			err := callErr(t, base, c.tool, c.args)
			if !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), Domain) {
				t.Errorf("error = %v, want it to name the %s capability and the %s domain", err, c.want, Domain)
			}
		})
	}
}

// A declaration-only module has no client. Dispatching to it is a wiring bug in
// main, and it must surface as an error rather than a nil dereference.
func TestHandlersRefuseWithoutAClient(t *testing.T) {
	m := New()
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"confluence_search", map[string]any{"cql": "type=page"}},
		{"confluence_get_page", map[string]any{"id": "123"}},
		{"confluence_create_page", map[string]any{"space": "DOCS", "title": "T", "body": "x"}},
		{"confluence_update_page", map[string]any{"id": "123", "body": "x"}},
		{"confluence_comment", map[string]any{"page_id": "123", "body": "x"}},
	} {
		if err := callErr(t, m, c.tool, c.args); !strings.Contains(err.Error(), "no client") {
			t.Errorf("%s: error = %v, want it to say the module has no client", c.tool, err)
		}
	}
}

// A child page lives in its parent's space. If the parent were in a space
// outside the allowlist, naming an allowlisted space in the request would be a
// way to write into the forbidden one. In restricted mode the parent's space is
// therefore resolved and required to match before anything is created.
func TestCreatePageRefusesAParentInAnotherSpace(t *testing.T) {
	posted := false
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posted = true
		}
		switch r.URL.Path {
		case "/wiki/api/v2/pages/42":
			_, _ = io.WriteString(w, `{"id":"42","title":"Parent","spaceId":"7","version":{"number":1}}`)
		case "/wiki/api/v2/spaces/7":
			_, _ = io.WriteString(w, `{"id":"7","key":"HR"}`)
		case "/wiki/api/v2/spaces":
			_, _ = io.WriteString(w, `{"results":[{"id":"9","key":"SANDBOX"}]}`)
		default:
			_, _ = io.WriteString(w, `{"id":"555","title":"T"}`)
		}
	}).(module)
	base.cfg.WriteSpaces = []string{"SANDBOX"}

	err := callErr(t, base, "confluence_create_page", map[string]any{
		"space": "SANDBOX", "title": "T", "body": "x", "parent_id": "42",
	})
	if !strings.Contains(err.Error(), "HR") || !strings.Contains(err.Error(), "SANDBOX") {
		t.Errorf("error = %v, want it to name both the parent's space and the requested one", err)
	}
	if posted {
		t.Error("refusal must happen before the page is created")
	}
}

func TestCreatePageAcceptsAParentInTheSameSpace(t *testing.T) {
	var body map[string]any
	base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, _ = io.WriteString(w, `{"id":"555","title":"T"}`)
		case r.URL.Path == "/wiki/api/v2/pages/42":
			_, _ = io.WriteString(w, `{"id":"42","title":"Parent","spaceId":"9","version":{"number":1}}`)
		case r.URL.Path == "/wiki/api/v2/spaces/9":
			// Mixed case on purpose: the comparison folds case the same way
			// the allowlist does, so "Sandbox" from the server matches.
			_, _ = io.WriteString(w, `{"id":"9","key":"Sandbox"}`)
		case r.URL.Path == "/wiki/api/v2/spaces":
			_, _ = io.WriteString(w, `{"results":[{"id":"9","key":"SANDBOX"}]}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}).(module)
	base.cfg.WriteSpaces = []string{"SANDBOX"}

	raw, _ := json.Marshal(map[string]any{
		"space": "SANDBOX", "title": "T", "body": "x", "parent_id": "42",
	})
	if _, err := declFor(t, base, "confluence_create_page").Handle(context.Background(), raw); err != nil {
		t.Fatalf("a parent in the requested space must be accepted: %v", err)
	}
	if body["parentId"] != "42" {
		t.Errorf("parentId = %v, want 42 sent in the create body", body["parentId"])
	}
}

// Without an allowlist every space is writable, so resolving the parent's space
// would cost two requests to learn nothing. The check is skipped, as it is for
// update and comment.
func TestCreatePageSkipsParentLookupWhenUnrestricted(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/wiki/api/v2/pages/") {
			t.Errorf("no parent lookup expected in unrestricted mode: %s", r.URL.Path)
		}
		if r.URL.Path == "/wiki/api/v2/spaces" {
			_, _ = io.WriteString(w, `{"results":[{"id":"9","key":"DOCS"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"555","title":"T"}`)
	})
	call(t, m, "confluence_create_page", map[string]any{
		"space": "DOCS", "title": "T", "body": "x", "parent_id": "42",
	})
}

// A space key in these messages is Confluence's text, not this package's: it
// arrives in a response and travels on into a tool result and a log line. So
// it is quoted and bounded like the transition names in the Jira package. Left
// raw, a control byte reaches an operator's terminal and message-shaped
// punctuation lets the key impersonate the sentence around it.
func TestSpaceKeysFromConfluenceAreQuotedAndBounded(t *testing.T) {
	hostile := "OTHER\x01\" is permitted by ATLAS_WRITE_SPACES; space \"DOCS" + strings.Repeat("X", 400)
	hostileSpace, err := json.Marshal(map[string]any{"id": "9", "key": hostile})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	for _, tc := range []struct {
		name string
		tool string
		args map[string]any
	}{
		{"update resolves the owning space", "confluence_update_page",
			map[string]any{"id": "123", "body": "x"}},
		{"create resolves the parent's space", "confluence_create_page",
			map[string]any{"space": "DOCS", "title": "T", "body": "x", "parent_id": "123"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/wiki/api/v2/pages/123":
					_, _ = io.WriteString(w, `{"id":"123","title":"T","spaceId":"9","version":{"number":1}}`)
				case "/wiki/api/v2/spaces/9":
					_, _ = w.Write(hostileSpace)
				default:
					_, _ = io.WriteString(w, `{"results":[{"id":"9","key":"DOCS"}]}`)
				}
			}).(module)
			base.cfg.WriteSpaces = []string{"DOCS"}

			msg := callErr(t, base, tc.tool, tc.args).Error()
			if strings.Contains(msg, "\x01") {
				t.Errorf("error carries a raw control byte: %q", msg)
			}
			if !strings.Contains(msg, `\x01`) {
				t.Errorf("error = %q, want the control byte rendered as an escape", msg)
			}
			if strings.Contains(msg, strings.Repeat("X", maxSpaceKeyEcho)) {
				t.Errorf("error = %q, want the key bounded", msg)
			}
			if !strings.Contains(msg, "…") {
				t.Errorf("error = %q, want the truncation marked", msg)
			}
		})
	}
}
