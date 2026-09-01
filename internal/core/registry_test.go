package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

type fakeModule struct {
	domain string
	tools  []ToolDecl
}

func (f fakeModule) Domain() string    { return f.domain }
func (f fakeModule) Tools() []ToolDecl { return f.tools }

func decl(name string, actions ...Action) ToolDecl {
	return ToolDecl{
		Name:        name,
		Actions:     actions,
		Description: name,
		Schema: func(c Caps) *jsonschema.Schema {
			props := map[string]*jsonschema.Schema{"key": {Type: "string"}}
			if c.Destructive {
				props["description"] = &jsonschema.Schema{Type: "string"}
			}
			return ObjectSchema(props, []string{"key"})
		},
		Handle: func(context.Context, json.RawMessage) (any, error) { return nil, nil },
	}
}

func capsCfg(c Caps) Config {
	return Config{Domains: map[string]Caps{"fake": c}}
}

func TestEnabledDropsDisabledActions(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{
		decl("fake_read", ActionRead),
		decl("fake_write", ActionWrite),
		decl("fake_nuke", ActionDestructive),
	}})

	got := r.Enabled(capsCfg(Caps{Read: true, Write: true}))
	names := map[string]bool{}
	for _, g := range got {
		names[g.Decl.Name] = true
	}
	if !names["fake_read"] || !names["fake_write"] {
		t.Errorf("enabled = %v, want read and write present", names)
	}
	if names["fake_nuke"] {
		t.Error("destructive tool registered while destructive=false")
	}
}

func TestEnabledEmptyWhenDomainFullyDisabled(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{decl("fake_read", ActionRead)}})
	if got := r.Enabled(capsCfg(Caps{})); len(got) != 0 {
		t.Errorf("enabled = %d tools, want 0", len(got))
	}
}

func TestSchemaBuiltFromCapsOmitsDestructiveProperty(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{decl("fake_update", ActionWrite)}})

	off := r.Enabled(capsCfg(Caps{Write: true}))
	if len(off) != 1 {
		t.Fatalf("got %d tools", len(off))
	}
	if _, ok := off[0].Schema.Properties["description"]; ok {
		t.Error("description must be absent from the schema when destructive=false")
	}

	on := r.Enabled(capsCfg(Caps{Write: true, Destructive: true}))
	if len(on) != 1 {
		t.Fatalf("destructive on: got %d tools, want 1", len(on))
	}
	if _, ok := on[0].Schema.Properties["description"]; !ok {
		t.Error("description must be present when destructive=true")
	}
}

func TestObjectSchemaRejectsUnknownProperties(t *testing.T) {
	s := ObjectSchema(map[string]*jsonschema.Schema{"key": {Type: "string"}}, []string{"key"})

	// Asserting AdditionalProperties is non-nil is not enough: a permissive
	// schema such as {} is also non-nil and accepts everything. Resolve the
	// schema and validate real documents against it.
	resolved, err := s.Resolve(&jsonschema.ResolveOptions{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := resolved.Validate(map[string]any{"key": "PROJ-1"}); err != nil {
		t.Errorf("a valid object must pass: %v", err)
	}
	if err := resolved.Validate(map[string]any{"key": "PROJ-1", "description": "x"}); err == nil {
		t.Fatal("an unknown property must fail validation; capability gating depends on it")
	}
}

// A tool spanning write and destructive must survive when only one of them is
// enabled. Testing Schema(caps) directly cannot catch this: the registry drops
// the tool before the schema is ever built.
func TestEnabledKeepsMultiClassToolWhenOnlyOneClassIsOn(t *testing.T) {
	multi := ToolDecl{
		Name:    "fake_update",
		Actions: []Action{ActionWrite, ActionDestructive},
		Schema: func(c Caps) *jsonschema.Schema {
			props := map[string]*jsonschema.Schema{"key": {Type: "string"}}
			if c.Write {
				props["assignee"] = &jsonschema.Schema{Type: "string"}
			}
			if c.Destructive {
				props["description"] = &jsonschema.Schema{Type: "string"}
			}
			return ObjectSchema(props, []string{"key"})
		},
		Handle: func(context.Context, json.RawMessage) (any, error) { return nil, nil },
	}
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{multi}})

	destructiveOnly := r.Enabled(capsCfg(Caps{Destructive: true}))
	if len(destructiveOnly) != 1 {
		t.Fatalf("destructive-only: registered %d tools, want 1", len(destructiveOnly))
	}
	props := destructiveOnly[0].Schema.Properties
	if _, ok := props["description"]; !ok {
		t.Error("destructive-only: description must be present")
	}
	if _, ok := props["assignee"]; ok {
		t.Error("destructive-only: assignee must be absent")
	}

	writeOnly := r.Enabled(capsCfg(Caps{Write: true}))
	if len(writeOnly) != 1 {
		t.Fatalf("write-only: registered %d tools, want 1", len(writeOnly))
	}
	if _, ok := writeOnly[0].Schema.Properties["description"]; ok {
		t.Error("write-only: description must be absent")
	}

	if got := r.Enabled(capsCfg(Caps{Read: true})); len(got) != 0 {
		t.Errorf("read-only: registered %d tools, want 0", len(got))
	}
}

func TestDomainsListedForConfigDerivation(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "jira"})
	r.Register(fakeModule{domain: "confluence"})
	got := r.Domains()
	if len(got) != 2 || got[0] != "jira" || got[1] != "confluence" {
		t.Errorf("Domains() = %v, want registration order", got)
	}
}

func TestActionNamesRenderEveryClass(t *testing.T) {
	if got := Action(99).String(); got != "unknown" {
		t.Errorf("Action(99).String() = %q, want \"unknown\"", got)
	}
	d := ToolDecl{Actions: []Action{ActionRead, ActionWrite, ActionDestructive}}
	if got := d.actionNames(); got != "read+write+destructive" {
		t.Errorf("actionNames() = %q, want \"read+write+destructive\"", got)
	}
	if got := (ToolDecl{}).actionNames(); got != "" {
		t.Errorf("actionNames() with no actions = %q, want empty", got)
	}
}

// An unknown action class must be denied, not silently permitted: the default
// arm of allowedBy is a security boundary, so a tool declaring only it stays
// out even when every capability is on.
func TestUnknownActionIsNeverAllowed(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{decl("fake_odd", Action(99))}})
	if got := r.Enabled(capsCfg(Caps{Read: true, Write: true, Destructive: true})); len(got) != 0 {
		t.Errorf("enabled = %d tools, want 0", len(got))
	}
}

// A module whose domain is absent from the config must be skipped entirely: a
// missing map key yields the zero Caps, which enables nothing.
func TestEnabledSkipsDomainAbsentFromConfig(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "other", tools: []ToolDecl{decl("other_read", ActionRead)}})
	if got := r.Enabled(capsCfg(Caps{Read: true})); len(got) != 0 {
		t.Errorf("enabled = %d tools, want 0", len(got))
	}
}

// Registered carries the domain so the server can log and route by product.
func TestRegisteredCarriesItsDomain(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{decl("fake_read", ActionRead)}})
	got := r.Enabled(capsCfg(Caps{Read: true}))
	if len(got) != 1 {
		t.Fatalf("registered %d tools, want 1", len(got))
	}
	if got[0].Domain != "fake" {
		t.Errorf("Domain = %q, want \"fake\"", got[0].Domain)
	}
	if got[0].Decl.Handle == nil {
		t.Error("Handle must survive gating; the dispatcher needs it")
	}
}

// Deny by default: a tool declaring no action class at all must never be
// registered, however permissive the domain is.
func TestEmptyActionsRegisterNothing(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{decl("fake_silent")}})
	if got := r.Enabled(capsCfg(Caps{Read: true, Write: true, Destructive: true})); len(got) != 0 {
		t.Errorf("enabled = %d tools, want 0", len(got))
	}
}

func mustPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		got, ok := recover().(string)
		if !ok {
			t.Fatalf("no string panic; want one containing %q", want)
		}
		if !strings.Contains(got, want) {
			t.Errorf("panic = %q, want it to contain %q", got, want)
		}
	}()
	f()
}

// A declaration that cannot be served must be rejected where it is written,
// naming the offender — not at first dispatch, after the server has already
// advertised the tool.
func TestRegisterRejectsUnservableDeclarations(t *testing.T) {
	ok := decl("fake_read", ActionRead)

	noSchema := ok
	noSchema.Schema = nil
	noHandle := ok
	noHandle.Handle = nil
	unnamed := ok
	unnamed.Name = ""

	cases := []struct {
		name  string
		want  string
		tools []ToolDecl
	}{
		{"nil schema", "has no Schema", []ToolDecl{noSchema}},
		{"nil handle", "has no Handle", []ToolDecl{noHandle}},
		{"no name", "declares a tool with no name", []ToolDecl{unnamed}},
		{"duplicate name in one module", "already declared by", []ToolDecl{ok, ok}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &Registry{}
			mustPanic(t, c.want, func() { r.Register(fakeModule{domain: "fake", tools: c.tools}) })
		})
	}
}

func TestRegisterRejectsEmptyDomain(t *testing.T) {
	r := &Registry{}
	mustPanic(t, "empty domain", func() { r.Register(fakeModule{}) })
}

// Two modules claiming the same domain would both read the same capability
// config, and Domains() would derive the key twice.
func TestRegisterRejectsDuplicateDomain(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "fake", tools: []ToolDecl{decl("fake_read", ActionRead)}})
	mustPanic(t, "duplicate module domain", func() {
		r.Register(fakeModule{domain: "fake", tools: []ToolDecl{decl("fake_other", ActionRead)}})
	})
}

// Tool names are the MCP dispatch key and are global, not per domain: a
// collision across two modules is last-wins at the server and must not reach
// it.
func TestRegisterRejectsToolNameTakenByAnotherModule(t *testing.T) {
	r := &Registry{}
	r.Register(fakeModule{domain: "jira", tools: []ToolDecl{decl("shared", ActionRead)}})
	mustPanic(t, "already declared by jira", func() {
		r.Register(fakeModule{domain: "confluence", tools: []ToolDecl{decl("shared", ActionRead)}})
	})
}
