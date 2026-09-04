package markup

import "testing"

// Item 3: a scheme-less target was accepted on the grounds that it stays on
// the Atlassian host. A protocol-relative target does not — "//evil.example/x"
// has no scheme and names another origin. Browsers and some renderers also
// read a backslash as a slash, so "\\host" and "/\host" reach the same place.
func TestSafeLinkTargetRefusesProtocolRelative(t *testing.T) {
	for _, in := range []string{
		"//evil.example/x",
		`\\evil.example\x`,
		`/\evil.example`,
		`\/evil.example`,
		"  //evil.example/x",
		"/\t/evil.example/x",
	} {
		if got, ok := safeLinkTarget(in); ok {
			t.Errorf("safeLinkTarget(%q) = %q, true; want refused", in, got)
		}
	}
}

// The refusal must not reach an ordinary relative or same-page target: those
// are the reason scheme-less targets are allowed at all.
func TestSafeLinkTargetKeepsRelativeTargets(t *testing.T) {
	for _, in := range []string{
		"/wiki/spaces/X",
		"#anchor",
		"page.html",
		"foo/bar:baz",
		"/",
	} {
		got, ok := safeLinkTarget(in)
		if !ok || got != in {
			t.Errorf("safeLinkTarget(%q) = %q, %v; want %q, true", in, got, ok, in)
		}
	}
}

// An invisible character spliced into the scheme must not turn a refused
// target into an allowed one. The scheme check runs before scrubBody, which
// removes the character from the finished markdown: were the check to see the
// unscrubbed string, a "javascript:" carrying a zero-width joiner would pass
// as a scheme-less relative path and arrive in the output as a working
// javascript: link once scrubBody removed the joiner.
func TestSafeLinkTargetStripsInvisiblesBeforeSchemeCheck(t *testing.T) {
	// The carriers are spelled as code points rather than pasted in: a literal
	// byte order mark is not valid Go source, and the rest would be invisible
	// in a diff, which is the whole point of them.
	splice := func(before string, r rune, after string) string {
		return before + string(r) + after
	}
	for _, in := range []string{
		splice("java", 0x200C, "script:alert(1)"), // zero-width non-joiner, Cf
		splice("java", 0x200B, "script:alert(1)"), // zero-width space, Cf
		splice("java", 0xFEFF, "script:alert(1)"), // byte order mark, Cf
		splice("java", 0x2028, "script:alert(1)"), // line separator, Zl
		splice("", 0x202E, "javascript:alert(1)"), // bidi override, Cf
		splice("da", 0x200C, "ta:text/html,<b>x"), // data: reassembled the same way
		splice("", 0xE0064, "ata:text/html,x"),    // tag block, encodes ASCII invisibly
		splice("java", 0x00, "script:alert(1)"),   // NUL, Cc
	} {
		if got, ok := safeLinkTarget(in); ok {
			t.Errorf("safeLinkTarget(%q) = %q, true; want refused", in, got)
		}
	}
}

// The invisible strip must also clean an allowed target, so nothing invisible
// rides through inside an http URL the model is handed.
func TestSafeLinkTargetCleansAllowedTargets(t *testing.T) {
	in := "https://example.com/a" + string(rune(0x200B)) + "b"
	got, ok := safeLinkTarget(in)
	if !ok || got != "https://example.com/ab" {
		t.Errorf("safeLinkTarget(%q) = %q, %v; want %q, true", in, got, ok, "https://example.com/ab")
	}
}
