//go:build cloud

package billing

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/metering"
)

// What a refused workspace reads, one sentence per reason.
const (
	reasonPending = "Add a card in Settings > Billing to start running anything on Loomplane Cloud."
	reasonPastDue = "Your last payment failed. Update your card in Settings > Billing."
	reasonLimited = "This workspace reached its spending limit for the month. Raise it in Settings > Billing."
	reasonNoAddOn = "Turn on the AI add-on in Settings > Billing, or add your own model key."
)

// aiCacheFor is how long the gate keeps a workspace's account for
// KindAIPlatform checks, which every model call makes. Deploys and builds
// always read the account fresh.
const aiCacheFor = 15 * time.Second

// aiCacheSweepAt is the cache size past which a store first drops the
// entries that have expired.
const aiCacheSweepAt = 1024

// Gate returns billing's metering.Gate.
func (s *Service) Gate() metering.Gate { return s.gate }

// gate is billing's metering.Gate: it refuses work by the workspace's
// account status. Its zero cache is ready to use.
type gate struct {
	account func(context.Context, string) (Account, error)
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]cachedAccount
	// gen counts the writes forgotten. A read that started before a write
	// doesn't store what it read, so a stale account never outlives the
	// write that changed it.
	gen uint64
}

type cachedAccount struct {
	a     Account
	until time.Time
}

var _ metering.Gate = (*gate)(nil)

// CanRun says whether the workspace may run work of this kind. A Blocked
// refusal carries the sentence the person reads; any other error (the
// account couldn't be read) refuses too.
//
//	status           deploy/build      deploy/build      ai_platform
//	                 platformOwned=f   platformOwned=t
//	pending_payment  refused           refused           refused
//	past_due         allowed           refused           refused
//	limited          allowed           refused           refused
//	active           allowed           allowed           allowed with the AI add-on
func (g *gate) CanRun(ctx context.Context, orgID string, kind metering.Kind, platformOwned bool) error {
	var (
		a   Account
		err error
	)
	switch kind {
	case metering.KindDeploy, metering.KindBuild:
		// Fresh every time, so the approval right after a card passes.
		a, err = g.account(ctx, orgID)
	case metering.KindAIPlatform:
		a, err = g.cached(ctx, orgID)
	default:
		return fmt.Errorf("billing: the gate doesn't know work of kind %q", kind)
	}
	if err != nil {
		return fmt.Errorf("billing: can't tell whether %s may run %s: %w", orgID, kind, err)
	}
	return decide(a, kind, platformOwned)
}

// decide applies the status matrix to one account.
func decide(a Account, kind metering.Kind, platformOwned bool) error {
	// What spends the platform's money: its own capacity, and AI on its key.
	spends := platformOwned || kind == metering.KindAIPlatform
	switch a.Status {
	case StatusPendingPayment:
		return &metering.Blocked{Reason: reasonPending}
	case StatusPastDue:
		if spends {
			return &metering.Blocked{Reason: reasonPastDue}
		}
		return nil
	case StatusLimited:
		if spends {
			return &metering.Blocked{Reason: reasonLimited}
		}
		return nil
	case StatusActive:
		if kind == metering.KindAIPlatform && !a.AIEnabled {
			return &metering.Blocked{Reason: reasonNoAddOn}
		}
		return nil
	default:
		return fmt.Errorf("billing: account %s has unknown status %q", a.OrgID, a.Status)
	}
}

// cached is the workspace's account, read at most once every 15 seconds.
// An add-on turned off expires the entry when it ends, not up to 15
// seconds later. A failed read is never kept.
func (g *gate) cached(ctx context.Context, orgID string) (Account, error) {
	now := g.now()
	g.mu.Lock()
	if c, ok := g.cache[orgID]; ok && now.Before(c.until) {
		g.mu.Unlock()
		return c.a, nil
	}
	gen := g.gen
	g.mu.Unlock()

	a, err := g.account(ctx, orgID)
	if err != nil {
		return Account{}, err
	}
	until := now.Add(aiCacheFor)
	if a.AIEndsAt != nil && a.AIEndsAt.After(now) && a.AIEndsAt.Before(until) {
		until = *a.AIEndsAt
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gen != gen {
		return a, nil // a write came in meanwhile; the next check reads again
	}
	if g.cache == nil {
		g.cache = map[string]cachedAccount{}
	}
	if len(g.cache) >= aiCacheSweepAt {
		for k, c := range g.cache {
			if !now.Before(c.until) {
				delete(g.cache, k)
			}
		}
	}
	g.cache[orgID] = cachedAccount{a: a, until: until}
	return a, nil
}

// forget drops the workspace's entry and keeps any read in flight from
// storing what it read.
func (g *gate) forget(orgID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.cache, orgID)
	g.gen++
}
