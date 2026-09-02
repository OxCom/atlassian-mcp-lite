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
		// too, or it forges a column there instead.
		cells = append(cells, strings.ReplaceAll(inlineFromWiki(strings.TrimSpace(c)), "|", `\|`))
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
	inCode, inQuote := false, false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// {code} and {noformat} both hold literal text. A real Jira fence
		// usually carries parameters — {code:title=Foo.java|borderStyle=solid}
		// — and a regex that only accepted a bare language missed the opening
		// line, then matched the *closing* one and swallowed the rest of the
		// document into a fence.
		if m := reWikiFence.FindStringSubmatch(trimmed); m != nil {
			if inCode {
				out, inCode = append(out, "```"), false
			} else {
				out, inCode = append(out, "```"+fenceLanguage(m[2])), true
			}
			continue
		}
		if inCode {
			// Code bodies are literal, with one exception: ToWiki breaks a
			// "{code}" inside the body so it cannot close the macro early, and
			// that break is ours to undo.
			out = append(out, strings.ReplaceAll(strings.ReplaceAll(line, `{code\}`, "{code}"), `{code\:`, "{code:"))
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
		out = append(out, "```")
	}
	return strings.Join(out, "\n")
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
func inlineFromWiki(s string) string {
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
	s = reWikiEscape.ReplaceAllStringFunc(s, func(m string) string { return hold(m[1:]) })

	protect := func(re *regexp.Regexp, render func([]string) string) {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			return hold(render(re.FindStringSubmatch(m)))
		})
	}
	protect(reWikiCode, func(g []string) string { return "`" + g[1] + "`" })
	protect(reWikiLink, func(g []string) string {
		// Same allowlist as the HTML reader: the target is issue text and goes
		// to the model as a markdown link, so a "javascript:" or "data:"
		// destination is dropped and only the label survives, escaped so it
		// cannot turn into markup once it is no longer inside a link.
		target, ok := safeLinkTarget(g[2])
		if !ok {
			return escapeMarkdown(g[1])
		}
		return "[" + g[1] + "](" + target + ")"
	})

	s = reWikiBold.ReplaceAllString(s, "**$1**")
	s = reWikiItalic.ReplaceAllString(s, "*$1*")

	return restoreHeld(s, held)
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
