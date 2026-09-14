//go:build selfupdate && !windows

package selfupdate

import "os"

// swapInPlace replaces the running executable with the staged file.
//
// On a POSIX system this is one rename. Unlinking the path a running program
// was loaded from is permitted: the kernel holds the inode open for as long as
// the process lives, so the process keeps executing the old image out of a file
// that no longer has a name, while every new exec of that path gets the new
// one. That is precisely the behaviour this feature wants — the current MCP
// session keeps serving from the old code, and the operator's next restart
// picks up the new code — and it is why the result says "restart required"
// rather than trying to re-exec.
//
// Rename is also atomic within a filesystem: there is no instant at which the
// executable path names a partially written file. Both arguments live in the
// same directory by construction, which is what guarantees that.
func swapInPlace(staged, exe string) error {
	return os.Rename(staged, exe)
}
