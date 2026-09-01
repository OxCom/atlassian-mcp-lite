// Package core provides the generic MCP provider: configuration, tool
// registration and gating, HTTP access, field selection and logging.
// Product modules declare tools; core decides what is registered.
package core

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Caps is the set of action classes enabled for one domain.
type Caps struct {
	Read        bool
	Write       bool
	Destructive bool
}

// Any reports whether the domain has any capability at all.
func (c Caps) Any() bool { return c.Read || c.Write || c.Destructive }

// Config is the fully resolved server configuration.
type Config struct {
	BaseURL string
	Email   string
	Token   string

	Domains map[string]Caps

	WriteProjects []string
	WriteSpaces   []string

	LogLevel     string
	LimitDefault int
	LimitMax     int

	// EpicFieldID is site-specific: the Epic Link custom field id differs
	// between Jira sites, so it is configurable rather than a constant.
	EpicFieldID string
}

// Load resolves configuration for the given domains. Capability env vars are
// derived from each domain name, so registering a new module needs no change
// here.
func Load(getenv func(string) string, domains []string) (Config, error) {
	cfg := Config{
		BaseURL:      strings.TrimRight(strings.TrimSpace(getenv("ATLAS_BASE_URL")), "/"),
		Email:        strings.TrimSpace(getenv("ATLAS_EMAIL")),
		Token:        strings.TrimSpace(getenv("ATLAS_TOKEN")),
		Domains:      make(map[string]Caps, len(domains)),
		LogLevel:     strings.ToLower(orDefault(getenv("ATLAS_LOG"), "info")),
		LimitDefault: intOrDefault(getenv("ATLAS_LIMIT_DEFAULT"), 20),
		LimitMax:     intOrDefault(getenv("ATLAS_LIMIT_MAX"), 50),
		EpicFieldID:  orDefault(getenv("ATLAS_EPIC_FIELD_ID"), "customfield_10014"),
	}

	for _, required := range []struct {
		name, val string
	}{
		{"ATLAS_BASE_URL", cfg.BaseURL},
		{"ATLAS_EMAIL", cfg.Email},
		{"ATLAS_TOKEN", cfg.Token},
	} {
		if required.val == "" {
			return Config{}, fmt.Errorf("%s is required", required.name)
		}
	}

	// ATLAS_LOG is a closed enum. An unknown value would leave the level
	// undefined for whichever component consumes it, so it fails at load.
	switch cfg.LogLevel {
	case "info", "debug":
	default:
		return Config{}, fmt.Errorf("ATLAS_LOG: %q is not a known level; use info or debug", cfg.LogLevel)
	}

	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return Config{}, fmt.Errorf("ATLAS_BASE_URL: %w", err)
	}

	for _, d := range domains {
		prefix := "ATLAS_" + strings.ToUpper(d) + "_"
		cfg.Domains[d] = Caps{
			Read:        isTrue(getenv(prefix + "READ")),
			Write:       isTrue(getenv(prefix + "WRITE")),
			Destructive: isTrue(getenv(prefix + "DESTRUCTIVE")),
		}
	}

	for _, list := range []struct {
		name string
		dst  *[]string
	}{
		{"ATLAS_WRITE_PROJECTS", &cfg.WriteProjects},
		{"ATLAS_WRITE_SPACES", &cfg.WriteSpaces},
	} {
		raw := getenv(list.name)
		*list.dst = splitList(raw)
		// Unset and empty both mean unrestricted. A value that is neither, but
		// yields no keys (","  or "  ,  "), expresses intent to restrict and
		// would silently allow everything, so it is rejected instead.
		if strings.TrimSpace(raw) != "" && len(*list.dst) == 0 {
			return Config{}, fmt.Errorf("%s is set but contains no keys", list.name)
		}
	}

	if cfg.LimitDefault > cfg.LimitMax {
		cfg.LimitDefault = cfg.LimitMax
	}
	return cfg, nil
}

// validateBaseURL rejects a base URL that would leak credentials or break path
// concatenation. Failing here beats failing on the first request, and an http
// URL would send Basic credentials in clear text.
//
// Loopback hosts may use http so tests can point at an httptest server.
func validateBaseURL(raw string) error {
	// A bare trailing "#" leaves Fragment empty, so url.Parse records nothing
	// to check: only the raw string still carries the delimiter. A bare "?"
	// sets ForceQuery instead. Either one would turn a path appended later into
	// a fragment or a query.
	if strings.Contains(raw, "#") {
		return errors.New("must be an origin only, with no fragment")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a valid URL: %w", err)
	}
	if u.Host == "" {
		return fmt.Errorf("no host in %q", raw)
	}
	if u.User != nil {
		return errors.New("must not contain credentials in the URL")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("must be an origin only, with no query or fragment")
	}
	if u.Path != "" {
		return fmt.Errorf("must be an origin only, but has path %q", u.Path)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopback(u.Hostname()) {
			return nil
		}
		return errors.New("http is only allowed for loopback hosts; Basic credentials must not travel in clear text")
	default:
		return fmt.Errorf("scheme %q is not supported; use https", u.Scheme)
	}
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// AllowProject reports whether writes to key are permitted. An unset or empty
// allowlist means unrestricted; a non-empty one is strict.
func (c Config) AllowProject(key string) bool { return allowed(c.WriteProjects, key) }

// AllowSpace reports whether writes to a Confluence space key are permitted.
func (c Config) AllowSpace(key string) bool { return allowed(c.WriteSpaces, key) }

func allowed(list []string, key string) bool {
	if len(list) == 0 {
		return true
	}
	key = strings.TrimSpace(key)
	for _, e := range list {
		if strings.EqualFold(e, key) {
			return true
		}
	}
	return false
}

func isTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func orDefault(s, def string) string {
	if s = strings.TrimSpace(s); s == "" {
		return def
	}
	return s
}

// intOrDefault parses a positive decimal integer, falling back to def for
// anything else. Parsing is strict: a value like "20x" is a misconfiguration,
// not a 20.
func intOrDefault(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return def
	}
	return n
}
