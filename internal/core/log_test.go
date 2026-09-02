package core

import (
	"bytes"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

func TestMaskKeepsHeadHidesRest(t *testing.T) {
	const secret = "TOKENsecretmiddlepart0123"
	got := Mask(secret)
	if strings.Contains(got, "secretmiddlepart") {
		t.Fatalf("Mask leaked the middle: %q", got)
	}
	if !strings.HasPrefix(got, "TOKE") {
		t.Errorf("Mask(%q) = %q, want the first 4 characters preserved", secret, got)
	}
	// The tail is where the secret lives. In the value this most often masks,
	// base64("email:token"), the last four characters decode to three bytes of
	// the token itself.
	if strings.HasSuffix(got, "0123") {
		t.Errorf("Mask(%q) = %q, want the trailing characters hidden", secret, got)
	}
	if len(got) != len(secret) {
		t.Errorf("Mask changed the length: %d, want %d", len(got), len(secret))
	}
}

// A short value has no middle to hide, so preserving four characters at each
// end would reveal all of it.
func TestMaskShortValueIsFullyHidden(t *testing.T) {
	for _, in := range []string{"a", "abcd", "abcdefgh"} {
		got := Mask(in)
		if strings.ContainsAny(got, "abcdefgh") {
			t.Errorf("Mask(%q) = %q, a short value must be fully hidden", in, got)
		}
		if len(got) != len(in) {
			t.Errorf("Mask(%q) = %q, want the length preserved", in, got)
		}
	}
}

func TestMaskEmptyStaysEmpty(t *testing.T) {
	if got := Mask(""); got != "" {
		t.Errorf("Mask(%q) = %q, want empty", "", got)
	}
}

func TestMaskHeadersPreservesAuthSchemeHidesCredential(t *testing.T) {
	// Computed, not a base64 literal. A hardcoded credential here is
	// indistinguishable from a real one to a secret scanner, and gitleaks did
	// flag the literal this replaced.
	credential := BasicCredential("user@example.com", "secretvalue")
	h := http.Header{}
	h.Set("Authorization", "Basic "+credential)
	h.Set("Accept", "application/json")

	got := MaskHeaders(h)

	// Assert the whole credential is gone. An earlier version of this test
	// looked for base64("secretvalue"), which never appears in the encoding of
	// the full string, so it passed even against an unchanged header.
	if strings.Contains(got["Authorization"], credential) {
		t.Errorf("Authorization still contains the credential: %q", got["Authorization"])
	}
	want := "Basic " + Mask(credential)
	if got["Authorization"] != want {
		t.Errorf("Authorization = %q, want %q", got["Authorization"], want)
	}
	if got["Accept"] != "application/json" {
		t.Errorf("Accept = %q, non-sensitive headers must pass through unchanged", got["Accept"])
	}
}

// A cookie value contains "name=value; attr=value", so splitting it at the
// first space the way an auth scheme is split would leave the whole credential
// segment in the clear.
func TestMaskHeadersCookiesAreMaskedWhole(t *testing.T) {
	h := http.Header{}
	h.Set("Cookie", "session=supersecretvalue; Path=/; HttpOnly")
	h.Add("Set-Cookie", "tenant=anothersecretvalue; Secure")
	h.Add("Set-Cookie", "third=yetanothersecret; Secure")
	h.Set("Proxy-Authorization", "Bearer proxysecretcredential")

	got := MaskHeaders(h)
	for _, key := range []string{"Cookie", "Set-Cookie"} {
		for _, leak := range []string{"supersecretvalue", "anothersecretvalue", "yetanothersecret", "session=", "tenant="} {
			if strings.Contains(got[key], leak) {
				t.Errorf("%s leaked %q: %q", key, leak, got[key])
			}
		}
	}
	if !strings.HasPrefix(got["Proxy-Authorization"], "Bearer ") {
		t.Errorf("Proxy-Authorization = %q, want the scheme preserved", got["Proxy-Authorization"])
	}
	if strings.Contains(got["Proxy-Authorization"], "proxysecretcredential") {
		t.Errorf("Proxy-Authorization leaked: %q", got["Proxy-Authorization"])
	}
}

// A sensitive header with no scheme at all must still be masked rather than
// passed through by a code path that only handles the "scheme credential" shape.
func TestMaskHeadersSchemelessAuthorizationIsMasked(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "rawcredentialwithnoscheme")
	if got := MaskHeaders(h)["Authorization"]; strings.Contains(got, "rawcredentialwithnoscheme") {
		t.Errorf("Authorization = %q, want masked", got)
	}
}

func TestLoggerInfoSuppressesDebug(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("info", &buf)
	l.Debugf("this must not appear")
	l.Errorf("this must appear")

	out := buf.String()
	if strings.Contains(out, "must not appear") {
		t.Error("debug output leaked at info level")
	}
	if !strings.Contains(out, "must appear") {
		t.Error("error output missing at info level")
	}
}

func TestLoggerDebugEmitsBoth(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("debug", &buf)
	l.Debugf("dbg")
	l.Errorf("err")

	out := buf.String()
	if !strings.Contains(out, "dbg") || !strings.Contains(out, "err") {
		t.Errorf("debug level output = %q, want both lines", out)
	}
}

func TestLoggerEnabled(t *testing.T) {
	debug := NewLogger("debug", &bytes.Buffer{})
	info := NewLogger("info", &bytes.Buffer{})

	if !debug.Enabled("debug") {
		t.Error("debug level must enable debug")
	}
	if info.Enabled("debug") {
		t.Error("info level must not enable debug")
	}
	for _, l := range []*Logger{debug, info} {
		if !l.Enabled("error") {
			t.Error("errors are emitted at every level")
		}
	}
}

// Masking headers is not enough on its own. An upstream error body is logged
// deliberately, because it carries the diagnostics, and it can echo the
// credential back.
func TestLoggerRedactsConfiguredSecrets(t *testing.T) {
	const token = "supersecrettokenvalue"
	var buf bytes.Buffer
	l := NewLogger("debug", &buf, token)

	l.Errorf("upstream said: %s", `{"errorMessages":["bad token supersecrettokenvalue"]}`)
	l.Debugf("also here: %s", token)

	if strings.Contains(buf.String(), token) {
		t.Fatalf("secret leaked into the log: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "supe") {
		t.Errorf("the masked form should still be recognisable: %q", buf.String())
	}
}

// Redacting a very short secret would blank out ordinary words wherever they
// happened to appear, which destroys the diagnostics the log exists for.
func TestLoggerIgnoresSecretsTooShortToRedactSafely(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("info", &buf, "err", "")
	l.Errorf("a genuine error message")
	if !strings.Contains(buf.String(), "a genuine error message") {
		t.Errorf("a short secret must not redact ordinary text: %q", buf.String())
	}
}

func TestLoggerUnknownLevelIsNotDebug(t *testing.T) {
	// Load validates ATLAS_LOG, but the logger must not silently turn on debug
	// for a value that reached it another way.
	var buf bytes.Buffer
	NewLogger("verbose", &buf).Debugf("must not appear")
	if buf.Len() != 0 {
		t.Errorf("unknown level enabled debug output: %q", buf.String())
	}
}

// MCP tool calls are served concurrently, so the logger is written to from
// several goroutines. Run with -race to make this meaningful.
func TestLoggerIsSafeForConcurrentUse(t *testing.T) {
	// A plain bytes.Buffer would not prove serialisation: without -race,
	// removing the mutex can still yield 100 well-formed lines depending on
	// scheduling. This writer reports overlap directly, so the test fails
	// whether or not the race detector is on.
	w := &overlapDetectingWriter{}
	l := NewLogger("debug", w, "concurrentsecretvalue")

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Error("line with concurrentsecretvalue in it")
			l.Debug("another line")
		}()
	}
	wg.Wait()

	if n := w.overlaps.Load(); n != 0 {
		t.Errorf("%d overlapping writes; the lock is not serialising output", n)
	}
	if strings.Contains(w.String(), "concurrentsecretvalue") {
		t.Error("secret leaked under concurrent writes")
	}
	if got := strings.Count(w.String(), "\n"); got != 100 {
		t.Errorf("wrote %d lines, want 100; interleaved writes lose or corrupt output", got)
	}
}

// overlapDetectingWriter counts Write calls that begin while another is still
// running. It deliberately does not lock around the body it is measuring; its
// own buffer is guarded separately.
type overlapDetectingWriter struct {
	inFlight atomic.Int32
	overlaps atomic.Int32

	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *overlapDetectingWriter) Write(p []byte) (int, error) {
	if w.inFlight.Add(1) > 1 {
		w.overlaps.Add(1)
	}
	defer w.inFlight.Add(-1)

	// Widen the window an unsynchronised caller would race in.
	runtime.Gosched()

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *overlapDetectingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// A value already shaped like its own mask must not be returned unchanged.
// Mask("abcd*efgh") used to produce exactly "abcd*efgh".
func TestMaskNeverReturnsItsInputUnchanged(t *testing.T) {
	for _, in := range []string{
		"abcd*efgh",
		"abcd**efgh",
		"TOKE****************0123",
		"abcd" + strings.Repeat("*", 20) + "efgh",
	} {
		if got := Mask(in); got == in {
			t.Errorf("Mask(%q) returned its input unchanged", in)
		}
	}
}

// Byte slicing would split a multi-byte rune and emit invalid UTF-8, and a
// byte-length threshold would treat a short non-ASCII value as long enough to
// reveal its ends.
func TestMaskHandlesMultiByteRunes(t *testing.T) {
	const short = "héllo" // 5 runes, 6 bytes
	if got := Mask(short); strings.ContainsAny(got, "héllo") {
		t.Errorf("Mask(%q) = %q, a 5-rune value must be fully hidden", short, got)
	}

	long := strings.Repeat("é", 20)
	got := Mask(long)
	if !utf8.ValidString(got) {
		t.Errorf("Mask produced invalid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 20 {
		t.Errorf("Mask changed the rune count: %d, want 20", n)
	}
}

// The threshold governs how much must be hidden before anything is revealed.
func TestMaskRevealsHeadOnlyWhenEnoughIsHidden(t *testing.T) {
	for _, n := range []int{8, 9, 12, 15} {
		in := strings.Repeat("a", n)
		if got := Mask(in); strings.Contains(got, "a") {
			t.Errorf("Mask of a %d-character value revealed characters: %q", n, got)
		}
	}
	if got := Mask(strings.Repeat("a", 16)); !strings.HasPrefix(got, "aaaa") || strings.HasSuffix(got, "aaaa") {
		t.Errorf("Mask of a 16-character value = %q, want its head revealed and its tail hidden", got)
	}
}

// A secret that is a prefix of another must not be substituted first: doing so
// destroys the longer match and leaves more of it visible than intended.
func TestLoggerRedactsLongestSecretFirst(t *testing.T) {
	const (
		short = "prefixsecretvalue"
		long  = "prefixsecretvalue-with-more-after-it"
	)
	for _, order := range [][]string{{short, long}, {long, short}} {
		var buf bytes.Buffer
		NewLogger("info", &buf, order...).Error("upstream echoed " + long)

		out := buf.String()
		if strings.Contains(out, long) {
			t.Errorf("order %v: the longer secret survived: %q", order, out)
		}
		if strings.Contains(out, "-with-more-after-it") {
			t.Errorf("order %v: the tail of the longer secret leaked: %q", order, out)
		}
	}
}

// Redaction runs once over the message, so a secret's masked form is never
// rewritten again by another secret.
func TestLoggerDoesNotRedactItsOwnReplacements(t *testing.T) {
	const secret = "originalsecretvalue"
	var buf bytes.Buffer
	// The second secret is the masked form of the first.
	NewLogger("info", &buf, secret, Mask(secret)).Error("body: " + secret)

	if !strings.Contains(buf.String(), "*") {
		t.Fatalf("nothing was masked: %q", buf.String())
	}
	if strings.Contains(buf.String(), secret) {
		t.Errorf("secret leaked: %q", buf.String())
	}
}

// The Basic credential is what actually travels on the wire, so an upstream
// echo of it must be redactable too.
func TestBasicCredentialIsRedactable(t *testing.T) {
	const (
		email = "user@example.com"
		token = "atlassian-api-token-value"
	)
	credential := BasicCredential(email, token)
	if credential == "" || strings.Contains(credential, token) {
		t.Fatalf("BasicCredential = %q, want a base64 encoding", credential)
	}

	var buf bytes.Buffer
	NewLogger("info", &buf, token, credential).
		Error("proxy rejected Authorization: Basic " + credential)

	if strings.Contains(buf.String(), credential) {
		t.Errorf("the encoded credential leaked: %q", buf.String())
	}
}

// Upstream bodies contain characters Printf would interpret. The plain variants
// exist so a stray verb cannot corrupt the diagnostics.
func TestPlainVariantsDoNotInterpretFormatVerbs(t *testing.T) {
	const body = `{"errorMessages":["100%s unexpected: %d %v"]}`
	var buf bytes.Buffer
	l := NewLogger("debug", &buf)
	l.Error(body)
	l.Debug(body)

	out := buf.String()
	if strings.Contains(out, "%!") {
		t.Errorf("format verbs were interpreted: %q", out)
	}
	if got := strings.Count(out, body); got != 2 {
		t.Errorf("body appeared %d times verbatim, want 2: %q", got, out)
	}
}

func TestNewLoggerNilWriterDoesNotPanic(t *testing.T) {
	l := NewLogger("debug", nil, "somesecretvalue")
	l.Error("no writer here")
	l.Debugf("nor %s", "here")
}

func TestMaskHeadersNilAndNonCanonicalKeys(t *testing.T) {
	if got := MaskHeaders(nil); len(got) != 0 {
		t.Errorf("MaskHeaders(nil) = %v, want empty", got)
	}
	// Built as a bare map, so the key never passes through Header.Set's
	// canonicalisation. Lookup must canonicalise it, or the value reaches the
	// log in the clear.
	h := http.Header{"authorization": {"Basic rawlowercasekeycredential"}}
	if got := MaskHeaders(h)["authorization"]; strings.Contains(got, "rawlowercasekeycredential") {
		t.Errorf("a non-canonical key bypassed masking: %q", got)
	}
}

// Log lines are the audit trail. A message carrying a newline could end one
// entry and forge the next, complete with a level prefix, so line breaks are
// rendered as their two-character escapes rather than written raw.
func TestEmitRendersLineBreaksAsEscapes(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("info", &buf)
	l.Error("upstream body:\nERROR forged entry\r\nmore")

	out := buf.String()
	if got := strings.Count(out, "\n"); got != 1 {
		t.Fatalf("wrote %d lines, want exactly 1: %q", got, out)
	}
	if strings.Contains(out, "\r") {
		t.Errorf("a raw carriage return survived: %q", out)
	}
	if !strings.Contains(out, `upstream body:\nERROR forged entry\r\nmore`) {
		t.Errorf("line breaks must be rendered as the literal escapes: %q", out)
	}
	if !strings.HasPrefix(out, "ERROR upstream body:") {
		t.Errorf("only the genuine level prefix may start the line: %q", out)
	}
}

// Redact feeds text that reaches the model. A partial mask still shows eight
// characters of a credential, and eight characters is a useful head start on
// guessing the rest, so the tool-facing form is a fixed marker instead.
func TestRedactReplacesSecretsWithAFixedMarker(t *testing.T) {
	const token = "ATATT3xFfGF0-fixture-token-value"
	basic := BasicCredential("user@example.com", token)
	l := NewLogger("info", &bytes.Buffer{}, token, basic)

	got := l.Redact("401: token " + token + " and Authorization: Basic " + basic + " rejected")
	want := "401: token [REDACTED] and Authorization: Basic [REDACTED] rejected"
	if got != want {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	for _, leak := range []string{token, basic, token[:4], token[len(token)-4:], basic[:4], basic[len(basic)-4:], "*"} {
		if strings.Contains(got, leak) {
			t.Errorf("Redact leaked %q: %q", leak, got)
		}
	}
}

// Mask keeps its recognisable shape for header values in debug logs, and Redact
// does not inherit it: the two are separate on purpose.
func TestRedactAndMaskAreDistinct(t *testing.T) {
	const token = "recognisable-token-value-0123"
	l := NewLogger("info", &bytes.Buffer{}, token)
	if l.Redact(token) == Mask(token) {
		t.Errorf("Redact must not produce the partial mask %q", Mask(token))
	}
	if !strings.HasPrefix(Mask(token), "reco") {
		t.Errorf("Mask must keep its current behaviour: %q", Mask(token))
	}
}
