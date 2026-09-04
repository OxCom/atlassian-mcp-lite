package core

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Query composition for the read allowlists. JQL and CQL are close enough in
// this one respect — a trailing sort clause and a boolean body — that both
// modules restrict a caller's query through the same code. Two copies of this
// would be two chances for one of them to drop the parentheses.

// InClause renders `field IN ("A", "B")` from an allowlist.
//
// Every key is re-checked against the same pattern Load enforces, even though
// Load has already run: this is the function that puts a configured value into
// a query language, so it is the function that must not be able to emit a
// quote or a backslash. A key that fails is an error rather than a skipped
// entry, because a silently shortened allowlist is a policy nobody wrote.
func InClause(field string, keys []string) (string, error) {
	quoted := make([]string, 0, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if !keyRe.MatchString(k) {
			return "", fmt.Errorf("%q is not a valid key", k)
		}
		quoted = append(quoted, `"`+k+`"`)
	}
	if len(quoted) == 0 {
		return "", fmt.Errorf("no keys to restrict %s to", field)
	}
	return field + " IN (" + strings.Join(quoted, ", ") + ")", nil
}

// RestrictQuery ANDs clause onto the caller's query, parenthesising what the
// caller wrote so that any OR inside it binds tighter than the restriction.
// Without the parentheses, `project = SECRET OR status = Open` would parse as
// `project = SECRET OR (status = Open AND project IN (...))` and return the
// forbidden project's issues.
//
// A trailing ORDER BY is moved to the end of the composed query: both JQL and
// CQL require the sort clause last, so wrapping it in the parentheses would
// turn a valid query into a syntax error.
func RestrictQuery(query, clause string) (string, error) {
	orderAt, err := scanQuery(query)
	if err != nil {
		return "", err
	}
	body, order := query, ""
	if orderAt >= 0 {
		body, order = query[:orderAt], query[orderAt:]
	}
	body = strings.TrimSpace(body)
	order = strings.TrimSpace(order)

	var composed string
	if body == "" {
		// A query that is nothing but a sort clause has no body to bind, so
		// the restriction is the whole condition.
		composed = clause
	} else {
		composed = "(" + body + ") AND " + clause
	}
	if order != "" {
		composed += " " + order
	}
	return composed, nil
}

// Refusals from scanQuery. Neither message quotes the query: the caller wrote
// it and gains nothing from an echo, and it travels to a model that must not
// be handed a confusing partial parse of its own input.
var (
	errUnbalanced   = errors.New("query has unbalanced parentheses")
	errUnterminated = errors.New("query has an unterminated quoted value")
)

// scanQuery finds the byte offset of the trailing ORDER BY clause, returning
// -1 when there is none, and refuses a query this code cannot compose onto
// safely.
//
// The scan tracks quoting and parenthesis depth because neither language
// forbids the words elsewhere: a value may contain "order by", and a subquery
// may carry its own. Only the last top-level, unquoted occurrence is the sort
// clause. The same walk is what proves the query is balanced — a parenthesis
// or a quote left open would make the restriction bind to only part of the
// query upstream.
func scanQuery(query string) (int, error) {
	// Runes are needed for the word-boundary tests, byte offsets for slicing
	// the query the caller actually sent, so both are kept.
	r := make([]rune, 0, len(query))
	off := make([]int, 0, len(query))
	for i, c := range query {
		r = append(r, c)
		off = append(off, i)
	}

	depth := 0
	var quote rune
	last := -1
	for i := 0; i < len(r); i++ {
		c := r[i]
		if quote != 0 {
			// A backslash escapes the next character in both JQL and CQL
			// string literals, so a quote it precedes does not close the
			// literal.
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(':
			depth++
		case ')':
			// A close parenthesis with nothing open is the bypass this
			// function exists to refuse. `status = Open) OR (status != Open`
			// is valid JQL once wrapped — it becomes
			// `(status = Open) OR (status != Open) AND project IN (...)`, and
			// because AND binds tighter than OR the first half of that
			// disjunction carries no restriction at all. There is no
			// parenthesisation that fixes an unbalanced query, so it is
			// refused rather than repaired.
			if depth == 0 {
				return -1, errUnbalanced
			}
			depth--
		default:
			if depth != 0 || (c != 'o' && c != 'O') {
				continue
			}
			if i > 0 && isWordRune(r[i-1]) {
				continue
			}
			if orderByAt(r, i) {
				last = off[i]
			}
		}
	}
	// An unterminated literal is refused for the same reason: what follows the
	// opening quote upstream is not what this code read, so the composition it
	// produced cannot be reasoned about.
	if quote != 0 {
		return -1, errUnterminated
	}
	if depth != 0 {
		return -1, errUnbalanced
	}
	return last, nil
}

// orderByAt reports whether r[i:] starts with the two words ORDER BY, followed
// by a word boundary.
func orderByAt(r []rune, i int) bool {
	i, ok := wordAt(r, i, "order")
	if !ok {
		return false
	}
	if i >= len(r) || !unicode.IsSpace(r[i]) {
		return false
	}
	for i < len(r) && unicode.IsSpace(r[i]) {
		i++
	}
	i, ok = wordAt(r, i, "by")
	if !ok {
		return false
	}
	return i >= len(r) || !isWordRune(r[i])
}

// wordAt matches word case-insensitively at r[i:], returning the index after
// it.
func wordAt(r []rune, i int, word string) (int, bool) {
	for _, w := range word {
		if i >= len(r) || unicode.ToLower(r[i]) != w {
			return i, false
		}
		i++
	}
	return i, true
}

func isWordRune(c rune) bool {
	return c == '_' || unicode.IsLetter(c) || unicode.IsDigit(c)
}
