//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/deployments"
)

func TestManagedNoEffectSettlement(t *testing.T) {
	for _, state := range []string{"unattached", "no_send", "rejected", "unknown", "accepted"} {
		t.Run(state, func(t *testing.T) {
			f, svc, p, result, _ := managedDemandFixture(t)
			svc.BindManagedRuntime(ledgerRuntimeFixture{})
			if err := svc.PrepareClusterWorkloadDemands(t.Context(), p, result); err != nil {
				t.Fatal(err)
			}
			saveManagedDemandPlan(t, f, p, result)
			if err := svc.ResolveCapacityIntents(t.Context(), "org", p.ID); err != nil {
				t.Fatal(err)
			}
			dep := capacityDeployment(t, deployments.NewPgStore(f.db), p)
			var scope kube.ManagedScope
			journal := kube.NewControllerStore(f.db).WithClusterCapacity(true).WithManagedDemandGuard(svc.GuardManagedDemandEffect)
			var issuance string
			if state != "unattached" {
				a, err := f.store.BeginWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: p.ID, Deployment: dep.ID}, nil, false)
				if err != nil {
					t.Fatal(err)
				}
				if err = workloadTx(t, f, func(tx driver.Tx) error {
					return f.store.AttachClusterWorkloadAttempt(t.Context(), tx, result.Actions[0].Service.ClusterDemand.Input, *a)
				}); err != nil {
					t.Fatal(err)
				}
				scope = kube.ManagedScope{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: p.ID, Deployment: dep.ID, Action: "service:api", Connection: "cluster-runtime", Instance: id.New(id.PrefixInstance).String(), RuntimeRef: "org/env/api/runtime_nosend", RuntimeRevision: dep.ID + "/service:api", Namespace: "apps", NamespaceUID: "namespace", ClusterCA: strings.Repeat("a", 64), Epoch: a.Generation}
				scope.Release = kube.WorkloadReleaseID(scope.RuntimeRef, scope.RuntimeRevision)
				if err = journal.OpenManagedScope(t.Context(), scope); err != nil {
					t.Fatal(err)
				}
				if state == "no_send" {
					r, readErr := f.store.GetManagedResource(t.Context(), "org", "cluster")
					if readErr != nil {
						t.Fatal(readErr)
					}
					policy := r.ScalingPolicies["pool"]
					policy.PauseReason = "after-open"
					r.ScalingPolicies["pool"] = policy
					r.Revision++
					if _, err = f.store.ClaimManagedResource(t.Context(), r, r.Revision-1); err != nil {
						t.Fatal(err)
					}
					if _, err = journal.Issue(t.Context(), scope, kube.ManagedRequest{Group: "apps", Version: "v1", Resource: "deployments", Namespace: "apps", Name: "app", Method: "POST", SchedulingArchitecture: "amd64"}); err == nil {
						t.Fatal("open authority bypassed current policy on Issue")
					}
				}
				if state != "no_send" {
					issuance, err = journal.Issue(t.Context(), scope, kube.ManagedRequest{Group: "apps", Version: "v1", Resource: "deployments", Namespace: "apps", Name: "app", Method: "POST", SchedulingArchitecture: "amd64"})
					if err != nil {
						t.Fatal(err)
					}
					if state != "unknown" {
						outcome := kube.ManagedOutcome{State: "rejected", StatusCode: 403}
						if state == "accepted" {
							outcome = kube.ManagedOutcome{State: "accepted", StatusCode: 201, UID: "remote", Generation: 1, ResourceVersion: "1"}
						}
						if err = journal.Resolve(t.Context(), issuance, scope, outcome); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			var before []byte
			if err := f.sql.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(outcome ORDER BY id),'[]') FROM loomplane_managed_workload_issuances WHERE scope->>'Plan'=$1`, p.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := deployments.NewPgStore(f.db).Finish(t.Context(), "org", dep.ID, deployments.StatusCancelled, "cancelled", time.Now()); err != nil {
				t.Fatal(err)
			}
			var held, settlements int
			var after []byte
			if err := f.sql.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM loomplane_cluster_workload_holds WHERE body->'input'->>'plan_id'=$1 AND NOT released),(SELECT count(*) FROM loomplane_cluster_demand_settlements WHERE plan_id=$1),(SELECT coalesce(jsonb_agg(outcome ORDER BY id),'[]') FROM loomplane_managed_workload_issuances WHERE scope->>'Plan'=$1)`, p.ID).Scan(&held, &settlements, &after); err != nil {
				t.Fatal(err)
			}
			retained := state == "unknown" || state == "accepted"
			if (held == 1) != retained || (settlements == 0) != retained || string(before) != string(after) {
				t.Fatal("unsafe settlement", held, settlements)
			}
			if _, err := f.store.BeginWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: p.ID, Deployment: dep.ID}, nil, false); err == nil {
				t.Fatal("cancelled attempt reopened")
			}
			if scope.Release != "" {
				if err := journal.OpenManagedScope(t.Context(), scope); err == nil {
					t.Fatal("cancelled release reopened")
				}
				if _, err := journal.Issue(t.Context(), scope, kube.ManagedRequest{Version: "v1", Resource: "services", Namespace: "apps", Name: "app", Method: "POST"}); err == nil {
					t.Fatal("cancelled release issued")
				}
			}
			if state == "unknown" {
				if err := journal.Resolve(t.Context(), issuance, scope, kube.ManagedOutcome{State: "accepted", StatusCode: 201, UID: "late", Generation: 1, ResourceVersion: "2"}); err != nil {
					t.Fatal("cancel blocked accepted-outcome recovery", err)
				}
			}
		})
	}
}
