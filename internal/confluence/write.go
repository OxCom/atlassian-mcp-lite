package confluence

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
	"github.com/OxCom/atlassian-mcp-lite/internal/markup"
)

// Bounds on the free-form strings a caller controls on the write paths, in the
// same spirit as maxCQLLen on the read path: every input in this repository is
// bounded, and an unbounded body is a memory and rate-limit amplifier that
// costs nothing to close.
//
// maxTitleLen is Confluence's own page-title limit, so rejecting here turns a
// server-side 400 into a local error naming the field. maxSpaceKeyLen is
// generous on purpose — the key is only ever compared, never stored — and
// maxBodyLen is far larger than any page a model should be writing in one call
// while staying well under the client's 8 MiB response cap.
const (
	maxTitleLen    = 255
	maxSpaceKeyLen = 255
	maxBodyLen     = 1 << 18 // 256 KiB
)

// bound rejects an over-long free-form string, naming the field and the limit.
// bound rejects an over-long free-form string, naming the field and the limit.
// Bytes, because this guards payload size rather than a product rule.
// boundRunes is the same guard for a limit Atlassian states in characters.
// Confluence's title limit is 255 code points, so measuring it in bytes
// invents a rejection the server would never have made — a 100-character CJK
// title is 300 bytes and perfectly valid.
// wikiBody builds a v2 body object. Every write uses the wiki representation:
// Atlassian expands it to storage format server-side, so no XHTML or ADF
// generator exists in this codebase.
func wikiBody(md string) map[string]any {
	return map[string]any{"representation": "wiki", "value": markup.ToWiki(md)}
}

func (m module) createPageDecl() core.ToolDecl {
	return core.ToolDecl{
		Name:        "confluence_create_page",
		Actions:     []core.Action{core.ActionWrite},
		Description: "Create a Confluence page. The body is written in markdown.",
		Schema: func(core.Caps) *jsonschema.Schema {
			return core.ObjectSchema(map[string]*jsonschema.Schema{
				fieldSpace:  stringProp("Space key, e.g. DOCS."),
				fieldTitle:  stringProp("Page title."),
				fieldBody:   stringProp("Page body. Markdown."),
				"parent_id": stringProp("Optional parent page id. Numeric, sent as a string."),
			}, []string{fieldSpace, fieldTitle, fieldBody})
		},
		Handle: m.handleCreatePage,
	}
}

type createPageArgs struct {
	Space    string `json:"space"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	ParentID string `json:"parent_id"`
}

func (m module) handleCreatePage(ctx context.Context, raw json.RawMessage) (any, error) {
	var in createPageArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("confluence_create_page: %w", err)
	}
	space := strings.TrimSpace(in.Space)
	if space == "" || strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Body) == "" {
		return nil, fmt.Errorf("confluence_create_page: space, title and body are all required")
	}
	for _, c := range []struct {
		field string
		value string
		limit int
	}{
		{fieldSpace, space, maxSpaceKeyLen},
		{fieldBody, in.Body, maxBodyLen},
	} {
		if err := core.BoundBytes(c.field, c.value, c.limit); err != nil {
			return nil, fmt.Errorf("confluence_create_page: %w", err)
		}
	}
	// Confluence states its title limit in characters, not bytes.
	if err := core.BoundRunes(fieldTitle, in.Title, maxTitleLen); err != nil {
		return nil, fmt.Errorf("confluence_create_page: %w", err)
	}
	// The allowlist is checked against the caller's own key before anything
	// leaves the process, and again implicitly by spaceIDFor, which accepts
	// only a space whose key equals this one ignoring case — the same folding
	// core.AllowSpace does, so the two checks agree rather than one being
	// laxer than the other.
	if !m.cfg.AllowSpace(space) {
		return nil, fmt.Errorf("confluence_create_page: writes to space %s are not permitted by ATLAS_WRITE_SPACES", space)
	}

	body := map[string]any{
		"status":   "current",
		fieldTitle: in.Title,
		fieldBody:  wikiBody(in.Body),
	}
	if in.ParentID != "" {
		pid, err := validPageID(in.ParentID)
		if err != nil {
			return nil, fmt.Errorf("confluence_create_page: parent_id: %w", err)
		}
		body["parentId"] = pid
	}

	spaceID, err := m.spaceIDFor(ctx, space)
	if err != nil {
		return nil, fmt.Errorf("confluence_create_page: %w", err)
	}
	body[fieldSpaceID] = spaceID

	var res struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := m.client.Do(ctx, http.MethodPost, "/wiki/api/v2/pages", nil, body, &res); err != nil {
		return nil, err
	}
	return map[string]any{fieldID: res.ID, fieldTitle: res.Title, fieldSpace: space}, nil
}

func (m module) updatePageDecl() core.ToolDecl {
	return core.ToolDecl{
		Name:    "confluence_update_page",
		Actions: []core.Action{core.ActionDestructive},
		Description: "Replace a Confluence page's body. Destructive: the previous body is " +
			"superseded, though Confluence keeps it in page history. Pass the version " +
			"returned by confluence_get_page to be sure nobody else edited the page in " +
			"the meantime.",
		Schema: func(core.Caps) *jsonschema.Schema {
			return core.ObjectSchema(map[string]*jsonschema.Schema{
				fieldID:    stringProp("Numeric page id, sent as a string."),
				fieldTitle: stringProp("Optional new title. Omit to keep the current one."),
				fieldBody:  stringProp("Replacement body. Markdown."),
				fieldVersion: stringProp("Optional version this update is based on — the one " +
					"confluence_get_page returned. Numeric, sent as a string. When supplied, the " +
					"update is refused if anyone else has edited the page since. Omit to overwrite " +
					"whatever is current."),
			}, []string{fieldID, fieldBody})
		},
		Handle: m.handleUpdatePage,
	}
}

type updatePageArgs struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Version string `json:"version"`
}

func (m module) handleUpdatePage(ctx context.Context, raw json.RawMessage) (any, error) {
	var in updatePageArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("confluence_update_page: %w", err)
	}
	id, err := validPageID(in.ID)
	if err != nil {
		return nil, fmt.Errorf("confluence_update_page: %w", err)
	}
	if strings.TrimSpace(in.Body) == "" {
		return nil, fmt.Errorf("confluence_update_page: body is empty; refusing to blank the page")
	}
	// Confluence states its title limit in characters, so measuring bytes here
	// would refuse a perfectly valid CJK title the server would have accepted.
	if err := core.BoundRunes(fieldTitle, in.Title, maxTitleLen); err != nil {
		return nil, fmt.Errorf("confluence_update_page: %w", err)
	}
	if err := core.BoundBytes(fieldBody, in.Body, maxBodyLen); err != nil {
		return nil, fmt.Errorf("confluence_update_page: %w", err)
	}
	var basedOn int
	if strings.TrimSpace(in.Version) != "" {
		basedOn, err = strconv.Atoi(strings.TrimSpace(in.Version))
		if err != nil || basedOn < 1 {
			return nil, fmt.Errorf("confluence_update_page: version %q is not a positive number", in.Version)
		}
	}

	// Confluence uses the version number for optimistic locking, and an
	// omitted title blanks it, so the current state is always fetched first.
	//
	// include-version is sent explicitly rather than relied on as a default,
	// for the same reason confluence_get_page sends it: the number is the
	// lock. The body is deliberately not requested — nothing here reads it,
	// and a page body is the largest thing this API returns.
	var current struct {
		Title   string `json:"title"`
		SpaceID string `json:"spaceId"`
		// A pointer, so a missing or null version is an error rather than
		// silently becoming 0 and being incremented to a 1 that would either
		// clobber a concurrent edit or fail with an opaque conflict.
		Version *struct {
			Number int `json:"number"`
		} `json:"version"`
	}
	path := "/wiki/api/v2/pages/" + url.PathEscape(id)
	q := url.Values{"include-version": {"true"}}
	if err := m.client.Do(ctx, http.MethodGet, path, q, nil, &current); err != nil {
		return nil, err
	}
	if current.Version == nil || current.Version.Number < 1 {
		return nil, fmt.Errorf("confluence_update_page: page %s returned no usable version number; refusing to write without an optimistic lock", id)
	}
	// Without a caller-supplied version the lock covers only the gap between
	// this handler's own GET and its PUT, which is microseconds — an edit made
	// between the caller READING the page and calling this tool is overwritten
	// silently. With one, the read the caller actually based its body on is
	// what gets checked.
	if basedOn != 0 && current.Version.Number != basedOn {
		return nil, fmt.Errorf(
			"confluence_update_page: page %s is at version %d, not the version %d this update was based on; someone else edited it, so re-read the page before writing",
			id, current.Version.Number, basedOn)
	}

	// The allowlist is keyed by space, and the caller named only a page, so
	// the owning space is resolved before the write. Skipped when the
	// allowlist is empty, which means unrestricted and would cost two requests
	// to confirm.
	if m.cfg.RestrictsSpaces() {
		key, err := m.spaceKeyFor(ctx, current.SpaceID)
		if err != nil {
			return nil, fmt.Errorf("confluence_update_page: %w", err)
		}
		if !m.cfg.AllowSpace(key) {
			return nil, fmt.Errorf("confluence_update_page: writes to space %s are not permitted by ATLAS_WRITE_SPACES", key)
		}
	}

	title := current.Title
	if strings.TrimSpace(in.Title) != "" {
		title = in.Title
	}

	next := current.Version.Number + 1
	body := map[string]any{
		fieldID:      id,
		"status":     "current",
		fieldTitle:   title,
		fieldBody:    wikiBody(in.Body),
		fieldVersion: map[string]any{"number": next},
	}
	var res struct {
		ID string `json:"id"`
		// A pointer for the same reason as the read above: a response without
		// a version would otherwise hand the model a confident 0.
		Version *struct {
			Number int `json:"number"`
		} `json:"version"`
	}
	if err := m.client.Do(ctx, http.MethodPut, path, nil, body, &res); err != nil {
		return nil, err
	}
	written := next
	if res.Version != nil && res.Version.Number > 0 {
		written = res.Version.Number
	}
	return map[string]any{fieldID: res.ID, fieldTitle: title, fieldVersion: written}, nil
}

func (m module) commentDecl() core.ToolDecl {
	return core.ToolDecl{
		Name:        "confluence_comment",
		Actions:     []core.Action{core.ActionWrite},
		Description: "Add a footer comment to a Confluence page. The body is written in markdown.",
		Schema: func(core.Caps) *jsonschema.Schema {
			return core.ObjectSchema(map[string]*jsonschema.Schema{
				fieldPageID: stringProp("Numeric page id, sent as a string."),
				fieldBody:   stringProp("Comment body. Markdown."),
			}, []string{fieldPageID, fieldBody})
		},
		Handle: m.handleComment,
	}
}

type commentArgs struct {
	PageID string `json:"page_id"`
	Body   string `json:"body"`
}

func (m module) handleComment(ctx context.Context, raw json.RawMessage) (any, error) {
	var in commentArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("confluence_comment: %w", err)
	}
	id, err := validPageID(in.PageID)
	if err != nil {
		return nil, fmt.Errorf("confluence_comment: %w", err)
	}
	if strings.TrimSpace(in.Body) == "" {
		return nil, fmt.Errorf("confluence_comment: body is empty")
	}
	if err := core.BoundBytes(fieldBody, in.Body, maxBodyLen); err != nil {
		return nil, fmt.Errorf("confluence_comment: %w", err)
	}

	// A comment is a write. Without this check the space allowlist would apply
	// to page creation and replacement but not to commenting, which is a hole
	// wide enough to exfiltrate through.
	if m.cfg.RestrictsSpaces() {
		key, err := m.spaceKeyForPage(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("confluence_comment: %w", err)
		}
		if !m.cfg.AllowSpace(key) {
			return nil, fmt.Errorf("confluence_comment: writes to space %s are not permitted by ATLAS_WRITE_SPACES", key)
		}
	}

	body := map[string]any{"pageId": id, fieldBody: wikiBody(in.Body)}
	var res struct {
		ID string `json:"id"`
	}
	if err := m.client.Do(ctx, http.MethodPost, "/wiki/api/v2/footer-comments", nil, body, &res); err != nil {
		return nil, err
	}
	return map[string]any{"pageId": id, "commentId": res.ID}, nil
}

// spaceIDFor resolves a space key to the numeric id the v2 API requires.
//
// The returned key is compared explicitly rather than trusting server-side
// filtering: taking results[0] on faith would let an unexpectedly broad
// response place a page in a different space than the one that passed the
// allowlist check.
func (m module) spaceIDFor(ctx context.Context, key string) (string, error) {
	var res struct {
		Results []struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"results"`
	}
	q := url.Values{"keys": {key}, "limit": {"25"}}
	if err := m.client.Do(ctx, http.MethodGet, "/wiki/api/v2/spaces", q, nil, &res); err != nil {
		return "", err
	}
	var found string
	for _, r := range res.Results {
		if !strings.EqualFold(r.Key, key) {
			continue
		}
		if found != "" && found != r.ID {
			return "", fmt.Errorf("space key %q matched more than one space", key)
		}
		found = r.ID
	}
	if found == "" {
		return "", fmt.Errorf("no space with key %q is visible to this account", key)
	}
	return found, nil
}

// spaceKeyForPage resolves the space key that owns a page, for allowlist checks.
func (m module) spaceKeyForPage(ctx context.Context, pageID string) (string, error) {
	var page struct {
		SpaceID string `json:"spaceId"`
	}
	if err := m.client.Do(ctx, http.MethodGet, "/wiki/api/v2/pages/"+url.PathEscape(pageID), nil, nil, &page); err != nil {
		return "", err
	}
	return m.spaceKeyFor(ctx, page.SpaceID)
}

// spaceKeyFor resolves a numeric space id back to its key, for allowlist
// checks. An unresolvable id fails closed: the caller turns the empty key into
// a refusal rather than treating "unknown" as "allowed".
func (m module) spaceKeyFor(ctx context.Context, id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("the page reported no owning space, so the write allowlist cannot be checked")
	}
	if !rePageID.MatchString(id) {
		return "", fmt.Errorf("invalid space id %q: expected a numeric id", id)
	}
	var res struct {
		Key string `json:"key"`
	}
	if err := m.client.Do(ctx, http.MethodGet, "/wiki/api/v2/spaces/"+url.PathEscape(id), nil, nil, &res); err != nil {
		return "", err
	}
	if res.Key == "" {
		return "", fmt.Errorf("space %s returned no key, so the write allowlist cannot be checked", id)
	}
	return res.Key, nil
}
