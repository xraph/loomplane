//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/fleets"
)

func TestFleetCreateCancelBeforeAdmission(t *testing.T) {
	for _, queued := range []bool{false, true} {
		name := "sequential"
		if queued {
			name = "blocked-admission"
		}
		t.Run(name, func(t *testing.T) {
			f := newFleetAuthorityFixture(t)
			in := f.input("a")
			in.ReleaseID = f.request.ReleaseID
			result, err := f.service.CreateInstance(t.Context(), f.principal, f.scope.FleetID, in)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			if queued {
				blocker, beginErr := f.sql.Begin(ctx)
				if beginErr != nil {
					t.Fatal(beginErr)
				}
				defer func() { _ = blocker.Rollback(context.Background()) }()
				if _, err = blocker.Exec(ctx, `SELECT id FROM loomplane_fleets WHERE id=$1 FOR UPDATE`, f.scope.FleetID); err != nil {
					t.Fatal(err)
				}
				cancelled := make(chan error, 1)
				go func() {
					_, cancelErr := f.service.CancelOperation(ctx, f.principal, result.Operation.ID, 1)
					cancelled <- cancelErr
				}()
				waitFleetAdmissionLock(ctx, t, blocker, "SELECT id FROM loomplane_fleets%FOR UPDATE")
				admitted := make(chan error, 1)
				go func() {
					_, admitErr := f.service.AuthorizeOperation(ctx, f.principal, f.scope, f.request)
					admitted <- admitErr
				}()
				waitFleetAdmissionLock(ctx, t, blocker, "SELECT%FROM loomplane_fleet%FOR SHARE%")
				if err = blocker.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case err = <-cancelled:
					if err != nil {
						t.Fatalf("cancel first: %v", err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				select {
				case err = <-admitted:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			} else {
				if _, err = f.service.CancelOperation(ctx, f.principal, result.Operation.ID, 1); err != nil {
					t.Fatal(err)
				}
				_, err = f.service.AuthorizeOperation(ctx, f.principal, f.scope, f.request)
			}
			if !errors.Is(err, fleets.ErrConflict) {
				t.Fatalf("cancelled target admitted authority: %v", err)
			}
			var count int
			if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_fleet_authorities WHERE fleet_id=$1`, f.scope.FleetID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("cancelled target consumed grant quota: %d", count)
			}
		})
	}
}

func waitFleetAdmissionLock(ctx context.Context, t *testing.T, tx pgx.Tx, query string) {
	t.Helper()
	timer := time.NewTicker(10 * time.Millisecond)
	defer timer.Stop()
	for {
		if _, err := tx.Exec(ctx, `SELECT pg_stat_clear_snapshot()`); err != nil {
			t.Fatal(err)
		}
		var waiting bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE $1)`, query).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-timer.C:
		}
	}
}

func TestFleetCreateAdmissionLocksParentBeforeStage(t *testing.T) {
	f := newFleetAuthorityFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	blocker, err := f.sql.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err = blocker.Exec(ctx, `SELECT id FROM loomplane_fleet_stages WHERE id=$1 FOR UPDATE`, f.stage.ID); err != nil {
		t.Fatal(err)
	}
	admitted := make(chan error, 1)
	go func() {
		_, admitErr := f.service.AuthorizeOperation(ctx, f.principal, f.scope, f.request)
		admitted <- admitErr
	}()
	waitFleetAdmissionLock(ctx, t, blocker, "SELECT%FROM loomplane_fleet_stages%FOR SHARE%")
	reserved := make(chan error, 1)
	go func() {
		_, reserveErr := f.store.ReserveInstance(ctx, f.scope, f.input("b"))
		reserved <- reserveErr
	}()
	waitFleetAdmissionLock(ctx, t, blocker, "SELECT%FROM loomplane_fleet%FOR UPDATE")
	var waitingOnParent bool
	if err = blocker.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE 'SELECT status FROM loomplane_fleets%FOR UPDATE')`).Scan(&waitingOnParent); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, done := range []<-chan error{admitted, reserved} {
		select {
		case err = <-done:
			if err != nil {
				t.Errorf("concurrent reservation/admission: %v", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if !waitingOnParent {
		t.Fatal("admission reached stage lock without fencing the fleet parent")
	}
}
