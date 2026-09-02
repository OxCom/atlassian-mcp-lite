package core

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"strings"
)

// EnvFileVar names the variable that points at the private configuration file.
const EnvFileVar = "ATLAS_ENV_FILE"

// maxEnvFileBytes bounds the file. A configuration is a few hundred bytes;
// anything far beyond that is the wrong file.
const maxEnvFileBytes = 64 * 1024

// LoadEnvFile reads KEY=VALUE lines from path and returns a getenv that
// consults the process environment first and the file second. lookup has the
// shape of os.LookupEnv so that a variable set to the empty string in the
// process still overrides the file: "ATLAS_JIRA_WRITE=" must turn a
// file-enabled capability off, not fall through to the file's "true".
//
// The file is refused unless it is a regular file with mode 0600 or 0400 and
// no setuid, setgid or sticky bit. It holds an API token with the full
// authority of its account, so a file another local user can read is a leaked
// credential, and the safe reaction is to refuse to start and say how to fix
// it. The check is skipped on Windows, whose ACLs are not expressed in Unix
// mode bits.
//
// A symbolic link is refused before the file is opened. The link's owner
// decides what it points at, and a 0600 target owned by that person passes
// every permission check while being their credential, not the operator's.
// The file must also be owned by the user running the server, for the same
// reason; root is not exempt, because running as root does not make another
// user's file the operator's.
//
// The file is opened once and every check runs on that descriptor, so the path
// cannot be swapped between the check and the read; the descriptor is compared
// with the pre-open Lstat so a swap between the two calls is caught as well.
//
// Syntax: one KEY=VALUE per line, blank lines and lines starting with "#"
// ignored, an optional "export " prefix, and a value optionally wrapped in
// single or double quotes. Nothing is interpolated.
func LoadEnvFile(path string, lookup func(string) (string, bool)) (func(string) string, error) {
	// Lstat, not Stat: Stat follows the link and would report the target as a
	// perfectly ordinary regular file.
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvFileVar, err)
	}
	if linkInfo.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s: %s is a symbolic link; point %s at the regular file itself", EnvFileVar, path, EnvFileVar)
	}

	f, err := os.Open(path) // #nosec G304 -- the operator names this path on purpose
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvFileVar, err)
	}
	defer f.Close() //nolint:errcheck // read-only descriptor; nothing to flush

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvFileVar, err)
	}
	// The descriptor must be the file Lstat looked at. If the path was
	// replaced between the two calls — by a symlink, say — the symlink check
	// above passed against a file that is not the one being read.
	if !os.SameFile(linkInfo, info) {
		return nil, fmt.Errorf("%s: %s changed between being checked and being opened", EnvFileVar, path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %s is not a regular file", EnvFileVar, path)
	}
	if err := checkOwner(path, info); err != nil {
		return nil, fmt.Errorf("%s: %w", EnvFileVar, err)
	}
	if info.Size() > maxEnvFileBytes {
		return nil, fmt.Errorf("%s: %s is %d bytes; a configuration file must be under %d", EnvFileVar, path, info.Size(), maxEnvFileBytes)
	}
	if err := checkPrivate(path, info.Mode()); err != nil {
		return nil, fmt.Errorf("%s: %w", EnvFileVar, err)
	}

	// Bounded by one byte more than the cap so a file that grew after Stat is
	// detected rather than silently truncated.
	raw, err := io.ReadAll(io.LimitReader(f, maxEnvFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvFileVar, err)
	}
	if len(raw) > maxEnvFileBytes {
		return nil, fmt.Errorf("%s: %s is larger than %d bytes", EnvFileVar, path, maxEnvFileBytes)
	}
	values, err := parseEnvFile(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", EnvFileVar, path, err)
	}

	return func(key string) string {
		if v, ok := lookup(key); ok {
			return v
		}
		return values[key]
	}, nil
}

// checkOwnerUID is the owner rule with its inputs already extracted, so the
// mismatch case can be tested without a second user account. Root is not
// exempt on purpose: a 0600 file owned by another user is that user's
// credential, and a server running as root would be reading it on their
// behalf rather than the operator's.
func checkOwnerUID(path string, fileUID, processUID int) error {
	if fileUID == processUID {
		return nil
	}
	return fmt.Errorf("%s is owned by uid %d but this process runs as uid %d; the file must be owned by the user running the server", path, fileUID, processUID)
}

// checkPrivate accepts exactly 0600 or 0400: owner read, optionally owner
// write, nothing for anyone else, no execute bit on a file that is data, and no
// setuid, setgid or sticky bit. The error carries the exact command that fixes
// it, because the operator reads this once, in a client's log, and should not
// have to look anything up.
func checkPrivate(path string, mode fs.FileMode) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	perm := mode.Perm()
	special := mode & (fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
	if special == 0 && (perm == 0o600 || perm == 0o400) {
		return nil
	}
	return fmt.Errorf("%s has permissions %04o, but it holds an API token and must be readable by its owner only; run: chmod 600 %s", path, perm|specialBits(special), shellQuote(path))
}

// specialBits renders setuid, setgid and sticky as the octal digit chmod uses,
// so the reported mode reads as the operator would see it in ls or stat.
func specialBits(m fs.FileMode) fs.FileMode {
	var out fs.FileMode
	if m&fs.ModeSetuid != 0 {
		out |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		out |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		out |= 0o1000
	}
	return out
}

// shellQuote wraps a path in single quotes for a copy-pasted command. A path
// with a space or a shell metacharacter would otherwise produce a command that
// does something other than what the message says.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// parseEnvFile turns the file body into a map. A line that is neither blank, a
// comment nor KEY=VALUE is an error: a malformed line silently dropped would
// make a capability the operator believes is set quietly fall back to default.
//
// A key assigned twice is an error naming both lines. Last-one-wins would let
// the operator read the first assignment, believe it, and run with the second.
func parseEnvFile(body string) (map[string]string, error) {
	out := make(map[string]string)
	definedAt := make(map[string]int)
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 4096), maxEnvFileBytes)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", n)
		}
		if !domainRe.MatchString(strings.ToLower(key)) {
			return nil, fmt.Errorf("line %d: %q is not a valid variable name", n, key)
		}
		// Keys are stored raw and Load looks them up in upper case, so a
		// lowercase line would validate here and then never be read: the
		// capability the operator believes they set would silently stay at its
		// default. Rejected rather than up-cased, because rewriting the
		// operator's key would hide a typo in a file that grants access.
		if upper := strings.ToUpper(key); key != upper {
			return nil, fmt.Errorf("line %d: %q must be written in upper case as %s", n, key, upper)
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 {
			if q := val[0]; (q == '"' || q == '\'') && val[len(val)-1] == q {
				val = val[1 : len(val)-1]
			}
		}
		if err := hasNoControlChars(val); err != nil {
			return nil, fmt.Errorf("line %d: %s: %w", n, key, err)
		}
		if first, dup := definedAt[key]; dup {
			return nil, fmt.Errorf("line %d: %s is already set on line %d; a key may appear only once", n, key, first)
		}
		definedAt[key] = n
		out[key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, errors.New("file could not be read line by line")
	}
	return out, nil
}
