//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xraph/dispatch/durable"
	"github.com/xraph/keysmith"
	keyid "github.com/xraph/keysmith/id"
	"github.com/xraph/keysmith/key"
	keymemory "github.com/xraph/keysmith/store/memory"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

type managedFailureDelivery struct {
	runs  *orchestration.PgRunStore
	calls int
}

func (d *managedFailureDelivery) StartFailedCleanup(context.Context, string, string, int) error {
	return errors.New("SQL workflow selected for managed review")
}
func (d *managedFailureDelivery) StartManagedFailedCleanup(ctx context.Context, org, deployment, review string, attempt int) error {
	raw, err := json.Marshal(map[string]any{"org_id": org, "deployment_id": deployment, "review_id": review, "attempt": attempt})
	if err != nil {
		return err
	}
	key := fmt.Sprintf("managed-failed-cleanup:%s:%d", review, attempt)
	_, err = d.runs.Save(ctx, orchestration.DeploymentRun{OrgID: org, PlanID: "managed-failed-cleanup:" + review, Request: durable.StartRequest{Key: durable.Key{Namespace: orchestration.DurableNamespace, WorkflowID: key, RunID: "run"}, RequestID: key, WorkflowType: "loomplane.managed-failed-cleanup.v1", BuildID: orchestration.ManagedCleanupBuild, Queue: orchestration.DurableQueue, Input: raw}})
	if err == nil {
		d.calls++
	}
	return err
}

type managedFailureFixture struct {
	*cleanupIssuerFixture
	service   *deployments.FailedCleanups
	delivery  *managedFailureDelivery
	principal security.Principal
}

func newManagedFailureFixture(t *testing.T) *managedFailureFixture {
	t.Helper()
	f := newCleanupIssuerFixture(t, false)
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_deployments SET status='succeeded',finished_at=now() WHERE id='cleanup-deployment'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sql.Exec(t.Context(), `DELETE FROM loomplane_bindings WHERE id='cleanup-binding'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_deployments SET status='failed',finished_at=now() WHERE id='cleanup-old-deployment'`); err != nil {
		t.Fatal(err)
	}
	ds := deployments.NewPgStore(f.db)
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: ds, Plans: f.plans, Authz: f.auth})
	delivery := &managedFailureDelivery{runs: orchestration.NewPgRunStore(f.db)}
	service := deployments.NewFailedCleanups(runner, ds, delivery).WithManagedCleanup(f.store, nil)
	p := security.Principal{OrgID: "org", UserID: "reviewer"}
	return &managedFailureFixture{cleanupIssuerFixture: f, service: service, delivery: delivery, principal: p}
}
func (f *managedFailureFixture) accept(t *testing.T) *deployments.FailedCleanup {
	t.Helper()
	review, err := f.service.Preview(f.ctx, f.principal, f.scope.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	if review.Variant != "managed_workload" || len(review.Targets) != 1 || review.Targets[0].Scope != f.scope {
		t.Fatalf("unsafe review: %+v", review)
	}
	accepted, err := f.service.Apply(f.ctx, f.principal, f.scope.Deployment, review.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}
func (f *managedFailureFixture) selectGrant(r *deployments.FailedCleanup) planning.ManagedCleanupSelection {
	return planning.ManagedCleanupSelection{Org: "org", Plan: f.scope.Plan, Action: f.scope.Action, Deployment: f.scope.Deployment, FailedReviewID: r.ReviewID, ExecutionAttempt: r.Attempt}
}
func (f *managedFailureFixture) failUnavailable(t *testing.T, r *deployments.FailedCleanup) {
	t.Helper()
	err := f.service.RunManagedFailedCleanup(orchestration.ManagedCleanupContext(t.Context()), "org", f.scope.Deployment, r.ReviewID, r.Attempt, func([]byte) error { return nil })
	if !errors.Is(err, planning.ErrManagedFailedCleanupUnavailable) {
		t.Fatalf("unconfigured engine: %v", err)
	}
	current, err := f.service.Get(f.ctx, f.principal, f.scope.Deployment)
	if err != nil || current.Status != "failed" {
		t.Fatalf("unavailable was success: %+v %v", current, err)
	}
}

func TestManagedFailedCleanupReviewAndRecovery(t *testing.T) {
	f := newManagedFailureFixture(t)
	accepted := f.accept(t)
	if _, err := f.service.Apply(t.Context(), f.principal, f.scope.Deployment, accepted.Revision); err == nil {
		t.Fatal("unauthenticated acceptance replay")
	}
	same, err := f.service.Apply(f.ctx, f.principal, f.scope.Deployment, accepted.Revision)
	if err != nil || same.ReviewID != accepted.ReviewID {
		t.Fatalf("accept replay: %+v %v", same, err)
	}
	before := f.delivery.calls
	if err = f.service.RecoverFailedCleanups(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.delivery.calls != before {
		t.Fatal("delivered attempt requeued")
	}
	if err = f.service.RunFailedCleanup(t.Context(), "org", f.scope.Deployment, accepted.Attempt, func([]byte) error { return nil }); !errors.Is(err, deployments.ErrManagedCleanupProtocol) {
		t.Fatalf("SQL/direct run accepted managed review: %v", err)
	}
	if err = f.service.RunManagedFailedCleanup(orchestration.ManagedWorkloadContext(t.Context()), "org", f.scope.Deployment, accepted.ReviewID, accepted.Attempt, func([]byte) error { return nil }); !errors.Is(err, deployments.ErrManagedCleanupProtocol) {
		t.Fatalf("v5 run accepted cleanup: %v", err)
	}
	f.failUnavailable(t, accepted)
	retry, err := f.service.Retry(f.ctx, f.principal, f.scope.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	if retry.ReviewID != accepted.ReviewID || retry.Attempt != accepted.Attempt+1 {
		t.Fatal("retry replaced review")
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_managed_failed_cleanups SET delivered_attempt=0,revision=revision+1 WHERE org_id='org' AND id=$1`, retry.ReviewID); err != nil {
		t.Fatal(err)
	}
	if err = f.service.RecoverFailedCleanups(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.delivery.calls != before+2 {
		t.Fatal("accept to delivery recovery lost attempt")
	}
}

func TestManagedFailedCleanupGrantVersions(t *testing.T) {
	f := newManagedFailureFixture(t)
	accepted := f.accept(t)
	grant, err := f.store.Open(f.ctx, f.selectGrant(accepted))
	if err != nil {
		t.Fatal(err)
	}
	journal := f.store.Journal("org", grant.ID)
	if err = kube.NewControllerStore(f.db).WithClusterCapacity(true).OpenManagedScope(t.Context(), f.scope); err == nil {
		t.Fatal("abandoned application authority reopened")
	}
	issuance, err := journal.Issue(f.ctx, f.scope, f.request)
	if err != nil {
		t.Fatal(err)
	}
	f.failUnavailable(t, accepted)
	retry, err := f.service.Retry(f.ctx, f.principal, f.scope.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.Issue(f.ctx, f.scope, f.request); err == nil {
		t.Fatal("old attempt issued after retry")
	}
	next, err := f.store.Open(f.ctx, f.selectGrant(retry))
	if err != nil {
		t.Fatal(err)
	}
	nextJournal := f.store.Journal("org", next.ID)
	nextID, err := nextJournal.Issue(f.ctx, f.scope, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.Revoke(f.ctx, "org", next.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Retry(f.ctx, f.principal, f.scope.Deployment); err == nil {
		t.Fatal("revoked review retried")
	}
	preview, err := f.service.Preview(f.ctx, f.principal, f.scope.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Apply(f.ctx, f.principal, f.scope.Deployment, preview.Revision); err == nil {
		t.Fatal("new review bypassed unknown writes")
	}
	outcome := kube.ManagedOutcome{State: "accepted", UID: f.request.UID, StatusCode: 200}
	if err = journal.Resolve(t.Context(), issuance, f.scope, outcome); err != nil {
		t.Fatal(err)
	}
	if err = nextJournal.Resolve(t.Context(), nextID, f.scope, outcome); err != nil {
		t.Fatal(err)
	}
	fresh := f.accept(t)
	if fresh.ReviewID == accepted.ReviewID || fresh.ReviewSequence != 2 {
		t.Fatal("fresh review mutated old identity")
	}
	if _, err = f.service.Apply(f.ctx, f.principal, f.scope.Deployment, accepted.Revision); err == nil {
		t.Fatal("stale acceptance replaced fresh review")
	}
	if _, err = f.store.Open(f.ctx, f.selectGrant(accepted)); err == nil {
		t.Fatal("old selection reopened after replacement")
	}
}

func TestManagedFailedCleanupAuthorizationAndStaleness(t *testing.T) {
	for _, scenario := range []string{"wrong actor", "revoked member", "key", "stale target", "serving binding", "unknown write", "tenant"} {
		t.Run(scenario, func(t *testing.T) {
			f := newManagedFailureFixture(t)
			preview, err := f.service.Preview(f.ctx, f.principal, f.scope.Deployment)
			if err != nil {
				t.Fatal(err)
			}
			p, ctx := f.principal, f.ctx
			switch scenario {
			case "wrong actor":
				p.UserID = "historical-application-actor"
				ctx = security.WithPrincipal(t.Context(), p)
			case "revoked member":
				f.member.Store(false)
			case "key":
				p.KeyID = "revoked-key"
				ctx = security.WithPrincipal(t.Context(), p)
			case "tenant":
				p.OrgID = "other"
				ctx = security.WithPrincipal(t.Context(), p)
			case "stale target":
				_, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET generation=generation+1 WHERE id='cluster-runtime'`)
			case "serving binding":
				err = f.plans.PutBinding(t.Context(), &planning.Binding{ID: "serving", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "api", Provider: "kubernetes", ExternalID: f.scope.Instance, PlanID: f.scope.Plan, ConnectionID: f.scope.Connection, Revision: "serving", CreatedAt: time.Now(), UpdatedAt: time.Now()})
			case "unknown write":
				_, err = f.sql.Exec(t.Context(), `UPDATE loomplane_deployments SET status='running' WHERE id=$1`, f.scope.Deployment)
				if err == nil {
					_, err = kube.NewControllerStore(f.db).WithClusterCapacity(true).Issue(t.Context(), f.scope, kube.ManagedRequest{Version: "v1", Resource: "services", Namespace: f.scope.Namespace, Name: f.scope.Instance, Method: "PUT"})
				}
				if err == nil {
					_, err = f.sql.Exec(t.Context(), `UPDATE loomplane_deployments SET status='failed' WHERE id=$1`, f.scope.Deployment)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.Apply(ctx, p, f.scope.Deployment, preview.Revision); err == nil {
				t.Fatal("unsafe approval accepted")
			}
		})
	}
}

func TestManagedFailedCleanupDatabaseFences(t *testing.T) {
	f := newManagedFailureFixture(t)
	accepted := f.accept(t)
	grant, err := f.store.Open(f.ctx, f.selectGrant(accepted))
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE loomplane_managed_failed_cleanups SET required_protocol='',revision=revision+1 WHERE id=$1`,
		`UPDATE loomplane_managed_failed_cleanups SET targets='[]',revision=revision+1 WHERE id=$1`,
		`UPDATE loomplane_managed_failed_cleanups SET actor='{}',revision=revision+1 WHERE id=$1`,
		`UPDATE loomplane_managed_failed_cleanups SET attempt=attempt+1,revision=revision+1 WHERE id=$1`,
		`DELETE FROM loomplane_managed_failed_cleanups WHERE id=$1`,
		`UPDATE loomplane_deployment_workflow_runs SET request=jsonb_set(request,'{build_id}','"deployment-v5"') WHERE plan_id='managed-failed-cleanup:'||$1`,
		`UPDATE loomplane_deployment_workflow_runs SET plan_id='stripped' WHERE plan_id='managed-failed-cleanup:'||$1`,
	} {
		if _, err = f.sql.Exec(t.Context(), query, accepted.ReviewID); err == nil {
			t.Fatal("accepted review/route tampering", query)
		}
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_managed_cleanup_grants SET failed_review_id=NULL,execution_attempt=NULL,revision=revision+1 WHERE id=$1`, grant.ID); err == nil {
		t.Fatal("changed grant source")
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_deployments SET status='running' WHERE id=$1`, f.scope.Deployment); err == nil {
		t.Fatal("reopened abandoned application")
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployment_workflow_runs(org_id,plan_id,workflow_id,request) VALUES('org','failed-cleanup:'||$1,'old-cleanup','{"build_id":"deployment-v2"}')`, f.scope.Deployment); err == nil {
		t.Fatal("managed cleanup used historical SQL route")
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployment_workflow_runs(org_id,plan_id,workflow_id,request) VALUES('org','unrelated','unrelated','{"build_id":"deployment-v6"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployment_workflow_participants(org_id,plan_id,workflow_id) VALUES('org','managed-failed-cleanup:'||$1,'unrelated')`, accepted.ReviewID); err == nil {
		t.Fatal("managed review attached to unrelated workflow")
	}
	if _, err = f.service.Cancel(f.ctx, f.principal, f.scope.Deployment, accepted.ReviewID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.Journal("org", grant.ID).Issue(f.ctx, f.scope, f.request); err == nil {
		t.Fatal("cancelled review still issued")
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_managed_failed_cleanups SET status='pending',revision=revision+1 WHERE id=$1`, accepted.ReviewID); err == nil {
		t.Fatal("revoked review became pending")
	}
}

func TestManagedFailedCleanupCurrentAttemptReplacement(t *testing.T) {
	f := newManagedFailureFixture(t)
	old := cloudinfra.WorkloadAttempt{Org: f.scope.Org, Project: f.scope.Project, Environment: f.scope.Environment, Service: f.scope.Service, Plan: f.scope.Plan, Deployment: f.scope.Deployment, Generation: f.scope.Epoch}
	if err := f.poolFixture.store.FinishWorkloadAttempt(t.Context(), old, true); err != nil {
		t.Fatal(err)
	}
	next := old
	next.Plan = "cleanup-new-plan"
	next.Deployment = "cleanup-deployment"
	attempt, err := f.poolFixture.store.BeginWorkloadAttempt(t.Context(), next, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.service.Preview(f.ctx, f.principal, f.scope.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Targets) != 1 || preview.Targets[0].Scope.Deployment != f.scope.Deployment {
		t.Fatal("mutable attempt erased original target")
	}
	if _, err = f.service.Apply(f.ctx, f.principal, f.scope.Deployment, preview.Revision); err == nil {
		t.Fatal("competing live attempt accepted")
	}
	if err = f.poolFixture.store.FinishWorkloadAttempt(t.Context(), *attempt, true); err != nil {
		t.Fatal(err)
	}
	accepted := f.accept(t)
	if _, err = f.store.Open(f.ctx, f.selectGrant(accepted)); err != nil {
		t.Fatal(err)
	}
}

func TestManagedFailedCleanupCancellation(t *testing.T) {
	for _, issued := range []bool{false, true} {
		t.Run(fmt.Sprint(issued), func(t *testing.T) {
			f := newManagedFailureFixture(t)
			accepted := f.accept(t)
			var journal kube.ManagedJournal
			var issuance string
			if issued {
				grant, err := f.store.Open(f.ctx, f.selectGrant(accepted))
				if err != nil {
					t.Fatal(err)
				}
				journal = f.store.Journal("org", grant.ID)
				issuance, err = journal.Issue(f.ctx, f.scope, f.request)
				if err != nil {
					t.Fatal(err)
				}
			}
			f.member.Store(false)
			if _, err := f.service.Cancel(f.ctx, f.principal, f.scope.Deployment, accepted.ReviewID); err == nil {
				t.Fatal("revoked member cancelled review")
			}
			f.member.Store(true)
			cancelled, err := f.service.Cancel(f.ctx, f.principal, f.scope.Deployment, accepted.ReviewID)
			if err != nil || cancelled.Status != "cancelled" {
				t.Fatalf("cancel: %+v %v", cancelled, err)
			}
			if err = f.delivery.StartManagedFailedCleanup(t.Context(), "org", f.scope.Deployment, accepted.ReviewID, accepted.Attempt); err == nil {
				t.Fatal("cancelled review rebound durable route")
			}
			var actor, cancelActor string
			if err = f.sql.QueryRow(t.Context(), `SELECT actor->>'UserID',cancelled_by->>'UserID' FROM loomplane_managed_failed_cleanups WHERE id=$1`, accepted.ReviewID).Scan(&actor, &cancelActor); err != nil || actor != "reviewer" || cancelActor != "reviewer" {
				t.Fatalf("cancel audit: %s %s %v", actor, cancelActor, err)
			}
			if err = f.service.RunManagedFailedCleanup(orchestration.ManagedCleanupContext(t.Context()), "org", f.scope.Deployment, accepted.ReviewID, accepted.Attempt, func([]byte) error { return nil }); err == nil {
				t.Fatal("cancelled queued task ran")
			}
			if issued {
				if _, err = journal.Issue(f.ctx, f.scope, f.request); err == nil {
					t.Fatal("cancelled running adapter issued")
				}
				preview, previewErr := f.service.Preview(f.ctx, f.principal, f.scope.Deployment)
				if previewErr != nil {
					t.Fatal(previewErr)
				}
				if len(preview.Blockers) == 0 {
					t.Fatal("unknown write blocker missing")
				}
				if _, err = f.service.Apply(f.ctx, f.principal, f.scope.Deployment, preview.Revision); err == nil {
					t.Fatal("cancel drained unknown request")
				}
				if err = journal.Resolve(t.Context(), issuance, f.scope, kube.ManagedOutcome{State: "accepted", UID: f.request.UID, StatusCode: 200}); err != nil {
					t.Fatal(err)
				}
			}
			fresh := f.accept(t)
			if _, err = f.service.Cancel(f.ctx, f.principal, f.scope.Deployment, accepted.ReviewID); err == nil {
				t.Fatal("stale cancellation cancelled new review")
			}
			current, err := f.service.Get(f.ctx, f.principal, f.scope.Deployment)
			if err != nil || current.ReviewID != fresh.ReviewID || current.Status != "pending" {
				t.Fatalf("fresh review changed: %+v %v", current, err)
			}
		})
	}
}

func TestManagedFailedCleanupRevokedKey(t *testing.T) {
	f := newManagedFailureFixture(t)
	keys := keymemory.New()
	engine, err := keysmith.NewEngine(keysmith.WithStore(keys))
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	k := &key.Key{ID: keyid.NewKeyID(), AppID: "app", TenantID: "org", CreatedBy: "reviewer", State: key.StateActive, ExpiresAt: &expires, Metadata: map[string]any{"access": "write"}}
	if err = keys.Keys().Create(t.Context(), k); err != nil {
		t.Fatal(err)
	}
	f.store = planning.NewManagedCleanupStore(f.db, f.auth, apikeys.New(engine, "app", nil, nil))
	f.service.WithManagedCleanup(f.store, nil)
	f.principal.KeyID = k.ID.String()
	f.ctx = security.WithPrincipal(t.Context(), f.principal)
	accepted := f.accept(t)
	grant, err := f.store.Open(f.ctx, f.selectGrant(accepted))
	if err != nil {
		t.Fatal(err)
	}
	k.State = key.StateRevoked
	if err = keys.Keys().Update(t.Context(), k); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.Journal("org", grant.ID).Issue(f.ctx, f.scope, f.request); err == nil {
		t.Fatal("revoked accepted key issued")
	}
	if err = f.service.RunManagedFailedCleanup(orchestration.ManagedCleanupContext(t.Context()), "org", f.scope.Deployment, accepted.ReviewID, accepted.Attempt, func([]byte) error { return nil }); err == nil {
		t.Fatal("revoked key executed")
	}
	if _, err = f.service.Retry(f.ctx, f.principal, f.scope.Deployment); err == nil {
		t.Fatal("revoked key retried")
	}
	f.principal.KeyID = ""
	f.ctx = security.WithPrincipal(t.Context(), f.principal)
	fresh := f.accept(t)
	if fresh.ReviewID == accepted.ReviewID {
		t.Fatal("session approval reused revoked key authority")
	}
}

func TestManagedFailedCleanupSharedReleaseBlocked(t *testing.T) {
	var failed kube.ManagedScope
	f := prepareCleanupIssuerFixture(t, false, func(pool *poolFixture, svc *planning.Service, _ *planning.Plan, _ *planning.Result, old kube.ManagedScope) {
		failed = unpublishedCleanupRelease(t, pool, svc, old)
	})
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_deployments SET status='succeeded',finished_at=now() WHERE id='cleanup-deployment'`); err != nil {
		t.Fatal(err)
	}
	ds := deployments.NewPgStore(f.db)
	service := deployments.NewFailedCleanups(deployments.NewRunner(deployments.RunnerDeps{Store: ds, Plans: f.plans, Authz: f.auth}), ds, &managedFailureDelivery{runs: orchestration.NewPgRunStore(f.db)}).WithManagedCleanup(f.store, nil)
	p := security.Principal{OrgID: "org", UserID: "reviewer"}
	for _, removeBinding := range []bool{false, true} {
		if removeBinding {
			if _, err := f.sql.Exec(t.Context(), `DELETE FROM loomplane_bindings WHERE id='cleanup-binding'`); err != nil {
				t.Fatal(err)
			}
		}
		preview, err := service.Preview(f.ctx, p, failed.Deployment)
		if err != nil {
			t.Fatal(err)
		}
		if len(preview.Targets) != 1 || preview.Targets[0].Scope != failed || len(preview.Blockers) == 0 {
			t.Fatalf("shared failure review: %+v", preview)
		}
		if _, err = service.Apply(f.ctx, p, failed.Deployment, preview.Revision); err == nil {
			t.Fatal("failed version borrowed normal whole-controller coauthorization")
		}
	}
}
