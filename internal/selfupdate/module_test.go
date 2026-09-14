//go:build selfupdate

package selfupdate

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
)

// unreachable points every base URL at a server that fails the test if it is
// contacted. It is how "this refusal happens before the network" is asserted as
// a fact rather than assumed from reading the code.
func unreachable(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("an outbound request was made to %s", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	prevAPI, prevDownload := apiBaseURL, downloadBaseURL
	apiBaseURL, downloadBaseURL = srv.URL, srv.URL
	t.Cleanup(func() { apiBaseURL, downloadBaseURL = prevAPI, prevDownload })
	allowHosts(t, hostOf(t, srv.URL))
}

// useVersion stamps the running build's version for one test.
func useVersion(t *testing.T, v string) {
	t.Helper()
	prev := core.Version
	core.Version = v
	t.Cleanup(func() { core.Version = prev })
}

// ed25519Sign is a thin alias so the test reads as "sign the manifest".
func ed25519Sign(priv ed25519.PrivateKey, msg []byte) []byte { return ed25519.Sign(priv, msg) }

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

func decl(t *testing.T) core.ToolDecl {
	t.Helper()
	tools := New().Tools()
	if len(tools) != 1 {
		t.Fatalf("the module declares %d tools, want exactly one", len(tools))
	}
	return tools[0]
}

func TestModuleDeclaresOneSelfUpdateToolInItsOwnDomain(t *testing.T) {
	if New().Domain() != "selfupdate" {
		t.Errorf("domain = %q, want selfupdate", New().Domain())
	}
	d := decl(t)
	if d.Name != "self_update" {
		t.Errorf("name = %q, want self_update", d.Name)
	}
	// Exactly one class, and its own. Because the tool spans a single class,
	// core.Registry.Enabled is an exact gate: the tool does not exist at
	// runtime unless ATLAS_SELFUPDATE is on.
	if len(d.Actions) != 1 || d.Actions[0] != core.ActionSelfUpdate {
		t.Errorf("actions = %v, want [selfupdate]", d.Actions)
	}

	// The gating is exercised through the registry rather than asserted about
	// the declaration, because the registry is what decides.
	reg := &core.Registry{}
	reg.Register(New())
	// Every other capability at once, including destructive: none of them may
	// reach this tool. That is the whole point of the separate class — "may
	// replace my own binary" must not be a side effect of "may reassign an
	// issue".
	cfg := core.Config{Domains: map[string]core.Caps{Domain: {Read: true, Write: true, Destructive: true}}}
	if got := reg.Enabled(cfg); len(got) != 0 {
		t.Errorf("read+write+destructive enabled the tool: %v", got)
	}
	cfg = core.Config{Domains: map[string]core.Caps{Domain: {SelfUpdate: true}}}
	if got := reg.Enabled(cfg); len(got) != 1 {
		t.Errorf("selfupdate did not enable the tool: %v", got)
	}
}

func TestToolSchemaIsClosedAndTakesNoArguments(t *testing.T) {
	schema := decl(t).Schema(core.Caps{Read: true, Write: true, Destructive: true, SelfUpdate: true})
	if schema == nil {
		t.Fatal("nil schema")
	}
	if schema.Type != "object" {
		t.Errorf("type = %q, want object", schema.Type)
	}
	// No properties at all: no version argument, so no way to ask for an older
	// release with a known vulnerability, and no path or URL for the surface
	// guard to have to reason about.
	if len(schema.Properties) != 0 {
		t.Errorf("schema declares %d properties, want none", len(schema.Properties))
	}
	if len(schema.Required) != 0 {
		t.Errorf("schema requires %v, want nothing", schema.Required)
	}
	if schema.AdditionalProperties == nil || schema.AdditionalProperties.Not == nil {
		t.Error("additionalProperties must be the closed {\"not\":{}} form")
	}
}

// TestToolDescriptionCarriesTheNotAuthorizedSentence: the attack is a page or
// comment that asks the model to update the server, and the tool description is
// the last thing the model reads before it decides to call one.
func TestToolDescriptionCarriesTheNotAuthorizedSentence(t *testing.T) {
	d := decl(t)
	if !strings.HasSuffix(d.Description, notAuthorizedNotice) {
		t.Errorf("description does not end with the not-authorized sentence: %q", d.Description)
	}
	if !containsAll(d.Description, "Destructive", "restart", "container") {
		t.Errorf("description does not state what the tool does: %q", d.Description)
	}
}

// TestHandlerRefusesWithoutASigningKeyAndBeforeTheNetwork covers a build whose
// releasePublicKey is empty — a tree where the constant was cleared, not the
// one this repository ships. Such a build must refuse, and must do so before a
// single byte goes out.
func TestHandlerRefusesWithoutASigningKey(t *testing.T) {
	useEmptyKey(t)
	useVersion(t, "v0.1.0")
	noContainer(t)
	unreachable(t)

	_, err := NewWith(quietLogger()).Tools()[0].Handle(t.Context(), nil)
	if err == nil {
		t.Fatal("an unsigned update path was allowed")
	}
	if !strings.Contains(err.Error(), "no release signing key") {
		t.Errorf("err = %v, want the missing-key refusal", err)
	}
}

// TestHandlerRefusesWithoutALogger mirrors the product modules' nil-client
// check: a declaration-only module must not silently do half the work.
func TestHandlerRefusesWithoutALogger(t *testing.T) {
	if _, err := New().Tools()[0].Handle(t.Context(), nil); err == nil {
		t.Fatal("the declaration-only module ran the handler")
	}
}

func TestHandlerReportsCurrentWhenTheLatestTagIsTheRunningVersion(t *testing.T) {
	useTestKey(t)
	useVersion(t, "0.1.0") // the unprefixed spelling core.Version defaults to
	noContainer(t)

	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{"tag_name":"v0.1.0"}`))
	}))
	defer srv.Close()
	prevAPI, prevDownload := apiBaseURL, downloadBaseURL
	apiBaseURL, downloadBaseURL = srv.URL, srv.URL
	defer func() { apiBaseURL, downloadBaseURL = prevAPI, prevDownload }()
	allowHosts(t, hostOf(t, srv.URL))

	out, err := NewWith(quietLogger()).Tools()[0].Handle(t.Context(), nil)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}

	// Nothing beyond the release lookup was fetched: an up-to-date server must
	// not download its own binary to discover it already has it.
	if len(paths) != 1 {
		t.Errorf("requests = %v, want only the release lookup", paths)
	}

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["status"] != "current" {
		t.Errorf("status = %v, want current", got["status"])
	}
	if got["running"] != "v0.1.0" {
		t.Errorf("running = %v, want v0.1.0", got["running"])
	}
	if _, present := got["staged"]; present {
		t.Errorf("a current result carries a staged version: %s", raw)
	}
	if got["restart_required"] != false {
		t.Errorf("restart_required = %v, want false", got["restart_required"])
	}
	if got["backup_kept"] != false {
		t.Errorf("backup_kept = %v, want false", got["backup_kept"])
	}
}

// TestResultCarriesNothingButVersionsAndStatus is the payload contract. The
// model must not learn where this binary lives, what the release asset is
// called, or anything GitHub built out of pull request titles.
func TestResultCarriesNothingButVersionsAndStatus(t *testing.T) {
	raw, err := json.Marshal(result{
		Status:          "staged",
		Running:         "v0.2.1",
		Staged:          "v0.3.0",
		BackupKept:      true,
		RestartRequired: true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"status":"staged","running":"v0.2.1","staged":"v0.3.0","backup_kept":true,"restart_required":true}`
	if string(raw) != want {
		t.Errorf("result = %s, want %s", raw, want)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	allowed := map[string]bool{"status": true, "running": true, "staged": true, "backup_kept": true, "restart_required": true}
	for name := range fields {
		if !allowed[name] {
			t.Errorf("result carries an unexpected field %q", name)
		}
	}
}

// TestTheBackupPathGoesToTheLoggerAndNotToTheResult is the other half of that
// contract: the operator needs the path to roll back, and the model must not
// have it.
func TestTheBackupPathGoesToTheLoggerAndNotToTheResult(t *testing.T) {
	exe := fakeExecutable(t, []byte("the old binary"))
	var stderr bytes.Buffer
	log := core.NewLogger("info", &stderr)

	backup, err := applyUpdate([]byte("the new binary"), "v0.1.0", log)
	if err != nil {
		t.Fatalf("applyUpdate: %v", err)
	}
	log.Error(toolName + ": the previous binary is kept at " + backup)

	if !strings.Contains(stderr.String(), backup) {
		t.Errorf("stderr does not carry the backup path: %q", stderr.String())
	}

	raw, err := json.Marshal(result{Status: "staged", Running: "v0.1.0", Staged: "v0.2.0", BackupKept: true, RestartRequired: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leak := range []string{exe, backup, assetName()} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the result carries %q", leak)
		}
	}
}

// TestHandlerStagesAVerifiedRelease walks the whole path once with a fake
// release: lookup, manifest, signature, asset, hash, swap. Each piece has its
// own test above; this one exists because the ordering between them is itself a
// security property — nothing touches the filesystem until the signature and
// the digest have both been checked.
func TestHandlerStagesAVerifiedRelease(t *testing.T) {
	priv := useTestKey(t)
	useVersion(t, "v0.1.0")
	noContainer(t)

	newBinary := []byte("the new binary, signed and hashed")
	manifest := []byte(
		sumsLine("atlassian-mcp-lite_linux_amd64", []byte("the default variant")) +
			sumsLine(assetName(), newBinary),
	)
	sig := ed25519Sign(priv, manifest)

	var fetched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched = append(fetched, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			// The document also offers a destination of its own, which this
			// package ignores in favour of a locally built URL.
			_, _ = w.Write([]byte(`{"tag_name":"v0.2.0","browser_download_url":"https://attacker.example/payload"}`))
		case strings.HasSuffix(r.URL.Path, "/SHA256SUMS.sig"):
			_, _ = w.Write(sig)
		case strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
			_, _ = w.Write(manifest)
		case strings.HasSuffix(r.URL.Path, assetName()):
			_, _ = w.Write(newBinary)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	prevAPI, prevDownload := apiBaseURL, downloadBaseURL
	apiBaseURL, downloadBaseURL = srv.URL, srv.URL+"/releases/download"
	defer func() { apiBaseURL, downloadBaseURL = prevAPI, prevDownload }()
	allowHosts(t, hostOf(t, srv.URL))

	exe := fakeExecutable(t, []byte("the old binary"))

	var stderr bytes.Buffer
	out, err := NewWith(core.NewLogger("info", &stderr)).Tools()[0].Handle(t.Context(), nil)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"status":"staged","running":"v0.1.0","staged":"v0.2.0","backup_kept":true,"restart_required":true}`
	if string(raw) != want {
		t.Errorf("result = %s, want %s", raw, want)
	}

	got, err := os.ReadFile(exe) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatalf("read executable: %v", err)
	}
	if !bytes.Equal(got, newBinary) {
		t.Errorf("executable = %q, want the new binary", got)
	}
	kept, err := os.ReadFile(exe + ".v0.1.0.bak") //nolint:gosec // a path this test created
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !bytes.Equal(kept, []byte("the old binary")) {
		t.Errorf("backup = %q, want the previous binary", kept)
	}
	if !strings.Contains(stderr.String(), exe+".v0.1.0.bak") {
		t.Errorf("stderr does not name the backup: %q", stderr.String())
	}

	// Four requests, none of them to a host named in the release document.
	if len(fetched) != 4 {
		t.Errorf("requests = %v, want four", fetched)
	}

	// A tampered asset must not reach the disk. The same server, one byte of
	// the binary changed, and the signed manifest unchanged.
	exe2 := fakeExecutable(t, []byte("the old binary"))
	newBinary = append([]byte("X"), newBinary[1:]...)
	if _, err := NewWith(quietLogger()).Tools()[0].Handle(t.Context(), nil); err == nil {
		t.Fatal("an asset that did not match its signed checksum was installed")
	}
	got2, err := os.ReadFile(exe2) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatalf("read executable: %v", err)
	}
	if !bytes.Equal(got2, []byte("the old binary")) {
		t.Errorf("a failed verification still replaced the binary: %q", got2)
	}
	noStagingLeftovers(t, filepath.Dir(exe2))
}
