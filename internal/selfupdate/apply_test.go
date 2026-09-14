//go:build selfupdate

package selfupdate

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeExecutable creates a stand-in binary in a fresh directory and points
// executablePath at it. Returning the path keeps each test's assertions about
// the filesystem local to the directory it owns.
func fakeExecutable(t *testing.T, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "atlassian-mcp-lite")
	if err := os.WriteFile(exe, content, 0o755); err != nil { //nolint:gosec // a test fixture standing in for an executable
		t.Fatalf("write fixture: %v", err)
	}
	prev := executablePath
	executablePath = func() (string, error) { return exe, nil }
	t.Cleanup(func() { executablePath = prev })
	return exe
}

// noStagingLeftovers asserts that no half-written update remains in dir.
func noStagingLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".atlassian-mcp-lite-update-") {
			t.Errorf("staging file %s was left behind", e.Name())
		}
		if strings.HasPrefix(e.Name(), ".atlassian-mcp-lite-probe-") {
			t.Errorf("probe file %s was left behind", e.Name())
		}
	}
}

func TestApplyUpdateSwapsTheBinaryAndKeepsThePreviousOne(t *testing.T) {
	old := []byte("the old binary")
	newBytes := []byte("the new binary")
	exe := fakeExecutable(t, old)
	dir := filepath.Dir(exe)

	backup, err := applyUpdate(newBytes, "v0.1.0", quietLogger())
	if err != nil {
		t.Fatalf("applyUpdate: %v", err)
	}

	got, err := os.ReadFile(exe) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatalf("read executable: %v", err)
	}
	if !bytes.Equal(got, newBytes) {
		t.Errorf("executable = %q, want %q", got, newBytes)
	}

	// The backup is the previous binary, readable, and named after the version
	// it holds — which is what makes a rollback something an operator can
	// perform without guessing.
	if backup != exe+".v0.1.0.bak" {
		t.Errorf("backup = %q, want %q", backup, exe+".v0.1.0.bak")
	}
	kept, err := os.ReadFile(backup) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !bytes.Equal(kept, old) {
		t.Errorf("backup = %q, want the previous binary %q", kept, old)
	}

	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(exe)
		if statErr != nil {
			t.Fatalf("stat: %v", statErr)
		}
		// Executable, and not group- or world-writable: a local user must not
		// be able to edit the binary between the swap and the restart.
		if info.Mode().Perm() != stagedFileMode {
			t.Errorf("mode = %v, want %v", info.Mode().Perm(), os.FileMode(stagedFileMode))
		}
	}

	noStagingLeftovers(t, dir)
}

// TestApplyUpdateAbortsBeforeTheSwapWhenTheBackupCannotBeMade is the invariant
// that decides whether a failed update is recoverable. If the swap ran first
// and the backup then failed, the operator would be left with a new binary they
// cannot undo.
func TestApplyUpdateAbortsBeforeTheSwapWhenTheBackupCannotBeMade(t *testing.T) {
	old := []byte("the old binary")
	exe := fakeExecutable(t, old)
	dir := filepath.Dir(exe)

	// A backup for this version is already there. It is the only copy of a
	// binary the operator may be about to roll back to, so it is never
	// overwritten — and that refusal has to stop the update, not be skipped.
	backup := exe + ".v0.1.0.bak"
	if err := os.WriteFile(backup, []byte("an older binary"), 0o755); err != nil { //nolint:gosec // a test fixture standing in for an executable
		t.Fatalf("write backup fixture: %v", err)
	}

	// Held in its own variable: the reads below reuse err, and the refusal is
	// asserted on again at the end of this test.
	refusal := func() error {
		_, err := applyUpdate([]byte("the new binary"), "v0.1.0", quietLogger())
		return err
	}()
	if !errors.Is(refusal, errBackupExists) {
		t.Fatalf("err = %v, want errBackupExists", refusal)
	}

	// Nothing changed: the executable is untouched, the existing backup is
	// untouched, and no staging file survives.
	got, err := os.ReadFile(exe) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatalf("read executable: %v", err)
	}
	if !bytes.Equal(got, old) {
		t.Errorf("executable = %q, want it unchanged as %q", got, old)
	}
	kept, err := os.ReadFile(backup) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !bytes.Equal(kept, []byte("an older binary")) {
		t.Errorf("the existing backup was overwritten: %q", kept)
	}
	noStagingLeftovers(t, dir)

	// The refusal names no filesystem path: it reaches the model through the
	// tool result.
	if strings.Contains(refusal.Error(), dir) {
		t.Errorf("the refusal leaks a filesystem path: %v", refusal)
	}
}

// TestApplyUpdateRemovesTheStagingFileWhenTheSwapFails drives the other
// cleanup path: the staging file is written, the backup is made, and the swap
// itself fails.
func TestApplyUpdateRemovesTheStagingFileWhenTheSwapFails(t *testing.T) {
	old := []byte("the old binary")
	exe := fakeExecutable(t, old)
	dir := filepath.Dir(exe)

	// EvalSymlinks is made to resolve the executable to a directory. The swap
	// then fails — renaming a file over a directory is refused on every
	// platform — without needing the test to break the filesystem.
	target := filepath.Join(dir, "not-a-file")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	prev := evalSymlinks
	evalSymlinks = func(string) (string, error) { return target, nil }
	defer func() { evalSymlinks = prev }()

	if _, err := applyUpdate([]byte("the new binary"), "v0.1.0", quietLogger()); err == nil {
		t.Fatal("a failed swap was reported as success")
	}

	noStagingLeftovers(t, dir)
	// The backup is removed too, so the directory is left as it was found: a
	// .bak file beside an unreplaced binary would be a filename claiming an
	// update that never happened.
	if _, err := os.Stat(target + ".v0.1.0.bak"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a backup survived a failed swap: %v", err)
	}
}

// TestTargetExecutableResolvesASymlink: a deployment that puts a symlink on
// PATH is ordinary, and renaming over the link would replace the link with a
// file, quietly converting a managed install into an unmanaged one while
// leaving the target binary running the old code.
func TestTargetExecutableResolvesASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs a privilege this test does not assume on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "atlassian-mcp-lite-v1")
	if err := os.WriteFile(target, []byte("the old binary"), 0o755); err != nil { //nolint:gosec // a test fixture standing in for an executable
		t.Fatalf("write fixture: %v", err)
	}
	link := filepath.Join(dir, "atlassian-mcp-lite")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	prev := executablePath
	executablePath = func() (string, error) { return link, nil }
	defer func() { executablePath = prev }()

	got, err := targetExecutable()
	if err != nil {
		t.Fatalf("targetExecutable: %v", err)
	}
	// EvalSymlinks also resolves the temp directory itself on macOS, so the
	// base name is what is compared.
	if filepath.Base(got) != filepath.Base(target) {
		t.Errorf("target = %q, want the target binary %q", got, target)
	}

	if _, err := applyUpdate([]byte("the new binary"), "v0.1.0", quietLogger()); err != nil {
		t.Fatalf("applyUpdate: %v", err)
	}
	// The link is still a link, and it still points at the file that was
	// replaced.
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}
	got2, err := os.ReadFile(target) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatalf("read target binary: %v", err)
	}
	if !bytes.Equal(got2, []byte("the new binary")) {
		t.Errorf("the target binary was not replaced: %q", got2)
	}
}

func TestProbeWritableRefusesADirectoryThisProcessCannotWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not govern writability on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory regardless of its mode")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	// Restored so t.TempDir can clean up after itself.
	defer func() { _ = os.Chmod(dir, 0o755) }()

	if err := probeWritable(dir); err == nil {
		t.Fatal("a read-only directory was reported writable")
	}
}

func TestProbeWritableLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	if err := probeWritable(dir); err != nil {
		t.Fatalf("probeWritable: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("probe left %d entries behind", len(entries))
	}
}
