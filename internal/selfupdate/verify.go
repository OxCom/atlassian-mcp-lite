//go:build selfupdate

package selfupdate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// errNoSigningKey is the refusal a build with an empty releasePublicKey gives.
// It is a distinct error, not a generic verification failure, because the two
// mean opposite things to an operator: a bad signature means something is wrong
// with the release, while this means something is missing from their binary,
// and only one of them is fixed by trying again later.
var errNoSigningKey = errors.New("this build carries no release signing key, so no update can be verified; fail-closed by design")

// errVerification is the class every genuine verification failure belongs to.
// Callers never distinguish which check failed when reporting to the model: the
// distinction is useful to an operator reading stderr and is an oracle to
// anyone probing the updater.
var errVerification = errors.New("release verification failed")

// signingKey decodes the compiled-in public key.
func signingKey() (ed25519.PublicKey, error) {
	if strings.TrimSpace(releasePublicKey) == "" {
		return nil, errNoSigningKey
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(releasePublicKey))
	if err != nil {
		// A key that does not decode is a maintainer's paste error, caught the
		// first time anyone runs the tool. The value is not echoed: it is not
		// secret, but echoing it teaches nothing a look at pubkey.go does not.
		return nil, fmt.Errorf("%w: the compiled-in release key is not valid base64", errVerification)
	}
	if len(raw) != ed25519.PublicKeySize {
		// The most likely wrong paste is a DER or PEM encoding rather than the
		// raw 32 bytes, and ed25519.Verify panics on a key of the wrong length
		// — so the length is checked here rather than discovered there.
		return nil, fmt.Errorf("%w: the compiled-in release key is %d bytes, expected %d", errVerification, len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// verifySums checks the detached signature over the exact bytes of the checksum
// manifest.
//
// The signature covers SHA256SUMS and nothing else. That one signature is what
// makes every hash in the manifest trustworthy, which in turn is what makes the
// asset trustworthy — so this is the only place where trust enters the process,
// and everything downstream is arithmetic.
func verifySums(sums, sig []byte) error {
	key, err := signingKey()
	if err != nil {
		return err
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: signature is %d bytes, expected %d", errVerification, len(sig), ed25519.SignatureSize)
	}
	// openssl pkeyutl -sign -rawin over the file produces exactly this: a raw
	// 64-byte Ed25519ph-free signature over the message bytes, which is what
	// ed25519.Verify consumes. No hashing beforehand — Ed25519 hashes the
	// message itself, and pre-hashing would be a different, incompatible scheme.
	if !ed25519.Verify(key, sums, sig) {
		return fmt.Errorf("%w: the checksum manifest is not signed by this build's release key", errVerification)
	}
	return nil
}

// hashFor returns the hex digest recorded for name in an already-verified
// manifest.
//
// The manifest is the sha256sum format: a 64-character lowercase hex digest,
// whitespace, an optional "*" binary marker, then the file name. Names in this
// project never contain a space, so splitting on whitespace is exact rather
// than approximate.
//
// The name is normalised by manifestName before it is compared, because
// sha256sum echoes back whatever the shell handed it: the release job runs
// `sha256sum ./*`, so every published line reads "./atlassian-mcp-lite_…".
// Comparing the raw field found no entry for any asset and refused every
// update with a message about the release — see manifestName for why matching
// the spelling loosely is safe here.
func hashFor(sums []byte, name string) (string, error) {
	found := ""
	for _, line := range strings.Split(string(sums), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			// Not an error: an unrelated line in the manifest is not this
			// build's problem, and failing on one would make the updater
			// hostage to a future extra line. A malformed line simply cannot
			// match our name.
			continue
		}
		if manifestName(fields[1]) != name {
			continue
		}
		if found != "" {
			// Two entries for one name is a manifest that says two different
			// things about the same file, and picking either one would be
			// picking which of them an attacker gets to choose. It is refused.
			return "", fmt.Errorf("%w: the checksum manifest lists %s more than once", errVerification, name)
		}
		found = strings.ToLower(fields[0])
	}
	if found == "" {
		return "", fmt.Errorf("%w: the checksum manifest has no entry for %s", errVerification, name)
	}
	if len(found) != hex.EncodedLen(sha256.Size) {
		return "", fmt.Errorf("%w: the checksum manifest entry for %s is not a SHA-256 digest", errVerification, name)
	}
	if _, err := hex.DecodeString(found); err != nil {
		return "", fmt.Errorf("%w: the checksum manifest entry for %s is not hexadecimal", errVerification, name)
	}
	return found, nil
}

// manifestName strips the two prefixes sha256sum may put in front of a name:
// the "*" it writes in binary mode, and the "./" it copies from the argument
// it was given.
//
// Loosening the comparison is safe because this runs on a manifest whose
// signature has already been checked against the key compiled into this
// binary. The names in it were written by this project's own release job, not
// by a caller, so the question here is not "may this entry be trusted" — the
// signature settled that — but only "which spelling of our own asset name did
// sha256sum happen to emit". Nothing beyond these two fixed prefixes is
// stripped, and a path segment is not: "other/atlassian-mcp-lite_linux_amd64"
// still does not match, so an entry for a different file cannot answer for
// ours.
func manifestName(field string) string {
	return strings.TrimPrefix(strings.TrimPrefix(field, "*"), "./")
}

// checkAsset hashes the downloaded bytes and compares them to the verified
// digest.
//
// The comparison is constant-time. Timing is a weak channel on a comparison of
// public values, and an attacker who can iterate on it is already in a strong
// position — but this is the last gate before bytes become the executable this
// process runs, it costs one function call, and a plain == here is the kind of
// thing a reader has to stop and reason about. subtle.ConstantTimeCompare also
// returns 0 for differing lengths, so the length case needs no separate branch.
func checkAsset(asset []byte, wantHex string) error {
	sum := sha256.Sum256(asset)
	got := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(got), []byte(wantHex)) != 1 {
		// Neither digest is quoted. The expected one is public, but printing
		// the pair invites an operator to "fix" the mismatch by trusting the
		// value that came with the download.
		return fmt.Errorf("%w: the downloaded asset does not match its signed checksum", errVerification)
	}
	return nil
}
