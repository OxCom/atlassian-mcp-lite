package core

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestAddrIsGloballyRoutableRejectsInternalRanges(t *testing.T) {
	// Every one of these is a documented SSRF target. The cloud metadata
	// endpoint at 169.254.169.254 is the reason this check exists at all.
	rejected := map[string]string{
		"cloud metadata":         "169.254.169.254",
		"link-local v4":          "169.254.1.1",
		"loopback v4":            "127.0.0.1",
		"loopback v4 obscured":   "127.1.2.3",
		"private 10/8":           "10.0.0.1",
		"private 172.16/12":      "172.16.0.1",
		"private 192.168/16":     "192.168.1.1",
		"unspecified v4":         "0.0.0.0",
		"CGNAT 100.64/10":        "100.64.0.1",
		"benchmarking 198.18/15": "198.18.0.1",
		"multicast v4":           "224.0.0.1",
		"broadcast":              "255.255.255.255",
		"loopback v6":            "::1",
		"unspecified v6":         "::",
		"link-local v6":          "fe80::1",
		"unique local v6":        "fd00::1",
		"v6 metadata":            "fd00:ec2::254",
		"multicast v6":           "ff02::1",
		"v4-mapped private v6":   "::ffff:10.0.0.1",
		"v4-mapped metadata v6":  "::ffff:169.254.169.254",
	}
	for name, raw := range rejected {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			t.Fatalf("%s: bad fixture %q: %v", name, raw, err)
		}
		if addrIsGloballyRoutable(addr) {
			t.Errorf("%s (%s) was accepted; it must be rejected", name, raw)
		}
	}
}

func TestAddrIsGloballyRoutableAcceptsPublicAddresses(t *testing.T) {
	for _, raw := range []string{"104.192.142.1", "8.8.8.8", "1.1.1.1", "2606:4700::1111"} {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			t.Fatalf("bad fixture %q: %v", raw, err)
		}
		if !addrIsGloballyRoutable(addr) {
			t.Errorf("%s was rejected; a public address must be accepted", raw)
		}
	}
}

// The guard exists to survive a future code path that builds a different URL,
// so it refuses any host other than the one configuration pinned.
func TestGuardRefusesAnyHostButTheConfiguredOne(t *testing.T) {
	g := &dialGuard{
		allowedHost: "example.atlassian.net",
		lookupIP: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("104.192.142.1")}, nil
		},
	}
	if _, err := g.resolve(context.Background(), "evil.example.com:443"); err == nil {
		t.Fatal("a host other than the configured one must be refused")
	} else if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("err = %v, want ErrBlockedAddress", err)
	}

	if _, err := g.resolve(context.Background(), "example.atlassian.net:443"); err != nil {
		t.Errorf("the configured host must be allowed: %v", err)
	}
}

// The host comparison must not be defeated by case or a trailing dot, both of
// which resolve to the same name.
func TestGuardHostComparisonIsNormalised(t *testing.T) {
	g := &dialGuard{
		allowedHost: "example.atlassian.net",
		lookupIP: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("104.192.142.1")}, nil
		},
	}
	for _, host := range []string{"EXAMPLE.Atlassian.NET:443", "example.atlassian.net.:443"} {
		if _, err := g.resolve(context.Background(), host); err != nil {
			t.Errorf("%s must be treated as the configured host: %v", host, err)
		}
	}
}

// This is the rebinding case: the name is the configured one, but it resolves
// somewhere internal.
func TestGuardRefusesConfiguredHostResolvingToInternalAddress(t *testing.T) {
	for _, internal := range []string{"169.254.169.254", "10.0.0.1", "127.0.0.1", "::1"} {
		g := &dialGuard{
			allowedHost: "example.atlassian.net",
			lookupIP: func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr(internal)}, nil
			},
		}
		_, err := g.resolve(context.Background(), "example.atlassian.net:443")
		if err == nil {
			t.Errorf("a configured host resolving to %s must be refused", internal)
			continue
		}
		if !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%s: err = %v, want ErrBlockedAddress", internal, err)
		}
		if !strings.Contains(err.Error(), internal) {
			t.Errorf("%s: err = %v, want the offending address named", internal, err)
		}
	}
}

// A name resolving to a mix must not be usable by picking the bad one, and must
// still work through the good one.
func TestGuardUsesOnlyRoutableAddressesFromAMixedAnswer(t *testing.T) {
	g := &dialGuard{
		allowedHost: "example.atlassian.net",
		lookupIP: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{
				netip.MustParseAddr("169.254.169.254"),
				netip.MustParseAddr("104.192.142.1"),
			}, nil
		},
	}
	got, err := g.resolve(context.Background(), "example.atlassian.net:443")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != 1 || got[0].Addr().String() != "104.192.142.1" {
		t.Errorf("resolved to %v, want only the routable address", got)
	}
}

// Loopback is allowed only when the operator configured a loopback base URL,
// which is what lets the test suite point at an httptest server.
func TestGuardAllowsLoopbackOnlyWhenConfiguredHostIsLoopback(t *testing.T) {
	loopbackOnly := func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}

	allowed := &dialGuard{allowedHost: "127.0.0.1", allowLocal: true, lookupIP: loopbackOnly}
	if _, err := allowed.resolve(context.Background(), "127.0.0.1:8080"); err != nil {
		t.Errorf("a loopback base URL must be usable: %v", err)
	}

	denied := &dialGuard{allowedHost: "127.0.0.1", allowLocal: false, lookupIP: loopbackOnly}
	if _, err := denied.resolve(context.Background(), "127.0.0.1:8080"); err == nil {
		t.Error("without the loopback exemption, loopback must be refused")
	}
}

// A resolution failure must not fall through to an unchecked dial.
func TestGuardPropagatesResolutionFailure(t *testing.T) {
	g := &dialGuard{
		allowedHost: "example.atlassian.net",
		lookupIP: func(context.Context, string) ([]netip.Addr, error) {
			return nil, errors.New("no such host")
		},
	}
	if _, err := g.resolve(context.Background(), "example.atlassian.net:443"); err == nil {
		t.Fatal("a resolution failure must be an error")
	}
}

func TestGuardRejectsMalformedAddresses(t *testing.T) {
	g := &dialGuard{allowedHost: "example.atlassian.net"}
	for _, addr := range []string{"", "no-port", "example.atlassian.net:notaport"} {
		if _, err := g.resolve(context.Background(), addr); err == nil {
			t.Errorf("%q must be refused", addr)
		}
	}
}

// The whole client must carry the guard, not just the helper.
func TestNewClientInstallsTheGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	// An httptest server is loopback, so the exemption must be derived from the
	// configured base URL for this to work at all.
	cfg := Config{BaseURL: srv.URL, Email: testEmail, Token: testToken}
	c := NewClient(cfg, NewLogger("info", &strings.Builder{}, cfg.Token))

	var out struct{ OK bool }
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out); err != nil {
		t.Fatalf("a loopback base URL must still work: %v", err)
	}
	if !out.OK {
		t.Error("body did not decode")
	}
}

// A public base URL must not be able to reach a private address, end to end
// through the transport rather than only through the helper.
func TestClientTransportBlocksInternalResolution(t *testing.T) {
	cfg := Config{BaseURL: "https://example.atlassian.net", Email: testEmail, Token: testToken}
	c := NewClient(cfg, NewLogger("info", &strings.Builder{}, cfg.Token))

	// Stand in for a DNS answer pointing at cloud metadata.
	c.guard.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
	}

	err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
	if err == nil {
		t.Fatal("resolving to cloud metadata must fail the request")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("err = %v, want ErrBlockedAddress through the transport", err)
	}
}

// Sanity: the guard must not have broken ordinary dialling mechanics.
func TestGuardResolvedAddressesCarryThePort(t *testing.T) {
	g := &dialGuard{
		allowedHost: "example.atlassian.net",
		lookupIP: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("104.192.142.1")}, nil
		},
	}
	got, err := g.resolve(context.Background(), net.JoinHostPort("example.atlassian.net", "8443"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != 1 || got[0].Port() != 8443 {
		t.Errorf("resolved to %v, want port 8443 preserved", got)
	}
}

// The loopback exemption must admit loopback and nothing else. It used to
// short-circuit the whole check, so a base URL of http://localhost accepted a
// name resolving to cloud metadata — the exemption becoming a bypass of the
// control it sits inside.
func TestLoopbackExemptionDoesNotAdmitOtherInternalAddresses(t *testing.T) {
	for _, internal := range []string{"169.254.169.254", "10.0.0.1", "192.168.1.1", "fd00::1"} {
		g := &dialGuard{
			allowedHost: "localhost",
			allowLocal:  true,
			lookupIP: func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr(internal)}, nil
			},
		}
		if _, err := g.resolve(context.Background(), "localhost:8080"); err == nil {
			t.Errorf("the loopback exemption must not admit %s", internal)
		}
	}

	// Loopback itself still works, which is the whole point of the exemption.
	g := &dialGuard{
		allowedHost: "localhost",
		allowLocal:  true,
		lookupIP: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
	}
	if _, err := g.resolve(context.Background(), "localhost:8080"); err != nil {
		t.Errorf("loopback must still be allowed under the exemption: %v", err)
	}
}

// netip.Prefix strips zones, so Prefix.Contains returns false for any zoned
// address. Verified against Go 1.27: 64:ff9b::a00:1 is contained by the NAT64
// prefix and 64:ff9b::a00:1%eth0 is not, while IsPrivate and IsGlobalUnicast
// report identically for both — so a zone walked past every prefix denial.
func TestZonedAddressesAreRejectedOutright(t *testing.T) {
	for _, raw := range []string{
		"64:ff9b::a00:1%eth0", // NAT64 wrapping 10.0.0.1
		"2002:0a00:0001::%eth0",
		"fe80::1%eth0",
		"2606:4700::1111%eth0", // even a public address: no routable destination needs a zone
	} {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			t.Fatalf("bad fixture %q: %v", raw, err)
		}
		if addrIsGloballyRoutable(addr) {
			t.Errorf("%s was accepted; a zoned address must be rejected", raw)
		}
		g := &dialGuard{allowedHost: "h", allowLocal: true}
		if g.permits(addr) {
			t.Errorf("%s was permitted even under the loopback exemption", raw)
		}
	}
}

// The unzoned forms of the same addresses must be caught by the prefix list, or
// the zone rejection above would be the only thing stopping them.
func TestTunnelPrefixesAreRejectedUnzoned(t *testing.T) {
	for _, raw := range []string{
		"64:ff9b::a00:1",   // RFC 6052 NAT64
		"64:ff9b:1::a00:1", // RFC 8215 local-use NAT64
		"2002:0a00:0001::", // RFC 3056 6to4
		"192.88.99.1",      // RFC 7526 6to4 relay anycast
		"0.0.0.1",          // 0.0.0.0/8; only 0.0.0.0 itself is IsUnspecified
		"100.64.0.1",       // CGNAT
	} {
		addr := netip.MustParseAddr(raw)
		if addrIsGloballyRoutable(addr) {
			t.Errorf("%s was accepted; it can reach a private destination", raw)
		}
	}
}

// dialContext must connect to the address the guard resolved, not re-resolve the
// name. resolve alone passing would not prove that.
func TestDialContextConnectsToTheResolvedAddress(t *testing.T) {
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	var lookups int
	g := &dialGuard{
		allowedHost: "example.atlassian.net",
		allowLocal:  true, // the listener is loopback
		lookupIP: func(context.Context, string) ([]netip.Addr, error) {
			lookups++
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
	}

	conn, err := g.dialContext(context.Background(), "tcp",
		net.JoinHostPort("example.atlassian.net", port))
	if err != nil {
		t.Fatalf("dialContext: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if lookups != 1 {
		t.Errorf("%d lookups, want exactly 1; a second resolution reopens the rebinding window", lookups)
	}
	if got := conn.RemoteAddr().String(); got != listener.Addr().String() {
		t.Errorf("connected to %s, want %s", got, listener.Addr())
	}
}

// TLS must verify against the hostname from the URL, not the pinned IP. An
// httptest TLS server presents a certificate for example.com and 127.0.0.1, so a
// request whose URL host is example.atlassian.net must fail certificate
// verification — which is only possible if the hostname is what gets verified.
func TestTLSVerifiesTheHostnameNotThePinnedAddress(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	cfg := Config{
		BaseURL: "https://example.atlassian.net:" + port,
		Email:   testEmail,
		Token:   testToken,
	}
	c := NewClient(cfg, NewLogger("info", &strings.Builder{}, cfg.Token))
	c.guard.allowLocal = true
	c.guard.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}

	err = c.Do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
	if err == nil {
		t.Fatal("the certificate does not cover example.atlassian.net, so verification must fail")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Errorf("err = %v, want a certificate verification failure, which proves the hostname is verified", err)
	}
}

// The transport must actually consult the guard, not merely carry one.
func TestTheInstalledTransportConsultsTheGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	cfg := Config{BaseURL: srv.URL, Email: testEmail, Token: testToken}
	c := NewClient(cfg, NewLogger("info", &strings.Builder{}, cfg.Token))

	var out struct{ OK bool }
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out); err != nil {
		t.Fatalf("the configured loopback host must work: %v", err)
	}

	// The guard runs when a connection is established, not on every request: a
	// pooled connection is reused without dialling. That is correct — the host
	// is fixed at construction and cannot change mid-flight — but it means the
	// pool has to be cleared for this assertion to exercise a dial at all.
	// Without this the test passed against an unguarded transport.
	transport, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", c.http.Transport)
	}
	transport.CloseIdleConnections()

	c.guard.allowedHost = "somewhere.else"
	err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, &out)
	if err == nil {
		t.Fatal("the request must fail once the guard no longer allows the host")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("err = %v, want ErrBlockedAddress from the transport", err)
	}
}
