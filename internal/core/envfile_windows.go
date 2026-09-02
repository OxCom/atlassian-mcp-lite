//go:build windows

package core

import "io/fs"

// checkOwner is a no-op on Windows. Ownership there is an ACL concept with no
// uid to compare, and os.Geteuid returns -1, so the Unix rule has nothing to
// check against. The permission check in checkPrivate is skipped for the same
// reason. Neither is silent: LoadEnvFile returns envFileWarning's notice on
// this platform, naming both skipped checks, and main logs it.
func checkOwner(string, fs.FileInfo) error { return nil }
