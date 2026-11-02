//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

func checkWholeInstanceRetirement(ctx context.Context, t *testing.T, e *enrollmentServiceFixture, store *planning.ManagedCleanupStore, creds *connections.Service, scope kube.ManagedScope, sel planning.ManagedCleanupSelection) {
	t.Helper()
	var schema []*migrate.Migration
	for _, g := range migrations.All() {
		if g.Name() == "loomplane_managed_instances" {
			schema = g.Migrations()
		}
	}
	if len(schema) != 2 {
		t.Fatalf("instance migration order: %d", len(schema))
	}
	executor := pgmigrate.New(e.pg)
	for i := len(schema) - 1; i >= 0; i-- {
		if downErr := schema[i].Down(ctx, executor); downErr != nil {
			t.Fatal("empty instance rollback", schema[i].Name, downErr)
		}
	}
	for _, m := range schema {
		if upErr := m.Up(ctx, executor); upErr != nil {
			t.Fatal("instance migration reinstall", m.Name, upErr)
		}
	}

	labels, err := json.Marshal(map[string]string{owned.LabelOrg: scope.Org, owned.LabelProject: scope.Project, owned.LabelEnvironment: scope.Environment, owned.LabelService: scope.Service, owned.LabelPlan: scope.Plan, owned.LabelRuntime: scope.RuntimeRef, owned.LabelRevision: scope.RuntimeRevision, owned.LabelConnection: scope.Connection})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.sql.Exec(ctx, `INSERT INTO cp_instances(id,tenant_id,slug,name,state,provider_name,labels) VALUES($1,$2,'old','old','running',$3,$4)`, scope.Instance, scope.Org, "kubernetes."+scope.Connection, labels); err != nil {
		t.Fatal(err)
	}
	if _, err = e.sql.Exec(ctx, `INSERT INTO cp_deployments(id,tenant_id,instance_id,release_id,state) VALUES('unreconciled',$1,$2,'release','pending')`, scope.Org, scope.Instance); err != nil {
		t.Fatal(err)
	}
	if err = store.FinalizeManagedCleanup(ctx, sel, creds); !errors.Is(err, kube.ErrManagedAuthority) {
		t.Fatal("pending ctrlplane adopter accepted", err)
	}
	if _, err = e.sql.Exec(ctx, `UPDATE cp_deployments SET state='failed' WHERE id='unreconciled'`); err != nil {
		t.Fatal(err)
	}
	if err = store.FinalizeManagedCleanup(ctx, sel, creds); err != nil {
		t.Fatal("finalizing retired identity", err)
	}
	for _, m := range schema {
		if downErr := m.Down(ctx, executor); downErr == nil || !strings.Contains(downErr.Error(), "must be retained") {
			t.Fatal("retired evidence rollback", m.Name, downErr)
		}
	}
	for _, action := range []string{
		`UPDATE cp_instances SET provider_name='other' WHERE id=$1`,
		`UPDATE cp_instances SET labels='{}',state='destroying' WHERE id=$1`,
		`UPDATE cp_instances SET tenant_id='other' WHERE id=$1`,
		`UPDATE cp_deployments SET state='running' WHERE instance_id=$1`,
		`INSERT INTO cp_deployments(id,tenant_id,instance_id,release_id,state) VALUES('other','org',$1,'release','pending')`,
		`INSERT INTO cp_releases(id,tenant_id,instance_id,version) VALUES('other','org',$1,1)`,
		`INSERT INTO loomplane_managed_workload_authority(org_id,release_id,epoch,body) SELECT org_id,'another-release',epoch,body||jsonb_build_object('Instance',$1::text,'Connection','other-backend','NamespaceUID','other-namespace','ClusterCA','other-ca','Release','another-release') FROM loomplane_managed_workload_authority WHERE org_id='org' AND body->>'Instance'=$1 LIMIT 1`,
	} {
		if _, writeErr := e.sql.Exec(ctx, action, scope.Instance); writeErr == nil || !strings.Contains(writeErr.Error(), "permanently retired") {
			t.Fatal("retired instance adopter", writeErr)
		}
	}
	if _, err = e.sql.Exec(ctx, `UPDATE cp_instances SET state='destroying' WHERE id=$1`, scope.Instance); err != nil {
		t.Fatal(err)
	}
	if _, err = e.sql.Exec(ctx, `INSERT INTO cp_instances(id,tenant_id,slug,name,state,provider_name) VALUES('fresh','org','new','new','running','other')`); err != nil {
		t.Fatal("fresh instance blocked", err)
	}
	if _, err = e.sql.Exec(ctx, `DELETE FROM cp_instances WHERE tenant_id=$1 AND id=$2`, scope.Org, scope.Instance); err != nil {
		t.Fatal(err)
	}
	if _, err = e.sql.Exec(ctx, `INSERT INTO cp_instances(id,tenant_id,slug,name,state,provider_name) VALUES($1,'org','reused','reused','running','other')`, scope.Instance); err == nil || !strings.Contains(err.Error(), "permanently retired") {
		t.Fatal("recreated retired identity", err)
	}
	if err = store.FinalizeManagedCleanup(ctx, sel, creds); err != nil {
		t.Fatal("absent-row replay", err)
	}
	g, err := store.Open(ctx, sel)
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.BeginRetiredRuntimeRemoval(ctx, *g)
	if err != nil || token == "" {
		t.Fatal("original runtime removal", err)
	}
	aliasSQL := `INSERT INTO cp_instances(id,tenant_id,slug,name,state,provider_name,labels) VALUES('ref-alias','org','alias','alias','running','other',jsonb_build_object('loomplane.runtime.ref',$1::text))`
	if _, err = e.sql.Exec(ctx, aliasSQL, scope.RuntimeRef); err == nil || !strings.Contains(err.Error(), "runtime values") {
		t.Fatal("aliased an unresolved runtime deletion", err)
	}
	if _, err = store.BeginRetiredRuntimeRemoval(ctx, *g); !errors.Is(err, planning.ErrRetiredRuntimeUnresolved) {
		t.Fatal("absence retried unknown Vault deletion", err)
	}
	// Model the original backend returning success late, against its own ticket.
	if err = store.ResolveRetiredRuntimeRemoval(ctx, *g, token); err != nil {
		t.Fatal(err)
	}
	if _, err = e.sql.Exec(ctx, aliasSQL, scope.RuntimeRef); err != nil {
		t.Fatal("resolved runtime reference remained fenced", err)
	}
	if next, readErr := store.BeginRetiredRuntimeRemoval(ctx, *g); readErr != nil || next != "" {
		t.Fatal("replay would delete the new owner's runtime values", next, readErr)
	}
}

// Simulate an already missing pre-cutover row in this disposable database.
// Current workers exercise the actual protected deletion in the runner tests.
func historicalCleanupBindingLoss(ctx context.Context, t *testing.T, db *pgx.Conn, org, id string) {
	t.Helper()
	if _, err := db.Exec(ctx, `DELETE FROM loomplane_bindings WHERE org_id=$1 AND id=$2`, org, id); err == nil || !strings.Contains(err.Error(), "exact reviewed binding") {
		t.Fatal("unreviewed live binding deletion", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `ALTER TABLE loomplane_bindings DISABLE TRIGGER loomplane_reviewed_binding_removal`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM loomplane_bindings WHERE org_id=$1 AND id=$2`, org, id); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `ALTER TABLE loomplane_bindings ENABLE TRIGGER loomplane_reviewed_binding_removal`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
