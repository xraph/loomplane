//go:build integration

package cloudinfra

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
)

func TestRegistryFailureFinalizerCancelsHeldRow(t *testing.T) {
	pg := routeBindingDatabase(t)
	store := NewPgStore(pg)
	now := time.Now().UTC()
	candidate := &connections.Connection{ID: "registry", OrgID: "org", Cloud: connections.Kubernetes, Name: "Synthetic registry", Status: connections.StatusReady, Details: map[string]string{"registry_hostname": "registry.example.test"}, CreatedAt: now, UpdatedAt: now}
	if err := connections.NewPgStore(pg).CreateConnection(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}
	state := newRegistryState(candidate, now)
	if err := store.SaveRegistryState(t.Context(), state); err != nil {
		t.Fatal(err)
	}
	locked, err := pg.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(func() { _ = locked.Rollback() }) }
	t.Cleanup(unlock)
	if _, err = locked.Exec(t.Context(), `UPDATE loomplane_registry_states SET status=status WHERE org_id='org' AND connection_id='registry'`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	done := make(chan struct{})
	t.Cleanup(func() {
		unlock()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("registry finalizer did not release after test row unlock")
		}
	})
	started := time.Now()
	go func() {
		defer close(done)
		svc := NewService(Deps{})
		result <- svc.reconcileManagedRegistry(ctx, store, registryShutdownCredentials{cancel: cancel}, candidate, nil)
	}()
	select {
	case err = <-result:
	case <-time.After(8 * time.Second):
		t.Fatal("registry failure persistence remained blocked on the held row")
	}
	if !errors.Is(err, ErrInvalid) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected original failure and failed persistence: %v", err)
	}
	if time.Since(started) > 8*time.Second {
		t.Fatal("registry finalizer exceeded the supported cleanup window")
	}
	checkCtx, cancelCheck := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelCheck()
	for {
		var waiting int
		if err = pg.QueryRow(checkCtx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 0 {
			break
		}
		select {
		case <-checkCtx.Done():
			t.Fatal("cancelled registry write retained its database wait")
		case <-time.After(10 * time.Millisecond):
		}
	}
	persisted, readErr := store.RegistryState(checkCtx, candidate.OrgID, candidate.ID)
	if readErr != nil || persisted.Status != "waiting" {
		t.Fatalf("failed save was treated as persisted: %+v %v", persisted, readErr)
	}
}
