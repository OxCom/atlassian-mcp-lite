package jira

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAccountIDForResolvesEmail(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/user/search" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if q := r.URL.Query().Get("query"); q != "u@example.com" {
			t.Errorf("query = %q", q)
		}
		_, _ = io.WriteString(w, `[{"accountId":"aid-1","displayName":"A User"}]`)
	}).(module)

	got, err := m.accountIDFor(context.Background(), "u@example.com")
	if err != nil {
		t.Fatalf("accountIDFor: %v", err)
	}
	if got != "aid-1" {
		t.Errorf("accountId = %q", got)
	}
}

func TestAccountIDForNoMatchIsAnError(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[]`)
	}).(module)
	if _, err := m.accountIDFor(context.Background(), "nobody@example.com"); err == nil {
		t.Fatal("an unresolvable assignee must be an error, not a silent no-op")
	}
}

func TestAccountIDForAmbiguousIsAnError(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"accountId":"a","displayName":"Ada Lovelace"},{"accountId":"b","displayName":"Adam Smith"}]`)
	}).(module)
	if _, err := m.accountIDFor(context.Background(), "Ada"); err == nil {
		t.Fatal("an ambiguous assignee must be an error, not a guess")
	}
}

// The user search matches substrings, so one result is not evidence of the
// right person: a query of "Ada" returning only "Alexander Adams" must not
// silently assign them.
func TestAccountIDForSingleFuzzyNameMatchIsRefused(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"accountId":"a","displayName":"Alexander Adams"}]`)
	}).(module)
	if _, err := m.accountIDFor(context.Background(), "Ada"); err == nil {
		t.Fatal("a partial name match must be refused, not assigned")
	}
}

// An exact display name is accepted even alongside other partial matches:
// it identifies one person, and refusing it would leave a caller who has no
// access to email addresses no way through.
func TestAccountIDForExactDisplayNameWins(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"accountId":"a","displayName":"Ada"},{"accountId":"b","displayName":"Adam Smith"}]`)
	}).(module)
	got, err := m.accountIDFor(context.Background(), "ada")
	if err != nil {
		t.Fatalf("accountIDFor: %v", err)
	}
	if got != "a" {
		t.Errorf("accountId = %q, want the exactly-named user", got)
	}
}

// Two people can share a display name. Then it identifies nobody.
func TestAccountIDForDuplicateDisplayNamesAreRefused(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"accountId":"a","displayName":"Ada"},{"accountId":"b","displayName":"ada"}]`)
	}).(module)
	if _, err := m.accountIDFor(context.Background(), "Ada"); err == nil {
		t.Fatal("two users with the same display name must be an error")
	}
}

// An exact, case-insensitive email match resolves even when the site returns
// several partial matches: refusing there would make the documented escape
// hatch ("use an exact email address") impossible to use.
func TestAccountIDForExactEmailWinsAmbiguity(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"accountId":"a","displayName":"A","emailAddress":"other@example.com"},
			{"accountId":"b","displayName":"B","emailAddress":"Target@Example.com"}]`)
	}).(module)
	got, err := m.accountIDFor(context.Background(), "target@example.com")
	if err != nil {
		t.Fatalf("accountIDFor: %v", err)
	}
	if got != "b" {
		t.Errorf("accountId = %q, want b", got)
	}
}

func TestAccountIDForEmptyIsAnErrorWithNoRequest(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for an empty query")
	}).(module)
	if _, err := m.accountIDFor(context.Background(), "   "); err == nil {
		t.Fatal("an empty assignee must error")
	}
}

func TestAccountIDForOverlongQueryIsRefused(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for an overlong query")
	}).(module)
	if _, err := m.accountIDFor(context.Background(), strings.Repeat("x", maxNameLen+1)); err == nil {
		t.Fatal("an unbounded assignee query must be refused")
	}
}

func TestAccountIDForUpstreamErrorPropagates(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"errorMessages":["nope"]}`)
	}).(module)
	if _, err := m.accountIDFor(context.Background(), "u@example.com"); err == nil {
		t.Fatal("an upstream failure must not be swallowed")
	}
}

func TestVersionIDForMatchesByNameCaseInsensitively(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/project/PROJ/versions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `[{"id":"111","name":"1.2.x"},{"id":"222","name":"Backlog"}]`)
	}).(module)

	got, err := m.versionIDFor(context.Background(), "PROJ", "backlog")
	if err != nil {
		t.Fatalf("versionIDFor: %v", err)
	}
	if got != "222" {
		t.Errorf("id = %q, want 222", got)
	}
}

func TestVersionIDForUnknownNameListsAvailable(t *testing.T) {
	m := newTestModule(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"id":"111","name":"1.2.x"},{"id":"222","name":"Backlog"}]`)
	}).(module)
	_, err := m.versionIDFor(context.Background(), "PROJ", "nope")
	if err == nil {
		t.Fatal("unknown version must error")
	}
	// Every candidate must be listed, not just the ones scanned before a
	// match: the message is the caller's only way to find the right name.
	for _, want := range []string{"1.2.x", "Backlog"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should list available version %q, got %q", want, err)
		}
	}
}

func TestVersionIDForEmptyNameIsAnErrorWithNoRequest(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for an empty version name")
	}).(module)
	if _, err := m.versionIDFor(context.Background(), "PROJ", " "); err == nil {
		t.Fatal("an empty fixVersion must error")
	}
}

func TestVersionIDForRejectsUnsafeProjectKey(t *testing.T) {
	m := newTestModule(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made for an invalid project key")
	}).(module)
	if _, err := m.versionIDFor(context.Background(), "../../admin", "1.0"); err == nil {
		t.Fatal("a project key that is not a project key must be refused before the URL is built")
	}
}
