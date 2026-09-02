package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

const (
	testEmail = "user@example.com"
	testToken = "test-token-value-1234"
)

// newTestClient wires a Client against a fake Atlassian and returns the buffer
// its logger writes to, so tests can assert on what was and was not logged.
func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	cfg := Config{BaseURL: srv.URL, Email: testEmail, Token: testToken}
	logs := &bytes.Buffer{}
	return NewClient(cfg, NewLogger("debug", logs, cfg.Token, BasicCredential(cfg.Email, cfg.Token))), logs
}

func TestDoSendsBasicAuthAndDecodes(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != testEmail || pass != testToken {
			t.Errorf("basic auth = %q/%q ok=%v", user, pass, ok)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("User-Agent must identify this client to Atlassian")
		}
		_, _ = io.WriteString(w, `{"key":"PROJ-123"}`)
	})

	var out struct{ Key string }
	if err := c.Do(context.Background(), http.MethodGet, "/rest/api/3/issue/PROJ-123", nil, nil, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out.Key != "PROJ-123" {
		t.Errorf("Key = %q", out.Key)
	}
}

func TestDoSendsJSONBodyAndQuery(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("maxResults"); got != "20" {
			t.Errorf("maxResults = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["jql"] != "project = PROJ" {
			t.Errorf("body jql = %v", body["jql"])
		}
		w.WriteHeader(http.StatusNoContent)
	})

	q := url.Values{"maxResults": {"20"}}
	body := map[string]any{"jql": "project = PROJ"}
	if err := c.Do(context.Background(), http.MethodPost, "/rest/api/3/search/jql", q, body, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
}

// A GET must not carry a Content-Type, which would imply a body it does not have.
func TestDoOmitsContentTypeWithoutBody(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "" {
			t.Errorf("Content-Type = %q, want none for a bodyless request", got)
		}
		_, _ = io.WriteString(w, `{}`)
	})
	var out any
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
}

func TestDoReturnsAPIErrorWithUpstreamMessage(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"errorMessages":["Field 'customfield_10014' cannot be set"],"errors":{}}`)
	})

	err := c.Do(context.Background(), http.MethodPut, "/rest/api/2/issue/PROJ-123", nil, map[string]any{}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want *APIError", err, err)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("Status = %d", apiErr.Status)
	}
	if !strings.Contains(apiErr.Message, "customfield_10014") {
		t.Errorf("Message = %q, want the upstream text preserved", apiErr.Message)
	}
	if !strings.Contains(apiErr.Error(), "PUT") || !strings.Contains(apiErr.Error(), "/rest/api/2/issue/PROJ-123") {
		t.Errorf("Error() = %q, want the method and path", apiErr.Error())
	}
}

// Atlassian reports field-level problems in an object, not the message array.
func TestDoSurfacesFieldLevelErrors(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"errorMessages":[],"errors":{"summary":"You must specify a summary"}}`)
	})
	err := c.Do(context.Background(), http.MethodPut, "/x", nil, map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "You must specify a summary") {
		t.Errorf("err = %v, want the field-level message", err)
	}
}

// Atlassian uses two shapes for the "errors" member: Jira sends an object of
// field name to message, Confluence v2 sends an array of objects. Decoding only
// Jira's shape made the whole unmarshal fail on a Confluence body, so every
// Confluence v2 failure arrived as a raw JSON blob with the useful sentence
// buried in it.
func TestDoSurfacesConfluenceV2ErrorArray(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"errors":[{"status":404,"code":"NOT_FOUND","title":"No page with id 123","detail":"check the id"}]}`)
	})
	err := c.Do(context.Background(), http.MethodGet, "/wiki/api/v2/pages/123", nil, nil, nil)

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not an *APIError", err)
	}
	if !strings.Contains(apiErr.Message, "No page with id 123") {
		t.Errorf("Message = %q, want the title from the error array", apiErr.Message)
	}
	if !strings.Contains(apiErr.Message, "check the id") {
		t.Errorf("Message = %q, want the detail too", apiErr.Message)
	}
	if strings.Contains(apiErr.Message, `"status"`) {
		t.Errorf("Message = %q, want the sentence rather than the raw JSON", apiErr.Message)
	}
}

// ClampLimit and the two bound helpers live in core because both product
// modules need them and a limit policy that can drift apart is not a policy.
func TestClampLimit(t *testing.T) {
	cfg := Config{LimitDefault: 20, LimitMax: 50}
	for _, c := range []struct{ in, want int }{
		{0, 20}, {-1, 20}, {1, 1}, {50, 50}, {51, 50}, {1000, 50},
	} {
		if got := cfg.ClampLimit(c.in); got != c.want {
			t.Errorf("ClampLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestBoundHelpers(t *testing.T) {
	if err := BoundBytes("f", "abc", 3); err != nil {
		t.Errorf("a value at the limit must pass: %v", err)
	}
	if err := BoundBytes("f", "abcd", 3); err == nil {
		t.Error("a value past the byte limit must fail")
	}
	// The whole point of the rune variant: 3 characters, 9 bytes.
	if err := BoundRunes("f", "課課課", 3); err != nil {
		t.Errorf("3 characters must pass a 3-character limit: %v", err)
	}
	if err := BoundRunes("f", "課課課課", 3); err == nil {
		t.Error("4 characters must fail a 3-character limit")
	}
}

func TestRestrictsAccessors(t *testing.T) {
	var none Config
	if none.RestrictsProjects() || none.RestrictsSpaces() {
		t.Error("an unset allowlist restricts nothing")
	}
	set := Config{WriteProjects: []string{"P"}, WriteSpaces: []string{"S"}}
	if !set.RestrictsProjects() || !set.RestrictsSpaces() {
		t.Error("a configured allowlist restricts")
	}
}

// A redirect is never followed. Atlassian's REST API does not redirect, so one
// is a misconfiguration or an open-redirect attempt, and following it could
// send the credential somewhere it does not belong.
func TestDoRefusesToFollowRedirects(t *testing.T) {
	// Atomic: the handler runs on the server's goroutine and the assertion on
	// the test's. Request completion is not a memory-model guarantee for an
	// ordinary int, and -race reports it.
	var hits atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/start" {
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, `{"reached":"elsewhere"}`)
	})

	var out any
	err := c.Do(context.Background(), http.MethodGet, "/start", nil, nil, &out)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusFound {
		t.Fatalf("err = %v, want *APIError with status 302", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server saw %d requests, want 1; the redirect was followed", got)
	}
}

func TestDoUnauthorizedIsAPIError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "Client must be authenticated to access this resource.")
	})
	var out any
	err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want *APIError with status 401", err)
	}
	// A non-JSON body must still reach the caller rather than being swallowed.
	if !strings.Contains(apiErr.Message, "must be authenticated") {
		t.Errorf("Message = %q, want the plain-text body", apiErr.Message)
	}
}

func TestDoRateLimitedSurfacesRetryAfterSeconds(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"errorMessages":["rate limit exceeded"]}`)
	})
	var out any
	err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", apiErr.RetryAfter)
	}
	if !strings.Contains(apiErr.Error(), "retry after") {
		t.Errorf("Error() = %q, want the backoff hint", apiErr.Error())
	}
}

// Retry-After may also be an HTTP date.
func TestDoRateLimitedAcceptsRetryAfterDate(t *testing.T) {
	// An hour out, asserted loosely. A 90-second fixture with a 10-second
	// tolerance fails on a stalled CI worker even though the parser is correct,
	// which makes the test a source of noise rather than signal.
	const offset = time.Hour
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", time.Now().Add(offset).UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusTooManyRequests)
	})

	var apiErr *APIError
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, nil); !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.RetryAfter <= 0 || apiErr.RetryAfter > offset {
		t.Errorf("RetryAfter = %v, want a positive value no greater than %v", apiErr.RetryAfter, offset)
	}
	if apiErr.RetryAfter < offset-5*time.Minute {
		t.Errorf("RetryAfter = %v, want roughly %v", apiErr.RetryAfter, offset)
	}
}

// An unparseable Retry-After must not fail the call; it only removes the hint.
func TestDoRateLimitedTolerAtesJunkRetryAfter(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "soon")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	var out any
	var apiErr *APIError
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out); !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.RetryAfter != 0 {
		t.Errorf("RetryAfter = %v, want 0 for an unparseable header", apiErr.RetryAfter)
	}
}

func TestDoCancelledContextAborts(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should reach the server with a cancelled context")
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out any
	err := c.Do(ctx, http.MethodGet, "/x", nil, nil, &out)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestDoMalformedSuccessBodyIsAnError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// A valid field first, then a fault. json.Unmarshal writes as it goes,
		// so a decode straight into out would set Key before failing and hand
		// the caller a struct that is neither the old value nor the new one.
		_, _ = io.WriteString(w, `{"key":"decoded","other":`)
	})

	out := struct {
		Key   string
		Other string
	}{Key: "original", Other: "original"}

	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out); err == nil {
		t.Fatal("invalid JSON on a 200 must be an error, not a partial result")
	}
	if out.Key != "original" || out.Other != "original" {
		t.Errorf("out was mutated by a failed decode: %+v", out)
	}
}

// A 204 has no body, and a caller may not want the body of a 200 either.
func TestDoHandlesEmptyBodyAndNilOut(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	var out struct{ Key string }
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out); err != nil {
		t.Errorf("204 into a non-nil out: %v", err)
	}
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, nil); err != nil {
		t.Errorf("204 with nil out: %v", err)
	}
}

// An unbounded response would be buffered in full. The cap turns a runaway or
// hostile response into an error instead of memory exhaustion.
func TestDoRejectsOversizedResponse(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"padding":"`))
		chunk := bytes.Repeat([]byte("a"), 1<<20)
		for range (maxResponseBody / len(chunk)) + 2 {
			_, _ = w.Write(chunk)
		}
		_, _ = w.Write([]byte(`"}`))
	})
	var out any
	err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out)
	if err == nil {
		t.Fatal("an oversized response must be an error")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("err = %v, want it to name the size limit", err)
	}
}

func TestErrorBodyIsLoggedButSuccessBodyIsNot(t *testing.T) {
	c, logs := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"errorMessages":["diagnostic-detail-here"]}`)
			return
		}
		_, _ = io.WriteString(w, `{"confidential":"business-data-value"}`)
	})

	var out any
	if err := c.Do(context.Background(), http.MethodGet, "/ok", nil, nil, &out); err != nil {
		t.Fatalf("ok request: %v", err)
	}
	_ = c.Do(context.Background(), http.MethodGet, "/fail", nil, nil, &out)

	got := logs.String()
	if strings.Contains(got, "business-data-value") {
		t.Error("a successful response body must never be logged: it is business data")
	}
	if !strings.Contains(got, "diagnostic-detail-here") {
		t.Error("an error response body should be logged; it carries the diagnostics")
	}
}

// The credential must survive no route into the log, including an upstream
// error that echoes it back.
func TestCredentialsNeverReachTheLog(t *testing.T) {
	credential := BasicCredential(testEmail, testToken)
	c, logs := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"errorMessages":["rejected `+testToken+` and Basic `+credential+`"]}`)
	})

	var out any
	_ = c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out)

	got := logs.String()
	if strings.Contains(got, testToken) {
		t.Error("the raw token leaked into the log")
	}
	if strings.Contains(got, credential) {
		t.Error("the encoded Basic credential leaked into the log")
	}
}

func TestDebugLogsShapeNotContent(t *testing.T) {
	c, logs := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"confidential":"business-data-value"}`)
	})
	var out any
	if err := c.Do(context.Background(), http.MethodGet, "/rest/api/3/myself", nil, nil, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}

	got := logs.String()
	for _, want := range []string{"GET", "/rest/api/3/myself", "200"} {
		if !strings.Contains(got, want) {
			t.Errorf("debug log missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "business-data-value") {
		t.Error("debug logging must record shape, not content")
	}
}

// A transport failure is not an APIError: there was no response to interpret.
func TestTransportErrorIsNotAnAPIError(t *testing.T) {
	// A closed server gives a deterministic connection failure. Pointing at a
	// low port and assuming nothing listens there is environment-dependent.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	cfg := Config{BaseURL: addr, Email: testEmail, Token: testToken}
	c := NewClient(cfg, NewLogger("debug", &bytes.Buffer{}, cfg.Token))

	var out any
	err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out)
	if err == nil {
		t.Fatal("a connection failure must be an error")
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Errorf("err = %v, want a transport error rather than *APIError", err)
	}
	// The wrap must preserve the chain, which is what lets callers reach
	// context.Canceled and friends through it.
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Errorf("err = %v, want the underlying *url.Error to remain reachable", err)
	}
}

// Go randomises map iteration, so field-level errors must be sorted or the
// message differs between runs and cannot be asserted on.
func TestFieldLevelErrorsAreOrderedStably(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"errors":{"summary":"required","assignee":"unknown","fixVersions":"invalid"}}`)
	})

	// Asserted as one exact string rather than by sampling several runs and
	// hoping randomised map order shows itself: an unsorted implementation can
	// coincidentally produce the expected order every time it is sampled. Each
	// part is quoted because each is third-party text.
	const want = `"assignee: unknown"; "fixVersions: invalid"; "summary: required"`

	err := c.Do(context.Background(), http.MethodPut, "/x", nil, map[string]any{}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Message != want {
		t.Errorf("Message = %q, want %q", apiErr.Message, want)
	}
}

func TestEmptyErrorBodyStillProducesAMessage(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	var apiErr *APIError
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, nil); !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Message == "" {
		t.Error("an empty body must still yield a message, or the error reads as a bare status")
	}
}

// A zero or negative delay is not a backoff hint.
func TestRetryAfterNonPositiveIsIgnored(t *testing.T) {
	for _, header := range []string{"0", "-5"} {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", header)
			w.WriteHeader(http.StatusTooManyRequests)
		})
		var apiErr *APIError
		if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, nil); !errors.As(err, &apiErr) {
			t.Fatalf("err = %v, want *APIError", err)
		}
		if apiErr.RetryAfter != 0 {
			t.Errorf("Retry-After %q gave %v, want 0", header, apiErr.RetryAfter)
		}
	}
}

// A Retry-After date in the past is stale, not a negative wait.
func TestRetryAfterPastDateIsIgnored(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusTooManyRequests)
	})
	var apiErr *APIError
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, nil); !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.RetryAfter != 0 {
		t.Errorf("RetryAfter = %v, want 0 for a past date", apiErr.RetryAfter)
	}
}

// A body that cannot be marshalled must fail before any request is made.
func TestUnmarshalableBodyFailsBeforeSending(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made when the body cannot be marshalled")
	})
	err := c.Do(context.Background(), http.MethodPost, "/x", nil, make(chan int), nil)
	if err == nil {
		t.Fatal("an unmarshalable body must be an error")
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Errorf("err = %v, want a local error rather than *APIError", err)
	}
}

// The SSRF argument rests on this, not on caller discipline. Concatenating a
// path with no leading slash silently changes the host:
// "https://x.atlassian.net" + "rest/api" parses with host "x.atlassian.netrest".
func TestDoRejectsPathsThatCouldChangeTheDestination(t *testing.T) {
	for _, path := range []string{
		"rest/api/3/myself",      // no leading slash: changes the host
		"",                       // empty
		"/x?a=b",                 // query belongs in the query argument
		"/x#frag",                // fragment
		"https://evil.example/x", // an absolute URL
	} {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("path %q reached the network", path)
		})
		err := c.Do(context.Background(), http.MethodGet, path, nil, nil, nil)
		if err == nil {
			t.Errorf("path %q must be rejected", path)
			continue
		}
		if !strings.Contains(err.Error(), "invalid request path") {
			t.Errorf("path %q: err = %v, want it to name the path contract", path, err)
		}
	}
}

func TestDoAcceptsOrdinaryPaths(t *testing.T) {
	for _, path := range []string{"/", "/rest/api/3/myself", "/wiki/api/v2/pages/123"} {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
		if err := c.Do(context.Background(), http.MethodGet, path, nil, nil, nil); err != nil {
			t.Errorf("path %q: %v", path, err)
		}
	}
}

// The cap exists because error bodies are logged. An HTML proxy page must not
// be able to flood the log or the error message.
func TestErrorBodyIsTruncatedToTheCap(t *testing.T) {
	c, logs := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>" + strings.Repeat("A", 4*maxErrorBody) + "</html>"))
	})

	var apiErr *APIError
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, nil); !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}

	// The guarantee is that the *body* contributes at most maxErrorBody bytes.
	// A fixed-size annotation saying it was truncated is added on top, which is
	// the point of the message and is bounded by its own literal.
	const annotationAllowance = 128
	if len(apiErr.Message) > maxErrorBody+annotationAllowance {
		t.Errorf("Message is %d bytes, want at most %d plus a short annotation",
			len(apiErr.Message), maxErrorBody)
	}
	if !strings.Contains(apiErr.Message, "truncated") {
		t.Errorf("a truncated body must say so: %q", apiErr.Message[:min(200, len(apiErr.Message))])
	}
	if logs.Len() > maxErrorBody+1024 {
		t.Errorf("log grew to %d bytes; the cap is not bounding it", logs.Len())
	}
}

// An oversized body must fail whether or not the caller wants to decode it.
// Otherwise the same response succeeds or fails depending on the caller's
// interest in it.
func TestOversizedResponseFailsRegardlessOfOut(t *testing.T) {
	for _, wantBody := range []bool{true, false} {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("a", 64)))
		})
		c.maxBody = 16

		var out any
		var err error
		if wantBody {
			err = c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out)
		} else {
			err = c.Do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
		}
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Errorf("out set = %v: err = %v, want a size error", wantBody, err)
		}
	}
}

// A body exactly at the cap is not oversized.
func TestResponseExactlyAtTheCapSucceeds(t *testing.T) {
	const size = 32
	// Built and measured rather than computed by hand: an earlier version of
	// this test was one byte short and so exercised cap-1, which would not have
	// caught an off-by-one rejection at exactly the cap.
	payload := `{"k":"` + strings.Repeat("a", size-len(`{"k":""}`)) + `"}`
	if len(payload) != size {
		t.Fatalf("test fixture is %d bytes, want exactly %d", len(payload), size)
	}

	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, payload)
	})
	c.maxBody = size

	var out struct{ K string }
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out); err != nil {
		t.Errorf("a body of exactly %d bytes must succeed: %v", size, err)
	}
	if out.K == "" {
		t.Error("the body should have decoded")
	}
}

// This client's own deadline must be distinguishable from the caller cancelling,
// which errors.Is(err, context.DeadlineExceeded) alone cannot tell apart.
func TestClientTimeoutIsDistinguishableFromCallerCancellation(t *testing.T) {
	release := make(chan struct{})
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
	})
	t.Cleanup(func() { close(release) })
	c.timeout = 50 * time.Millisecond

	err := c.Do(context.Background(), http.MethodGet, "/slow", nil, nil, nil)
	if !errors.Is(err, ErrRequestTimeout) {
		t.Errorf("err = %v, want it to wrap ErrRequestTimeout", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, must not read as a caller cancellation", err)
	}
}

// Retry-After is useful on any failure, not only 429: Atlassian also sends it
// with 503 during maintenance.
func TestRetryAfterParsedOnServiceUnavailable(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "15")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	var apiErr *APIError
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, nil); !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.RetryAfter != 15*time.Second {
		t.Errorf("RetryAfter = %v, want 15s on a 503", apiErr.RetryAfter)
	}
}

// Atlassian's correlation id is what lets a logged failure be matched to a
// server-side trace in a support ticket.
func TestTraceIDIsCapturedAndLogged(t *testing.T) {
	c, logs := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Atl-Traceid", "abc123trace")
		w.WriteHeader(http.StatusInternalServerError)
	})
	var apiErr *APIError
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, nil); !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.TraceID != "abc123trace" {
		t.Errorf("TraceID = %q, want the upstream trace id", apiErr.TraceID)
	}
	if !strings.Contains(logs.String(), "abc123trace") {
		t.Errorf("the trace id must reach the log: %q", logs.String())
	}
}

// A decode failure on a non-JSON body should point at the network, not leave the
// reader hunting through their own structs.
func TestNonJSONBodyNamesTheContentType(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><body>Sign in to continue</body></html>")
	})
	var out struct{ K string }
	err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out)
	if err == nil {
		t.Fatal("HTML in place of JSON must be an error")
	}
	if !strings.Contains(err.Error(), "text/html") || !strings.Contains(err.Error(), "proxy") {
		t.Errorf("err = %v, want it to name the content type and suggest a proxy", err)
	}
}

// Valid JSON served with an unexpected content type still decodes: the type is
// used to explain a failure, never to reject a success.
func TestOddContentTypeStillDecodesValidJSON(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, `{"k":"value"}`)
	})
	var out struct{ K string }
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out.K != "value" {
		t.Errorf("K = %q, want the body decoded", out.K)
	}
}

// At info level a successful call must be silent, and a failure must not be.
func TestInfoLevelLogsFailuresOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, `{"k":"v"}`)
	}))
	t.Cleanup(srv.Close)

	cfg := Config{BaseURL: srv.URL, Email: testEmail, Token: testToken}
	logs := &bytes.Buffer{}
	c := NewClient(cfg, NewLogger("info", logs, cfg.Token))

	var out struct{ K string }
	if err := c.Do(context.Background(), http.MethodGet, "/ok", nil, nil, &out); err != nil {
		t.Fatalf("ok request: %v", err)
	}
	if logs.Len() != 0 {
		t.Errorf("a successful call must write nothing at info level: %q", logs.String())
	}

	_ = c.Do(context.Background(), http.MethodGet, "/fail", nil, nil, &out)
	if logs.Len() == 0 {
		t.Error("a failure must be logged at info level")
	}
}

// A 200 whose body is literally null leaves out at its zero value rather than
// erroring, the same as an empty body.
func TestNullBodyLeavesOutAtZeroValue(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `null`)
	})
	out := struct{ K string }{K: "untouched"}
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out.K != "untouched" {
		t.Errorf("K = %q; a null body should leave out alone", out.K)
	}
}

// The message travels back to the caller inside the error, and from there into
// an MCP tool result. Redacting only at the log would let an upstream body that
// echoes the credential escape without ever being logged.
func TestCredentialsNeverReachTheReturnedError(t *testing.T) {
	credential := BasicCredential(testEmail, testToken)
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"errorMessages":["rejected `+testToken+` and Basic `+credential+`"]}`)
	})

	err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	for name, secret := range map[string]string{"token": testToken, "Basic credential": credential} {
		if strings.Contains(apiErr.Message, secret) {
			t.Errorf("the %s leaked into APIError.Message", name)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the %s leaked into the returned error string", name)
		}
	}
}

// RFC 9110 delay-seconds is digits only, and an arbitrary integer multiplied by
// time.Second overflows into a negative duration.
func TestRetryAfterRejectsNonRFCAndSaturatesOverflow(t *testing.T) {
	// A slice rather than a map: one case deliberately has leading whitespace,
	// which reads as a mistake in a map key, and the order is then deterministic.
	cases := []struct {
		name   string
		header string
		want   func(time.Duration) bool
	}{
		{"signed is not delay-seconds", "+30", func(d time.Duration) bool { return d == 0 }},
		{"surrounding space is trimmed", " 30 ", func(d time.Duration) bool { return d == 30*time.Second }},
		{"decimal is not delay-seconds", "30.5", func(d time.Duration) bool { return d == 0 }},
		{"exponent is not delay-seconds", "1e3", func(d time.Duration) bool { return d == 0 }},
		{"beyond int64 saturates", "99999999999999999999",
			func(d time.Duration) bool { return d == maxRetryAfterSeconds*time.Second }},
		{"int64 max saturates", "9223372036854775807",
			func(d time.Duration) bool { return d == maxRetryAfterSeconds*time.Second }},
		{"absurd but in range saturates", "999999999",
			func(d time.Duration) bool { return d == maxRetryAfterSeconds*time.Second }},
	}

	for _, tc := range cases {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", tc.header)
			w.WriteHeader(http.StatusTooManyRequests)
		})
		var apiErr *APIError
		if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, nil); !errors.As(err, &apiErr) {
			t.Fatalf("%s: err = %v, want *APIError", tc.name, err)
		}
		if !tc.want(apiErr.RetryAfter) {
			t.Errorf("%s: Retry-After %q gave %v", tc.name, tc.header, apiErr.RetryAfter)
		}
		if apiErr.RetryAfter < 0 {
			t.Errorf("%s: Retry-After %q overflowed to %v", tc.name, tc.header, apiErr.RetryAfter)
		}
	}
}

// A trace id is copied into a log line, so an unbounded or exotic header value
// would walk straight past the error-body cap.
func TestTraceIDIsBoundedAndValidated(t *testing.T) {
	// A NUL or a newline in a header value never reaches this code: Go's
	// transport rejects the malformed MIME header first. Only values a real
	// server can actually send are worth asserting on here.
	for name, value := range map[string]string{
		"too long": strings.Repeat("a", maxTraceID+1),
		"spaces":   "abc def",
		"brackets": "abc[def]",
	} {
		c, logs := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header()["Atl-Traceid"] = []string{value}
			w.WriteHeader(http.StatusInternalServerError)
		})
		var apiErr *APIError
		if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, nil); !errors.As(err, &apiErr) {
			t.Fatalf("%s: err = %v, want *APIError", name, err)
		}
		if apiErr.TraceID != "" {
			t.Errorf("%s: TraceID = %q, want it discarded", name, apiErr.TraceID)
		}
		if logs.Len() > 4096 {
			t.Errorf("%s: log grew to %d bytes", name, logs.Len())
		}
	}
}

// A proxy or login page is not JSON, so the fallback hands back the raw body.
// That text ends up in a log line and in a tool result, so it must be short and
// must not carry raw newlines or control characters: a newline lets the body
// forge a second log line, and 8 KiB of HTML is not a diagnostic.
func TestRawBodyFallbackIsCappedAndQuoted(t *testing.T) {
	body := "<html>\n<head><title>Sign in</title></head>\n<body>\nERROR forged line\n" +
		strings.Repeat("<p>padding</p>\n", 200) + "</body>\n</html>\n"
	if len(body) < 2048 {
		t.Fatalf("fixture is %d bytes, want at least 2 KiB", len(body))
	}

	got := upstreamMessage([]byte(body))
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("message carries a raw line break: %q", got)
	}
	if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Errorf("message must be a quoted Go string: %q", got)
	}
	unquoted, err := strconv.Unquote(got)
	if err != nil {
		t.Fatalf("message is not strconv.Quote output: %v: %q", err, got)
	}
	if !strings.HasSuffix(unquoted, "…") {
		t.Errorf("a truncated body must end with an ellipsis: %q", unquoted)
	}
	if n := len(strings.TrimSuffix(unquoted, "…")); n > maxRawMessageBytes {
		t.Errorf("kept %d bytes of body, want at most %d", n, maxRawMessageBytes)
	}
	if !strings.HasPrefix(unquoted, "<html>") {
		t.Errorf("the start of the body must survive so the reader can recognise it: %q", unquoted)
	}
	if strings.Contains(got, "</html>") {
		t.Error("the tail of a 2 KiB body must not survive the cap")
	}
}

// The cut must fall on a rune boundary, or the message is invalid UTF-8 and
// strconv.Quote renders the split bytes as \x escapes.
func TestRawBodyFallbackTruncatesOnRuneBoundary(t *testing.T) {
	body := strings.Repeat("é", maxRawMessageBytes) // 2 bytes each, so the cap falls mid-rune
	got, err := strconv.Unquote(upstreamMessage([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncation split a rune: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("want an ellipsis after the cut: %q", got)
	}
}

// A short body is quoted but otherwise intact.
func TestRawBodyFallbackShortBodyIsQuotedWhole(t *testing.T) {
	got := upstreamMessage([]byte("  Bad gateway\n"))
	if got != strconv.Quote("Bad gateway") {
		t.Errorf("got %q, want %q", got, strconv.Quote("Bad gateway"))
	}
}

// The JSON-shaped branch carries third-party text just as the raw-body fallback
// does: errorMessages, the per-field errors and message are all written by
// Atlassian or echoed from a caller. A newline there would forge a second log
// line exactly as one in an HTML body would, and an unbounded message would
// push everything else off the line, so each part is capped and quoted before
// the parts are joined.
func TestJSONErrorMessagesAreCappedAndQuoted(t *testing.T) {
	long := strings.Repeat("z", 2048)
	body := `{"errorMessages":["boom\nERROR forged","` + long + `"],"message":"tail\r\nsecond"}`

	got := upstreamMessage([]byte(body))
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("message carries a raw line break: %q", got)
	}
	if strings.Contains(got, "ERROR forged\n") {
		t.Error("a forged log line survived")
	}

	parts := strings.Split(got, "; ")
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want 3: %q", len(parts), got)
	}
	for i, p := range parts {
		unquoted, err := strconv.Unquote(p)
		if err != nil {
			t.Fatalf("part %d is not strconv.Quote output: %v: %q", i, err, p)
		}
		if n := len(strings.TrimSuffix(unquoted, "…")); n > maxRawMessageBytes {
			t.Errorf("part %d kept %d bytes, want at most %d", i, n, maxRawMessageBytes)
		}
	}
	if first, err := strconv.Unquote(parts[0]); err != nil || first != "boom\nERROR forged" {
		t.Errorf("part 0 = %q, want the escaped form of the original text", parts[0])
	}
	if !strings.HasSuffix(strings.TrimSuffix(parts[1], `"`), "…") {
		t.Errorf("the 2 KiB part must be truncated with an ellipsis: %q", parts[1])
	}
	if strings.Contains(got, long) {
		t.Error("an unbounded message survived the cap")
	}
}

// The Content-Type is chosen by whatever answered the request — the real
// endpoint, a hostile one, or an intercepting proxy — so on this error path it
// is third-party text and must be quoted and bounded like every other such
// string. What can actually arrive is narrower than it looks: Go's transport
// refuses a header line carrying a control byte, and an unparseable media type
// never reaches this branch because isJSONContentType lets the decoder produce
// the error instead. A parseable type with a hostile quoted parameter passes
// both gates, and that is what this sends: a tab and message-shaped
// punctuation, followed by 2 KiB of filler that would otherwise push the rest
// of the message out of the log line and out of the tool result.
func TestNonJSONContentTypeIsQuotedAndBounded(t *testing.T) {
	hostile := "text/html; x=\"\tERROR forged entry " + strings.Repeat("A", 2048) + "\""
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", hostile)
		_, _ = io.WriteString(w, "<html><body>Sign in</body></html>")
	})

	var out struct{ K string }
	err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out)
	if err == nil {
		t.Fatal("HTML in place of JSON must be an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"text/html`) {
		t.Errorf("error = %q, want the content type quoted", msg)
	}
	if strings.Contains(msg, "\t") {
		t.Errorf("error carries a raw tab: %q", msg)
	}
	if !strings.Contains(msg, `\t`) {
		t.Errorf("error = %q, want the tab rendered as an escape", msg)
	}
	if len(msg) > 512 {
		t.Errorf("error is %d bytes long; the content type must be bounded: %q", len(msg), msg)
	}
	if !strings.Contains(msg, "…") {
		t.Errorf("error = %q, want the truncation marked", msg)
	}
}
