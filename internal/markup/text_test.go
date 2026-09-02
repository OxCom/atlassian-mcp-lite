package markup

import "testing"

// A plain scalar — an issue summary, a page title, a status name — is not
// markup and gets no conversion, but it is still written by a third party. The
// two things it can do are pinned here: forge terminal or line structure with a
// control character, and forge markdown structure with link, image, HTML or
// code syntax.
func TestSafeText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Ordinary values must survive byte for byte: several of them are
		// round-trip inputs — jira_transition takes a status name, jira_update
		// takes a version name and a summary — and an escape that reached
		// those would be written back into Atlassian as punctuation.
		{"plain summary", "Fix login timeout", "Fix login timeout"},
		{"version name", "1.0-beta_2", "1.0-beta_2"},
		{"asterisks alone are not link syntax", "*urgent* review", "*urgent* review"},
		{"pipe alone is not link syntax", "A | B", "A | B"},

		// Control characters always go, escape or no escape.
		{"escape sequence removed", "Done\x1b[31m red", "Done[31m red"},
		{"newline removed", "line one\nline two", "line oneline two"},
		{"c1 control removed", "next\u0085line", "nextline"},

		// A value carrying the syntax that forges structure is escaped whole.
		// The brackets are what makes a markdown link; escaping them is enough
		// to kill it, so the parentheses are left as ordinary punctuation.
		{
			"markdown link is disarmed",
			"[click](javascript:alert(1))",
			`\[click\](javascript:alert(1))`,
		},
		{
			"inline html is disarmed",
			`see <a href="javascript:alert(1)">x</a>`,
			`see \<a href="javascript:alert(1)">x\</a>`,
		},
		{"code span is disarmed", "run `rm -rf /`", "run \\`rm -rf /\\`"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SafeText(c.in); got != c.want {
				t.Errorf("SafeText(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// The escape must not run twice on a value that already carries a backslash
// for an unrelated reason: escaping is triggered by link, HTML or code syntax,
// and a lone backslash is none of those.
func TestSafeTextLeavesABackslashAloneWithoutStructure(t *testing.T) {
	const in = `C:\Users\ada`
	if got := SafeText(in); got != in {
		t.Errorf("SafeText(%q) = %q, want it unchanged", in, got)
	}
}
