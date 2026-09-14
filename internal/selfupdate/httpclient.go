//go:build selfupdate

package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// This package builds its own HTTP client and never borrows core's.
//
// core.Client carries the Atlassian Basic credential on every request it makes
// and is pinned to the configured Atlassian host. Reusing it here would put a
// credential for the operator's Jira and Confluence site one redirect away from
// github.com, which is exactly the confused-deputy shape this repository spends
// most of its guards avoiding. This client holds no credential at all: every
// request it makes is an anonymous GET for a public release artifact.
const (
	// clientTimeout bounds the whole exchange, connection through body read.
	// Generous because a 64 MiB asset on a slow office link is a legitimate
	// case; still bounded, because a server that trickles bytes forever would
	// otherwise pin this tool call open for the life of the session.
	clientTimeout = 120 * time.Second

	// Byte caps, one per document kind. Each is enforced with io.LimitReader
	// plus a read of one extra byte, so an oversized body is an error rather
	// than a silent truncation: a truncated SHA256SUMS verifies against nothing
	// and a truncated asset would be written to disk as if it were a binary.
	maxReleaseJSONBytes = 1 << 20 // release metadata; we read one field of it
	maxSumsBytes        = 64 << 10
	maxSigBytes         = 1 << 10
	maxAssetBytes       = 64 << 20
)

// allowedHosts is the complete set of hosts this package will talk to, for the
// initial request and for every redirect hop alike.
//
// api.github.com and github.com are where the two URLs are constructed against;
// the two githubusercontent hosts are where a release-asset download is
// redirected to, and GitHub has used both spellings. An exact-match set, not a
// suffix test: a suffix test on "githubusercontent.com" is satisfied by
// "evil-githubusercontent.com", and one on ".github.com" by a hostname an
// attacker who can create a subdomain record controls.
var allowedHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
}

// errBlockedTarget is returned for any destination outside the allowlist.
var errBlockedTarget = errors.New("blocked destination")

// validateTarget enforces the two properties every hop must have: the scheme is
// https, and the host is one this package knows about.
//
// The scheme check is not redundant with the host check. http.Client will
// happily follow an https URL to an http one, and the release artifacts are
// verified by signature rather than by transport — but a plaintext hop still
// hands the URL, and the fact that this operator is updating, to anyone on the
// path, and it is the hop where a downgrade attack starts.
//
// The host is compared as u.Hostname(), which strips any port and any IPv6
// brackets. Comparing u.Host instead would let "github.com:443" — the same
// destination — miss the set, and, worse, would compare a value an attacker can
// pad in ways the dialler ignores.
func validateTarget(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("%w: no destination", errBlockedTarget)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q is not https", errBlockedTarget, u.Scheme)
	}
	if !allowedHosts[u.Hostname()] {
		return fmt.Errorf("%w: host %q is not a release host", errBlockedTarget, u.Hostname())
	}
	return nil
}

// checkTarget is the indirection the tests replace so an httptest server on
// 127.0.0.1 can stand in for github.com. Production code never assigns it, and
// it is unexported, so the allowlist cannot be relaxed from outside the
// package. validateTarget above is what the tests exercise directly.
var checkTarget = validateTarget

// newHTTPClient builds the constrained client.
//
// ProxyFromEnvironment and the default root pool are deliberate: an office
// network that intercepts TLS already points SSL_CERT_FILE at its own CA and
// HTTPS_PROXY at its proxy, and Go reads both without help. Pinning a
// certificate here would break exactly the network this project is developed
// on, and would buy little: the artifact is verified by an ed25519 signature
// against a key compiled into this binary, which an interception CA cannot
// forge.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: clientTimeout,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
		// Every hop is re-validated. http.Client follows redirects anywhere by
		// default, which turns any allowlist applied only to the first URL into
		// decoration: one 302 from a release host is enough to reach the cloud
		// metadata service or an internal address. This is also why the check
		// runs on req.URL, the hop about to be made, rather than on anything
		// carried in via.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return checkTarget(req.URL)
		},
	}
}

// fetch performs one GET and returns at most limit bytes of the body.
//
// what names the document for error messages. The URL itself never appears in
// an error: every URL this package builds is constructed locally from a
// validated tag and a compiled-in asset name, so printing it back tells the
// operator nothing they did not already know, and the error text reaches the
// model through core's envelope.
func fetch(ctx context.Context, c *http.Client, what, rawURL, accept string, limit int64) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%s: destination is not a valid URL", what)
	}
	// The first hop is checked with the same function as every later one, so
	// the two can never drift apart.
	if err := checkTarget(u); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", what, err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := c.Do(req)
	if err != nil {
		// The error from Do embeds the URL, and for a redirect failure it also
		// embeds our own refusal message. Both are safe here — neither carries
		// a credential or third-party text — but the wrapping keeps the shape
		// of the message stable for callers that test it.
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// The status only, never the body: an error body is text written by
		// whoever is answering, which on a hijacked or proxied hop is not
		// GitHub. It would reach the model through the tool result.
		return nil, fmt.Errorf("%s: unexpected HTTP status %d", what, resp.StatusCode)
	}

	// One byte past the cap. io.ReadAll on a LimitReader of exactly limit
	// cannot distinguish "the document is exactly limit bytes" from "the
	// document is larger and was cut here", and silently accepting the cut
	// version is the failure mode that matters: a truncated SHA256SUMS simply
	// would not contain our asset's line, and a truncated binary would be
	// written to disk.
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s: read body: %w", what, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s: response exceeds the %d-byte limit", what, limit)
	}
	return body, nil
}
