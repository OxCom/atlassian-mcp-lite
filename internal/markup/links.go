package markup

import (
	"regexp"
	"strings"
)

// linkSchemeRe matches a URL scheme at the start of a target. The character
// class is RFC 3986's, so "foo/bar:baz" — a slash before the colon — has no
// scheme and is a relative path, not a scheme named "foo/bar".
var linkSchemeRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// safeLinkTarget decides whether a link or image destination read from
// Atlassian may be copied into the markdown the model sees, and returns the
// cleaned target when it may.
//
// Page and issue text is untrusted. Copying the target through verbatim would
// hand the model — and any client that renders its output — a "javascript:" or
// "data:" URL an author planted in a page. The allowlist is deliberately short:
// web and mail links, plus scheme-less targets (relative paths, "/wiki/...",
// "#anchor"), which stay on the Atlassian host. Everything else, including
// schemes this code has never heard of, is refused; deny by default.
//
// Whitespace and ASCII control characters are removed before the scheme is
// read, because HTML parsers tolerate "java\tscript:" and " JAVASCRIPT:" and a
// check on the raw string would let both through.
func safeLinkTarget(s string) (string, bool) {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)

	scheme := linkSchemeRe.FindString(s)
	if scheme == "" {
		return s, true
	}
	switch strings.ToLower(strings.TrimSuffix(scheme, ":")) {
	case "http", "https", "mailto":
		return s, true
	}
	return "", false
}
