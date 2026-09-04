package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
	"github.com/OxCom/atlassian-mcp-lite/internal/markup"
)

// fieldBody is the comment-body property name, in the schema and in the
// request body alike.
const fieldBody = "body"

func (m module) commentDecl() core.ToolDecl {
	return core.ToolDecl{
		Name:        "jira_comment",
		Actions:     []core.Action{core.ActionWrite},
		Description: "Add a comment to a Jira issue. The body is written in markdown." + descNotAuthorized,
		Schema: func(core.Caps) *jsonschema.Schema {
			return core.ObjectSchema(map[string]*jsonschema.Schema{
				fieldKey:  {Type: typeString, Description: descIssueKey},
				fieldBody: {Type: typeString, Description: "Comment body. Markdown."},
			}, []string{fieldKey, fieldBody})
		},
		Handle: m.handleComment,
	}
}

type commentArgs struct {
	Key  string `json:"key"`
	Body string `json:"body"`
}

func (m module) handleComment(ctx context.Context, raw json.RawMessage) (any, error) {
	if m.client == nil {
		return nil, fmt.Errorf("jira_comment: module has no client; construct it with NewWith")
	}
	var in commentArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("jira_comment: %w", err)
	}
	key, err := validKey(in.Key)
	if err != nil {
		return nil, fmt.Errorf("jira_comment: %w", err)
	}
	// The registry already withholds a tool whose class is disabled, and the
	// SDK validates arguments against the schema — but the handler is where the
	// write actually happens, so it re-checks rather than trusting its caller.
	if !m.cfg.Domains[Domain].Write {
		return nil, fmt.Errorf("jira_comment: commenting requires the write capability for %s", Domain)
	}
	// Bytes, because the point here is to keep an unbounded payload off the
	// wire rather than to mirror a limit Atlassian states in characters.
	if err := core.BoundBytes(fieldBody, in.Body, maxBodyLen); err != nil {
		return nil, fmt.Errorf("jira_comment: %w", err)
	}
	if strings.TrimSpace(in.Body) == "" {
		return nil, fmt.Errorf("jira_comment: body is empty")
	}
	// Markdown that is syntax only — an HTML comment, a stray marker — renders
	// to nothing. Posting that would leave an empty comment on the issue, which
	// is not what the caller asked for and cannot be told apart from a bug.
	wiki := markup.ToWiki(in.Body)
	if wiki == "" {
		return nil, fmt.Errorf("jira_comment: body renders to nothing; supply text, not markup alone")
	}

	if err := m.authorizeWrite(ctx, key); err != nil {
		return nil, fmt.Errorf("jira_comment: %w", err)
	}

	// v2 accepts wiki markup as a plain string; v3 would demand ADF.
	body := map[string]any{fieldBody: wiki}
	var res struct {
		ID string `json:"id"`
	}
	path := "/rest/api/2/issue/" + url.PathEscape(key) + "/comment"
	if err := m.client.Do(ctx, http.MethodPost, path, nil, body, &res); err != nil {
		return nil, err
	}
	return map[string]any{fieldKey: key, "commentId": res.ID}, nil
}

func (m module) transitionDecl() core.ToolDecl {
	return core.ToolDecl{
		Name: "jira_transition",
		// Destructive, not write: a workflow move is not trivially reversible.
		// A transition can be one-way, and it fires notifications, automation
		// rules and post-functions that no later transition undoes.
		Actions: []core.Action{core.ActionDestructive},
		Description: "Move a Jira issue to another status. Destructive: a workflow move can be " +
			"one-way and can trigger notifications and automation." + descNotAuthorized,
		Schema: func(core.Caps) *jsonschema.Schema {
			return core.ObjectSchema(map[string]*jsonschema.Schema{
				fieldKey: {Type: typeString, Description: descIssueKey},
				// A transition id is a string even though it looks numeric: the
				// SDK re-marshals arguments through map[string]any, where a
				// JSON number loses precision above 2^53.
				fieldStatus: {Type: typeString,
					Description: "Target status name, the transition's own name, or a transition id."},
			}, []string{fieldKey, fieldStatus})
		},
		Handle: m.handleTransition,
	}
}

type transitionArgs struct {
	Key    string `json:"key"`
	Status string `json:"status"`
}

func (m module) handleTransition(ctx context.Context, raw json.RawMessage) (any, error) {
	if m.client == nil {
		return nil, fmt.Errorf("jira_transition: module has no client; construct it with NewWith")
	}
	var in transitionArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("jira_transition: %w", err)
	}
	key, err := validKey(in.Key)
	if err != nil {
		return nil, fmt.Errorf("jira_transition: %w", err)
	}
	if !m.cfg.Domains[Domain].Destructive {
		return nil, fmt.Errorf("jira_transition: a workflow move requires the destructive capability for %s", Domain)
	}
	if err := core.BoundBytes(fieldStatus, in.Status, maxNameLen); err != nil {
		return nil, fmt.Errorf("jira_transition: %w", err)
	}
	want := strings.TrimSpace(in.Status)
	if want == "" {
		return nil, fmt.Errorf("jira_transition: status is empty")
	}

	if err := m.authorizeWrite(ctx, key); err != nil {
		return nil, fmt.Errorf("jira_transition: %w", err)
	}

	// Transition ids are workflow-specific and differ per project and per
	// current status, so they are always looked up and never assumed. The same
	// path serves the lookup and the move.
	path := "/rest/api/3/issue/" + url.PathEscape(key) + "/transitions"
	var list struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	if err := m.client.Do(ctx, http.MethodGet, path, nil, nil, &list); err != nil {
		return nil, err
	}
	if len(list.Transitions) == 0 {
		return nil, fmt.Errorf("jira_transition: %s has no transitions available from its current status", key)
	}

	// An id, a transition name and a target status name are all accepted, but
	// they are NOT tried in priority order. Ranked tiers look reasonable and
	// are wrong here: a workflow may hold a transition *named* "Done" that
	// moves an issue to Closed alongside one that targets the Done status, and
	// a name-beats-target rule would send "Done" to Closed without a word.
	// Measured, on exactly that workflow, before this was rewritten.
	//
	// So every category is collected, matches are deduplicated by transition,
	// and the move happens only when the whole set names one transition.
	// Anything else is an ambiguity the caller has to settle with an id,
	// because a transition fires automation, notifies people, and is not
	// always reversible.
	matched := map[int]bool{}
	available := make([]string, 0, len(list.Transitions))
	for i, tr := range list.Transitions {
		// Names are quoted and bounded before they reach an error: they are
		// workflow data chosen by someone else, not part of the message.
		available = append(available, fmt.Sprintf("%q -> %q (id %s)",
			core.TruncateRunes(tr.Name, maxCandidateRunes), core.TruncateRunes(tr.To.Name, maxCandidateRunes), tr.ID))
		if tr.ID == want || strings.EqualFold(tr.Name, want) || strings.EqualFold(tr.To.Name, want) {
			matched[i] = true
		}
	}

	if len(matched) != 1 {
		// The list is withheld when read is off; see candidateSuffix. It is
		// also withheld when the read allowlist does not cover this issue: the
		// transitions are metadata about an issue the operator has said this
		// deployment may not read, and a write permission is not a read
		// permission. Resolved only on the error paths, so a successful move
		// never pays for the round trip.
		shown := available
		if err := m.authorizeRead(ctx, key); err != nil {
			shown = nil
		}
		if shown == nil {
			// The count is workflow metadata too: how many transitions a
			// status offers describes the workflow of a project this
			// deployment may not read. Withheld with the names.
			if len(matched) == 0 {
				return nil, fmt.Errorf("jira_transition: %s has no transition to %q; use an exact transition name", key, want)
			}
			return nil, fmt.Errorf("jira_transition: %q does not identify exactly one transition on %s; use the transition id", want, key)
		}
		if len(matched) == 0 {
			return nil, fmt.Errorf("jira_transition: %s has no transition to %q (%d available%s); use an exact transition name",
				key, want, len(available), m.candidateSuffix(shown))
		}
		return nil, fmt.Errorf("jira_transition: %q matches %d transitions on %s%s; use the transition id",
			want, len(matched), key, m.candidateSuffix(shown))
	}
	chosen := -1
	for i := range matched {
		chosen = i
	}

	target := list.Transitions[chosen]
	body := map[string]any{"transition": map[string]any{"id": target.ID}}
	if err := m.client.Do(ctx, http.MethodPost, path, nil, body, nil); err != nil {
		return nil, err
	}
	// The status name is third-party text — a workflow administrator wrote it —
	// so it leaves through the same scalar treatment as every name a read tool
	// returns.
	return map[string]any{fieldKey: key, fieldStatus: markup.SafeText(target.To.Name), "transitionId": target.ID}, nil
}

// authorizeWrite refuses a write the operator's allowlist does not permit.
//
// The prefix of a key is not proof of its project: Jira keeps every past key
// working after an issue moves, resolving it to the issue's current home with
// no redirect a client could notice. So an allowlisted old prefix would
// otherwise authorise a write into a project the operator never allowed. The
// verification costs a round trip, so it is only made when an allowlist exists
// at all — an unrestricted deployment should not pay for a check it can never
// fail.
func (m module) authorizeWrite(ctx context.Context, key string) error {
	project := projectOf(key)
	if !m.cfg.AllowProject(project) {
		return fmt.Errorf("writes to project %s are not permitted by ATLAS_WRITE_PROJECTS", project)
	}
	if !m.cfg.RestrictsProjects() {
		return nil
	}
	current, err := m.currentProjectOf(ctx, key)
	if err != nil {
		return err
	}
	if !m.cfg.AllowProject(current) {
		return m.movedIssueRefusal("issue", key, current)
	}
	return nil
}
