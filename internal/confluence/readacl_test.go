package confluence

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
)

// newReadRestrictedModule wires a module whose read allowlist names spaces.
func newReadRestrictedModule(t *testing.T, h http.HandlerFunc, readSpaces ...string) core.Module {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := core.Config{
		BaseURL:      srv.URL,
		Email:        "u@example.com",
		Token:        "test-token-value-1234",
		Domains:      map[string]core.Caps{Domain: {Read: true, Write: true, Destructive: true}},
		ReadSpaces:   readSpaces,
		LimitDefault: 20,
		LimitMax:     50,
	}
	var logs bytes.Buffer
	return NewWith(cfg, core.NewClient(cfg, core.NewLogger("debug", &logs)))
}

func TestApplyReadSpaceFilter(t *testing.T) {
	cases := []struct {
		name   string
		cql    string
		spaces []string
		want   string
	}{
		{
			name:   "unrestricted leaves the query untouched",
			cql:    "type = page",
			spaces: nil,
			want:   "type = page",
		},
		{
			name:   "the allowlist is ANDed on",
			cql:    "type = page",
			spaces: []string{"ENG", "DOCS"},
			want:   `(type = page) AND space IN ("ENG", "DOCS")`,
		},
		{
			name:   "an OR naming a forbidden space cannot escape",
			cql:    "space = SECRET OR type = page",
			spaces: []string{"ENG", "DOCS"},
			want:   `(space = SECRET OR type = page) AND space IN ("ENG", "DOCS")`,
		},
		{
			name:   "nested CQL stays nested",
			cql:    "(space = SECRET AND type = page) OR label = x",
			spaces: []string{"ENG"},
			want:   `((space = SECRET AND type = page) OR label = x) AND space IN ("ENG")`,
		},
		{
			name:   "a personal space key is quoted like any other",
			cql:    "type = page",
			spaces: []string{"~712020abc"},
			want:   `(type = page) AND space IN ("~712020abc")`,
		},
		{
			name:   "a sort clause survives",
			cql:    "type = page order by created desc",
			spaces: []string{"ENG"},
			want:   `(type = page) AND space IN ("ENG") order by created desc`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := applyReadSpaceFilter(c.cql, c.spaces)
			if err != nil {
				t.Fatalf("applyReadSpaceFilter: %v", err)
			}
			if got != c.want {
				t.Errorf("applyReadSpaceFilter = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSearchSendsRestrictedCQL(t *testing.T) {
	for _, c := range []struct {
		name string
		cql  string
		want string
	}{
		{
			name: "plain condition",
			cql:  "type = page",
			want: `(type = page) AND space IN ("ENG", "DOCS")`,
		},
		{
			name: "attempted bypass with OR",
			cql:  "space = SECRET OR type = page",
			want: `(space = SECRET OR type = page) AND space IN ("ENG", "DOCS")`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			var sent string
			m := newReadRestrictedModule(t, func(w http.ResponseWriter, r *http.Request) {
				sent = r.URL.Query().Get("cql")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"results":[],"totalSize":0}`))
			}, "ENG", "DOCS")
			call(t, m, "confluence_search", map[string]any{"cql": c.cql})
			if sent != c.want {
				t.Errorf("CQL sent = %q, want %q", sent, c.want)
			}
		})
	}
}

// An unbalanced CQL cannot be restricted by wrapping it, so the search is
// refused before any request goes out. See the Jira twin for the mechanics.
func TestSearchRefusesCQLItCannotRestrict(t *testing.T) {
	for _, cql := range []string{
		"type = page) OR (type != page",
		"(type = page",
		`title ~ "unterminated`,
	} {
		sent := false
		m := newReadRestrictedModule(t, func(w http.ResponseWriter, r *http.Request) {
			sent = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[],"totalSize":0}`))
		}, "ENG")
		if err := callErr(t, m, "confluence_search", map[string]any{"cql": cql}); err == nil {
			t.Errorf("confluence_search(%q) = nil error, want refusal", cql)
		}
		if sent {
			t.Errorf("confluence_search(%q) reached Confluence; it must be refused first", cql)
		}
	}
}

func TestSearchLeavesCQLAloneWhenUnrestricted(t *testing.T) {
	var sent string
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		sent = r.URL.Query().Get("cql")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[],"totalSize":0}`))
	})
	call(t, m, "confluence_search", map[string]any{"cql": "space = SECRET"})
	if sent != "space = SECRET" {
		t.Errorf("CQL sent = %q, want it unchanged", sent)
	}
}

// pageHandler serves one page in one space, plus the space lookup the
// allowlist check needs, and counts the space lookups so the cache is
// observable.
func pageHandler(spaceID, spaceKey, title, body string, spaceLookups *int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/wiki/api/v2/spaces/"):
			*spaceLookups++
			_, _ = w.Write([]byte(`{"id":"` + spaceID + `","key":"` + spaceKey + `"}`))
		default:
			_, _ = w.Write([]byte(`{"id":"77","title":"` + title + `","spaceId":"` + spaceID +
				`","version":{"number":3},"body":{"view":{"value":"<p>` + body + `</p>"}}}`))
		}
	}
}

func TestGetPageEnforcesTheSpaceThePageIsInNow(t *testing.T) {
	const (
		secretTitle = "board minutes"
		secretBody  = "acquisition price"
	)
	cases := []struct {
		name    string
		key     string
		allowed bool
	}{
		{name: "first allowed space", key: "ENG", allowed: true},
		{name: "second allowed space", key: "DOCS", allowed: true},
		{name: "forbidden space", key: "SECRET", allowed: false},
		// A page that has moved out of an allowed space reports its new space
		// and stops being readable, whatever id the caller used.
		{name: "moved into a forbidden space", key: "SECRET_NEW", allowed: false},
		{name: "case differs from the allowlist entry", key: "eng", allowed: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lookups := 0
			m := newReadRestrictedModule(t, pageHandler("9001", c.key, secretTitle, secretBody, &lookups), "ENG", "DOCS")
			if c.allowed {
				out := call(t, m, "confluence_get_page", map[string]any{"id": "77"})
				got, ok := out.(map[string]any)
				if !ok {
					t.Fatalf("confluence_get_page returned %T, want a map", out)
				}
				if got[fieldTitle] != secretTitle {
					t.Errorf("title = %v, want %q", got[fieldTitle], secretTitle)
				}
				return
			}
			err := callErr(t, m, "confluence_get_page", map[string]any{"id": "77"})
			if !strings.Contains(err.Error(), "access denied") || !strings.Contains(err.Error(), "ATLAS_READ_SPACES") {
				t.Errorf("error = %q, want an access-denied message naming ATLAS_READ_SPACES", err)
			}
			for _, leak := range []string{secretTitle, secretBody, c.key} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error %q leaks %q", err, leak)
				}
			}
		})
	}
}

func TestGetPageSkipsTheSpaceLookupWhenUnrestricted(t *testing.T) {
	lookups := 0
	m := newTestModule(t, pageHandler("9001", "SECRET", "t", "b", &lookups))
	call(t, m, "confluence_get_page", map[string]any{"id": "77"})
	if lookups != 0 {
		t.Errorf("space lookups = %d, want none with no allowlist configured", lookups)
	}
}

// The space id to key mapping is resolved once per space, not once per read.
func TestGetPageCachesTheSpaceKeyLookup(t *testing.T) {
	lookups := 0
	m := newReadRestrictedModule(t, pageHandler("9001", "ENG", "t", "b", &lookups), "ENG")
	for range 3 {
		call(t, m, "confluence_get_page", map[string]any{"id": "77"})
	}
	if lookups != 1 {
		t.Errorf("space lookups = %d, want 1", lookups)
	}
}

// A deployment may allow writes to a space it does not allow reads of. An
// update that omits a title must then not hand the page's existing title back,
// since that title was read out of a space the caller may not read.
func TestUpdatePageWithholdsTheExistingTitleWhenTheSpaceIsUnreadable(t *testing.T) {
	const existingTitle = "board minutes"
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/wiki/api/v2/spaces/"):
			_, _ = w.Write([]byte(`{"id":"9001","key":"SECRET"}`))
		case r.Method == http.MethodPut:
			_, _ = w.Write([]byte(`{"id":"77","version":{"number":4}}`))
		default:
			_, _ = w.Write([]byte(`{"id":"77","title":"` + existingTitle +
				`","spaceId":"9001","version":{"number":3}}`))
		}
	}
	// Writes unrestricted, reads restricted to a space the page is not in.
	m := newReadRestrictedModule(t, handler, "ENG")
	out, ok := call(t, m, "confluence_update_page", map[string]any{"id": "77", "body": "new text"}).(map[string]any)
	if !ok {
		t.Fatal("confluence_update_page did not return a map")
	}
	if got, present := out[fieldTitle]; present {
		t.Errorf("result carries title %v, read from a space outside the read allowlist", got)
	}

	// A title the caller supplied itself is theirs, and comes back.
	out, ok = call(t, m, "confluence_update_page", map[string]any{"id": "77", "body": "new text", "title": "caller title"}).(map[string]any)
	if !ok {
		t.Fatal("confluence_update_page did not return a map")
	}
	if out[fieldTitle] != "caller title" {
		t.Errorf("title = %v, want the caller's own", out[fieldTitle])
	}
}

// A cached space key must not outlive its TTL: an administrator can rename a
// space key, and both allowlists key on the name, so a cache with no expiry
// would keep a renamed space authorized until the process restarted.
func TestSpaceKeyCacheExpires(t *testing.T) {
	c := newSpaceKeyCache()
	now := time.Now()
	c.now = func() time.Time { return now }

	c.put("9001", "ENG")
	if key, ok := c.get("9001"); !ok || key != "ENG" {
		t.Fatalf("get = (%q, %v), want (ENG, true)", key, ok)
	}
	now = now.Add(spaceKeyTTL - time.Second)
	if _, ok := c.get("9001"); !ok {
		t.Error("entry expired before its TTL")
	}
	now = now.Add(2 * time.Second)
	if _, ok := c.get("9001"); ok {
		t.Error("entry survived its TTL")
	}
}

// The entry count is bounded: space ids come from Confluence, and a
// long-running process reads whatever it is pointed at.
func TestSpaceKeyCacheIsBounded(t *testing.T) {
	c := newSpaceKeyCache()
	for i := range spaceKeyCacheMax + 10 {
		c.put(strconv.Itoa(i), "ENG")
	}
	c.mu.Lock()
	held := len(c.keys)
	c.mu.Unlock()
	if held > spaceKeyCacheMax {
		t.Errorf("cache holds %d entries, above the %d cap", held, spaceKeyCacheMax)
	}
}

// A blank key is never cached: it would authorise nothing and would keep a
// failed resolution around.
func TestSpaceKeyCacheIgnoresBlankKey(t *testing.T) {
	c := newSpaceKeyCache()
	c.put("9001", "  ")
	if _, ok := c.get("9001"); ok {
		t.Error("a blank key must not be cached")
	}
}

// A page that reports no owning space cannot be checked, so it is refused.
func TestGetPageRefusesWhenTheSpaceCannotBeResolved(t *testing.T) {
	m := newReadRestrictedModule(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"77","title":"t","version":{"number":1},"body":{"view":{"value":"x"}}}`))
	}, "ENG")
	if err := callErr(t, m, "confluence_get_page", map[string]any{"id": "77"}); err == nil {
		t.Error("expected a refusal when the owning space is unknown")
	}
}
