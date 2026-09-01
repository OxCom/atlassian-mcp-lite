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
func TestRoundTripRestoresEscapedText(t *testing.T) {
	for _, md := range []string{
		"literal {code} and [x|y] and a|b",
		"a * b _ c",
		"snake_case_name and 2*3",
		"braces {like this} stay text",
		"```\nbody with {code} inside\n```",
	} {
		if got := FromWiki(ToWiki(md)); got != md {
			t.Errorf("round trip changed document:\n in:  %q\n out: %q\n wiki: %q", md, got, ToWiki(md))
		}
	}
}

// A backslash escape is ToWiki's, so reading wiki back must consume it rather
// than leaving it in the markdown.
func TestFromWikiConsumesEscapes(t *testing.T) {
	if got, want := FromWiki(`a \{b\} c`), "a {b} c"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// An escaped star is literal text, not the start of bold.
	if got, want := FromWiki(`\*not bold\*`), "*not bold*"; got != want {
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
