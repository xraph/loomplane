package dbgateway

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sync"
)

// Every gateway instance polls for its snapshot about once a second, and
// almost every poll finds nothing new. Compiling reads each route's address
// from Vault, decrypting it, and seals each credential afresh, so a poll
// first looks at what the snapshot is compiled from, cheaply: the route rows,
// the gateway's sealing key and which version of each address Vault holds,
// without reading the addresses themselves. When that is what the last
// compile saw, and the stored snapshot is still that compile's at the very
// version the gateway holds, nothing changed. A gateway ahead of the stored
// version, as after a database restore, is not that case. Anything else compiles as before, so
// a snapshot the service signs is always compiled and sealed for that poll.

// URLVersions is a ResourceURLs that can say which version of an address it
// holds without reading the address: a string that changes whenever the
// address is written again. A resource with none gives ErrNoURL. Without it
// the service compiles on every poll.
type URLVersions interface {
	URLVersion(ctx context.Context, org, environmentID, resource string) (string, error)
}

// maxCompiled bounds the compiled-inputs cache across every gateway.
const maxCompiled = 10_000

// compiledCache remembers, per gateway, the inputs of its last compile and
// the digest they gave.
type compiledCache struct {
	mu        sync.Mutex
	byGateway map[string]compiledEntry
}

type compiledEntry struct{ inputs, digest string }

func newCompiledCache() *compiledCache {
	return &compiledCache{byGateway: map[string]compiledEntry{}}
}

// digest returns the digest compiled from inputs, if those were the
// gateway's last.
func (c *compiledCache) digest(gatewayID, inputs string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byGateway[gatewayID]
	if !ok || e.inputs != inputs {
		return "", false
	}
	return e.digest, true
}

func (c *compiledCache) put(gatewayID, inputs, digest string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.byGateway[gatewayID]; !ok && len(c.byGateway) >= maxCompiled {
		for id := range c.byGateway {
			delete(c.byGateway, id)
			break
		}
	}
	c.byGateway[gatewayID] = compiledEntry{inputs: inputs, digest: digest}
}

// inputs is a hash of everything gw's snapshot is compiled from, or false
// when it can't be had cheaply or a read failed.
func (s *Service) inputs(ctx context.Context, gw *Gateway) (string, bool) {
	versions, ok := s.urls.(URLVersions)
	if !ok {
		return "", false
	}
	routes, err := s.store.Routes(ctx, gw.OrgID, gw.ID)
	if err != nil {
		return "", false
	}
	h := sha256.New()
	field := func(b []byte) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	field([]byte(gw.ID))
	if gw.SealingPublicKey != nil {
		field(gw.SealingPublicKey.Bytes())
	} else {
		field(nil)
	}
	for _, r := range routes {
		v, err := versions.URLVersion(ctx, gw.OrgID, gw.EnvironmentID, r.Resource)
		switch {
		case errors.Is(err, ErrNoURL):
			v = "none"
		case err != nil:
			return "", false
		default:
			v = "v:" + v
		}
		for _, f := range []string{r.ID, r.Resource, r.State, string(r.Salt), v} {
			field([]byte(f))
		}
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// unchanged reports whether gw has nothing newer than after, judged from
// inputs alone. A failed read says no, and the caller compiles.
func (s *Service) unchanged(ctx context.Context, gw *Gateway, inputs string, after uint64) bool {
	digest, ok := s.compiled.digest(gw.ID, inputs)
	if !ok {
		return false
	}
	version, stored, err := s.store.LatestSnapshot(ctx, gw.OrgID, gw.ID)
	return err == nil && stored == digest && version == after
}
