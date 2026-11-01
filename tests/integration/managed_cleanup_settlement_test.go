//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"

	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func checkReleaseSettlement(ctx context.Context, t *testing.T, e *enrollmentServiceFixture, creds *connections.Service, infra *cloudinfra.Service, scope kube.ManagedScope, input cloudinfra.ClusterWorkloadInput, sel planning.ManagedCleanupSelection, auth *security.Authorizer, shared bool) {
	t.Helper()
	store := planning.NewManagedCleanupStore(e.pg, auth, nil)
	plans := planning.NewPgStore(e.pg)
	if !shared {
		other, readErr := plans.GetBinding(ctx, scope.Org, "closure-binding")
		if readErr != nil {
			t.Fatal(readErr)
		}
		other.ID, other.Name = "competing-owner", "other"
		if writeErr := plans.PutBinding(ctx, other); writeErr != nil {
			t.Fatal(writeErr)
		}
		if settleErr := store.SettleManagedCleanup(ctx, sel, creds); !errors.Is(settleErr, kube.ErrManagedAuthority) {
			t.Fatal("unreviewed binding allowed settlement", settleErr)
		}
		var stillHeld bool
		if readErr = e.sql.QueryRow(ctx, `SELECT NOT released FROM loomplane_cluster_workload_holds WHERE id=$1`, input.ID).Scan(&stillHeld); readErr != nil || !stillHeld {
			t.Fatal("unreviewed binding released capacity", readErr)
		}
		if writeErr := plans.DeleteBinding(ctx, scope.Org, other.ID); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := store.SettleManagedCleanup(ctx, sel, creds); err != nil {
		t.Fatal("qualified absence", err)
	}
	var released bool
	var rev int64
	if err := e.sql.QueryRow(ctx, `SELECT h.released,t.revision FROM loomplane_cluster_workload_holds h JOIN loomplane_managed_cleanup_tombstones t ON t.org_id=h.org_id AND t.release_id=$2 WHERE h.id=$1`, input.ID, scope.Release).Scan(&released, &rev); err != nil || !released || rev < 1 {
		t.Fatalf("receipt or hold settlement: %v/%d %v", released, rev, err)
	}
	if shared {
		return
	}
	if err := store.SettleManagedCleanup(ctx, sel, creds); err != nil {
		t.Fatal("idempotent tombstone", err)
	}
	settledGrant, err := store.Open(ctx, sel)
	if err != nil {
		t.Fatal(err)
	}
	var known kube.ManagedRequest
	for _, write := range settledGrant.Target.Accepted {
		if write.Request.Resource == "deployments" && write.Outcome.State == "accepted" {
			known = write.Request
			known.Method = "DELETE"
			known.UID = write.Outcome.UID
			known.ResourceVersion = write.Outcome.ResourceVersion
			known.PropagationPolicy = "Orphan"
			break
		}
	}
	if known.UID == "" {
		t.Fatal("missing reviewed parent")
	}
	if _, issueErr := store.Journal(scope.Org, settledGrant.ID).Issue(ctx, scope, known); issueErr == nil || !strings.Contains(issueErr.Error(), "accepted cleanup review has settled") {
		t.Fatal("pre-settlement review issued another cleanup request", issueErr)
	}
	// Accept an independent later review while its binding still exists. It
	// supplies the distinct source for the simulated unresolved send below.
	freshReview := &planning.Plan{ID: "post-settlement-review", OrgID: scope.Org, ProjectID: scope.Project, EnvironmentID: scope.Environment}
	freshResult := &planning.Result{Executable: true, Connections: map[string]string{}, Actions: []planning.Action{{ID: scope.Action, Subject: planning.ServiceSubject, Name: scope.Service, Op: planning.Delete, Provider: "kubernetes"}}}
	planner := planning.NewService(planning.Deps{Store: plans})
	if _, err = e.sql.Exec(ctx, `UPDATE loomplane_deployments SET status='succeeded' WHERE id=$1`, sel.Deployment); err != nil {
		t.Fatal(err)
	}
	if err = planner.PrepareManagedCleanupTargets(ctx, freshReview, freshResult); err != nil {
		t.Fatal(err)
	}
	saveCleanupReview(t, &poolFixture{sql: e.sql}, freshReview, freshResult)
	if ok, decisionErr := plans.Decide(ctx, scope.Org, freshReview.ID, "approved", "reviewer", "", time.Now()); decisionErr != nil || !ok {
		t.Fatal("fresh review", decisionErr)
	}
	if _, err = e.sql.Exec(ctx, `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES('after-closure','org','proj','env','production',$1,'after-closure','running','reviewer',now())`, freshReview.ID); err != nil {
		t.Fatal(err)
	}
	freshGrant, err := store.Open(ctx, planning.ManagedCleanupSelection{Org: scope.Org, Plan: freshReview.ID, Action: scope.Action, Deployment: "after-closure"})
	if err != nil {
		t.Fatal(err)
	}
	// The durable cleanup receipt must survive losing the service binding.
	if _, err = e.sql.Exec(ctx, `DELETE FROM loomplane_bindings WHERE org_id=$1 AND connection_id=$2 AND external_id=$3`, scope.Org, scope.Connection, scope.Instance); err != nil {
		t.Fatal(err)
	}
	adapters := cloudinfra.NewAdapters()
	if err = adapters.RegisterCluster("digitalocean", clusterCapacityKindReader{e.provider}, provider.ClusterDiscovery, provider.ClusterBootstrapAccess, provider.ClusterCapacityObservation); err != nil {
		t.Fatal(err)
	}
	infra.Adapters = adapters
	pods := e.bootstrap.Kube.CoreV1().Pods(scope.Namespace)
	late, err := pods.Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "late-residual", Annotations: map[string]string{kube.WorkloadReleaseAnnotation: scope.Release}}, Spec: corev1.PodSpec{NodeSelector: map[string]string{"fixture": "not-present"}, Containers: []corev1.Container{{Name: "late", Image: "registry.k8s.io/pause:3.10", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("37m")}}}}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	observe := func() *cloudinfra.ClusterCapacitySnapshot {
		t.Helper()
		got, readErr := infra.ObserveClusterCapacity(security.WithPrincipal(ctx, e.principal), e.principal, "cloud", "cluster")
		if readErr != nil || got == nil {
			t.Fatal("authorized observation", readErr)
		}
		if got.Complete {
			t.Fatal("kind was incorrectly qualified as DigitalOcean supply")
		}
		return got
	}
	blocked := func(want bool) {
		t.Helper()
		var active bool
		if readErr := e.sql.QueryRow(ctx, `SELECT coalesce((SELECT present FROM loomplane_managed_cleanup_current_drift WHERE org_id=$1 AND release_id=$2 AND resource='pods' AND uid=$3),false)`, scope.Org, scope.Release, string(late.UID)).Scan(&active); readErr != nil || active != want {
			t.Fatalf("active drift %v, want %v: %v", active, want, readErr)
		}
	}
	snap := observe()
	blocked(true)
	var count int
	for _, w := range snap.Workloads {
		if w.PodUID == string(late.UID) {
			count++
			if w.ReleaseID != "" || w.Requests.Pods != 1 || w.Requests.CPUMillis != 37 {
				t.Fatalf("orphan received credit or wrong requests: %+v", w)
			}
		}
	}
	if count != 1 {
		t.Fatalf("residual charged %d times", count)
	}
	if err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		pod, readErr := pods.Get(ctx, late.Name, metav1.GetOptions{})
		if readErr != nil {
			return readErr
		}
		if pod.UID != late.UID {
			return errors.New("residual was replaced")
		}
		delete(pod.Annotations, kube.WorkloadReleaseAnnotation)
		_, readErr = pods.Update(ctx, pod, metav1.UpdateOptions{})
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	_ = observe()
	blocked(true)
	// Only the fixture owner removes this unproved orphan. Read-only recovery
	// must never issue a DELETE using its marker or historical approval.
	zero := int64(0)
	if err = pods.Delete(ctx, late.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &late.UID}}); err != nil {
		t.Fatal(err)
	}
	if err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 15*time.Second, true, func(call context.Context) (bool, error) {
		_, readErr := pods.Get(call, late.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(readErr) {
			return true, nil
		}
		return false, readErr
	}); err != nil {
		t.Fatal(err)
	}
	// Replaying a saved snapshot or omitting the new projection cannot clear.
	r, err := e.store.GetManagedResource(ctx, scope.Org, "cluster")
	if err != nil {
		t.Fatal(err)
	}
	partial := *snap
	partial.ObservationRevision, err = e.store.BeginClusterCapacityObservation(ctx, scope.Org, "cloud", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	partial.CheckedAt = time.Now().UTC()
	partial.CleanupInventory = nil
	if err = e.store.SaveClusterCapacity(ctx, r, partial); err != nil {
		t.Fatal(err)
	}
	blocked(true)
	pendingJournal := store.Journal(scope.Org, freshGrant.ID)
	pending, err := pendingJournal.Issue(ctx, scope, known)
	if err != nil {
		t.Fatal(err)
	}
	_ = observe()
	blocked(true)
	if err = pendingJournal.Resolve(ctx, pending, scope, kube.ManagedOutcome{State: "rejected", StatusCode: 404}); err != nil {
		t.Fatal(err)
	}
	_ = observe()
	blocked(false)
	var history int
	if err = e.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_managed_cleanup_drift_events WHERE org_id=$1 AND release_id=$2 AND uid=$3`, scope.Org, scope.Release, string(late.UID)).Scan(&history); err != nil || history < 3 {
		t.Fatalf("drift history %d: %v", history, err)
	}
	var tombstones int
	if err = e.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_managed_cleanup_tombstones WHERE org_id=$1 AND release_id=$2`, scope.Org, scope.Release).Scan(&tombstones); err != nil || tombstones != 1 {
		t.Fatal("tombstone lost or multiplied", tombstones, err)
	}
	for _, m := range migrations.Group().Migrations() {
		if m.Name == "managed_cleanup_observations" {
			if err = m.Down(ctx, pgmigrate.New(e.pg)); err == nil || !strings.Contains(err.Error(), "must be retained") {
				t.Fatal("downgrade discarded settled release or drift history", err)
			}
			return
		}
	}
	t.Fatal("missing cleanup observation migration")
}
