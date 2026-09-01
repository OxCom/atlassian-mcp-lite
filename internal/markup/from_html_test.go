package markup

import (
	"strings"
	"testing"
)

func TestFromHTMLHeadingAndEmphasis(t *testing.T) {
	got := FromHTML("<h2>Title</h2><p>Some <strong>bold</strong> and <em>italic</em>.</p>")
	for _, want := range []string{"## Title", "**bold**", "*italic*"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestFromHTMLListAndLink(t *testing.T) {
	got := FromHTML(`<ul><li>one</li><li>two</li></ul><p><a href="https://example.com">link</a></p>`)
	for _, want := range []string{"- one", "- two", "[link](https://example.com)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestFromHTMLOrderedList(t *testing.T) {
	got := FromHTML("<ol><li>first</li><li>second</li></ol>")
	if !strings.Contains(got, "1. first") || !strings.Contains(got, "2. second") {
		t.Errorf("ordered list wrong: %q", got)
	}
}

func TestFromHTMLCodeBlock(t *testing.T) {
	got := FromHTML("<pre><code>x := 1</code></pre>")
	if !strings.Contains(got, "```") || !strings.Contains(got, "x := 1") {
		t.Errorf("code block missing: %q", got)
	}
}

func TestFromHTMLTableIsValidMarkdown(t *testing.T) {
	got := FromHTML("<table><thead><tr><th>A</th><th>B</th></tr></thead><tbody><tr><td>1</td><td>2</td></tr></tbody></table>")
	for _, want := range []string{"| A | B |", "|---|---|", "| 1 | 2 |"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestFromHTMLHeaderlessTableStillGetsSeparator(t *testing.T) {
	// Markdown has no headerless table form, so the first row becomes the header.
	got := FromHTML("<table><tr><td>1</td><td>2</td></tr><tr><td>3</td><td>4</td></tr></table>")
	if !strings.Contains(got, "|---|---|") {
		t.Errorf("separator missing, output is not a valid table: %q", got)
	}
}

func TestFromHTMLUnknownTagsStrippedTextKept(t *testing.T) {
	got := FromHTML(`<div class="macro"><span>kept text</span></div>`)
	if !strings.Contains(got, "kept text") {
		t.Errorf("text must survive: %q", got)
	}
	if strings.Contains(got, "<") || strings.Contains(got, "class=") {
		t.Errorf("markup must not survive: %q", got)
	}
}

func TestFromHTMLDecodesEntities(t *testing.T) {
	got := FromHTML("<p>a &amp; b &lt; c &quot;d&quot;</p>")
	for _, want := range []string{"a & b", "< c", `"d"`} {
		if !strings.Contains(got, want) {
			t.Errorf("entity not decoded (%q): %q", want, got)
		}
	}
}

func TestFromHTMLScriptContentDropped(t *testing.T) {
	got := FromHTML("<p>before</p><script>alert('x')</script><p>after</p>")
	if strings.Contains(got, "alert") {
		t.Errorf("script content must be dropped: %q", got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("surrounding text must survive: %q", got)
	}
}

func TestFromHTMLEmpty(t *testing.T) {
	if got := FromHTML(""); got != "" {
		t.Errorf("FromHTML(\"\") = %q", got)
	}
}

// An img has no children, so the default walk emitted nothing at all and the
// image disappeared — alt text included. Confluence uses img for every
// attachment and emoticon.
func TestFromHTMLImageIsNotDropped(t *testing.T) {
	got := FromHTML(`<p>see <img src="diag.png" alt="diagram"> here</p>`)
	if !strings.Contains(got, "![diagram](diag.png)") {
		t.Errorf("image lost: %q", got)
	}
}

// Quoted text is often third-party. Rendered as ordinary prose, the model
// cannot tell it from the page's own words.
func TestFromHTMLBlockquoteKeepsItsQuoteMarker(t *testing.T) {
	got := FromHTML("<blockquote><p>quoted line</p></blockquote><p>after</p>")
	if !strings.Contains(got, "> quoted line") {
		t.Errorf("quote marker missing: %q", got)
	}
	if strings.Contains(got, "> after") {
		t.Errorf("marker leaked past the quote: %q", got)
	}
}

// Confluence wraps li content in p, so a two-paragraph item is routine. Without
// the continuation indent its second paragraph leaves the list and splits it.
func TestFromHTMLMultiBlockListItemStaysInTheList(t *testing.T) {
	got := FromHTML("<ul><li><p>a</p><p>b</p></li><li><p>c</p></li></ul>")
	if !strings.Contains(got, "\n  b") {
		t.Errorf("continuation not indented, the list is split: %q", got)
	}
}

// Page text is untrusted: prose containing markdown metacharacters must not
// become live markdown in what the model reads.
func TestFromHTMLEscapesMarkdownMetacharacters(t *testing.T) {
	got := FromHTML("<p>*not bold* and `not code`</p>")
	if strings.Contains(got, "*not bold*") || strings.Contains(got, "`not code`") {
		t.Errorf("metacharacters not escaped: %q", got)
	}
	if !strings.Contains(got, "not bold") || !strings.Contains(got, "not code") {
		t.Errorf("text lost: %q", got)
	}
	// Code is already literal; escaping inside it would corrupt the code.
	if got := FromHTML("<pre><code>a * b_c</code></pre>"); !strings.Contains(got, "a * b_c") {
		t.Errorf("code body must not be escaped: %q", got)
	}
}

// A pipe or a newline inside a cell ends the row early and drops everything
// after it out of the table.
func TestFromHTMLTableCellCannotBreakTheRow(t *testing.T) {
	got := FromHTML("<table><tr><th>A</th><th>B</th></tr><tr><td>x|y</td><td>a<br>b</td></tr></table>")
	want := "| A | B |\n|---|---|\n| x\\|y | a b |"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A spanning cell would otherwise shorten its row and shift every later cell
// under the wrong heading.
func TestFromHTMLColspanKeepsColumnsAligned(t *testing.T) {
	got := FromHTML(`<table><tr><th>A</th><th>B</th><th>C</th></tr><tr><td colspan="2">wide</td><td>c</td></tr></table>`)
	if !strings.Contains(got, "| wide |  | c |") {
		t.Errorf("colspan not padded: %q", got)
	}
}

// A pre body containing a fence would close the block early and leak the rest
// of the page as markdown.
func TestFromHTMLPreBodyCannotCloseTheFence(t *testing.T) {
	got := FromHTML("<pre>```\nx\n```</pre>")
	if !strings.HasPrefix(got, "````") {
		t.Errorf("fence must be longer than the body's own: %q", got)
	}
}

// Confluence carries the code macro's language as a syntaxhighlighter brush;
// dropping it left every code block untagged for the model.
func TestFromHTMLCodeLanguageIsKept(t *testing.T) {
	got := FromHTML(`<pre data-syntaxhighlighter-params="brush: java; gutter: false"><code>x</code></pre>`)
	if !strings.Contains(got, "```java") {
		t.Errorf("language lost: %q", got)
	}
	if got := FromHTML(`<pre><code class="language-go">x</code></pre>`); !strings.Contains(got, "```go") {
		t.Errorf("language class lost: %q", got)
	}
}

// Confluence attachment links routinely carry spaces and parentheses, which
// would end the markdown link target early.
func TestFromHTMLLinkTargetIsEncoded(t *testing.T) {
	got := FromHTML(`<a href="https://e.com/a (1).png?x=y z">t</a>`)
	if !strings.Contains(got, "%28") || !strings.Contains(got, "%20") {
		t.Errorf("target not encoded: %q", got)
	}
}

// A definition list fused term and definition into one run of text.
func TestFromHTMLDefinitionListSeparatesTerms(t *testing.T) {
	got := FromHTML("<dl><dt>Term</dt><dd>Definition</dd></dl>")
	if strings.Contains(got, "TermDefinition") {
		t.Errorf("term and definition fused: %q", got)
	}
}

// x/net/html recovers its own >512-open-element panic as an error, so a deeply
// nested page really does reach the parse-error branch. Returning the raw input
// there would hand the model the page's script bodies verbatim — the exact
// content dropContent exists to remove.
func TestFromHTMLParseFailureStillDropsScripts(t *testing.T) {
	deep := strings.Repeat("<div>", 600) + "<script>alert(1)</script>visible text" + strings.Repeat("</div>", 600)
	got := FromHTML(deep)
	if strings.Contains(got, "alert") {
		t.Errorf("script body survived the parse-failure fallback: %q", got)
	}
	if !strings.Contains(got, "visible text") {
		t.Errorf("visible text lost: %q", got)
	}
	if strings.Contains(got, "<div>") {
		t.Errorf("raw markup survived: %q", got)
	}
}

// CommonMark does not close emphasis on a marker preceded by whitespace, so
// "<strong>bold </strong>" emitted as "**bold **" loses its emphasis and shows
// the asterisks to the model instead.
func TestFromHTMLEmphasisSpacingRenders(t *testing.T) {
	if got, want := FromHTML("<p>a <strong>bold </strong>b</p>"), "a **bold** b"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// HTML collapses whitespace and markdown does not, so a pretty-printed page
// would otherwise carry its source indentation into the output, where markdown
// reads leading spaces as an indented code block.
func TestFromHTMLCollapsesSourceWhitespace(t *testing.T) {
	if got, want := FromHTML("<p>\n  hello\n  world\n</p>"), "hello world"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
