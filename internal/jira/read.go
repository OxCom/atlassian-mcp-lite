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
		Description: "Search Jira issues with JQL. Returns a compact field set by default." + descThirdParty,
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

	// The read allowlist is applied to the caller's JQL after its own length
	// bound, not before: the bound is on what the caller may send, and the
	// clause this server adds is not the caller's to be charged for.
	jql, err := applyReadProjectFilter(in.JQL, m.cfg.ReadProjects)
	if err != nil {
		return nil, fmt.Errorf("jira_search: %w", err)
	}

	// The injected clause is not the only check. JQL's `project` field resolves
	// a value by key, by name OR by id, so `project IN ("DEV")` also matches a
	// project whose *name* is DEV whatever its key — and the allowlist is a
	// list of keys. Every returned issue is therefore re-checked against the
	// project it reports, exactly as jira_get checks the one it fetched, so the
	// clause narrows the query and the check decides what may be shown.
	upstream := m.toUpstreamFields(fields)
	injectedProject := false
	if m.cfg.RestrictsReadProjects() && !fieldsInclude(fields, fieldProject) {
		upstream = append(upstream, fieldProject)
		injectedProject = true
	}

	// JQL travels as a JSON body value, never concatenated into a URL.
	body := map[string]any{
		"jql":        jql,
		"maxResults": limit,
		fieldsParam:  upstream,
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
		// Dropped rather than refused: one issue the allowlist does not cover
		// must not deny the caller the rest of a legitimate result, and a
		// refusal naming it would disclose the very issue being withheld. The
		// decision is made before flatten, so a denied issue's content is
		// never converted, let alone returned.
		if err := m.authorizeFetchedIssue(i.Key, i.Fields); err != nil {
			continue
		}
		if injectedProject {
			// Fetched for the allowlist check alone, not part of the answer.
			delete(i.Fields, fieldProject)
		}
		// v3: unknown text fields arrive as ADF, so string leaves get SafeText
		// rather than a wiki conversion.
		issues = append(issues, m.flatten(i.Key, i.Fields, false))
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
		Description: "Get one Jira issue. The description is returned as markdown." + descThirdParty,
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
	// Under a read allowlist the issue's project is fetched in the SAME request
	// as its content, and the authorization decision is made on that one
	// response. Two requests — check the project, then fetch the issue — would
	// leave a window in which an issue moves from a permitted project into a
	// forbidden one between them and is served anyway. One response cannot
	// disagree with itself.
	upstream := m.toUpstreamFields(fields)
	injectedProject := false
	if m.cfg.RestrictsReadProjects() && !fieldsInclude(fields, fieldProject) {
		upstream = append(upstream, fieldProject)
		injectedProject = true
	}

	// v2 returns rich-text fields as wiki markup, which markup.FromWiki turns
	// into markdown. v3 would return ADF, which we deliberately do not parse.
	q := url.Values{fieldsParam: {strings.Join(upstream, ",")}}
	var res struct {
		Key    string                     `json:"key"`
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := m.client.Do(ctx, http.MethodGet, "/rest/api/2/issue/"+url.PathEscape(key), q, nil, &res); err != nil {
		return nil, err
	}
	// Decided before any of the response is flattened, converted or returned,
	// so a denied issue contributes nothing to what the caller sees.
	if err := m.authorizeFetchedIssue(key, res.Fields); err != nil {
		return nil, fmt.Errorf("jira_get: %w", err)
	}
	if injectedProject {
		// The caller did not ask for the project; it was fetched for the
		// allowlist check alone and is not part of the answer.
		delete(res.Fields, fieldProject)
	}

	// The validated key is preferred over the one in the body: it is
	// canonicalised and always present, whereas a response omitting `key` would
	// otherwise produce an issue with an empty key.
	responseKey := res.Key
	if responseKey == "" {
		responseKey = key
	}
	// v2: unknown text fields arrive as wiki markup, so string leaves go
	// through FromWiki, which is also what vets their link targets.
	out := m.flatten(responseKey, res.Fields, true)
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
//   - present and not understood — the value Jira sent, decoded and run through
//     scrubPassthrough rather than handed over verbatim, so an unexpected shape
//     cannot become the way around the reducers that drop embedded email
//     addresses. A shape this code cannot read is still data, and null would
//     assert the field is empty.
//
// text is how a string this code does not recognise is treated, and it differs
// by endpoint. On the v2 path (jira_get) an unknown text field holds wiki
// markup, so it goes through markup.FromWiki, which is also what vets its link
// targets — without the conversion a "javascript:" destination planted in a
// custom field reached the model verbatim, which is exactly what
// safeLinkTarget exists to prevent for the fields this code does know. On the
// v3 path (jira_search) the same field arrives as ADF, a format this project
// deliberately does not parse, so its string leaves get markup.SafeText: no
// conversion, but control characters gone and markdown structure disarmed.
func (m module) flatten(key string, fields map[string]json.RawMessage, wiki bool) map[string]any {
	out := map[string]any{fieldKey: key}
	text := markup.SafeText
	if wiki {
		text = markup.FromWiki
	}
	// Under a read allowlist, anything this walk finds that describes a
	// *different* issue is reduced to its key; see scrubLinkedIssues.
	restricted := m.cfg.RestrictsReadProjects()
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
		case fieldSummary:
			// Plain text, not markup: Jira renders a summary literally, and
			// jira_update writes one back literally, so a converter would be
			// both wrong and lossy. SafeText is the scalar treatment.
			var s string
			if json.Unmarshal(raw, &s) == nil {
				v, ok = markup.SafeText(s), true
			}
		case "comment":
			v, ok = commentValue(raw)
		case "parent":
			v, ok = parentValue(raw, m.cfg.RestrictsReadProjects())
		case fieldStatus, "issuetype", "priority", "resolution":
			v, ok = namedValue(raw)
		case "assignee", "reporter", "creator":
			v, ok = personValue(raw)
		case "fixVersions", "components", "versions":
			v, ok = namedList(raw)
		default:
			var g any
			if json.Unmarshal(raw, &g) == nil {
				v, ok = convertText(scrubLinkedIssues(scrubPassthrough(g), restricted), text), true
			}
		}
		if !ok {
			// A known field in a shape the reducer could not read is still
			// passed through, but as decoded, scrubbed and converted JSON: the
			// shape was unexpected, which is no reason to let an embedded user
			// through with the email the reducer would have dropped, nor to let
			// an unconverted string through with the link syntax the converter
			// would have vetted.
			var g any
			if json.Unmarshal(raw, &g) == nil {
				out[name] = convertText(scrubLinkedIssues(scrubPassthrough(g), restricted), text)
			} else {
				out[name] = raw
			}
			continue
		}
		out[name] = v
	}
	return out
}

// scrubPassthrough removes personal data from a field this code does not
// understand and would otherwise hand over verbatim.
//
// The known-field reducers (personValue and friends) return only a display
// name and account id. But Jira embeds a full user object — email address,
// avatar URLs, a self link — wherever a person appears: comment authors,
// worklog authors, watchers, custom user pickers, and whatever "*all" brings
// back. Passing those fields through raw would make the passthrough the way
// around the reducers. So every map loses its emailAddress, and every map
// loses avatarUrls and self, which are never useful
// to a model and point back at the site's own user and object endpoints. The
// walk mutates in place; the value was decoded for this call alone.
//
// The address is removed from every map, not only from one that also carries an
// accountId. Keying the removal on the id made the scrub depend on Jira's user
// object keeping its present shape, and an email address is never something the
// model needs from a field this code does not understand — so the shape is not
// consulted at all.
func scrubPassthrough(v any) any {
	switch t := v.(type) {
	case map[string]any:
		delete(t, "avatarUrls")
		delete(t, "self")
		delete(t, "emailAddress")
		for k, child := range t {
			t[k] = scrubPassthrough(child)
		}
	case []any:
		for i, child := range t {
			t[i] = scrubPassthrough(child)
		}
	}
	return v
}

// scrubLinkedIssues drops the embedded field block of every *other* issue a
// result mentions, when a read allowlist is in force.
//
// `parent`, `subtasks`, `issuelinks` and whatever `*all` brings back carry
// nested issue objects of the form {"key": "SECRET-1", "fields": {"summary":
// …}}. The allowlist decides which project's issues may be read, and the issue
// the caller asked for passing that test says nothing about the issues it links
// to: a permitted issue may be the subtask of a forbidden epic, or linked to
// one. Without this, `jira_get` with `+issuelinks` or `*all` returned the
// summaries of issues in projects the operator excluded.
//
// The key survives. It identifies rather than describes, the caller usually
// needs it to ask for that issue through jira_get — where the allowlist gets
// its own say — and dropping the whole object would leave the model unable to
// tell a hidden link from no link.
//
// A no-op when unrestricted: the walk is skipped entirely rather than running
// and changing nothing.
func scrubLinkedIssues(v any, restricted bool) any {
	if !restricted {
		return v
	}
	switch t := v.(type) {
	case map[string]any:
		// Both keys together are what identifies an embedded issue. The
		// flattened top-level result has a key and no "fields" member, so it
		// is not touched by this test.
		if _, hasKey := t[fieldKey]; hasKey {
			delete(t, fieldsParam)
		}
		for k, child := range t {
			t[k] = scrubLinkedIssues(child, restricted)
		}
	case []any:
		for i, child := range t {
			t[i] = scrubLinkedIssues(child, restricted)
		}
	}
	return v
}

// convertText applies text to every string leaf of a decoded JSON value. It is
// the companion of scrubPassthrough: that one decides what a field this code
// does not understand may still carry, this one decides what shape its text
// reaches the model in. Neither is a substitute for the other — a scrubbed
// custom field still held raw wiki markup whose links had never been vetted.
//
// The walk mutates maps and slices in place and returns the value, so a string
// leaf can be replaced by its converted form; the value was decoded for this
// call alone.
func convertText(v any, text func(string) string) any {
	switch t := v.(type) {
	case string:
		return text(t)
	case map[string]any:
		for k, child := range t {
			t[k] = convertText(child, text)
		}
	case []any:
		for i, child := range t {
			t[i] = convertText(child, text)
		}
	}
	return v
}

// commentValue reduces Jira's comment container. A comment body is wiki markup
// written by anyone who can comment on the issue — as untrusted as the
// description — so it goes through FromWiki for the same reason: without the
// conversion the body reached the model as raw wiki text whose links had never
// been past safeLinkTarget, and a "javascript:" target planted in a comment
// was copied through verbatim.
//
// An unexpected shape is passed through scrubbed rather than refused: the
// container is still useful, and the caller's fallback would only repeat the
// scrub.
func commentValue(raw json.RawMessage) (any, bool) {
	var g any
	if json.Unmarshal(raw, &g) != nil {
		return nil, false
	}
	container, isMap := scrubPassthrough(g).(map[string]any)
	if !isMap {
		return nil, false
	}
	list, isList := container["comments"].([]any)
	if !isList {
		return convertText(container, markup.SafeText), true
	}
	// Two treatments in one container: a body is wiki markup and goes through
	// FromWiki, while every other string here — an author's display name, a
	// timestamp, a visibility role — is a plain scalar and goes through
	// SafeText. The bodies are lifted out before the scalar walk and put back
	// after it, because running SafeText over a wiki body first would escape
	// the very syntax FromWiki has to read.
	bodies := make(map[int]string, len(list))
	for i, item := range list {
		c, isMap := item.(map[string]any)
		if !isMap {
			continue
		}
		if body, isText := c["body"].(string); isText {
			bodies[i] = body
			delete(c, "body")
		}
	}
	convertText(container, markup.SafeText)
	for i, body := range bodies {
		if c, isMap := list[i].(map[string]any); isMap {
			c["body"] = markup.FromWiki(body)
		}
	}
	return container, true
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
	// A name is third-party text: a status, issue type, priority or resolution
	// is named by whoever administers the site. It is a plain scalar and a
	// round-trip input — jira_transition takes a status name — so SafeText is
	// the treatment, not a converter and not a blanket escape.
	name := markup.SafeText(v.Name)
	switch {
	case v.Key != "" && name != "":
		return v.Key + " (" + name + ")", true
	case name != "":
		return name, true
	case v.Key != "":
		return v.Key, true
	}
	return nil, false
}

// parentValue renders an issue's parent. Jira's parent object carries no name:
// the key is in `key` and the human-readable half is in `fields.summary`, so
// treating it like any other named object drops the summary and forces a second
// call to learn what the parent is.
func parentValue(raw json.RawMessage, restricted bool) (any, bool) {
	var v struct {
		Key    string `json:"key"`
		Fields struct {
			Summary string `json:"summary"`
		} `json:"fields"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return nil, false
	}
	// A parent is a different issue, and a parent in a hierarchy above an
	// allowed project may itself live outside the read allowlist. Its key
	// stays — the caller needs it to ask for the parent through jira_get,
	// which will make its own decision — but its summary is content of an
	// issue this deployment has not authorised reading, so under a read
	// allowlist only the key is returned.
	summary := markup.SafeText(v.Fields.Summary)
	if restricted {
		summary = ""
	}
	switch {
	case v.Key != "" && summary != "":
		return v.Key + " (" + summary + ")", true
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
	// A display name is chosen by the person it names, so it is third-party
	// text like any other. The account id is a site-generated identifier and
	// needs no treatment.
	switch {
	case v.DisplayName != "":
		return markup.SafeText(v.DisplayName), true
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
			out = append(out, markup.SafeText(v.Name))
		}
	}
	return out, true
}
