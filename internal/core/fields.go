package core

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	// maxFieldNameLen bounds a single field name. Atlassian's longest real name
	// is a custom field id at around twenty characters; anything far beyond that
	// is a mistake or an attempt at something.
	maxFieldNameLen = 100

	// maxFieldCount bounds how many fields one request may name. Enough for
	// "*all" plus a long explicit list, small enough that a query string cannot
	// be inflated without limit.
	maxFieldCount = 100

	// maxRequestEntries bounds the raw request before it is parsed. maxFieldCount
	// applies to the deduplicated result, so on its own it would still admit an
	// arbitrarily long list of duplicates.
	maxRequestEntries = 500
)

// fieldNameRe is the shape an ordinary field name may take.
//
// A name reaches a query string and a JSON object key, so it is validated here,
// at the boundary, rather than at the point of use. A comma in particular would
// inject a second field into the query; whitespace and separators are excluded
// for the same reason.
var fieldNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]*$`)

// starSelectors are Atlassian's own field selectors, allowed by name rather than
// by pattern. A pattern of `\*?...` would have admitted `*anything`, which
// passes local validation and then fails at Atlassian — moving the error away
// from the thing that caused it.
var starSelectors = map[string]bool{
	"*all":       true,
	"*navigable": true,
}

// ErrFieldSelection is the class of error returned for an unusable field
// request, so a caller can tell a bad request from a transport failure.
var ErrFieldSelection = errors.New("invalid field selection")

// ResolveFields turns a caller's field request into the concrete list to send.
//
// The grammar is deliberately small:
//
//	omitted or empty  the tool's default set
//	"name"            replaces the default set entirely
//	"+name"           added to the default set
//	"-name"           removed from the default set
//
// Bare and prefixed forms may not be mixed. They express different intents —
// replace the set, or adjust it — so a request containing both is ambiguous, and
// guessing which the caller meant would silently return the wrong data.
func ResolveFields(defaults, requested []string) ([]string, error) {
	// Bounded before anything is allocated. Capping only the deduplicated
	// output would let an arbitrarily long list of duplicates be parsed and
	// grouped first.
	if len(requested) > maxRequestEntries {
		return nil, fmt.Errorf("%w: %d entries requested, at most %d are allowed",
			ErrFieldSelection, len(requested), maxRequestEntries)
	}

	// Defaults are validated once, before branching. An earlier version checked
	// them only on the add/remove path, so an omitted request returned them
	// straight to the query string unchecked — the one path where validation
	// mattered most, because it is the common case.
	for _, name := range defaults {
		if err := validateFieldName(name); err != nil {
			return nil, fmt.Errorf("default field set: %w", err)
		}
	}

	var bare, added, removed []string

	for _, raw := range requested {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		switch entry[0] {
		case '+', '-':
			name := strings.TrimSpace(entry[1:])
			if name == "" {
				// A prefix with no name is malformed, not empty. Discarding it
				// would silently return the defaults for a request the caller
				// clearly meant to change something with.
				return nil, fmt.Errorf("%w: %q has a prefix but no field name", ErrFieldSelection, entry)
			}
			if entry[0] == '+' {
				added = append(added, name)
			} else {
				removed = append(removed, name)
			}
		default:
			bare = append(bare, entry)
		}
	}

	if len(bare) > 0 && (len(added) > 0 || len(removed) > 0) {
		return nil, fmt.Errorf(`%w: cannot mix bare names with "+" or "-" prefixes; `+
			`use bare names to replace the default set, or prefixes to adjust it`, ErrFieldSelection)
	}

	for _, group := range [][]string{bare, added, removed} {
		for _, name := range group {
			if err := validateFieldName(name); err != nil {
				return nil, err
			}
		}
	}

	if len(bare) > 0 {
		return finalise(bare)
	}

	if len(added) == 0 && len(removed) == 0 {
		if len(defaults) == 0 {
			return nil, fmt.Errorf("%w: no fields requested and the tool has no default set", ErrFieldSelection)
		}
		return finalise(defaults)
	}

	// A field both added and removed is a contradiction. Serving an arbitrary
	// winner would give the caller data they did not ask for, or withhold data
	// they did.
	removeSet := make(map[string]bool, len(removed))
	for _, name := range removed {
		removeSet[strings.ToLower(name)] = true
	}
	for _, name := range added {
		if removeSet[strings.ToLower(name)] {
			return nil, fmt.Errorf("%w: %q is both added and removed", ErrFieldSelection, name)
		}
	}

	result := make([]string, 0, len(defaults)+len(added))
	for _, name := range defaults {
		// Removal is case-insensitive: Atlassian field names arrive in mixed
		// case and the caller should not have to match it exactly.
		if !removeSet[strings.ToLower(name)] {
			result = append(result, name)
		}
	}
	result = append(result, added...)

	return finalise(result)
}

// finalise deduplicates case-insensitively, preserving first-seen order, and
// rejects an empty outcome.
func finalise(names []string) ([]string, error) {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: the request resolves to no fields at all", ErrFieldSelection)
	}
	if len(out) > maxFieldCount {
		return nil, fmt.Errorf("%w: %d fields requested, at most %d are allowed",
			ErrFieldSelection, len(out), maxFieldCount)
	}
	return out, nil
}

func validateFieldName(name string) error {
	// Runes, not bytes: a length limit measured in bytes would reject a shorter
	// name written in a multi-byte script for the wrong reason. The pattern
	// rejects non-ASCII anyway, so this only affects which error is reported.
	if n := len([]rune(name)); n > maxFieldNameLen {
		return fmt.Errorf("%w: field name is %d characters, at most %d are allowed",
			ErrFieldSelection, n, maxFieldNameLen)
	}
	if starSelectors[strings.ToLower(name)] {
		return nil
	}
	if !fieldNameRe.MatchString(name) {
		return fmt.Errorf("%w: %q is not a valid field name", ErrFieldSelection, name)
	}
	return nil
}
