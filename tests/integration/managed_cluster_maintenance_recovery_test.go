//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

type maintenanceRecoveryCredentials struct{ store *connections.PgStore }

func (c maintenanceRecoveryCredentials) Credential(ctx context.Context, org, id string) (*connections.Connection, []byte, error) {
	conn, err := c.store.GetConnection(ctx, org, id)
	return conn, []byte("test-only"), err
}

type maintenanceRecoveryProvider struct {
	enrollmentKindProvider
	effects   int
	readError error
	reads     int
}

func (p *maintenanceRecoveryProvider) GetCluster(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef) (provider.ClusterObservation, error) {
	p.reads++
	if p.readError != nil {
		return provider.ClusterObservation{}, p.readError
	}
	return p.enrollmentKindProvider.GetCluster(ctx, a, ref)
}
func (p *maintenanceRecoveryProvider) ObserveClusterMutation(context.Context, provider.AccountAccess, provider.ClusterMutation) (provider.ClusterObservation, error) {
	return p.observation, nil
}
func (*maintenanceRecoveryProvider) Options(context.Context, provider.AccountAccess, string) (provider.ClusterOptions, error) {
	price := 0.02
	return provider.ClusterOptions{Digest: "options", CheckedAt: time.Now().Add(-time.Second), Regions: []string{"nyc1"}, NodeSizes: []string{"s-2"}, NodeSizeFacts: []provider.ClusterNodeSize{{ID: "s-2", CPUs: 2, MemoryMiB: 4096, DiskGB: 80, HourlyUSD: &price, Regions: []string{"nyc1"}}}}, nil
}
func (*maintenanceRecoveryProvider) CapacityInventory(context.Context, provider.InfrastructureCredential) (provider.CapacityInventory, error) {
	quota := int64(20)
	return provider.CapacityInventory{Provider: "digitalocean", AccountID: "account", CheckedAt: time.Now(), Quota: provider.CapacityQuota{Servers: &quota}}, nil
}
func (*maintenanceRecoveryProvider) CreateNodePool(context.Context, provider.AccountAccess, provider.ClusterRef, provider.NodePoolSpec) (provider.ClusterMutation, error) {
	return provider.ClusterMutation{}, provider.ErrInfrastructureUnsupported
}
func (*maintenanceRecoveryProvider) DeleteNodePool(context.Context, provider.AccountAccess, provider.ClusterRef, string) (provider.ClusterMutation, error) {
	return provider.ClusterMutation{}, provider.ErrInfrastructureUnsupported
}
func (p *maintenanceRecoveryProvider) UpdateNodePool(ctx context.Context, a provider.AccountAccess, _ provider.ClusterRef, _ string, spec provider.NodePoolSpec) (provider.ClusterMutation, error) {
	if err := a.BeforeEffect(ctx); err != nil {
		return provider.ClusterMutation{}, err
	}
	p.effects++
	p.observation.Pools[0].Count = spec.Count
	// The provider accepted the request, but its response never reached the worker.
	return provider.ClusterMutation{}, provider.ErrInfrastructureAmbiguous
}
func (p *maintenanceRecoveryProvider) UpdateNodePoolScaling(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef, pool string, bounds provider.NodePoolScaling) (provider.ClusterMutation, error) {
	if err := a.BeforeEffect(ctx); err != nil {
		return provider.ClusterMutation{}, err
	}
	if bounds.Native || bounds.Max != 0 || bounds.Min != 0 {
		return provider.ClusterMutation{}, provider.ErrInfrastructureUnsupported
	}
	p.effects++
	return provider.ClusterMutation{Kind: "node-pool-scaling", Ref: ref, PoolID: pool}, nil
}
func TestClusterScalingRevokeBeforeRetirement(t *testing.T) { maintenanceRecoveryCase(t, true) }
func TestManagedClusterMaintenanceServiceResponseLossRestart(t *testing.T) {
	maintenanceRecoveryCase(t, false)
}
func maintenanceRecoveryCase(t *testing.T, revoke bool) {
	db := managedClusterDB(t)
	store, clouds := managedClusterStore(t, db)
	pg := openPg(t, db.Config().ConnString())
	if _, err := db.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET cloud='digitalocean',generation=1,details='{"account_uuid":"account"}' WHERE id='conn'`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ref := provider.ClusterRef{ID: "physical", AccountID: "account", Region: "nyc1", Incarnation: "birth"}
	observed := provider.ClusterObservation{Ref: ref, State: "running", Endpoint: "https://cluster.test", CAHash: "ca", Pools: []provider.NodePoolSpec{{ID: "pool", Name: "workers", Size: "s-2", Count: 2, Labels: map[string]string{}, Taints: []provider.NodePoolTaint{}}}, ObservedAt: now}
	original := &cloudinfra.ClusterOperation{ID: "original", OrgID: "org", CloudID: "cloud", Kind: "create", SchemaVersion: 1, Status: "preview", Revision: 1, Digest: "original", Input: cloudinfra.ClusterInput{Kind: "create", Provider: "digitalocean", AccountID: "account", ConnectionID: "conn", CloudRevision: 1, ConnectionGeneration: 1, Spec: provider.ClusterSpec{OperationID: "original"}}, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := store.CreateClusterPreview(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	receipt := provider.ClusterMutation{OperationID: original.ID, Kind: "create", Ref: ref, RequestDigest: provider.ClusterRequestDigest(original.Input.Spec)}
	original.ResourceID = "cluster"
	original.Receipts = []provider.ClusterMutation{receipt}
	original.Status = "succeeded"
	original.Revision++
	r := &cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", ConnectionID: "conn", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: ref.ID, Incarnation: ref.Incarnation, Ownership: "managed", Status: "running", Revision: 1, ObservedRevision: 1, SchemaVersion: 1, Observation: &observed, CreationReceipt: &receipt}
	native := cloudinfra.ClusterScalingPolicy{ID: "prior-policy", OrgID: "org", ResourceID: "cluster", SchemaVersion: 1, Revision: 1, OperationID: "prior-native", PoolID: "pool", Mode: "native", MinNodes: 1, MaxNodes: 3, CooldownSeconds: 600}
	if !revoke {
		r.ScalingPolicies = map[string]cloudinfra.ClusterScalingPolicy{"pool": {ID: "manual", OrgID: "org", ResourceID: "cluster", PoolID: "pool", Revision: 1, Mode: "manual", Revoked: true, CooldownSeconds: 600}}
		observed.Version = "1.36"
	}
	if revoke {
		observed.Pools[0].Autoscale = true
		observed.Pools[0].Min = 1
		observed.Pools[0].Max = 3
		r.ScalingPolicies = map[string]cloudinfra.ClusterScalingPolicy{"pool": native}
	}
	if _, err := store.ClaimManagedResource(t.Context(), r, 0); err != nil {
		t.Fatal(err)
	}

	inventoryRevision, inventoryErr := store.BeginClusterCapacityObservation(t.Context(), "org", "cloud", r.ID)
	if inventoryErr != nil {
		t.Fatal(inventoryErr)
	}
	servers, cpu, memory, disk := int64(1), int64(2000), int64(4<<30), int64(80_000_000_000)
	inventory := cloudinfra.ClusterCapacitySnapshot{ResourceID: r.ID, Provider: r.Provider, AccountID: r.AccountID, Ref: ref, ObservationRevision: inventoryRevision, CheckedAt: time.Now().UTC(), Complete: true}
	for _, id := range []string{"node-1", "node-2"} {
		inventory.Nodes = append(inventory.Nodes, cloudinfra.ClusterNodeCapacity{ExternalVMID: id, NodeUID: id, PoolID: "pool", Provisioned: cloudinfra.CapacityResources{Servers: &servers, CPUMillis: &cpu, MemoryBytes: &memory, StorageBytes: &disk}})
	}
	if inventoryErr = store.SaveClusterCapacity(t.Context(), r, inventory); inventoryErr != nil {
		t.Fatal(inventoryErr)
	}
	if err := store.SaveClusterCheckpoint(t.Context(), original, 1); err != nil {
		t.Fatal(err)
	}
	policy := cloudinfra.CapacityPolicy{OrgID: "org", CloudID: "cloud", Revision: 1, Limits: cloudinfra.ResourceVector{Servers: 10, CPUMillis: 20000, MemoryBytes: 40 << 30, StorageBytes: 800_000_000_000}}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(t.Context(), `INSERT INTO loomplane_cloud_capacity_policies(org_id,cloud_id,revision,body) VALUES('org','cloud',1,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	adapter := &maintenanceRecoveryProvider{enrollmentKindProvider: enrollmentKindProvider{observation: observed}}
	makeService := func() *cloudinfra.Service {
		adapters := cloudinfra.NewAdapters()
		if e := adapters.RegisterCluster("digitalocean", adapter, provider.NodePoolUpdate, provider.NodePoolAutoscaling, provider.ClusterMutationObservation); e != nil {
			t.Fatal(e)
		}
		return cloudinfra.NewService(cloudinfra.Deps{Clusters: cloudinfra.NewPgStore(pg), Clouds: clouds, Connections: maintenanceRecoveryCredentials{connections.NewPgStore(pg)}, Authz: enrollmentServiceAuth{}, Adapters: adapters, ClusterCapacity: config.ClusterCapacityConfig{Enabled: true}, ManagedClusters: config.ManagedClustersConfig{Enabled: true, QualificationOrgID: "org", QualificationOperatorID: "manager", QualificationAccountID: "account", QualificationRegion: "nyc1", MaxNodes: 3, MaxNodeHourlyMicroUSD: 1000000}})
	}
	svc := makeService()
	principal := security.Principal{OrgID: "org", UserID: "manager"}
	in := cloudinfra.ClusterInput{Kind: "node-pool-update", Provider: "digitalocean", ConnectionID: "conn", ConnectionGeneration: 1, AccountID: "account", CloudRevision: 1, PolicyRevision: 1, ResourceID: "cluster", ResourceRevision: 1, Ref: ref, OptionsDigest: "options", AcknowledgeNodeCosts: true, AcknowledgeUnknownCosts: true, Maintenance: &cloudinfra.ClusterMaintenanceInput{ObservedRevision: 1, PoolID: "pool", Pool: &provider.NodePoolSpec{Name: "workers", Size: "s-2", Count: 3}}}
	if revoke {
		prior := *original
		prior.ID = native.OperationID
		prior.Revision = 1
		prior.Status = "preview"
		prior.Kind = "node-pool-scaling"
		prior.Principal = principal
		prior.Receipts = nil
		prior.Input = in
		prior.Input.Kind = prior.Kind
		prior.Input.Scaling = &native
		prior.Input.Maintenance = &cloudinfra.ClusterMaintenanceInput{PoolID: "pool", ObservedRevision: 1}
		if err = store.CreateClusterPreview(t.Context(), &prior); err != nil {
			t.Fatal(err)
		}
		prior.Status = "succeeded"
		prior.Revision = 2
		if err = store.SaveClusterCheckpoint(t.Context(), &prior, 1); err != nil {
			t.Fatal(err)
		}
		adapter.readError = &provider.InfrastructureAPIError{Status: 429, RetryAfter: 7 * time.Minute}
		if _, e := svc.ObserveClusterScaling(t.Context(), "org", "cloud", "cluster", "pool"); e == nil {
			t.Fatal("missing provider throttle")
		}
		reads := adapter.reads
		adapter.readError = nil
		if _, e := makeService().ObserveClusterScaling(t.Context(), "org", "cloud", "cluster", "pool"); !errors.Is(e, cloudinfra.ErrPending) || adapter.reads != reads {
			t.Fatal("restart ignored provider throttle", e)
		}
		if _, err = db.Exec(t.Context(), `UPDATE loomplane_cluster_pool_advice SET retry_observation_at=clock_timestamp()-interval '1 second' WHERE org_id='org' AND resource_id='cluster' AND pool_id='pool'`); err != nil {
			t.Fatal(err)
		}
		// An independent change outside reviewed bounds must pause new admissions.
		adapter.observation.Pools[0].Max = 4
		_, obsErr := svc.ObserveClusterScaling(t.Context(), "org", "cloud", "cluster", "pool")
		current, readErr := store.GetManagedResource(t.Context(), "org", "cluster")
		if readErr != nil || current.ScalingPolicies["pool"].PauseReason != "native_bounds_changed" || current.ScalingPolicies["pool"].Revoked || adapter.effects != 0 {
			t.Fatal("external bounds drift", readErr, obsErr)
		}
		retirementBlocked := func(want bool) {
			t.Helper()
			deps, e := store.ClusterRetirementDependencies(t.Context(), "org", "cluster", "", "")
			if e != nil || slices.Contains(deps, "native_policy:pool") != want {
				t.Fatal("retirement dependency", deps, e)
			}
		}
		retirementBlocked(true)
		in.ResourceRevision = current.Revision
		in.Maintenance = &cloudinfra.ClusterMaintenanceInput{ObservedRevision: current.ObservedRevision, PoolID: "pool"}
		preview, e := svc.PreviewClusterScaling(t.Context(), principal, "cloud", "cluster", cloudinfra.ClusterScalingPolicy{PoolID: "pool", Mode: "manual", ExpectedRevision: 1, ReviewInput: &in})
		if e != nil {
			t.Fatal(e)
		}
		op, e := svc.ApplyCluster(t.Context(), principal, "cloud", preview.ID, cloudinfra.ApplyInput{Review: preview.Digest})
		if e != nil {
			t.Fatal(e)
		}
		if e = svc.ReconcileClusterOperation(t.Context(), "org", op.ID, nil); e != nil {
			t.Fatal(e)
		}
		retirementBlocked(true)
		adapter.observation.Pools[0].Autoscale = false
		adapter.observation.ObservedAt = time.Now().UTC()
		if _, e = db.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET body=jsonb_set(jsonb_set(body,'{lease_expires_at}',to_jsonb($2::text)),'{next_attempt_at}',to_jsonb($2::text)) WHERE id=$1`, op.ID, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)); e != nil {
			t.Fatal(e)
		}
		if e = makeService().ReconcileClusterOperation(t.Context(), "org", op.ID, nil); e != nil {
			t.Fatal(e)
		}
		current, e = store.GetManagedResource(t.Context(), "org", "cluster")
		if e != nil || !current.ScalingPolicies["pool"].Revoked || adapter.effects != 1 {
			t.Fatal("disable did not converge once", e, adapter.effects)
		}
		retirementBlocked(false)
		return
	}
	f := &poolFixture{sql: db, db: pg, store: store, clouds: clouds, plans: planning.NewPgStore(pg)}
	group := reviewedCountCohort(t, f, r, inventory)
	op, err := svc.PreviewCluster(t.Context(), principal, "cloud", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyCluster(t.Context(), principal, "cloud", op.ID, cloudinfra.ApplyInput{Review: op.Digest}); err != nil {
		t.Fatal(err)
	}
	if err = svc.ReconcileClusterOperation(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetClusterOperation(t.Context(), "org", "cloud", op.ID)
	if err != nil {
		t.Fatal(err)
	}
	var linked string
	if err = db.QueryRow(t.Context(), `SELECT c.operation_id FROM loomplane_cluster_demand_cohorts c JOIN loomplane_cluster_pool_growth g ON g.org_id=c.org_id AND g.resource_id=c.resource_id AND g.pool_id=c.pool_id AND g.operation_id=c.operation_id WHERE c.id=$1 AND c.closed_at IS NULL AND c.policy_revision=1 AND c.budget_revision=1 AND c.issued_at IS NOT NULL`, group).Scan(&linked); err != nil || linked != op.ID {
		t.Fatal("reviewed count did not link to its durable cohort", linked, err)
	}
	if saved.Steps[0].State != "attempted" || saved.Steps[0].AttemptedAt == nil || len(saved.Receipts) != 0 || adapter.effects != 1 {
		t.Fatalf("lost response state: %+v, effects %d", saved, adapter.effects)
	}
	if _, err = db.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET body=jsonb_set(jsonb_set(body,'{lease_expires_at}',to_jsonb($2::text)),'{next_attempt_at}',to_jsonb($2::text)) WHERE id=$1`, op.ID, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err = makeService().ReconcileClusterOperation(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	saved, err = store.GetClusterOperation(t.Context(), "org", "cloud", op.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.GetManagedResource(t.Context(), "org", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "succeeded" || adapter.effects != 1 || len(saved.Receipts) != 0 || current.Observation.Pools[0].ID != "pool" || current.Observation.Pools[0].Count != 3 {
		t.Fatalf("restart replayed or failed convergence: %+v effects=%d", saved, adapter.effects)
	}
	var status string
	var nodes int64
	if err = db.QueryRow(t.Context(), `SELECT status,servers FROM loomplane_cloud_capacity_reservations WHERE id=$1`, op.ID).Scan(&status, &nodes); err != nil || status != "reserved" || nodes != 1 {
		t.Fatalf("reservation not retained: %s %d %v", status, nodes, err)
	}
}

func reviewedCountCohort(t *testing.T, f *poolFixture, r *cloudinfra.ManagedResource, snap cloudinfra.ClusterCapacitySnapshot) string {
	t.Helper()
	now := time.Now().UTC()
	for _, q := range []string{
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('env','org','proj','production','production',now(),now()) ON CONFLICT DO NOTHING`,
		`INSERT INTO loomplane_cloud_project_grants(org_id,cloud_id,project_id) VALUES('org','cloud','proj') ON CONFLICT DO NOTHING`,
		`INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('org','proj','env','cloud',1) ON CONFLICT DO NOTHING`,
		`INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('pool-manifest-proj','org','proj',1,'version: 1','pool','usr',now())`,
	} {
		if _, err := f.sql.Exec(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	c := &connections.Connection{ID: "cluster-runtime", OrgID: "org", Cloud: "kubernetes", Method: connections.MethodManagedKubernetes, Name: "cluster-runtime", Status: "ready", Details: map[string]string{"namespace": "apps", "namespace_uid": "namespace", "cluster_ca": "ca", "managed_attachment_id": "attachment", "managed_resource_id": "cluster"}, CreatedAt: now, UpdatedAt: now}
	if err := connections.NewPgStore(f.db).CreateConnection(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	a := &cloudinfra.RuntimeAttachment{ID: "attachment", OrgID: "org", CloudID: "cloud", ClusterID: r.ID, ProjectID: "proj", Namespace: "apps", NamespaceUID: "namespace", ServiceAccountUID: "account", RuntimeConnectionID: c.ID, TrustFingerprint: "ca", TrustRevision: 1, Revision: 1}
	if err := f.store.SaveRuntimeAttachment(t.Context(), a, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.clouds.CreateTarget(t.Context(), &cloudprofiles.Target{ID: "cluster-target", OrgID: "org", ProjectID: "proj", CloudID: "cloud", Name: "cluster", DisplayName: "Cluster", Provider: "kubernetes", Runtime: "managed", ManagedResourceID: r.ID, ConnectionID: c.ID, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	rev, err := f.store.BeginClusterCapacityObservation(t.Context(), "org", "cloud", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	snap.ObservationRevision = rev
	snap.CheckedAt = now
	snap.WorkloadsComplete = true
	input := kube.CapacityInventory{Complete: true, CheckedAt: now}
	for i := range snap.Nodes {
		n := &snap.Nodes[i]
		n.Ready = true
		n.Architecture = "amd64"
		n.Zone = "nyc1"
		n.Allocatable = cloudinfra.ClusterResourceMetrics{CPUMillis: inventoryNumber(2000), MemoryBytes: inventoryNumber(4 << 30), EphemeralStorageBytes: inventoryNumber(10 << 30), Pods: inventoryNumber(20)}
		input.Nodes = append(input.Nodes, corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: n.NodeUID, UID: types.UID(n.NodeUID), Labels: map[string]string{corev1.LabelInstanceTypeStable: "s-2"}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.36"}}})
	}
	snap.SchedulingProfiles = cloudinfra.BuildClusterSchedulingProfiles(snap, *r.Observation, input, now)
	if len(snap.SchedulingProfiles) != 1 {
		t.Fatal("manual scheduling profile")
	}
	if err = f.store.SaveClusterCapacity(t.Context(), r, snap); err != nil {
		t.Fatal(err)
	}
	svc := planning.NewService(planning.Deps{Store: f.plans, Authz: &capacityPlanAuth{}, Capacity: managedDemandAuthority{}})
	svc.BindManagedRuntime(ledgerRuntimeFixture{})
	plan := &planning.Plan{ID: "manual-wait", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", CreatedBy: "usr"}
	cfg := &planning.ServiceConfig{Provider: "kubernetes", Target: "cluster-runtime", TargetSettings: map[string]string{"managed_attachment_id": "attachment", "managed_project_id": "proj", "managed_resource_id": "cluster", "architecture": "amd64"}, Size: &manifest.Size{CPU: .5, Memory: 128}, Scale: &manifest.Scale{Min: 1, Max: 90}, CloudPlacement: &planning.CloudPlacement{CloudID: "cloud", CloudRevision: 1, AssignmentRevision: 1, TargetID: "cluster-target", TargetRevision: 1, GrantRevision: 1, ConnectionID: "cluster-runtime", Provider: "kubernetes"}}
	result := &planning.Result{Executable: true, Connections: map[string]string{"kubernetes": "cluster-runtime"}, Actions: []planning.Action{{ID: "service:api", Subject: planning.ServiceSubject, Name: "api", Op: planning.Create, Provider: "kubernetes", Service: cfg}}}

	if err = svc.PrepareClusterWorkloadDemands(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	saveManagedDemandPlan(t, f, plan, result)
	d, err := svc.ReconcileClusterDemand(t.Context(), "org", "cloud", cfg.ClusterDemand.Input.ID)
	if err != nil || d.ClusterDemandState != "awaiting_review" || d.ClusterCohortID == "" || d.Block == nil {
		t.Fatal("unaccepted manual demand", d, err)
	}
	return d.ClusterCohortID
}
