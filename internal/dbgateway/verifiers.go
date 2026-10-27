package dbgateway

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/xraph/loomplane/internal/dbgateway/plane"
)

// maxCachedVerifiers bounds the verifier cache across every gateway.
const maxCachedVerifiers = 10_000

// verifierCache keeps derived route verifiers, so a snapshot compiled again
// with nothing changed runs no PBKDF2. An entry is keyed by the route, its
// salt and a hash of the user and password, so a new password misses. Each
// compile of a gateway drops that gateway's entries it didn't use, which
// takes out routes that are gone and passwords that were replaced.
type verifierCache struct {
	mu        sync.Mutex
	byGateway map[string]map[string]plane.Verifier
	total     int
	// derive is plane.NewVerifier; tests count calls through it.
	derive func(role, password string, salt []byte, iterations int) plane.Verifier
}

func newVerifierCache() *verifierCache {
	return &verifierCache{byGateway: map[string]map[string]plane.Verifier{}, derive: plane.NewVerifier}
}

func verifierKey(r RouteRecord, user, password string) string {
	sum := sha256.Sum256([]byte(user + "\x00" + password))
	return r.ID + "\x00" + hex.EncodeToString(r.Salt) + "\x00" + hex.EncodeToString(sum[:])
}

// compileFor returns the verifierFunc for one compile of gatewayID, and done,
// which the caller runs after the compile to evict what it didn't use.
func (c *verifierCache) compileFor(gatewayID string) (verify verifierFunc, done func()) {
	used := map[string]plane.Verifier{}
	verify = func(r RouteRecord, user, password string) plane.Verifier {
		key := verifierKey(r, user, password)
		c.mu.Lock()
		v, ok := c.byGateway[gatewayID][key]
		c.mu.Unlock()
		if !ok {
			v = c.derive(user, password, r.Salt, scramIterations)
		}
		if len(v.StoredKey) > 0 {
			used[key] = v
		}
		return v
	}
	done = func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.total -= len(c.byGateway[gatewayID])
		delete(c.byGateway, gatewayID)
		if len(used) == 0 || len(used) > maxCachedVerifiers {
			// Nothing to keep, or one gateway alone is over the cap.
			return
		}
		for c.total+len(used) > maxCachedVerifiers {
			if !c.evictOne() {
				break
			}
		}
		c.byGateway[gatewayID] = used
		c.total += len(used)
	}
	return verify, done
}

// evictOne drops one entry of some gateway, and reports whether there was one.
// Called with c.mu held.
func (c *verifierCache) evictOne() bool {
	for id, entries := range c.byGateway {
		for key := range entries {
			delete(entries, key)
			c.total--
			if len(entries) == 0 {
				delete(c.byGateway, id)
			}
			return true
		}
		delete(c.byGateway, id)
	}
	return false
}

// size is how many verifiers are cached.
func (c *verifierCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}
