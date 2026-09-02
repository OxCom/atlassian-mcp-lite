package markup

import (
	"strings"
	"testing"
)

// These are the cases the policy is there for: input where the tree a walk
// builds is not the tree the page appears to describe. Each asserts on the
// markdown FromHTML returns, not on the sanitiser's own output, because the
// markdown is what the model reads.
func TestFromHTMLPolicyDropsDangerousMarkup(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		absent  []string
		present []string
	}{
		{
			name:   "event handler attribute",
			in:     `<p onclick="fetch('//evil.example')">click</p>`,
			absent: []string{"onclick", "evil.example"},
			// The prose survives; only the attribute is gone.
			present: []string{"click"},
		},
		{
			name:   "style element content",
			in:     `<p>before</p><style>body{background:url(//evil.example)}</style><p>after</p>`,
			absent: []string{"evil.example", "background"},
			// A rendered page cannot include CSS the reader is meant to read,
			// so nothing of it belongs in the markdown.
			present: []string{"before", "after"},
		},
		{
			name:   "svg foreign content",
			in:     `<svg><desc><a href="javascript:alert(1)">x</a></desc></svg>`,
			absent: []string{"javascript:", "](", "<svg"},
		},
		{
			name:   "mathml annotation",
			in:     `<math><annotation-xml encoding="text/html"><img src=x onerror="alert(1)"></annotation-xml></math>`,
			absent: []string{"onerror", "annotation-xml"},
		},
		{
			name:   "template content",
			in:     `<template><a href="//evil.example/x">hidden</a></template><p>shown</p>`,
			absent: []string{"evil.example", "hidden"},
			// A template's content is inert in a browser; it must not become
			// live prose here.
			present: []string{"shown"},
		},
		{
			name:    "iframe",
			in:      `<iframe src="//evil.example/x">fallback</iframe><p>page</p>`,
			absent:  []string{"evil.example"},
			present: []string{"page"},
		},
		{
			name:   "comment that looks closed",
			in:     `<!--><img src="x" onerror="alert(1)">--><p>text</p>`,
			absent: []string{"onerror"},
		},
		{
			name:    "unknown attribute on an allowed element",
			in:      `<pre data-lang="` + "```" + `sh"><code>ls</code></pre>`,
			absent:  []string{"data-lang"},
			present: []string{"ls"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FromHTML(c.in)
			for _, s := range c.absent {
				if strings.Contains(got, s) {
					t.Errorf("FromHTML(%q) contains %q:\n%s", c.in, s, got)
				}
			}
			for _, s := range c.present {
				if !strings.Contains(got, s) {
					t.Errorf("FromHTML(%q) lost %q:\n%s", c.in, s, got)
				}
			}
		})
	}
}

func TestFromHTMLPolicyKeepsWhatTheRendererReads(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"http link", `<p><a href="https://example.com/a">label</a></p>`, "[label](https://example.com/a)"},
		{"relative link", `<p><a href="/wiki/spaces/X">label</a></p>`, "[label](/wiki/spaces/X)"},
		{"mailto link", `<p><a href="mailto:a@example.com">mail</a></p>`, "[mail](mailto:a@example.com)"},
		{"image", `<p><img src="/download/a.png" alt="shot"></p>`, "![shot](/download/a.png)"},
		{"code language from class", "<pre><code class=\"language-go\">x</code></pre>", "```go"},
		{"heading", `<h2>Title</h2>`, "## Title"},
		{"list", `<ul><li>one</li></ul>`, "- one"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FromHTML(c.in); !strings.Contains(got, c.want) {
				t.Errorf("FromHTML(%q) = %q, want it to contain %q", c.in, got, c.want)
			}
		})
	}
}

func TestFromHTMLPolicyRefusesJavascriptHrefWithoutEmittingALink(t *testing.T) {
	// The policy drops the attribute rather than blanking it, so the anchor
	// arrives with no href at all. Emitting "[label]()" for that would hand
	// the model a link to nowhere; the label alone is the answer.
	got := FromHTML(`<p><a href="javascript:alert(1)">label</a></p>`)
	if got != "label" {
		t.Errorf("FromHTML = %q, want %q", got, "label")
	}
}

func TestFromHTMLPolicyBoundsColspan(t *testing.T) {
	// An unbounded colspan is a repeat count taken from page content. Five
	// digits fails the policy's pattern, the attribute is dropped, and the
	// cell spans one column.
	got := FromHTML(`<table><tr><td colspan="99999">a</td></tr><tr><td>b</td></tr></table>`)
	if strings.Count(got, "|") > 6 {
		t.Errorf("colspan was honoured:\n%s", got)
	}
	got = FromHTML(`<table><tr><td colspan="2">a</td></tr><tr><td>b</td><td>c</td></tr></table>`)
	if !strings.Contains(got, "| a |  |") {
		t.Errorf("a two-column span was not honoured:\n%s", got)
	}
}

func TestFromHTMLScriptOnlyBodyIsEmpty(t *testing.T) {
	if got := FromHTML(`<script>alert(1)</script>`); got != "" {
		t.Errorf("FromHTML = %q, want the empty string", got)
	}
}
