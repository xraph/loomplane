//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/planning"
)

// This fixture has qualified provider/node supply. The real kind namespace
// observation intentionally cannot supply that DigitalOcean capacity evidence.
func TestManagedCleanupDriftAdmission(t *testing.T) {
	for _, mode := range []string{"guaranteed", "best_effort"} {
		t.Run(mode, func(t *testing.T) {
			f := newCleanupIssuerFixture(t, false)
			grant := f.open(t)
			ctx := t.Context()
			cluster := f.poolFixture.store
			var oldRaw []byte
			if err := f.sql.QueryRow(ctx, `SELECT result FROM loomplane_plans WHERE org_id=$1 AND id=$2`, f.scope.Org, f.scope.Plan).Scan(&oldRaw); err != nil {
				t.Fatal(err)
			}
			var result planning.Result
			if err := json.Unmarshal(oldRaw, &result); err != nil {
				t.Fatal(err)
			}
			r, err := cluster.GetManagedResource(ctx, "org", "cluster")
			if err != nil {
				t.Fatal(err)
			}
			svc := planning.NewService(planning.Deps{Store: f.plans, Authz: &capacityPlanAuth{}, Capacity: managedDemandAuthority{policy: r.ScalingPolicies["pool"]}})
			plan := &planning.Plan{ID: "positive-supply", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", CreatedBy: "usr"}
			cfg := result.Actions[0].Service
			cfg.CapacityMode = mode
			cfg.ClusterDemand = nil
			if err = svc.PrepareClusterWorkloadDemands(ctx, plan, &result); err != nil {
				t.Fatal(err)
			}
			if _, err = f.sql.Exec(ctx, `UPDATE loomplane_deployments SET status='succeeded' WHERE id=$1`, f.selection.Deployment); err != nil {
				t.Fatal(err)
			}
			saveManagedDemandPlan(t, f.poolFixture, plan, &result)
			svc.BindManagedRuntime(ledgerRuntimeFixture{})
			resolve := func(want string) {
				t.Helper()
				got := svc.ResolveCapacityIntents(ctx, "org", plan.ID)
				if want == "" && got == nil {
					return
				}
				var block *cloudinfra.CapacityAdmissionError
				if !errors.As(got, &block) || block.Block.Reason != want {
					t.Fatalf("expected %s, got %v", want, got)
				}
			}
			resolve("")
			if _, err = f.sql.Exec(ctx, `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES('positive-supply-deployment','org','proj','env','production',$1,'workload','running','usr',now())`, plan.ID); err != nil {
				t.Fatal(err)
			}
			old := f.scope
			if err = cluster.FinishWorkloadAttempt(ctx, cloudinfra.WorkloadAttempt{Org: old.Org, Project: old.Project, Environment: old.Environment, Service: old.Service, Plan: old.Plan, Deployment: old.Deployment, Generation: old.Epoch}, true); err != nil {
				t.Fatal(err)
			}
			attempt, err := cluster.BeginWorkloadAttempt(ctx, cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Plan: plan.ID, Service: "api", Deployment: "positive-supply-deployment"}, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "guaranteed" {
				in := cfg.ClusterDemand.Input
				if err = workloadTx(t, f.poolFixture, func(tx driver.Tx) error { return cluster.AttachClusterWorkloadAttempt(ctx, tx, in, *attempt) }); err != nil {
					t.Fatal(err)
				}
			}
			scope := f.scope
			scope.Plan, scope.Deployment, scope.Epoch, scope.Instance = plan.ID, attempt.Deployment, attempt.Generation, "new-instance"
			scope.RuntimeRef = "org/env/api/runtime_positive_supply"
			scope.RuntimeRevision = scope.Deployment + "/" + scope.Action
			scope.Release = kube.WorkloadReleaseID(scope.RuntimeRef, scope.RuntimeRevision)
			journal := kube.NewControllerStore(f.db).WithClusterCapacity(true).WithManagedDemandGuard(managedFixtureDemandGuard(f.db))
			if err = journal.OpenManagedScope(ctx, scope); err != nil {
				t.Fatal(err)
			}
			// Seed a prior observed closure for the admission fixture. Real closure and
			// drift publication are independently exercised by TestManagedCleanupEffectsRealAPI.
			var start time.Time
			if err = f.sql.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&start); err != nil {
				t.Fatal(err)
			}
			tomb := cloudinfra.ClusterCleanupTombstone{SchemaVersion: 1, OrgID: "org", CloudID: "cloud", ResourceID: "cluster", GrantID: grant.ID, ReleaseID: f.scope.Release, Namespace: f.scope.Namespace, NamespaceUID: f.scope.NamespaceUID, ClusterCA: f.scope.ClusterCA, Ref: r.Observation.Ref, ObservationStartedAt: start, ObservedAt: start, InventoryBarrier: 1, ResourceVersions: map[string]string{"deployments": "1", "replicasets": "1", "pods": "1"}}
			raw, err := json.Marshal(tomb)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.sql.Exec(ctx, `INSERT INTO loomplane_managed_cleanup_tombstones(org_id,release_id,cloud_id,resource_id,grant_id,schema_version,observation_started_at,observed_at,inventory_barrier,body) VALUES('org',$1,'cloud','cluster',$2,1,$3,$3,1,$4)`, tomb.ReleaseID, grant.ID, start, raw); err != nil {
				t.Fatal(err)
			}
			var capacityRaw []byte
			if err = f.sql.QueryRow(ctx, `SELECT body FROM loomplane_cluster_capacity_inventory WHERE org_id='org' AND resource_id='cluster'`).Scan(&capacityRaw); err != nil {
				t.Fatal(err)
			}
			var supply cloudinfra.ClusterCapacitySnapshot
			if err = json.Unmarshal(capacityRaw, &supply); err != nil || !supply.Complete || !supply.WorkloadsComplete {
				t.Fatal("positive supply fixture incomplete", err)
			}
			event := func(present bool) {
				t.Helper()
				rev, readErr := cluster.BeginClusterCapacityObservation(ctx, "org", "cloud", "cluster")
				if readErr != nil {
					t.Fatal(readErr)
				}
				supply.ObservationRevision, supply.CheckedAt = rev, time.Now().UTC()
				if readErr = cluster.SaveClusterCapacity(ctx, r, supply); readErr != nil {
					t.Fatal(readErr)
				}
				// The independently tested kind observer supplies release coverage in
				// production. Seed that qualified fact in this positive-supply ledger fixture.
				if _, readErr = f.sql.Exec(ctx, `UPDATE loomplane_managed_cleanup_coverage SET qualified=true WHERE org_id='org' AND release_id=$1 AND observation_revision=$2`, tomb.ReleaseID, rev); readErr != nil {
					t.Fatal(readErr)
				}
				if _, readErr = f.sql.Exec(ctx, `INSERT INTO loomplane_managed_cleanup_drift_events(org_id,release_id,resource,uid,observation_revision,checked_at,present) VALUES('org',$1,'pods','fixture-orphan',$2,$3,$4)`, tomb.ReleaseID, rev, supply.CheckedAt, present); readErr != nil {
					t.Fatal(readErr)
				}
			}
			event(true)
			var unrelated string
			if err = f.sql.QueryRow(ctx, `SELECT loomplane_managed_cleanup_admission_block('org','cloud','other-cluster')`).Scan(&unrelated); err != nil || unrelated != "" {
				t.Fatal("drift affected unrelated resource", unrelated, err)
			}
			req := kube.ManagedRequest{Group: "apps", Version: "v1", Resource: "deployments", Name: "new-instance", Namespace: scope.Namespace, Method: "POST"}
			if mode == "guaranteed" {
				resolve("managed_cleanup_drift")
				if _, err = journal.Issue(ctx, scope, req); err == nil || !strings.Contains(err.Error(), "managed_cleanup_drift") {
					t.Fatalf("first guaranteed effect: %v", err)
				}
			} else {
				resolve("")
				best, writeErr := journal.Issue(ctx, scope, req)
				if writeErr != nil {
					t.Fatal("best effort blocked by guarantee drift", writeErr)
				}
				if writeErr = journal.Resolve(ctx, best, scope, kube.ManagedOutcome{State: "rejected", StatusCode: 409}); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			event(false)
			write, err := journal.Issue(ctx, scope, req)
			if err != nil {
				t.Fatal("qualified first effect", err)
			}
			if err = journal.Resolve(ctx, write, scope, kube.ManagedOutcome{State: "rejected", StatusCode: 409}); err != nil {
				t.Fatal(err)
			}
			resolve("")
			var count int
			if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_cluster_workload_holds WHERE plan_id=$1`, plan.ID).Scan(&count); err != nil || (mode == "guaranteed" && count != 1) || (mode == "best_effort" && count != 0) {
				t.Fatal("reservation duplication", count, err)
			}
		})
	}
}
