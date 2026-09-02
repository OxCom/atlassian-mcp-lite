package markup

import (
	"regexp"
	"strconv"
	"strings"
)

// Wiki markup is line-oriented, so a line scanner is appropriate here. This is
// not markdown parsing — there is no nesting to get wrong at block level.
var (
	reWikiHeading = regexp.MustCompile(`^h([1-6])\.\s+(.*)$`)
	// Both literal-text macros, with any parameter tail: a real fence is often
	// "{code:title=Foo.java|borderStyle=solid}", and {noformat} is how logs and
	// stack traces are pasted into tickets.
	reWikiFence  = regexp.MustCompile(`^\{(code|noformat)(?::([^}]*))?\}$`)
	reWikiBold   = regexp.MustCompile(`\*([^*\n]+)\*`)
	reWikiItalic = regexp.MustCompile(`_([^_\n]+)_`)
	reWikiCode   = regexp.MustCompile(`\{\{([^}\n]+)\}\}`)
	reWikiLink   = regexp.MustCompile(`\[([^\]|\n]+)\|([^\]\n]+)\]`)
	// One pattern for both kinds: wiki encodes list nesting as the full marker
	// ancestry, so a bullet inside an ordered list is "#*".
	reWikiList  = regexp.MustCompile(`^([*#]+)\s+(.*)$`)
	reWikiQuote = regexp.MustCompile(`^bq\.\s+(.*)$`)
	// A backslash before ASCII punctuation is ToWiki's escape, not content.
	reWikiEscape  = regexp.MustCompile(`\\[[:punct:]]`)
	reWikiHeadRow = regexp.MustCompile(`^\s*\|\|.*\|\|\s*$`)
	reWikiBodyRow = regexp.MustCompile(`^\s*\|.*\|\s*$`)
)

// wikiTableRow splits a wiki table row on its separator and renders a markdown
// row. header also emits the separator line markdown requires.
// wikiTableRow splits a wiki table row on its separator and renders a markdown
// row. header also emits the separator line markdown requires.
func wikiTableRow(line string, header bool) []string {
	sep := "|"
	if header {
		sep = "||"
	}
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, sep)
	t = strings.TrimSuffix(t, sep)
	raw := splitCells(t, sep)
	cells := make([]string, 0, len(raw))
	for _, c := range raw {
		// A pipe inside cell text has to stay escaped in the markdown output
		// too, or it forges a column there instead. inlineFromWikiCell does
		// that as part of escaping, which a blanket rewrite here could not: it
		// would also hit the backslash-pipe pairs the escaper had just
		// written, turning "\|" into "\\|" and putting the pipe back on the
		// boundary.
		cells = append(cells, inlineFromWikiCell(strings.TrimSpace(c)))
	}
	row := "| " + strings.Join(cells, " | ") + " |"
	if !header {
		return []string{row}
	}
	dashes := make([]string, len(cells))
	for i := range dashes {
		dashes[i] = "---"
	}
	return []string{row, "|" + strings.Join(dashes, "|") + "|"}
}

// splitCells splits a row on its separator, honouring backslash escapes. A
// plain Split would break a cell at the "\|" that ToWiki wrote precisely so the
// pipe would not be a boundary, turning one cell into two and misaligning the
// row against its header.
// splitCells splits a row on its separator, honouring backslash escapes and
// link spans. A plain Split would break a cell at the "\|" that ToWiki wrote
// precisely so the pipe would not be a boundary, and again at the "|" that
// separates a link's label from its target — turning one cell into two and
// misaligning the row against its header.
func splitCells(s, sep string) []string {
	var cells []string
	var cur strings.Builder
	inLink := false
	for i := 0; i < len(s); {
		if s[i] == '\\' && i+1 < len(s) {
			cur.WriteString(s[i : i+2])
			i += 2
			continue
		}
		switch s[i] {
		case '[':
			inLink = true
		case ']':
			inLink = false
		}
		if !inLink && strings.HasPrefix(s[i:], sep) {
			cells = append(cells, cur.String())
			cur.Reset()
			i += len(sep)
			continue
		}
		cur.WriteByte(s[i])
		i++
	}
	cells = append(cells, cur.String())
	return cells
}

// FromWiki converts Atlassian wiki markup to markdown. Over the supported
// subset it is the inverse of ToWiki; anything else is passed through.
// FromWiki converts Atlassian wiki markup to markdown. Over the supported
// subset it is the inverse of ToWiki; anything else is passed through.
func FromWiki(wiki string) string {
	lines := strings.Split(wiki, "\n")
	out := make([]string, 0, len(lines))
	inQuote := false

	// A code body is buffered rather than emitted line by line, because the
	// fence has to be longer than the longest backtick run inside the body and
	// that is not known until the closing macro arrives. A fixed "```" let a
	// body containing one close the block early, after which the rest of the
	// field was read as live markdown — a planted "[x](javascript:...)" became
	// a real link without ever passing safeLinkTarget, which the code path
	// skips. The HTML reader already sizes its <pre> fence this way.
	var (
		inCode   bool
		codeLang string
		codeBody []string
	)
	flushCode := func() {
		fence := strings.Repeat("`", maxBacktickRun(strings.Join(codeBody, "\n"))+1)
		if len(fence) < 3 {
			fence = "```"
		}
		out = append(out, fence+codeLang)
		out = append(out, codeBody...)
		out = append(out, fence)
		inCode, codeLang, codeBody = false, "", nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// {code} and {noformat} both hold literal text. A real Jira fence
		// usually carries parameters — {code:title=Foo.java|borderStyle=solid}
		// — and a regex that only accepted a bare language missed the opening
		// line, then matched the *closing* one and swallowed the rest of the
		// document into a fence.
		if m := reWikiFence.FindStringSubmatch(trimmed); m != nil {
			if inCode {
				flushCode()
			} else {
				inCode, codeLang, codeBody = true, fenceLanguage(m[2]), nil
			}
			continue
		}
		if inCode {
			// Code bodies are literal, with one exception: ToWiki breaks a
			// "{code}" inside the body so it cannot close the macro early, and
			// that break is ours to undo.
			codeBody = append(codeBody, strings.ReplaceAll(strings.ReplaceAll(line, `{code\}`, "{code}"), `{code\:`, "{code:"))
			continue
		}

		// {quote} is the block form ToWiki emits for a quote holding anything
		// but paragraphs, and Jira users write it directly too.
		if trimmed == "{quote}" {
			inQuote = !inQuote
			continue
		}

		var rendered []string
		switch {
		case trimmed == "----":
			rendered = []string{"---"}
		// Tables: a "||" row is the header and also emits the markdown
		// separator; a "|" row is a body row. Checked before emphasis handling
		// so cell pipes are not mistaken for markup.
		case reWikiHeadRow.MatchString(line):
			rendered = wikiTableRow(line, true)
		case reWikiBodyRow.MatchString(line):
			rendered = wikiTableRow(line, false)
		default:
			if m := reWikiHeading.FindStringSubmatch(line); m != nil {
				rendered = []string{strings.Repeat("#", int(m[1][0]-'0')) + " " + inlineFromWiki(m[2])}
				break
			}
			if m := reWikiQuote.FindStringSubmatch(line); m != nil {
				rendered = []string{"> " + inlineFromWiki(m[1])}
				break
			}
			// One pattern for both list kinds: wiki encodes nesting as the full
			// marker ancestry, so a bullet inside an ordered list is "#*" and
			// matches neither an all-"#" nor an all-"*" pattern. The last
			// character decides the kind, the length decides the depth.
			if m := reWikiList.FindStringSubmatch(line); m != nil {
				marker := "- "
				if m[1][len(m[1])-1] == '#' {
					marker = "1. "
				}
				rendered = []string{strings.Repeat("  ", len(m[1])-1) + marker + inlineFromWiki(m[2])}
				break
			}
			rendered = []string{inlineFromWiki(line)}
		}

		if inQuote {
			for i, r := range rendered {
				rendered[i] = strings.TrimRight("> "+r, " ")
			}
		}
		out = append(out, rendered...)
	}
	if inCode {
		// An unterminated {code} still gets a closing fence, so the block does
		// not run to the end of whatever the caller concatenates next.
		flushCode()
	}
	// Scrubbed on the way out, once, rather than per line: the field is
	// untrusted text and a bidirectional override or a tag-block character in
	// it survives every regex above untouched. Layout is kept, because the
	// newlines and tabs in this output are the markdown structure.
	return scrubBody(strings.Join(out, "\n"))
}

// fenceLanguage keeps a code fence's language only when the parameter really is
// one. Jira's first parameter is often an attribute such as "title=Foo.java",
// which is not a language and must not be emitted as one.
func fenceLanguage(params string) string {
	first, _, _ := strings.Cut(params, "|")
	first, _, _ = strings.Cut(first, ",")
	first = strings.TrimSpace(first)
	if first == "" || strings.Contains(first, "=") {
		return ""
	}
	// The info string is copied onto the fence line, so it has to be shaped
	// like a language and nothing else. A parameter of "```" would otherwise
	// produce a six-backtick opener that the three-backtick closer never
	// matches, swallowing the rest of the document into a code block.
	// codeLangRe is the same shape ToWiki accepts in the other direction.
	if !codeLangRe.MatchString(first) {
		return ""
	}
	return first
}

// inlineFromWiki converts span-level markup. Code spans, links and escaped
// characters are replaced with placeholders first: applying the emphasis
// regexes over them would rewrite markup inside {{...}}, mangle URLs
// containing * or _, and re-activate the very characters a backslash escape
// was written to disable.
// inlineFromWiki converts span-level markup. Code spans, links and escaped
// characters are replaced with placeholders first: applying the emphasis
// regexes over them would rewrite markup inside {{...}}, mangle URLs
// containing * or _, and re-activate the very characters a backslash escape
// was written to disable.
func inlineFromWiki(s string) string { return inlineFromWikiIn(s, false) }

// inlineFromWikiCell is inlineFromWiki for text that will be placed in a
// markdown table cell. There a pipe is a column boundary even inside a code
// span or a link destination, neither of which the markdown escaper touches,
// so those two get the escape as well.
func inlineFromWikiCell(s string) string { return inlineFromWikiIn(s, true) }

// inlineFromWikiIn is the shared implementation; cell selects table-cell
// escaping.
func inlineFromWikiIn(s string, cell bool) string {
	// The placeholders are delimited by NUL, which is not text any Atlassian
	// field should carry. Input that contains one anyway would be able to name
	// a placeholder and have a held value substituted into it, so the byte is
	// removed before any placeholder exists.
	s = strings.ReplaceAll(s, "\x00", "")

	var held []string
	hold := func(v string) string {
		held = append(held, v)
		return "\x00h" + strconv.Itoa(len(held)-1) + "\x00"
	}

	// Escapes come first. ToWiki writes "\{" so a literal brace cannot open a
	// macro; reading it back, the brace is plain text and must not be seen by
	// any later pattern.
	//
	// The unescaped character is markdown-escaped as it is held: wiki "\[" is
	// a literal bracket, and emitting a bare "[" would hand the model live
	// markdown — enough, with a "]" and a parenthesis, to rebuild the very
	// link the allowlist below exists to refuse.
	s = reWikiEscape.ReplaceAllStringFunc(s, func(m string) string { return hold(escapeMarkdown(m[1:])) })

	protect := func(re *regexp.Regexp, render func([]string) string) {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			return hold(render(re.FindStringSubmatch(m)))
		})
	}
	protect(reWikiCode, func(g []string) string { return wikiCodeSpan(g[1], cell) })
	protect(reWikiLink, func(g []string) string {
		// Same allowlist as the HTML reader: the target is issue text and goes
		// to the model as a markdown link, so a "javascript:" or "data:"
		// destination is dropped and only the label survives, escaped so it
		// cannot turn into markup once it is no longer inside a link.
		// The target is restored before it is validated. An escape held
		// earlier leaves a placeholder inside it, and safeLinkTarget strips
		// NUL as a control character — so the allowlist would inspect
		// "h0evil.example" while the link emitted placeholder debris, and a
		// target written as "\\evil.example" would slip past the
		// protocol-relative refusal because its backslashes were held.
		target, ok := safeLinkTarget(restoreHeld(g[2], held))
		if !ok {
			return escapeMarkdown(g[1])
		}
		// The accepted target still needs the URL escaping the HTML reader
		// applies, or a space or parenthesis ends the destination at a
		// different place than the one the allowlist inspected.
		target = escapeMarkdownURL(target)
		if cell {
			target = strings.ReplaceAll(target, "|", `\|`)
		}
		return "[" + escapeMarkdown(g[1]) + "](" + target + ")"
	})

	// The emphasis results are held too, so the escaping below cannot disable
	// the markers this function just wrote. Their content is plain text and is
	// escaped on the way in.
	protect(reWikiBold, func(g []string) string { return "**" + escapeMarkdown(g[1]) + "**" })
	protect(reWikiItalic, func(g []string) string { return "*" + escapeMarkdown(g[1]) + "*" })

	// Whatever is left is plain text: not wiki markup, not something this
	// function produced. Page and issue text is untrusted, and it goes to the
	// model as markdown, so it is escaped exactly as the HTML reader escapes a
	// text node. Without this a plain markdown link written in a wiki field
	// reaches the model live and never meets the allowlist above at all.
	//
	// This runs before restoreHeld, so held code spans and validated links
	// keep their markup. The placeholder syntax survives it: a placeholder is
	// a NUL, "h", digits and a NUL, and the escaper touches none of those.
	s = escapeMarkdown(s)

	return restoreHeld(s, held)
}

// wikiCodeSpan wraps code-span content in a backtick fence longer than the
// longest run of backticks inside it. A fixed single backtick let content such
// as "a`b`c" close the span early and turned the middle into live markdown —
// the same failure the HTML reader already avoids for code blocks. CommonMark
// also requires a space pad when the content itself begins or ends with a
// backtick, or the fence and the content run together.
//
// cell adds the table-cell pipe escape: inside a markdown table a pipe is a
// column boundary even within a code span, and "\|" is how GFM spells a
// literal one there.
func wikiCodeSpan(content string, cell bool) string {
	if cell {
		content = strings.ReplaceAll(content, "|", `\|`)
	}
	fence := strings.Repeat("`", maxBacktickRun(content)+1)
	if strings.HasPrefix(content, "`") || strings.HasSuffix(content, "`") {
		content = " " + content + " "
	}
	return fence + content + fence
}

// restoreHeld substitutes every placeholder in one pass. A held link or code
// span can itself contain a placeholder for an escape held earlier, so the scan
// repeats until a pass changes nothing — bounded by the number of held values,
// since each pass resolves at least one nesting level.
func restoreHeld(s string, held []string) string {
	for range len(held) {
		if !strings.Contains(s, "\x00h") {
			break
		}
		var b strings.Builder
		b.Grow(len(s))
		rest := s
		for {
			before, after, found := strings.Cut(rest, "\x00h")
			if !found {
				b.WriteString(rest)
				break
			}
			b.WriteString(before)
			digits, tail, closed := strings.Cut(after, "\x00")
			idx, err := strconv.Atoi(digits)
			if !closed || err != nil || idx < 0 || idx >= len(held) {
				// Not one of ours: emit it back unchanged rather than dropping
				// text.
				b.WriteString("\x00h")
				rest = after
				continue
			}
			b.WriteString(held[idx])
			rest = tail
		}
		next := b.String()
		if next == s {
			break
		}
		s = next
	}
	return s
}
