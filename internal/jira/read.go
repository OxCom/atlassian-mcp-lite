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

// searchDefaults is the triage set: what an issue is, where it stands, when it
// last moved and who owns and raised it. The description is left out because it
// dominates the payload. Anything else is one "+name" away, and "*all" returns
// every field Jira has.
//
// This list is documented in docs/configuration.md; change both together.
var searchDefaults = []string{
	"summary", fieldStatus, "updated", "assignee", "reporter",
}

// logicalEpic is the name callers use for the Epic Link field. Jira has no
// field called epic — classic projects store the link in a site-specific custom
// field — so every field list is translated before the request and translated
// back in the response.

const logicalEpic = "epic"

// toUpstreamFields translates logical field names to what Jira expects.
func (m module) toUpstreamFields(fields []string) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if strings.EqualFold(f, logicalEpic) {
			out = append(out, m.cfg.EpicFieldID)
			continue
		}
		out = append(out, f)
	}
	return out
}

// getDefaults adds the description, which is normally wanted for a single issue.
var getDefaults = append(append([]string{}, searchDefaults...), "description")

func fieldsProperty() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "array",
		Items:       &jsonschema.Schema{Type: typeString},
		Description: `Fields to return. Omit for the default set (summary, status, updated, assignee, reporter; jira_get adds description). Bare names replace the default set entirely; "+name" adds to it; "-name" removes from it; "*all" returns every field. Do not mix bare and prefixed names.`,
	}
}

func (m module) searchDecl() core.ToolDecl {
	return core.ToolDecl{
		Name:        "jira_search",
		Actions:     []core.Action{core.ActionRead},
		Description: "Search Jira issues with JQL. Returns a compact field set by default.",
		Schema: func(core.Caps) *jsonschema.Schema {
			return core.ObjectSchema(map[string]*jsonschema.Schema{
				"jql":       {Type: typeString, Description: "A JQL query."},
				fieldsParam: fieldsProperty(),
				"limit":     {Type: "integer", Description: "Maximum issues to return."},
			}, []string{"jql"})
		},
		Handle: m.handleSearch,
	}
}

type searchArgs struct {
	JQL    string   `json:"jql"`
	Fields []string `json:"fields"`
	Limit  int      `json:"limit"`
}

func (m module) handleSearch(ctx context.Context, raw json.RawMessage) (any, error) {
	if m.client == nil {
		return nil, fmt.Errorf("jira_search: module has no client; construct it with NewWith")
	}
	var in searchArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("jira_search: %w", err)
	}
	if strings.TrimSpace(in.JQL) == "" {
		return nil, fmt.Errorf("jira_search: jql is required")
	}
	if len(in.JQL) > maxJQLLen {
		return nil, fmt.Errorf("jira_search: jql is %d bytes, limit is %d", len(in.JQL), maxJQLLen)
	}
	fields, err := core.ResolveFields(searchDefaults, in.Fields)
	if err != nil {
		return nil, fmt.Errorf("jira_search: %w", err)
	}
	// Search runs on v3, which returns rich text as ADF — a format this project
	// deliberately does not parse. Asking for description here would return a
	// raw ADF blob, while jira_get returns the same field as markdown from v2.
	// Refusing is better than shipping two different formats for one field.
	if bad := richTextRequested(fields); bad != "" {
		return nil, fmt.Errorf("jira_search: field %q is not available here because search returns it in a format this server does not convert; use jira_get for it", bad)
	}
	limit := m.cfg.ClampLimit(in.Limit)

	// JQL travels as a JSON body value, never concatenated into a URL.
	body := map[string]any{
		"jql":        in.JQL,
		"maxResults": limit,
		fieldsParam:  m.toUpstreamFields(fields),
	}

	var res struct {
		Issues []struct {
			Key    string                     `json:"key"`
			Fields map[string]json.RawMessage `json:"fields"`
		} `json:"issues"`
		// isLast and nextPageToken are the API's own completion signals.
		// Counting returned issues cannot distinguish a complete result set of
		// exactly maxResults from a truncated one.
		IsLast        *bool  `json:"isLast"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := m.client.Do(ctx, http.MethodPost, "/rest/api/3/search/jql", nil, body, &res); err != nil {
		return nil, err
	}

	issues := make([]map[string]any, 0, len(res.Issues))
	for _, i := range res.Issues {
		issues = append(issues, m.flatten(i.Key, i.Fields))
	}

	out := map[string]any{"issues": issues, "returned": len(issues)}
	if missing := unavailableFields(fields, issues); len(missing) > 0 {
		out["unavailable_fields"] = missing
	}
	if m.moreAvailable(res.IsLast, res.NextPageToken, len(issues), limit) {
		out["truncated"] = fmt.Sprintf(
			"more results exist beyond limit %d; narrow the JQL or raise limit (max %d). paging is not supported",
			limit, m.cfg.LimitMax)
	}
	return out, nil
}

// richTextRequested returns the first rich-text field in the list, or "".
func richTextRequested(fields []string) string {
	for _, f := range fields {
		if f == fieldDescription || f == fieldEnvironment {
			return f
		}
	}
	return ""
}

// moreAvailable decides whether results were cut short. The API's explicit
// signals win; the count heuristic is only a fallback for responses that carry
// neither, and a full page with no other signal is reported as possibly
// truncated rather than silently complete.
func (m module) moreAvailable(isLast *bool, nextPageToken string, returned, limit int) bool {
	if nextPageToken != "" {
		return true
	}
	if isLast != nil {
		return !*isLast
	}
	return returned >= limit
}

func (m module) getDecl() core.ToolDecl {
	return core.ToolDecl{
		Name:        "jira_get",
		Actions:     []core.Action{core.ActionRead},
		Description: "Get one Jira issue. The description is returned as markdown.",
		Schema: func(core.Caps) *jsonschema.Schema {
			return core.ObjectSchema(map[string]*jsonschema.Schema{
				fieldKey:    {Type: typeString, Description: descIssueKey},
				fieldsParam: fieldsProperty(),
			}, []string{fieldKey})
		},
		Handle: m.handleGet,
	}
}

type getArgs struct {
	Key    string   `json:"key"`
	Fields []string `json:"fields"`
}

func (m module) handleGet(ctx context.Context, raw json.RawMessage) (any, error) {
	if m.client == nil {
		return nil, fmt.Errorf("jira_get: module has no client; construct it with NewWith")
	}
	var in getArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("jira_get: %w", err)
	}
	key, err := validKey(in.Key)
	if err != nil {
		return nil, fmt.Errorf("jira_get: %w", err)
	}
	fields, err := core.ResolveFields(getDefaults, in.Fields)
	if err != nil {
		return nil, fmt.Errorf("jira_get: %w", err)
	}

	// v2 returns rich-text fields as wiki markup, which markup.FromWiki turns
	// into markdown. v3 would return ADF, which we deliberately do not parse.
	q := url.Values{fieldsParam: {strings.Join(m.toUpstreamFields(fields), ",")}}
	var res struct {
		Key    string                     `json:"key"`
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := m.client.Do(ctx, http.MethodGet, "/rest/api/2/issue/"+url.PathEscape(key), q, nil, &res); err != nil {
		return nil, err
	}

	// The validated key is preferred over the one in the body: it is
	// canonicalised and always present, whereas a response omitting `key` would
	// otherwise produce an issue with an empty key.
	responseKey := res.Key
	if responseKey == "" {
		responseKey = key
	}
	out := m.flatten(responseKey, res.Fields)
	if missing := unavailableFields(fields, []map[string]any{out}); len(missing) > 0 {
		out["unavailable_fields"] = missing
	}
	return out, nil
}

// flatten reduces Jira's nested field objects to scalars a model can read,
// converts wiki-markup bodies to markdown, and renames the site-specific Epic
// Link custom field back to the logical name callers used.
// flatten reduces Jira's nested field objects to scalars a model can read,
// converts wiki-markup bodies to markdown, and renames the site-specific Epic
// Link custom field back to the logical name callers used.
//
// Three states are kept distinguishable, because collapsing any two of them
// misleads the reader:
//
//   - present and null — the field exists and has no value. Emitted as null.
//     Skipping it would make "unassigned" look identical to a field Jira never
//     sent, which is what an unknown field name produces.
//   - present and understood — the flattened scalar.
//   - present and not understood — the raw JSON, passed through. A shape this
//     code cannot read is still data, and null would assert the field is empty.
func (m module) flatten(key string, fields map[string]json.RawMessage) map[string]any {
	out := map[string]any{fieldKey: key}
	for name, raw := range fields {
		// The rename happens first so every later decision, including the null
		// case, keys the value by the name the caller actually asked for.
		if name == m.cfg.EpicFieldID {
			name = logicalEpic
		}
		if len(raw) == 0 || string(raw) == "null" {
			out[name] = nil
			continue
		}

		var (
			v  any
			ok bool
		)
		switch name {
		case fieldDescription, fieldEnvironment:
			var s string
			if json.Unmarshal(raw, &s) == nil {
				v, ok = markup.FromWiki(s), true
			}
		case "parent":
			v, ok = parentValue(raw)
		case fieldStatus, "issuetype", "priority", "resolution":
			v, ok = namedValue(raw)
		case "assignee", "reporter", "creator":
			v, ok = personValue(raw)
		case "fixVersions", "components", "versions":
			v, ok = namedList(raw)
		default:
			var g any
			if json.Unmarshal(raw, &g) == nil {
				v, ok = g, true
			}
		}
		if !ok {
			out[name] = raw
			continue
		}
		out[name] = v
	}
	return out
}

// unavailableFields lists the field names the caller asked for that Jira did
// not return for any issue. Jira Cloud silently ignores a field name it does
// not know or the caller cannot see, so without this the result is quietly
// short and the caller cannot tell which name was wrong.
func unavailableFields(requested []string, issues []map[string]any) []string {
	if len(issues) == 0 {
		return nil
	}
	var missing []string
	for _, f := range requested {
		// "*all" and "*navigable" expand server-side and never come back under
		// their own name, so they cannot be missing.
		if core.IsStarSelector(f) {
			continue
		}
		found := false
		for _, issue := range issues {
			if _, ok := issue[f]; ok {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, f)
		}
	}
	return missing
}

// namedValue renders an object identified by name, key, or both. The bool
// reports whether the shape was understood, so the caller can pass the raw
// value through rather than asserting the field is empty.
func namedValue(raw json.RawMessage) (any, bool) {
	var v struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return nil, false
	}
	switch {
	case v.Key != "" && v.Name != "":
		return v.Key + " (" + v.Name + ")", true
	case v.Name != "":
		return v.Name, true
	case v.Key != "":
		return v.Key, true
	}
	return nil, false
}

// parentValue renders an issue's parent. Jira's parent object carries no name:
// the key is in `key` and the human-readable half is in `fields.summary`, so
// treating it like any other named object drops the summary and forces a second
// call to learn what the parent is.
func parentValue(raw json.RawMessage) (any, bool) {
	var v struct {
		Key    string `json:"key"`
		Fields struct {
			Summary string `json:"summary"`
		} `json:"fields"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return nil, false
	}
	switch {
	case v.Key != "" && v.Fields.Summary != "":
		return v.Key + " (" + v.Fields.Summary + ")", true
	case v.Key != "":
		return v.Key, true
	}
	return nil, false
}

// personValue renders a user. displayName is absent whenever the site's
// privacy settings hide it, which is common, so the account id is the fallback
// rather than nothing at all — an issue that is assigned must never read as
// unassigned.
func personValue(raw json.RawMessage) (any, bool) {
	var v struct {
		DisplayName string `json:"displayName"`
		AccountID   string `json:"accountId"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return nil, false
	}
	switch {
	case v.DisplayName != "":
		return v.DisplayName, true
	case v.AccountID != "":
		return v.AccountID, true
	}
	return nil, false
}

func namedList(raw json.RawMessage) (any, bool) {
	var vs []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &vs) != nil {
		return nil, false
	}
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		if v.Name != "" {
			out = append(out, v.Name)
		}
	}
	return out, true
}
