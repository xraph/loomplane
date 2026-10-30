//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

func cleanupTargetFixture(t *testing.T) (*poolFixture, *planning.Service, *planning.Plan, *planning.Result, kube.ManagedScope) {
	t.Helper()
	f, svc, old, result, _ := managedDemandFixture(t, map[string]string{"namespace": "apps", "namespace_uid": "namespace-uid", "cluster_ca": strings.Repeat("a", 64)})
	result.Actions[0].Service.CapacityMode = "best_effort"
	if err := svc.PrepareClusterWorkloadDemands(t.Context(), old, result); err != nil {
		t.Fatal(err)
	}
	saveManagedDemandPlan(t, f, old, result)
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES('cleanup-old-deployment','org','proj','env','production',$1,'old','running','usr',now())`, old.ID); err != nil {
		t.Fatal(err)
	}
	attempt, err := f.store.BeginWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: old.ID, Deployment: "cleanup-old-deployment"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	scope := kube.ManagedScope{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: old.ID, Deployment: attempt.Deployment, Action: "service:api", Connection: "cluster-runtime", Instance: "old-instance", RuntimeRef: "org/env/api/runtime_cleanup", RuntimeRevision: attempt.Deployment + "/service:api", Namespace: "apps", NamespaceUID: "namespace-uid", ClusterCA: strings.Repeat("a", 64), Epoch: attempt.Generation}
	scope.Release = kube.WorkloadReleaseID(scope.RuntimeRef, scope.RuntimeRevision)
	store := kube.NewControllerStore(f.db).WithClusterCapacity(true)
	if err = store.OpenManagedScope(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	id, err := store.Issue(t.Context(), scope, kube.ManagedRequest{Group: "apps", Version: "v1", Resource: "deployments", Namespace: scope.Namespace, Name: "old-instance", Method: "POST"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Resolve(t.Context(), id, scope, kube.ManagedOutcome{State: "accepted", UID: "deployment-uid", ResourceVersion: "1", Generation: 1, StatusCode: 201}); err != nil {
		t.Fatal(err)
	}
	binding := &planning.Binding{ID: "cleanup-binding", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "api", Provider: "kubernetes", ExternalID: scope.Instance, PlanID: old.ID, ConnectionID: scope.Connection, Revision: "old-image", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = f.plans.PutBinding(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	plan := &planning.Plan{ID: "cleanup-new-plan", OrgID: "org", ProjectID: "proj", EnvironmentID: "env"}
	reviewed := &planning.Result{Executable: true, Connections: map[string]string{}, Actions: []planning.Action{{ID: "service:api", Subject: planning.ServiceSubject, Name: "api", Op: planning.Delete, Provider: "kubernetes"}}}
	if err = svc.PrepareManagedCleanupTargets(t.Context(), plan, reviewed); err != nil {
		t.Fatal(err)
	}
	if len(reviewed.Actions[0].CleanupTargets) != 1 {
		t.Fatalf("targets: %+v", reviewed.Actions[0].CleanupTargets)
	}
	return f, svc, plan, reviewed, scope
}

func saveCleanupReview(t *testing.T, f *poolFixture, plan *planning.Plan, result *planning.Result) {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at) VALUES($1,'org','proj','env','production','ready','cleanup-review','pool-manifest-proj',1,$2,true,'reviewer',now())`, plan.ID, raw); err != nil {
		t.Fatal(err)
	}
}

func TestManagedCleanupTargetsApproval(t *testing.T) {
	for _, op := range []planning.Op{planning.Delete, planning.Replace} {
		t.Run(string(op), func(t *testing.T) {
			f, _, plan, result, scope := cleanupTargetFixture(t)
			result.Actions[0].Op = op
			if op == planning.Replace {
				result.Actions[0].Provider = "server"
				result.Actions[0].Service = &planning.ServiceConfig{Provider: "server"}
			}
			saveCleanupReview(t, f, plan, result)
			if ok, err := f.plans.Decide(t.Context(), "org", plan.ID, "approved", "reviewer", "", time.Now()); err == nil || ok {
				t.Fatal("unauthenticated cleanup approval succeeded")
			}
			for _, principal := range []security.Principal{{OrgID: "foreign", UserID: "reviewer"}, {OrgID: "org", UserID: "reviewer", ReadOnly: true}} {
				if ok, err := f.plans.Decide(security.WithPrincipal(t.Context(), principal), "org", plan.ID, "approved", "reviewer", "", time.Now()); err == nil || ok {
					t.Fatal("invalid actor approved cleanup")
				}
			}
			ctx := security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "reviewer"})
			if ok, err := f.plans.Decide(ctx, "org", plan.ID, "approved", "reviewer", "", time.Now()); err != nil || !ok {
				t.Fatalf("approve cleanup: %v %v", ok, err)
			}
			var actor string
			var raw []byte
			if err := f.sql.QueryRow(t.Context(), `SELECT actor->>'UserID',body FROM loomplane_managed_cleanup_intents WHERE org_id='org' AND plan_id=$1`, plan.ID).Scan(&actor, &raw); err != nil {
				t.Fatal(err)
			}
			var targets []planning.ManagedCleanupTarget
			if err := json.Unmarshal(raw, &targets); err != nil {
				t.Fatal(err)
			}
			if actor != "reviewer" || targets[0].Scope != scope || targets[0].Accepted[0].Outcome.UID != "deployment-uid" {
				t.Fatalf("wrong approval actor or target: %s %+v", actor, targets)
			}
			for _, query := range []string{`UPDATE loomplane_managed_cleanup_intents SET actor='{}' WHERE org_id='org'`, `DELETE FROM loomplane_managed_cleanup_intents WHERE org_id='org'`, `UPDATE loomplane_plans SET result='{}' WHERE id='cleanup-new-plan'`} {
				if _, err := f.sql.Exec(t.Context(), query); err == nil {
					t.Fatal("immutable cleanup changed")
				}
			}
			var foreign int
			if err := f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_managed_cleanup_intents WHERE org_id='foreign'`).Scan(&foreign); err != nil || foreign != 0 {
				t.Fatalf("foreign intent visible: %d %v", foreign, err)
			}
		})
	}
}

func TestManagedCleanupTargetsStaleApproval(t *testing.T) {
	for _, change := range []string{"binding", "epoch", "outcome", "stripped", "foreign"} {
		t.Run(change, func(t *testing.T) {
			f, _, plan, result, _ := cleanupTargetFixture(t)
			if change == "stripped" {
				result.Actions[0].CleanupTargets = nil
			}
			if change == "foreign" {
				result.Actions[0].CleanupTargets[0].Scope.Org = "foreign"
			}
			saveCleanupReview(t, f, plan, result)
			queries := map[string]string{
				"binding": `UPDATE loomplane_bindings SET external_id='different-instance' WHERE id='cleanup-binding'`,
				"epoch":   `UPDATE loomplane_managed_workload_authority SET epoch=epoch+1,body=jsonb_set(body,'{Epoch}',to_jsonb(epoch+1)),revision=revision+1 WHERE org_id='org'`,
				"outcome": `UPDATE loomplane_managed_workload_issuances SET outcome=jsonb_set(outcome,'{UID}','"changed-uid"') WHERE org_id='org'`,
			}
			if query := queries[change]; query != "" {
				if _, err := f.sql.Exec(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			ctx := security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "reviewer"})
			if ok, err := f.plans.Decide(ctx, "org", plan.ID, "approved", "reviewer", "", time.Now()); err == nil || ok {
				t.Fatal("stale cleanup approval succeeded")
			}
		})
	}
}

func TestManagedCleanupTargetsUnknownWrite(t *testing.T) {
	f, svc, plan, result, scope := cleanupTargetFixture(t)
	store := kube.NewControllerStore(f.db).WithClusterCapacity(true)
	if _, err := store.Issue(t.Context(), scope, kube.ManagedRequest{Version: "v1", Resource: "services", Namespace: scope.Namespace, Name: scope.Instance, Method: "POST"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.PrepareManagedCleanupTargets(t.Context(), plan, result); err == nil {
		t.Fatal("unknown write did not block review")
	}
	if len(result.Actions[0].CleanupTargets[0].Unresolved) != 1 {
		t.Fatal("unknown write was not pinned")
	}
	saveCleanupReview(t, f, plan, result)
	ctx := security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "reviewer"})
	if ok, err := f.plans.Decide(ctx, "org", plan.ID, "approved", "reviewer", "", time.Now()); err == nil || ok {
		t.Fatal("unknown write approval succeeded")
	}
}

func TestManagedCleanupTargetsPreserveRevokedHistory(t *testing.T) {
	f, svc, plan, result, scope := cleanupTargetFixture(t)
	var before []byte
	if err := f.sql.QueryRow(t.Context(), `SELECT result FROM loomplane_plans WHERE id=$1`, scope.Plan).Scan(&before); err != nil {
		t.Fatal(err)
	}
	store := kube.NewControllerStore(f.db).WithClusterCapacity(true)
	if err := store.RevokeManagedScope(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if err := svc.PrepareManagedCleanupTargets(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	if result.Actions[0].CleanupTargets[0].RevokedAt == nil {
		t.Fatal("revocation missing from reviewed target")
	}
	saveCleanupReview(t, f, plan, result)
	ctx := security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "reviewer"})
	if ok, err := f.plans.Decide(ctx, "org", plan.ID, "approved", "reviewer", "", time.Now()); err != nil || !ok {
		t.Fatalf("approve cleanup: %v %v", ok, err)
	}
	authority, err := store.GetManagedAuthority(t.Context(), scope.Org, scope.Release)
	if err != nil || authority.RevokedAt == nil {
		t.Fatalf("application authority reopened: %+v %v", authority, err)
	}
	if err = store.OpenManagedScope(t.Context(), scope); err == nil {
		t.Fatal("cleanup approval reopened application authority")
	}
	var after []byte
	if err = f.sql.QueryRow(t.Context(), `SELECT result FROM loomplane_plans WHERE id=$1`, scope.Plan).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("cleanup approval changed old plan")
	}
}

func TestManagedCleanupRetainedTargets(t *testing.T) {
	f, svc, plan, result, scope := cleanupTargetFixture(t)
	old, err := f.plans.GetBinding(t.Context(), "org", "cleanup-binding")
	if err != nil {
		t.Fatal(err)
	}
	next := scope
	next.Instance = "new-instance"
	next.RuntimeRevision += "/next"
	next.Release = kube.WorkloadReleaseID(next.RuntimeRef, next.RuntimeRevision)
	store := kube.NewControllerStore(f.db).WithClusterCapacity(true)
	if err = store.OpenManagedScope(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	current := *old
	current.ExternalID = next.Instance
	current.Revision = "new-image"
	current.Replaced = []planning.Binding{*old}
	if err = f.plans.PutBinding(t.Context(), &current); err != nil {
		t.Fatal(err)
	}
	if err = svc.PrepareManagedCleanupTargets(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	targets := result.Actions[0].CleanupTargets
	if len(targets) != 2 || targets[0].Scope.Release != next.Release || targets[1].Scope.Release != scope.Release || targets[1].Origin != "retained" {
		t.Fatalf("retained targets: %+v", targets)
	}
	result.Actions[0].Op = planning.Update
	if err = svc.PrepareManagedCleanupTargets(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	if result.Actions[0].CleanupTargets[0].Kind != planning.ManagedCleanupRetireDependents || result.Actions[0].CleanupTargets[1].Kind != planning.ManagedCleanupRemoveRelease {
		t.Fatal("Update changed distinct retained resource purpose")
	}
	result.Actions[0].Op = planning.Delete
	if err = svc.PrepareManagedCleanupTargets(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	saveCleanupReview(t, f, plan, result)
	ctx := security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "reviewer"})
	if ok, e := f.plans.Decide(ctx, "org", plan.ID, "approved", "reviewer", "", time.Now()); e != nil || !ok {
		t.Fatalf("retained approval: %v %v", ok, e)
	}
	var targetCount int
	if e := f.sql.QueryRow(t.Context(), `SELECT jsonb_array_length(body) FROM loomplane_managed_cleanup_intents WHERE plan_id=$1`, plan.ID).Scan(&targetCount); e != nil || targetCount != 2 {
		t.Fatalf("retained intent: %d %v", targetCount, e)
	}
	plan.ID += "-stale"
	saveCleanupReview(t, f, plan, result)
	current.Replaced[0].Revision = "changed-old-image"
	if err = f.plans.SetReplaced(t.Context(), "org", current.ID, current.Replaced); err != nil {
		t.Fatal(err)
	}
	if ok, e := f.plans.Decide(ctx, "org", plan.ID, "approved", "reviewer", "", time.Now()); e == nil || ok {
		t.Fatal("stale retained target approved")
	}
	// A move back to the same instance still retains the old release's evidence.
	current.ExternalID = old.ExternalID
	current.Replaced = []planning.Binding{*old, *old}
	if err = f.plans.PutBinding(t.Context(), &current); err != nil {
		t.Fatal(err)
	}
	if err = svc.PrepareManagedCleanupTargets(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	targets = result.Actions[0].CleanupTargets
	if len(targets) != 1 || targets[0].Scope.Release != scope.Release || targets[0].Kind != planning.ManagedCleanupRemoveRelease || len(targets[0].BindingRefs) != 2 {
		t.Fatalf("repeated identity lost old release: %+v", targets)
	}
}

func TestManagedCleanupUpdateRetirement(t *testing.T) {
	f, svc, plan, result, scope := cleanupTargetFixture(t)
	result.Actions[0].Op = planning.Update
	if err := svc.PrepareManagedCleanupTargets(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	target := result.Actions[0].CleanupTargets[0]
	if target.Kind != "retire_release_dependents" || target.Scope != scope || target.Accepted[0].Outcome.UID != "deployment-uid" {
		t.Fatalf("update target: %+v", target)
	}
	saveCleanupReview(t, f, plan, result)
	ctx := security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "reviewer"})
	if ok, err := f.plans.Decide(ctx, "org", plan.ID, "approved", "reviewer", "", time.Now()); err != nil || !ok {
		t.Fatalf("approve retirement: %v %v", ok, err)
	}
	var kind string
	if err := f.sql.QueryRow(t.Context(), `SELECT body->0->>'kind' FROM loomplane_managed_cleanup_intents WHERE plan_id=$1`, plan.ID).Scan(&kind); err != nil || kind != "retire_release_dependents" {
		t.Fatalf("saved retirement: %s %v", kind, err)
	}
}

func cleanupSyncFixture(t *testing.T) (*poolFixture, *planning.Service, *cloudprofiles.Preview, *cloudprofiles.Snapshot, *cloudprofiles.Sync) {
	t.Helper()
	f, _, plan, result, scope := cleanupTargetFixture(t)
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET status='superseded' WHERE id='workload'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_deployments SET status='succeeded' WHERE id=$1`, scope.Deployment); err != nil {
		t.Fatal(err)
	}
	plan.Environment = "production"
	plan.ManifestID = "pool-manifest-proj"
	plan.ManifestRevision = 1
	plan.Executable = true
	plan.Actions = result.Actions
	plan.Connections = result.Connections
	var err error
	plan.Revision, err = planning.Digest(plan.Actions)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := f.clouds.SnapshotBindings(t.Context(), "org", "env")
	if err != nil {
		t.Fatal(err)
	}
	generation, err := f.clouds.AssignmentGeneration(t.Context(), "org", "proj", "env")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	envs := []cloudprofiles.EnvironmentChange{{EnvironmentID: "env", Environment: "production", Status: cloudprofiles.SyncPending}}
	preview := &cloudprofiles.Preview{ID: "cleanup-preview", OrgID: "org", ProjectID: "proj", Revision: "cleanup-digest", Input: cloudprofiles.ChangeInput{EnvironmentID: "env", Revision: generation}, Environments: envs, CreatedAt: now}
	snapshot := &cloudprofiles.Snapshot{Actor: security.Principal{OrgID: "org", UserID: "preview-author"}, Environments: []cloudprofiles.EnvironmentSnapshot{{EnvironmentID: "env", AssignmentRevision: generation, Bindings: bindings, Plan: raw}}, Connections: []string{scope.Connection}}
	if err = f.clouds.SavePreview(t.Context(), preview, snapshot); err != nil {
		t.Fatal(err)
	}
	actor := security.Principal{OrgID: "org", UserID: "reviewer"}
	sync := &cloudprofiles.Sync{ID: "cleanup-sync", OrgID: "org", ProjectID: "proj", PreviewID: preview.ID, Status: cloudprofiles.SyncPending, Actor: actor, CreatedBy: actor.UserID, Environments: append([]cloudprofiles.EnvironmentChange(nil), envs...), Attempt: 1, CreatedAt: now, UpdatedAt: now}
	snapshot.Actor = actor
	result.CloudSyncID = sync.ID
	acceptedRaw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Environments[0].AcceptedPlan = &cloudprofiles.AcceptedPlan{ID: sync.ID + ".env", Revision: plan.Revision, ManifestID: plan.ManifestID, ManifestRevision: 1, Result: acceptedRaw}
	sync.Environments[0].PlanID = sync.ID + ".env"
	service := planning.NewService(planning.Deps{Store: f.plans, CloudProfiles: f.clouds, Projects: projects.NewPgStore(f.db), Connections: &syncAccounts{store: connections.NewPgStore(f.db)}, Authz: &syncAuth{}})
	return f, service, preview, snapshot, sync
}

func TestManagedCleanupCloudSyncAcceptActor(t *testing.T) {
	f, _, preview, snapshot, sync := cleanupSyncFixture(t)
	if _, err := f.clouds.Accept(t.Context(), preview, snapshot, sync, "accept"); err == nil {
		t.Fatal("missing actor accepted sync")
	}
	for _, principal := range []security.Principal{{}, {OrgID: "foreign", UserID: "reviewer"}, {OrgID: "org", UserID: "different"}, {OrgID: "org", UserID: "reviewer", ReadOnly: true}} {
		if _, err := f.clouds.Accept(security.WithPrincipal(t.Context(), principal), preview, snapshot, sync, "accept"); err == nil {
			t.Fatal("untrusted actor accepted sync")
		}
	}
	ctx := security.WithPrincipal(t.Context(), sync.Actor)
	snapshot.Actor.UserID = "forged"
	if _, err := f.clouds.Accept(ctx, preview, snapshot, sync, "accept"); err == nil {
		t.Fatal("forged snapshot actor accepted")
	}
	snapshot.Actor = sync.Actor
	accepted, err := f.clouds.Accept(ctx, preview, snapshot, sync, "accept")
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.clouds.Accept(ctx, preview, snapshot, sync, "accept")
	if err != nil || again.ID != accepted.ID {
		t.Fatalf("replay: %+v %v", again, err)
	}
	var actor string
	var count int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*),min(actor->>'UserID') FROM loomplane_managed_cleanup_intents WHERE plan_id=$1`, sync.ID+".env").Scan(&count, &actor); err != nil || count != 1 || actor != "reviewer" {
		t.Fatalf("accepted actor: %d %s %v", count, actor, err)
	}
}

func TestManagedCleanupCloudSyncApply(t *testing.T) {
	f, svc, preview, _, _ := cleanupSyncFixture(t)
	actor := security.Principal{OrgID: "org", UserID: "reviewer"}
	accepted, err := svc.ApplyCloudChange(t.Context(), actor, "proj", preview.ID, preview.Revision, "apply")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := svc.ApplyCloudChange(t.Context(), actor, "proj", preview.ID, preview.Revision, "apply")
	if err != nil || replay.ID != accepted.ID {
		t.Fatalf("apply replay: %+v %v", replay, err)
	}
	var saved string
	if err = f.sql.QueryRow(t.Context(), `SELECT actor->>'UserID' FROM loomplane_managed_cleanup_intents WHERE plan_id=$1`, accepted.ID+".env").Scan(&saved); err != nil || saved != actor.UserID {
		t.Fatalf("apply actor: %s %v", saved, err)
	}
}

func TestManagedCleanupCloudSyncRollback(t *testing.T) {
	f, _, preview, snapshot, sync := cleanupSyncFixture(t)
	var generation int64
	if err := f.sql.QueryRow(t.Context(), `SELECT revision FROM loomplane_cloud_assignments WHERE environment_id='env'`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_managed_workload_authority SET revision=revision+1 WHERE org_id='org'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.clouds.Accept(security.WithPrincipal(t.Context(), sync.Actor), preview, snapshot, sync, "accept"); !errors.Is(err, cloudprofiles.ErrInvalid) {
		t.Fatalf("stale target acceptance: %v", err)
	}
	var current int64
	var effects int
	if err := f.sql.QueryRow(t.Context(), `SELECT revision FROM loomplane_cloud_assignments WHERE environment_id='env'`).Scan(&current); err != nil || current != generation {
		t.Fatalf("assignment did not roll back: %d %v", current, err)
	}
	if err := f.sql.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM loomplane_cloud_syncs)+(SELECT count(*) FROM loomplane_managed_cleanup_intents)+(SELECT count(*) FROM loomplane_environment_sync_guards)`).Scan(&effects); err != nil || effects != 0 {
		t.Fatalf("partial acceptance: %d %v", effects, err)
	}
}
