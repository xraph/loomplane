package currentauthority

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/database/migrations"
)

func TestFleetPostgresFiniteSchemaCatalog(t *testing.T) {
	f := newOrdinaryPGFixture(t)
	if err := fleetNamespaceMigration(t, f, false); err != nil {
		t.Fatalf("empty journal down: %v", err)
	}
	if err := fleetNamespaceMigration(t, f, true); err != nil {
		t.Fatalf("empty journal up: %v", err)
	}
	contract, err := fleetContract()
	if err != nil {
		t.Fatal(err)
	}
	tables := make([]string, 0, len(contract.Columns))
	functions := make([]string, 0, len(contract.Functions))
	for name := range contract.Columns {
		tables = append(tables, name)
	}
	for name := range contract.Functions {
		functions = append(functions, name)
	}
	sort.Strings(tables)
	sort.Strings(functions)
	writes, err := json.Marshal(contract.WriteEvents)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := f.main.QueryRow(t.Context(), fleetSchemaSQL, f.schema, tables, functions, string(writes)).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var actual fleetCatalog
	if err := json.Unmarshal(raw, &actual); err != nil {
		t.Fatal(err)
	}
	for _, check := range actual.Checks {
		if check.Table == "loomplane_fleet_namespace_attempts" || check.Name == "fleet_namespace_revision" {
			t.Logf("qualified CHECK %s.%s: %s", check.Table, check.Name, check.Definition)
		}
	}
	if err := actual.validateFleet(f.schema, f.role, contract); err != nil {
		t.Fatal(err)
	}
}

func TestFleetPostgresNamespaceEvidence(t *testing.T) {
	f := newOrdinaryPGFixture(t)
	ctx := t.Context()
	org := f.binding.Actor.OrgID
	project := f.project
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.main.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE loomplane_projects SET deployment_mode='fleet' WHERE id=$1`, project)
	exec(`INSERT INTO loomplane_cloud_profiles(id,org_id,project_id,name,revision,created_at,updated_at) VALUES('cloud',$1,$2,'Fleet',1,now(),now())`, org, project)
	exec(`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('environment',$1,$2,'Fleet','production',now(),now())`, org, project)
	exec(`INSERT INTO loomplane_fleets(id,org_id,project_id,revision,status,created_at,updated_at) VALUES('fleet',$1,$2,1,'active',now(),now())`, org, project)
	exec(`INSERT INTO loomplane_fleet_stages(id,org_id,project_id,fleet_id,name,environment_kind,max_instances,policy_revision,created_at) VALUES('stage',$1,$2,'fleet','Prod','production',10,1,now())`, org, project)
	exec(`INSERT INTO loomplane_fleet_instances(id,org_id,project_id,fleet_id,stage_id,environment_id,external_ref,profile_id,desired_release_id,status,revision,desired_generation,observed_generation,ownership_epoch,created_at,updated_at) VALUES('instance',$1,$2,'fleet','stage','environment','external','profile','release','pending',1,1,0,1,now(),now())`, org, project)
	exec(`INSERT INTO loomplane_cluster_operations(id,org_id,cloud_id,status,revision,digest,body,principal) VALUES('operation',$1,'cloud','accepted',1,repeat('a',64),'{"kind":"instance-namespace"}','{}')`, org)
	at := time.Date(2026, 11, 4, 12, 0, 0, 123456000, time.UTC)
	stamp := at.Format("2006-01-02T15:04:05.000000Z")
	envelope := fmt.Sprintf(`{"authorized_at":%q,"authorized_at_ns":789}`, stamp)
	insert := func(tx driver.Tx, id, kind string, nsBefore, nsAfter, opBefore, opAfter int64, in, result string) error {
		_, err := tx.Exec(ctx, `INSERT INTO loomplane_fleet_namespace_attempts(id,org_id,operation_id,project_id,fleet_id,instance_id,environment_id,key_id,kind,input_digest,expected_namespace_revision,result_namespace_revision,expected_operation_revision,result_operation_revision,generation,ownership_epoch,authority_id,authorized_at,authorized_at_ns,input,result) VALUES($1,$2,'operation',$3,'fleet','instance','environment','key',$4,repeat('a',64),$5,$6,$7,$8,1,1,'authority',$9,789,$10,$11)`, id, org, project, kind, nsBefore, nsAfter, opBefore, opAfter, at, in, result)
		return err
	}
	tx, err := f.main.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if insertErr := insert(tx, "reserve", "reserve", 0, 1, 0, 1, envelope, envelope); insertErr != nil {
		_ = tx.Rollback()
		t.Fatal(insertErr)
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_namespaces(operation_id,org_id,project_id,fleet_id,instance_id,environment_id,cloud_id,cluster_id,namespace,ownership_epoch,generation,snapshot_digest,authority,body,revision,creation_attempt_id,current_attempt_id) VALUES('operation',$1,$2,'fleet','instance','environment','cloud','cluster','namespace',1,1,repeat('a',64),'{"id":"authority","principal":{"KeyID":"key"}}','{}',1,'reserve','reserve')`, org, project)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, query string }{
		{"immutable journal", `UPDATE loomplane_fleet_namespace_attempts SET authorized_at_ns=788 WHERE id='reserve'`},
		{"retained journal", `DELETE FROM loomplane_fleet_namespace_attempts WHERE id='reserve'`},
		{"unrecorded namespace update", `UPDATE loomplane_fleet_namespaces SET revision=revision+1 WHERE operation_id='operation'`},
		{"retained namespace", `DELETE FROM loomplane_fleet_namespaces WHERE operation_id='operation'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.main.Exec(ctx, tc.query); err == nil {
				t.Fatal("mutation accepted")
			}
		})
	}
	t.Run("timestamp remainder mismatch rolls back", func(t *testing.T) {
		tx, err := f.main.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.Exec(ctx, `UPDATE loomplane_cluster_operations SET revision=2 WHERE id='operation'`); err != nil {
			t.Fatal(err)
		}
		bad := fmt.Sprintf(`{"authorized_at":%q,"authorized_at_ns":788}`, stamp)
		if err := insert(tx, "bad", "checkpoint", 1, 2, 1, 2, bad, envelope); err == nil {
			t.Fatal("different exact instant accepted")
		}
	})
	t.Run("timestamp microsecond mismatch rolls back", func(t *testing.T) {
		tx, err := f.main.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.Exec(ctx, `UPDATE loomplane_cluster_operations SET revision=2 WHERE id='operation'`); err != nil {
			t.Fatal(err)
		}
		bad := fmt.Sprintf(`{"authorized_at":%q,"authorized_at_ns":789}`, at.Add(time.Microsecond).Format("2006-01-02T15:04:05.000000Z"))
		if err := insert(tx, "bad", "checkpoint", 1, 2, 1, 2, envelope, bad); err == nil {
			t.Fatal("different exact instant accepted")
		}
	})
	t.Run("checkpoint retains creation evidence", func(t *testing.T) {
		tx, err := f.main.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.Exec(ctx, `UPDATE loomplane_cluster_operations SET revision=2 WHERE id='operation'`); err != nil {
			t.Fatal(err)
		}
		if err := insert(tx, "checkpoint", "checkpoint", 1, 2, 1, 2, envelope, envelope); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE loomplane_fleet_namespaces SET revision=2,current_attempt_id='checkpoint' WHERE operation_id='operation'`); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var creation, current string
		if err := f.main.QueryRow(ctx, `SELECT creation_attempt_id,current_attempt_id FROM loomplane_fleet_namespaces WHERE operation_id='operation'`).Scan(&creation, &current); err != nil {
			t.Fatal(err)
		}
		if creation != "reserve" || current != "checkpoint" {
			t.Fatal("creation evidence was replaced")
		}
	})
	var count int
	if err := f.main.QueryRow(ctx, `SELECT count(*) FROM loomplane_fleet_namespace_attempts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("journal contains %d rows", count)
	}
	if err := fleetNamespaceMigration(t, f, false); err == nil {
		t.Fatal("downgrade discarded namespace authorization history")
	}
	var retained int
	if err := f.main.QueryRow(ctx, `SELECT count(*) FROM loomplane_fleet_namespace_attempts`).Scan(&retained); err != nil || retained != 2 {
		t.Fatalf("history after refused downgrade: %d %v", retained, err)
	}
}

// Keep the public installer refusal distinct from SQL catalog qualification.
func TestFleetSchemaUnavailableWithoutInput(t *testing.T) {
	if err := inspectFleetSchema(context.Background(), nil, "", 0); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func fleetNamespaceMigration(t *testing.T, f *ordinaryPGFixture, up bool) error {
	t.Helper()
	for _, step := range migrations.Group().Migrations() {
		if step.Version != "20261223001000" {
			continue
		}
		executor := pgmigrate.New(f.main)
		if err := executor.AcquireLock(t.Context(), "fleet-namespace-migration-test"); err != nil {
			return err
		}
		defer func() { _ = executor.ReleaseLock(context.Background()) }()
		if err := executor.BeginTx(t.Context()); err != nil {
			return err
		}
		defer func() { _ = executor.RollbackTx(context.Background()) }()
		run := step.Down
		if up {
			run = step.Up
		}
		if err := run(t.Context(), executor); err != nil {
			return err
		}
		return executor.CommitTx(t.Context())
	}
	return errors.New("namespace attempt migration is missing")
}
