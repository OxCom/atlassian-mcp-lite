// Package confluence declares the Confluence tools. Like the jira package it
// must not import the MCP SDK, read the environment, build an HTTP client, or
// log — core owns all of that.
package confluence

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
)

// Domain is the config and gating key for this module.
const Domain = "confluence"

// maxCQLLen bounds the one free-form string a caller controls on this path.
// Every other input in the repository is bounded — field names, field counts,
// limits, ids — and an unbounded query would be the only exception.
const maxCQLLen = 4096

// maxIDLen bounds a numeric content or space id. The digit pattern alone
// accepts a string of any length, so without this a megabyte of "9" would
// satisfy it and reach a URL path. Confluence ids are far shorter than this.
const maxIDLen = 20

// The Confluence v2 field names this package reads, writes and advertises.
// They are constants because the same name appears in a tool schema, in a
// request body and in a returned map, and a typo in any one of those three is
// silent: the schema would advertise a property no handler reads, or the body
// would carry a key Atlassian ignores.
const (
	fieldID      = "id"
	fieldTitle   = "title"
	fieldBody    = "body"
	fieldSpace   = "space"
	fieldSpaceID = "spaceId"
	fieldVersion = "version"
	fieldPageID  = "page_id"
)

// stringProp is a string-typed schema property. Every identifier this package
// accepts is a string, never a JSON number: Confluence page ids are already
// past 2^53 and the SDK re-marshals arguments through map[string]any, where a
// number would lose precision silently.
func stringProp(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Description: description}
}

// boundedProp is a string property that advertises the bound its handler
// enforces, so a compliant client can refuse locally instead of discovering
// the limit through an error.
func boundedProp(description string, maxLen int) *jsonschema.Schema {
	p := stringProp(description)
	p.MaxLength = &maxLen
	return p
}

// idProp is a numeric-id property, bounded by maxIDLen.
func idProp(description string) *jsonschema.Schema {
	return boundedProp(description, maxIDLen)
}

// rePageID matches Confluence's numeric content ids. Enforced before any id
// reaches a URL path.
var rePageID = regexp.MustCompile(`^\d+$`)

type module struct {
	cfg    core.Config
	client *core.Client
}

// New returns a declaration-only module for domain discovery.
func New() core.Module { return module{} }

// NewWith returns a functional module.
func NewWith(cfg core.Config, c *core.Client) core.Module {
	return module{cfg: cfg, client: c}
}

func (m module) Domain() string { return Domain }

func (m module) Tools() []core.ToolDecl {
	return []core.ToolDecl{
		m.searchDecl(),
		m.getPageDecl(),
		m.createPageDecl(),
		m.updatePageDecl(),
		m.commentDecl(),
	}
}

func validPageID(id string) (string, error) {
	return validNumericID("page id", id)
}

// validNumericID is the one gate every id passes before it reaches a URL path
// or a request body, whether the caller supplied it or Confluence did. The
// length is checked first and the value is not echoed on that path: an
// over-long id is exactly the input an error message should not reflect.
func validNumericID(kind, id string) (string, error) {
	id = strings.TrimSpace(id)
	if len(id) > maxIDLen {
		return "", fmt.Errorf("%s is %d bytes, limit is %d", kind, len(id), maxIDLen)
	}
	if !rePageID.MatchString(id) {
		return "", fmt.Errorf("invalid %s %q: expected a numeric id", kind, id)
	}
	return id, nil
}
