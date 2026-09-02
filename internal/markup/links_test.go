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
