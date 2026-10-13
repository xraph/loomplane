//go:build integration

package realtime

import (
	"context"
	"testing"
	"time"
)

func TestBus_broadcastsCommittedEventsToIndependentReplicas(t *testing.T) {
	store, conn, dsn := eventDatabase(t)
	buses := make([]*Bus, 0, 2)
	for _, replica := range []string{"one", "two"} {
		bus, err := NewBus(dsn, replica)
		if err != nil {
			t.Fatal(err)
		}
		bus.CatchupInterval = time.Hour
		bus.Warn = func(err error) { t.Logf("Conduit publish: %v", err) }
		if err = bus.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = bus.Stop(context.Background()) })
		buses = append(buses, bus)
	}
	watchers := make([]<-chan struct{}, 0, len(buses))
	for _, bus := range buses {
		wake, cancel := bus.Watch("org_1")
		t.Cleanup(cancel)
		watchers = append(watchers, wake)
	}
	if _, err := store.Append(t.Context(), "org_1", "fixture.changed", []byte(`{"id":"committed"}`)); err != nil {
		t.Fatal(err)
	}
	for _, wake := range watchers {
		select {
		case <-wake:
		case <-time.After(5 * time.Second):
			t.Fatal("committed event did not reach both replicas")
		}
	}
	// Nothing is published while the transaction holds its cursor open.
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `SELECT loomplane_append_event('org_1','','fixture.changed','rolled-back','{}')`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, wake := range watchers {
		select {
		case <-wake:
			t.Fatal("uncommitted event was delivered")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func TestBus_catchupWakesReadersAfterAMissedNotification(t *testing.T) {
	_, _, dsn := eventDatabase(t)
	bus, err := NewBus(dsn, "catchup")
	if err != nil {
		t.Fatal(err)
	}
	bus.CatchupInterval = 30 * time.Millisecond
	if err = bus.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer bus.Stop(context.Background())
	wake, cancel := bus.Watch("org_1")
	defer cancel()
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("no bounded durable catchup wake-up")
	}
}
