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
//   - ASCII control characters are always removed. An ESC begins a terminal
//     escape sequence in a client that prints the value, and a newline in a
//     value the caller reads as single-line lets it forge a second line.
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
	s = stripControl(s)
	if strings.Contains(s, "](") || strings.ContainsAny(s, "<`") {
		return escapeMarkdown(s)
	}
	return s
}

// stripControl removes C0 controls, DEL and the C1 range. The C1 block is
// included because a terminal reading UTF-8 treats U+0085 as a line break and
// U+009B as a control sequence introducer, so leaving them in would keep the
// forging the C0 strip exists to stop.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}
