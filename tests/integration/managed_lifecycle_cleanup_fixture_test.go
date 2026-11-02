//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	apiclient "github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/manifest"
)

// lifecycleCleanupFixture records a failed local attempt, then uses real review routes.
func lifecycleCleanupFixture(t *testing.T, a *capacityAPI, attachmentID string) string {
	t.Helper()
	if attachmentID == "" {
		return ""
	}
	ctx := t.Context()
	attachment, err := a.store.GetRuntimeAttachment(ctx, a.org, attachmentID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.store.GetManagedResource(ctx, a.org, attachment.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := a.store.BeginClusterCapacityObservation(ctx, a.org, a.cloud, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := cloudinfra.ClusterCapacitySnapshot{ResourceID: r.ID, Provider: r.Provider, AccountID: r.AccountID, Ref: r.Observation.Ref, ObservationRevision: revision, CheckedAt: time.Now().UTC(), Complete: true, WorkloadsComplete: true}
	if err = a.store.SaveClusterCapacity(ctx, r, snapshot); err != nil {
		t.Fatal(err)
	}
	var owner, environment, target string
	if err = a.sql.QueryRow(ctx, `SELECT id FROM authsome_users WHERE email='clouds@example.test' AND deleted_at IS NULL`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err = a.sql.QueryRow(ctx, `SELECT id FROM loomplane_environments WHERE org_id=$1 AND project_id=$2 AND name='production'`, a.org, a.project).Scan(&environment); err != nil {
		t.Fatal(err)
	}
	if err = a.sql.QueryRow(ctx, `SELECT id FROM loomplane_cloud_targets WHERE org_id=$1 AND connection_id=$2`, a.org, attachment.RuntimeConnectionID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if _, err = a.sql.Exec(ctx, `INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES($1,$2,$3,$4,1) ON CONFLICT DO NOTHING`, a.org, a.project, environment, a.cloud); err != nil {
		t.Fatal(err)
	}
	infrastructure, err := forge.InjectType[*cloudinfra.Service](a.r.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	svc, err := forge.InjectType[*planning.Service](a.r.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	plan := &planning.Plan{ID: "fixture-cleanup-plan", OrgID: a.org, ProjectID: a.project, EnvironmentID: environment, Environment: "production", CreatedBy: owner}
	cfg := &planning.ServiceConfig{Provider: "kubernetes", Target: attachment.RuntimeConnectionID, TargetSettings: map[string]string{"managed_attachment_id": attachmentID, "managed_project_id": a.project, "managed_resource_id": r.ID, "architecture": "amd64"}, CapacityMode: "best_effort", Size: &manifest.Size{CPU: .5, Memory: 128}, Scale: &manifest.Scale{Min: 1, Max: 1}, CloudPlacement: &planning.CloudPlacement{CloudID: a.cloud, CloudRevision: 1, AssignmentRevision: 1, TargetID: target, TargetRevision: 1, GrantRevision: 1, ConnectionID: attachment.RuntimeConnectionID, Provider: "kubernetes"}}
	result := &planning.Result{Executable: true, Connections: map[string]string{"kubernetes": attachment.RuntimeConnectionID}, Actions: []planning.Action{{ID: "service:fixture", Subject: planning.ServiceSubject, Name: "fixture", Op: planning.Create, Provider: "kubernetes", Service: cfg}}}
	if err = svc.PrepareClusterWorkloadDemands(ctx, plan, result); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.sql.Exec(ctx, `INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('fixture-cleanup-manifest',$1,$2,1,'version: 1','fixture',$3,now())`, a.org, a.project, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = a.sql.Exec(ctx, `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at) VALUES($1,$2,$3,$4,'production','ready','fixture-cleanup-review','fixture-cleanup-manifest',1,$5,true,$6,now())`, plan.ID, a.org, a.project, environment, raw, owner); err != nil {
		t.Fatal(err)
	}
	pg := openPg(t, a.sql.Config().ConnString())
	principal := security.Principal{OrgID: a.org, UserID: owner}
	if ok, checkErr := planning.NewPgStore(pg).Decide(security.WithPrincipal(ctx, principal), a.org, plan.ID, "approved", owner, "", time.Now()); checkErr != nil || !ok {
		t.Fatalf("fixture approval: %v", checkErr)
	}
	const deployment = "fixture-cleanup-deployment"
	if _, err = a.sql.Exec(ctx, `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES($1,$2,$3,$4,'production',$5,'fixture','running',$6,now())`, deployment, a.org, a.project, environment, plan.ID, owner); err != nil {
		t.Fatal(err)
	}
	attempt, err := a.store.BeginWorkloadAttempt(ctx, cloudinfra.WorkloadAttempt{Org: a.org, Project: a.project, Environment: environment, Service: "fixture", Plan: plan.ID, Deployment: deployment}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	scope := kube.ManagedScope{Org: a.org, Project: a.project, Environment: environment, Service: "fixture", Plan: plan.ID, Deployment: deployment, Action: "service:fixture", Connection: attachment.RuntimeConnectionID, Instance: "fixture-failed", RuntimeRef: a.org + "/" + environment + "/fixture/runtime_fixture", RuntimeRevision: deployment + "/service:fixture", Namespace: attachment.Namespace, NamespaceUID: attachment.NamespaceUID, ClusterCA: attachment.TrustFingerprint, Epoch: attempt.Generation}
	scope.Release = kube.WorkloadReleaseID(scope.RuntimeRef, scope.RuntimeRevision)
	control := kube.NewControllerStore(pg).WithClusterCapacity(true).WithManagedDemandGuard(managedFixtureDemandGuard(pg, infrastructure.Authz))
	if err = control.OpenManagedScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	accepted, issueErr := control.Issue(ctx, scope, kube.ManagedRequest{Version: "v1", Resource: "services", Namespace: scope.Namespace, Name: scope.Instance, Method: "POST"})
	if issueErr != nil {
		t.Fatal(issueErr)
	}
	if err = control.Resolve(ctx, accepted, scope, kube.ManagedOutcome{State: "accepted", UID: "fixture-service-uid", ResourceVersion: "1", StatusCode: 201}); err != nil {
		t.Fatal(err)
	}
	// An unresolved request is deliberately left visible; it must block cleanup.
	unresolved, err := control.Issue(ctx, scope, kube.ManagedRequest{Group: "apps", Version: "v1", Resource: "deployments", Namespace: scope.Namespace, Name: scope.Instance, Method: "POST"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.sql.Exec(ctx, `UPDATE loomplane_deployments SET status='failed',finished_at=now() WHERE id=$1`, deployment); err != nil {
		t.Fatal(err)
	}
	var review apiclient.CleanupReview
	a.c.must(http.StatusOK, http.MethodPost, "/api/v1/deployments/"+deployment+"/cleanup/preview", nil, &review)
	if review.Variant != "managed_workload" || len(review.Targets) != 1 || review.Targets[0].UnresolvedWrites != 1 {
		t.Fatalf("missing unresolved review: %+v", review)
	}
	a.c.must(http.StatusConflict, http.MethodPost, "/api/v1/deployments/"+deployment+"/cleanup/apply", map[string]string{"revision": review.Revision}, nil)
	if os.Getenv("LOOMPLANE_CLEANUP_KEEP_UNRESOLVED") == "1" {
		return deployment
	}
	if err = control.Resolve(ctx, unresolved, scope, kube.ManagedOutcome{State: "rejected", StatusCode: 404}); err != nil {
		t.Fatal(err)
	}
	a.c.must(http.StatusOK, http.MethodPost, "/api/v1/deployments/"+deployment+"/cleanup/preview", nil, &review)
	var acceptedReview apiclient.FailedCleanup
	a.c.must(http.StatusAccepted, http.MethodPost, "/api/v1/deployments/"+deployment+"/cleanup/apply", map[string]string{"revision": review.Revision}, &acceptedReview)
	a.c.must(http.StatusConflict, http.MethodPost, "/api/v1/deployments/"+deployment+"/cleanup/cancel", map[string]string{"expected_review_id": "outdated-review"}, nil)
	a.c.must(http.StatusOK, http.MethodPost, "/api/v1/deployments/"+deployment+"/cleanup/cancel", map[string]string{"expected_review_id": acceptedReview.ReviewID}, nil)
	a.c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+deployment+"/cleanup", nil, &acceptedReview)
	if acceptedReview.Status != "cancelled" {
		t.Fatalf("cleanup cancellation: %+v", acceptedReview)
	}
	a.c.must(http.StatusOK, http.MethodPost, "/api/v1/deployments/"+deployment+"/cleanup/preview", nil, &review)
	if review.PreviousReviewID != acceptedReview.ReviewID {
		t.Fatal("new cleanup review lost predecessor")
	}
	return deployment
}
