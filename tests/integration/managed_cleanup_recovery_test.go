//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/database/migrations"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

func recoverCleanupAfterRestart(ctx context.Context, t *testing.T, e *enrollmentServiceFixture, creds *connections.Service, scope kube.ManagedScope, selection planning.ManagedCleanupSelection, auth *security.Authorizer, foreign *corev1.ConfigMap) {
	t.Helper()
	var setName, podName string
	if err := e.sql.QueryRow(ctx, `SELECT rs->>'name',p->>'name' FROM loomplane_managed_workload_issuances w CROSS JOIN LATERAL jsonb_array_elements(w.cleanup_children) rs CROSS JOIN LATERAL jsonb_array_elements(w.cleanup_children) p WHERE w.release_id=$1 AND rs->>'resource'='replicasets' AND p->>'resource'='pods' AND rs->>'uid'=p->>'owner_uid'`, scope.Release).Scan(&setName, &podName); err != nil {
		t.Fatal(err)
	}
	sets := e.bootstrap.Kube.AppsV1().ReplicaSets(scope.Namespace)
	pods := e.bootstrap.Kube.CoreV1().Pods(scope.Namespace)
	resume := func() error {
		return planning.NewManagedCleanupStore(e.pg, auth, nil).RecoverCleanupChildren(ctx, selection, creds)
	}
	ref := metav1.OwnerReference{Kind: "ConfigMap", APIVersion: "v1", Name: foreign.Name, UID: foreign.UID}
	set, err := sets.Get(ctx, setName, metav1.GetOptions{})
	if err != nil || len(set.OwnerReferences) != 0 {
		t.Fatalf("expected orphan before restart: %v", err)
	}
	set.OwnerReferences = []metav1.OwnerReference{ref}
	_, err = sets.Update(ctx, set, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = resume(); !errors.Is(err, planning.ErrCleanupUnprovedChild) {
		t.Fatalf("reparented set: %v", err)
	}
	set, err = sets.Get(ctx, setName, metav1.GetOptions{})
	if err != nil || len(set.OwnerReferences) != 1 || set.OwnerReferences[0].UID != ref.UID || set.DeletionTimestamp != nil {
		t.Fatalf("reparented set removed: %v", err)
	}
	set.OwnerReferences = nil
	set, err = sets.Update(ctx, set, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before := set.ResourceVersion
	set.Status.Conditions = append(set.Status.Conditions, appsv1.ReplicaSetCondition{Type: appsv1.ReplicaSetReplicaFailure, Status: corev1.ConditionFalse, Reason: "ObservedForCleanup"})
	set, err = sets.UpdateStatus(ctx, set, metav1.UpdateOptions{})
	if err != nil || before == set.ResourceVersion {
		t.Fatalf("status-only change: %v", err)
	}
	if err = resume(); !errors.Is(err, planning.ErrCleanupConverging) {
		t.Fatalf("restart after parent: %v", err)
	}
	if err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 15*time.Second, true, func(call context.Context) (bool, error) {
		_, readErr := sets.Get(call, setName, metav1.GetOptions{})
		if apierrors.IsNotFound(readErr) {
			return true, nil
		}
		return false, readErr
	}); err != nil {
		t.Fatal(err)
	}
	pod, err := pods.Get(ctx, podName, metav1.GetOptions{})
	if err != nil || len(pod.OwnerReferences) != 0 {
		t.Fatalf("missing orphaned captured pod: %v", err)
	}
	pod.OwnerReferences = []metav1.OwnerReference{ref}
	_, err = pods.Update(ctx, pod, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = resume(); !errors.Is(err, planning.ErrCleanupUnprovedChild) {
		t.Fatalf("reparented pod: %v", err)
	}
	pod, err = pods.Get(ctx, podName, metav1.GetOptions{})
	if err != nil || len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].UID != ref.UID || pod.DeletionTimestamp != nil {
		t.Fatalf("reparented pod removed: %v", err)
	}
	pod.OwnerReferences = nil
	if _, err = pods.Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = resume(); !errors.Is(err, planning.ErrCleanupConverging) {
		t.Fatalf("restart after set: %v", err)
	}
	if err = wait.PollUntilContextTimeout(ctx, 150*time.Millisecond, 60*time.Second, true, func(call context.Context) (bool, error) {
		_, readErr := pods.Get(call, podName, metav1.GetOptions{})
		if apierrors.IsNotFound(readErr) {
			return true, nil
		}
		return false, readErr
	}); err != nil {
		t.Fatal(err)
	}
	if err = resume(); err != nil {
		t.Fatalf("restart after pod: %v", err)
	}
	var accepted int
	if err = e.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_managed_workload_issuances WHERE release_id=$1 AND cleanup_grant_id IS NOT NULL AND request->>'Resource' IN ('replicasets','pods') AND request->>'Method'='DELETE' AND request->>'PropagationPolicy'='Orphan' AND request->>'UID'=outcome->>'UID' AND request->>'ResourceVersion'<>'' AND outcome->>'State'='accepted'`, scope.Release).Scan(&accepted); err != nil || accepted != 2 {
		t.Fatalf("reviewed child effects: %d %v", accepted, err)
	}
	var grant, source string
	if err = e.sql.QueryRow(ctx, `SELECT cleanup_grant_id,request->>'CleanupSource' FROM loomplane_managed_workload_issuances WHERE release_id=$1 AND request->>'Resource'='pods' AND outcome->>'State'='accepted'`, scope.Release).Scan(&grant, &source); err != nil || source == "" {
		t.Fatal("missing capture source", err)
	}
	for _, stmt := range []string{
		`UPDATE loomplane_managed_workload_issuances SET request=jsonb_set(request,'{Name}','"different"') WHERE id=$1`,
		`UPDATE loomplane_managed_workload_issuances SET outcome=NULL,resolved_at=NULL WHERE id=$1`,
	} {
		if _, err = e.sql.Exec(ctx, stmt, source); err == nil {
			t.Fatal("accepted parent capture changed")
		}
	}
	clone := func(id, patch string) error {
		_, cloneErr := e.sql.Exec(ctx, `INSERT INTO loomplane_managed_workload_issuances(id,org_id,release_id,epoch,scope,request,cleanup_grant_id,cleanup_grant_revision)
 SELECT $2,org_id,release_id,epoch,scope,request||$3::jsonb,cleanup_grant_id,cleanup_grant_revision FROM loomplane_managed_workload_issuances WHERE release_id=$1 AND request->>'Resource'='pods' AND outcome->>'State'='accepted'`, scope.Release, id, patch)
		return cloneErr
	}
	for _, patch := range []string{`{"CleanupSource":"foreign"}`, `{"UID":"foreign"}`, `{"ResourceVersion":""}`, `{"PropagationPolicy":"Foreground"}`} {
		if err = clone("forged", patch); err == nil {
			t.Fatalf("forged recovery source: %s", patch)
		}
	}
	// Model an unresolved send with an independently recorded issuance. Current
	// absence must stop recovery, even after every observed child has disappeared.
	if err = clone("unresolved-recovered-child", "{}"); err != nil {
		t.Fatal(err)
	}
	if err = planning.NewManagedCleanupStore(e.pg, auth, nil).SettleManagedCleanup(ctx, selection, creds); !errors.Is(err, planning.ErrCleanupWritesUnresolved) {
		t.Fatalf("unknown send settled an empty namespace: %v", err)
	}
	if err = resume(); !errors.Is(err, planning.ErrCleanupWritesUnresolved) {
		t.Fatalf("unknown child send: %v", err)
	}
	if _, err = e.sql.Exec(ctx, `INSERT INTO loomplane_managed_workload_authority(org_id,release_id,epoch,body) SELECT org_id,'unreviewed',epoch,body||'{"Connection":"another-connection","Release":"unreviewed"}'::jsonb FROM loomplane_managed_workload_authority WHERE release_id=$1`, scope.Release); err == nil {
		t.Fatal("new aliased owner crossed pending orphan cleanup")
	}
	historical := planning.NewManagedCleanupStore(e.pg, auth, nil).Journal(scope.Org, grant)
	if err = historical.Resolve(ctx, "unresolved-recovered-child", scope, kube.ManagedOutcome{State: "rejected", StatusCode: 409}); err != nil {
		t.Fatal(err)
	}
	if err = resume(); err != nil {
		t.Fatal("late resolution", err)
	}
	// The migration framework removes the newer compatibility record after
	// its no-op Down. Exercise the DDL owner's subsequent Down with retained
	// evidence, restoring the original record in this disposable fixture.
	var renamed []byte
	if err = e.sql.QueryRow(ctx, `DELETE FROM grove_migrations g WHERE "group"='loomplane' AND version='20261207000100' RETURNING to_jsonb(g)`).Scan(&renamed); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, restoreErr := e.sql.Exec(ctx, `INSERT INTO grove_migrations SELECT * FROM jsonb_populate_record(NULL::grove_migrations,$1::jsonb)`, renamed); restoreErr != nil {
			t.Fatal(restoreErr)
		}
	}()
	for _, m := range migrations.Group().Migrations() {
		if m.Version == "20261206000500" {
			if err = m.Down(ctx, pgmigrate.New(e.pg)); err == nil {
				t.Fatal("downgrade discarded child recovery evidence")
			}
			return
		}
	}
	t.Fatal("missing recovery migration")
}
