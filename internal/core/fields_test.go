package core

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

var testFieldDefaults = []string{"key", "summary", "status"}

func TestResolveFieldsOmittedReturnsDefaults(t *testing.T) {
	for _, requested := range [][]string{nil, {}, {"", "   "}} {
		got, err := ResolveFields(testFieldDefaults, requested)
		if err != nil {
			t.Fatalf("ResolveFields(%v): %v", requested, err)
		}
		if !slices.Equal(got, testFieldDefaults) {
			t.Errorf("ResolveFields(%v) = %v, want the defaults %v", requested, got, testFieldDefaults)
		}
	}
}

// The default set must not be reachable through the returned slice: a caller
// that appends to its result would otherwise corrupt every later call.
func TestResolveFieldsDoesNotAliasTheDefaults(t *testing.T) {
	defaults := []string{"key", "summary"}
	got, err := ResolveFields(defaults, nil)
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	got[0] = "mutated"
	if defaults[0] != "key" {
		t.Errorf("the caller's defaults were mutated: %v", defaults)
	}
}

func TestResolveFieldsBareNamesReplace(t *testing.T) {
	got, err := ResolveFields(testFieldDefaults, []string{"summary", "assignee"})
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	if !slices.Equal(got, []string{"summary", "assignee"}) {
		t.Errorf("got %v, want exactly the requested names in order", got)
	}
}

func TestResolveFieldsPlusAddsToDefaults(t *testing.T) {
	got, err := ResolveFields(testFieldDefaults, []string{"+description"})
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	want := []string{"key", "summary", "status", "description"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v: additions follow the defaults, in order", got, want)
	}
}

func TestResolveFieldsMinusRemovesFromDefaults(t *testing.T) {
	got, err := ResolveFields(testFieldDefaults, []string{"-status"})
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	if !slices.Equal(got, []string{"key", "summary"}) {
		t.Errorf("got %v, want the defaults minus status", got)
	}
}

func TestResolveFieldsCombinesAddAndRemove(t *testing.T) {
	got, err := ResolveFields(testFieldDefaults, []string{"+description", "-status"})
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	want := []string{"key", "summary", "description"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Bare and prefixed forms express different intents — replace the set, or adjust
// it — and a request containing both is ambiguous rather than merely unusual.
func TestResolveFieldsRejectsMixedForms(t *testing.T) {
	for _, requested := range [][]string{
		{"summary", "+description"},
		{"-status", "summary"},
		{"summary", "-status", "+description"},
	} {
		_, err := ResolveFields(testFieldDefaults, requested)
		if err == nil {
			t.Errorf("ResolveFields(%v) must be an error", requested)
			continue
		}
		if !strings.Contains(err.Error(), "mix") {
			t.Errorf("ResolveFields(%v) = %v, want the error to explain the mixing rule", requested, err)
		}
	}
}

func TestResolveFieldsRejectsAnEmptyResult(t *testing.T) {
	_, err := ResolveFields(testFieldDefaults, []string{"-key", "-summary", "-status"})
	if err == nil {
		t.Fatal("removing every field must be an error, not a request for nothing")
	}
}

func TestResolveFieldsRemovingAnAbsentFieldIsNotAnError(t *testing.T) {
	// The caller cannot be expected to know the default set by heart, so asking
	// to remove something already absent is a no-op rather than a failure.
	got, err := ResolveFields(testFieldDefaults, []string{"-labels"})
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	if !slices.Equal(got, testFieldDefaults) {
		t.Errorf("got %v, want the defaults unchanged", got)
	}
}

func TestResolveFieldsIsCaseInsensitiveForRemoval(t *testing.T) {
	// Atlassian field names arrive in mixed case; "-Status" must remove
	// "status".
	got, err := ResolveFields(testFieldDefaults, []string{"-STATUS"})
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	if slices.Contains(got, "status") {
		t.Errorf("got %v, want status removed regardless of case", got)
	}
}

func TestResolveFieldsDeduplicates(t *testing.T) {
	got, err := ResolveFields(testFieldDefaults, []string{"+description", "+description", "+DESCRIPTION"})
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("got %v (%d fields), want 4 unique", got, len(got))
	}
}

// Adding a field already in the defaults must not duplicate it or reorder it.
func TestResolveFieldsAddingAnExistingDefaultIsANoOp(t *testing.T) {
	got, err := ResolveFields(testFieldDefaults, []string{"+summary"})
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	if !slices.Equal(got, testFieldDefaults) {
		t.Errorf("got %v, want the defaults unchanged", got)
	}
}

// A field removed and added in the same request is contradictory; the caller
// should be told rather than served an arbitrary winner.
func TestResolveFieldsRejectsAddingAndRemovingTheSameField(t *testing.T) {
	_, err := ResolveFields(testFieldDefaults, []string{"+labels", "-labels"})
	if err == nil {
		t.Fatal("adding and removing the same field must be an error")
	}
	if !strings.Contains(err.Error(), "labels") {
		t.Errorf("err = %v, want the contradictory field named", err)
	}
}

func TestResolveFieldsIgnoresBlanksButRejectsBarePrefixes(t *testing.T) {
	// Blank entries are noise and are skipped.
	got, err := ResolveFields(testFieldDefaults, []string{"+description", "", "  "})
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	want := []string{"key", "summary", "status", "description"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// A prefix with no name is not noise, it is malformed. Discarding it used
	// to mean a request of just {"+"} silently returned the defaults, for a
	// caller who plainly meant to change something.
	for _, requested := range [][]string{{"+"}, {"-"}, {"+description", "-"}, {" + "}} {
		if _, err := ResolveFields(testFieldDefaults, requested); err == nil {
			t.Errorf("ResolveFields(%v) must reject a prefix with no field name", requested)
		}
	}
}

func TestResolveFieldsTrimsWhitespaceAroundNames(t *testing.T) {
	got, err := ResolveFields(testFieldDefaults, []string{" + description ", " - status "})
	if err != nil {
		t.Fatalf("ResolveFields: %v", err)
	}
	want := []string{"key", "summary", "description"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A field name travels into a query string and a JSON object key, so it is
// validated at this boundary rather than at the point of use.
func TestResolveFieldsRejectsMalformedNames(t *testing.T) {
	for _, name := range []string{
		"sum mary",       // space
		"summary,status", // would inject a second field into the query
		"summary;drop",
		"summary\nstatus",
		"../../etc/passwd",
		"summary=1",
		strings.Repeat("a", 300), // absurd length
	} {
		if _, err := ResolveFields(testFieldDefaults, []string{name}); err == nil {
			t.Errorf("bare %q must be rejected", name)
		}
		if _, err := ResolveFields(testFieldDefaults, []string{"+" + name}); err == nil {
			t.Errorf("added %q must be rejected", name)
		}
	}
}

func TestResolveFieldsAcceptsRealAtlassianFieldNames(t *testing.T) {
	for _, name := range []string{"summary", "customfield_10014", "issuetype", "fixVersions", "*all", "*navigable"} {
		if _, err := ResolveFields(testFieldDefaults, []string{name}); err != nil {
			t.Errorf("%q must be accepted: %v", name, err)
		}
	}
}

// An empty default set is a programming error in the caller, not a request.
func TestResolveFieldsRejectsEmptyDefaultsWhenRelyingOnThem(t *testing.T) {
	if _, err := ResolveFields(nil, nil); err == nil {
		t.Error("no defaults and no request must be an error rather than an empty field list")
	}
	if _, err := ResolveFields(nil, []string{"+summary"}); err != nil {
		t.Errorf("an addition supplies its own content, so it should succeed: %v", err)
	}
}

// The omitted-request path is the common one, and it used to return the defaults
// straight to the query string without validating them — the single place where
// validation mattered most.
func TestResolveFieldsValidatesTheDefaultsOnEveryPath(t *testing.T) {
	badDefaults := []string{"key", "summary,status"}
	for _, requested := range [][]string{nil, {"+description"}, {"-key"}} {
		_, err := ResolveFields(badDefaults, requested)
		if err == nil {
			t.Errorf("ResolveFields(badDefaults, %v) must reject the malformed default", requested)
			continue
		}
		if !strings.Contains(err.Error(), "default field set") {
			t.Errorf("%v: err = %v, want the error to name the default set", requested, err)
		}
	}
}

// Only Atlassian's own selectors are accepted with a leading star. A pattern of
// `\*?name` would admit `*anything`, which passes here and then fails at
// Atlassian, moving the error away from its cause.
func TestResolveFieldsStarSelectorsAreAnAllowlist(t *testing.T) {
	for _, name := range []string{"*all", "*navigable", "*ALL"} {
		if _, err := ResolveFields(testFieldDefaults, []string{name}); err != nil {
			t.Errorf("%q must be accepted: %v", name, err)
		}
	}
	for _, name := range []string{"*anything", "*", "*foo.bar", "**all"} {
		if _, err := ResolveFields(testFieldDefaults, []string{name}); err == nil {
			t.Errorf("%q must be rejected", name)
		}
	}
}

// The output cap alone would still let an arbitrarily long list of duplicates be
// parsed and grouped first.
func TestResolveFieldsBoundsTheRawRequest(t *testing.T) {
	huge := make([]string, maxRequestEntries+1)
	for i := range huge {
		huge[i] = "+summary"
	}
	_, err := ResolveFields(testFieldDefaults, huge)
	if err == nil {
		t.Fatal("an oversized request must be rejected before it is parsed")
	}
	if !strings.Contains(err.Error(), "entries") {
		t.Errorf("err = %v, want the entry limit named", err)
	}
}

func TestResolveFieldsRejectsTooManyDistinctFields(t *testing.T) {
	many := make([]string, 0, maxFieldCount+5)
	for i := range maxFieldCount + 5 {
		many = append(many, fmt.Sprintf("field%d", i))
	}
	if _, err := ResolveFields(testFieldDefaults, many); err == nil {
		t.Fatal("more distinct fields than the cap must be rejected")
	}
}

// Non-ASCII is rejected by the pattern, and the length check counts runes so a
// short multi-byte name fails for the right reason.
func TestResolveFieldsRejectsNonASCIINames(t *testing.T) {
	// The last one is a zero-width space, written as an escape so it is visible
	// to a reader rather than lurking in the literal.
	for _, name := range []string{"résumé", "поле", "field\\u200bname"} {
		if _, err := ResolveFields(testFieldDefaults, []string{name}); err == nil {
			t.Errorf("%q must be rejected", name)
		}
	}
}
