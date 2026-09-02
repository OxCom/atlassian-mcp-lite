package markup

import "strings"

// SafeText neutralises a plain-text scalar read from Atlassian — an issue
// summary, a page title, a status or version name — before it reaches the
// model.
//
// These values never pass through FromWiki or FromHTML, because they are not
// markup: Jira renders a summary literally and Confluence renders a title
// literally. But whoever wrote them chose their bytes, so the two things a
// scalar can still do are done here:
//
//   - Invisible characters are always removed: the ASCII controls, the C1
//     range, and — the part a control strip misses — the Unicode format
//     characters. An ESC begins a terminal escape sequence in a client that
//     prints the value, a newline in a value the caller reads as single-line
//     lets it forge a second line, and a bidirectional override reverses what
//     a reader sees without appearing in what they read. scrubScalar in
//     sanitize.go names the whole set and why.
//   - Markdown structure is escaped, but only when the value actually contains
//     the syntax that forges it: a link or image destination ("]("), inline
//     HTML ("<") or a code span ("`"). Everything else is returned byte for
//     byte.
//
// The condition is what keeps the escape from doing more harm than the syntax
// it disarms. Several of these scalars are round-trip inputs — jira_transition
// takes a status name, jira_update takes a version name and a summary, all as
// plain text — so escaping every value would have the model write "1\.0" back
// into Jira. Escaping only a value carrying link, HTML or code syntax leaves
// every ordinary name exact and disarms the ones an author shaped like markup.
//
// A value that reaches the model already escaped is still readable: a summary
// of `\[click\]\(javascript:alert\(1\)\)` says what it says and is no longer a
// link a rendering client will follow.
func SafeText(s string) string {
	s = scrubScalar(s)
	if strings.Contains(s, "](") || strings.ContainsAny(s, "<`") {
		return escapeMarkdown(s)
	}
	return s
}
