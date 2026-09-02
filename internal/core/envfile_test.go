package core

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeEnvFile(t *testing.T, body string, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(path, []byte(body), perm); err != nil {
		t.Fatal(err)
	}
	// WriteFile's mode is subject to umask; force the bits the test asks for.
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
	return path
}

// lookupEnv has the shape of os.LookupEnv over a fixed map, so a test can set a
// variable to the empty string and have it count as set.
func lookupEnv(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLoadEnvFileParsesAndProcessEnvWins(t *testing.T) {
	path := writeEnvFile(t, strings.Join([]string{
		"# comment",
		"",
		"ATLAS_BASE_URL=https://x.atlassian.net",
		"export ATLAS_EMAIL = a@b.c ",
		`ATLAS_TOKEN="tok en"`,
		"ATLAS_JIRA_WRITE='true'",
		"ATLAS_JIRA_DESTRUCTIVE=true",
	}, "\n"), 0o600)

	getenv, err := LoadEnvFile(path, lookupEnv(map[string]string{
		"ATLAS_EMAIL": "override@b.c",
		// Set to empty in the process: must override the file's "true", not
		// fall through to it.
		"ATLAS_JIRA_DESTRUCTIVE": "",
	}))
	if err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	for k, want := range map[string]string{
		"ATLAS_BASE_URL":         "https://x.atlassian.net",
		"ATLAS_EMAIL":            "override@b.c",
		"ATLAS_TOKEN":            "tok en",
		"ATLAS_JIRA_WRITE":       "true",
		"ATLAS_JIRA_DESTRUCTIVE": "",
		"ATLAS_UNSET":            "",
	} {
		if got := getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestLoadEnvFileRefusesSharedPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not checked on Windows")
	}
	for _, perm := range []os.FileMode{
		0o644, 0o640, 0o604, 0o660, 0o700, 0o500,
		0o600 | os.ModeSetuid, 0o600 | os.ModeSetgid, 0o600 | os.ModeSticky,
	} {
		path := writeEnvFile(t, "ATLAS_TOKEN=t\n", perm)
		_, err := LoadEnvFile(path, lookupEnv(nil))
		if err == nil {
			t.Errorf("mode %o must be refused", perm)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "chmod 600 '"+path+"'") {
			t.Errorf("mode %o: error must name the fix; got %q", perm, msg)
		}
		if strings.Contains(msg, "ATLAS_TOKEN=t") {
			t.Errorf("mode %o: error must not echo the file contents", perm)
		}
	}
	for _, perm := range []os.FileMode{0o600, 0o400} {
		path := writeEnvFile(t, "ATLAS_TOKEN=t\n", perm)
		if _, err := LoadEnvFile(path, lookupEnv(nil)); err != nil {
			t.Errorf("mode %04o must be accepted: %v", perm, err)
		}
	}
}

// A path with a space or a quote must still yield a command that chmods that
// exact file when pasted into a shell.
func TestPermissionErrorQuotesThePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not checked on Windows")
	}
	dir := filepath.Join(t.TempDir(), "my config's dir")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "env")
	if err := os.WriteFile(path, []byte("A=b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadEnvFile(path, lookupEnv(nil))
	if err == nil {
		t.Fatal("0644 must be refused")
	}
	want := `chmod 600 '` + strings.ReplaceAll(path, "'", `'\''`) + `'`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), want)
	}
}

func TestLoadEnvFileRejectsMissingAndMalformed(t *testing.T) {
	if _, err := LoadEnvFile(filepath.Join(t.TempDir(), "absent"), lookupEnv(nil)); err == nil {
		t.Error("a missing file must be an error, not an empty configuration")
	}
	if _, err := LoadEnvFile(t.TempDir(), lookupEnv(nil)); err == nil {
		t.Error("a directory must be refused")
	}
	for _, body := range []string{
		"ATLAS_TOKEN\n",
		"=value\n",
		"BAD-NAME=x\n",
		"ATLAS_TOKEN=a\x01b\n",
	} {
		path := writeEnvFile(t, body, 0o600)
		if _, err := LoadEnvFile(path, lookupEnv(nil)); err == nil {
			t.Errorf("%q must be rejected", body)
		}
	}
}

func TestLoadEnvFileBoundsSize(t *testing.T) {
	path := writeEnvFile(t, "# "+strings.Repeat("x", maxEnvFileBytes)+"\n", 0o600)
	if _, err := LoadEnvFile(path, lookupEnv(nil)); err == nil {
		t.Error("a file over the size cap must be refused")
	}
}

// The end-to-end shape: a file with nothing but credentials yields a read-only
// server for every domain, through the same Load the binary uses.
func TestLoadEnvFileFeedsLoad(t *testing.T) {
	path := writeEnvFile(t, "ATLAS_BASE_URL=https://x.atlassian.net\nATLAS_EMAIL=a@b.c\nATLAS_TOKEN=t\n", 0o600)
	getenv, err := LoadEnvFile(path, lookupEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(getenv, []string{"jira", "confluence"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Domains["jira"].Read || cfg.Domains["jira"].Write || !cfg.Domains["confluence"].Read {
		t.Errorf("caps = %+v, want read-only defaults", cfg.Domains)
	}
}
