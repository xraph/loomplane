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
	drt "github.com/xraph/dispatch/durable/runtime"
	dispatchpg "github.com/xraph/dispatch/store/postgres"
	"github.com/xraph/grove"
	"github.com/xraph/grove/migrate"
	"github.com/xraph/keysmith"
	keyid "github.com/xraph/keysmith/id"
	"github.com/xraph/keysmith/key"
	kspg "github.com/xraph/keysmith/store/postgres"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
)

func restartedCapacitySyncService(f *forwardFixture, verifier security.PrincipalVerifier) *planning.Service {
	s := planning.NewService(planning.Deps{Store: f.plans, Projects: projects.NewPgStore(f.db), CloudProfiles: f.clouds, Connections: &syncAccounts{store: connections.NewPgStore(f.db)}, Providers: providers.Registry(), Authz: f.auth, CapacityPrincipal: verifier})
	r := f.runner()
	s.BindCloudSyncRunner(r, r)
	return s
}

func completeCapacitySyncAttempt(t *testing.T, f *forwardFixture, s *planning.Service, sync *cloudprofiles.Sync) {
	t.Helper()
	if err := ops.Migrate(t.Context(), f.sql.Config().ConnString(), []*migrate.Group{dispatchpg.Migrations}); err != nil {
		t.Fatal(err)
	}
	db, err := grove.Open(f.db)
	if err != nil {
		t.Fatal(err)
	}
	st := dispatchpg.New(db)
	workflows := orchestration.NewDeployments()
	workflows.BindCloudSync(s)
	workflows.BindRunStore(orchestration.NewPgRunStore(f.db))
	options := workflows.CapacityOptions("review-sync", 1)
	worker, err := drt.NewWorker(st, options)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"org_id": "org", "sync_id": sync.ID, "reconcile_interval": 30 * time.Second})
	request := durable.StartRequest{Key: durable.Key{Namespace: orchestration.DurableNamespace, WorkflowID: fmt.Sprintf("%s.attempt.%d", sync.ID, sync.Attempt), RunID: "1"}, RequestID: fmt.Sprintf("sync:%s:%d", sync.ID, sync.Attempt), WorkflowType: "loomplane.cloud.sync.v1", BuildID: orchestration.CapacityBuild, Queue: orchestration.DurableQueue, Input: raw}
	if _, err = orchestration.NewPgRunStore(f.db).Save(t.Context(), orchestration.DeploymentRun{OrgID: "org", PlanID: sync.ID, Request: request}); err != nil {
		t.Fatal(err)
	}
	if _, err = worker.StartExecution(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if worked, e := worker.RunOnce(t.Context(), durable.TaskWorkflow); e != nil || !worked {
		t.Fatalf("schedule prepare: %v %v", worked, e)
	}
	worker, err = drt.NewWorker(st, options)
	if err != nil {
		t.Fatal(err)
	}
	pumpWorkflow(t, worker)
	execution, err := st.GetExecution(t.Context(), request.Key)
	if err != nil || execution.State != durable.StateCompleted || execution.NextRunID != "" {
		t.Fatalf("business failure did not settle durable attempt: %+v %v", execution, err)
	}
	if _, err = worker.StartExecution(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	pumpWorkflow(t, worker)
}

func TestCapacitySyncPrepareFailureRecovery(t *testing.T) {
	for _, failure := range []string{"revoked-key", "immutable-pin", "removed-grant"} {
		t.Run(failure, func(t *testing.T) {
			f := newForwardFixture(t)
			if err := ops.Migrate(t.Context(), f.sql.Config().ConnString(), []*migrate.Group{kspg.Migrations}); err != nil {
				t.Fatal(err)
			}
			keys := kspg.New(f.db)
			ke, err := keysmith.NewEngine(keysmith.WithStore(keys))
			if err != nil {
				t.Fatal(err)
			}
			expires := time.Now().Add(time.Hour)
			k := &key.Key{ID: keyid.NewKeyID(), AppID: "app", TenantID: "org", CreatedBy: "usr", State: key.StateActive, ExpiresAt: &expires, Metadata: map[string]any{"access": "write"}}
			if err = keys.Keys().Create(t.Context(), k); err != nil {
				t.Fatal(err)
			}
			svc := restartedCapacitySyncService(f, apikeys.New(ke, "app", nil, nil))
			actor := security.Principal{OrgID: "org", UserID: "usr", KeyID: k.ID.String()}
			preview := reviewPoolChange(t, svc)
			accepted, err := svc.ApplyCloudChange(t.Context(), actor, "", preview.ID, preview.Revision, "review-failure")
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "revoked-key":
				k.State = key.StateRevoked
				if err = keys.Keys().Update(t.Context(), k); err != nil {
					t.Fatal(err)
				}
			case "immutable-pin":
				if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET account='changed',details=jsonb_set(details,'{docker_id}','"changed"') WHERE id=$1`, "runtime-2"); err != nil {
					t.Fatal(err)
				}
			case "removed-grant":
				if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_project_grants SET revoked=true,revision=revision+1 WHERE cloud_id='cloud' AND project_id='proj'`); err != nil {
					t.Fatal(err)
				}
			}
			restarted, err := keysmith.NewEngine(keysmith.WithStore(kspg.New(f.db)))
			if err != nil {
				t.Fatal(err)
			}
			svc = restartedCapacitySyncService(f, apikeys.New(restarted, "app", nil, nil))
			completeCapacitySyncAttempt(t, f, svc, accepted)
			failed, _, err := f.clouds.GetSync(t.Context(), "org", "", accepted.ID)
			if err != nil || failed.Status != cloudprofiles.SyncFailed || failed.Error == "" || len(f.executor.calls) != 0 {
				t.Fatalf("recoverable failure: %+v calls=%v %v", failed, f.executor.calls, err)
			}
			replacement := security.Principal{OrgID: "org", UserID: "replacement-manager"}
			if failure == "revoked-key" {
				retried, e := svc.RetryCloudSync(t.Context(), replacement, "", accepted.ID)
				if e != nil || retried.Attempt != accepted.Attempt+1 {
					t.Fatalf("authorized retry: %+v %v", retried, e)
				}
				participants, e := svc.PrepareCapacityCloudSync(orchestration.CapacityContext(t.Context()), "org", "", accepted.ID)
				if e != nil || len(participants.Deployments) != 2 {
					t.Fatalf("replacement prepare: %+v %v", participants, e)
				}
				stale := orchestration.CloudSyncAttemptContext(t.Context(), accepted.Attempt)
				if e = svc.FinishCapacityCloudSync(stale, "org", "", accepted.ID, nil); e != nil {
					t.Fatal(e)
				}
				stalePrepare, e := svc.PrepareCapacityCloudSync(stale, "org", "", accepted.ID)
				if e != nil || !stalePrepare.Stopped {
					t.Fatalf("old prepare was not fenced: %+v %v", stalePrepare, e)
				}
				current, _, e := f.clouds.GetSync(t.Context(), "org", "", accepted.ID)
				if e != nil || current.Status != cloudprofiles.SyncRunning || current.Attempt != retried.Attempt {
					t.Fatalf("old finish overwrote retry: %+v %v", current, e)
				}
			} else {
				if failure == "removed-grant" {
					if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_project_grants SET revoked=false,revision=revision+1 WHERE cloud_id='cloud' AND project_id='proj'`); err != nil {
						t.Fatal(err)
					}
				}
				revert, e := svc.PreviewCloudRevert(t.Context(), replacement, "", accepted.ID)
				if e != nil || len(revert.Blockers) > 0 {
					t.Fatalf("authorized revert review: %+v %v", revert, e)
				}
				compensation, e := svc.ApplyCloudRevert(t.Context(), replacement, "", revert.ID, revert.Revision, "review-compensate")
				if e != nil || compensation.Reverts != accepted.ID {
					t.Fatalf("authorized revert acceptance: %+v %v", compensation, e)
				}
			}
		})
	}
}

func TestCapacitySyncFinishRedeliveryPreservesRevert(t *testing.T) {
	f := newForwardFixture(t)
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	preview := reviewPoolChange(t, f.service)
	original, err := f.service.ApplyCloudChange(t.Context(), actor, "", preview.ID, preview.Revision, "finish-forward")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(t.Context(), "org", "", original.ID, nil); err == nil {
		t.Fatal("expected retained cleanup")
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_deployments SET status='failed' WHERE plan_id=$1`, original.ID+".second-env"); err != nil {
		t.Fatal(err)
	}
	if err = f.service.FinishCapacityCloudSync(t.Context(), "org", "", original.ID, nil); err != nil {
		t.Fatal(err)
	}
	failed, _, err := f.clouds.GetSync(t.Context(), "org", "", original.ID)
	if err != nil || failed.Status != cloudprofiles.SyncFailed {
		t.Fatalf("finish: %+v %v", failed, err)
	}
	receipts := len(f.executor.attempts)
	revert, err := f.service.PreviewCloudRevert(t.Context(), actor, "", original.ID)
	if err != nil || len(revert.Blockers) > 0 {
		t.Fatalf("review compensation: %+v %v", revert, err)
	}
	compensation, err := f.service.ApplyCloudRevert(t.Context(), actor, "", revert.ID, revert.Revision, "finish-restore")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the lost successful finish receipt after the revert was accepted.
	svc := restartedCapacitySyncService(f, nil)
	if err = svc.FinishCapacityCloudSync(t.Context(), "org", "", original.ID, nil); err != nil {
		t.Fatal(err)
	}
	reverting, _, err := f.clouds.GetSync(t.Context(), "org", "", original.ID)
	if err != nil || reverting.Status != cloudprofiles.SyncReverting || len(f.executor.attempts) != receipts {
		t.Fatalf("finish stole compensation ownership: %+v %v", reverting, err)
	}
	f.executor.failCleanup = false
	if err = svc.ReconcileCloudSync(t.Context(), "org", "", compensation.ID, nil); err != nil {
		t.Fatal(err)
	}
	done, _, err := f.clouds.GetSync(t.Context(), "org", "", original.ID)
	if err != nil || done.Status != cloudprofiles.SyncReverted {
		t.Fatalf("compensation did not settle: %+v %v", done, err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_deployments SET status='failed' WHERE plan_id=$1`, compensation.ID+".env"); err != nil {
		t.Fatal(err)
	}
	if err = svc.FinishCapacityCloudSync(t.Context(), "org", "", compensation.ID, nil); err != nil {
		t.Fatal(err)
	}
	settled, _, e := f.clouds.GetSync(t.Context(), "org", "", compensation.ID)
	if e != nil || settled.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("redelivery clobbered success: %+v %v", settled, e)
	}
	if err = svc.FinishCapacityCloudSync(t.Context(), "org", "", original.ID, nil); err != nil {
		t.Fatal(err)
	}
	done, _, err = f.clouds.GetSync(t.Context(), "org", "", original.ID)
	if err != nil || done.Status != cloudprofiles.SyncReverted {
		t.Fatalf("redelivery clobbered reverted state: %+v %v", done, err)
	}
}

type outageSyncAccounts struct{ *syncAccounts }

func (a outageSyncAccounts) All(context.Context, string) ([]*connections.Connection, error) {
	return nil, errors.New("connection storage temporarily unavailable")
}

func TestCapacitySyncPrepareStorageFailureRemainsRetryable(t *testing.T) {
	f := newForwardFixture(t)
	preview := reviewPoolChange(t, f.service)
	accepted, err := f.service.ApplyCloudChange(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, "", preview.ID, preview.Revision, "outage")
	if err != nil {
		t.Fatal(err)
	}
	svc := planning.NewService(planning.Deps{Store: f.plans, Projects: projects.NewPgStore(f.db), CloudProfiles: f.clouds, Connections: outageSyncAccounts{&syncAccounts{store: connections.NewPgStore(f.db)}}, Providers: providers.Registry(), Authz: f.auth})
	result, err := svc.PrepareCapacityCloudSync(t.Context(), "org", "", accepted.ID)
	if err == nil || result.Stopped {
		t.Fatalf("transient read became terminal: %+v %v", result, err)
	}
	persisted, _, err := f.clouds.GetSync(t.Context(), "org", "", accepted.ID)
	if err != nil || persisted.Status != cloudprofiles.SyncPending || persisted.Error != "" {
		t.Fatalf("transient read changed business state: %+v %v", persisted, err)
	}
	healthy := restartedCapacitySyncService(f, nil)
	result, err = healthy.PrepareCapacityCloudSync(orchestration.CapacityContext(t.Context()), "org", "", accepted.ID)
	if err != nil || result.Stopped || len(result.Deployments) != 2 {
		t.Fatalf("recovered read failed: %+v %v", result, err)
	}
}

type failedCapacitySyncSave struct {
	*cloudprofiles.PgStore
	fail bool
}

func (s *failedCapacitySyncSave) SaveSync(ctx context.Context, sync *cloudprofiles.Sync, snapshot *cloudprofiles.Snapshot) error {
	if s.fail {
		return errors.New("sync storage temporarily unavailable")
	}
	return s.PgStore.SaveSync(ctx, sync, snapshot)
}
func TestCapacitySyncPrepareFailureWriteRetry(t *testing.T) {
	f := newForwardFixture(t)
	preview := reviewPoolChange(t, f.service)
	accepted, err := f.service.ApplyCloudChange(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, "", preview.ID, preview.Revision, "write-outage")
	if err != nil {
		t.Fatal(err)
	}
	f.auth.deniedUser = "usr"
	store := &failedCapacitySyncSave{PgStore: f.clouds, fail: true}
	svc := planning.NewService(planning.Deps{Store: f.plans, Projects: projects.NewPgStore(f.db), CloudProfiles: store, Connections: &syncAccounts{store: connections.NewPgStore(f.db)}, Providers: providers.Registry(), Authz: f.auth})
	result, err := svc.PrepareCapacityCloudSync(t.Context(), "org", "", accepted.ID)
	if err == nil || result.Stopped {
		t.Fatalf("failed write acknowledged: %+v %v", result, err)
	}
	persisted, _, err := f.clouds.GetSync(t.Context(), "org", "", accepted.ID)
	if err != nil || persisted.Status != cloudprofiles.SyncPending {
		t.Fatalf("failed write changed state: %+v %v", persisted, err)
	}
	store.fail = false
	result, err = svc.PrepareCapacityCloudSync(t.Context(), "org", "", accepted.ID)
	if err != nil || !result.Stopped {
		t.Fatalf("recovered write failed: %+v %v", result, err)
	}
	persisted, _, err = f.clouds.GetSync(t.Context(), "org", "", accepted.ID)
	if err != nil || persisted.Status != cloudprofiles.SyncFailed {
		t.Fatalf("terminal failure not durable: %+v %v", persisted, err)
	}
}
