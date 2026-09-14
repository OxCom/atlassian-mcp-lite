//go:build selfupdate

package selfupdate

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"testing"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
)

// No test in this package contacts a real host. Every request goes to an
// httptest server, and the destination allowlist is narrowed to that server for
// the duration of the test — never widened in the package itself, which is why
// checkTarget is an unexported var and not an exported option.

// testSeed is a fixed ed25519 seed. A fixed seed rather than a generated key
// so that a failure is reproducible, and a seed rather than a stored private
// key so that no private key is ever committed to this repository — the bytes
// below are a constant, not a credential, and the public half they derive is
// used by nothing outside these tests.
var testSeed = bytes.Repeat([]byte{0x2a}, ed25519.SeedSize)

// testKey returns the throwaway signing pair.
func testKey(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey) {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(testSeed)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatalf("unexpected public key type %T", priv.Public())
	}
	return priv, pub
}

// useTestKey installs the throwaway public key as this build's release key for
// the duration of one test, and restores whatever was there before.
func useTestKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	priv, pub := testKey(t)
	prev := releasePublicKey
	releasePublicKey = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { releasePublicKey = prev })
	return priv
}

// useEmptyKey installs the shipped default — no key at all.
func useEmptyKey(t *testing.T) {
	t.Helper()
	prev := releasePublicKey
	releasePublicKey = ""
	t.Cleanup(func() { releasePublicKey = prev })
}

// allowHosts narrows the destination check to the given host:port values, which
// is how an httptest server on 127.0.0.1 stands in for github.com. It replaces
// checkTarget rather than editing allowedHosts because validateTarget compares
// u.Hostname(), which drops the port: two httptest servers share a hostname, so
// only a host:port comparison can tell "allowed" from "not allowed" apart in a
// redirect test. validateTarget itself is exercised directly elsewhere.
func allowHosts(t *testing.T, hosts ...string) {
	t.Helper()
	prev := checkTarget
	checkTarget = func(u *url.URL) error {
		if u == nil {
			return fmt.Errorf("%w: no destination", errBlockedTarget)
		}
		for _, h := range hosts {
			if u.Host == h {
				return nil
			}
		}
		return fmt.Errorf("%w: host %q", errBlockedTarget, u.Host)
	}
	t.Cleanup(func() { checkTarget = prev })
}

// hostOf returns the host:port of an httptest server URL.
func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Host
}

// sumsLine renders one line of a SHA256SUMS manifest for the given content.
func sumsLine(name string, content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

// quietLogger discards output. Tests that care about what was logged build
// their own with a bytes.Buffer.
func quietLogger() *core.Logger { return core.NewLogger("debug", nil) }
