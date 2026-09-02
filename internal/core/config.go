// Package core provides the generic MCP provider: configuration, tool
// registration and gating, HTTP access, field selection and logging.
// Product modules declare tools; core decides what is registered.
package core

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Validation patterns. Every configured value that later becomes part of a URL
// path, a query string or a JSON field name is checked against a strict
// allowlist here, at the trust boundary, rather than at the point of use.
var (
	// A domain name becomes part of an environment variable name, so it must
	// contain nothing that cannot appear in one.
	domainRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	// Jira project keys and Confluence space keys travel in URL paths and in
	// JQL/CQL. Confluence personal spaces are prefixed with "~".
	keyRe = regexp.MustCompile(`^~?[A-Za-z0-9_]+$`)
	// A Jira field id is a JSON object key and a query-string value.
	fieldIDRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
)

// minTokenRunes is the shortest ATLAS_TOKEN accepted. Atlassian API tokens are
// far longer, so anything under this is a placeholder or a truncated paste,
// and a placeholder that passes validation fails only on the first request
// with a 401 that names nothing. It is also what lets the logger's
// minRedactableSecret floor stay safe: no real credential can be shorter than
// this, so none is ever dropped from redaction.
const minTokenRunes = 16

// limitCeiling bounds ATLAS_LIMIT_MAX. Atlassian pages results, so an
// unbounded limit is a request to buffer an unbounded response.
const limitCeiling = 1000

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
		BaseURL:     strings.TrimRight(strings.TrimSpace(getenv("ATLAS_BASE_URL")), "/"),
		Email:       strings.TrimSpace(getenv("ATLAS_EMAIL")),
		Token:       strings.TrimSpace(getenv("ATLAS_TOKEN")),
		Domains:     make(map[string]Caps, len(domains)),
		LogLevel:    strings.ToLower(orDefault(getenv("ATLAS_LOG"), "info")),
		EpicFieldID: orDefault(getenv("ATLAS_EPIC_FIELD_ID"), "customfield_10014"),
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

	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return Config{}, fmt.Errorf("ATLAS_BASE_URL: %w", err)
	}
	if err := validateEmail(cfg.Email); err != nil {
		return Config{}, fmt.Errorf("ATLAS_EMAIL: %w", err)
	}
	// The token itself is never echoed in an error: a value that reached a log
	// through a diagnostic message would defeat the masking the spec requires.
	if err := validateToken(cfg.Token); err != nil {
		return Config{}, fmt.Errorf("ATLAS_TOKEN: %w", err)
	}

	// ATLAS_LOG is a closed enum. An unknown value would leave the level
	// undefined for whichever component consumes it, so it fails at load.
	switch cfg.LogLevel {
	case "info", "debug":
	default:
		return Config{}, fmt.Errorf("ATLAS_LOG: %q is not a known level; use info or debug", cfg.LogLevel)
	}

	if !fieldIDRe.MatchString(cfg.EpicFieldID) {
		return Config{}, fmt.Errorf("ATLAS_EPIC_FIELD_ID: %q is not a valid field id; expected a name such as customfield_10014", cfg.EpicFieldID)
	}

	seen := make(map[string]struct{}, len(domains))
	for _, d := range domains {
		if !domainRe.MatchString(d) {
			return Config{}, fmt.Errorf("domain %q is not a valid domain name; expected lowercase letters, digits and underscores", d)
		}
		if _, dup := seen[d]; dup {
			return Config{}, fmt.Errorf("domain %q is registered twice", d)
		}
		seen[d] = struct{}{}

		prefix := "ATLAS_" + strings.ToUpper(d) + "_"
		caps := Caps{}
		// Read is on unless the operator turns it off; write and destructive
		// are off unless turned on. Reading changes nothing, so an unset
		// installation is useful out of the box while still unable to modify
		// anything.
		for _, flag := range []struct {
			suffix string
			dst    *bool
			def    bool
		}{
			{"READ", &caps.Read, true},
			{"WRITE", &caps.Write, false},
			{"DESTRUCTIVE", &caps.Destructive, false},
		} {
			// A typo such as "ture" must not quietly mean false: it would
			// disable a capability the operator believes is on, or — read the
			// other way round in a future refactor — enable one they believe is
			// off. Either way the operator gets no signal, so it fails at load.
			v, err := parseBool(getenv(prefix+flag.suffix), flag.def)
			if err != nil {
				return Config{}, fmt.Errorf("%s%s: %w", prefix, flag.suffix, err)
			}
			*flag.dst = v
		}
		cfg.Domains[d] = caps
	}

	for _, list := range []struct {
		name string
		dst  *[]string
	}{
		{"ATLAS_WRITE_PROJECTS", &cfg.WriteProjects},
		{"ATLAS_WRITE_SPACES", &cfg.WriteSpaces},
	} {
		raw := getenv(list.name)
		keys := splitList(raw)
		// Unset and empty both mean unrestricted. A value that is neither, but
		// yields no keys ("," or " , "), expresses intent to restrict and would
		// silently allow everything, so it is rejected instead.
		if strings.TrimSpace(raw) != "" && len(keys) == 0 {
			return Config{}, fmt.Errorf("%s is set but contains no keys", list.name)
		}
		for _, k := range keys {
			// A key reaches a URL path and a JQL/CQL clause. Anything outside
			// the allowlist could only get there as an injection attempt or a
			// typo, and an allowlist entry that never matches a real key would
			// fail closed silently.
			if !keyRe.MatchString(k) {
				return Config{}, fmt.Errorf("%s: %q is not a valid key", list.name, k)
			}
		}
		*list.dst = keys
	}

	var err error
	if cfg.LimitDefault, err = positiveInt(getenv("ATLAS_LIMIT_DEFAULT"), 20); err != nil {
		return Config{}, fmt.Errorf("ATLAS_LIMIT_DEFAULT: %w", err)
	}
	if cfg.LimitMax, err = positiveInt(getenv("ATLAS_LIMIT_MAX"), 50); err != nil {
		return Config{}, fmt.Errorf("ATLAS_LIMIT_MAX: %w", err)
	}
	if cfg.LimitMax > limitCeiling {
		return Config{}, fmt.Errorf("ATLAS_LIMIT_MAX: %d exceeds the ceiling of %d", cfg.LimitMax, limitCeiling)
	}
	// Silently clamping would hand back a configuration the operator did not
	// write, and the mismatch is a plain contradiction, so it is an error.
	if cfg.LimitDefault > cfg.LimitMax {
		return Config{}, fmt.Errorf("ATLAS_LIMIT_DEFAULT (%d) must not exceed ATLAS_LIMIT_MAX (%d)", cfg.LimitDefault, cfg.LimitMax)
	}
	return cfg, nil
}

// validateEmail checks the address is a single parseable mailbox with no
// display name. The address becomes the user half of an HTTP Basic credential,
// so a control character or a colon in it would corrupt that credential.
func validateEmail(raw string) error {
	if strings.ContainsAny(raw, ":") {
		return errors.New("must not contain a colon; it is the HTTP Basic separator")
	}
	if err := hasNoControlChars(raw); err != nil {
		return err
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil {
		return fmt.Errorf("not a valid email address: %w", err)
	}
	if addr.Name != "" || addr.Address != raw {
		return fmt.Errorf("must be a bare address, not %q", raw)
	}
	if !strings.Contains(addr.Address, "@") {
		return errors.New("must contain a domain")
	}
	return nil
}

// validateToken checks only structure, never content, and its errors never
// quote the value.
func validateToken(raw string) error {
	if err := hasNoControlChars(raw); err != nil {
		return err
	}
	if strings.ContainsAny(raw, " \t") {
		return errors.New("must not contain whitespace")
	}
	// Counted in runes so the rule matches how the operator counts characters.
	// The count is deliberately not in the message: it would be one more fact
	// about the credential in a log.
	if utf8.RuneCountInString(raw) < minTokenRunes {
		return fmt.Errorf("must be at least %d characters", minTokenRunes)
	}
	return nil
}

// hasNoControlChars rejects C0 and C1 control characters, which have no place
// in a credential or a header value.
func hasNoControlChars(raw string) error {
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("must not contain control character %#U", r)
		}
	}
	return nil
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

// RestrictsProjects and RestrictsSpaces report whether an allowlist is in force
// at all. A module uses these to decide whether an extra verification round
// trip is worth making: with no allowlist there is nothing to verify against,
// and an unrestricted deployment should not pay for a check it never fails.
func (c Config) RestrictsProjects() bool { return len(c.WriteProjects) > 0 }

// RestrictsSpaces reports whether a Confluence space allowlist is in force.
func (c Config) RestrictsSpaces() bool { return len(c.WriteSpaces) > 0 }

// ClampLimit applies the configured default and hard cap to a caller's
// requested result count. Zero or negative means "unspecified", which is the
// default rather than an error, because the limit is a convenience and a
// missing one should not fail a call.
//
// This lives in core because the two values it reads are core's: leaving each
// product module to clamp for itself meant the same six lines in both, and a
// limit policy that can drift apart is not a policy.
func (c Config) ClampLimit(requested int) int {
	if requested <= 0 {
		return c.LimitDefault
	}
	if requested > c.LimitMax {
		return c.LimitMax
	}
	return requested
}

// BoundBytes rejects a free-form string longer than limit bytes, naming the
// field. Used where the point is to keep an unbounded payload off the wire.
func BoundBytes(field, value string, limit int) error {
	if len(value) > limit {
		return fmt.Errorf("%s is %d bytes, limit is %d", field, len(value), limit)
	}
	return nil
}

// BoundRunes rejects a string longer than limit characters. Used where
// Atlassian states a limit in characters: measuring those in bytes invents a
// rejection the server would never have made, refusing a valid CJK or accented
// value at a third of the real limit.
func BoundRunes(field, value string, limit int) error {
	if n := utf8.RuneCountInString(value); n > limit {
		return fmt.Errorf("%s is %d characters, limit is %d", field, n, limit)
	}
	return nil
}

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

// parseBool accepts the usual spellings of both truth values. An unset value
// yields def, but an unrecognised one is an error rather than a silent default.
func parseBool(s string, def bool) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return def, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%q is not a boolean; use true or false", s)
	}
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

// positiveInt parses a positive decimal integer, using def only when the value
// is unset. Anything set but unparsable is a misconfiguration, not a fallback:
// "20x" silently becoming 20 hides a typo in a value that bounds result sizes.
func positiveInt(s string, def int) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not an integer", s)
	}
	if n < 1 {
		return 0, fmt.Errorf("%d must be at least 1", n)
	}
	return n, nil
}
