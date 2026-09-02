package markup

import (
	"strings"
	"testing"
)

func TestToWikiHeadings(t *testing.T) {
	for in, want := range map[string]string{
		"# One":      "h1. One",
		"## Two":     "h2. Two",
		"###### Six": "h6. Six",
	} {
		if got := ToWiki(in); got != want {
			t.Errorf("ToWiki(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToWikiInlineEmphasis(t *testing.T) {
	for in, want := range map[string]string{
		"**bold**":            "*bold*",
		"*italic*":            "_italic_",
		"_italic_":            "_italic_",
		"`code`":              "{{code}}",
		"**bold** and `code`": "*bold* and {{code}}",
	} {
		if got := ToWiki(in); got != want {
			t.Errorf("ToWiki(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToWikiLink(t *testing.T) {
	if got, want := ToWiki("see [the docs](https://example.com/x)"), "see [the docs|https://example.com/x]"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWikiFencedCodeKeepsLanguage(t *testing.T) {
	if got, want := ToWiki("```go\nfmt.Println(1)\n```"), "{code:go}\nfmt.Println(1)\n{code}"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWikiFencedCodeWithoutLanguage(t *testing.T) {
	if got, want := ToWiki("```\nplain\n```"), "{code}\nplain\n{code}"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWikiCodeContentNotTransformed(t *testing.T) {
	in := "```\n# not a heading\n**not bold**\n```"
	want := "{code}\n# not a heading\n**not bold**\n{code}"
	if got := ToWiki(in); got != want {
		t.Errorf("code contents must be literal: got %q, want %q", got, want)
	}
}

func TestToWikiLists(t *testing.T) {
	if got, want := ToWiki("- one\n- two\n  - nested"), "* one\n* two\n** nested"; got != want {
		t.Errorf("unordered: got %q, want %q", got, want)
	}
	if got, want := ToWiki("1. first\n2. second"), "# first\n# second"; got != want {
		t.Errorf("ordered: got %q, want %q", got, want)
	}
}

// Wiki markup encodes list nesting as the full marker ancestry, so a list of a
// different type nested inside another must not repeat the outer marker.
func TestToWikiMixedNestedListMarkers(t *testing.T) {
	if got, want := ToWiki("1. outer\n   - inner"), "# outer\n#* inner"; got != want {
		t.Errorf("unordered in ordered: got %q, want %q", got, want)
	}
	if got, want := ToWiki("- outer\n   1. inner"), "* outer\n*# inner"; got != want {
		t.Errorf("ordered in unordered: got %q, want %q", got, want)
	}
}

func TestToWikiRawHTMLIsNotDropped(t *testing.T) {
	// The contract is never to lose content, even for unsupported syntax. Each
	// case asserts its own marker: a shared "either substring" check passed
	// for a converter that returned one constant.
	for _, c := range []struct{ in, want string }{
		{"before <span>x</span> after", "before <span>x</span> after"},
		{"<div>block content</div>", "<div>block content</div>"},
	} {
		if got := ToWiki(c.in); got != c.want {
			t.Errorf("ToWiki(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestToWikiBlockquoteAndRule(t *testing.T) {
	if got, want := ToWiki("> quoted"), "bq. quoted"; got != want {
		t.Errorf("blockquote: got %q, want %q", got, want)
	}
	if got, want := ToWiki("---"), "----"; got != want {
		t.Errorf("rule: got %q, want %q", got, want)
	}
}

func TestToWikiTable(t *testing.T) {
	if got, want := ToWiki("| A | B |\n|---|---|\n| 1 | 2 |"), "||A||B||\n|1|2|"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWikiParagraphsSeparated(t *testing.T) {
	if got, want := ToWiki("one\n\ntwo"), "one\n\ntwo"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWikiUnsupportedInlinePassesThroughAsText(t *testing.T) {
	// Strikethrough is not in the subset, and with only the table extension
	// enabled goldmark never builds a node for it — "~~gone~~" stays ordinary
	// text, so this case exercises the text path, not the fallback.
	got := ToWiki("~~gone~~ but kept")
	if !contains(got, "gone") || !contains(got, "kept") {
		t.Errorf("content dropped: %q", got)
	}

	// An autolink is a node this parser really does build and this converter
	// has no wiki equivalent for beyond a plain link, so it exercises the
	// preservation rule on a node that exists.
	if got, want := ToWiki("<https://example.com/a>"), "[https://example.com/a]"; got != want {
		t.Errorf("autolink: got %q, want %q", got, want)
	}
	if got, want := ToWiki("<someone@example.com>"), "[mailto:someone@example.com]"; got != want {
		t.Errorf("email autolink must carry a scheme: got %q, want %q", got, want)
	}
}

func TestToWikiEmpty(t *testing.T) {
	if got := ToWiki(""); got != "" {
		t.Errorf("ToWiki(\"\") = %q", got)
	}
}

func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}

// An image carries its meaning in the URL. Rendering only the alt text loses
// the picture entirely, which is the "never silently drop content" rule.
func TestToWikiImageKeepsTheURL(t *testing.T) {
	if got, want := ToWiki("![alt text](https://example.com/img.png)"), "!https://example.com/img.png|alt=alt text!"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := ToWiki("![](https://example.com/i.png)"), "!https://example.com/i.png!"; got != want {
		t.Errorf("no alt: got %q, want %q", got, want)
	}
}

// Text is user-controlled and arrives from a model. Unescaped, it can open a
// macro that swallows the rest of the issue, forge a link, or add a table
// column. This is the converter's injection boundary.
func TestToWikiEscapesStructuralCharacters(t *testing.T) {
	got := ToWiki("literal {code} and [x|y] and a|b")
	want := `literal \{code\} and \[x\|y\] and a\|b`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	for _, forged := range []string{"{code}", "[x|y]"} {
		if contains(got, forged) {
			t.Errorf("output %q still contains unescaped %q", got, forged)
		}
	}
}

// A pipe in a table cell would forge a column boundary.
func TestToWikiEscapesPipeInsideTableCell(t *testing.T) {
	if got, want := ToWiki("| A | B |\n|---|---|\n| a\\|b | 2 |"), `||A||B||`+"\n"+`|a\|b|2|`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// An empty body cell rendered as "||" reads as a header delimiter in wiki and
// shifts every cell after it.
func TestToWikiEmptyTableCellIsNotAHeaderDelimiter(t *testing.T) {
	if got, want := ToWiki("| A | B |\n|---|---|\n| 1 |  |"), "||A||B||\n|1| |"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A code block whose body contains {code} would close the macro early and
// leave the rest of the document inside it.
func TestToWikiCodeBodyCannotCloseTheMacro(t *testing.T) {
	got := ToWiki("```\nbody with {code} inside\n```")
	want := "{code}\nbody with {code\\} inside\n{code}"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A quote holding anything but paragraphs needs the block form: "bq. " covers
// one line, so a list inside it was being flattened and a code block inside it
// was being dropped outright.
func TestToWikiBlockquoteWithBlocksUsesQuoteMacro(t *testing.T) {
	if got, want := ToWiki("> - one\n> - two"), "{quote}\n* one\n* two\n{quote}"; got != want {
		t.Errorf("list in quote: got %q, want %q", got, want)
	}
	got := ToWiki("> ```\n> code line\n> ```")
	if !contains(got, "code line") {
		t.Errorf("code in quote was dropped: %q", got)
	}
}

// A block that keeps its content in Lines() has no inline children, so
// rendering a list item's children as inline lost them completely.
func TestToWikiListItemKeepsNestedCodeBlock(t *testing.T) {
	got := ToWiki("- item\n\n  ```\n  code in item\n  ```")
	if !contains(got, "code in item") {
		t.Errorf("nested code dropped: %q", got)
	}
	if !contains(got, "* item") {
		t.Errorf("item text lost: %q", got)
	}
}

// Two paragraphs in one list item were concatenated, fusing the last word of
// one to the first of the next.
func TestToWikiListItemParagraphsAreSeparated(t *testing.T) {
	got := ToWiki("- para1\n\n  para2 of same item")
	if contains(got, "para1para2") {
		t.Errorf("paragraphs fused: %q", got)
	}
	if !contains(got, "para1") || !contains(got, "para2") {
		t.Errorf("content lost: %q", got)
	}
}

// goldmark keeps the closing tag of an HTML block in ClosureLine rather than
// in Lines(), so writing only Lines() dropped it.
func TestToWikiHTMLBlockKeepsItsClosingTag(t *testing.T) {
	if got, want := ToWiki("<div>\nblock content\n</div>"), "<div>\nblock content\n</div>"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// An HTML block is content, not markup this converter produced, so it goes
// through the escaper like any other text. Passing it through raw was a hole
// straight past the injection boundary: a block containing {code} opened a live
// macro that swallowed the rest of the issue.
func TestToWikiHTMLBlockCannotForgeWikiMarkup(t *testing.T) {
	got := ToWiki("<div>{code}malicious{code}</div>")
	if contains(got, "{code}") {
		t.Errorf("output %q still contains a live macro", got)
	}
	if !contains(got, "malicious") {
		t.Errorf("content lost: %q", got)
	}
	// Angle brackets mean nothing to wiki, so ordinary HTML is untouched.
	if got, want := ToWiki("<div>block content</div>"), "<div>block content</div>"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Wiki monospace has no escape for its own terminator, so a code span
// containing "}}" would end early and leak the rest as markup. Dropping the
// monospace is the lesser loss; dropping the text is not allowed.
func TestToWikiCodeSpanContainingTerminatorKeepsItsText(t *testing.T) {
	got := ToWiki("a `tick }} tock` b")
	if contains(got, "{{") {
		t.Errorf("span must not be emitted as monospace: %q", got)
	}
	if !contains(got, "tick") || !contains(got, "tock") {
		t.Errorf("text lost: %q", got)
	}
}

// A pipe in a URL would split the alias from the target and produce a link to
// the wrong place. Percent-encoding keeps the URL working, which a backslash
// inside a link target does not.
func TestToWikiPercentEncodesPipeInURL(t *testing.T) {
	if got, want := ToWiki("[t](https://e.com/?a=1|b)"), "[t|https://e.com/?a=1%7Cb]"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// goldmark leaves markdown's own backslash escapes in the text segment, so
// without stripping them first the wiki escaper would double them.
func TestToWikiDoesNotDoubleMarkdownEscapes(t *testing.T) {
	if got, want := ToWiki(`\*not bold\*`), `\*not bold\*`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Two documented limitations, pinned so a change to either is deliberate.
func TestToWikiDocumentedLimitations(t *testing.T) {
	// Wiki markup has no start-offset for ordered lists: numbering restarts.
	if got, want := ToWiki("3. third\n4. fourth"), "# third\n# fourth"; got != want {
		t.Errorf("ordered start: got %q, want %q", got, want)
	}
	// A row wider than the header is truncated by the markdown parser itself,
	// exactly as GitHub-flavoured markdown renders it, before this package
	// sees it.
	if got, want := ToWiki("| A | B |\n|---|---|\n| 1 | 2 | 3 |"), "||A||B||\n|1|2|"; got != want {
		t.Errorf("ragged row: got %q, want %q", got, want)
	}
}

// Inline HTML is content the author typed, not markup this converter produced.
// Written through raw, an attribute value or a comment body is a hole straight
// past the escaper: "{code}" inside a title attribute opens a live macro.
func TestToWikiRawHTMLCannotForgeWikiMarkup(t *testing.T) {
	for _, c := range []struct {
		name, in, want, forbid string
	}{
		{"macro in attribute", `<span title="{code}">x</span>`, `\{code\}`, "{code}"},
		{"mention in attribute", `<b title="[~accountid:557058:abc]">t</b>`, `\[\~accountid:557058:abc\]`, "[~accountid"},
		{"link in inline comment", `x <!-- [link|https://evil.example] --> y`, `\[link\|https://evil.example\]`, "[link|"},
	} {
		got := ToWiki(c.in)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: ToWiki(%q) = %q, want it to contain %q", c.name, c.in, got, c.want)
		}
		if strings.Contains(got, c.forbid) {
			t.Errorf("%s: ToWiki(%q) = %q, must not contain live %q", c.name, c.in, got, c.forbid)
		}
	}
}

// The fence info string is a macro parameter. Unvalidated, "}" inside it
// closes {code:...} early and the rest of the line, and the body after it, is
// live markup rather than literal code.
func TestToWikiFenceLanguageIsValidated(t *testing.T) {
	got := ToWiki("```x}TEXT{code\n{code}\nbody\n```")
	first, _, _ := strings.Cut(got, "\n")
	if first != "{code}" {
		t.Errorf("first line = %q, want bare {code}", first)
	}
	if !strings.Contains(got, `{code\}`) {
		t.Errorf("body must still pass through safeCodeBody: %q", got)
	}
	if strings.Contains(got, "TEXT") {
		t.Errorf("rejected language must be dropped, not emitted: %q", got)
	}

	for lang, want := range map[string]string{
		"go":  "{code:go}",
		"c++": "{code:c++}",
		"c#":  "{code:c#}",
	} {
		got := ToWiki("```" + lang + "\nx\n```")
		if first, _, _ := strings.Cut(got, "\n"); first != want {
			t.Errorf("language %q: first line = %q, want %q", lang, first, want)
		}
	}
}
