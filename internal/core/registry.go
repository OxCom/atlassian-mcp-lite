package core

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// Action is a tool's capability class.
type Action int

const (
	// ActionRead returns data and changes nothing.
	ActionRead Action = iota
	// ActionWrite is additive and reversible.
	ActionWrite
	// ActionDestructive overwrites or moves state that is hard to recover.
	ActionDestructive
)

// String renders an Action for logs and errors.
func (a Action) String() string {
	switch a {
	case ActionRead:
		return "read"
	case ActionWrite:
		return "write"
	case ActionDestructive:
		return "destructive"
	}
	return "unknown"
}

func (a Action) allowedBy(c Caps) bool {
	switch a {
	case ActionRead:
		return c.Read
	case ActionWrite:
		return c.Write
	case ActionDestructive:
		return c.Destructive
	}
	return false
}

// ToolDecl is a tool a module offers. It is a declaration, not a registration:
// core decides whether it reaches the MCP server.
type ToolDecl struct {
	Name string
	// Actions is every class this tool spans. The tool is registered when at
	// least one of them is enabled, and Schema then advertises only the
	// properties those enabled classes permit. A tool spanning write and
	// destructive must list both, or it disappears entirely when only one is
	// enabled.
	Actions     []Action
	Description string
	// Schema builds the input schema from the domain's enabled capabilities,
	// so a tool spanning two action classes can advertise only the properties
	// its enabled classes permit.
	Schema func(Caps) *jsonschema.Schema
	// Handle receives the raw validated arguments.
	Handle func(context.Context, json.RawMessage) (any, error)
}

// enabled reports whether any of the tool's action classes is permitted.
func (d ToolDecl) enabled(c Caps) bool {
	for _, a := range d.Actions {
		if a.allowedBy(c) {
			return true
		}
	}
	return false
}

// actionNames renders the declared classes for logging.
func (d ToolDecl) actionNames() string {
	names := make([]string, 0, len(d.Actions))
	for _, a := range d.Actions {
		names = append(names, a.String())
	}
	return strings.Join(names, "+")
}

// Module is a product integration. A Module must not import the MCP SDK, read
// the environment, build an HTTP client, or log.
type Module interface {
	Domain() string
	Tools() []ToolDecl
}

// Registered is a tool that survived gating, with its schema already built.
type Registered struct {
	Domain string
	Decl   ToolDecl
	Schema *jsonschema.Schema
}

// Registry holds modules in registration order.
type Registry struct {
	modules []Module
}

// Register adds a module. Call before Domains or Enabled.
// Register adds a module and rejects a declaration that cannot be served.
// Call before Domains or Enabled.
//
// The rejected cases are all wiring mistakes in this repository's own code,
// made once at compile time, and each degrades badly if it survives to
// runtime: a nil Schema panics at startup anyway but without naming the tool,
// a nil Handle panics on the first call after the server has already
// advertised the tool, and a duplicate domain or tool name reaches the MCP
// server as a silent last-wins collision. Failing loudly at registration is
// the only one of those that names the offender.
func (r *Registry) Register(m Module) {
	domain := m.Domain()
	if domain == "" {
		panic("core: module registered with an empty domain")
	}
	taken := map[string]string{}
	for _, existing := range r.modules {
		if existing.Domain() == domain {
			panic("core: duplicate module domain " + domain)
		}
		for _, d := range existing.Tools() {
			taken[d.Name] = existing.Domain()
		}
	}
	for _, d := range m.Tools() {
		switch {
		case d.Name == "":
			panic("core: module " + domain + " declares a tool with no name")
		case d.Schema == nil:
			panic("core: tool " + d.Name + " (" + domain + ") has no Schema")
		case d.Handle == nil:
			panic("core: tool " + d.Name + " (" + domain + ") has no Handle")
		case taken[d.Name] != "":
			panic("core: tool name " + d.Name + " (" + domain + ") is already declared by " + taken[d.Name])
		}
		taken[d.Name] = domain
	}
	r.modules = append(r.modules, m)
}

// Domains returns the registered domain names, used to derive config keys.
func (r *Registry) Domains() []string {
	out := make([]string, 0, len(r.modules))
	for _, m := range r.modules {
		out = append(out, m.Domain())
	}
	return out
}

// Enabled returns only the tools whose action class is enabled for their
// domain, with schemas built from that domain's capabilities. A tool that is
// not returned here is never registered with the MCP server, so it is absent
// from tools/list and unknown to the dispatcher.
func (r *Registry) Enabled(cfg Config) []Registered {
	var out []Registered
	for _, m := range r.modules {
		caps := cfg.Domains[m.Domain()]
		if !caps.Any() {
			continue
		}
		for _, d := range m.Tools() {
			if !d.enabled(caps) {
				continue
			}
			out = append(out, Registered{Domain: m.Domain(), Decl: d, Schema: d.Schema(caps)})
		}
	}
	return out
}

// ObjectSchema builds an object schema that rejects unknown properties.
//
// AdditionalProperties is mandatory here. JSON Schema accepts unknown
// properties by default, so a property omitted from Properties would still
// validate and still unmarshal into the handler's struct — defeating the
// capability gating entirely.
func ObjectSchema(props map[string]*jsonschema.Schema, required []string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:                 "object",
		Properties:           props,
		Required:             required,
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
	}
}
