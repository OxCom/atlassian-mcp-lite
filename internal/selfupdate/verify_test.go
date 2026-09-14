//go:build selfupdate

package selfupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestVerifySumsAcceptsASignatureFromThisBuildsKey(t *testing.T) {
	priv := useTestKey(t)
	sums := []byte(sumsLine("atlassian-mcp-lite-selfupdate_linux_amd64", []byte("binary")))
	if err := verifySums(sums, ed25519.Sign(priv, sums)); err != nil {
		t.Fatalf("verifySums: %v", err)
	}
}

func TestVerifySumsRejectsATamperedManifest(t *testing.T) {
	priv := useTestKey(t)
	sums := []byte(sumsLine("asset", []byte("binary")))
	sig := ed25519.Sign(priv, sums)

	// One character of the digest changed. This is the whole point of the
	// signature: the attacker who can serve a modified manifest is the attacker
	// who can serve a modified binary, and without the signature the two
	// changes agree with each other.
	tampered := []byte(strings.Replace(string(sums), "a", "b", 1))
	if err := verifySums(tampered, sig); !errors.Is(err, errVerification) {
		t.Errorf("err = %v, want errVerification", err)
	}
	// Appending an extra entry is the same attack in a friendlier shape.
	extended := append(append([]byte{}, sums...), []byte(sumsLine("asset2", []byte("other")))...)
	if err := verifySums(extended, sig); !errors.Is(err, errVerification) {
		t.Errorf("appended manifest: err = %v, want errVerification", err)
	}
}

func TestVerifySumsRejectsASignatureFromAnotherKey(t *testing.T) {
	useTestKey(t)
	// A different seed, so a different, perfectly valid key. The signature
	// verifies against its own key and against nothing else — which is what
	// makes the key a pin rather than a formality.
	other := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	sums := []byte(sumsLine("asset", []byte("binary")))
	if err := verifySums(sums, ed25519.Sign(other, sums)); !errors.Is(err, errVerification) {
		t.Errorf("err = %v, want errVerification", err)
	}
}

func TestVerifySumsRejectsAMalformedSignature(t *testing.T) {
	useTestKey(t)
	sums := []byte(sumsLine("asset", []byte("binary")))
	for name, sig := range map[string][]byte{
		"empty":     {},
		"too short": make([]byte, ed25519.SignatureSize-1),
		"too long":  make([]byte, ed25519.SignatureSize+1),
	} {
		if err := verifySums(sums, sig); !errors.Is(err, errVerification) {
			t.Errorf("%s signature: err = %v, want errVerification", name, err)
		}
	}
}

// TestVerifySumsRefusesWhenTheBuildCarriesNoKey covers a build whose key was
// cleared or mangled. An empty releasePublicKey must be a refusal
// with its own message, not a signature failure: the two mean opposite things
// to an operator, and only one of them is fixed by waiting.
func TestVerifySumsRefusesWhenTheBuildCarriesNoKey(t *testing.T) {
	useEmptyKey(t)
	sums := []byte(sumsLine("asset", []byte("binary")))
	err := verifySums(sums, make([]byte, ed25519.SignatureSize))
	if !errors.Is(err, errNoSigningKey) {
		t.Fatalf("err = %v, want errNoSigningKey", err)
	}
	if !strings.Contains(err.Error(), "no release signing key") {
		t.Errorf("err = %v, want it to say the build carries no key", err)
	}
	// And it must not be mistakable for a verification failure, which would
	// send the operator looking at the release instead of at their binary.
	if errors.Is(err, errVerification) {
		t.Error("the missing-key refusal is reported as a verification failure")
	}
}

func TestSigningKeyRejectsAMalformedCompiledInKey(t *testing.T) {
	prev := releasePublicKey
	defer func() { releasePublicKey = prev }()

	// Not base64 at all.
	releasePublicKey = "not base64!!"
	if _, err := signingKey(); !errors.Is(err, errVerification) {
		t.Errorf("err = %v, want errVerification", err)
	}
	// Valid base64 of the wrong length — the shape of a DER or PEM paste.
	// ed25519.Verify panics on a key of the wrong size, so this must be caught
	// before it gets there.
	releasePublicKey = base64.StdEncoding.EncodeToString(make([]byte, 44))
	if _, err := signingKey(); !errors.Is(err, errVerification) {
		t.Errorf("err = %v, want errVerification", err)
	}
	// Whitespace-only is the empty case, not the malformed one.
	releasePublicKey = "   \n"
	if _, err := signingKey(); !errors.Is(err, errNoSigningKey) {
		t.Errorf("err = %v, want errNoSigningKey", err)
	}
}

func TestHashForFindsThisBuildsLineAndNothingElse(t *testing.T) {
	name := "atlassian-mcp-lite-selfupdate_linux_amd64"
	content := []byte("binary")
	manifest := sumsLine("atlassian-mcp-lite_linux_amd64", []byte("default build")) +
		sumsLine(name, content) +
		sumsLine("atlassian-mcp-lite-selfupdate_darwin_arm64", []byte("other platform"))

	got, err := hashFor([]byte(manifest), name)
	if err != nil {
		t.Fatalf("hashFor: %v", err)
	}
	want := strings.Fields(sumsLine(name, content))[0]
	if got != want {
		t.Errorf("hash = %q, want %q", got, want)
	}

	// The binary marker sha256sum writes for a binary file must not change the
	// name that is matched.
	starred := strings.Replace(manifest, "  "+name, " *"+name, 1)
	if got, err := hashFor([]byte(starred), name); err != nil || got != want {
		t.Errorf("starred manifest: hash = %q, err = %v", got, err)
	}
}

// TestHashForAcceptsTheSpellingsSha256sumEmits is a regression test for the
// defect that made the first signed release unusable: the release job runs
// `sha256sum ./*`, so every published line names "./atlassian-mcp-lite_…",
// while the lookup stripped only the "*" binary marker. The signature verified,
// the manifest was genuine, and the update was refused with "no entry for" its
// own asset — the fixtures here all used the bare spelling, so nothing caught
// it until a real release existed.
func TestHashForAcceptsTheSpellingsSha256sumEmits(t *testing.T) {
	name := "atlassian-mcp-lite-selfupdate_linux_amd64"
	content := []byte("binary")
	want := strings.Fields(sumsLine(name, content))[0]

	for _, spelling := range []string{name, "./" + name, "*" + name, "*./" + name} {
		manifest := want + "  " + spelling + "\n"
		got, err := hashFor([]byte(manifest), name)
		if err != nil {
			t.Errorf("%q: hashFor: %v", spelling, err)
			continue
		}
		if got != want {
			t.Errorf("%q: hash = %q, want %q", spelling, got, want)
		}
	}

	// Only those two prefixes are stripped. An entry for a file in another
	// directory is a different file and must not answer for ours.
	if _, err := hashFor([]byte(want+"  other/"+name+"\n"), name); !errors.Is(err, errVerification) {
		t.Errorf("a path-qualified entry matched; err = %v", err)
	}

	// Two spellings of one name still say two things about the same file, so
	// the duplicate refusal must survive the normalisation.
	both := want + "  " + name + "\n" + want + "  ./" + name + "\n"
	if _, err := hashFor([]byte(both), name); !errors.Is(err, errVerification) {
		t.Errorf("duplicate spellings accepted; err = %v", err)
	}
}

func TestHashForRefusesAMissingEntry(t *testing.T) {
	manifest := sumsLine("atlassian-mcp-lite_linux_amd64", []byte("default build"))
	_, err := hashFor([]byte(manifest), "atlassian-mcp-lite-selfupdate_linux_amd64")
	if !errors.Is(err, errVerification) {
		t.Fatalf("err = %v, want errVerification", err)
	}
	if !strings.Contains(err.Error(), "no entry") {
		t.Errorf("err = %v, want it to say the entry is missing", err)
	}
}

// TestHashForRefusesADuplicateEntry: a manifest that says two things about one
// file lets whoever wrote it choose which one this code believes.
func TestHashForRefusesADuplicateEntry(t *testing.T) {
	name := "asset"
	manifest := sumsLine(name, []byte("first")) + sumsLine(name, []byte("second"))
	_, err := hashFor([]byte(manifest), name)
	if !errors.Is(err, errVerification) {
		t.Fatalf("err = %v, want errVerification", err)
	}
	if !strings.Contains(err.Error(), "more than once") {
		t.Errorf("err = %v, want it to name the duplicate", err)
	}
	// Even two identical lines are refused: the manifest is signed, so a
	// duplicate is a fact about the release, not noise to be smoothed over.
	same := sumsLine(name, []byte("first")) + sumsLine(name, []byte("first"))
	if _, err := hashFor([]byte(same), name); !errors.Is(err, errVerification) {
		t.Errorf("identical duplicate: err = %v, want errVerification", err)
	}
}

func TestHashForRefusesANonDigestEntry(t *testing.T) {
	name := "asset"
	for _, line := range []string{
		"zz  " + name + "\n",
		"abc  " + name + "\n",
		strings.Repeat("g", 64) + "  " + name + "\n",
	} {
		if _, err := hashFor([]byte(line), name); !errors.Is(err, errVerification) {
			t.Errorf("%q: err = %v, want errVerification", line, err)
		}
	}
	// An unrelated malformed line elsewhere in the manifest is ignored rather
	// than fatal: it cannot match this build's name, and failing on it would
	// make the updater hostage to a future extra line.
	manifest := "# a comment line\n\n" + sumsLine(name, []byte("binary"))
	if _, err := hashFor([]byte(manifest), name); err != nil {
		t.Errorf("an unrelated line made hashFor fail: %v", err)
	}
}

func TestCheckAssetComparesTheWholeDigest(t *testing.T) {
	content := []byte("the new binary")
	want := strings.Fields(sumsLine("asset", content))[0]

	if err := checkAsset(content, want); err != nil {
		t.Fatalf("checkAsset: %v", err)
	}
	if err := checkAsset([]byte("something else"), want); !errors.Is(err, errVerification) {
		t.Errorf("mismatched content: err = %v, want errVerification", err)
	}
	// A prefix of the correct digest must not pass. subtle.ConstantTimeCompare
	// returns 0 for differing lengths, which is what makes this safe without a
	// separate length branch.
	if err := checkAsset(content, want[:32]); !errors.Is(err, errVerification) {
		t.Errorf("truncated digest: err = %v, want errVerification", err)
	}
	if err := checkAsset(content, ""); !errors.Is(err, errVerification) {
		t.Errorf("empty digest: err = %v, want errVerification", err)
	}
	// Neither digest is quoted back: an operator who can read both is invited
	// to "fix" a mismatch by trusting the one that came with the download.
	err := checkAsset([]byte("something else"), want)
	if strings.Contains(err.Error(), want) {
		t.Errorf("the error quotes the expected digest: %v", err)
	}
}
