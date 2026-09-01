package core

import "testing"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadRequiresBaseURLEmailToken(t *testing.T) {
	_, err := Load(env(map[string]string{"ATLAS_BASE_URL": "https://x.atlassian.net"}), []string{"jira"})
	if err == nil {
		t.Fatal("expected error when ATLAS_EMAIL and ATLAS_TOKEN are missing")
	}
}

// Each credential must be required on its own: omitting all of them at once
// would pass even if Load checked only one.
func TestLoadRequiresEachCredentialIndependently(t *testing.T) {
	full := map[string]string{
		"ATLAS_BASE_URL": "https://x.atlassian.net",
		"ATLAS_EMAIL":    "a@b.c",
		"ATLAS_TOKEN":    "t",
	}
	for _, omit := range []string{"ATLAS_BASE_URL", "ATLAS_EMAIL", "ATLAS_TOKEN"} {
		m := map[string]string{}
		for k, v := range full {
			if k != omit {
				m[k] = v
			}
		}
		if _, err := Load(env(m), []string{"jira"}); err == nil {
			t.Errorf("missing %s must be an error", omit)
		}
		m[omit] = "   "
		if _, err := Load(env(m), []string{"jira"}); err == nil {
			t.Errorf("whitespace-only %s must be an error", omit)
		}
	}
}

func TestCapabilityFlagSynonyms(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"true", true}, {"TRUE", true}, {" 1 ", true}, {"yes", true}, {"on", true},
		{"false", false}, {"0", false}, {"", false}, {"ture", false},
	} {
		cfg, err := Load(env(map[string]string{
			"ATLAS_BASE_URL":  "https://x.atlassian.net",
			"ATLAS_EMAIL":     "a@b.c",
			"ATLAS_TOKEN":     "t",
			"ATLAS_JIRA_READ": tc.raw,
		}), []string{"jira"})
		if err != nil {
			t.Fatalf("%q: Load: %v", tc.raw, err)
		}
		if got := cfg.Domains["jira"].Read; got != tc.want {
			t.Errorf("ATLAS_JIRA_READ=%q gave Read=%v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestCapsAny(t *testing.T) {
	if (Caps{}).Any() {
		t.Error("zero Caps must have no capability")
	}
	for _, c := range []Caps{{Read: true}, {Write: true}, {Destructive: true}} {
		if !c.Any() {
			t.Errorf("%+v must report a capability", c)
		}
	}
}

func TestLoadDerivesDomainCaps(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"ATLAS_BASE_URL":         "https://x.atlassian.net",
		"ATLAS_EMAIL":            "a@b.c",
		"ATLAS_TOKEN":            "t",
		"ATLAS_JIRA_READ":        "true",
		"ATLAS_JIRA_WRITE":       "true",
		"ATLAS_JIRA_DESTRUCTIVE": "false",
		"ATLAS_CONFLUENCE_READ":  "true",
	}), []string{"jira", "confluence"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Domains["jira"]; !got.Read || !got.Write || got.Destructive {
		t.Errorf("jira caps = %+v, want read+write only", got)
	}
	if got := cfg.Domains["confluence"]; !got.Read || got.Write || got.Destructive {
		t.Errorf("confluence caps = %+v, want read only (unset means false)", got)
	}
}

func TestAllowlistUnsetAndEmptyAreUnrestricted(t *testing.T) {
	for name, m := range map[string]map[string]string{
		"unset": {},
		"empty": {"ATLAS_WRITE_PROJECTS": ""},
	} {
		m["ATLAS_BASE_URL"] = "https://x.atlassian.net"
		m["ATLAS_EMAIL"] = "a@b.c"
		m["ATLAS_TOKEN"] = "t"
		cfg, err := Load(env(m), []string{"jira"})
		if err != nil {
			t.Fatalf("%s: Load: %v", name, err)
		}
		if !cfg.AllowProject("ANYTHING") {
			t.Errorf("%s: want unrestricted", name)
		}
	}
}

func TestAllowlistNonEmptyIsStrict(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"ATLAS_BASE_URL":       "https://x.atlassian.net",
		"ATLAS_EMAIL":          "a@b.c",
		"ATLAS_TOKEN":          "t",
		"ATLAS_WRITE_PROJECTS": "PROJ, CEM",
	}), []string{"jira"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AllowProject("PROJ") {
		t.Error("PROJ should be allowed")
	}
	if !cfg.AllowProject("cem") {
		t.Error("matching must be case-insensitive and whitespace-trimmed")
	}
	if cfg.AllowProject("HR") {
		t.Error("HR must be refused")
	}
}

func TestSpaceAllowlist(t *testing.T) {
	base := map[string]string{
		"ATLAS_BASE_URL": "https://x.atlassian.net",
		"ATLAS_EMAIL":    "a@b.c",
		"ATLAS_TOKEN":    "t",
	}
	cfg, err := Load(env(base), []string{"confluence"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AllowSpace("ANY") {
		t.Error("unset ATLAS_WRITE_SPACES must be unrestricted")
	}

	base["ATLAS_WRITE_SPACES"] = "DOCS, ops"
	cfg, err = Load(env(base), []string{"confluence"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AllowSpace("DOCS") || !cfg.AllowSpace("OPS") {
		t.Error("listed spaces must be allowed, case-insensitively")
	}
	if cfg.AllowSpace("SECRET") {
		t.Error("unlisted space must be refused")
	}
}

// The two allowlists must read their own env vars: a crossed assignment would
// pass every single-list test.
func TestProjectAndSpaceAllowlistsAreIndependent(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"ATLAS_BASE_URL":       "https://x.atlassian.net",
		"ATLAS_EMAIL":          "a@b.c",
		"ATLAS_TOKEN":          "t",
		"ATLAS_WRITE_PROJECTS": "PROJ",
		"ATLAS_WRITE_SPACES":   "DOCS",
	}), []string{"jira", "confluence"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AllowProject("PROJ") || cfg.AllowProject("DOCS") {
		t.Errorf("WriteProjects = %v, want only PROJ", cfg.WriteProjects)
	}
	if !cfg.AllowSpace("DOCS") || cfg.AllowSpace("PROJ") {
		t.Errorf("WriteSpaces = %v, want only DOCS", cfg.WriteSpaces)
	}
}

// A non-empty allowlist that yields no keys means restriction was intended;
// treating it as unrestricted would fail open.
func TestAllowlistWithNoUsableKeysIsRejected(t *testing.T) {
	for _, name := range []string{"ATLAS_WRITE_PROJECTS", "ATLAS_WRITE_SPACES"} {
		for _, raw := range []string{",", " , ", "  "} {
			_, err := Load(env(map[string]string{
				"ATLAS_BASE_URL": "https://x.atlassian.net",
				"ATLAS_EMAIL":    "a@b.c",
				"ATLAS_TOKEN":    "t",
				name:             raw,
			}), []string{"jira"})
			if raw == "  " {
				if err != nil {
					t.Errorf("%s=%q is whitespace only, same as unset: %v", name, raw, err)
				}
				continue
			}
			if err == nil {
				t.Errorf("%s=%q must be rejected rather than silently unrestricted", name, raw)
			}
		}
	}
}

func TestLogLevel(t *testing.T) {
	load := func(v string) (Config, error) {
		return Load(env(map[string]string{
			"ATLAS_BASE_URL": "https://x.atlassian.net",
			"ATLAS_EMAIL":    "a@b.c",
			"ATLAS_TOKEN":    "t",
			"ATLAS_LOG":      v,
		}), []string{"jira"})
	}
	for _, tc := range []struct {
		raw, want string
	}{
		{"", "info"}, {"debug", "debug"}, {"DEBUG", "debug"}, {" info ", "info"},
	} {
		raw, want := tc.raw, tc.want
		cfg, err := load(raw)
		if err != nil {
			t.Fatalf("ATLAS_LOG=%q: %v", raw, err)
		}
		if cfg.LogLevel != want {
			t.Errorf("ATLAS_LOG=%q gave %q, want %q", raw, cfg.LogLevel, want)
		}
	}
	if _, err := load("verbose"); err == nil {
		t.Error("ATLAS_LOG=verbose is outside the enum and must be rejected")
	}
}

func TestLimitParsingAndClamp(t *testing.T) {
	load := func(def, max string) Config {
		cfg, err := Load(env(map[string]string{
			"ATLAS_BASE_URL":      "https://x.atlassian.net",
			"ATLAS_EMAIL":         "a@b.c",
			"ATLAS_TOKEN":         "t",
			"ATLAS_LIMIT_DEFAULT": def,
			"ATLAS_LIMIT_MAX":     max,
		}), []string{"jira"})
		if err != nil {
			t.Fatalf("Load(%q, %q): %v", def, max, err)
		}
		return cfg
	}
	if cfg := load("10", "25"); cfg.LimitDefault != 10 || cfg.LimitMax != 25 {
		t.Errorf("custom limits = %d/%d, want 10/25", cfg.LimitDefault, cfg.LimitMax)
	}
	// Parsing is strict: anything that is not a positive integer falls back.
	for _, raw := range []string{"20x", "0", "-5", "abc", " "} {
		if cfg := load(raw, "50"); cfg.LimitDefault != 20 {
			t.Errorf("ATLAS_LIMIT_DEFAULT=%q gave %d, want the 20 default", raw, cfg.LimitDefault)
		}
	}
	if cfg := load("40", "30"); cfg.LimitDefault != 30 || cfg.LimitMax != 30 {
		t.Errorf("default above max = %d/%d, want both 30", cfg.LimitDefault, cfg.LimitMax)
	}
}

func TestLimitDefaults(t *testing.T) {
	cfg, _ := Load(env(map[string]string{
		"ATLAS_BASE_URL": "https://x.atlassian.net",
		"ATLAS_EMAIL":    "a@b.c",
		"ATLAS_TOKEN":    "t",
	}), []string{"jira"})
	if cfg.LimitDefault != 20 || cfg.LimitMax != 50 {
		t.Errorf("limits = %d/%d, want 20/50", cfg.LimitDefault, cfg.LimitMax)
	}
	if cfg.EpicFieldID != "customfield_10014" {
		t.Errorf("EpicFieldID = %q, want customfield_10014", cfg.EpicFieldID)
	}
}

func TestLoadRejectsUnsafeBaseURLs(t *testing.T) {
	for name, raw := range map[string]string{
		"plain http":  "http://example.atlassian.net",
		"credentials": "https://u:p@example.atlassian.net",
		"has path":    "https://example.atlassian.net/wiki",
		"has query":   "https://example.atlassian.net?a=b",
		"unsupported": "ftp://example.atlassian.net",
		"no host":     "https://",
		"bare query":  "https://example.atlassian.net?",
		"bare frag":   "https://example.atlassian.net#",
		"fragment":    "https://example.atlassian.net#frag",
	} {
		_, err := Load(env(map[string]string{
			"ATLAS_BASE_URL": raw,
			"ATLAS_EMAIL":    "a@b.c",
			"ATLAS_TOKEN":    "t",
		}), []string{"jira"})
		if err == nil {
			t.Errorf("%s (%q) must be rejected", name, raw)
		}
	}
}

func TestLoadAllowsHTTPForLoopbackSoTestsCanRun(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8080", "http://localhost:9000", "http://[::1]:9000"} {
		if _, err := Load(env(map[string]string{
			"ATLAS_BASE_URL": raw,
			"ATLAS_EMAIL":    "a@b.c",
			"ATLAS_TOKEN":    "t",
		}), []string{"jira"}); err != nil {
			t.Errorf("%q must be accepted: %v", raw, err)
		}
	}
}

func TestBaseURLTrailingSlashStripped(t *testing.T) {
	cfg, _ := Load(env(map[string]string{
		"ATLAS_BASE_URL": "https://x.atlassian.net/",
		"ATLAS_EMAIL":    "a@b.c",
		"ATLAS_TOKEN":    "t",
	}), []string{"jira"})
	if cfg.BaseURL != "https://x.atlassian.net" {
		t.Errorf("BaseURL = %q, want no trailing slash", cfg.BaseURL)
	}
}
