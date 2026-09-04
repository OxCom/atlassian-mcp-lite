package markup

import (
	"regexp"
	"strings"
	"unicode"
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
// "#anchor"), which resolve against the page they were read from and so stay
// on the Atlassian host. Everything else, including schemes this code has
// never heard of, is refused; deny by default.
//
// Whitespace, ASCII control characters and every character in the `invisible`
// set are removed before the scheme is read, because HTML parsers tolerate
// "java\tscript:" and " JAVASCRIPT:" and a check on the raw string would let
// both through.
//
// The `invisible` set is the load-bearing half. scrubBody runs on the finished
// markdown, after this function has already judged the target, so a target
// carrying a zero-width joiner between "java" and "script:" matches no scheme
// this regex can see, is let through as a scheme-less relative path, and then
// has its joiner removed downstream — reassembling a live javascript: link in
// the text the model reads. Both passes must see the same string.
//
// A scheme-less target beginning with two slashes is the exception that makes
// the "stays on the Atlassian host" claim true: "//evil.example/x" carries no
// scheme yet names another origin, so it is refused. A backslash counts as a
// slash for that test, because renderers and browsers normalise "\\host\x" and
// "/\host" to the same protocol-relative target.
func safeLinkTarget(s string) (string, bool) {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || unicode.In(r, invisible) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)

	if len(s) >= 2 && isSlashOrBackslash(s[0]) && isSlashOrBackslash(s[1]) {
		return "", false
	}

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

// isSlashOrBackslash reports whether c is either slash. The two are
// interchangeable in the leading position of a target: a UNC-style
// "\\host\share" and a mixed "/\host" both reach the protocol-relative
// destination that "//host" names.
func isSlashOrBackslash(c byte) bool { return c == '/' || c == '\\' }
