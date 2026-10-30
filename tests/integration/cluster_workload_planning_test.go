//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

type managedDemandAuthority struct {
	policy   cloudinfra.ClusterScalingPolicy
	disabled bool
}

func (a managedDemandAuthority) EnsureCapacity(context.Context, cloudinfra.CapacityDemand) (*cloudinfra.CapacityDecision, error) {
	return nil, errors.New("server expansion must not run")
}
func (a managedDemandAuthority) ClusterWorkloadCapacityEnabled() bool { return !a.disabled }
func (a managedDemandAuthority) CheckClusterWorkloadCapacity(context.Context) error {
	if a.disabled {
		return cloudinfra.ErrConflict
	}
	return nil
}
func (a managedDemandAuthority) CurrentClusterScalingPolicy(context.Context, string, string, string, string) (*cloudinfra.ClusterScalingPolicy, error) {
	return &a.policy, nil
}

func managedDemandFixture(t *testing.T, identity ...map[string]string) (*poolFixture, *planning.Service, *planning.Plan, *planning.Result, *capacityPlanAuth) {
	t.Helper()
	f, r, _, _ := clusterWorkloadFixture(t, identity...)
	now := time.Now().UTC()
	policy := cloudinfra.ClusterScalingPolicy{ID: "scaling-fixture", OrgID: "org", SchemaVersion: 1, Revision: 1, ResourceID: r.ID, PoolID: "pool", Mode: "native", MinNodes: 1, MaxNodes: 3, OperationID: "scaling-operation", CooldownSeconds: 600}
	r.ScalingPolicies = map[string]cloudinfra.ClusterScalingPolicy{"pool": policy}
	r.Observation.Version = "1.36"
	r.Observation.ObservedAt = now
	r.Observation.Pools = []provider.NodePoolSpec{{ID: "pool", Name: "pool", Size: "known-fixture", Count: 1, Autoscale: true, Min: 1, Max: 3}}
	r.Revision++
	if _, err := f.store.ClaimManagedResource(t.Context(), r, r.Revision-1); err != nil {
		t.Fatal(err)
	}
	revision, err := f.store.BeginClusterCapacityObservation(t.Context(), "org", "cloud", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := cloudinfra.ClusterCapacitySnapshot{ResourceID: r.ID, Provider: r.Provider, AccountID: r.AccountID, Ref: r.Observation.Ref, ObservationRevision: revision, CheckedAt: now, Complete: true, WorkloadsComplete: true, Workloads: []cloudinfra.ClusterObservedWorkload{{PodUID: "busy-application", Requests: cloudinfra.ClusterWorkloadVector{CPUMillis: 1900, MemoryBytes: 1 << 30, Pods: 1}}}, Nodes: []cloudinfra.ClusterNodeCapacity{{ExternalVMID: "vm", NodeUID: "node", PoolID: "pool", Ready: true, Architecture: "amd64", Zone: "zone", Allocatable: cloudinfra.ClusterResourceMetrics{CPUMillis: inventoryNumber(2000), MemoryBytes: inventoryNumber(2 << 30), EphemeralStorageBytes: inventoryNumber(10 << 30), Pods: inventoryNumber(20)}}}}
	snapshot.SchedulingProfiles = cloudinfra.BuildClusterSchedulingProfiles(snapshot, *r.Observation, kube.CapacityInventory{Complete: true, CheckedAt: now, Nodes: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node", Labels: map[string]string{corev1.LabelInstanceTypeStable: "known-fixture"}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.36"}}}}}, now)
	if len(snapshot.SchedulingProfiles) != 1 {
		t.Fatal("profile fixture unqualified")
	}
	if err = f.store.SaveClusterCapacity(t.Context(), r, snapshot); err != nil {
		t.Fatal(err)
	}
	envelope := cloudinfra.ClusterCapacityEnvelope{SchemaVersion: 1, ID: "envelope", OrgID: "org", CloudID: "cloud", ResourceID: r.ID, Provider: r.Provider, AccountID: r.AccountID, PoolID: "pool", Ref: r.Observation.Ref, Maximum: cloudinfra.ResourceVector{Servers: 3}, DesiredMaximum: cloudinfra.ResourceVector{Servers: 3}, State: "active", CheckedAt: now}
	raw, _ := json.Marshal(envelope)
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_cluster_capacity_envelopes(org_id,cloud_id,id,resource_id,provider,account_id,pool_id,revision,body) VALUES('org','cloud','envelope','cluster','digitalocean','account','pool',1,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	auth := &capacityPlanAuth{}
	svc := planning.NewService(planning.Deps{Store: f.plans, Authz: auth, Capacity: managedDemandAuthority{policy: policy}})
	plan := &planning.Plan{ID: "managed-plan", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", CreatedBy: "usr"}
	cfg := &planning.ServiceConfig{Provider: "kubernetes", Target: "cluster-runtime", TargetSettings: map[string]string{"managed_attachment_id": "attachment", "managed_project_id": "proj", "managed_resource_id": "cluster", "architecture": "amd64"}, Size: &manifest.Size{CPU: .5, Memory: 128}, Scale: &manifest.Scale{Min: 1, Max: 4}, CloudPlacement: &planning.CloudPlacement{CloudID: "cloud", CloudRevision: 1, AssignmentRevision: 1, TargetID: "cluster-target", TargetRevision: 1, GrantRevision: 1, ConnectionID: "cluster-runtime", Provider: "kubernetes"}}
	result := &planning.Result{Executable: true, Connections: map[string]string{"kubernetes": "cluster-runtime"}, Actions: []planning.Action{{ID: "service:api", Subject: planning.ServiceSubject, Name: "api", Op: planning.Create, Provider: "kubernetes", Service: cfg}}}
	return f, svc, plan, result, auth
}

func saveManagedDemandPlan(t *testing.T, f *poolFixture, plan *planning.Plan, result *planning.Result) {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at) VALUES($1,'org','proj','env','production','ready','managed-review','pool-manifest-proj',1,$2,true,'usr',now())`, plan.ID, raw); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.plans.Decide(t.Context(), "org", plan.ID, "approved", "usr", "", time.Now()); err == nil || ok {
		t.Fatal("approval without authenticated actor succeeded")
	}
	acceptFuture(t, f, plan.ID)
}

func TestClusterScalingApprovedDemandReservation(t *testing.T) {
	f, svc, plan, result, _ := managedDemandFixture(t)
	if err := svc.PrepareClusterWorkloadDemands(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	saveManagedDemandPlan(t, f, plan, result)
	for range 2 {
		err := svc.ResolveCapacityIntents(t.Context(), "org", plan.ID)
		var blocked *cloudinfra.CapacityAdmissionError
		if !errors.As(err, &blocked) || blocked.Block.Reason != "managed_workload_execution_unqualified" {
			t.Fatalf("reservation did not reach execution gate: %v", err)
		}
	}
	var raw []byte
	var count int
	if err := f.sql.QueryRow(t.Context(), `SELECT body FROM loomplane_cluster_workload_holds WHERE plan_id=$1`, plan.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var hold cloudinfra.ClusterWorkloadHold
	if err := json.Unmarshal(raw, &hold); err != nil {
		t.Fatal(err)
	}
	if hold.Input.Maximum.CPUMillis != 2000 || hold.Input.Maximum.Pods != 4 || hold.Input.Native == nil {
		t.Fatalf("HPA maximum not retained: %+v", hold)
	}
	if err := f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cluster_workload_holds WHERE plan_id=$1`, plan.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate hold: %d %v", count, err)
	}
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions,0,service,size,cpu}','99') WHERE id=$1`, plan.ID); err == nil {
		t.Fatal("approved maximum remained mutable")
	}
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_capacity_inventory SET body=jsonb_set(body,'{checked_at}',to_jsonb(now()-interval '3 minutes')) WHERE resource_id='cluster'`); err != nil {
		t.Fatal(err)
	}
	err := svc.ResolveCapacityIntents(t.Context(), "org", plan.ID)
	var blocked *cloudinfra.CapacityAdmissionError
	if !errors.As(err, &blocked) || blocked.Block.Reason != "native_template_unqualified" {
		t.Fatalf("expired profile: %v", err)
	}
}

func TestClusterScalingDemandCurrentAuthority(t *testing.T) {
	for _, which := range []string{"principal", "grant", "cancelled", "empty_profile", "best_effort", "missing_requests", "maximum_mismatch"} {
		t.Run(which, func(t *testing.T) {
			f, svc, plan, result, auth := managedDemandFixture(t)
			if which == "best_effort" {
				result.Actions[0].Service.CapacityMode = "best_effort"
				result.Actions[0].Service.Size = nil
			}
			if which == "missing_requests" {
				result.Actions[0].Service.Size = nil
			}
			err := svc.PrepareClusterWorkloadDemands(t.Context(), plan, result)
			if which == "missing_requests" {
				if err == nil {
					t.Fatal("missing requests accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if which == "maximum_mismatch" {
				d := result.Actions[0].Service.ClusterDemand
				d.Input.Maximum.CPUMillis++
				d.Input.ReviewDigest = ""
				d.Input.ReviewDigest, _ = planning.Digest(d)
			}
			saveManagedDemandPlan(t, f, plan, result)
			switch which {
			case "principal":
				auth.denied = true
			case "grant":
				_, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_project_grants SET revoked=true,revision=revision+1 WHERE org_id='org' AND cloud_id='cloud' AND project_id='proj'`)
			case "cancelled":
				_, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at,finished_at) VALUES('cancelled-managed','org','proj','env','production',$1,'reviewed','cancelled','usr',now(),now())`, plan.ID)
			case "empty_profile":
				_, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_capacity_inventory SET body=jsonb_set(body,'{scheduling_profiles}','[]') WHERE resource_id='cluster'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = svc.ResolveCapacityIntents(t.Context(), "org", plan.ID); err == nil {
				t.Fatal("execution must remain gated")
			}
			var count int
			if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cluster_workload_holds WHERE plan_id=$1`, plan.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("denied/best-effort demand reserved guaranteed fleet capacity: %d", count)
			}
		})
	}
}
