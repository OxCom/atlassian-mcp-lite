package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
)

// fieldProject is the project field name, in a field list and in a returned
// field map alike.
const fieldProject = "project"

// Read authorization for Jira. ATLAS_READ_PROJECTS is enforced here and
// nowhere else, so the two read tools cannot drift apart, and it is enforced
// in this process rather than by asking the caller for a well-formed JQL: a
// filter the model supplies is a filter the model can omit.

// deniedRead is the message a refused read returns. It names the resource the
// caller asked for — that value came from the caller, so echoing it leaks
// nothing — and never the project the resource turned out to be in, which
// would tell a caller where a forbidden issue lives, nor any of its content.
func deniedRead(resource string) error {
	return fmt.Errorf("access denied: %s is not in a Jira project permitted by the configured read allowlist (ATLAS_READ_PROJECTS)", resource)
}

// movedIssueRefusal renders a write refusal for an issue whose current project
// is not writable.
//
// Where the issue actually lives is named only when the read allowlist covers
// that project. Otherwise the refusal would report where an issue this
// deployment may not read has been moved to — the one fact deniedRead exists
// to withhold — and a write permission is not a read permission. With no read
// allowlist AllowReadProject is true and the message is the one it always was.
func (m module) movedIssueRefusal(what, key, current string) error {
	if !m.cfg.AllowReadProject(current) {
		return fmt.Errorf("%s %q does not live in a project permitted by ATLAS_WRITE_PROJECTS", what, key)
	}
	return fmt.Errorf("%s %q now lives in project %q, which is not permitted by ATLAS_WRITE_PROJECTS", what, key, current)
}

// applyReadProjectFilter ANDs the read allowlist onto the caller's JQL.
//
// The caller's query is never inspected for a project clause it may already
// carry: a query that names a permitted project loses nothing by the extra
// condition, and one that names a forbidden project must not be trusted to
// have been honest about it. The restriction is added unconditionally, so
// there is no analysis to get wrong.
//
// An empty allowlist returns the query unchanged, which is the documented
// unrestricted default.
func applyReadProjectFilter(jql string, projects []string) (string, error) {
	if len(projects) == 0 {
		return jql, nil
	}
	// Jira project keys are narrower than the key pattern core validates —
	// a Confluence personal space may start with "~", a Jira project may not.
	// Checking here keeps a value that could never match a real project from
	// silently becoming part of a security clause.
	for _, p := range projects {
		if !reProjectKey.MatchString(strings.TrimSpace(p)) {
			return "", fmt.Errorf("ATLAS_READ_PROJECTS: %q is not a Jira project key", p)
		}
	}
	clause, err := core.InClause("project", projects)
	if err != nil {
		return "", fmt.Errorf("ATLAS_READ_PROJECTS: %w", err)
	}
	// A query this server cannot compose onto safely is refused, not repaired:
	// an unbalanced `status = Open) OR (status != Open` would otherwise wrap
	// into a disjunction whose first half carries no restriction.
	restricted, err := core.RestrictQuery(jql, clause)
	if err != nil {
		return "", fmt.Errorf("jql: %w", err)
	}
	return restricted, nil
}

// authorizeFetchedIssue decides on the response that carries the issue, not on
// a separate request: `fields` is the field map jira_get just received, with
// `project` in it because the handler asked for it.
//
// Deciding here rather than in a pre-flight request closes a check-then-fetch
// window. A pre-check plus a fetch are two points in time, and an issue moved
// into a forbidden project between them would have been served on the strength
// of a project it no longer belonged to.
//
// The issue key's prefix is not consulted at all. Jira keeps every key an
// issue has ever had resolving to it, so DEV-123 may today live in SECRET —
// and a prefix that passes proves nothing while a prefix that fails would
// refuse a legitimately moved issue the operator can read.
func (m module) authorizeFetchedIssue(key string, fields map[string]json.RawMessage) error {
	if !m.cfg.RestrictsReadProjects() {
		return nil
	}
	raw, ok := fields[fieldProject]
	if !ok {
		// Fails closed: Jira drops a field name it does not recognise or the
		// account cannot see, and an issue whose project did not come back is
		// an issue there is nothing to check.
		return fmt.Errorf("issue %s returned no project, so the allowlist cannot be checked", key)
	}
	var project struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &project); err != nil || project.Key == "" {
		return fmt.Errorf("issue %s returned no project key, so the allowlist cannot be checked", key)
	}
	if !m.cfg.AllowReadProject(project.Key) {
		return deniedRead("issue " + key)
	}
	return nil
}

// fieldsInclude reports whether the caller's resolved field list already asks
// for name, including through a star selector, which brings every field back
// under its own name.
func fieldsInclude(fields []string, name string) bool {
	for _, f := range fields {
		if strings.EqualFold(f, name) || core.IsStarSelector(f) {
			return true
		}
	}
	return false
}

// authorizeRead refuses a read of an issue outside the read allowlist, by a
// request of its own. jira_get does not use it — it decides on the response
// that carries the issue, see authorizeFetchedIssue — but a caller that has no
// such response, such as the transition path deciding what an error may
// disclose, does.
//
// The issue key's prefix is not consulted at all, not even as a first filter.
// Jira keeps every key an issue has ever had resolving to it, so DEV-123 may
// today live in SECRET — and unlike the write path, where a prefix check is a
// free early refusal, here a prefix that passes proves nothing and a prefix
// that fails would refuse a legitimately moved issue the operator can read.
// Only where the issue is now decides.
//
// The check runs before the issue is fetched, so the content of a denied issue
// never enters this process, let alone an error message. It costs one round
// trip, and only when an allowlist is in force.
func (m module) authorizeRead(ctx context.Context, key string) error {
	if !m.cfg.RestrictsReadProjects() {
		return nil
	}
	current, err := m.currentProjectOf(ctx, key)
	if err != nil {
		// Fails closed: an issue whose project cannot be resolved is not
		// readable under an allowlist, because there is nothing to check.
		return err
	}
	if !m.cfg.AllowReadProject(current) {
		return deniedRead("issue " + key)
	}
	return nil
}
