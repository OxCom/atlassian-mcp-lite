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

// Bounds for the two free-form bodies this tool accepts. maxSummaryLen is
// Jira's own limit for the summary field, so a longer value is a guaranteed
// 400; maxBodyLen is this server's, and exists so that no caller-controlled
// string reaches the wire unbounded.
const (
	maxSummaryLen = 255
	maxBodyLen    = 32768
)

func (m module) updateDecl() core.ToolDecl {
	return core.ToolDecl{
		Name: "jira_update",
		// Spans two classes: the additive fields are write; summary and
		// description are destructive. Declaring both means the tool still
		// registers when only one class is enabled, carrying just that
		// class's properties.
		Actions: []core.Action{core.ActionWrite, core.ActionDestructive},
		Description: "Update fields on a Jira issue. Bodies are written in markdown. " +
			"Which fields are accepted depends on the server's enabled capabilities. " +
			"Omitted fields are left unchanged, and an empty string counts as omitted: " +
			"this tool cannot clear a field, unassign an issue, or remove a version.",
		Schema: func(c core.Caps) *jsonschema.Schema {
			// Every identifier is a string. The SDK re-marshals arguments
			// through map[string]any, so a JSON number would lose precision
			// above 2^53 and silently change the id it names.
			props := map[string]*jsonschema.Schema{
				fieldKey: {Type: typeString, Description: "Issue key, e.g. PROJ-123."},
			}
			// Additive, reversible fields.
			if c.Write {
				props["assignee"] = &jsonschema.Schema{Type: typeString,
					Description: "Assignee email address, or an exact display name."}
				props["fixVersion"] = &jsonschema.Schema{Type: typeString,
					Description: "Fix version name, resolved to its id and ADDED to the issue's existing versions."}
				props["epic"] = &jsonschema.Schema{Type: typeString,
					Description: "Epic issue key to link this issue to. Company-managed projects only; team-managed projects use parent."}
				props["parent"] = &jsonschema.Schema{Type: typeString,
					Description: "Parent issue key."}
			}
			// Overwrites that are hard to recover.
			if c.Destructive {
				props["summary"] = &jsonschema.Schema{Type: typeString,
					Description: "Replaces the summary."}
				props["description"] = &jsonschema.Schema{Type: typeString,
					Description: "Replaces the description. Markdown."}
			}
			return core.ObjectSchema(props, []string{fieldKey})
		},
		Handle: m.handleUpdate,
	}
}

type updateArgs struct {
	Key         string `json:"key"`
	Assignee    string `json:"assignee"`
	FixVersion  string `json:"fixVersion"`
	Epic        string `json:"epic"`
	Parent      string `json:"parent"`
	Summary     string `json:"summary"`
	Description string `json:"description"`
}

func (m module) handleUpdate(ctx context.Context, raw json.RawMessage) (any, error) {
	if m.client == nil {
		return nil, fmt.Errorf("jira_update: module has no client; construct it with NewWith")
	}
	var in updateArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("jira_update: %w", err)
	}
	key, err := validKey(in.Key)
	if err != nil {
		return nil, fmt.Errorf("jira_update: %w", err)
	}

	// The schema already hides the properties a capability forbids and the SDK
	// validates against it, but the handler is where the write actually
	// happens, so it re-checks rather than trusting its caller.
	caps := m.cfg.Domains[Domain]
	if !caps.Write && (in.Assignee != "" || in.FixVersion != "" || in.Epic != "" || in.Parent != "") {
		return nil, fmt.Errorf("jira_update: assignee, fixVersion, epic and parent require the write capability for %s", Domain)
	}
	if !caps.Destructive && (in.Summary != "" || in.Description != "") {
		return nil, fmt.Errorf("jira_update: summary and description require the destructive capability for %s", Domain)
	}

	// Jira's own summary limit is 255 characters, not bytes, so an accented or
	// CJK summary well inside the limit must not be refused here.
	if err := core.BoundRunes("summary", in.Summary, maxSummaryLen); err != nil {
		return nil, fmt.Errorf("jira_update: %w", err)
	}
	if err := core.BoundBytes(fieldDescription, in.Description, maxBodyLen); err != nil {
		return nil, fmt.Errorf("jira_update: %w", err)
	}
	if err := core.BoundBytes("assignee", in.Assignee, maxNameLen); err != nil {
		return nil, fmt.Errorf("jira_update: %w", err)
	}
	if err := core.BoundBytes("fixVersion", in.FixVersion, maxNameLen); err != nil {
		return nil, fmt.Errorf("jira_update: %w", err)
	}

	// Nothing to do is caught before any lookup: an update with no fields must
	// not cost a user search.
	if in.Assignee == "" && in.FixVersion == "" && in.Epic == "" &&
		in.Parent == "" && in.Summary == "" && in.Description == "" {
		return nil, fmt.Errorf("jira_update: nothing to set; supply at least one field. " +
			"An empty string means \"leave unchanged\", so this tool cannot clear a field")
	}

	// The allowlist is checked before anything reaches the network, so a
	// refused project never even leaks the existence of its issues through a
	// lookup.
	project := projectOf(key)
	if !m.cfg.AllowProject(project) {
		return nil, fmt.Errorf("jira_update: writes to project %s are not permitted by ATLAS_WRITE_PROJECTS", project)
	}
	// The key's prefix is not proof of the issue's project. Jira keeps every
	// past key working after an issue moves, resolving it to the issue's
	// current home with no redirect a client could notice — so an allowlisted
	// old prefix would otherwise authorise a write into a project the operator
	// never allowed. Only worth a round trip when an allowlist exists at all.
	if m.cfg.RestrictsProjects() {
		current, err := m.currentProjectOf(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("jira_update: %w", err)
		}
		if !m.cfg.AllowProject(current) {
			return nil, fmt.Errorf("jira_update: issue %s now lives in project %s, which is not permitted by ATLAS_WRITE_PROJECTS", key, current)
		}
		// Versions are looked up in the project that actually holds the issue.
		project = current
	}

	fields := map[string]any{}
	// update carries the verbs Jira offers for multi-value fields. A value
	// placed in `fields` is a SET and replaces the whole array.
	update := map[string]any{}
	applied := make([]string, 0, 6)

	if in.Assignee != "" {
		id, err := m.accountIDFor(ctx, in.Assignee)
		if err != nil {
			return nil, fmt.Errorf("jira_update: assignee: %w", err)
		}
		fields["assignee"] = map[string]any{"accountId": id}
		applied = append(applied, "assignee")
	}
	if in.FixVersion != "" {
		id, err := m.versionIDFor(ctx, project, in.FixVersion)
		if err != nil {
			return nil, fmt.Errorf("jira_update: fixVersion: %w", err)
		}
		// The `add` verb, not a `fields` assignment: putting fixVersions in
		// `fields` sets the whole array and silently discards every version
		// already on the issue. That is data loss reachable with only the
		// write capability, whose contract is "additive and reversible".
		update["fixVersions"] = []any{map[string]any{"add": map[string]any{"id": id}}}
		applied = append(applied, "fixVersion")
	}
	if in.Epic != "" {
		epic, err := validKey(in.Epic)
		if err != nil {
			return nil, fmt.Errorf("jira_update: epic: %w", err)
		}
		// Classic projects use an Epic Link custom field whose id is
		// site-specific; team-managed projects use parent. The id comes from
		// configuration so it is never a shared hardcoded constant. A
		// team-managed project answers with a 400 naming the field, which the
		// error path surfaces verbatim.
		fields[m.cfg.EpicFieldID] = epic
		applied = append(applied, logicalEpic)
	}
	if in.Parent != "" {
		parent, err := validKey(in.Parent)
		if err != nil {
			return nil, fmt.Errorf("jira_update: parent: %w", err)
		}
		fields["parent"] = map[string]any{fieldKey: parent}
		applied = append(applied, "parent")
	}
	if in.Summary != "" {
		// Plain text: Jira renders the summary literally, so markdown would
		// appear as punctuation in the issue title.
		fields["summary"] = strings.TrimSpace(in.Summary)
		applied = append(applied, "summary")
	}
	if in.Description != "" {
		// v2 accepts wiki markup as a plain string; v3 would demand ADF.
		wiki := markup.ToWiki(in.Description)
		// A whitespace-only description survives the non-empty check above and
		// renders to nothing, which would erase the issue's real description
		// while reporting success. Clearing a field is not something this tool
		// offers, so this is a refusal rather than a silent write.
		if wiki == "" {
			return nil, fmt.Errorf("jira_update: description renders to nothing; this tool cannot clear a field")
		}
		fields[fieldDescription] = wiki
		applied = append(applied, fieldDescription)
	}

	body := map[string]any{}
	if len(fields) > 0 {
		body[fieldsParam] = fields
	}
	if len(update) > 0 {
		body["update"] = update
	}
	path := "/rest/api/2/issue/" + url.PathEscape(key)
	if err := m.client.Do(ctx, http.MethodPut, path, nil, body, nil); err != nil {
		return nil, err
	}
	return map[string]any{fieldKey: key, "updated": strings.Join(applied, ", ")}, nil
}

// currentProjectOf asks Jira which project an issue is in now. Any key the
// issue has ever had resolves to the same issue, so this is the only way to
// learn where a write would actually land.
func (m module) currentProjectOf(ctx context.Context, key string) (string, error) {
	q := url.Values{fieldsParam: {"project"}}
	var res struct {
		Fields struct {
			Project struct {
				Key string `json:"key"`
			} `json:"project"`
		} `json:"fields"`
	}
	if err := m.client.Do(ctx, http.MethodGet, "/rest/api/2/issue/"+url.PathEscape(key), q, nil, &res); err != nil {
		return "", err
	}
	if res.Fields.Project.Key == "" {
		return "", fmt.Errorf("issue %s returned no project, so the write allowlist cannot be checked", key)
	}
	return res.Fields.Project.Key, nil
}

// boundedField refuses a free-form string longer than the limit, naming the
// field so the caller knows which one to shorten.
// boundedField refuses a free-form string longer than the limit, naming the
// field so the caller knows which one to shorten. Bytes, because this is a
// guard against an unbounded payload rather than a product rule.
// boundedRunes is the same guard for a limit Atlassian states in characters.
// Measuring those in bytes rejects a perfectly valid accented or CJK value at
// well under the real limit.
