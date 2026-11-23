// Package relay reaches hosts through a runner pool. A server, cluster or
// Firecracker connection whose settings name via_pool_id dials its host
// through a runner of that pool, from inside the runner's network, instead
// of from Loomplane's. SSH and TLS run end to end through the runner, so it
// carries only ciphertext.
//
// The runner pool service registers itself with Set when it starts. Every
// parser that builds a client from a connection's settings asks DialFunc for
// its dialer, so all of a connection's checks, deploys and reads go the same
// way. A connection check calls CheckOwner, so a connection can only name a
// pool of its own organization; a pool never moves to another one.
package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sync/atomic"
)

// SettingKey is the connection setting that names the relay pool.
const SettingKey = "via_pool_id"

// Dialer reaches hosts through runner pools. The runner pool service
// satisfies it.
type Dialer interface {
	// DialVia opens TCP to address through an online runner of pool, in
	// the pool's own organization.
	DialVia(ctx context.Context, pool, address string) (net.Conn, error)
	// PoolOwner is the organization pool belongs to.
	PoolOwner(ctx context.Context, pool string) (string, error)
}

type holder struct{ d Dialer }

var current atomic.Pointer[holder]

// Set registers the dialer every relayed connection uses.
func Set(d Dialer) { current.Store(&holder{d: d}) }

// ErrUnavailable is a relayed connection in a process with no runner pools.
var ErrUnavailable = errors.New("relayed connections need runner pools, which aren't available here")

var poolID = regexp.MustCompile(`^rpool_[0-9a-z]{26}$`)

// ValidPool reports whether id is a runner pool's ID.
func ValidPool(id string) bool { return poolID.MatchString(id) }

// DialFunc is the dialer for a connection that relays through pool.
func DialFunc(pool string) func(ctx context.Context, network, address string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, fmt.Errorf("a relay carries TCP only, not %s", network)
		}
		h := current.Load()
		if h == nil || h.d == nil {
			return nil, ErrUnavailable
		}
		return h.d.DialVia(ctx, pool, address)
	}
}

// CheckOwner refuses a pool that isn't orgID's.
func CheckOwner(ctx context.Context, orgID, pool string) error {
	if !ValidPool(pool) {
		return fmt.Errorf("%s %q isn't a runner pool's ID", SettingKey, pool)
	}
	h := current.Load()
	if h == nil || h.d == nil {
		return ErrUnavailable
	}
	owner, err := h.d.PoolOwner(ctx, pool)
	if err != nil {
		return fmt.Errorf("the relay pool: %w", err)
	}
	if owner == "" || owner != orgID {
		return errors.New("the relay pool isn't one of this organization's")
	}
	return nil
}
