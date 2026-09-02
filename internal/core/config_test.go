package core

import (
	"strings"
	"testing"
)

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
	load := func(raw string) (Config, error) {
		return Load(env(map[string]string{
			"ATLAS_BASE_URL":  "https://x.atlassian.net",
			"ATLAS_EMAIL":     "a@b.c",
			"ATLAS_TOKEN":     "t",
			"ATLAS_JIRA_READ": raw,
		}), []string{"jira"})
	}
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"true", true}, {"TRUE", true}, {" 1 ", true}, {"yes", true}, {"on", true},
		{"false", false}, {"0", false}, {"no", false}, {"off", false}, {"", true},
	} {
		cfg, err := load(tc.raw)
		if err != nil {
			t.Fatalf("%q: Load: %v", tc.raw, err)
		}
		if got := cfg.Domains["jira"].Read; got != tc.want {
			t.Errorf("ATLAS_JIRA_READ=%q gave Read=%v, want %v", tc.raw, got, tc.want)
		}
	}
	// A typo must not read as false: the operator would believe reads are on.
	for _, raw := range []string{"ture", "yep", "2", "-1", "true false"} {
		if _, err := load(raw); err == nil {
			t.Errorf("ATLAS_JIRA_READ=%q must be rejected, not silently false", raw)
		}
	}
}

func TestLoadRejectsInvalidEmail(t *testing.T) {
	load := func(email string) error {
		_, err := Load(env(map[string]string{
			"ATLAS_BASE_URL": "https://x.atlassian.net",
			"ATLAS_EMAIL":    email,
			"ATLAS_TOKEN":    "t",
		}), []string{"jira"})
		return err
	}
	for name, email := range map[string]string{
		"no domain":      "nobody",
		"no local part":  "@example.com",
		"display name":   "A <a@b.c>",
		"two addresses":  "a@b.c, d@e.f",
		"colon":          "a:b@example.com",
		"embedded CR":    "a@b.c\rX-Injected: 1",
		"embedded LF":    "a@b.c\nX-Injected: 1",
		"inner space":    "a b@example.com",
		"bare host only": "a@",
	} {
		if err := load(email); err == nil {
			t.Errorf("%s (%q) must be rejected", name, email)
		}
	}
	for _, email := range []string{"a@b.c", "first.last+tag@example.co.uk", " a@b.c "} {
		if err := load(email); err != nil {
			t.Errorf("%q must be accepted: %v", email, err)
		}
	}
}

func TestLoadRejectsMalformedToken(t *testing.T) {
	load := func(token string) error {
		_, err := Load(env(map[string]string{
			"ATLAS_BASE_URL": "https://x.atlassian.net",
			"ATLAS_EMAIL":    "a@b.c",
			"ATLAS_TOKEN":    token,
		}), []string{"jira"})
		return err
	}
	for name, token := range map[string]string{
		"newline": "abc\ndef",
		"CR":      "abc\rdef",
		"tab":     "abc\tdef",
		"space":   "abc def",
		"NUL":     "abc\x00def",
	} {
		err := load(token)
		if err == nil {
			t.Errorf("%s token must be rejected", name)
			continue
		}
		// A validation error must never carry the credential.
		if strings.Contains(err.Error(), "abc") {
			t.Errorf("%s: error message quotes the token: %v", name, err)
		}
	}
	if err := load("ATATT3xFfGF0-abc_DEF.123:="); err != nil {
		t.Errorf("a realistic Atlassian token must be accepted: %v", err)
	}
}

func TestLoadValidatesEpicFieldID(t *testing.T) {
	load := func(v string) (Config, error) {
		return Load(env(map[string]string{
			"ATLAS_BASE_URL":      "https://x.atlassian.net",
			"ATLAS_EMAIL":         "a@b.c",
			"ATLAS_TOKEN":         "t",
			"ATLAS_EPIC_FIELD_ID": v,
		}), []string{"jira"})
	}
	cfg, err := load("customfield_12345")
	if err != nil || cfg.EpicFieldID != "customfield_12345" {
		t.Errorf("override = %q, %v", cfg.EpicFieldID, err)
	}
	for _, raw := range []string{"custom field", "customfield-1", "../x", "cf?a=b", "10014", ""} {
		if raw == "" {
			// Empty falls back to the default rather than failing.
			if cfg, err := load(raw); err != nil || cfg.EpicFieldID != "customfield_10014" {
				t.Errorf("empty must default: %q, %v", cfg.EpicFieldID, err)
			}
			continue
		}
		if _, err := load(raw); err == nil {
			t.Errorf("ATLAS_EPIC_FIELD_ID=%q must be rejected", raw)
		}
	}
}

func TestLoadValidatesAllowlistKeys(t *testing.T) {
	for _, name := range []string{"ATLAS_WRITE_PROJECTS", "ATLAS_WRITE_SPACES"} {
		for _, raw := range []string{"PROJ/../OTHER", "PROJ KEY", "PROJ*", `PROJ" OR 1=1`, "PR%20OJ", "..", "PROJ,../x"} {
			if _, err := Load(env(map[string]string{
				"ATLAS_BASE_URL": "https://x.atlassian.net",
				"ATLAS_EMAIL":    "a@b.c",
				"ATLAS_TOKEN":    "t",
				name:             raw,
			}), []string{"jira"}); err == nil {
				t.Errorf("%s=%q must be rejected", name, raw)
			}
		}
	}
	// Confluence personal space keys are prefixed with a tilde.
	if _, err := Load(env(map[string]string{
		"ATLAS_BASE_URL":     "https://x.atlassian.net",
		"ATLAS_EMAIL":        "a@b.c",
		"ATLAS_TOKEN":        "t",
		"ATLAS_WRITE_SPACES": "~712020abc, DOCS_1",
	}), []string{"confluence"}); err != nil {
		t.Errorf("personal space key must be accepted: %v", err)
	}
}

func TestLoadValidatesDomainNames(t *testing.T) {
	load := func(domains []string) error {
		_, err := Load(env(map[string]string{
			"ATLAS_BASE_URL": "https://x.atlassian.net",
			"ATLAS_EMAIL":    "a@b.c",
			"ATLAS_TOKEN":    "t",
		}), domains)
		return err
	}
	for _, domains := range [][]string{
		{""}, {"Jira"}, {"ji ra"}, {"ji-ra"}, {"1jira"}, {"jira", "jira"},
	} {
		if err := load(domains); err == nil {
			t.Errorf("domains %q must be rejected", domains)
		}
	}
	if err := load([]string{"jira", "confluence", "service_desk"}); err != nil {
		t.Errorf("valid domains rejected: %v", err)
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

// With nothing set, every domain reads and nothing writes: the server is
// useful out of the box and still unable to modify anything.
func TestCapabilityDefaultsAreReadOnly(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"ATLAS_BASE_URL": "https://x.atlassian.net",
		"ATLAS_EMAIL":    "a@b.c",
		"ATLAS_TOKEN":    "t",
	}), []string{"jira", "confluence"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, d := range []string{"jira", "confluence"} {
		if got := cfg.Domains[d]; !got.Read || got.Write || got.Destructive {
			t.Errorf("%s caps = %+v, want read only by default", d, got)
		}
	}

	cfg, err = Load(env(map[string]string{
		"ATLAS_BASE_URL":  "https://x.atlassian.net",
		"ATLAS_EMAIL":     "a@b.c",
		"ATLAS_TOKEN":     "t",
		"ATLAS_JIRA_READ": "false",
	}), []string{"jira"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Domains["jira"].Any() {
		t.Errorf("jira caps = %+v, want nothing enabled once read is turned off", cfg.Domains["jira"])
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

func TestLimitValidation(t *testing.T) {
	load := func(def, max string) (Config, error) {
		return Load(env(map[string]string{
			"ATLAS_BASE_URL":      "https://x.atlassian.net",
			"ATLAS_EMAIL":         "a@b.c",
			"ATLAS_TOKEN":         "t",
			"ATLAS_LIMIT_DEFAULT": def,
			"ATLAS_LIMIT_MAX":     max,
		}), []string{"jira"})
	}
	cfg, err := load("10", "25")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LimitDefault != 10 || cfg.LimitMax != 25 {
		t.Errorf("custom limits = %d/%d, want 10/25", cfg.LimitDefault, cfg.LimitMax)
	}

	// A value that is set but unparsable is a typo in a bound on result size,
	// so it fails rather than falling back to the default.
	for _, raw := range []string{"20x", "0", "-5", "abc", "1e3", "20 30"} {
		if _, err := load(raw, "50"); err == nil {
			t.Errorf("ATLAS_LIMIT_DEFAULT=%q must be rejected", raw)
		}
	}
	// Unset still means the default.
	if cfg, err := load("", ""); err != nil || cfg.LimitDefault != 20 || cfg.LimitMax != 50 {
		t.Errorf("unset limits = %d/%d, %v; want 20/50", cfg.LimitDefault, cfg.LimitMax, err)
	}
	// A default above the max is a contradiction, not something to clamp.
	if _, err := load("40", "30"); err == nil {
		t.Error("ATLAS_LIMIT_DEFAULT above ATLAS_LIMIT_MAX must be rejected")
	}
	if _, err := load("20", "1001"); err == nil {
		t.Error("ATLAS_LIMIT_MAX above the ceiling must be rejected")
	}
	if _, err := load("20", "1000"); err != nil {
		t.Errorf("ATLAS_LIMIT_MAX at the ceiling must be accepted: %v", err)
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
