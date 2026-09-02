//go:build !windows

package core

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// checkOwner requires the file to be owned by the effective user running the
// server. The effective uid is the one whose permissions actually apply to the
// open, so it is the right one to compare against a setuid or su'd process.
func checkOwner(path string, info fs.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		// Every Unix os.FileInfo carries a *syscall.Stat_t. Anything else means
		// the platform is not what this file was built for, and refusing is
		// safer than assuming the owner is fine.
		return fmt.Errorf("%s: cannot determine the file owner on this platform", path)
	}
	return checkOwnerUID(path, int(st.Uid), os.Geteuid())
}
