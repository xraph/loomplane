//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

func TestSharedDeploymentFinishRejectsHistoricalAndFutureAttempts(t *testing.T) {
	f := newSyncFixture(t)
	ctx := orchestration.CapacityContext(t.Context())
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	preview, previewErr := f.service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"})
	if previewErr != nil {
		t.Fatal(previewErr)
	}
	sync, applyErr := f.service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "finish-upgrade")
	if applyErr != nil {
		t.Fatal(applyErr)
	}
	prepare := func(attempt int) orchestration.CloudSyncPreparation {
		t.Helper()
		p, e := f.service.PrepareCapacityCloudSync(orchestration.CloudSyncAttemptContext(ctx, attempt), "org", "proj", sync.ID)
		if e != nil || p.Stopped || len(p.Deployments) != 2 {
			t.Fatal("prepare", p, e)
		}
		return p
	}
	prepared := prepare(1)
	dep := prepared.Deployments[0].DeploymentID
	store := deployments.NewPgStore(f.db)
	// Simulate a finish committed by the version before durable receipts existed.
	if err := store.Finish(ctx, "org", dep, deployments.StatusFailed, "old failure", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.service.FinishCapacityCloudSync(orchestration.CloudSyncAttemptContext(ctx, 1), "org", "proj", sync.ID, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RetryCloudSync(ctx, actor, "proj", sync.ID); err != nil {
		t.Fatal(err)
	}
	prepare(2)
	workflows := orchestration.NewDeployments()
	workflows.Bind(nil, f.newRunner(), nil)
	finish := workflows.CapacityOptions("test", 1).Activities[orchestration.ActivityDeployFinish]
	body, _ := json.Marshal(map[string]string{"org_id": "org", "deployment_id": dep, "reason": "old failure"})
	call := func(workflow string) error {
		info := drt.ActivityInfo{Key: durable.Key{Namespace: "loomplane", WorkflowID: workflow, RunID: "run"}, CommandID: "finish-" + dep, Attempt: 1}
		_, e := finish(ctx, info, body)
		return e
	}
	assertRunning := func(retries int) {
		t.Helper()
		d, e := store.GetDeployment(ctx, "org", dep)
		if e != nil || d.Status != deployments.StatusRunning || d.Retries != retries || d.Error != "" {
			t.Fatal("stale finish changed reopened shared deployment", d, e)
		}
		for _, step := range d.Steps {
			if step.Status != deployments.StepPending {
				t.Fatalf("stale finish changed pending step: %+v", step)
			}
		}
	}
	if err := call(sync.ID + ".attempt.1"); err != nil {
		t.Fatal(err)
	}
	assertRunning(1)
	for _, id := range []string{sync.ID + ".attempt.3", sync.ID + ".attempt.02", "another-sync.attempt.2", prepared.Deployments[0].PlanID, fmt.Sprintf("%s.retry.1", dep)} {
		if err := call(id); err == nil {
			t.Fatalf("accepted unowned workflow %q", id)
		}
		assertRunning(1)
	}
	if err := call(sync.ID + ".attempt.2"); err != nil {
		t.Fatal(err)
	}
	d, readErr := store.GetDeployment(ctx, "org", dep)
	if readErr != nil || d.Status != deployments.StatusFailed || d.Error != "old failure" {
		t.Fatal("current shared attempt did not settle", d, readErr)
	}

	// The second participant was never reopened; its retry count is still zero.
	other := prepared.Deployments[1].DeploymentID
	otherBody, _ := json.Marshal(map[string]string{"org_id": "org", "deployment_id": other, "reason": "current shared failure"})
	otherInfo := drt.ActivityInfo{Key: durable.Key{Namespace: "loomplane", WorkflowID: sync.ID + ".attempt.2", RunID: "run"}, CommandID: "finish-" + other, Attempt: 1}
	if _, err := finish(ctx, otherInfo, otherBody); err != nil {
		t.Fatal(err)
	}
	otherDeployment, otherErr := store.GetDeployment(ctx, "org", other)
	if otherErr != nil || otherDeployment.Status != deployments.StatusFailed || otherDeployment.Retries != 0 {
		t.Fatal("shared attempt was confused with participant retries", otherDeployment, otherErr)
	}

	advance := func(attempt int) {
		t.Helper()
		if err := store.Finish(ctx, "org", dep, deployments.StatusFailed, "interleaved failure", time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := f.service.FinishCapacityCloudSync(orchestration.CloudSyncAttemptContext(ctx, attempt), "org", "proj", sync.ID, func([]byte) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.RetryCloudSync(ctx, actor, "proj", sync.ID); err != nil {
			t.Fatal(err)
		}
		prepare(attempt + 1)
	}
	advance(2)
	// Change the owning attempt after the preflight read and before finalization.
	interleaved := &sharedFinishAdvanceStore{PgStore: store, advance: func() { advance(3) }}
	workflows.Bind(nil, deployments.NewRunner(deployments.RunnerDeps{Store: interleaved, Plans: planning.NewPgStore(f.db)}), nil)
	if err := call(sync.ID + ".attempt.3"); err != nil {
		t.Fatal(err)
	}
	assertRunning(3)

	// Hold the receipt insert after finalization has locked the sync row.
	workflows.Bind(nil, f.newRunner(), nil)
	block, blockErr := f.db.BeginTx(ctx, nil)
	if blockErr != nil {
		t.Fatal(blockErr)
	}
	defer func() { _ = block.Rollback() }()
	var blockerPID int
	if err := block.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(714002)`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE FUNCTION wait_finish_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(714002); RETURN NEW; END $$`,
		`CREATE TRIGGER wait_finish_receipt BEFORE INSERT ON loomplane_deployment_finishes FOR EACH ROW EXECUTE FUNCTION wait_finish_receipt()`,
	} {
		if _, err := f.conn.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	waitBlocked := func(blocker int, completed <-chan error) int {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			var pid int
			err := f.conn.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) LIMIT 1`, blocker).Scan(&pid)
			if err == nil {
				return pid
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatal(err)
			}
			select {
			case result := <-completed:
				t.Fatalf("operation completed before lock release: %v", result)
			default:
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("operation never waited for expected lock")
		return 0
	}
	finished := make(chan error, 1)
	go func() { finished <- call(sync.ID + ".attempt.4") }()
	finishPID := waitBlocked(blockerPID, finished)
	current, snapshot, currentErr := f.store.GetSync(ctx, "org", "proj", sync.ID)
	if currentErr != nil {
		t.Fatal(currentErr)
	}
	current.Attempt++
	current.Status = cloudprofiles.SyncPending
	advanced := make(chan error, 1)
	go func() { advanced <- f.store.SaveSync(ctx, current, snapshot) }()
	waitBlocked(finishPID, advanced)
	if err := block.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, done := range []<-chan error{finished, advanced} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("operation did not complete after lock release")
		}
	}
}

type sharedFinishAdvanceStore struct {
	*deployments.PgStore
	once    sync.Once
	advance func()
}

func (s *sharedFinishAdvanceStore) FinishedActivity(ctx context.Context, org, id, key string) (int, bool, error) {
	s.once.Do(s.advance)
	return s.PgStore.FinishedActivity(ctx, org, id, key)
}
