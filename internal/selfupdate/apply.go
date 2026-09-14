//go:build selfupdate

package selfupdate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
)

// This file is the only place in the repository that writes to the filesystem,
// and everything in it is arranged so that a failure at any step leaves the
// disk exactly as it was found.
//
// Every error returned from here is deliberately path-free. os error values
// embed the path they failed on, and an error returned by a tool handler
// travels to the model inside core's result envelope: a model that learns where
// this binary lives has learned the one fact the surface guard spends its whole
// length keeping out of tool arguments. The real error, path and all, goes to
// the logger, which writes to stderr where the operator is.

// stagedFileMode is the mode the replacement binary is given. It must be
// executable or the next start fails; it is not group- or world-writable, so a
// local user cannot edit the binary between the swap and the restart.
const stagedFileMode = 0o755

// tempPattern names the staging file. The leading dot keeps it out of a casual
// listing, and the fixed prefix makes an orphan left by a crashed update
// identifiable rather than mysterious.
const tempPattern = ".atlassian-mcp-lite-update-*"

// probePattern is the same idea for the writability probe.
const probePattern = ".atlassian-mcp-lite-probe-*"

// Indirection points for the tests. Production never assigns them.
var (
	executablePath = os.Executable
	evalSymlinks   = filepath.EvalSymlinks
)

// errNotWritable is the refusal for a directory this process cannot write.
var errNotWritable = errors.New("self_update: the directory holding this executable is not writable by this process; an update would have to be installed by whoever owns it")

// errBackupExists is the refusal for a backup that is already there. It is not
// overwritten: the file it would overwrite is the only copy of a binary the
// operator may be about to roll back to.
var errBackupExists = errors.New("self_update: a backup of the running version already exists beside this executable; move or remove it before updating again")

// targetExecutable resolves the file that will actually be replaced.
//
// EvalSymlinks is the point of this function. A deployment that puts a symlink
// on PATH pointing at a versioned binary is ordinary, and renaming over the
// symlink would replace the link with a file — quietly converting a managed
// install into an unmanaged one, and leaving the real binary untouched so that
// anything else pointing at it keeps running the old code.
func targetExecutable() (string, error) {
	exe, err := executablePath()
	if err != nil {
		return "", err
	}
	resolved, err := evalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

// probeWritable answers whether a file can be created in dir, by creating one
// and removing it again.
//
// There is no portable way to ask this question without answering it: a mode
// bit comparison ignores ACLs, immutable flags, read-only mounts and every
// container filesystem this is most likely to be run on. The probe runs before
// anything is downloaded, so an operator whose install directory is root-owned
// gets the refusal in a second rather than after 64 MiB.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, probePattern)
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// applyUpdate stages the verified bytes, keeps the previous binary and swaps
// the two. It returns the backup path for the logger; the caller must not put
// it in a tool result.
//
// running is an already-validated tag, which is what makes it safe to build a
// filename from.
func applyUpdate(newBytes []byte, running string, log *core.Logger) (string, error) {
	exe, err := targetExecutable()
	if err != nil {
		log.Errorf("self_update: locate executable: %v", err)
		return "", errors.New("self_update: cannot determine which file to replace")
	}
	dir := filepath.Dir(exe)

	// The staging file goes in the same directory as the target, not in the
	// system temp directory. os.Rename is only atomic within one filesystem,
	// and /tmp is very often a different one — a cross-device rename fails
	// outright on Linux, which would be survivable, but the tempting fix for
	// that failure is a copy, and a copy over a running binary is a window in
	// which the file on disk is half of each version.
	tmp, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		log.Errorf("self_update: create staging file: %v", err)
		return "", errNotWritable
	}
	tmpName := tmp.Name()
	// Cleanup runs on every path that does not reach a successful swap. It is
	// set up before the first write so that no early return can skip it.
	staged := false
	defer func() {
		if !staged {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(newBytes); err != nil {
		_ = tmp.Close()
		log.Errorf("self_update: write staging file: %v", err)
		return "", errors.New("self_update: cannot write the new binary beside the current one")
	}
	// Sync before the rename, not after. The rename is what makes the new file
	// visible under the old name; if the metadata operation reaches the disk
	// before the data does, a crash in between leaves the executable path
	// pointing at a file of zeros, and this binary is the thing that would have
	// to fix that.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		log.Errorf("self_update: sync staging file: %v", err)
		return "", errors.New("self_update: cannot flush the new binary to disk")
	}
	// Chmod on the open file rather than on the name: CreateTemp makes the file
	// 0600, and between a close and a path-based chmod the name could be
	// something else.
	if err := tmp.Chmod(stagedFileMode); err != nil {
		_ = tmp.Close()
		log.Errorf("self_update: set mode on staging file: %v", err)
		return "", errors.New("self_update: cannot make the new binary executable")
	}
	if err := tmp.Close(); err != nil {
		log.Errorf("self_update: close staging file: %v", err)
		return "", errors.New("self_update: cannot finish writing the new binary")
	}

	// The backup is made before the swap and a failure here aborts it. A
	// signature proves the artifact is authentic, not that it runs on this
	// machine: a glibc that is too old, a kernel that lacks a syscall, or a
	// plain bug in the new version all end with an operator who needs the
	// previous binary back, and at that moment the only copy of it is the one
	// this step makes.
	backup := exe + "." + running + ".bak"
	if err := keepPrevious(exe, backup); err != nil {
		if errors.Is(err, os.ErrExist) {
			log.Errorf("self_update: backup %s already exists", backup)
			return "", errBackupExists
		}
		log.Errorf("self_update: back up the current binary: %v", err)
		return "", errors.New("self_update: cannot keep a copy of the current binary, so the update was not applied")
	}

	if err := swapInPlace(tmpName, exe); err != nil {
		// The disk is put back the way it was found: the staging file goes via
		// the deferred cleanup, and the backup goes here. Leaving a backup of a
		// binary that was never replaced would be a file whose name claims an
		// update happened.
		_ = os.Remove(backup)
		log.Errorf("self_update: swap in the new binary: %v", err)
		return "", errors.New("self_update: cannot replace the running executable; nothing was changed")
	}
	staged = true
	return backup, nil
}

// keepPrevious puts a copy of the current binary at backup.
//
// A hard link is tried first because it costs nothing and cannot half-succeed:
// it is one atomic directory operation, and it is exactly what "keep the
// previous inode" means. It fails on a filesystem without hard links and across
// some container storage drivers, and there the byte copy is the fallback.
// Both refuse to overwrite an existing backup.
func keepPrevious(exe, backup string) error {
	if err := os.Link(exe, backup); err == nil {
		return nil
	} else if errors.Is(err, os.ErrExist) {
		return err
	}
	return copyFile(exe, backup)
}

// copyFile writes src to dst, refusing to replace an existing dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src) // #nosec G304 -- src is os.Executable, never a caller-supplied path
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	// O_EXCL, so this cannot silently overwrite a backup the operator is
	// relying on. The mode matches the binary it copies.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, stagedFileMode) // #nosec G304 -- dst is os.Executable plus a validated tag, never caller input
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("close backup: %w", err)
	}
	return nil
}
