package core

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

const (
	// maskKeep is how many characters survive at each end of a masked value.
	// Enough to recognise which credential is in play, never enough to use it.
	maskKeep = 4

	// revealRatio sets how much longer than the revealed part a value must be
	// before any of it is revealed at all. At maskKeep*2, a nine-character
	// secret would have shown eight of its nine characters.
	revealRatio = 4

	// maskRune replaces every hidden character. One rune wide, so a masked
	// value keeps the original character count.
	maskRune = "*"

	levelDebug = "DEBUG"
	levelError = "ERROR"
)

// sensitiveHeaders never reach a log in the clear, at any level. Masking is a
// property of the header, not of the verbosity setting.
var sensitiveHeaders = map[string]bool{
	"Authorization":       true,
	"Cookie":              true,
	"Set-Cookie":          true,
	"Proxy-Authorization": true,
}

// schemeHeaders are the ones whose first space-separated word is an
// authentication scheme rather than part of the credential. Knowing Basic from
// Bearer is useful in a log and is not secret.
//
// Cookies deliberately are not here: a cookie value reads
// "name=value; Path=/", so cutting at the first space would leave the entire
// credential segment in the clear.
var schemeHeaders = map[string]bool{
	"Authorization":       true,
	"Proxy-Authorization": true,
}

// Mask hides the middle of a secret, preserving the length so a truncated value
// is still visibly distinct from a short one. A value with no middle to hide is
// replaced entirely.
func Mask(s string) string {
	if s == "" {
		return ""
	}
	// Runes, not bytes: slicing bytes would split a multi-byte character and
	// emit invalid UTF-8, and a byte-length threshold would treat a short
	// non-ASCII value as long enough to reveal its ends.
	r := []rune(s)

	// Revealing the ends is only safe when far more is hidden than shown.
	// At the old threshold of maskKeep*2 a nine-character secret gave away
	// eight of its nine characters.
	if len(r) < maskKeep*revealRatio {
		return strings.Repeat(maskRune, len(r))
	}

	masked := string(r[:maskKeep]) + strings.Repeat(maskRune, len(r)-maskKeep*2) + string(r[len(r)-maskKeep:])
	if masked == s {
		// The value already has the shape of its own mask, so masking it
		// changed nothing and it would be logged verbatim. Hide it entirely.
		return strings.Repeat(maskRune, len(r))
	}
	return masked
}

// MaskHeaders returns a loggable copy of h with credentials masked. Every value
// of a multi-valued header is masked, not just the first: Set-Cookie routinely
// carries several.
func MaskHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for name, values := range h {
		canonical := http.CanonicalHeaderKey(name)
		if !sensitiveHeaders[canonical] {
			out[name] = strings.Join(values, ", ")
			continue
		}

		masked := make([]string, 0, len(values))
		for _, v := range values {
			if schemeHeaders[canonical] {
				if scheme, credential, ok := strings.Cut(v, " "); ok {
					masked = append(masked, scheme+" "+Mask(credential))
					continue
				}
			}
			// No scheme to preserve, or a header where the whole value is
			// sensitive.
			masked = append(masked, Mask(v))
		}
		out[name] = strings.Join(masked, ", ")
	}
	return out
}

// minRedactableSecret is the shortest value worth redacting from a log message.
// Below it, a secret is likely to occur inside ordinary words, and blanking
// those out destroys the diagnostics the log exists to provide.
const minRedactableSecret = 8

// Logger writes diagnostics to the writer it is given. Callers must pass stderr
// in production: stdout carries the MCP protocol, and anything written there
// corrupts the session. Nothing here enforces that, since the constructor
// accepts any writer, so that choice belongs to the wiring in main.
type Logger struct {
	mu      sync.Mutex
	w       io.Writer
	debug   bool
	secrets []string
}

// NewLogger returns a Logger at the given level, which is "debug" or anything
// else, treated as "info". Load validates ATLAS_LOG against a closed enum, so an
// unknown value here means the level arrived by some other route; it is treated
// as the quieter setting rather than silently enabling debug output.
//
// Any secrets given are redacted from every message at every level. Masking
// headers is not sufficient on its own: upstream error bodies are logged
// deliberately, because they carry Atlassian's diagnostics, and an error body
// can echo the credential back.
func NewLogger(level string, w io.Writer, secrets ...string) *Logger {
	if w == nil {
		// A logger that panics on its first write is worse than one that says
		// nothing, and the caller has nowhere to report the failure anyway.
		w = io.Discard
	}

	kept := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if len([]rune(secret)) >= minRedactableSecret {
			kept = append(kept, secret)
		}
	}
	// Longest first. redact matches greedily at each position, so a secret that
	// is a prefix of another must be tried second or the longer one is never
	// matched and keeps more than its permitted characters.
	sort.Slice(kept, func(i, j int) bool { return len(kept[i]) > len(kept[j]) })

	return &Logger{
		w:       w,
		debug:   strings.EqualFold(strings.TrimSpace(level), "debug"),
		secrets: kept,
	}
}

// Enabled reports whether the named level would produce output. Errors are
// always emitted; only debug is conditional.
func (l *Logger) Enabled(level string) bool {
	if strings.EqualFold(strings.TrimSpace(level), "debug") {
		return l.debug
	}
	return true
}

// Debugf logs only when the level is debug.
func (l *Logger) Debugf(format string, args ...any) {
	if l.debug {
		l.emit(levelDebug, fmt.Sprintf(format, args...))
	}
}

// Debug logs a message that is already assembled, without treating it as a
// format string. Use it for anything containing text from an upstream response:
// a stray % in a JSON error body would otherwise corrupt the very diagnostics
// the message exists to carry.
func (l *Logger) Debug(msg string) {
	if l.debug {
		l.emit(levelDebug, msg)
	}
}

// Errorf always logs.
func (l *Logger) Errorf(format string, args ...any) {
	l.emit(levelError, fmt.Sprintf(format, args...))
}

// Error logs an already-assembled message, without format interpretation. See
// Debug for why this matters for upstream text.
func (l *Logger) Error(msg string) {
	l.emit(levelError, msg)
}

// BasicCredential returns the credential half of an HTTP Basic Authorization
// header value, as base64(email:token).
//
// Pass this to NewLogger alongside the raw token. Redacting the token alone is
// not enough: an upstream error or a proxy diagnostic can echo the encoded
// form, and no amount of token redaction would match it.
func BasicCredential(email, token string) string {
	return base64.StdEncoding.EncodeToString([]byte(email + ":" + token))
}

func (l *Logger) emit(level, msg string) {
	msg = l.redact(msg)
	// One Write under the lock, so concurrent tool calls cannot interleave
	// halves of two messages on the same line.
	l.mu.Lock()
	defer l.mu.Unlock()
	// The write error is deliberately discarded: the only way to report a
	// failure to write to the log is the log. Returning it would push that
	// choice onto every call site for no gain.
	_, _ = io.WriteString(l.w, level+" "+msg+"\n")
}

// redact replaces every configured secret with its masked form in a single
// left-to-right pass, preferring the longest secret at each position.
//
// Sequential strings.ReplaceAll calls would be order-dependent and would also
// rescan text they had already substituted, so one secret's masked form could
// be rewritten again by another secret. A single pass cannot do either.
func (l *Logger) redact(msg string) string {
	if len(l.secrets) == 0 {
		return msg
	}
	var b strings.Builder
	b.Grow(len(msg))
	for i := 0; i < len(msg); {
		matched := false
		for _, secret := range l.secrets {
			if strings.HasPrefix(msg[i:], secret) {
				b.WriteString(Mask(secret))
				i += len(secret)
				matched = true
				break
			}
		}
		if !matched {
			b.WriteByte(msg[i])
			i++
		}
	}
	return b.String()
}
