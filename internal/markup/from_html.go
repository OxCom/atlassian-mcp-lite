package markup

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// dropContent are elements whose text must never reach the model.
var dropContent = map[string]bool{"script": true, "style": true, "noscript": true}

// FromHTML converts Confluence rendered HTML (body-format=view) to markdown.
//
// view is requested rather than storage because Atlassian has already expanded
// macros, so no macro handling is needed. Parsing uses x/net/html: regex over
// HTML mishandles nesting and attribute edge cases.
func FromHTML(h string) string {
	if strings.TrimSpace(h) == "" {
		return ""
	}
	// Sanitised before it is parsed. The walk below reads a tree, and the
	// attacks worth worrying about are the ones that make the tree differ from
	// what the page appears to say: an mXSS payload that means one thing on the
	// first parse and another on the second, a comment or CDATA section that
	// ends where a walk does not expect, an entity decoded twice, or a switch
	// into SVG or MathML where foreign-content rules apply. htmlPolicy hands
	// the walk a document containing only the elements and attributes the
	// renderer reads. See html_policy.go.
	h = htmlPolicy.Sanitize(h)
	if strings.TrimSpace(h) == "" {
		// A page made entirely of elements whose content is dropped — a body
		// that is one <script> — sanitises to nothing, and the parse below
		// would otherwise return an empty document for it anyway.
		return ""
	}
	doc, err := html.Parse(strings.NewReader(h))
	if err != nil {
		// Reachable, not theoretical: x/net/html recovers its own
		// >512-open-element panic as an error, so a deeply nested page reaches
		// this branch. Returning the raw input here would hand the model the
		// page's script and style bodies verbatim — exactly what dropContent
		// exists to remove — so the fallback tokenizes for text instead.
		return scrubBody(collapseBlankLines(strings.TrimSpace(textFallback(h))))
	}
	var b strings.Builder
	render(&b, doc, 0)
	// Scrubbed after rendering, not before parsing, and this order is the
	// point: html.Parse decodes "&#x202e;" and "&#xe0041;" into the real code
	// points, so an entity-encoded bidirectional override or tag character is
	// invisible to any check on the page source and present in the output.
	// Layout is kept — the newlines here are the markdown structure.
	return scrubBody(collapseBlankLines(strings.TrimSpace(b.String())))
}

// textFallback extracts visible text without building a tree, for input the
// parser rejects. It drops the elements whose content must never reach the
// model and escapes what is left, so a failure degrades to plain text rather
// than to unfiltered HTML.
func textFallback(h string) string {
	z := html.NewTokenizer(strings.NewReader(h))
	var b strings.Builder
	var skip []string
	for {
		switch z.Next() {
		case html.ErrorToken:
			return b.String()
		case html.StartTagToken:
			name, _ := z.TagName()
			if dropContent[string(name)] {
				skip = append(skip, string(name))
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			if n := len(skip); n > 0 && skip[n-1] == string(name) {
				skip = skip[:n-1]
			}
		case html.TextToken:
			if len(skip) > 0 {
				continue
			}
			text := strings.ReplaceAll(string(z.Text()), " ", " ")
			if strings.TrimSpace(text) == "" {
				continue
			}
			b.WriteString(escapeMarkdown(strings.TrimSpace(text)) + "\n")
		}
	}
}

func render(b *strings.Builder, n *html.Node, listDepth int) {
	switch n.Type {
	case html.TextNode:
		// Confluence view HTML emits non-breaking spaces liberally.
		text := strings.ReplaceAll(n.Data, " ", " ")
		// Page text is untrusted: a page whose prose contains "*not bold*" or a
		// backtick must not have it turn into live markdown in what the model
		// reads. Inside pre/code the text is already literal, and escaping it
		// there would corrupt the code.
		if !inLiteral(n) {
			// HTML collapses whitespace; markdown does not. Without this the
			// source indentation of a pretty-printed page is copied through as
			// literal leading spaces, which markdown reads as an indented code
			// block.
			text = collapseSpaces(text)
			text = escapeMarkdown(text)
		}
		b.WriteString(text)
		return
	case html.ElementNode:
		if dropContent[n.Data] {
			return
		}
	}

	switch n.Data {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		level, _ := strconv.Atoi(n.Data[1:])
		b.WriteString("\n" + strings.Repeat("#", level) + " ")
		renderChildren(b, n, listDepth)
		b.WriteString("\n")
		return
	case "p", "div":
		b.WriteString("\n")
		renderChildren(b, n, listDepth)
		b.WriteString("\n")
		return
	case "br":
		b.WriteString("\n")
		return
	case "hr":
		b.WriteString("\n---\n")
		return
	case "strong", "b":
		writeEmphasis(b, n, "**", listDepth)
		return
	case "em", "i":
		writeEmphasis(b, n, "*", listDepth)
		return
	case "img":
		// An img has no children, so without this the default walk emits
		// nothing at all and the image disappears — alt text included.
		// Confluence uses img for every attachment and emoticon.
		src := attr(n, "src")
		alt := attr(n, "alt")
		if alt == "" {
			alt = attr(n, "title")
		}
		// A refused target keeps the alt text so the reader knows a picture
		// was there; the destination itself is never emitted.
		src, ok := safeLinkTarget(src)
		// An empty target is treated as a refused one, not as an empty link.
		// htmlPolicy refuses a target by dropping the attribute rather than by
		// blanking its value, so "no src" and "a src the policy would not
		// allow" arrive here identically, and "![alt]()" is not a useful thing
		// to hand the model for either.
		if !ok || src == "" {
			b.WriteString(escapeMarkdown(alt))
			return
		}
		b.WriteString("![" + escapeMarkdown(alt) + "](" + escapeMarkdownURL(src) + ")")
		return
	case "blockquote":
		// Without this a quote renders as ordinary prose and the model cannot
		// tell quoted, often third-party, text from the page's own words.
		var inner strings.Builder
		renderChildren(&inner, n, listDepth)
		b.WriteString("\n")
		for _, line := range strings.Split(strings.Trim(inner.String(), "\n"), "\n") {
			b.WriteString(strings.TrimRight("> "+line, " ") + "\n")
		}
		return
	case "code":
		if n.Parent != nil && n.Parent.Data == "pre" {
			renderChildren(b, n, listDepth)
			return
		}
		// A single fixed backtick let content such as "a`b`c" close the span
		// early and turned the middle into live markdown, so a "javascript:"
		// link planted inside <code> reached the model without ever passing
		// safeLinkTarget. wikiCodeSpan sizes the fence to the content and pads
		// it when the content itself starts or ends with a backtick; the wiki
		// reader has used it for its own code spans all along.
		var inner strings.Builder
		renderChildren(&inner, n, listDepth)
		b.WriteString(wikiCodeSpan(inner.String(), false))
		return
	case "pre":
		var inner strings.Builder
		renderChildren(&inner, n, listDepth)
		body := strings.Trim(inner.String(), "\n")
		// A body containing a fence would close the block early and leak the
		// rest of the page as markdown. CommonMark allows a longer fence.
		fence := strings.Repeat("`", maxBacktickRun(body)+1)
		if len(fence) < 3 {
			fence = "```"
		}
		b.WriteString("\n" + fence + codeLanguage(n) + "\n")
		b.WriteString(body)
		b.WriteString("\n" + fence + "\n")
		return
	case "a":
		var inner strings.Builder
		renderChildren(&inner, n, listDepth)
		// The children are already markdown-escaped text, so when the target
		// is refused the label stands on its own as plain text and nothing of
		// the destination reaches the output.
		href, ok := safeLinkTarget(attr(n, "href"))
		// Empty is refused for the same reason as on img: htmlPolicy drops a
		// target it will not allow, so an anchor whose href was refused there
		// is indistinguishable from an anchor that never had one, and "[x]()"
		// is a link to nowhere either way.
		if !ok || href == "" {
			b.WriteString(strings.TrimSpace(inner.String()))
			return
		}
		b.WriteString("[" + strings.TrimSpace(inner.String()) + "](" + escapeMarkdownURL(href) + ")")
		return
	case "dt":
		b.WriteString("\n**")
		renderChildren(b, n, listDepth)
		b.WriteString("**\n")
		return
	case "dd":
		b.WriteString("\n")
		renderChildren(b, n, listDepth)
		b.WriteString("\n")
		return
	case "ul", "ol":
		b.WriteString("\n")
		i := 1
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode || c.Data != "li" {
				continue
			}
			indent := strings.Repeat("  ", listDepth)
			b.WriteString(indent)
			if n.Data == "ol" {
				b.WriteString(strconv.Itoa(i) + ". ")
			} else {
				b.WriteString("- ")
			}
			var inner strings.Builder
			renderChildren(&inner, c, listDepth+1)
			// Confluence wraps li content in p, so an item with two paragraphs
			// is routine. Its later lines need the continuation indent, or they
			// leave the list and split it in two.
			b.WriteString(indentContinuation(strings.TrimSpace(inner.String()), indent+"  "))
			b.WriteString("\n")
			i++
		}
		return
	case "table":
		// Handled whole rather than per row, because a markdown table needs a
		// separator line after the header, and a row alone cannot know whether
		// it is the header.
		rows := collectRows(n, listDepth)
		if len(rows) == 0 {
			return
		}
		b.WriteString("\n")
		b.WriteString("| " + strings.Join(rows[0].cells, " | ") + " |\n")
		dashes := make([]string, len(rows[0].cells))
		for i := range dashes {
			dashes[i] = "---"
		}
		b.WriteString("|" + strings.Join(dashes, "|") + "|\n")
		for _, r := range rows[1:] {
			b.WriteString("| " + strings.Join(r.cells, " | ") + " |\n")
		}
		return
	}

	renderChildren(b, n, listDepth)
}

type htmlRow struct {
	cells  []string
	header bool
}

// collectRows walks a table for tr elements at any depth, so thead and tbody
// wrappers do not hide rows. When no th is present the first row is used as the
// header, because markdown has no headerless table form.
// collectRows walks a table for tr elements at any depth, so thead and tbody
// wrappers do not hide rows. When no th is present the first row is used as the
// header, because markdown has no headerless table form.
func collectRows(table *html.Node, listDepth int) []htmlRow {
	var rows []htmlRow
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.Data == "tr" {
				var row htmlRow
				for cell := c.FirstChild; cell != nil; cell = cell.NextSibling {
					if cell.Type != html.ElementNode || (cell.Data != "td" && cell.Data != "th") {
						continue
					}
					if cell.Data == "th" {
						row.header = true
					}
					var inner strings.Builder
					renderChildren(&inner, cell, listDepth)
					row.cells = append(row.cells, tableCell(inner.String()))
					// A spanning cell would otherwise shorten the row and shift
					// every cell after it under the wrong heading.
					for pad := colspan(cell); pad > 1; pad-- {
						row.cells = append(row.cells, "")
					}
				}
				if len(row.cells) > 0 {
					rows = append(rows, row)
				}
				continue
			}
			walk(c)
		}
	}
	walk(table)
	return rows
}

// tableCell makes one cell safe to place in a markdown row. A literal pipe
// would forge a column, and a newline — from a br or a nested paragraph, both
// routine in Confluence tables — would end the row and drop everything after it
// out of the table.
// tableCell makes one cell safe to place in a markdown row. A literal pipe
// would forge a column, and a newline — from a br or a nested paragraph, both
// routine in Confluence tables — would end the row and drop everything after it
// out of the table.
func tableCell(s string) string {
	// Text nodes are already markdown-escaped, so only a pipe that arrived
	// unescaped needs escaping here; escaping it twice would show the
	// backslash to the model.
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			b.WriteString(s[i : i+2])
			i++
			continue
		}
		if s[i] == '|' {
			b.WriteString(`\|`)
			continue
		}
		b.WriteByte(s[i])
	}
	out := strings.ReplaceAll(b.String(), "\r\n", "\n")
	out = strings.ReplaceAll(out, "\n", " ")
	return strings.Join(strings.Fields(out), " ")
}

func colspan(cell *html.Node) int {
	n, err := strconv.Atoi(attr(cell, "colspan"))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

func renderChildren(b *strings.Builder, n *html.Node, listDepth int) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		render(b, c, listDepth)
	}
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// inLiteral reports whether a text node sits inside pre or code, where its
// content is already literal and escaping it would corrupt the code.
func inLiteral(n *html.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && (p.Data == "pre" || p.Data == "code") {
			return true
		}
	}
	return false
}

// markdownEscaper escapes the characters that would turn page text into live
// markdown. Confluence page content is untrusted: without this, a page whose
// prose contains "*not bold*", a backtick or a bracket forges structure in the
// text the model reads.
//
// Line-leading "#", "-" and ">" are not escaped here — this runs per text node,
// which does not know where a line begins — and their worst case is a heading
// or bullet where prose was meant, not a forged link or code block.
//
// "<" is escaped because markdown carries inline HTML through. html.Parse
// decodes "&lt;a href=&quot;javascript:...&quot;&gt;" back into real angle
// brackets, so without this a page whose prose spells out an anchor hands a
// rendering client the very link safeLinkTarget exists to refuse. ">" needs no
// escape once "<" has one: a lone ">" cannot open a tag, and line-leading ">"
// is the blockquote case above.
var markdownEscaper = strings.NewReplacer(
	`\`, `\\`,
	"*", `\*`,
	"_", `\_`,
	"`", "\\`",
	"[", `\[`,
	"]", `\]`,
	"|", `\|`,
	"<", `\<`,
)

func escapeMarkdown(s string) string { return markdownEscaper.Replace(s) }

// markdownURLEscaper percent-encodes what would otherwise end a markdown link
// target early. Confluence attachment links routinely carry spaces and
// parentheses in query parameters.
var markdownURLEscaper = strings.NewReplacer("(", "%28", ")", "%29", " ", "%20")

func escapeMarkdownURL(s string) string { return markdownURLEscaper.Replace(s) }

// maxBacktickRun returns the longest run of backticks in s, so a code fence can
// be made longer than anything inside it.
func maxBacktickRun(s string) int {
	best, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			run++
			if run > best {
				best = run
			}
			continue
		}
		run = 0
	}
	return best
}

// codeLanguage recovers the language of a Confluence code macro, which carries
// it either as a syntaxhighlighter brush or as a language- class.
//
// The value is page content, and it is copied onto the fence line, so it must
// be shaped like a language and nothing else: a "brush:```" would otherwise
// lengthen the opening fence past its own closer and swallow the rest of the
// page into a code block. codeLangRe is the same shape ToWiki accepts in the
// other direction; anything else yields no language rather than a rejected
// block.
func codeLanguage(pre *html.Node) string {
	nodes := []*html.Node{pre}
	for c := pre.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "code" {
			nodes = append(nodes, c)
		}
	}
	for _, n := range nodes {
		if params := attr(n, "data-syntaxhighlighter-params"); params != "" {
			for _, part := range strings.Split(params, ";") {
				part = strings.TrimSpace(part)
				if rest, ok := strings.CutPrefix(part, "brush:"); ok {
					if lang := strings.TrimSpace(rest); codeLangRe.MatchString(lang) {
						return lang
					}
				}
			}
		}
		for _, class := range strings.Fields(attr(n, "class")) {
			if lang, ok := strings.CutPrefix(class, "language-"); ok && codeLangRe.MatchString(lang) {
				return lang
			}
		}
	}
	return ""
}

// indentContinuation indents every line after the first, so a list item's
// second paragraph stays inside the item instead of ending the list.
func indentContinuation(s, indent string) string {
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}

// collapseSpaces reduces every run of HTML whitespace to a single space, the
// way a browser lays the text out.
func collapseSpaces(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		} else if space {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	if space {
		b.WriteByte(' ')
	}
	return b.String()
}

// writeEmphasis renders an emphasis element, moving any leading or trailing
// space outside the markers. "<strong>bold </strong>" would otherwise emit
// "**bold **", and CommonMark does not close emphasis on a marker preceded by
// whitespace — so the text loses its emphasis and shows the asterisks instead.
func writeEmphasis(b *strings.Builder, n *html.Node, marker string, listDepth int) {
	var inner strings.Builder
	renderChildren(&inner, n, listDepth)
	s := inner.String()
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		b.WriteString(s)
		return
	}
	if strings.HasPrefix(s, " ") {
		b.WriteString(" ")
	}
	b.WriteString(marker + trimmed + marker)
	if strings.HasSuffix(s, " ") {
		b.WriteString(" ")
	}
}
