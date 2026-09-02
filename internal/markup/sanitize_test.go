package markup

import (
	"strings"
	"testing"
)

// The invisible characters are written as escapes, never as literals: a
// literal one is unreadable in the test that asserts it is removed, and a
// literal U+FEFF is a byte order mark the Go parser rejects outright.
// Built from code points rather than written as literals. A literal U+FEFF is
// a byte order mark the Go parser rejects outright, and the rest are invisible
// in the very test that asserts they are removed.
var (
	rlo    = string(rune(0x202E))  // RIGHT-TO-LEFT OVERRIDE
	lrm    = string(rune(0x200E))  // LEFT-TO-RIGHT MARK
	rlm    = string(rune(0x200F))  // RIGHT-TO-LEFT MARK
	lri    = string(rune(0x2066))  // LEFT-TO-RIGHT ISOLATE
	fsi    = string(rune(0x2068))  // FIRST STRONG ISOLATE
	pdi    = string(rune(0x2069))  // POP DIRECTIONAL ISOLATE
	zwsp   = string(rune(0x200B))  // ZERO WIDTH SPACE
	zwj    = string(rune(0x200D))  // ZERO WIDTH JOINER
	wj     = string(rune(0x2060))  // WORD JOINER
	bom    = string(rune(0xFEFF))  // ZERO WIDTH NO-BREAK SPACE
	shy    = string(rune(0x00AD))  // SOFT HYPHEN
	lsep   = string(rune(0x2028))  // LINE SEPARATOR
	psep   = string(rune(0x2029))  // PARAGRAPH SEPARATOR
	nel    = string(rune(0x0085))  // NEXT LINE, a C1 control
	pua    = string(rune(0xE000))  // private use
	tagG   = string(rune(0xE0067)) // tag block: "g"
	tagI   = string(rune(0xE0069)) // tag block: "i"
	tagN   = string(rune(0xE006E)) // tag block: "n"
	varSel = string(rune(0xFE0F))  // VARIATION SELECTOR-16, kept
	acute  = string(rune(0x0301))  // COMBINING ACUTE ACCENT
)

func TestSafeTextRemovesInvisible(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// The bidirectional overrides and isolates are the reason this exists:
		// left in, "annexe" + RLO + "gpj.txt" is displayed as "annexetxt.jpg".
		{"rtl override", "annexe" + rlo + "gpj.txt", "annexegpj.txt"},
		{"marks", "a" + lrm + "b" + rlm + "c", "abc"},
		{"isolates", lri + "a" + fsi + "b" + pdi, "ab"},
		{"zero width space", "adm" + zwsp + "in", "admin"},
		{"zero width joiner", "a" + zwj + "b", "ab"},
		{"word joiner and bom", "a" + wj + "b" + bom + "c", "abc"},
		// The tag block encodes ASCII invisibly, which is how injected text
		// survives a human reading the field it is planted in.
		{"tag block", "ok" + tagI + tagG + tagN + "ore", "okore"},
		{"soft hyphen", "pass" + shy + "word", "password"},
		{"line separator", "one" + lsep + "two", "onetwo"},
		{"paragraph separator", "one" + psep + "two", "onetwo"},
		{"c0 control", "a\x1b[31mb", "a[31mb"},
		{"c1 control", "a" + nel + "b", "ab"},
		{"newline", "one\ntwo", "onetwo"},
		{"tab", "one\ttwo", "onetwo"},
		{"private use", "a" + pua + "b", "ab"},
		// Kept: a variation selector is Mn, and it chooses between renderings
		// of a character that is there rather than hiding one.
		{"variation selector kept", "❤" + varSel, "❤" + varSel},
		// Composed text is returned byte for byte; the NFC test below covers the
		// decomposed case on its own.
		{"ordinary text untouched", "PROJ-1 ready for QA", "PROJ-1 ready for QA"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SafeText(c.in); got != c.want {
				t.Errorf("SafeText(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSafeTextNormalisesToNFC(t *testing.T) {
	// "café" with the accent as a combining mark, against the same word with
	// the single code point. Two values a reader cannot tell apart must be the
	// same bytes, or a comparison on them does not mean what it looks like.
	decomposed := "cafe" + acute
	composed := "caf" + string(rune(0x00E9))
	got := SafeText(decomposed)
	if got != composed {
		t.Fatalf("SafeText(%q) = %q, want %q", decomposed, got, composed)
	}
}

func TestSafeTextKeepsNFKCFoldingAlone(t *testing.T) {
	// NFKC would rewrite the ligature into "fi" and the superscript into "2",
	// editing the visible text of a value the model may write back to
	// Atlassian. NFC leaves both alone.
	ligature := string(rune(0xFB01)) + "le"
	superscript := "m" + string(rune(0x00B2))
	for _, s := range []string{ligature, superscript} {
		if got := SafeText(s); got != s {
			t.Errorf("SafeText(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestScrubBodyKeepsLayout(t *testing.T) {
	const want = "one\n\ttwothree"
	got := scrubBody("one\n\ttwo" + rlo + "three" + zwsp)
	if got != want {
		t.Fatalf("scrubBody = %q, want %q", got, want)
	}
}

func TestFromHTMLRemovesEntityEncodedInvisible(t *testing.T) {
	// The order the scrub runs in is what this asserts: html.Parse turns the
	// entity into a real U+202E, so a check on the page source would not see
	// it and the character would reach the model.
	got := FromHTML("<p>annexe&#x202e;gpj.txt</p>")
	if strings.Contains(got, rlo) {
		t.Errorf("FromHTML kept an entity-encoded override: %q", got)
	}
	if got != "annexegpj.txt" {
		t.Errorf("FromHTML = %q, want %q", got, "annexegpj.txt")
	}
}

func TestFromHTMLRemovesTagBlockInCode(t *testing.T) {
	// Inside <pre> the text is not markdown-escaped, so the scrub on the way
	// out is the only thing that covers it.
	got := FromHTML("<pre><code>run" + tagN + tagG + "me</code></pre>")
	if strings.Contains(got, tagN) {
		t.Errorf("FromHTML kept a tag character inside a code block: %q", got)
	}
	if !strings.Contains(got, "runme") {
		t.Errorf("FromHTML lost the code text: %q", got)
	}
}

func TestFromWikiRemovesInvisible(t *testing.T) {
	got := FromWiki("h1. Head" + rlo + "er\n\nbody" + zwsp + "text")
	for _, s := range []string{rlo, zwsp} {
		if strings.Contains(got, s) {
			t.Errorf("FromWiki kept %q: %q", s, got)
		}
	}
	if !strings.Contains(got, "# Header") {
		t.Errorf("FromWiki lost its structure: %q", got)
	}
}

func TestFromWikiKeepsNewlines(t *testing.T) {
	got := FromWiki("one\n\ntwo")
	if !strings.Contains(got, "\n") {
		t.Fatalf("FromWiki dropped its newlines: %q", got)
	}
}
