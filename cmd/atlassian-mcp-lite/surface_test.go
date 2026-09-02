package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/OxCom/atlassian-mcp-lite/internal/confluence"
	"github.com/OxCom/atlassian-mcp-lite/internal/core"
	"github.com/OxCom/atlassian-mcp-lite/internal/jira"
)

// This file guards two properties of the whole binary rather than of one
// package: no tool argument names a local file or an outbound destination, and
// no code outside the config loader opens a file, runs a command or listens on
// a socket.
//
// Both are regressions against sooperset/mcp-atlassian, the Python MCP server
// for the same two products, which published thirty-six advisories between
// March and August 2026. The two largest families are exactly these:
//
//   - Caller-chosen paths. CVE-2026-27825 (Critical 9.0) let download_path
//     write anywhere on disk — cron files, authorized_keys, the server's own
//     source — and CVE-2026-77271 was its incomplete fix, which defaulted the
//     base directory to the working directory and so left the package
//     importable-and-writable in a container. CVE-2026-77255 and twelve
//     independently reported duplicates were the read direction: a path list
//     passed to update_issue made the server read /proc/self/environ, SSH keys
//     and its own Atlassian token and publish them as an issue attachment.
//   - Caller-chosen destinations. CVE-2026-27826, CVE-2026-73497,
//     CVE-2026-77274, CVE-2026-77261 and CVE-2026-77245 all end at an outbound
//     request to a host the caller named, reaching the cloud metadata service.
//
// The common root is that an MCP server holds authority the agent driving it
// does not, so any parameter naming a local path or a URL turns the server into
// a confused deputy for whoever can write into a Jira issue or a Confluence
// page. This binary removes the parameter rather than validating it, because
// upstream's path validator needed three attempts and its URL validator five.

// forbiddenPropertyNames are argument names no tool may declare, whatever it
// does with them.
var forbiddenPropertyNames = map[string]string{
	"path":          "a caller-chosen filesystem path",
	"file":          "a caller-chosen filesystem path",
	"file_path":     "a caller-chosen filesystem path",
	"filepath":      "a caller-chosen filesystem path",
	"local_path":    "a caller-chosen filesystem path",
	"content_file":  "a caller-chosen filesystem path",
	"dir":           "a caller-chosen directory",
	"directory":     "a caller-chosen directory",
	"output_path":   "a caller-chosen write destination",
	"download_path": "a caller-chosen write destination (CVE-2026-27825)",
	"destination":   "a caller-chosen write destination",
	"url":           "a caller-chosen outbound destination (SSRF)",
	"uri":           "a caller-chosen outbound destination (SSRF)",
	"href":          "a caller-chosen outbound destination (SSRF)",
	"endpoint":      "a caller-chosen outbound destination (SSRF)",
	"base_url":      "a caller-chosen outbound destination (SSRF)",
	"host":          "a caller-chosen outbound destination (SSRF)",
	"icon_url":      "a caller-chosen outbound destination (CVE-2026-77245)",
	"attachments":   "the parameter CVE-2026-77255 was reported against; attachment content is base64, not a path list",
}

// pathPhrases catch the same defect under a name the map above does not list:
// a property called "source" whose description says "path to a local file".
var pathPhrases = []string{"path to", "local file", "on disk", "file path", "absolute path"}

// TestNoToolAcceptsAPathOrAURL walks every tool of every module at full
// capability, so a property that appears only when write or destructive is
// enabled is checked too.
func TestNoToolAcceptsAPathOrAURL(t *testing.T) {
	all := core.Caps{Read: true, Write: true, Destructive: true}
	for _, m := range []core.Module{jira.New(), confluence.New()} {
		for _, decl := range m.Tools() {
			schema := decl.Schema(all)
			if schema == nil {
				t.Errorf("%s: nil schema", decl.Name)
				continue
			}
			walkSchema(t, decl.Name, "", schema)
		}
	}
}

func walkSchema(t *testing.T, tool, prefix string, s *jsonschema.Schema) {
	t.Helper()
	for name, prop := range s.Properties {
		full := prefix + name
		if why, bad := forbiddenPropertyNames[strings.ToLower(name)]; bad {
			t.Errorf("%s declares property %q: %s", tool, full, why)
		}
		lower := strings.ToLower(prop.Description)
		for _, phrase := range pathPhrases {
			if strings.Contains(lower, phrase) {
				t.Errorf("%s property %q describes %q; no tool may name a local file", tool, full, phrase)
			}
		}
		if prop.Items != nil {
			walkSchema(t, tool, full+"[].", prop.Items)
		}
		walkSchema(t, tool, full+".", prop)
	}
}

// TestEveryToolSchemaIsClosed keeps additionalProperties false on every tool.
// An open schema lets an argument the handler never validated ride along, which
// is how upstream's icon_url reached a server-side fetch (CVE-2026-77245).
func TestEveryToolSchemaIsClosed(t *testing.T) {
	all := core.Caps{Read: true, Write: true, Destructive: true}
	for _, m := range []core.Module{jira.New(), confluence.New()} {
		for _, decl := range m.Tools() {
			schema := decl.Schema(all)
			// A permissive {} is also non-nil and accepts everything, so the
			// "not: {}" shape core.ObjectSchema produces is what is checked.
			if schema.AdditionalProperties == nil || schema.AdditionalProperties.Not == nil {
				t.Errorf("%s: additionalProperties must be the closed {\"not\":{}} form", decl.Name)
			}
		}
	}
}

// forbiddenCalls are identifiers that must not appear in non-test source.
//
// Each removes a class rather than a bug. No file is opened, so there is no
// path to traverse; no command is run, so there is nothing to inject into; no
// socket is listened on, so there is no unauthenticated transport to bypass —
// upstream's GHSA-5j8j-256g-vvp5 (CVSS 10.0) was an auth middleware that
// path-matched one transport and left the other serving anonymous callers with
// the operator's credentials, and CVE-2026-77254 was an HTTP server bound to
// 0.0.0.0 that fell back to those credentials. A stdio server cannot have
// either defect.
var forbiddenCalls = map[string]string{
	"os.Open":             "opening a file (CVE-2026-77255 class)",
	"os.OpenFile":         "opening a file (CVE-2026-77255 class)",
	"os.ReadFile":         "reading a file (CVE-2026-77255 class)",
	"os.WriteFile":        "writing a file (CVE-2026-27825 class)",
	"os.Create":           "creating a file (CVE-2026-27825 class)",
	"os.Remove":           "deleting a file",
	"os.MkdirAll":         "creating directories",
	"exec.Command":        "running a subprocess",
	"exec.CommandContext": "running a subprocess",
	"net.Listen":          "listening on a socket (GHSA-5j8j-256g-vvp5 class)",
	"ListenAndServe":      "listening on a socket (CVE-2026-77254 class)",
	"http.Handle":         "serving HTTP",
	"http.HandleFunc":     "serving HTTP",
}

// allowedFileAccess names the one place a file is opened, and why.
//
// internal/core/envfile.go reads the config file. That path is named by the
// operator in ATLAS_ENV_FILE, never by a tool argument, and the file must be a
// regular file owned by the effective uid with mode 0600 or 0400 before a byte
// of it is read.
var allowedFileAccess = map[string]bool{
	filepath.Join("internal", "core", "envfile.go"): true,
}

// TestNoSourceFileOpensAFileOrListens scans the repository's own Go source.
//
// It is a text scan on purpose. A type-aware check would follow the import
// graph and pass a call reached through an alias or an interface; what this
// guards is the simpler and more useful property that the identifier does not
// appear at all, so a reviewer grepping for it finds nothing and a new call
// fails a test that explains itself.
func TestNoSourceFileOpensAFileOrListens(t *testing.T) {
	root := moduleRoot(t)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "dist", "docs", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		body, readErr := os.ReadFile(path) //nolint:gosec // the test walks its own repository
		if readErr != nil {
			return readErr
		}
		for call, why := range forbiddenCalls {
			if !strings.Contains(string(body), call) {
				continue
			}
			if strings.HasPrefix(call, "os.") && allowedFileAccess[rel] {
				continue
			}
			t.Errorf("%s contains %s: %s", rel, call, why)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// moduleRoot returns the repository root, found by walking up from this test's
// own directory until go.mod appears.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}
