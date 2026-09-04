package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
)

// maxNameLen bounds the free-form names a caller supplies for lookup — an
// assignee query and a version name. Both travel to Jira as query parameters,
// and neither has a legitimate use anywhere near this length. maxJQLLen bounds
// the equivalent string on the read path; nothing user-controlled is unbounded.
const maxNameLen = 256

// lookupMaxResults caps the user search. Two is enough to detect ambiguity;
// five leaves room for the error message to name the candidates.
const lookupMaxResults = 5

// accountIDFor resolves an email or display name to an accountId. An
// unresolvable or ambiguous query is an error: silently assigning the wrong
// person is worse than failing.
//
// Jira's user search is fuzzy — a partial name matches — so a single result is
// not evidence that it is the right person. Only an exact email or display
// name is accepted, however many results came back.
// accountIDFor resolves an email or display name to an accountId. An
// unresolvable or ambiguous query is an error: silently assigning the wrong
// person is worse than failing.
//
// Jira's user search is a substring match, so a single result is not by itself
// evidence of the right person — a query of "and" can return exactly one user
// called "Alexander". The rules, in order:
//
//   - an exact email match wins, and is the escape hatch the ambiguity error
//     tells the caller to use;
//   - failing that, an exact display-name match wins, when it is the only one;
//   - failing that, a single result is accepted only for an email-shaped query.
//     Sites with GDPR privacy settings match on an address they then decline to
//     return, so insisting on an exact email would make an emailed assignee
//     unresolvable there — and a full address is not a plausible accidental
//     substring of someone else's name.
//
// Anything else is refused.
func (m module) accountIDFor(ctx context.Context, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("assignee is empty")
	}
	if len(query) > maxNameLen {
		return "", fmt.Errorf("assignee is %d bytes, limit is %d", len(query), maxNameLen)
	}
	var users []struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
		Email       string `json:"emailAddress"`
	}
	q := url.Values{"query": {query}, "maxResults": {fmt.Sprint(lookupMaxResults)}}
	if err := m.client.Do(ctx, http.MethodGet, "/rest/api/3/user/search", q, nil, &users); err != nil {
		return "", err
	}
	if len(users) == 0 {
		return "", fmt.Errorf("no Atlassian user matches %q", query)
	}

	var byEmail, byName []string
	names := make([]string, 0, len(users))
	for _, u := range users {
		if u.Email != "" && strings.EqualFold(u.Email, query) {
			byEmail = append(byEmail, u.AccountID)
		}
		if u.DisplayName != "" && strings.EqualFold(u.DisplayName, query) {
			byName = append(byName, u.AccountID)
		}
		names = append(names, u.DisplayName)
	}
	switch {
	case len(byEmail) == 1:
		return byEmail[0], nil
	case len(byEmail) == 0 && len(byName) == 1:
		return byName[0], nil
	case len(users) == 1 && strings.Contains(query, "@"):
		return users[0].AccountID, nil
	}
	return "", fmt.Errorf("%q does not identify exactly one Atlassian user (%d candidates%s); use an exact email address",
		query, len(users), m.candidateSuffix(quoteNames(names)))
}

// maxCandidateNames and maxCandidateRunes bound what an error may echo back
// from Jira: at most this many names, each cut to this many characters. The
// names help a caller pick the right one, but they are third-party data and an
// error message is not a place for an unbounded dump of it.
const (
	maxCandidateNames = 5
	maxCandidateRunes = 80
)

// candidateSuffix renders the candidates an error may list, or nothing at all.
//
// A deployment with read turned off has said its caller may not see Jira's
// data. Listing users, versions or transitions in a write error would hand
// that data over anyway, one failed write at a time, so without read the error
// carries only the count and the hint. With read on, the list is capped and the
// items are expected to be already quoted (see quoteNames), so a name cannot
// masquerade as part of the message.
func (m module) candidateSuffix(items []string) string {
	if !m.cfg.Domains[Domain].Read || len(items) == 0 {
		return ""
	}
	if len(items) > maxCandidateNames {
		items = items[:maxCandidateNames]
	}
	return ": " + strings.Join(items, ", ")
}

// quoteNames truncates each name to maxCandidateRunes and quotes it, so a name
// that is long, or that contains punctuation the message itself uses, is still
// readable as a single value chosen by someone else.
func quoteNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, fmt.Sprintf("%q", core.TruncateRunes(n, maxCandidateRunes)))
	}
	return out
}

// versionIDFor resolves a version name to its id within a project.
// versionIDFor resolves a version name to its id within a project.
//
// An exact name wins outright. Jira permits versions whose names differ only by
// case, so a case-insensitive scan that returned the first hit would resolve
// "Beta" to "beta"'s id — a silent wrong write.
func (m module) versionIDFor(ctx context.Context, projectKey, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("fixVersion is empty")
	}
	if len(name) > maxNameLen {
		return "", fmt.Errorf("fixVersion is %d bytes, limit is %d", len(name), maxNameLen)
	}
	// The project key reaches a URL path segment. It is validated rather than
	// only escaped, so a value that is not a project key never becomes one.
	if !reProjectKey.MatchString(projectKey) {
		return "", fmt.Errorf("invalid project key %q", projectKey)
	}
	var versions []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	path := "/rest/api/3/project/" + url.PathEscape(projectKey) + "/versions"
	if err := m.client.Do(ctx, http.MethodGet, path, nil, nil, &versions); err != nil {
		return "", err
	}

	// Version names are project metadata. A deployment whose read allowlist
	// excludes this project has said its caller may not read the project, so
	// the names stay out of the error even though the write itself was
	// permitted. With no read allowlist AllowReadProject is true and nothing
	// changes.
	disclose := m.cfg.AllowReadProject(projectKey)

	var folded []string
	available := make([]string, 0, len(versions))
	for _, v := range versions {
		if v.Name == name {
			return v.ID, nil
		}
		if strings.EqualFold(v.Name, name) {
			folded = append(folded, v.ID)
		}
		available = append(available, v.Name)
	}
	// A case-insensitive match is accepted only when it is the only one.
	if len(folded) == 1 {
		return folded[0], nil
	}
	if len(folded) > 1 {
		// The count is a fact about a project this deployment may not read —
		// it says how many versions exist there whose names differ from the
		// caller's only by case — so it follows the same gate as the names
		// below. The caller still learns that its own value was not exact,
		// which is what it needs to fix the call.
		if !disclose {
			return "", fmt.Errorf("%q is not an exact version name in %s; use the exact name", name, projectKey)
		}
		return "", fmt.Errorf("%q matches %d versions in %s differing only by case; use the exact name",
			name, len(folded), projectKey)
	}
	if !disclose {
		// How many versions a project has, and whether it has any at all, are
		// facts about a project this deployment may not read. Withheld with
		// the names; the name the caller asked for and the project it named
		// are its own.
		return "", fmt.Errorf("no version named %q in %s; use an exact version name", name, projectKey)
	}
	if len(available) == 0 {
		return "", fmt.Errorf("no version named %q in %s; the project has no versions", name, projectKey)
	}
	return "", fmt.Errorf("no version named %q in %s (%d versions%s); use an exact version name",
		name, projectKey, len(available), m.candidateSuffix(quoteNames(available)))
}
