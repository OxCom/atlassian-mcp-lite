package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// requestTimeout bounds a single Atlassian call. Long enough for a slow JQL
	// search, short enough that a hung request does not hold an MCP tool call
	// open indefinitely.
	requestTimeout = 30 * time.Second

	// maxErrorBody caps how much of a failing response is read. Error bodies
	// are logged, so an HTML proxy page must not be able to flood the log.
	maxErrorBody = 8 << 10 // 8 KiB

	// maxResponseBody caps a successful response. The whole body is buffered to
	// decode it, so without a cap a runaway or hostile response is a memory
	// exhaustion primitive.
	maxResponseBody = 8 << 20 // 8 MiB

	// Transport tuning. Only one host is ever contacted, so the idle pool is
	// small on purpose; the timeouts exist so a stalled TLS handshake or a
	// half-open connection cannot consume the whole request budget.
	dialTimeout           = 10 * time.Second
	dialKeepAlive         = 30 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	expectContinueTimeout = 1 * time.Second
	idleConnTimeout       = 90 * time.Second
	maxIdleConns          = 4
)

// Version is reported in the User-Agent and to MCP clients during initialize.
//
// A var rather than a const so a release build can stamp the real tag with
// -ldflags "-X github.com/OxCom/atlassian-mcp-lite/internal/core.Version=v1.2.3".
// The linker silently ignores an -X naming a symbol it cannot set, so a const
// here meant every release binary reported this default and nobody was told.
var Version = "0.1.0"

// userAgent identifies this client in Atlassian's logs, which is what makes a
// misbehaving integration attributable. A var for the same reason as Version,
// which it is built from.
var userAgent = "atlassian-mcp-lite/" + Version

// APIError is a response outside 2xx, carrying the upstream message because
// Atlassian's own error text is the useful half of a failure.
type APIError struct {
	Status  int
	Method  string
	Path    string
	Message string

	// RetryAfter is the parsed Retry-After header, so a caller has a backoff
	// signal rather than only a status code. Zero when absent or unparseable.
	RetryAfter time.Duration

	// TraceID is Atlassian's own correlation id for the failing request, when
	// it sent one. It is what makes a logged failure matchable to a server-side
	// trace in a support ticket.
	TraceID string
}

func (e *APIError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%s %s: HTTP %d: %s (retry after %s)",
			e.Method, e.Path, e.Status, e.Message, e.RetryAfter)
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Message)
}

// ErrRequestTimeout is the cause when this client's own deadline expires. It
// exists so a caller can tell our timeout from its own context being cancelled,
// which errors.Is(err, context.DeadlineExceeded) alone cannot do.
var ErrRequestTimeout = errors.New("atlassian request timed out")

// errBadPath is returned for a path that could change the destination.
var errBadPath = errors.New("invalid request path")

// pathRe is the whole of the path contract: an absolute path with no query and
// no fragment. Both halves matter — see Do.
var pathRe = regexp.MustCompile(`^/[^?#]*$`)

// delaySecondsRe is RFC 9110 delay-seconds: digits only, no sign. Go's \d is
// exactly [0-9], so this stays ASCII-only as the RFC requires.
var delaySecondsRe = regexp.MustCompile(`^\d+$`)

// maxRetryAfterSeconds saturates an absurd backoff hint. A day is far longer
// than any caller would wait, and the cap keeps the multiplication below the
// point where time.Duration overflows into a negative value.
const maxRetryAfterSeconds = 24 * 60 * 60

// Client is the only way a module may reach Atlassian.
//
// The base URL is fixed at construction from validated configuration and is
// never derived from tool input, and no method accepts a URL. That is what
// makes SSRF unreachable here: there is no code path by which model output
// chooses a destination host.
type Client struct {
	base  string
	email string
	token string
	log   *Logger
	http  *http.Client

	// timeout and maxBody are fields rather than constants so tests can drive
	// the paths that depend on them without a 30-second wait or an 8 MiB
	// fixture.
	timeout time.Duration
	maxBody int

	// guard refuses a connection before it is made unless the destination is
	// the configured host resolving to a globally routable address.
	guard *dialGuard
}

// NewClient builds a Client bound to cfg.BaseURL.
func NewClient(cfg Config, log *Logger) *Client {
	base := strings.TrimRight(cfg.BaseURL, "/")

	// The host and the loopback exemption both come from configuration that
	// validateBaseURL has already checked, so the guard is pinned to exactly
	// one destination and only relaxes for a base URL that is itself loopback.
	host := ""
	allowLocal := false
	if u, err := url.Parse(base); err == nil {
		host = u.Hostname()
		allowLocal = isLoopback(host)
	}
	guard := newDialGuard(host, allowLocal)

	return &Client{
		base:    base,
		email:   cfg.Email,
		token:   cfg.Token,
		log:     log,
		timeout: requestTimeout,
		maxBody: maxResponseBody,
		guard:   guard,
		http: &http.Client{
			// No http.Client.Timeout: Do sets a context deadline instead, so
			// the expiry carries ErrRequestTimeout as its cause and is
			// distinguishable from a caller cancelling.
			//
			// Redirects are never followed. Atlassian's REST API does not
			// redirect, so a 3xx is a misconfiguration or an open-redirect
			// attempt; returning it unfollowed both avoids sending the
			// credential onward and surfaces the status to the caller.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: &http.Transport{
				// Every connection goes through the guard, so the destination
				// is checked at the point of dialling rather than only at the
				// point of building the URL.
				DialContext:           guard.dialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          maxIdleConns,
				IdleConnTimeout:       idleConnTimeout,
				TLSHandshakeTimeout:   tlsHandshakeTimeout,
				ExpectContinueTimeout: expectContinueTimeout,
			},
		},
	}
}

// Do performs a request against path, which must be a server-controlled
// constant with any interpolated segments already escaped by the caller.
//
// body, when non-nil, is sent as JSON. out, when non-nil, receives the decoded
// 2xx response. A response outside 2xx is returned as *APIError.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	// The SSRF argument depends on this check, not on caller discipline.
	// Concatenating a path that does not begin with "/" changes the host:
	// "https://x.atlassian.net" + "rest/api" parses with host
	// "x.atlassian.netrest". A "?" or "#" in the path would likewise
	// reinterpret everything after it, and would collide with query.
	if !pathRe.MatchString(path) {
		return fmt.Errorf("%w %q: must be an absolute path with no query or fragment", errBadPath, path)
	}

	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	var reader io.Reader
	var sent int
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s %s: marshal request body: %w", method, path, err)
		}
		sent = len(buf)
		reader = bytes.NewReader(buf)
	}

	// The deadline is set here rather than on http.Client so that expiry
	// carries ErrRequestTimeout as its cause.
	ctx, cancel := context.WithTimeoutCause(ctx, c.timeout, ErrRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("%s %s: build request: %w", method, path, err)
	}
	req.SetBasicAuth(c.email, c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		// Only when there is a body: a Content-Type on a bodyless request
		// claims content that is not there.
		req.Header.Set("Content-Type", "application/json")
	}

	start := time.Now()
	res, err := c.http.Do(req)
	if err != nil {
		// url.Error implements Unwrap, so wrapping with %w keeps the chain
		// intact and errors.Is reaches context.Canceled or the timeout cause.
		if cause := context.Cause(ctx); cause != nil && errors.Is(cause, ErrRequestTimeout) {
			err = fmt.Errorf("%w after %s: %w", ErrRequestTimeout, elapsed(start), err)
		}
		c.log.Errorf("%s %s: transport error after %s: %v", method, path, elapsed(start), err)
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()

	// Anything outside 2xx is a failure. Checking only >= 400 would let an
	// unfollowed 3xx be decoded as a successful result.
	if res.StatusCode < http.StatusOK || res.StatusCode > 299 {
		return c.apiError(res, method, path, start)
	}

	received, err := c.decode(res, out)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}

	// Shape, never content: a successful body is business data and is not
	// logged at any level. Headers are masked even here, because the request
	// carries the Authorization header.
	c.log.Debugf("%s %s -> %d in %s (sent %dB, received %dB) headers=%v",
		method, path, res.StatusCode, elapsed(start), sent, received, MaskHeaders(req.Header))
	return nil
}

// apiError builds the error for a non-2xx response and logs it. The body is
// read and logged deliberately: it is where Atlassian's diagnostics live, and
// the logger redacts configured secrets from it.
func (c *Client) apiError(res *http.Response, method, path string, start time.Time) error {
	// One byte past the cap, so a truncated body is detectable rather than
	// silently indistinguishable from a body that happened to end there.
	raw, readErr := io.ReadAll(io.LimitReader(res.Body, maxErrorBody+1))
	truncated := len(raw) > maxErrorBody
	if truncated {
		raw = raw[:maxErrorBody]
	}

	message := upstreamMessage(raw)
	if readErr != nil {
		message += " (the response body was cut short: " + readErr.Error() + ")"
	}
	if truncated {
		message += fmt.Sprintf(" (truncated at %d bytes)", maxErrorBody)
	}

	// Redaction happens here, not only at the log. This message travels back to
	// the caller inside the error, and from there into an MCP tool result — so
	// an upstream body echoing the credential would escape without ever being
	// logged.
	apiErr := &APIError{
		Status:  res.StatusCode,
		Method:  method,
		Path:    path,
		Message: c.log.Redact(message),
		TraceID: traceID(res.Header),
		// Parsed on any failure, not only 429: Atlassian also sends it with
		// 503 during maintenance and rate limiting, and a backoff hint is
		// just as useful there.
		RetryAfter: parseRetryAfter(res.Header.Get("Retry-After")),
	}

	line := fmt.Sprintf("%s %s -> %d in %s: ", method, path, res.StatusCode, elapsed(start)) + apiErr.Message
	if apiErr.TraceID != "" {
		line += " [trace " + apiErr.TraceID + "]"
	}
	c.log.Error(line)
	return apiErr
}

const (
	// maxTraceID bounds an upstream correlation id. It is copied into a log
	// line, so without a limit a hostile or broken response header could add
	// megabytes to one entry and walk straight past the error-body cap.
	maxTraceID = 200
)

// traceHeaders are the correlation ids Atlassian returns. Recording one is what
// lets a logged failure be matched to a server-side trace in a support ticket.
var traceHeaders = []string{"X-Atlassian-Request-Id", "Atl-Traceid", "X-Arequestid", "X-Trace-Id"}

// traceIDRe is the shape a correlation id may take. Anything else is discarded
// rather than sanitised: a trace id that needs escaping is not a trace id.
var traceIDRe = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

func traceID(h http.Header) string {
	for _, name := range traceHeaders {
		v := strings.TrimSpace(h.Get(name))
		if v == "" || len(v) > maxTraceID || !traceIDRe.MatchString(v) {
			continue
		}
		return v
	}
	return ""
}

// decode reads a successful body, enforcing the size cap, and unmarshals it
// into out when out is non-nil. It returns the number of bytes read.
func (c *Client) decode(res *http.Response, out any) (int, error) {
	// One byte past the cap, so a body exactly at the limit still succeeds and
	// anything larger is detectable. Applied whether or not the caller wants
	// the body: an oversized response must be an error either way, or the same
	// response would succeed or fail depending on the caller's interest in it.
	raw, err := io.ReadAll(io.LimitReader(res.Body, int64(c.maxBody)+1))
	if err != nil {
		return len(raw), fmt.Errorf("read body: %w", err)
	}
	if len(raw) > c.maxBody {
		return len(raw), fmt.Errorf("response too large: over %d bytes", c.maxBody)
	}
	if out == nil {
		return len(raw), nil
	}
	if len(raw) == 0 || isJSONNull(raw) {
		// A 204, a 200 with nothing in it, or a body that is literally `null`.
		// All three mean the server sent no value, so out is left untouched
		// rather than zeroed: "no content" and "the zero value" are different
		// answers, and only the caller knows which its own zero value means.
		return len(raw), nil
	}

	// Decoded into a fresh value and only then assigned, so a malformed body
	// cannot leave out half-populated. json.Unmarshal writes fields as it goes
	// and can fail partway, which would hand the caller a struct that is
	// neither the old value nor the new one.
	target := reflect.ValueOf(out)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return len(raw), fmt.Errorf("decode body: out must be a non-nil pointer, got %T", out)
	}
	scratch := reflect.New(target.Type().Elem())

	if err := json.Unmarshal(raw, scratch.Interface()); err != nil {
		// The content type is used to explain a failure, never to reject a
		// success: valid JSON served with an odd type still decodes. But when
		// the decode does fail, "invalid character '<'" sends the reader
		// hunting through their own code, whereas naming the content type
		// points at the network — an intercepting proxy's HTML login page is
		// the realistic cause.
		if ct := res.Header.Get("Content-Type"); ct != "" && !isJSONContentType(ct) {
			return len(raw), fmt.Errorf("expected JSON but the response is %s, so a proxy or login page may have intercepted the request: %w", ct, err)
		}
		return len(raw), fmt.Errorf("decode body: %w", err)
	}
	target.Elem().Set(scratch.Elem())
	return len(raw), nil
}

// isJSONContentType accepts application/json and the +json structured suffix,
// ignoring any parameters such as charset.
func isJSONContentType(ct string) bool {
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		// Unparseable: let the JSON decoder produce the error rather than
		// rejecting a response that might be perfectly good.
		return true
	}
	return mediaType == "application/json" ||
		mediaType == "text/json" ||
		strings.HasSuffix(mediaType, "+json")
}

// isJSONNull reports whether raw is the JSON null literal, ignoring surrounding
// whitespace.
func isJSONNull(raw []byte) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// upstreamMessage extracts a human-readable message from an Atlassian error
// body, falling back to the raw text so a proxy's plain-text or HTML response
// is not swallowed.
func upstreamMessage(raw []byte) string {
	var shape struct {
		ErrorMessages []string `json:"errorMessages"`
		// Deliberately raw: Atlassian uses two different shapes for this
		// member, and decoding it as either one makes the other fail the whole
		// unmarshal, dropping the caller into the raw-body fallback.
		Errors  json.RawMessage `json:"errors"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(raw, &shape); err == nil {
		parts := make([]string, 0, len(shape.ErrorMessages)+1)
		parts = append(parts, shape.ErrorMessages...)
		parts = append(parts, errorDetails(shape.Errors)...)
		if shape.Message != "" {
			parts = append(parts, shape.Message)
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
	}
	if s := strings.TrimSpace(string(raw)); s != "" {
		return s
	}
	return "(empty error body)"
}

// errorDetails reads both shapes Atlassian uses for the "errors" member: Jira's
// object of field name to message, and Confluence v2's array of objects
// carrying a title and detail. Handling only the first left every Confluence v2
// failure reaching the model as a raw JSON blob with the useful sentence buried
// inside it.
func errorDetails(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}

	var fieldErrors map[string]string
	if json.Unmarshal(raw, &fieldErrors) == nil {
		out := make([]string, 0, len(fieldErrors))
		// Sorted, so the message is stable across runs: Go randomises map
		// iteration order and an unstable error string is untestable.
		for _, k := range sortedKeys(fieldErrors) {
			out = append(out, k+": "+fieldErrors[k])
		}
		return out
	}

	var list []struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Code   string `json:"code"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		switch {
		case e.Title != "" && e.Detail != "":
			out = append(out, e.Title+": "+e.Detail)
		case e.Title != "":
			out = append(out, e.Title)
		case e.Detail != "":
			out = append(out, e.Detail)
		case e.Code != "":
			out = append(out, e.Code)
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Small maps; a simple insertion sort avoids pulling in sort for this.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// parseRetryAfter handles both forms the header may take: delay-seconds, or an
// HTTP date. An unparseable value yields zero rather than an error, because a
// missing backoff hint must not fail the call.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}

	// RFC 9110 delay-seconds is 1*DIGIT: no sign, no decimal point. Using
	// strconv.Atoi alone would accept "+30", and multiplying an arbitrary
	// integer by time.Second overflows into a negative duration for values
	// above about 292 years.
	if delaySecondsRe.MatchString(v) {
		secs, err := strconv.ParseInt(v, 10, 64)
		switch {
		case err != nil:
			// The regex guarantees digits only, so the sole way ParseInt can
			// fail here is a value beyond int64. That is still the server
			// asking us to wait a long time, so it saturates rather than
			// discarding the hint entirely.
			return maxRetryAfterSeconds * time.Second
		case secs <= 0:
			return 0
		case secs > maxRetryAfterSeconds:
			return maxRetryAfterSeconds * time.Second
		default:
			return time.Duration(secs) * time.Second
		}
	}

	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			if d > maxRetryAfterSeconds*time.Second {
				return maxRetryAfterSeconds * time.Second
			}
			return d
		}
	}
	return 0
}

func elapsed(start time.Time) time.Duration {
	return time.Since(start).Round(time.Millisecond)
}
