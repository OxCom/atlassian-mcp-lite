package markup

import (
	"strings"
	"testing"
)

func TestFromWikiHeading(t *testing.T) {
	if got, want := FromWiki("h3. Objective"), "### Objective"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFromWikiInline(t *testing.T) {
	for in, want := range map[string]string{
		"*bold*":                           "**bold**",
		"_italic_":                         "*italic*",
		"{{code}}":                         "`code`",
		"[the docs|https://example.com/x]": "[the docs](https://example.com/x)",
	} {
		if got := FromWiki(in); got != want {
			t.Errorf("FromWiki(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFromWikiCodeBlock(t *testing.T) {
	if got, want := FromWiki("{code:go}\nfmt.Println(1)\n{code}"), "```go\nfmt.Println(1)\n```"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFromWikiCodeContentNotTransformed(t *testing.T) {
	in := "{code}\n*not bold*\n{code}"
	want := "```\n*not bold*\n```"
	if got := FromWiki(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFromWikiLists(t *testing.T) {
	if got, want := FromWiki("* one\n** nested"), "- one\n  - nested"; got != want {
		t.Errorf("unordered: got %q, want %q", got, want)
	}
	if got, want := FromWiki("# first\n# second"), "1. first\n1. second"; got != want {
		t.Errorf("ordered: got %q, want %q", got, want)
	}
}

func TestFromWikiRule(t *testing.T) {
	if got, want := FromWiki("----"), "---"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFromWikiTable(t *testing.T) {
	in := "||A||B||\n|1|2|"
	want := "| A | B |\n|---|---|\n| 1 | 2 |"
	if got := FromWiki(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFromWikiProtectsCodeAndLinks(t *testing.T) {
	// Emphasis conversion must not reach inside a code span or a URL.
	if got, want := FromWiki("{{*x*}}"), "`*x*`"; got != want {
		t.Errorf("code span: got %q, want %q", got, want)
	}
	if got, want := FromWiki("[t|https://e.com/a_b_c]"), "[t](https://e.com/a_b_c)"; got != want {
		t.Errorf("link with underscores: got %q, want %q", got, want)
	}
}

func TestRoundTripStability(t *testing.T) {
	// Every construct the spec lists as supported must survive md -> wiki -> md.
	for _, md := range []string{
		"# One",
		"## Two",
		"### Objective",
		"#### Four",
		"##### Five",
		"###### Six",
		"a **bold** word",
		"an *italic* word",
		"some `code` inline",
		"see [docs](https://example.com/a)",
		"- one\n- two",
		"1. first\n1. second",
		"- outer\n  - inner",
		"| A | B |\n|---|---|\n| 1 | 2 |",
		"```go\nx := 1\n```",
		"---",
	} {
		if got := FromWiki(ToWiki(md)); got != md {
			t.Errorf("round trip changed document:\n in:  %q\n out: %q", md, got)
		}
	}
}

// Text that ToWiki had to escape must come back as the author wrote it. If
// FromWiki did not know about the escapes, the emphasis and link patterns
// would match the very characters the backslash disabled.
//
// The round trip is over the *text*, not the bytes. FromWiki now escapes the
// plain text it emits, because that text is untrusted and goes to the model as
// markdown, so a literal "*" comes back as "\*" — the markdown spelling of the
// same character, which renders identically. Characters markdown does not care
// about, such as a brace, are unchanged.
func TestRoundTripRestoresEscapedText(t *testing.T) {
	for md, want := range map[string]string{
		"literal {code} and [x|y] and a|b":  `literal {code} and \[x\|y\] and a\|b`,
		"a * b _ c":                         `a \* b \_ c`,
		"snake_case_name and 2*3":           `snake\_case\_name and 2\*3`,
		"braces {like this} stay text":      "braces {like this} stay text",
		"```\nbody with {code} inside\n```": "```\nbody with {code} inside\n```",
	} {
		got := FromWiki(ToWiki(md))
		if got != want {
			t.Errorf("round trip changed document:\n in:   %q\n out:  %q\n want: %q\n wiki: %q", md, got, want, ToWiki(md))
		}
		// The escaping is stable: converting the result back to wiki yields
		// the same wiki markup, so a read-modify-write cycle does not
		// accumulate backslashes.
		if reWiki := ToWiki(got); reWiki != ToWiki(md) {
			t.Errorf("escaping is not stable across a second pass:\n first:  %q\n second: %q", ToWiki(md), reWiki)
		}
	}
}

// A backslash escape is ToWiki's, so reading wiki back must consume it rather
// than leaving it in the markdown.
func TestFromWikiConsumesEscapes(t *testing.T) {
	if got, want := FromWiki(`a \{b\} c`), "a {b} c"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// An escaped star is literal text, not the start of bold — and it stays
	// escaped on the markdown side, where a bare "*not bold*" would be bold.
	if got, want := FromWiki(`\*not bold\*`), `\*not bold\*`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A real Jira fence usually carries parameters. A pattern that only accepted a
// bare language missed the opening line, then matched the closing one and
// swallowed the rest of the document into a code fence.
func TestFromWikiCodeFenceWithParameters(t *testing.T) {
	got := FromWiki("{code:title=Foo.java|borderStyle=solid}\nint x = *1*;\n{code}\nafter *bold*")
	want := "```\nint x = *1*;\n```\nafter **bold**"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// A parameter that is a language is still kept as one.
	if got, want := FromWiki("{code:java}\nx\n{code}"), "```java\nx\n```"; got != want {
		t.Errorf("language: got %q, want %q", got, want)
	}
}

// {noformat} is how logs and stack traces are pasted into tickets. Its body is
// literal, so inline conversion must not run over it.
func TestFromWikiNoformatBodyIsLiteral(t *testing.T) {
	got := FromWiki("{noformat}\n*x* and [a|b]\n{noformat}")
	want := "```\n*x* and [a|b]\n```"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Wiki encodes list nesting as the full marker ancestry, so a bullet inside an
// ordered list is "#*" — which matches neither an all-"#" nor an all-"*"
// pattern and was passing through as literal text.
func TestFromWikiMixedNestedListMarkers(t *testing.T) {
	if got, want := FromWiki("# a\n#* b"), "1. a\n  - b"; got != want {
		t.Errorf("bullet in ordered: got %q, want %q", got, want)
	}
	if got, want := FromWiki("* a\n*# b"), "- a\n  1. b"; got != want {
		t.Errorf("ordered in bullet: got %q, want %q", got, want)
	}
}

// A cell pipe is escaped by ToWiki precisely so it is not a boundary; splitting
// on it anyway forges a column and misaligns the row against its header.
func TestFromWikiEscapedPipeStaysInsideItsCell(t *testing.T) {
	got := FromWiki(`||A||B||` + "\n" + `|a\|b|2|`)
	want := "| A | B |\n|---|---|\n| a\\|b | 2 |"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ToWiki emits {quote} for a blockquote holding anything but paragraphs, so
// FromWiki has to invert it or the round trip loses the quote.
func TestFromWikiQuoteBlock(t *testing.T) {
	if got, want := FromWiki("{quote}\n* one\n* two\n{quote}"), "> - one\n> - two"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := FromWiki(ToWiki("> - item\n> - two")), "> - item\n> - two"; got != want {
		t.Errorf("round trip: got %q, want %q", got, want)
	}
}

// The placeholder scheme is delimited by NUL. Input carrying one could
// otherwise name a placeholder and have a held value substituted into it,
// swapping attacker-supplied text for someone else's content.
func TestFromWikiInputCannotForgeAPlaceholder(t *testing.T) {
	got := FromWiki("\x00h0\x00 and {{code}} and \\* lit")
	if strings.Contains(got, "\x00") {
		t.Errorf("NUL survived into the output: %q", got)
	}
	if !strings.Contains(got, "`code`") || !strings.Contains(got, "* lit") {
		t.Errorf("real conversions broken: %q", got)
	}
}

// A link's own pipe separates label from target and is not a cell boundary.
func TestFromWikiLinkInsideTableCellIsNotSplit(t *testing.T) {
	if got, want := FromWiki("|[t|https://e.com/x]|b|"), "| [t](https://e.com/x) | b |"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A wiki link target is copied into markdown for the model. The same scheme
// allowlist as the HTML reader applies: an unsafe target is dropped and the
// label is kept as text.
func TestFromWikiUnsafeLinkKeepsLabelDropsTarget(t *testing.T) {
	for _, c := range []struct{ in, forbid string }{
		{"[click|javascript:alert(1)]", "javascript"},
		{"[click| JAVASCRIPT:alert(1)]", "JAVASCRIPT"},
		{"[click|java\tscript:x]", "script:"},
		{"[click|data:text/html,x]", "data:"},
	} {
		got := FromWiki(c.in)
		if !strings.Contains(got, "click") || strings.Contains(got, c.forbid) || strings.Contains(got, "](") {
			t.Errorf("FromWiki(%q) = %q, want plain label with no link", c.in, got)
		}
	}
	for in, want := range map[string]string{
		// The accepted target is URL-escaped, as in the HTML reader: an
		// unescaped parenthesis ends the destination before the target the
		// allowlist inspected.
		"[t|https://ok.example/a(b)]": "[t](https://ok.example/a%28b%29)",
		"[t|/wiki/spaces/X]":          "[t](/wiki/spaces/X)",
		"[t|mailto:a@b.c]":            "[t](mailto:a@b.c)",
		"[t|#top]":                    "[t](#top)",
		"[t|foo/bar:baz]":             "[t](foo/bar:baz)",
	} {
		if got := FromWiki(in); got != want {
			t.Errorf("FromWiki(%q) = %q, want %q", in, got, want)
		}
	}
}

// Item 2: issue and page text read back from Atlassian is untrusted, and
// FromWiki hands it straight to the model as markdown. Without escaping the
// text that is not wiki markup, an author can write a plain markdown link and
// bypass the safeLinkTarget allowlist this same function applies to wiki
// links.
func TestFromWikiEscapesPlainText(t *testing.T) {
	got := FromWiki("see [click](javascript:alert(1)) here")
	if strings.Contains(got, "[click](") {
		t.Errorf("markdown link survived as live markup: %q", got)
	}
	if want := `see \[click\](javascript:alert(1)) here`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !strings.Contains(got, "click") {
		t.Errorf("text was dropped rather than escaped: %q", got)
	}
}

// A pipe in prose must not forge a table column, a backtick must not open a
// code span, and an asterisk that formed no wiki emphasis must not become one
// in markdown.
func TestFromWikiEscapesStructuralCharactersInProse(t *testing.T) {
	for in, want := range map[string]string{
		"x |a|b| y":      `x \|a\|b\| y`,
		"a ` b":          "a \\` b",
		"half *open":     `half \*open`,
		"under_score":    `under\_score`,
		`a \\ b`:         `a \\ b`,
		"brackets [x] y": `brackets \[x\] y`,
	} {
		if got := FromWiki(in); got != want {
			t.Errorf("FromWiki(%q) = %q, want %q", in, got, want)
		}
	}
}

// Escaping the residue must not disturb anything the placeholder scheme is
// holding: a code span, an allowlisted link and both emphasis forms all keep
// their markup.
func TestFromWikiEscapingKeepsRealMarkup(t *testing.T) {
	for in, want := range map[string]string{
		"{{code}}":                   "`code`",
		"[lbl|https://ok.example]":   "[lbl](https://ok.example)",
		"*bold*":                     "**bold**",
		"_italic_":                   "*italic*",
		"a *b* and {{c}} and [d|/e]": "a **b** and `c` and [d](/e)",
	} {
		if got := FromWiki(in); got != want {
			t.Errorf("FromWiki(%q) = %q, want %q", in, got, want)
		}
	}
}

// Item 4: a code span whose content holds backticks closed early, and the
// middle of it became live markdown. The fence has to be longer than the
// longest run inside it, as the HTML reader already does for a code block.
func TestFromWikiCodeSpanFenceOutrunsItsContent(t *testing.T) {
	for in, want := range map[string]string{
		"{{a`b`c}}":  "``a`b`c``",
		"{{a``b}}":   "```a``b```",
		"{{a```b}}":  "````a```b````",
		"{{`lead}}":  "`` `lead ``",
		"{{trail`}}": "`` trail` ``",
	} {
		if got := FromWiki(in); got != want {
			t.Errorf("FromWiki(%q) = %q, want %q", in, got, want)
		}
	}
}

// Item 5: the target that passed the allowlist is inserted into a markdown
// link, so it needs the same URL escaping the HTML reader applies. A space or
// a parenthesis otherwise ends the destination somewhere the allowlist never
// looked.
func TestFromWikiEscapesAcceptedLinkTarget(t *testing.T) {
	if got, want := FromWiki("[lbl|http://a.com/ (x)]"), "[lbl](http://a.com/%20%28x%29)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A pipe inside a link target still has to be escaped when the text lands in a
// markdown table cell, where a bare pipe forges a column. The markdown escaper
// does not reach into a held link, so the cell variant adds it.
func TestFromWikiTableCellEscapesPipesInHeldSpans(t *testing.T) {
	if got, want := FromWiki("|[t|http://a.example/x|y]|b|"), `| [t](http://a.example/x\|y) | b |`; got != want {
		t.Errorf("link target: got %q, want %q", got, want)
	}
}

// A wiki escape inside a link target is held as a placeholder before the
// target is validated. safeLinkTarget strips NUL as a control character, so an
// unrestored target would be validated as "h0evil.example" and emitted with
// the placeholder debris in it — and a protocol-relative target spelled with
// backslashes would never be seen as one.
func TestFromWikiLinkTargetIsRestoredBeforeValidation(t *testing.T) {
	for _, in := range []string{
		`[t|\\evil.example\x]`,
		`[t|\/\/evil.example/x]`,
	} {
		got := FromWiki(in)
		if strings.Contains(got, "](") {
			t.Errorf("FromWiki(%q) = %q, want the target refused", in, got)
		}
		if strings.Contains(got, "h0") || strings.Contains(got, "\x00") {
			t.Errorf("FromWiki(%q) = %q, placeholder debris leaked", in, got)
		}
	}
	// A held escape in the target of an otherwise safe link is restored, not
	// leaked as a placeholder.
	if got, want := FromWiki(`[t|https://ok.example/a\-b]`), "[t](https://ok.example/a-b)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
