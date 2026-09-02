package markup

import (
	"strings"
	"testing"
)

// The read direction is the one an attacker controls: a Jira description or a
// Confluence page can be written by anyone with edit rights, and whatever these
// converters emit is what the model reads. safeLinkTarget refuses a
// "javascript:" destination, but it only sees targets that reach it — a link
// that escapes a code fence or a code span never passes through it at all.
// These tests pin the three escapes that did.

func TestFromWikiCodeBodyCannotCloseItsOwnFence(t *testing.T) {
	// A backtick fence inside a {code} body used to close the markdown block
	// early, after which the following line was live markdown.
	in := "{code}\n```\n[click me](javascript:alert(1))\n```\n{code}"
	got := FromWiki(in)

	// The body's own "```" lines survive verbatim; what matters is that they
	// are shorter than the fence around them, so CommonMark keeps reading the
	// block and the link between them is never live markdown.
	if !strings.HasPrefix(got, "````\n") || !strings.HasSuffix(got, "\n````") {
		t.Errorf("FromWiki(%q) = %q, want a fence longer than the backtick run in the body at both ends", in, got)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(got, "````\n"), "\n````")
	if strings.Contains(body, "````") {
		t.Errorf("FromWiki(%q) = %q, the body still holds a run as long as its fence", in, got)
	}
}

func TestFromWikiFenceLanguageIsValidated(t *testing.T) {
	// A code macro parameter is page content too. An info string of "```" gave
	// a six-backtick opener that the three-backtick closer never matched, so
	// everything after it was swallowed into a code block.
	in := "{code:```}\nbody\n{code}\n\nback in prose"
	got := FromWiki(in)

	if strings.Contains(got, "``````") {
		t.Errorf("FromWiki(%q) = %q, want the info string refused, not copied onto the fence", in, got)
	}
	if !strings.Contains(got, "back in prose") {
		t.Errorf("FromWiki(%q) = %q, want the text after the block to survive", in, got)
	}
}

func TestFromWikiUnterminatedCodeIsClosed(t *testing.T) {
	got := FromWiki("{code}\nbody")
	if !strings.HasSuffix(got, "\n```") {
		t.Errorf("FromWiki = %q, want an unterminated {code} closed", got)
	}
}

func TestFromHTMLInlineCodeCannotCloseItsOwnSpan(t *testing.T) {
	// A single fixed backtick let a backtick in the content end the span, and
	// the rest of it became live markdown.
	in := "<p><code>x`[click](javascript:alert(1))</code></p>"
	got := FromHTML(in)

	if !strings.Contains(got, "``x`[click](javascript:alert(1))``") {
		t.Errorf("FromHTML(%q) = %q, want the span fenced longer than the backtick inside it", in, got)
	}
}

func TestFromHTMLCodeLanguageIsValidated(t *testing.T) {
	in := "<pre data-syntaxhighlighter-params=\"brush:```\"><code>body</code></pre><p>after</p>"
	got := FromHTML(in)

	if strings.Contains(got, "``````") {
		t.Errorf("FromHTML(%q) = %q, want the brush value refused, not copied onto the fence", in, got)
	}
	if !strings.Contains(got, "after") {
		t.Errorf("FromHTML(%q) = %q, want the text after the block to survive", in, got)
	}
}

func TestEscapeMarkdownDisarmsInlineHTML(t *testing.T) {
	// html.Parse decodes the entities, so the text node really does hold "<a
	// href=...>". Markdown carries inline HTML through, so an unescaped "<"
	// hands a rendering client the link safeLinkTarget refused.
	in := `<p>&lt;a href="javascript:alert(1)"&gt;click&lt;/a&gt;</p>`
	got := FromHTML(in)

	if !strings.Contains(got, `\<a href`) {
		t.Errorf("FromHTML(%q) = %q, want a backslash-escaped tag, with the text preserved", in, got)
	}
	if bare := strings.ReplaceAll(got, `\<`, ""); strings.Contains(bare, "<") {
		t.Errorf("FromHTML(%q) = %q, want every angle bracket escaped", in, got)
	}
}

func TestFromWikiEscapesInlineHTML(t *testing.T) {
	got := FromWiki(`<a href="javascript:alert(1)">click</a>`)
	if bare := strings.ReplaceAll(got, `\<`, ""); strings.Contains(bare, "<") {
		t.Errorf("FromWiki = %q, want every angle bracket escaped", got)
	}
}
