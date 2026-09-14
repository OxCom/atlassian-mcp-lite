//go:build selfupdate

package selfupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

// TestShippedKeyIsUsable guards the one value in this package that no other
// test can check, because every other test substitutes a throwaway key.
//
// The failure it exists for is a paste: releasePublicKey is filled in by hand
// from an openssl command, and the three ways that goes wrong — an empty value,
// a PEM or DER blob instead of the raw 32 bytes, a truncated or whitespace-torn
// line — all produce a binary that compiles, ships, and refuses every update
// with a message about the release rather than about itself. Nothing else in
// the build would notice, so this is the notice.
func TestShippedKeyIsUsable(t *testing.T) {
	if strings.TrimSpace(releasePublicKey) == "" {
		t.Fatal("releasePublicKey is empty; a build with no key refuses every update")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(releasePublicKey))
	if err != nil {
		t.Fatalf("releasePublicKey is not valid base64: %v", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		t.Fatalf("releasePublicKey decodes to %d bytes, want the %d raw bytes of an ed25519 public key; a PEM or DER blob pasted whole is the usual cause", len(raw), ed25519.PublicKeySize)
	}
	// signingKey is what the handler actually calls, so the check ends where
	// the code does rather than at the decode above.
	key, err := signingKey()
	if err != nil {
		t.Fatalf("signingKey: %v", err)
	}
	if len(key) != ed25519.PublicKeySize {
		t.Fatalf("signingKey returned %d bytes, want %d", len(key), ed25519.PublicKeySize)
	}
}
