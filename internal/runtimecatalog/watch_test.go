package runtimecatalog

import (
	"context"
	"errors"
	"testing"

	domain "github.com/xraph/loomplane/pkg/federation"
)

type catalogWake struct {
	org    string
	wake   chan struct{}
	closed bool
}

func (b *catalogWake) Watch(org string) (<-chan struct{}, func()) {
	b.org = org
	return b.wake, func() { b.closed = true }
}
func TestCatalogWatchRevocationAndScope(t *testing.T) {
	c, _, agent, ep := catalogFixture(t)
	observe(t, c, agent, 1, ep)
	bus := &catalogWake{wake: make(chan struct{}, 1)}
	count := 0
	err := c.Watch(t.Context(), agent, bus, func(_ context.Context, s *domain.Snapshot) error {
		count++
		if len(s.Endpoints) != 1 || s.NextCursor != "" || !s.Full {
			t.Fatalf("partial snapshot: %+v", s)
		}
		c.authorize = nil
		bus.wake <- struct{}{}
		return nil
	})
	if !errors.Is(err, ErrUnauthorized) || count != 1 || !bus.closed || bus.org != agent.OrgID {
		t.Fatalf("watch outlived authority: %v, %+v", err, bus)
	}
}
func TestCatalogWatchAssemblesAtomicPages(t *testing.T) {
	c, _, agent, ep := catalogFixture(t)
	batch := domain.ObservationBatch{Agent: agent, DestinationGeneration: 1, Sequence: 1}
	for i := 0; i < MaxPage+1; i++ {
		e := ep
		e.ID = string(rune('a'+i/26)) + string(rune('a'+i%26))
		batch.Endpoints = append(batch.Endpoints, e)
	}
	if err := c.Observe(t.Context(), agent, batch); err != nil {
		t.Fatal(err)
	}
	bus := &catalogWake{wake: make(chan struct{}, 1)}
	stop := errors.New("transport closed")
	err := c.Watch(t.Context(), agent, bus, func(_ context.Context, s *domain.Snapshot) error {
		if len(s.Endpoints) != MaxPage+1 || s.NextCursor != "" {
			t.Fatalf("incomplete atomic snapshot: %+v", s)
		}
		return stop
	})
	if !errors.Is(err, stop) || !bus.closed {
		t.Fatalf("transport failure ignored: %v", err)
	}
}
