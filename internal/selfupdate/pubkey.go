//go:build selfupdate

package selfupdate

// releasePublicKey is the base64 (standard encoding, with padding) of the
// 32-byte *raw* ed25519 public key that signs SHA256SUMS for every release of
// this project. It is source rather than configuration on purpose: an operator
// who could point the updater at a different key could point it at their own,
// and the whole value of the signature is that the running binary decides which
// key it trusts. Rotating the key therefore means shipping a binary that
// carries the new one.
//
// The key below was generated on 2026-09-14. Its private half never enters
// this repository: it lives as the RELEASE_SIGNING_KEY repository secret, which
// the release job feeds to `openssl pkeyutl -sign -rawin` over SHA256SUMS.
//
// An empty value is still a refusal rather than a permissive default, and the
// refusal happens before a single request goes out — see verifySums and the
// handler. That matters for any build made from a tree where this constant has
// been cleared or mangled: an unsigned update path is the entire threat this
// feature exists to defend against, so the failure has to be closed and has to
// say "this build cannot verify anything" rather than "that release looks
// corrupt".
//
// Regenerating the pair, should it ever be compromised or lost:
//
//	openssl genpkey -algorithm ed25519 -out release.pem
//	openssl pkey -in release.pem -pubout -outform DER | tail -c 32 | base64
//
// The private PEM replaces the RELEASE_SIGNING_KEY secret; the base64 above is
// pasted here. Every release signed by the old key becomes unverifiable to a
// binary carrying the new one, which is the intended meaning of a rotation.
//
// A var and not a const only so the package's own tests can substitute a
// throwaway key. Nothing outside the package can reach it.
var releasePublicKey = "aVWaPg3gb+dyQ2kUAmR2a470m0bG0CX/FcohYH6tnbw="
