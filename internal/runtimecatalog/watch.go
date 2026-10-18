package runtimecatalog

import (
	"context"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// WakeSource is implemented by realtime.Bus. Its one-slot, org-scoped wakeups
// carry no endpoint data; the catalog authorizes every durable read separately.
type WakeSource interface {
	Watch(org string) (<-chan struct{}, func())
}

// Watch sends complete snapshots, including on reconnect. A consumer must apply
// each snapshot atomically and enforce its expiry locally. Send must honor ctx;
// stream transport and peer authentication are supplied by the agent endpoint.
func (c *Catalog) Watch(ctx context.Context, agent domain.AgentIdentity, bus WakeSource, send func(context.Context, *domain.Snapshot) error) error {
	if bus == nil || send == nil || !validScope(agent.Scope) {
		return ErrInvalid
	}
	wake, unsubscribe := bus.Watch(agent.OrgID)
	defer unsubscribe()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Bound both snapshot assembly and transport by the endpoint lease. There
		// is no unbounded per-client queue, and no snapshot outlives its first page.
		delivery, cancel := context.WithTimeout(ctx, EndpointLease)
		snapshot, err := c.completeSnapshot(delivery, agent)
		if err == nil {
			remaining := snapshot.ExpiresAt.Sub(c.now())
			if remaining <= 0 {
				err = ErrCursorExpired
			} else {
				var stop context.CancelFunc
				delivery, stop = context.WithTimeout(delivery, remaining)
				err = send(delivery, snapshot)
				stop()
			}
		}
		cancel()
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-wake:
			if !ok {
				return ErrCursorExpired
			}
		case <-ticker.C:
		}
	}
}

func (c *Catalog) completeSnapshot(ctx context.Context, agent domain.AgentIdentity) (*domain.Snapshot, error) {
	out, err := c.Snapshot(ctx, agent, domain.SnapshotRequest{Agent: agent})
	if err != nil {
		return nil, err
	}
	for out.NextCursor != "" {
		page, err := c.Snapshot(ctx, agent, domain.SnapshotRequest{Agent: agent, Cursor: out.NextCursor})
		if err != nil {
			return nil, err
		}
		if page.Revision != out.Revision || !page.ExpiresAt.Equal(out.ExpiresAt) {
			return nil, ErrCursorExpired
		}
		out.Endpoints = append(out.Endpoints, page.Endpoints...)
		out.Tombstones = append(out.Tombstones, page.Tombstones...)
		if len(out.Endpoints)+len(out.Tombstones) > MaxSnapshot {
			return nil, ErrLimit
		}
		out.NextCursor = page.NextCursor
	}
	return out, nil
}
