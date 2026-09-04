package core

import "testing"

func TestInClauseQuotesEveryKey(t *testing.T) {
	got, err := InClause("project", []string{"DEV", "PLATFORM", "INFRA"})
	if err != nil {
		t.Fatalf("InClause: %v", err)
	}
	if want := `project IN ("DEV", "PLATFORM", "INFRA")`; got != want {
		t.Errorf("InClause = %q, want %q", got, want)
	}
}

func TestInClauseAcceptsPersonalSpaceKey(t *testing.T) {
	got, err := InClause("space", []string{"~alice1"})
	if err != nil {
		t.Fatalf("InClause: %v", err)
	}
	if want := `space IN ("~alice1")`; got != want {
		t.Errorf("InClause = %q, want %q", got, want)
	}
}

// A key that could carry query syntax must never reach a clause, even though
// Load rejects it first: this function is the one that does the quoting, so it
// is the one that has to fail closed.
func TestInClauseRejectsKeyCarryingQuerySyntax(t *testing.T) {
	for _, key := range []string{`DEV") OR project=SECRET OR ("`, `DE V`, `DEV\`, ``, `'DEV'`} {
		if _, err := InClause("project", []string{key}); err == nil {
			t.Errorf("InClause(%q) = nil error, want refusal", key)
		}
	}
}

func TestInClauseRejectsEmptyList(t *testing.T) {
	if _, err := InClause("project", nil); err == nil {
		t.Error("InClause(nil) = nil error, want refusal")
	}
}

func TestRestrictQuery(t *testing.T) {
	clause := `project IN ("DEV")`
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "simple condition is parenthesised",
			query: "status = Open",
			want:  `(status = Open) AND project IN ("DEV")`,
		},
		{
			name:  "an OR cannot escape the restriction",
			query: "project = SECRET OR status = Open",
			want:  `(project = SECRET OR status = Open) AND project IN ("DEV")`,
		},
		{
			name:  "nested parentheses are preserved",
			query: "(project = SECRET OR (status = Open AND assignee = me)) OR labels = x",
			want:  `((project = SECRET OR (status = Open AND assignee = me)) OR labels = x) AND project IN ("DEV")`,
		},
		{
			name:  "an existing project clause is not analysed away",
			query: "project = DEV",
			want:  `(project = DEV) AND project IN ("DEV")`,
		},
		{
			name:  "a trailing sort clause stays last",
			query: "status = Open ORDER BY created DESC",
			want:  `(status = Open) AND project IN ("DEV") ORDER BY created DESC`,
		},
		{
			name:  "lower-case and multi-space sort clause",
			query: "status = Open  order   by created",
			want:  `(status = Open) AND project IN ("DEV") order   by created`,
		},
		{
			name:  "a sort clause is the whole query",
			query: "ORDER BY created",
			want:  `project IN ("DEV") ORDER BY created`,
		},
		{
			name:  "order by inside a quoted value is not a sort clause",
			query: `summary ~ "order by created"`,
			want:  `(summary ~ "order by created") AND project IN ("DEV")`,
		},
		{
			name:  "order by inside parentheses is not the top-level sort clause",
			query: `status IN (order, by) AND summary ~ "x"`,
			want:  `(status IN (order, by) AND summary ~ "x") AND project IN ("DEV")`,
		},
		{
			name:  "the last top-level sort clause is the one that moves",
			query: `summary ~ "a order by b" ORDER BY updated`,
			want:  `(summary ~ "a order by b") AND project IN ("DEV") ORDER BY updated`,
		},
		{
			name:  "orderby without a boundary is a field name, not a clause",
			query: "orderby = 3",
			want:  `(orderby = 3) AND project IN ("DEV")`,
		},
		{
			name:  "reorder by is not a sort clause",
			query: "reorder by = 3",
			want:  `(reorder by = 3) AND project IN ("DEV")`,
		},
		{
			name:  "an escaped quote does not close the literal",
			query: `summary ~ "a\" order by b"`,
			want:  `(summary ~ "a\" order by b") AND project IN ("DEV")`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := RestrictQuery(c.query, clause)
			if err != nil {
				t.Fatalf("RestrictQuery(%q): %v", c.query, err)
			}
			if got != c.want {
				t.Errorf("RestrictQuery(%q) = %q, want %q", c.query, got, c.want)
			}
		})
	}
}

// An unbalanced query is the one shape parenthesising cannot restrict: wrapped,
// `status = Open) OR (status != Open` becomes
// `(status = Open) OR (status != Open) AND project IN ("DEV")`, and because AND
// binds tighter than OR the first disjunct carries no restriction at all. It is
// refused rather than repaired.
func TestRestrictQueryRefusesQueryItCannotBind(t *testing.T) {
	for _, query := range []string{
		"status = Open) OR (status != Open",
		"type = page) OR (type != page",
		")",
		"status = Open)",
		"(status = Open",
		"status = Open AND (labels = x",
		`summary ~ "unterminated`,
		`summary ~ 'unterminated`,
		// The escape consumes the closing quote, so the literal is still open.
		`summary ~ "a\"`,
	} {
		if _, err := RestrictQuery(query, `project IN ("DEV")`); err == nil {
			t.Errorf("RestrictQuery(%q) = nil error, want refusal", query)
		}
	}
}

// A balanced query whose parentheses are inside a quoted value is not
// unbalanced, and must still compose.
func TestRestrictQueryAcceptsParenthesesInsideValues(t *testing.T) {
	got, err := RestrictQuery(`summary ~ "a) (b"`, `project IN ("DEV")`)
	if err != nil {
		t.Fatalf("RestrictQuery: %v", err)
	}
	if want := `(summary ~ "a) (b") AND project IN ("DEV")`; got != want {
		t.Errorf("RestrictQuery = %q, want %q", got, want)
	}
}
