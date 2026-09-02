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

	getenv, _, err := LoadEnvFile(path, lookupEnv(map[string]string{
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
		_, _, err := LoadEnvFile(path, lookupEnv(nil))
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
		if _, _, err := LoadEnvFile(path, lookupEnv(nil)); err != nil {
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
	_, _, err := LoadEnvFile(path, lookupEnv(nil))
	if err == nil {
		t.Fatal("0644 must be refused")
	}
	want := `chmod 600 '` + strings.ReplaceAll(path, "'", `'\''`) + `'`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), want)
	}
}

func TestLoadEnvFileRejectsMissingAndMalformed(t *testing.T) {
	if _, _, err := LoadEnvFile(filepath.Join(t.TempDir(), "absent"), lookupEnv(nil)); err == nil {
		t.Error("a missing file must be an error, not an empty configuration")
	}
	if _, _, err := LoadEnvFile(t.TempDir(), lookupEnv(nil)); err == nil {
		t.Error("a directory must be refused")
	}
	for _, body := range []string{
		"ATLAS_TOKEN\n",
		"=value\n",
		"BAD-NAME=x\n",
		"ATLAS_TOKEN=a\x01b\n",
	} {
		path := writeEnvFile(t, body, 0o600)
		if _, _, err := LoadEnvFile(path, lookupEnv(nil)); err == nil {
			t.Errorf("%q must be rejected", body)
		}
	}
}

func TestLoadEnvFileBoundsSize(t *testing.T) {
	path := writeEnvFile(t, "# "+strings.Repeat("x", maxEnvFileBytes)+"\n", 0o600)
	if _, _, err := LoadEnvFile(path, lookupEnv(nil)); err == nil {
		t.Error("a file over the size cap must be refused")
	}
}

// The end-to-end shape: a file with nothing but credentials yields a read-only
// server for every domain, through the same Load the binary uses.
func TestLoadEnvFileFeedsLoad(t *testing.T) {
	path := writeEnvFile(t, "ATLAS_BASE_URL=https://x.atlassian.net\nATLAS_EMAIL=a@b.c\nATLAS_TOKEN="+fixtureToken+"\n", 0o600)
	getenv, _, err := LoadEnvFile(path, lookupEnv(nil))
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

// A symlink lets whoever controls the link decide which file is read, and a
// 0600 target owned by that person passes every permission check while being
// their credential rather than the operator's. The path must be the file.
func TestLoadEnvFileRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.WriteFile(target, []byte("ATLAS_TOKEN="+fixtureToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}

	_, _, err := LoadEnvFile(link, lookupEnv(nil))
	if err == nil {
		t.Fatal("a symbolic link must be refused even when its target is private")
	}
	want := EnvFileVar + ": " + link + " is a symbolic link; point " + EnvFileVar + " at the regular file itself"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if strings.Contains(err.Error(), fixtureToken) {
		t.Error("error echoed the file contents")
	}

	// The control: the target itself is accepted.
	if _, _, err := LoadEnvFile(target, lookupEnv(nil)); err != nil {
		t.Errorf("the regular file must be accepted: %v", err)
	}
}

// Two assignments of one key are a contradiction, not a last-one-wins: the
// operator would otherwise believe whichever line they looked at.
func TestLoadEnvFileRejectsDuplicateKeys(t *testing.T) {
	path := writeEnvFile(t, strings.Join([]string{
		"ATLAS_JIRA_WRITE=false",
		"# a comment in between",
		"ATLAS_EMAIL=a@b.c",
		"export ATLAS_JIRA_WRITE=true",
	}, "\n")+"\n", 0o600)
	_, _, err := LoadEnvFile(path, lookupEnv(nil))
	if err == nil {
		t.Fatal("a duplicate key must be refused")
	}
	msg := err.Error()
	for _, want := range []string{"ATLAS_JIRA_WRITE", "line 1", "line 4"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to mention %q", msg, want)
		}
	}
	if strings.Contains(msg, "ATLAS_EMAIL") {
		t.Errorf("error = %q, must not name a key that was set once", msg)
	}
}

// The owner rule compares two ids and nothing else, so the mismatch case can be
// exercised without a second user account. Root is deliberately not exempt: a
// 0600 file owned by another user is that user's credential, and running as
// root does not make it the operator's.
func TestCheckOwnerMatchesOnlyTheSameUID(t *testing.T) {
	if err := checkOwnerUID("/etc/x", 1000, 1000); err != nil {
		t.Errorf("same uid must pass: %v", err)
	}
	if err := checkOwnerUID("/etc/x", 0, 0); err != nil {
		t.Errorf("root-owned file read by root must pass: %v", err)
	}
	err := checkOwnerUID("/etc/x", 1001, 1000)
	if err == nil {
		t.Fatal("a foreign owner must be refused")
	}
	want := "/etc/x is owned by uid 1001 but this process runs as uid 1000; the file must be owned by the user running the server"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if err := checkOwnerUID("/etc/x", 1000, 0); err == nil {
		t.Error("root must not be exempt from the owner check")
	}
}

// The happy path for the real owner check: a file this process just created is
// owned by this process, so it must load.
func TestLoadEnvFileAcceptsOwnFile(t *testing.T) {
	path := writeEnvFile(t, "ATLAS_TOKEN="+fixtureToken+"\n", 0o600)
	if _, _, err := LoadEnvFile(path, lookupEnv(nil)); err != nil {
		t.Errorf("a private file owned by this process must load: %v", err)
	}
}

// Keys are stored raw and Load looks them up in upper case, so a lowercase line
// would validate and then never be read: the operator would believe a
// capability or a credential is set while the process runs on the default. The
// error must name the line and the upper-case form to write instead.
func TestLoadEnvFileRejectsLowercaseKeys(t *testing.T) {
	for _, key := range []string{"atlas_token", "Atlas_Token", "ATLAS_jira_WRITE"} {
		path := writeEnvFile(t, "ATLAS_URL=https://x.atlassian.net\n"+key+"=value\n", 0o600)
		_, _, err := LoadEnvFile(path, lookupEnv(nil))
		if err == nil {
			t.Errorf("%q must be rejected: a key Load can never find is a silent misconfiguration", key)
			continue
		}
		for _, want := range []string{"line 2", key, strings.ToUpper(key)} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q: error = %v, want it to mention %q", key, err, want)
			}
		}
	}
}

// The rejection must be of the case, not of the name: the same key in upper
// case is exactly what the operator is told to write, so it has to be accepted.
func TestLoadEnvFileAcceptsUppercaseKeys(t *testing.T) {
	path := writeEnvFile(t, "ATLAS_TOKEN=value\nATLAS_JIRA_WRITE=true\n", 0o600)
	getenv, _, err := LoadEnvFile(path, lookupEnv(nil))
	if err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	if got := getenv("ATLAS_TOKEN"); got != "value" {
		t.Errorf("ATLAS_TOKEN = %q, want %q", got, "value")
	}
	if got := getenv("ATLAS_JIRA_WRITE"); got != "true" {
		t.Errorf("ATLAS_JIRA_WRITE = %q, want %q", got, "true")
	}
}

// On Windows neither the owner check nor the permission check can run: access
// there is an ACL, not a set of Unix mode bits, and os.Geteuid returns -1. The
// checks used to be silent no-ops, so a token file readable by every local
// account loaded with nothing said about it. The decision is exercised as a
// pure function, since the released windows/amd64 build cannot be run here.
func TestEnvFileWarningNamesTheChecksThatDidNotRun(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		if w := envFileWarning(goos, "/srv/atlassian.env"); w != "" {
			t.Errorf("%s: warning = %q, want none where both checks run", goos, w)
		}
	}

	w := envFileWarning("windows", `C:\ProgramData\atlassian.env`)
	if w == "" {
		t.Fatal("Windows skips both checks, so it must say so")
	}
	for _, want := range []string{EnvFileVar, `C:\ProgramData\atlassian.env`, "owner", "permission", "Windows", "restrict"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning = %q, want it to mention %q", w, want)
		}
	}
}

// The warning is a platform fact, so on a platform where both checks did run
// the caller must be given nothing to log: an unconditional notice would train
// the operator to ignore it.
func TestLoadEnvFileWarnsOnlyWhereACheckIsSkipped(t *testing.T) {
	path := writeEnvFile(t, "ATLAS_TOKEN="+fixtureToken+"\n", 0o600)
	_, warning, err := LoadEnvFile(path, lookupEnv(nil))
	if err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	want := envFileWarning(runtime.GOOS, path)
	if warning != want {
		t.Errorf("warning = %q, want %q for %s", warning, want, runtime.GOOS)
	}
}
