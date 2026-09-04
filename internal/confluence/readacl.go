package confluence

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OxCom/atlassian-mcp-lite/internal/core"
)

// Read authorization for Confluence. ATLAS_READ_SPACES is enforced here and
// nowhere else, in this process rather than by trusting the CQL a caller
// supplies: a filter the model writes is a filter the model can leave out.

// deniedRead is the message a refused read returns. It names only the page id
// the caller already had, never the space the page turned out to be in and
// never its title or body.
func deniedRead(resource string) error {
	return fmt.Errorf("access denied: %s is not in a Confluence space permitted by the configured read allowlist (ATLAS_READ_SPACES)", resource)
}

// applyReadSpaceFilter ANDs the read allowlist onto the caller's CQL.
//
// The caller's query is not inspected for a space clause of its own: an honest
// one loses nothing by the extra condition, and a dishonest one is exactly
// what this exists to defeat.
func applyReadSpaceFilter(cql string, spaces []string) (string, error) {
	if len(spaces) == 0 {
		return cql, nil
	}
	clause, err := core.InClause(fieldSpace, spaces)
	if err != nil {
		return "", fmt.Errorf("ATLAS_READ_SPACES: %w", err)
	}
	// Refused rather than repaired, for the reason the Jira twin gives: an
	// unbalanced query wraps into a disjunction the restriction does not bind.
	restricted, err := core.RestrictQuery(cql, clause)
	if err != nil {
		return "", fmt.Errorf("cql: %w", err)
	}
	return restricted, nil
}

// authorizeReadSpaceID refuses a read of content in a space outside the read
// allowlist. The space is the one the page reports now, so a page moved out of
// an allowed space stops being readable the moment it moves.
//
// It fails closed: a space id that cannot be resolved to a key is not
// something an allowlist can approve.
func (m module) authorizeReadSpaceID(ctx context.Context, resource, spaceID string) error {
	if !m.cfg.RestrictsReadSpaces() {
		return nil
	}
	key, err := m.spaceKeyFor(ctx, spaceID)
	if err != nil {
		return err
	}
	if !m.cfg.AllowReadSpace(key) {
		return deniedRead(resource)
	}
	return nil
}

// deniedWriteSpace renders a write refusal for a page whose owning space is
// not writable. The space is named only when the read allowlist covers it:
// otherwise the refusal would report which space a page this deployment may
// not read now lives in. With no read allowlist AllowReadSpace is true and the
// message is unchanged.
func (m module) deniedWriteSpace(tool, key string) error {
	if !m.cfg.AllowReadSpace(key) {
		return fmt.Errorf("%s: writes to that page's space are not permitted by ATLAS_WRITE_SPACES", tool)
	}
	return fmt.Errorf("%s: writes to space %s are not permitted by ATLAS_WRITE_SPACES", tool, quoteKey(key))
}

// readableSpaceOfPage reports whether the read allowlist covers the space that
// owns a page. It is for deciding what a write result or a write error may
// echo back, never for authorising a read: a resolution failure answers "not
// readable", which is the safe answer for a disclosure question and the wrong
// one for an access question.
//
// With no read allowlist the answer is yes without a request, so an
// unrestricted deployment behaves exactly as it did.
func (m module) readableSpaceOfPage(ctx context.Context, pageID string) bool {
	if !m.cfg.RestrictsReadSpaces() {
		return true
	}
	key, err := m.spaceKeyForPage(ctx, pageID)
	if err != nil {
		return false
	}
	return m.cfg.AllowReadSpace(key)
}

// spaceKeyTTL bounds how long a resolved space key is reused, and
// spaceKeyCacheMax how many are held.
//
// The mapping is stable but not immutable: an administrator can rename a space
// key, and both allowlists key on the name. A cache with no expiry would keep
// a renamed space readable — and writable, since the write path shares this
// memo — until the process restarted, and would keep the space under its new
// key denied for just as long. Ten minutes keeps the per-read request cost at
// effectively zero while bounding how long a rename can be wrong; the entry
// cap bounds the map on a site with many spaces, since ids come from
// Confluence and a long-running process reads whatever it is pointed at.
const (
	spaceKeyTTL      = 10 * time.Minute
	spaceKeyCacheMax = 512
)

// spaceKeyCache memoises the space id to space key lookup.
//
// The read path hits it on every page: without the cache an allowlisted
// deployment would pay one extra request per read. It is a pointer field on
// the module so the copies the SDK makes of the handler receiver share one
// map, and it is guarded by a mutex because concurrent tool calls are the
// normal case.
//
// Only successful resolutions are stored: caching a failure would turn one
// transient error into a lasting refusal.
type spaceKeyCache struct {
	mu   sync.Mutex
	keys map[string]spaceKeyEntry
	// now is the clock, injectable so the expiry has a test that does not
	// sleep.
	now func() time.Time
}

type spaceKeyEntry struct {
	key      string
	resolved time.Time
}

func newSpaceKeyCache() *spaceKeyCache {
	return &spaceKeyCache{keys: make(map[string]spaceKeyEntry), now: time.Now}
}

func (c *spaceKeyCache) get(id string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.keys[id]
	if !ok {
		return "", false
	}
	if c.now().Sub(entry.resolved) >= spaceKeyTTL {
		delete(c.keys, id)
		return "", false
	}
	return entry.key, true
}

func (c *spaceKeyCache) put(id, key string) {
	if c == nil || strings.TrimSpace(key) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.keys) >= spaceKeyCacheMax {
		// Full: drop everything rather than evicting one arbitrary entry. The
		// cache is a cost optimisation, so the worst outcome of a flush is one
		// extra request per space, and a policy with no bookkeeping cannot
		// hold an entry past its TTL by accident.
		c.keys = make(map[string]spaceKeyEntry, spaceKeyCacheMax)
	}
	c.keys[id] = spaceKeyEntry{key: key, resolved: c.now()}
}
