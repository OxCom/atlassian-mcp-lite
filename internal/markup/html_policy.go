package markup

import (
	"regexp"

	"github.com/microcosm-cc/bluemonday"
)

// digitsOnly bounds a colspan. The value is copied into a repeat count, so a
// page naming a colspan of a million would have the reader allocate a million
// empty cells; four digits is more columns than a markdown table can carry and
// far less than a page can ask for.
var digitsOnly = regexp.MustCompile(`^\d{1,4}$`)

// htmlPolicy is the allowlist Confluence view HTML passes through before
// FromHTML walks it.
//
// FromHTML already ignores every element it does not recognise, so the policy
// is not what decides which tags become markdown. What it adds is a parser
// hardened against the tricks that make one HTML document mean two things:
// mXSS through re-parsing, comments and CDATA sections that end where a naive
// walk does not expect, double-decoded entities, and the foreign-content
// switch into SVG and MathML, where a different set of parsing rules applies
// and an attribute can carry markup. Those are the failure modes a hand-written
// walk over a token stream gets wrong, and they are what bluemonday has spent
// years absorbing reports about.
//
// The allowlist is the set of elements the renderer actually reads plus the
// containers they sit in; everything else has its tags dropped and its text
// kept, which is what the renderer's default branch does with an unrecognised
// element anyway. Content is dropped outright only for the three elements
// whose text must never reach the model — the same three as dropContent, which
// stays in render as the second layer.
//
// The policy is built once at init: bluemonday's setup methods mutate the
// policy, but Sanitize only reads it, so one shared value is safe for
// concurrent tool calls.
var htmlPolicy = newHTMLPolicy()

func newHTMLPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()

	p.AllowElements(
		"h1", "h2", "h3", "h4", "h5", "h6",
		"p", "div", "span", "br", "hr",
		"strong", "b", "em", "i", "u", "s", "del", "ins", "sub", "sup", "small",
		"blockquote", "q", "cite",
		"pre", "code", "kbd", "samp", "var",
		"ul", "ol", "li", "dl", "dt", "dd",
		"table", "caption", "thead", "tbody", "tfoot", "tr", "td", "th", "colgroup", "col",
	)

	// The renderer reads exactly these attributes. Nothing else is allowed
	// through, so an event handler, a style, or an attribute belonging to a
	// foreign-content element never reaches the walk.
	p.AllowAttrs("href").OnElements("a")
	p.AllowAttrs("src", "alt", "title").OnElements("img")
	p.AllowAttrs("colspan").Matching(digitsOnly).OnElements("td", "th")
	p.AllowAttrs("class", "data-syntaxhighlighter-params").OnElements("pre", "code")

	// No URL policy here, deliberately. safeLinkTarget (links.go) stays the
	// single authority on a link or image target, and this was tried the other
	// way round first: bluemonday's scheme allowlist only takes effect under
	// RequireParseableURLs, which refuses any target containing a space, and
	// Confluence attachment URLs routinely carry spaces and parentheses — so
	// the policy silently dropped the href of a legitimate attachment link
	// (TestFromHTMLLinkTargetIsEncoded). Two checks that disagree about which
	// targets are real would have been worse than one that is stricter:
	// safeLinkTarget allows the same three schemes, and beyond them it strips
	// control characters before reading the scheme and refuses
	// protocol-relative and UNC targets, neither of which bluemonday's check
	// covers.

	// Tags dropped and text kept is the default for a disallowed element. For
	// these three the text is the thing that must not survive: a script body
	// read as prose is exactly the injected instruction the whole read path
	// exists to keep out.
	p.SkipElementsContent("script", "style", "noscript", "template", "iframe", "object", "embed")

	return p
}
