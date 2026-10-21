//go:build integration

package integration

import (
	"errors"
	"strings"
	"testing"

	"github.com/xraph/ctrlplane/id"
	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func TestWorkloadControllerReceiptPersistence(t *testing.T) {
	db, _, _, _ := installationDatabase(t)
	ctx := t.Context()
	ca := strings.Repeat("a", 64)
	if _, err := db.Exec(ctx, `UPDATE loomplane_cloud_connections SET details=jsonb_build_object('cluster_ca',$1::text,'namespace_uid','namespace-uid','namespace','apps') WHERE id='c'`, ca); err != nil {
		t.Fatal(err)
	}
	receipt := kube.ControllerReceipt{Scope: domain.Scope{OrgID: "o", ProjectID: "p", EnvironmentID: "e"}, ConnectionID: "c", InstanceID: id.New(id.PrefixInstance).String(), PlanID: "plan", RuntimeRef: "runtime-ref", ServiceID: "api", Container: "main", Image: "registry.example/api@sha256:" + strings.Repeat("b", 64), Namespace: "apps", NamespaceUID: "namespace-uid", ClusterCA: ca, ServiceAccount: "default", DeploymentUID: "deployment-uid", DeploymentGeneration: 1, RuntimeRevision: "release"}
	store := kube.NewControllerStore(db)
	for range 2 {
		if err := store.RecordController(ctx, receipt); err != nil {
			t.Fatal(err)
		}
	}
	reopened := kube.NewControllerStore(db)
	got, err := reopened.GetController(ctx, receipt.OrgID, receipt.ConnectionID, receipt.InstanceID, receipt.RuntimeRef)
	if err != nil || *got != receipt {
		t.Fatal("receipt lost across restart", got, err)
	}
	if _, err = reopened.GetController(ctx, "foreign", receipt.ConnectionID, receipt.InstanceID, receipt.RuntimeRef); !errors.Is(err, kube.ErrControllerMissing) {
		t.Fatal("foreign receipt exposed", err)
	}
	for name, change := range map[string]func(*kube.ControllerReceipt){
		"replacement":                        func(r *kube.ControllerReceipt) { r.DeploymentUID = "replacement" },
		"same generation different revision": func(r *kube.ControllerReceipt) { r.RuntimeRevision = "other" },
		"changed image": func(r *kube.ControllerReceipt) {
			r.Image = "registry.example/api@sha256:" + strings.Repeat("c", 64)
			r.DeploymentGeneration++
		},
		"foreign scope":     func(r *kube.ControllerReceipt) { r.ProjectID = "foreign" },
		"changed namespace": func(r *kube.ControllerReceipt) { r.NamespaceUID = "replacement" },
		"changed plan":      func(r *kube.ControllerReceipt) { r.PlanID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := receipt
			change(&candidate)
			if recordErr := store.RecordController(ctx, candidate); recordErr == nil {
				t.Fatal("untrusted receipt replaced ownership")
			}
		})
	}
	newer := receipt
	newer.DeploymentGeneration = 2
	newer.RuntimeRevision = "retry"
	if err = store.RecordController(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if err = store.RecordController(ctx, receipt); !errors.Is(err, kube.ErrOwned) {
		t.Fatal("stale mutation replaced current receipt", err)
	}
	var count int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM loomplane_events WHERE org_id='o' AND type='workload.controller.recorded'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("receipt events", count, err)
	}
	for _, q := range []string{
		`CREATE FUNCTION reject_controller_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture event unavailable'; END $$`,
		`CREATE TRIGGER reject_controller_event BEFORE INSERT ON loomplane_events FOR EACH ROW WHEN (NEW.type='workload.controller.recorded') EXECUTE FUNCTION reject_controller_event()`,
	} {
		if _, err = db.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	failed := receipt
	failed.RuntimeRef = "failed-attempt"
	if err = store.RecordController(ctx, failed); err == nil {
		t.Fatal("failed audit committed receipt")
	}
	if _, err = store.GetController(ctx, failed.OrgID, failed.ConnectionID, failed.InstanceID, failed.RuntimeRef); !errors.Is(err, kube.ErrControllerMissing) {
		t.Fatal("receipt survived audit rollback", err)
	}
	if _, err = db.Exec(ctx, `DROP TRIGGER reject_controller_event ON loomplane_events`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `DROP FUNCTION reject_controller_event()`); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, generation := range []int64{3, 4} {
		go func() {
			candidate := newer
			candidate.DeploymentGeneration = generation
			candidate.RuntimeRevision = "concurrent"
			results <- store.RecordController(ctx, candidate)
		}()
	}
	for range 2 {
		if err = <-results; err != nil && !errors.Is(err, kube.ErrOwned) {
			t.Fatal(err)
		}
	}
	newer.DeploymentGeneration = 4
	newer.RuntimeRevision = "concurrent"
	m := federationMigration(t, "20261025000200")
	if err = m.Up(ctx, pgmigrate.New(db)); err != nil {
		t.Fatal("migration replay", err)
	}
	if err = m.Down(ctx, pgmigrate.New(db)); err == nil {
		t.Fatal("downgrade discarded receipt")
	}
	got, err = store.GetController(ctx, receipt.OrgID, receipt.ConnectionID, receipt.InstanceID, receipt.RuntimeRef)
	if err != nil || *got != newer {
		t.Fatal("failed operation changed receipt", got, err)
	}
	if _, err = db.Exec(ctx, `DELETE FROM loomplane_workload_controllers`); err != nil {
		t.Fatal(err)
	}
	if err = m.Down(ctx, pgmigrate.New(db)); err != nil {
		t.Fatal(err)
	}
	if err = m.Up(ctx, pgmigrate.New(db)); err != nil {
		t.Fatal(err)
	}
}
