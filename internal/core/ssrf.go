package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// ErrBlockedAddress is returned when a connection is refused before it is made,
// because the destination is not the configured Atlassian host or does not
// resolve to a globally routable address.
var ErrBlockedAddress = errors.New("blocked destination address")

// extraBlockedPrefixes are ranges the net package has no predicate for but which
// are just as unsuitable a destination as the ones it does cover.
var extraBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // RFC 1122 "this network"; only 0.0.0.0 is IsUnspecified
	netip.MustParsePrefix("100.64.0.0/10"),   // RFC 6598 carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),    // RFC 6890 IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // RFC 5737 documentation
	netip.MustParsePrefix("192.88.99.0/24"),  // RFC 7526 6to4 relay anycast, deprecated
	netip.MustParsePrefix("198.18.0.0/15"),   // RFC 2544 benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // RFC 5737 documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // RFC 5737 documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // RFC 1112 reserved
	netip.MustParsePrefix("2001:db8::/32"),   // RFC 3849 documentation
	netip.MustParsePrefix("64:ff9b::/96"),    // RFC 6052 NAT64, an IPv4 tunnel
	netip.MustParsePrefix("64:ff9b:1::/48"),  // RFC 8215 local-use NAT64, reaches private IPv4
	netip.MustParsePrefix("2002::/16"),       // RFC 3056 6to4, an IPv4 tunnel
}

// addrIsGloballyRoutable reports whether addr is a destination on the public
// internet.
//
// The cloud metadata service at 169.254.169.254 is why this exists: it is the
// canonical SSRF target, it needs no credentials, and it is reachable from any
// process that can make an outbound request.
func addrIsGloballyRoutable(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	// A zone identifier is rejected outright rather than stripped. netip.Prefix
	// strips zones, so Prefix.Contains returns false for any zoned address —
	// verified against Go 1.27: 64:ff9b::a00:1 is contained by the NAT64 prefix,
	// 64:ff9b::a00:1%eth0 is not, while IsPrivate and IsGlobalUnicast report
	// identically for both. A zoned address would therefore walk straight past
	// every prefix in extraBlockedPrefixes. No globally routable destination
	// needs a zone in the first place.
	if addr.Zone() != "" {
		return false
	}

	// A v4-mapped v6 address such as ::ffff:169.254.169.254 is the same
	// destination as its v4 form, and the predicates below do not all see
	// through the mapping. Unmap first so one representation cannot be used to
	// smuggle past a check on the other.
	addr = addr.Unmap()

	switch {
	case addr.IsUnspecified(),
		addr.IsLoopback(),
		addr.IsLinkLocalUnicast(),
		addr.IsLinkLocalMulticast(),
		addr.IsInterfaceLocalMulticast(),
		addr.IsMulticast(),
		addr.IsPrivate():
		return false
	}
	if !addr.IsGlobalUnicast() {
		return false
	}
	// 255.255.255.255 is global unicast by the predicate above but is the
	// limited broadcast address.
	if addr.Is4() && addr.As4() == [4]byte{255, 255, 255, 255} {
		return false
	}
	for _, prefix := range extraBlockedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// dialGuard decides, before any connection is made, whether an address may be
// dialled.
//
// It closes the gap the "no tool accepts a URL" argument leaves open. That
// argument shows model output cannot choose a host; it says nothing about what
// the configured host resolves to. A name that resolves to cloud metadata, or a
// DNS answer that changes between the check and the connection, would both slip
// past it. Four of the CVEs in the alternative this project was measured against
// were exactly that.
type dialGuard struct {
	// allowedHost is the only hostname that may be dialled, taken from the
	// configured base URL.
	allowedHost string

	// allowLocal permits non-routable addresses, and is set only when the
	// configured base URL is itself loopback. That is what lets the test suite
	// point at an httptest server without weakening the check in production.
	allowLocal bool

	// lookupIP is injectable so the resolution paths can be tested without
	// controlling DNS.
	lookupIP func(ctx context.Context, host string) ([]netip.Addr, error)
}

func newDialGuard(host string, allowLocal bool) *dialGuard {
	return &dialGuard{
		allowedHost: normaliseHost(host),
		allowLocal:  allowLocal,
		lookupIP:    defaultLookupIP,
	}
}

// permits reports whether a resolved candidate may be dialled.
//
// The loopback exemption is deliberately narrow: it admits loopback and nothing
// else. An earlier version short-circuited the whole check, so a base URL of
// http://localhost would have accepted a name resolving to 169.254.169.254 —
// turning the exemption into a bypass of exactly the control it sits inside.
func (g *dialGuard) permits(addr netip.Addr) bool {
	if addr.Zone() != "" {
		return false
	}
	if g.allowLocal && addr.Unmap().IsLoopback() {
		return true
	}
	return addrIsGloballyRoutable(addr)
}

func defaultLookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// normaliseHost lowercases and strips the root label, so neither casing nor a
// trailing dot can present the configured host as a different one.
func normaliseHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// resolve validates address and returns the concrete endpoints that may be
// dialled.
//
// Resolution happens exactly once and the caller connects to the returned
// address, never to the name. That is what removes the rebinding window: there
// is no second lookup between the check and the connection for a DNS answer to
// change underneath.
func (g *dialGuard) resolve(ctx context.Context, address string) ([]netip.AddrPort, error) {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot parse %q: %w", ErrBlockedAddress, address, err)
	}
	// Parsed rather than looked up: the port always arrives numerically from a
	// URL, a service-name lookup would need its own context, and bounding it to
	// 16 bits here makes the conversion below provably safe.
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("%w: bad port in %q: %w", ErrBlockedAddress, address, err)
	}

	if normaliseHost(host) != g.allowedHost {
		return nil, fmt.Errorf("%w: %q is not the configured Atlassian host %q",
			ErrBlockedAddress, host, g.allowedHost)
	}

	// A literal address needs no lookup, and must be checked directly.
	var candidates []netip.Addr
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		candidates = []netip.Addr{addr}
	} else {
		lookup := g.lookupIP
		if lookup == nil {
			lookup = defaultLookupIP
		}
		candidates, err = lookup(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", host, err)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: %q resolved to no addresses", ErrBlockedAddress, host)
	}

	allowed := make([]netip.AddrPort, 0, len(candidates))
	var blocked []string
	for _, addr := range candidates {
		if g.permits(addr) {
			allowed = append(allowed, netip.AddrPortFrom(addr.Unmap(), uint16(port)))
			continue
		}
		blocked = append(blocked, addr.String())
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("%w: %q resolves only to non-routable addresses (%s)",
			ErrBlockedAddress, host, strings.Join(blocked, ", "))
	}
	return allowed, nil
}

// dialContext is the transport's DialContext. It connects only to an address
// resolve has already validated.
func (g *dialGuard) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	endpoints, err := g.resolve(ctx, address)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: dialKeepAlive}
	var lastErr error
	for _, endpoint := range endpoints {
		conn, dialErr := dialer.DialContext(ctx, network, endpoint.String())
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}
