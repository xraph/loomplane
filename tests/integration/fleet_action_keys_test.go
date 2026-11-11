//go:build integration

package integration

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
)

func TestFleetActionIdempotencyIsTargetScoped(t *testing.T) {
	f := newFleetFixture(t, 2)
	first, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("second"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_operations SET status='succeeded' WHERE fleet_id=$1`, f.scope.FleetID); err != nil {
		t.Fatal(err)
	}
	insert := func(instance, kind, key string) error {
		_, e := f.sql.Exec(t.Context(), `INSERT INTO loomplane_fleet_operations(id,org_id,fleet_id,instance_id,kind,status,idempotency_key,request_hash,generation,created_at) VALUES($1,$2,$3,$4,$5,'succeeded',$6,'synthetic-hash',2,clock_timestamp())`, uuid.NewString(), f.scope.OrgID, f.scope.FleetID, instance, kind, key)
		return e
	}
	if err = insert(first.Instance.ID, "update", "shared-key"); err != nil {
		t.Fatal(err)
	}
	if err = insert(second.Instance.ID, "update", "shared-key"); err != nil {
		t.Fatalf("distinct target key collided: %v", err)
	}
	if err = insert(first.Instance.ID, "update", "shared-key"); err == nil {
		t.Fatal("same target operation duplicated")
	}
	if err = insert(first.Instance.ID, "suspend", "shared-key"); err != nil {
		t.Fatalf("different action key collided: %v", err)
	}
	if err = insert(second.Instance.ID, "create", "create-first"); err == nil {
		t.Fatal("fleet-wide create key was weakened")
	}
	step := fleetActionKeyMigration(t)
	err = step.Down(t.Context(), pgmigrate.New(f.db))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("downgrade must refuse accepted target-scoped keys: %v", err)
	}
	var indexCount int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM pg_indexes WHERE tablename='loomplane_fleet_operations' AND indexname IN ('loomplane_fleet_create_key','loomplane_fleet_action_key')`).Scan(&indexCount); err != nil || indexCount != 2 {
		t.Fatalf("refused downgrade changed indexes: %d %v", indexCount, err)
	}
	var id, hash string
	if err = f.sql.QueryRow(t.Context(), `SELECT id,request_hash FROM loomplane_fleet_operations WHERE org_id=$1 AND fleet_id=$2 AND kind='create' AND idempotency_key='create-first'`, f.scope.OrgID, f.scope.FleetID).Scan(&id, &hash); err != nil {
		t.Fatal(err)
	}
	if id != first.Operation.ID || hash != first.Operation.RequestHash {
		t.Fatal("original create identity changed")
	}
}

func fleetActionKeyMigration(t *testing.T) *migrate.Migration {
	t.Helper()
	for _, step := range migrations.Group().Migrations() {
		if step.Version == "20261223001200" {
			return step
		}
	}
	t.Fatal("fleet action key migration is not registered")
	return nil
}

func TestFleetActionKeyUpgradePreservesCreateIdentity(t *testing.T) {
	f := newFleetFixture(t, 1)
	created, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("existing"))
	if err != nil {
		t.Fatal(err)
	}
	step := fleetActionKeyMigration(t)
	executor := pgmigrate.New(f.db)
	if err = step.Down(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	if err = step.Up(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	replay, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("existing"))
	if err != nil || replay.Operation.ID != created.Operation.ID || replay.Operation.RequestHash != created.Operation.RequestHash || replay.Instance.ID != created.Instance.ID {
		t.Fatalf("upgrade changed accepted create: %+v %v", replay, err)
	}
}
