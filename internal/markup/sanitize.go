package markup

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
	"golang.org/x/text/unicode/rangetable"
)

// invisible is every code point that occupies no width of its own and so can
// change what a reader sees without appearing in what they read.
//
// It is built from Unicode's own categories rather than from a hand-written
// list of ranges, because the list is the part that goes stale: Unicode adds
// format characters, and a table maintained here would have to be revisited on
// every release to keep saying what "invisible" means. The categories are:
//
//   - Cc — the C0 controls, DEL and the C1 range. ESC begins a terminal escape
//     sequence in any client that prints the value, and a newline in a value
//     the caller reads as one line lets its author forge a second.
//   - Cf — format characters. This is the category that matters most and the
//     one a control-character strip misses entirely: the bidirectional
//     overrides and isolates (U+202A-202E, U+2066-2069) reverse the order in
//     which text is displayed, so "gpj.exe" can be shown as "exe.jpg"; the
//     zero-width characters (U+200B-200D, U+2060-2064, U+FEFF) are content
//     nobody can see; and the tag block (U+E0000-E007F) encodes the whole of
//     ASCII invisibly, which is the carrier a prompt injection uses when the
//     text has to survive a human reading it.
//   - Co, Cs — private use and surrogates. Neither has an agreed appearance,
//     and a lone surrogate cannot be encoded as valid UTF-8 in the first place.
//   - Zl, Zp — LINE SEPARATOR and PARAGRAPH SEPARATOR. They are not Cc, but a
//     client that lays text out breaks a line on them, so leaving them in
//     keeps exactly the line forging the Cc strip exists to stop.
//
// Removing all of Cf has a cost worth stating: a zero-width joiner is
// structural in a few scripts and in emoji sequences, so a family emoji in a
// summary arrives as the people it is composed of, and a Persian or Hindi word
// written with an explicit joiner loses it. That is a display difference in
// data the model reads. A bidirectional override left in place is a
// misrepresentation of it, which is the worse of the two.
//
// Variation selectors (U+FE00-FE0F) are Mn, not Cf, and so are kept: they
// choose between renderings of a visible character rather than hiding one.
var invisible = rangetable.Merge(unicode.C, unicode.Zl, unicode.Zp)

// scrubScalar cleans a single-line value — an issue summary, a page title, a
// status or version name. Every invisible character is removed, tab, newline
// and carriage return included, because none of them belongs in a value the
// caller reads as one line.
func scrubScalar(s string) string { return scrub(s, false) }

// scrubBody cleans multi-line text produced by FromWiki or FromHTML, keeping
// the tab and newline that markdown structure is made of. It runs on the
// finished markdown rather than on the input, which is what makes it cover the
// HTML path: html.Parse decodes "&#x202e;" into a real bidirectional override,
// so a check on the raw page would not have seen it.
func scrubBody(s string) string { return scrub(s, true) }

// scrub removes the invisible characters and normalises the result to NFC.
//
// Normalisation is here so that two values a reader cannot tell apart are the
// same bytes, which is what lets a comparison elsewhere mean what it looks
// like. It is NFC and not NFKC: the compatibility mappings rewrite visible
// characters — ﬁ becomes fi, ² becomes 2 — and this function is also applied
// to values the model writes back to Atlassian, where a rewrite would be a
// silent edit of someone's data.
//
// The transformer is built per call rather than kept in a package variable
// because transform.Transformer holds the state of one conversion and
// transform.String resets it as it starts; a shared one would corrupt both
// results when two tool calls ran at once. The allocation is a small slice and
// two value types.
func scrub(s string, keepLayout bool) string {
	remove := func(r rune) bool {
		if keepLayout && (r == '\n' || r == '\t') {
			return false
		}
		return unicode.In(r, invisible)
	}
	out, _, err := transform.String(
		transform.Chain(runes.Remove(runes.Predicate(remove)), norm.NFC), s)
	if err != nil {
		// Neither transformer in the chain reports an error today — Remove
		// drops runes, NFC maps them, and invalid UTF-8 is replaced rather
		// than rejected — so this branch exists for a future change to the
		// chain. It removes without normalising rather than returning the
		// input, because handing back the unscrubbed value is the one outcome
		// that must not follow a failure here.
		return strings.Map(func(r rune) rune {
			if remove(r) {
				return -1
			}
			return r
		}, s)
	}
	return out
}
