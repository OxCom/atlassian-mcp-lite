// Package markup converts between markdown, which the tools speak, and the
// formats Atlassian accepts. Neither Jira nor Confluence accepts markdown over
// REST, so every write is converted to wiki markup, which both products accept
// and expand server-side.
//
// Markdown is parsed with goldmark rather than regex: regex markdown parsing
// silently corrupts nested and escaped constructs. Unsupported nodes render
// their text content rather than being dropped.
package markup

import (
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

var mdParser = goldmark.New(goldmark.WithExtensions(extension.Table))

// ToWiki converts a markdown document to Atlassian wiki markup.
func ToWiki(md string) string {
	if strings.TrimSpace(md) == "" {
		return ""
	}
	src := []byte(md)
	doc := mdParser.Parser().Parse(text.NewReader(src))

	var b strings.Builder
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		writeBlock(&b, n, src, "")
		if n.NextSibling() != nil {
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// writeBlock renders one block node. listPrefix carries the accumulated list
// markers of the ancestors, because Atlassian wiki markup encodes nesting as
// the full ancestry: an unordered list inside an ordered one is "#*", not "**".
// writeBlock renders one block node. listPrefix carries the accumulated list
// markers of the ancestors, because Atlassian wiki markup encodes nesting as
// the full ancestry: an unordered list inside an ordered one is "#*", not "**".
func writeBlock(b *strings.Builder, n ast.Node, src []byte, listPrefix string) {
	switch node := n.(type) {
	case *ast.Heading:
		b.WriteString("h" + strconv.Itoa(node.Level) + ". ")
		writeInline(b, node, src)
		b.WriteString("\n")

	case *ast.Paragraph:
		writeInline(b, node, src)
		b.WriteString("\n")

	case *ast.TextBlock:
		writeInline(b, node, src)

	case *ast.Blockquote:
		// "bq. " quotes exactly one line, so it only serves a quote made of
		// paragraphs. Anything else — a list, a code block, a nested quote —
		// has to go inside {quote}, or its structure is flattened into the
		// neighbouring text and its non-inline content is lost outright.
		simple := true
		for c := node.FirstChild(); c != nil; c = c.NextSibling() {
			if _, ok := c.(*ast.Paragraph); !ok {
				simple = false
				break
			}
		}
		if simple {
			for c := node.FirstChild(); c != nil; c = c.NextSibling() {
				b.WriteString("bq. ")
				writeInline(b, c, src)
				b.WriteString("\n")
			}
			return
		}
		var inner strings.Builder
		for c := node.FirstChild(); c != nil; c = c.NextSibling() {
			writeBlock(&inner, c, src, listPrefix)
		}
		b.WriteString("{quote}\n")
		b.WriteString(strings.TrimRight(inner.String(), "\n"))
		b.WriteString("\n{quote}\n")

	case *ast.ThematicBreak:
		b.WriteString("----\n")

	case *ast.FencedCodeBlock:
		lang := string(node.Language(src))
		if lang != "" {
			b.WriteString("{code:" + lang + "}\n")
		} else {
			b.WriteString("{code}\n")
		}
		b.WriteString(safeCodeBody(rawLines(node, src)))
		b.WriteString("{code}\n")

	case *ast.CodeBlock:
		b.WriteString("{code}\n")
		b.WriteString(safeCodeBody(rawLines(node, src)))
		b.WriteString("{code}\n")

	case *ast.List:
		marker := "*"
		if node.IsOrdered() {
			marker = "#"
		}
		prefix := listPrefix + marker
		for item := node.FirstChild(); item != nil; item = item.NextSibling() {
			// The item's own text and its nested blocks are built separately:
			// the text belongs on the marker line, a nested list or code block
			// belongs on its own lines after it.
			var line, nested strings.Builder
			line.WriteString(prefix + " ")
			firstPara := true
			for c := item.FirstChild(); c != nil; c = c.NextSibling() {
				switch c.(type) {
				case *ast.Paragraph, *ast.TextBlock:
					if !firstPara {
						// A second paragraph in one item: "\\" is wiki's
						// forced line break. Without it the paragraphs are
						// concatenated and the last word of one runs into the
						// first word of the next.
						line.WriteString(`\\`)
					}
					writeInline(&line, c, src)
					firstPara = false
				default:
					// A nested list, or any block carrying its content in
					// Lines() — code, HTML, a quote. Rendering these as inline
					// would drop them entirely, because they have no inline
					// children to walk.
					writeBlock(&nested, c, src, prefix)
				}
			}
			b.WriteString(strings.TrimRight(line.String(), "\n") + "\n")
			if nested.Len() > 0 {
				b.WriteString(strings.TrimRight(nested.String(), "\n") + "\n")
			}
		}

	case *extast.Table:
		for row := node.FirstChild(); row != nil; row = row.NextSibling() {
			_, header := row.(*extast.TableHeader)
			sep := "|"
			if header {
				sep = "||"
			}
			b.WriteString(sep)
			for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
				var c strings.Builder
				writeInline(&c, cell, src)
				if c.Len() == 0 {
					// An empty body cell would emit "||", which wiki reads as a
					// header delimiter, shifting every following cell.
					b.WriteString(" ")
				} else {
					b.WriteString(c.String())
				}
				if cell.NextSibling() != nil || header {
					b.WriteString(sep)
				} else {
					b.WriteString("|")
				}
			}
			b.WriteString("\n")
		}

	case *ast.HTMLBlock:
		// Not in the subset. Emit the literal source rather than dropping it:
		// an HTMLBlock has no inline children, so recursing would lose the
		// whole block. Lines() excludes the closing tag, which lives in
		// ClosureLine, so that has to be written separately.
		//
		// Escaped like any other text. An HTML block is content, not markup
		// this converter produced, and passing it through raw was a hole
		// straight past the escaper: "<div>{code}</div>" opened a live macro.
		// Angle brackets mean nothing to wiki, so ordinary HTML is unchanged.
		var html strings.Builder
		html.WriteString(rawLines(node, src))
		if node.HasClosure() {
			closure := node.ClosureLine
			html.Write(closure.Value(src))
		}
		b.WriteString(escapeWiki(html.String()))

	default:
		// Unknown block: emit its text so nothing is lost, and its raw lines
		// too if it keeps its content there rather than in child nodes.
		if n.Type() == ast.TypeBlock && n.FirstChild() == nil && n.Lines().Len() > 0 {
			b.WriteString(rawLines(n, src))
			return
		}
		writeInline(b, n, src)
		b.WriteString("\n")
	}
}

func writeInline(b *strings.Builder, n ast.Node, src []byte) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch node := c.(type) {
		case *ast.Text:
			b.WriteString(escapeWiki(unescapeMarkdown(string(node.Segment.Value(src)))))
			if node.SoftLineBreak() || node.HardLineBreak() {
				b.WriteString("\n")
			}
		case *ast.Emphasis:
			if node.Level == 2 {
				b.WriteString("*")
				writeInline(b, node, src)
				b.WriteString("*")
			} else {
				b.WriteString("_")
				writeInline(b, node, src)
				b.WriteString("_")
			}
		case *ast.CodeSpan:
			// Code span content is literal, so it is not escaped — but wiki
			// monospace has no escape for its own terminator, so content
			// containing "}}" would end the span early and leak the rest as
			// markup. Losing the monospace is better than losing the text.
			raw := inlineRaw(node, src)
			if strings.Contains(raw, "}}") {
				b.WriteString(escapeWiki(raw))
			} else {
				b.WriteString("{{" + raw + "}}")
			}
		case *ast.Link:
			// Destination keeps markdown's backslash escapes: goldmark stores
			// the raw slice and only resolves them in its own HTML renderer,
			// so a target such as "https://e.com/a\_b" would ship the
			// backslash to Atlassian.
			b.WriteString("[")
			writeInline(b, node, src)
			b.WriteString("|" + escapeURL(unescapeMarkdown(string(node.Destination))) + "]")
		case *ast.Image:
			// Without this the default branch recurses into the alt text and
			// the destination is silently dropped — the image disappears.
			var alt strings.Builder
			writeInline(&alt, node, src)
			if alt.Len() > 0 {
				b.WriteString("!" + escapeURL(string(node.Destination)) + "|alt=" + alt.String() + "!")
			} else {
				b.WriteString("!" + escapeURL(string(node.Destination)) + "!")
			}
		case *ast.AutoLink:
			// URL() prepends a scheme only when the source had one, so a bare
			// email autolink comes back as "a@b.com" and wiki would read it as
			// a page link rather than an address.
			url := string(node.URL(src))
			if node.AutoLinkType == ast.AutoLinkEmail && !strings.HasPrefix(strings.ToLower(url), "mailto:") {
				url = "mailto:" + url
			}
			b.WriteString("[" + escapeURL(url) + "]")
		case *ast.RawHTML:
			// Not in the subset, but the contract is never to drop content, so
			// the literal source is emitted.
			//
			// Segment.Value has a pointer receiver, so the segment cannot be
			// written inline from the accessor's return value.
			for i := 0; i < node.Segments.Len(); i++ {
				seg := node.Segments.At(i)
				b.Write(seg.Value(src))
			}
		default:
			// Unknown inline: recurse so its text survives.
			writeInline(b, c, src)
		}
	}
}

// rawLines returns a block node's literal source lines.
func rawLines(n ast.Node, src []byte) string {
	var b strings.Builder
	l := n.Lines()
	for i := 0; i < l.Len(); i++ {
		seg := l.At(i)
		b.Write(seg.Value(src))
	}
	return b.String()
}

// inlineRaw returns the literal text of an inline node's children, unescaped
// and unconverted. Used for code spans, whose content is not markup.
func inlineRaw(n ast.Node, src []byte) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			b.Write(t.Segment.Value(src))
			continue
		}
		b.WriteString(inlineRaw(c, src))
	}
	return b.String()
}

// wikiEscaper escapes the characters Jira and Confluence read as markup.
//
// This is the injection boundary of the whole converter: without it a comment
// body containing "{code}" opens a macro that swallows everything after it,
// "[a|b]" becomes a live link the author never wrote, and a "|" inside a table
// cell forges a new column. The text is user-controlled and arrives from a
// model, so it is escaped rather than trusted.
//
// The backslash itself goes first, so an escape this function adds is never
// re-escaped. "-" and "?" are deliberately absent: they only mean something
// doubled or paired around a word, the failure is cosmetic rather than
// structural, and escaping every hyphen in ordinary prose is a worse trade.
var wikiEscaper = strings.NewReplacer(
	`\`, `\\`,
	"{", `\{`,
	"}", `\}`,
	"[", `\[`,
	"]", `\]`,
	"|", `\|`,
	"*", `\*`,
	"_", `\_`,
	"+", `\+`,
	"^", `\^`,
	"~", `\~`,
	"!", `\!`,
)

func escapeWiki(s string) string { return wikiEscaper.Replace(s) }

// unescapeMarkdown removes markdown's own backslash escapes. goldmark keeps
// them in the text segment and only resolves them in its HTML renderer, so
// without this "\*literal\*" would reach escapeWiki as a backslash plus a star
// and come out doubly escaped.
func unescapeMarkdown(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && isASCIIPunct(s[i+1]) {
			i++
			b.WriteByte(s[i])
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isASCIIPunct(c byte) bool {
	switch {
	case c >= '!' && c <= '/', c >= ':' && c <= '@', c >= '[' && c <= '`', c >= '{' && c <= '~':
		return true
	}
	return false
}

// escapeURL percent-encodes the two characters that would otherwise end a wiki
// link or split its alias from its target. Percent-encoding keeps the URL
// working, which backslash escaping inside a link target does not.
var urlEscaper = strings.NewReplacer("|", "%7C", "]", "%5D")

func escapeURL(s string) string { return urlEscaper.Replace(s) }

// safeCodeBody breaks any "{code}" inside a code block so it cannot close the
// macro early. A wiki code macro has no escape sequence for its own
// terminator, so the choice is between one visible backslash in a rare body
// and every following block being swallowed into the code block.
func safeCodeBody(s string) string {
	if !strings.Contains(s, "{code") {
		return s
	}
	s = strings.ReplaceAll(s, "{code}", `{code\}`)
	return strings.ReplaceAll(s, "{code:", `{code\:`)
}
