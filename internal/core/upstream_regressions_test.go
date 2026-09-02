package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tests in this file are regressions against defects reported publicly in
// sooperset/mcp-atlassian, the Python MCP server for the same two products.
// None of them is a defect here — each names the control that makes the
// equivalent attack impossible in this codebase, so that a later change which
// removes the control fails a test that says why the control existed.
//
// The advisories are cited by identifier. They are not our history; they are a
// list of the ways a server with this job has actually been broken.

// TestBaseURLRejectsAuthorityParserConfusion covers CVE-2026-77274: a URL that
// one parser reads as a hostile authority and another as an innocent one. The
// reported payload was http://127.0.0.1:6666\@www.baidu.com, where urlparse saw
// www.baidu.com and the HTTP client connected to 127.0.0.1.
//
// Two independent controls stop it here, and this test wants both: validateBaseURL
// refuses userinfo outright, and the dial guard resolves the configured host and
// checks the address it will actually connect to, so a disagreement between
// parsers cannot produce a connection to a host that was never validated.
func TestBaseURLRejectsAuthorityParserConfusion(t *testing.T) {
	for name, raw := range map[string]string{
		"backslash before at, loopback": `http://127.0.0.1:6666\@www.baidu.com`,
		"backslash before at, https":    `https://169.254.169.254\@example.atlassian.net`,
		"userinfo hides the real host":  "https://example.atlassian.net@169.254.169.254",
		"two at signs":                  "https://a@b@example.atlassian.net",
		"backslash in host":             `https://example.atlassian.net\.evil.example`,
		"tab inside the host":           "https://example.atlassian\t.net",
		"newline inside the host":       "https://example.atlassian\n.net",
		"CRLF request splitting":        "https://example.atlassian.net\r\nHost: evil.example",
		"space inside the host":         "https://example.atlassian.net evil.example",
	} {
		_, err := Load(env(map[string]string{
			"ATLAS_BASE_URL": raw,
			"ATLAS_EMAIL":    "a@b.c",
			"ATLAS_TOKEN":    fixtureToken,
		}), []string{"jira"})
		if err == nil {
			t.Errorf("%s (%q) must be rejected", name, raw)
			continue
		}
		// The error must not echo the value: a malformed base URL is the input
		// most likely to carry credentials, and *url.Error prints the whole URL.
		if strings.Contains(err.Error(), fixtureToken) {
			t.Errorf("%s: error quotes the token: %v", name, err)
		}
	}
}

// TestGuardRefusesAHostThatRebindsBetweenConnections covers CVE-2026-73497: the
// validator resolved the name, returned a verdict rather than an address, and
// the connection re-resolved — so a DNS answer that changed in between reached
// 169.254.169.254 through a check that had passed.
//
// Here resolve returns the addresses themselves and dialContext connects to
// those, so there is no second lookup inside one connection. A later answer
// that flips to an internal address does not poison a connection already
// validated; it is refused on its own attempt.
func TestGuardRefusesAHostThatRebindsBetweenConnections(t *testing.T) {
	var calls atomic.Int64
	g := &dialGuard{
		allowedHost: "example.atlassian.net",
		lookupIP: func(context.Context, string) ([]netip.Addr, error) {
			// First answer public, every later one the cloud metadata service.
			if calls.Add(1) == 1 {
				return []netip.Addr{netip.MustParseAddr("104.192.142.1")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
		},
	}

	first, err := g.resolve(context.Background(), "example.atlassian.net:443")
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if len(first) != 1 || first[0].Addr().String() != "104.192.142.1" {
		t.Fatalf("first resolve = %v, want the public address only", first)
	}

	if _, err := g.resolve(context.Background(), "example.atlassian.net:443"); err == nil {
		t.Fatal("the rebound answer must be refused")
	} else if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("err = %v, want ErrBlockedAddress", err)
	}

	// The address the caller connects to is the one that was checked, not the
	// name. A dial that reached the resolver again would be the rebinding
	// window this test exists to keep closed.
	if _, err := g.dialContext(context.Background(), "tcp", "example.atlassian.net:443"); err == nil {
		t.Error("dialContext must refuse once the host resolves internally")
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("lookups = %d, want 3 — one per resolve, none extra at dial time", got)
	}
}

// TestGuardRefusesAMixedAnswerContainingOnlyOnePublicAddress is the other half
// of the rebinding class: an answer that is partly routable must not make the
// non-routable entries dialable, because the dialer walks the list on failure.
func TestGuardRefusesAMixedAnswerContainingOnlyOnePublicAddress(t *testing.T) {
	g := &dialGuard{
		allowedHost: "example.atlassian.net",
		lookupIP: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{
				netip.MustParseAddr("169.254.169.254"),
				netip.MustParseAddr("104.192.142.1"),
				netip.MustParseAddr("127.0.0.1"),
			}, nil
		},
	}
	endpoints, err := g.resolve(context.Background(), "example.atlassian.net:443")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, e := range endpoints {
		if !addrIsGloballyRoutable(e.Addr()) {
			t.Errorf("%s must not be offered to the dialer", e)
		}
	}
	if len(endpoints) != 1 {
		t.Errorf("endpoints = %v, want only the routable one", endpoints)
	}
}

// TestWriteAllowlistIsExactAndNotASubstring covers CVE-2026-77251, where the
// project and space allowlists were enforced by asking whether the caller's
// query mentioned a project at all — so naming any project satisfied the guard
// — and by a case-sensitive substring test on the Confluence side.
//
// allowed() is an exact, case-insensitive comparison against each entry. A key
// that merely contains, extends or is extended by an allowed key is not
// allowed, in either direction.
func TestWriteAllowlistIsExactAndNotASubstring(t *testing.T) {
	cfg := Config{WriteProjects: []string{"PROJ"}, WriteSpaces: []string{"ENG"}}

	for _, key := range []string{"PROJ", "proj", "Proj", " PROJ ", "pRoJ"} {
		if !cfg.AllowProject(key) {
			t.Errorf("AllowProject(%q) = false, want true — matching is case-insensitive", key)
		}
	}
	for _, key := range []string{
		"PROJECT",    // the allowed key is a prefix of this one
		"XPROJ",      // and a suffix of this one
		"PROJ2",      //
		"PR",         // a prefix of the allowed key
		"PROJ,OPS",   // the whole list as one value
		"PROJ OPS",   //
		"OPS",        //
		"",           //
		"PROJ-123",   // an issue key, not a project key
		"P%",         // a wildcard, in case anything downstream expands one
		"*",          //
		`PROJ" OR "`, // an injection attempt that contains the allowed key
	} {
		if cfg.AllowProject(key) {
			t.Errorf("AllowProject(%q) = true, want false", key)
		}
	}
	for _, key := range []string{"ENGINEERING", "EN", "XENG", "~eng", "eng2"} {
		if cfg.AllowSpace(key) {
			t.Errorf("AllowSpace(%q) = true, want false", key)
		}
	}
	// A personal space key is matched whole, tilde included.
	tilde := Config{WriteSpaces: []string{"~jdoe"}}
	if !tilde.AllowSpace("~JDOE") {
		t.Error(`AllowSpace("~JDOE") = false, want true`)
	}
	if tilde.AllowSpace("jdoe") {
		t.Error(`AllowSpace("jdoe") = true, want false — the tilde is part of the key`)
	}
}

// TestDisabledToolIsUnknownToTheDispatcher covers CVE-2026-77243: the tool
// allowlist filtered tools/list but not the call handler, so a hidden tool was
// still invocable by name.
//
// Registry.Enabled drops a disabled declaration before the server is built, so
// there is no handler to reach. The call must fail, and it must fail the same
// way a genuinely unknown name does — a distinguishable error would tell an
// attacker which capabilities the operator turned off.
func TestDisabledToolIsUnknownToTheDispatcher(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{
		decl("fake_read", ActionRead),
		decl("fake_nuke", ActionDestructive),
	}})

	sess := connectFakeServer(t, r, Config{Domains: map[string]Caps{"fake": {Read: true}}})
	ctx := context.Background()

	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "fake_nuke" {
			t.Fatal("a destructive tool must not be advertised when destructive is off")
		}
	}

	_, disabledErr := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "fake_nuke", Arguments: map[string]any{"key": "x"},
	})
	if disabledErr == nil {
		t.Fatal("calling a disabled tool by name must fail")
	}
	_, unknownErr := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "fake_never_existed", Arguments: map[string]any{"key": "x"},
	})
	if unknownErr == nil {
		t.Fatal("calling an unknown tool must fail")
	}

	// Byte-identical but for the name: nothing in the message may reveal that
	// one of the two was configured off rather than never declared.
	normalise := func(err error) string {
		return strings.ReplaceAll(strings.ReplaceAll(err.Error(),
			"fake_nuke", "NAME"), "fake_never_existed", "NAME")
	}
	if got, want := normalise(disabledErr), normalise(unknownErr); got != want {
		t.Errorf("disabled tool error %q differs from unknown tool error %q", got, want)
	}
}

// TestHandlerFailureIsReportedAsAnError covers the class in upstream PR 1413,
// where several handlers returned a failure as ordinary success content with
// isError unset, so the model read the failure as a completed action.
//
// A handler error here becomes a tool result with IsError set, and the text
// carries the notice that says the quoted upstream message is data.
func TestHandlerFailureIsReportedAsAnError(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{{
		Name:        "fake_fail",
		Actions:     []Action{ActionRead},
		Description: "fails",
		Schema: func(Caps) *jsonschema.Schema {
			return ObjectSchema(map[string]*jsonschema.Schema{"key": {Type: "string"}}, []string{"key"})
		},
		Handle: func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("upstream said no")
		},
	}}})

	sess := connectFakeServer(t, r, Config{Domains: map[string]Caps{"fake": {Read: true}}})
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "fake_fail", Arguments: map[string]any{"key": "x"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("a handler error must set IsError; a failure read as success is what makes the model act on it")
	}
	text := resultText(t, res)
	if !strings.Contains(text, "upstream said no") {
		t.Errorf("result = %q, want the upstream message", text)
	}
	if !strings.Contains(text, ErrorNotice) {
		t.Errorf("result = %q, want the untrusted-data notice", text)
	}
}

// TestNoConfigurationDisablesTLSVerification covers the SSL_VERIFY=false class:
// upstream offers per-product switches that turn certificate verification off,
// and every one of them has been part of a bypass at some point.
//
// There is no such setting here. Load reads a fixed set of keys, and this test
// asserts that none of the usual spellings does anything at all — an unknown
// key is ignored, so a copied config line cannot silently weaken TLS.
func TestNoConfigurationDisablesTLSVerification(t *testing.T) {
	base := map[string]string{
		"ATLAS_BASE_URL": "https://example.atlassian.net",
		"ATLAS_EMAIL":    "a@b.c",
		"ATLAS_TOKEN":    fixtureToken,
	}
	for _, key := range []string{
		"ATLAS_SSL_VERIFY", "ATLAS_TLS_VERIFY", "ATLAS_INSECURE",
		"ATLAS_SKIP_TLS_VERIFY", "ATLAS_JIRA_SSL_VERIFY", "ATLAS_VERIFY_SSL",
	} {
		withKey := map[string]string{}
		for k, v := range base {
			withKey[k] = v
		}
		withKey[key] = "false"

		cfg, err := Load(env(withKey), []string{"jira"})
		if err != nil {
			t.Fatalf("%s: Load: %v", key, err)
		}
		client := NewClient(cfg, NewLogger("error", nil))
		transport, ok := client.http.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("%s: transport is %T, want *http.Transport", key, client.http.Transport)
		}
		if transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
			t.Fatalf("%s turned certificate verification off", key)
		}
	}
}

// connectFakeServer builds a server from reg and returns a connected client
// session, closed when the test ends.
func connectFakeServer(t *testing.T, reg *Registry, cfg Config) *mcp.ClientSession {
	t.Helper()
	srv, _, err := NewServer(cfg, reg, NewLogger("error", nil))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	sess, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		if err := sess.Close(); err != nil {
			t.Errorf("close session: %v", err)
		}
	})
	return sess
}

// resultText concatenates the text content of a tool result.
func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
