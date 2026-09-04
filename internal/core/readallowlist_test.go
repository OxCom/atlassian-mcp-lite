package core

import (
	"reflect"
	"strings"
	"testing"
)

// loadWith resolves a configuration with only the credentials and the given
// extra environment, for both domains.
func loadWith(t *testing.T, extra map[string]string) Config {
	t.Helper()
	vars := map[string]string{
		"ATLAS_BASE_URL": "https://x.atlassian.net",
		"ATLAS_EMAIL":    "a@b.c",
		"ATLAS_TOKEN":    fixtureToken,
	}
	for k, v := range extra {
		vars[k] = v
	}
	cfg, err := Load(env(vars), []string{"jira", "confluence"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestLoadParsesReadAllowlists(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		want     []string
		restrict bool
	}{
		{name: "single value", value: "DEV", want: []string{"DEV"}, restrict: true},
		{name: "multiple values", value: "DEV,PLATFORM,INFRA", want: []string{"DEV", "PLATFORM", "INFRA"}, restrict: true},
		{name: "whitespace is trimmed", value: " DEV , PLATFORM , INFRA ", want: []string{"DEV", "PLATFORM", "INFRA"}, restrict: true},
		// Duplicates are kept rather than deduplicated: membership is a scan,
		// so a repeat is harmless, and rewriting the operator's list would
		// make the resolved configuration differ from what they wrote.
		{name: "duplicates are harmless", value: "DEV,DEV,PLATFORM", want: []string{"DEV", "DEV", "PLATFORM"}, restrict: true},
		{name: "empty means unrestricted", value: "", want: nil, restrict: false},
	}
	for _, c := range cases {
		t.Run("projects/"+c.name, func(t *testing.T) {
			cfg := loadWith(t, map[string]string{"ATLAS_READ_PROJECTS": c.value})
			if !reflect.DeepEqual(cfg.ReadProjects, c.want) {
				t.Errorf("ReadProjects = %#v, want %#v", cfg.ReadProjects, c.want)
			}
			if got := cfg.RestrictsReadProjects(); got != c.restrict {
				t.Errorf("RestrictsReadProjects() = %v, want %v", got, c.restrict)
			}
		})
		t.Run("spaces/"+c.name, func(t *testing.T) {
			cfg := loadWith(t, map[string]string{"ATLAS_READ_SPACES": c.value})
			if !reflect.DeepEqual(cfg.ReadSpaces, c.want) {
				t.Errorf("ReadSpaces = %#v, want %#v", cfg.ReadSpaces, c.want)
			}
			if got := cfg.RestrictsReadSpaces(); got != c.restrict {
				t.Errorf("RestrictsReadSpaces() = %v, want %v", got, c.restrict)
			}
		})
	}
}

// An unset variable is the backwards-compatible case: an existing deployment
// gains no restriction by upgrading.
func TestLoadLeavesReadAllowlistsUnsetByDefault(t *testing.T) {
	cfg := loadWith(t, nil)
	if cfg.ReadProjects != nil || cfg.ReadSpaces != nil {
		t.Fatalf("read allowlists = (%#v, %#v), want both nil", cfg.ReadProjects, cfg.ReadSpaces)
	}
	if cfg.RestrictsReadProjects() || cfg.RestrictsReadSpaces() {
		t.Error("an unset variable must not restrict reads")
	}
	for _, key := range []string{"DEV", "SECRET", "~alice"} {
		if !cfg.AllowReadProject(key) || !cfg.AllowReadSpace(key) {
			t.Errorf("%q must be readable with no allowlist configured", key)
		}
	}
}

// A value that is set but names no key expresses intent to restrict; allowing
// everything then would be the opposite of what the operator asked for.
func TestLoadRejectsReadAllowlistWithNoKeys(t *testing.T) {
	for _, name := range []string{"ATLAS_READ_PROJECTS", "ATLAS_READ_SPACES"} {
		for _, raw := range []string{",", " , ", " "} {
			_, err := Load(env(map[string]string{
				"ATLAS_BASE_URL": "https://x.atlassian.net",
				"ATLAS_EMAIL":    "a@b.c",
				"ATLAS_TOKEN":    fixtureToken,
				name:             raw,
			}), []string{"jira", "confluence"})
			if raw == " " {
				// A whitespace-only value is indistinguishable from unset.
				if err != nil {
					t.Errorf("%s=%q: %v", name, raw, err)
				}
				continue
			}
			if err == nil {
				t.Errorf("%s=%q must be rejected", name, raw)
			}
		}
	}
}

func TestLoadValidatesReadAllowlistKeys(t *testing.T) {
	for _, name := range []string{"ATLAS_READ_PROJECTS", "ATLAS_READ_SPACES"} {
		for _, raw := range []string{"PROJ/../OTHER", "PROJ KEY", "PROJ*", `PROJ" OR 1=1`, "PR%20OJ", "..", `DEV") OR project=SECRET OR ("`} {
			_, err := Load(env(map[string]string{
				"ATLAS_BASE_URL": "https://x.atlassian.net",
				"ATLAS_EMAIL":    "a@b.c",
				"ATLAS_TOKEN":    fixtureToken,
				name:             raw,
			}), []string{"jira", "confluence"})
			if err == nil {
				t.Errorf("%s=%q must be rejected", name, raw)
				continue
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("%s=%q: error %q does not name the setting", name, raw, err)
			}
		}
	}
}

// A project list is validated as project keys, not as the shared key pattern:
// a value that can never name a Jira project must fail at startup rather than
// load cleanly and deny every read at runtime. Space lists keep the wider
// pattern, because a personal space key really does start with a tilde.
func TestLoadValidatesProjectListsAsProjectKeys(t *testing.T) {
	for _, name := range []string{"ATLAS_READ_PROJECTS", "ATLAS_WRITE_PROJECTS"} {
		for _, raw := range []string{"~alice", "1DEV", "DEV,~bob"} {
			if _, err := Load(env(map[string]string{
				"ATLAS_BASE_URL": "https://x.atlassian.net",
				"ATLAS_EMAIL":    "a@b.c",
				"ATLAS_TOKEN":    fixtureToken,
				name:             raw,
			}), []string{"jira", "confluence"}); err == nil {
				t.Errorf("%s=%q must be rejected", name, raw)
			}
		}
	}
	for _, name := range []string{"ATLAS_READ_SPACES", "ATLAS_WRITE_SPACES"} {
		for _, raw := range []string{"~alice", "1DOCS", "ENG,~bob"} {
			if _, err := Load(env(map[string]string{
				"ATLAS_BASE_URL": "https://x.atlassian.net",
				"ATLAS_EMAIL":    "a@b.c",
				"ATLAS_TOKEN":    fixtureToken,
				name:             raw,
			}), []string{"jira", "confluence"}); err != nil {
				t.Errorf("%s=%q must be accepted: %v", name, raw, err)
			}
		}
	}
}

func TestReadAllowlistMembershipIsCaseInsensitiveAndIndependentOfWrites(t *testing.T) {
	cfg := loadWith(t, map[string]string{
		"ATLAS_READ_PROJECTS":  "DEV,PLATFORM,INFRA",
		"ATLAS_WRITE_PROJECTS": "DEV,PLATFORM",
		"ATLAS_READ_SPACES":    "ENG,ARCHITECTURE,DOCS",
		"ATLAS_WRITE_SPACES":   "ENG",
	})
	for _, c := range []struct {
		key                          string
		readable, writable           bool
		spaceKey                     string
		spaceReadable, spaceWritable bool
	}{
		{"DEV", true, true, "ENG", true, true},
		{"infra", true, false, "docs", true, false},
		{"SECRET", false, false, "SECRET", false, false},
	} {
		if got := cfg.AllowReadProject(c.key); got != c.readable {
			t.Errorf("AllowReadProject(%q) = %v, want %v", c.key, got, c.readable)
		}
		if got := cfg.AllowProject(c.key); got != c.writable {
			t.Errorf("AllowProject(%q) = %v, want %v", c.key, got, c.writable)
		}
		if got := cfg.AllowReadSpace(c.spaceKey); got != c.spaceReadable {
			t.Errorf("AllowReadSpace(%q) = %v, want %v", c.spaceKey, got, c.spaceReadable)
		}
		if got := cfg.AllowSpace(c.spaceKey); got != c.spaceWritable {
			t.Errorf("AllowSpace(%q) = %v, want %v", c.spaceKey, got, c.spaceWritable)
		}
	}
}
