//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type fleetAuth struct{}

func (fleetAuth) Require(_ context.Context, p security.Principal, permission security.Permission, _ string) error {
	if p.UserID != "usr" || p.OrgID != "org" || (p.ReadOnly && !permission.Reads()) {
		return security.ErrForbidden
	}
	return nil
}

type fleetFixture struct {
	db       *pgdriver.PgDB
	store    *fleets.PgStore
	service  *fleets.Service
	projects *projects.PgStore
	sql      *pgx.Conn
	scope    fleets.FleetScope
	stage    *fleet.Stage
}

func newFleetFixture(t *testing.T, maximum int) fleetFixture {
	t.Helper()
	_, sql, db := cloudSyncDatabase(t)
	ps := projects.NewPgStore(db)
	store := fleets.NewPgStore(db)
	service := fleets.NewService(fleets.Deps{Store: store, Projects: ps, Authz: fleetAuth{}})
	p := security.Principal{UserID: "usr", OrgID: "org"}
	f, err := service.CreateFleet(t.Context(), p, "proj")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := service.CreateStage(t.Context(), p, f.ID, fleet.CreateStageInput{Name: "production", EnvironmentKind: "production", AllowedCloudIDs: []string{"cloud"}, MaxInstances: maximum})
	if err != nil {
		t.Fatal(err)
	}
	return fleetFixture{db: db, store: store, service: service, projects: ps, sql: sql, scope: fleets.FleetScope{OrgID: "org", ProjectID: "proj", FleetID: f.ID}, stage: stage}
}
func (f fleetFixture) input(ref string) fleet.CreateInstanceInput {
	return fleet.CreateInstanceInput{StageID: f.stage.ID, ReleaseID: "release", ProfileID: "small", ExternalRef: ref, IdempotencyKey: "create-" + ref}
}

func TestFleetScopeIsolation(t *testing.T) {
	f := newFleetFixture(t, 2)
	if _, err := f.store.GetFleet(t.Context(), "other", f.scope.FleetID); !errors.Is(err, fleets.ErrNotFound) {
		t.Fatalf("foreign fleet: %v", err)
	}
	scope := f.scope
	scope.OrgID = "other"
	if _, err := f.store.ReserveInstance(t.Context(), scope, f.input("a")); !errors.Is(err, fleets.ErrNotFound) {
		t.Fatalf("foreign reservation: %v", err)
	}
	p := security.Principal{OrgID: "org", UserID: "usr", ReadOnly: true}
	if _, err := f.service.CreateFleet(t.Context(), p, "proj"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("read-only create: %v", err)
	}
	if _, err := f.service.CreateStage(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, f.scope.FleetID, fleet.CreateStageInput{Name: "wrong", EnvironmentKind: "production", AllowedCloudIDs: []string{"missing"}, MaxInstances: 1}); !errors.Is(err, fleets.ErrNotFound) {
		t.Fatalf("unknown Cloud: %v", err)
	}
	pjt, err := f.projects.GetProject(t.Context(), "org", "proj")
	if err != nil || pjt.DeploymentMode != "fleet" {
		t.Fatalf("project mode: %+v %v", pjt, err)
	}
}

func TestFleetExternalRefReplay(t *testing.T) {
	f := newFleetFixture(t, 1)
	in := f.input("a")
	a, err := f.store.ReserveInstance(t.Context(), f.scope, in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.store.ReserveInstance(t.Context(), f.scope, in)
	if err != nil {
		t.Fatal(err)
	}
	if a.Instance.ID != b.Instance.ID || a.Operation.ID != b.Operation.ID {
		t.Fatalf("replay created new identities: %+v %+v", a, b)
	}
	instance, err := f.store.ManagedEnvironment(t.Context(), "org", a.Instance.EnvironmentID)
	if err != nil || instance.ID != a.Instance.ID {
		t.Fatalf("mapping: %+v %v", instance, err)
	}
	if _, err = f.store.ManagedEnvironment(t.Context(), "other", a.Instance.EnvironmentID); !errors.Is(err, fleets.ErrNotFound) {
		t.Fatalf("mapping leaked: %v", err)
	}
	if _, err = f.store.ReserveInstance(t.Context(), f.scope, f.input("b")); !errors.Is(err, fleets.ErrCapacity) {
		t.Fatalf("quota bypass: %v", err)
	}
}

func TestFleetExternalRefBodyConflict(t *testing.T) {
	f := newFleetFixture(t, 5)
	in := f.input("a")
	if _, err := f.store.ReserveInstance(t.Context(), f.scope, in); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*fleet.CreateInstanceInput){func(v *fleet.CreateInstanceInput) { v.ProfileID = "large" }, func(v *fleet.CreateInstanceInput) { v.ExternalRef = "b" }, func(v *fleet.CreateInstanceInput) { v.IdempotencyKey = "new-key" }} {
		altered := in
		change(&altered)
		if _, err := f.store.ReserveInstance(t.Context(), f.scope, altered); !errors.Is(err, fleets.ErrConflict) {
			t.Fatalf("request accepted: %+v %v", altered, err)
		}
	}
	var count int
	if err := f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_fleet_instances`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("conflict created instances: %d %v", count, err)
	}
	if err := f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_environments`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("conflict leaked environments: %d %v", count, err)
	}
}

func TestFleetEnvironmentUnique(t *testing.T) {
	f := newFleetFixture(t, 5)
	a, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("b"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_instances SET environment_id=$1 WHERE id=$2`, a.Instance.EnvironmentID, b.Instance.ID); err == nil {
		t.Fatal("environment reassigned")
	}
	if err = f.projects.DeleteEnvironment(t.Context(), "org", "proj", a.Instance.EnvironmentID); !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("managed environment delete: %v", err)
	}
	if err = f.projects.DeleteProject(t.Context(), "org", "proj"); !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("fleet project delete: %v", err)
	}
	if _, err = f.sql.Exec(t.Context(), `DELETE FROM loomplane_environments WHERE id=$1`, a.Instance.EnvironmentID); err == nil {
		t.Fatal("database allowed environment deletion")
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_environments SET org_id='other' WHERE id=$1`, a.Instance.EnvironmentID); err == nil {
		t.Fatal("database allowed cross-org environment")
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_projects SET deployment_mode='application' WHERE id='proj'`); err == nil {
		t.Fatal("fleet mode cleared")
	}
}

func TestFleetReserveConcurrent(t *testing.T) {
	f := newFleetFixture(t, 1)
	var wg sync.WaitGroup
	results := make(chan *fleet.OperationResult, 12)
	failures := make(chan error, 12)
	for range 12 {
		wg.Go(func() {
			r, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("a"))
			if err != nil {
				failures <- err
				return
			}
			results <- r
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var identity string
	for r := range results {
		if identity != "" && identity != r.Instance.ID {
			t.Fatal("concurrent duplicate instance")
		}
		identity = r.Instance.ID
	}
	var count int
	if err := f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_fleet_operations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate operations: %d %v", count, err)
	}
}

func TestFleetReserveConcurrentQuota(t *testing.T) {
	f := newFleetFixture(t, 2)
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for n := range 8 {
		wg.Go(func() {
			_, err := f.store.ReserveInstance(t.Context(), f.scope, f.input(fmt.Sprintf("customer-%d", n)))
			failures <- err
		})
	}
	wg.Wait()
	close(failures)
	succeeded := 0
	for err := range failures {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, fleets.ErrCapacity) {
			t.Fatal(err)
		}
	}
	if succeeded != 2 {
		t.Fatalf("admitted %d, want 2", succeeded)
	}
	var count int
	if err := f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_environments`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("failed admission leaked environments: %d %v", count, err)
	}
}

func TestFleetReserveRollback(t *testing.T) {
	f := newFleetFixture(t, 1)
	_, err := f.sql.Exec(t.Context(), `CREATE FUNCTION reject_fleet_operation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected operation storage failure'; END $$; CREATE TRIGGER reject_fleet_operation BEFORE INSERT ON loomplane_fleet_operations FOR EACH ROW EXECUTE FUNCTION reject_fleet_operation();`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ReserveInstance(t.Context(), f.scope, f.input("a")); err == nil {
		t.Fatal("fault did not interrupt reservation")
	}
	var instances, environments int
	if err = f.sql.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM loomplane_fleet_instances),(SELECT count(*) FROM loomplane_environments)`).Scan(&instances, &environments); err != nil || instances != 0 || environments != 2 {
		t.Fatalf("partial reservation survived: %d %d %v", instances, environments, err)
	}
	if _, err = f.sql.Exec(t.Context(), `DROP TRIGGER reject_fleet_operation ON loomplane_fleet_operations`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ReserveInstance(t.Context(), f.scope, f.input("a")); err != nil {
		t.Fatalf("failed transaction consumed quota: %v", err)
	}
}
