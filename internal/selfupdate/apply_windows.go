//go:build selfupdate && windows

package selfupdate

import "os"

// asideSuffix names the temporary home of the outgoing binary during the swap.
// It is distinct from the .bak suffix the backup uses: the backup is a file the
// operator is meant to keep, while this one exists for the width of two
// renames and is removed on success.
const asideSuffix = ".aside"

// swapInPlace replaces the running executable with the staged file.
//
// Windows refuses to rename over a file that is mapped as a running image, so
// the single atomic rename the Unix build relies on is not available. What
// Windows does permit is renaming the running image itself: the mapping follows
// the inode, not the name. So the outgoing binary is moved aside first and the
// staged file takes the freed name.
//
// That leaves a window — after the first rename and before the second — in
// which the executable path does not exist at all. It is closed by undoing the
// first rename whenever the second fails, so the only way to leave the disk in
// a state the operator did not ask for is for the undo to fail too, which means
// the filesystem is already failing. The window is why this is a separate file
// with its own explanation rather than a runtime.GOOS branch: the two platforms
// have genuinely different failure modes and each deserves to be read on its
// own.
func swapInPlace(staged, exe string) error {
	aside := exe + asideSuffix
	// A leftover aside file from a previous crashed update would make this
	// rename fail on Windows, so it is cleared first. Unlike the backup, it is
	// not a file anyone is meant to be keeping.
	_ = os.Remove(aside)
	if err := os.Rename(exe, aside); err != nil {
		return err
	}
	if err := os.Rename(staged, exe); err != nil {
		// Put the running binary's name back before reporting the failure. The
		// caller removes the staging file and the backup, so after this the
		// directory is as it was.
		_ = os.Rename(aside, exe)
		return err
	}
	// The old image is still mapped by this process, so the delete may be
	// refused until it exits. That is not a failure of the update: the backup
	// the caller made is the copy that matters, and an orphaned .aside file is
	// cosmetic.
	_ = os.Remove(aside)
	return nil
}
