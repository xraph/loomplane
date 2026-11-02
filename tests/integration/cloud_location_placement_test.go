//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
)

type locationCapacityCounter struct{ calls int }

func TestCloudLocationPlacementReadyPoolSync(t *testing.T) {
	f, svc := sharedAcceptanceFixture(t)
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_bindings SET config=jsonb_set(config,'{placement}','{}')`); err != nil {
		t.Fatal(err)
	}
	preview := reviewPoolChange(t, svc)
	if _, err := svc.ApplyCloudChange(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, "", preview.ID, preview.Revision, "location-ready"); err != nil {
		t.Fatal(err)
	}
}

func (c *locationCapacityCounter) EnsureCapacity(_ context.Context, _ cloudinfra.CapacityDemand) (*cloudinfra.CapacityDecision, error) {
	c.calls++
	return nil, fmt.Errorf("unexpected capacity effect")
}

func locationDeferredSyncFixture(t *testing.T) (*poolFixture, *planning.Service, *locationCapacityCounter, *cloudprofiles.Sync) {
	t.Helper()
	f, _ := sharedAcceptanceFixture(t)
	ctx := t.Context()
	next, err := f.store.GetPool(ctx, "org", "cloud", "next-pool")
	if err != nil {
		t.Fatal(err)
	}
	next.Members = nil
	next.Revision++
	if err = f.store.SavePool(ctx, next, next.Revision-1); err != nil {
		t.Fatal(err)
	}
	policy := cloudinfra.CapacityPolicy{OrgID: "org", CloudID: "cloud", Revision: 1, Enabled: true, Pools: []cloudinfra.PoolExpansionPolicy{{PoolID: "next-pool", ConnectionID: "runtime-1", Region: "region", Architecture: "amd64", Image: "ubuntu", AllowedSizes: []string{"small"}}}}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(ctx, `INSERT INTO loomplane_cloud_capacity_policies(org_id,cloud_id,revision,enabled,servers,cpu_millis,memory_bytes,storage_bytes,body) VALUES('org','cloud',1,true,10,20000,21474836480,107374182400,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_bindings SET config=jsonb_set(config,'{placement}','{}')`); err != nil {
		t.Fatal(err)
	}
	counter := &locationCapacityCounter{}
	svc := planning.NewService(planning.Deps{Store: f.plans, Projects: projects.NewPgStore(f.db), CloudProfiles: f.clouds, Connections: &syncAccounts{store: connections.NewPgStore(f.db)}, Providers: providers.Registry(), Authz: &syncAuth{}, CapacityTemplates: capacityPlanningTemplate{}, Capacity: counter})
	svc.BindCloudSyncRunner(nil, acceptanceInputs{})
	preview := reviewPoolChange(t, svc)
	accepted, err := svc.ApplyCloudChange(ctx, security.Principal{OrgID: "org", UserID: "usr"}, "", preview.ID, preview.Revision, "location-future")
	if err != nil {
		t.Fatal(err)
	}
	return f, svc, counter, accepted
}

func TestCloudLocationPlacementDeferredSyncRetainsReview(t *testing.T) {
	f, svc, counter, accepted := locationDeferredSyncFixture(t)
	ctx := t.Context()
	if _, err := f.clouds.SaveLocationPolicy(ctx, "org", "cloud", cloudprofiles.LocationPolicy{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResolveCapacityIntents(ctx, "org", accepted.ID+".env"); err == nil || counter.calls != 0 {
		t.Fatalf("stale deferred review reached capacity: calls=%d err=%v", counter.calls, err)
	}
	var review []byte
	if err := f.sql.QueryRow(ctx, `SELECT body->'demand'->'location_review' FROM loomplane_capacity_intents WHERE plan_id=$1`, accepted.ID+".env").Scan(&review); err != nil || len(review) == 0 {
		t.Fatalf("deferred review lost: %s %v", review, err)
	}
	var connection string
	if err := f.sql.QueryRow(ctx, `SELECT connection_id FROM loomplane_bindings WHERE id='binding-env'`).Scan(&connection); err != nil || connection != "runtime-1" {
		t.Fatalf("serving source changed: %s %v", connection, err)
	}
}

func TestCloudLocationPlacementDeferredSyncAdmitsReview(t *testing.T) {
	f, _, _, accepted := locationDeferredSyncFixture(t)
	ctx := t.Context()
	next, err := f.store.GetPool(ctx, "org", "cloud", "next-pool")
	if err != nil {
		t.Fatal(err)
	}
	next.Members = []cloudinfra.PoolMember{{ServerID: "server-2", CPUMillis: 2000, MemoryBytes: 2 << 30}}
	if _, err = f.sql.Exec(ctx, `INSERT INTO loomplane_server_pool_members(org_id,pool_id,server_id,cpu_millis,memory_bytes) VALUES('org','next-pool','server-2',2000,2147483648)`); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_server_pools SET body=$1 WHERE id='next-pool'`, raw); err != nil {
		t.Fatal(err)
	}
	req := f.request("env", "api")
	req.PoolID, req.TargetRevision, req.PlanID = "next-pool", 2, accepted.ID+".env"
	candidate, err := f.store.PreviewPlacement(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	resolver := planning.NewService(planning.Deps{Store: f.plans, Authz: &capacityPlanAuth{}, Capacity: capacityPlanCandidate{candidate}})
	if err = resolver.ResolveCapacityIntents(ctx, "org", req.PlanID); err != nil {
		t.Fatal(err)
	}
	pins, err := f.plans.ResolvedCapacityPlacements(ctx, "org", req.PlanID)
	if err != nil || pins["api"].LocationReview == nil || pins["api"].LocationReview.SelectedTargetID != "pool-target" {
		t.Fatalf("admitted placement lost review: %+v %v", pins, err)
	}
}

func TestCloudLocationPlacementSyncPolicyChanged(t *testing.T) {
	f := newSyncFixture(t)
	ctx := t.Context()
	if _, err := f.conn.Exec(ctx, `UPDATE loomplane_cloud_connections SET checked_at=now()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(ctx, `UPDATE loomplane_bindings SET config=jsonb_set(config,'{placement}','{}')`); err != nil {
		t.Fatal(err)
	}
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	preview, err := f.service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"})
	if err != nil || len(preview.Blockers) > 0 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	if _, err = f.store.SaveLocationPolicy(ctx, "org", "cloud", cloudprofiles.LocationPolicy{PreferredProviders: []string{"vercel"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "location-change"); err == nil {
		t.Fatal("policy changed after review but Cloud sync was accepted")
	}
	if got := bindingConnection(t, f, "binding-env-api"); got != "old" {
		t.Fatalf("old serving binding changed: %s", got)
	}
}

func TestCloudLocationPlacementPolicyPersistence(t *testing.T) {
	st, _, _ := cloudSyncDatabase(t)
	ctx := t.Context()
	p, err := st.GetLocationPolicy(ctx, "org", "cloud")
	if err != nil || p.Revision != 0 {
		t.Fatalf("default: %+v %v", p, err)
	}
	saved, err := st.SaveLocationPolicy(ctx, "org", "cloud", cloudprofiles.LocationPolicy{PreferredLocations: []string{"location"}})
	if err != nil || saved.Revision != 1 {
		t.Fatalf("save: %+v %v", saved, err)
	}
	if _, err = st.SaveLocationPolicy(ctx, "org", "cloud", cloudprofiles.LocationPolicy{}); err == nil {
		t.Fatal("stale revision accepted")
	}
	if _, err = st.GetLocationPolicy(ctx, "other", "cloud"); err == nil {
		t.Fatal("foreign policy disclosed")
	}
}

func TestCloudLocationPlacementAcceptanceGrantChanged(t *testing.T) {
	f := newSyncFixture(t)
	ctx := t.Context()
	a, err := f.store.Resolve(ctx, "org", "proj", "env")
	if err != nil {
		t.Fatal(err)
	}
	review := &cloudprofiles.LocationReview{CloudID: "cloud", GrantRevision: a.GrantRevision, Candidates: []cloudprofiles.LocationCandidatePin{{TargetID: "target", TargetRevision: 1}}}
	cfg := &planning.ServiceConfig{Placement: &manifest.PlacementConstraints{}, CloudPlacement: &planning.CloudPlacement{LocationReview: review}}
	raw, err := json.Marshal(planning.Result{Connections: map[string]string{}, Actions: []planning.Action{{ID: "service/api", Name: "api", Subject: planning.ServiceSubject, Op: planning.Create, Service: cfg}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.conn.Exec(ctx, `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,result,created_by,created_at) VALUES('location-plan','org','proj','env','production','ready','manifest',1,$1,'usr',now())`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.SetProjectGrant(ctx, "org", "cloud", "proj", nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err = planning.NewPgStore(f.db).Decide(ctx, "org", "location-plan", planning.StatusApproved, "usr", "", time.Now()); err == nil {
		t.Fatal("revoked grant accepted")
	}
}

func TestCloudLocationPlacementEvidenceAndRevalidation(t *testing.T) {
	st, conn, db := cloudSyncDatabase(t)
	ctx := t.Context()
	now := time.Now().UTC()
	if _, err := conn.Exec(ctx, `UPDATE loomplane_cloud_connections SET checked_at=now()`); err != nil {
		t.Fatal(err)
	}
	l := cloudprofiles.CloudLocation{ID: "location", OrgID: "org", CloudID: "cloud", Name: "Primary", Provider: "vercel", AccountID: connections.AccountIdentity("vercel", "old", map[string]string{"team_id": "old"}), ConnectionID: "old", Region: "iad1", Revision: 1}
	if err := st.SaveLocation(ctx, &l, 1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE loomplane_cloud_targets SET location_id='location',region='iad1',revision=revision+1 WHERE id='target'; INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('org','proj','env','cloud',1)`); err != nil {
		t.Fatal(err)
	}
	svc := cloudprofiles.NewService(cloudprofiles.Deps{Store: st, Authz: &infraAuth{}, Connections: &syncAccounts{store: connections.NewPgStore(db)}, Providers: providers.Registry()})
	checked, err := svc.VerifyLocation(ctx, security.Principal{OrgID: "org", UserID: "manager"}, "cloud", "location", cloudprofiles.LocationVerificationInput{CloudRevision: 2, Revision: 1, EvidenceRef: "audit:residency", FailureDomains: []cloudprofiles.AttestedFailureDomain{{Kind: "residency", ID: "US"}}})
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := st.Resolve(ctx, "org", "proj", "env")
	if err != nil {
		t.Fatal(err)
	}
	plan := &planning.Plan{OrgID: "org", ProjectID: "proj", EnvironmentID: "env"}
	set := manifest.Settings{Placement: &manifest.PlacementConstraints{Residency: []string{"US"}}}
	candidate, err := planning.NewPgStore(db).LocationCandidate(ctx, plan, assignment, assignment.Targets[0], set, cloudinfra.PlacementRequest{}, cloudinfra.ClusterWorkloadVector{})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := planning.EvaluateLocations(planning.LocationPlacementInput{Now: now, OrgID: "org", ProjectID: "proj", EnvironmentID: "env", GrantRevision: assignment.GrantRevision, Constraints: *set.Placement, Candidates: []planning.PlacementCandidate{candidate}})
	if err != nil || decision.TargetID != "target" {
		t.Fatalf("verified residency blocked: %+v %v", decision, err)
	}
	review := &cloudprofiles.LocationReview{SelectedTargetID: "target", CloudID: "cloud", GrantRevision: assignment.GrantRevision, Candidates: decision.Pins}
	if err = st.ValidateLocationReview(ctx, "org", "proj", review, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `UPDATE loomplane_cloud_connections SET checked_at=now()-interval '10 minutes' WHERE id='old'`); err != nil {
		t.Fatal(err)
	}
	if err = st.ValidateLocationReview(ctx, "org", "proj", review, time.Now()); err == nil {
		t.Fatal("expired runtime evidence accepted")
	}
	if _, err = conn.Exec(ctx, `UPDATE loomplane_cloud_connections SET checked_at=now() WHERE id='old'`); err != nil {
		t.Fatal(err)
	}
	changed := *checked
	changed.Revision++
	changed.Name = "Changed"
	if err = st.SaveLocation(ctx, &changed, 3, checked.Revision); err != nil {
		t.Fatal(err)
	}
	if err = st.ValidateLocationReview(ctx, "org", "proj", review, time.Now()); err == nil {
		t.Fatal("changed location accepted")
	}
}

func TestCloudLocationPlacementClusterCumulativeCapacity(t *testing.T) {
	f, _, in, _ := clusterWorkloadFixture(t)
	_, fits, err := f.store.PreviewLocationWorkload(t.Context(), in, cloudinfra.ClusterWorkloadVector{})
	if err != nil || !fits {
		t.Fatalf("ready CPU inventory rejected: %v %v", fits, err)
	}
	_, fits, err = f.store.PreviewLocationWorkload(t.Context(), in, cloudinfra.ClusterWorkloadVector{CPUMillis: 1500, MemoryBytes: 1000, Pods: 1})
	if err != nil || fits {
		t.Fatalf("cumulative capacity oversubscribed: %v %v", fits, err)
	}
}

func TestCloudLocationPlacementCapacityStalePolicyAndGrant(t *testing.T) {
	for _, change := range []string{"policy", "grant"} {
		t.Run(change, func(t *testing.T) {
			f, _, in, _ := clusterWorkloadFixture(t)
			ctx := t.Context()
			a, err := f.clouds.Resolve(ctx, "org", "proj", "env")
			if err != nil {
				t.Fatal(err)
			}
			in.LocationReview = &cloudprofiles.LocationReview{CloudID: "cloud", GrantRevision: a.GrantRevision, Candidates: []cloudprofiles.LocationCandidatePin{{TargetID: in.TargetID, TargetRevision: 1}}}
			switch change {
			case "policy":
				_, err = f.clouds.SaveLocationPolicy(ctx, "org", "cloud", cloudprofiles.LocationPolicy{Ranking: "default"})
			case "grant":
				_, err = f.clouds.SetProjectGrant(ctx, "org", "cloud", "proj", nil, true)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = workloadTx(t, f, func(tx driver.Tx) error {
				_, e := f.store.ReserveClusterWorkload(ctx, tx, in, clusterWorkloadGuard)
				return e
			})
			if err == nil {
				t.Fatalf("%s changed but capacity was reserved", change)
			}
			var count int
			if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_cluster_workload_holds WHERE org_id='org'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed reservation leaked: %d %v", count, err)
			}
		})
	}
}

func TestCloudLocationPlacementQualifiedClusterCPU(t *testing.T) {
	f, _, plan, _, _ := managedDemandFixture(t)
	ctx := t.Context()
	a, err := f.clouds.Resolve(ctx, "org", "proj", "env")
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.clouds.GetTarget(ctx, "org", "proj", "cloud", "cluster-target")
	if err != nil {
		t.Fatal(err)
	}
	set := manifest.Settings{Placement: &manifest.PlacementConstraints{Runtime: "kubernetes", Architecture: "amd64"}, Size: &manifest.Size{CPU: .05, Memory: 64}}
	candidate, err := f.plans.LocationCandidate(ctx, plan, a, *target, set, cloudinfra.PlacementRequest{Service: "api"}, cloudinfra.ClusterWorkloadVector{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := planning.EvaluateLocations(planning.LocationPlacementInput{Now: time.Now(), OrgID: "org", ProjectID: "proj", EnvironmentID: "env", GrantRevision: a.GrantRevision, Constraints: *set.Placement, Candidates: []planning.PlacementCandidate{candidate}})
	if err != nil || d.TargetID != "cluster-target" {
		t.Fatalf("qualified CPU destination unavailable: %+v %v", d, err)
	}
	set.Placement.GPU = &manifest.GPU{Kind: "nvidia-l4", Count: 1}
	candidate, err = f.plans.LocationCandidate(ctx, plan, a, *target, set, cloudinfra.PlacementRequest{Service: "api"}, cloudinfra.ClusterWorkloadVector{})
	if err != nil {
		t.Fatal(err)
	}
	d, err = planning.EvaluateLocations(planning.LocationPlacementInput{Now: time.Now(), OrgID: "org", ProjectID: "proj", EnvironmentID: "env", GrantRevision: a.GrantRevision, Constraints: *set.Placement, Candidates: []planning.PlacementCandidate{candidate}})
	if err != nil || d.TargetID != "" {
		t.Fatalf("unqualified GPU scheduled: %+v %v", d, err)
	}
}

func TestCloudLocationPlacementBeforeEffects(t *testing.T) {
	f := newSyncFixture(t)
	ctx := orchestration.CapacityContext(t.Context())
	f.executor.failStage = false
	if _, err := f.conn.Exec(ctx, `UPDATE loomplane_cloud_connections SET checked_at=now(); UPDATE loomplane_bindings SET config=jsonb_set(config,'{placement}','{}')`); err != nil {
		t.Fatal(err)
	}
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	preview, err := f.service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"})
	if err != nil || len(preview.Blockers) > 0 {
		t.Fatalf("preview %+v %v", preview, err)
	}
	sync, err := f.service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "effect-review")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.SaveLocationPolicy(ctx, "org", "cloud", cloudprofiles.LocationPolicy{Ranking: "default"}); err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, func([]byte) error { return nil }); err == nil {
		t.Fatal("stale location policy reached runtime")
	}
	if len(f.executor.calls) != 0 || len(f.executor.removed) != 0 {
		t.Fatalf("provider effects after stale review: %+v %+v", f.executor.calls, f.executor.removed)
	}
	if got := bindingConnection(t, f, "binding-env-api"); got != "old" {
		t.Fatal("serving binding changed")
	}
}

func TestCloudLocationPlacementPoolStickyAndCumulative(t *testing.T) {
	f := newPoolFixture(t)
	ctx := t.Context()
	req := f.request("env", "api")
	req.CPUMillis = 2000
	placed, err := f.store.PreviewPlacement(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	pin := f.plan(t, "applied", req, placed)
	f.accept(t, "applied")
	raw, err := json.Marshal(planning.ServiceConfig{Provider: "server", Target: placed.ConnectionID, CloudPlacement: pin})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.plans.PutBinding(ctx, &planning.Binding{ID: "binding-location", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "api", Provider: "server", ConnectionID: placed.ConnectionID, Config: raw, PlanID: "applied", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	a, err := f.clouds.Resolve(ctx, "org", "proj", "env")
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.clouds.GetTarget(ctx, "org", "proj", "cloud", "pool-target")
	if err != nil {
		t.Fatal(err)
	}
	plan := &planning.Plan{ID: "next", OrgID: "org", ProjectID: "proj", EnvironmentID: "env"}
	set := manifest.Settings{Size: &manifest.Size{CPU: 2, Memory: 512}, Placement: &manifest.PlacementConstraints{Architecture: "amd64"}}
	request := cloudinfra.PlacementRequest{Service: "api", StickyServerID: placed.ServerID, Allocation: placed.Allocation}
	c, err := f.plans.LocationCandidate(ctx, plan, a, *target, set, request, cloudinfra.ClusterWorkloadVector{})
	if err != nil || !c.ReadyCapacity || c.RuntimeConnectionID != placed.ConnectionID {
		t.Fatalf("sticky allocation counted twice: %+v %v", c, err)
	}
	request.Planned = []cloudinfra.PlannedPlacement{{Service: "other", Placement: cloudinfra.Placement{ServerID: placed.ServerID, CPUMillis: 1000, MemoryBytes: 512 << 20}}}
	c, err = f.plans.LocationCandidate(ctx, plan, a, *target, set, request, cloudinfra.ClusterWorkloadVector{})
	if err != nil || c.ReadyCapacity {
		t.Fatalf("planned demand ignored: %+v %v", c, err)
	}
}

func TestCloudLocationPlacementPolicyAggregate(t *testing.T) {
	for _, invalid := range []string{"version", "body"} {
		t.Run(invalid, func(t *testing.T) {
			st, conn, _ := cloudSyncDatabase(t)
			ctx := t.Context()
			if _, err := st.SaveLocationPolicy(ctx, "org", "cloud", cloudprofiles.LocationPolicy{}); err != nil {
				t.Fatal(err)
			}
			var version int
			var created, updated time.Time
			if err := conn.QueryRow(ctx, `SELECT schema_version,created_at,updated_at FROM loomplane_cloud_location_policies WHERE org_id='org' AND cloud_id='cloud'`).Scan(&version, &created, &updated); err != nil || version != 1 || created.IsZero() || updated.IsZero() {
				t.Fatalf("aggregate metadata: %d %v %v %v", version, created, updated, err)
			}
			query := `UPDATE loomplane_cloud_location_policies SET revision=2,body=jsonb_set(body,'{revision}','2'),schema_version=2 WHERE org_id='org' AND cloud_id='cloud'`
			if invalid == "body" {
				query = `UPDATE loomplane_cloud_location_policies SET revision=2,body='{"revision":2,"ranking":"invented"}' WHERE org_id='org' AND cloud_id='cloud'`
			}
			if _, err := conn.Exec(ctx, query); err != nil {
				t.Fatal(err)
			}
			if _, err := st.GetLocationPolicy(ctx, "org", "cloud"); err == nil {
				t.Fatal("invalid stored policy accepted")
			}
			if _, err := st.SaveLocationPolicy(ctx, "org", "cloud", cloudprofiles.LocationPolicy{Revision: 2}); err == nil {
				t.Fatal("unsupported stored policy overwritten")
			}
		})
	}
}
