//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

func TestSharedSyncCompensationRestoresRetainedSourceWithPendingGuard(t *testing.T) {
	for _, routed := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup", true: "routing"}[routed], func(t *testing.T) {
			f := newForwardFixture(t)
			ctx := t.Context()
			actor := security.Principal{OrgID: "org", UserID: "usr"}
			if routed {
				addForwardRoute(t, f)
				f.routes.pending = true
			}
			preview := reviewPoolChange(t, f.service)
			sync, err := f.service.ApplyCloudChange(ctx, actor, "", preview.ID, preview.Revision, "compensate-forward")
			if err != nil {
				t.Fatal(err)
			}
			if err = f.service.ReconcileCloudSync(ctx, "org", "", sync.ID, func([]byte) error { return nil }); err == nil {
				t.Fatal("missing cleanup refusal")
			}
			var before int
			if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_capacity_reservations`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			revert, err := f.service.PreviewCloudRevert(ctx, actor, "", sync.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(revert.Blockers) > 0 {
				t.Fatal(revert.Blockers)
			}
			for _, environment := range revert.Environments {
				if len(environment.Services) > 0 && (environment.CompensationMode != "restore_retained" || environment.CompensationMessage == "") {
					t.Fatal("review lost retained compensation mode", environment)
				}
			}
			compensation, err := f.service.ApplyCloudRevert(ctx, actor, "", revert.ID, revert.Revision, "restore")
			if err != nil {
				t.Fatal(err)
			}
			var after int
			if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_capacity_reservations`).Scan(&after); err != nil || after != before {
				t.Fatal("restore allocated third capacity", before, after, err)
			}
			f.executor.failCleanup = false
			f.routes.pending = false
			if err = f.service.ReconcileCloudSync(ctx, "org", "", compensation.ID, func([]byte) error { return nil }); err != nil {
				t.Fatal(err)
			}
			saved, _, err := f.clouds.GetSync(ctx, "org", "", compensation.ID)
			if err != nil || saved.Status != cloudprofiles.SyncSucceeded {
				t.Fatal(saved, err)
			}
			for _, environment := range saved.Environments {
				if len(environment.Services) > 0 && environment.CompensationMode != "restore_retained" {
					t.Fatal("history lost immutable compensation mode", environment)
				}
			}
			for _, env := range []string{"env", "second-env"} {
				bindings, readErr := f.plans.ListBindings(ctx, "org", env)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if len(bindings) != 1 || bindings[0].ExternalID != "instance-"+env || len(bindings[0].Replaced) != 0 {
					t.Fatal("source was not restored", bindings)
				}
				if f.executor.calls[env] != 1 || f.executor.removals["instance-"+env] != 0 || f.executor.removals["candidate-"+env+"-api"] != 1 {
					t.Fatal("restore rebuilt or removed source", f.executor.calls, f.executor.removals)
				}
				stale := f.executor.attempts[env]
				if err = f.store.FinishWorkloadAttempt(ctx, stale, true); !errors.Is(err, cloudinfra.ErrConflict) {
					t.Fatal("old attempt settled compensation", err)
				}
			}
		})
	}
}

func (r *forwardRoutes) PrepareCompensationRoutes(ctx context.Context, _ security.Principal, _ cloudinfra.ReleaseRouteRequest) error {
	if _, ok := cloudinfra.WorkloadAttemptFrom(ctx); !ok {
		return errors.New("compensation lost original guard")
	}
	return nil
}
func (r *forwardRoutes) ReconcileCompensationRoutes(ctx context.Context, p security.Principal, in cloudinfra.ReleaseRouteRequest) error {
	return r.ReconcileReleaseRoutes(ctx, p, in)
}

func (e *forwardExecutor) PendingCloudResource(_ context.Context, job *deployments.Job) (string, error) {
	if len(job.Resume) == 0 {
		return "", nil
	}
	return "candidate-" + job.Deployment.EnvironmentID + "-" + job.Action.Name, nil
}
func TestSharedSyncCompensationResolvesFailedOriginalWithoutThirdRuntime(t *testing.T) {
	f := newForwardFixture(t)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	f.executor.failApply = true
	bindings, err := f.plans.ListBindings(ctx, "org", "second-env")
	if err != nil {
		t.Fatal(err)
	}
	settledSource, err := f.store.BeginWorkloadAttempt(ctx, cloudinfra.WorkloadAttempt{Org: "org", Project: bindings[0].ProjectID, Environment: "second-env", Service: "api", Plan: bindings[0].PlanID, Deployment: "settled-source"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishWorkloadAttempt(ctx, *settledSource, true); err != nil {
		t.Fatal(err)
	}
	preview := reviewPoolChange(t, f.service)
	original, err := f.service.ApplyCloudChange(ctx, actor, "", preview.ID, preview.Revision, "failed-forward")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "", original.ID, func([]byte) error { return nil }); err == nil {
		t.Fatal("missing candidate failure")
	}
	revert, err := f.service.PreviewCloudRevert(ctx, actor, "", original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revert.Blockers) > 0 {
		t.Fatal(revert.Blockers)
	}
	compensation, err := f.service.ApplyCloudRevert(ctx, actor, "", revert.ID, revert.Revision, "failed-compensation")
	if err != nil {
		t.Fatal(err)
	}
	f.executor.failApply = false
	f.executor.failCleanup = false
	if err = f.service.ReconcileCloudSync(ctx, "org", "", compensation.ID, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"env", "second-env"} {
		bindings, readErr := f.plans.ListBindings(ctx, "org", env)
		if readErr != nil || len(bindings) != 1 || bindings[0].ExternalID != "instance-"+env {
			t.Fatal("lost original", bindings, readErr)
		}
	}
	if f.executor.calls["env"] != 1 || f.executor.calls["second-env"] != 0 || f.executor.removals["candidate-env-api"] != 1 {
		t.Fatal("failed original rebuilt", f.executor.calls, f.executor.removals)
	}
	var pending int
	if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_capacity_reservations WHERE binding_id=''`).Scan(&pending); err != nil || pending != 0 {
		t.Fatal("unpublished allocation leaked", pending, err)
	}
}

func TestSharedSyncCompensationRecreatesReleasedSourceFromSavedInputs(t *testing.T) {
	f := newForwardFixture(t)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	f.executor.failCleanup = false
	preview := reviewPoolChange(t, f.service)
	original, err := f.service.ApplyCloudChange(ctx, actor, "", preview.ID, preview.Revision, "released-forward")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "", original.ID, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	revert, err := f.service.PreviewCloudRevert(ctx, actor, "", original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revert.Blockers) > 0 {
		t.Fatal(revert.Blockers)
	}
	compensation, err := f.service.ApplyCloudRevert(ctx, actor, "", revert.ID, revert.Revision, "recreate")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "", compensation.ID, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"env", "second-env"} {
		bindings, readErr := f.plans.ListBindings(ctx, "org", env)
		if readErr != nil || len(bindings) != 1 || bindings[0].ExternalID != "recreated-"+env+"-api" {
			t.Fatal("released runtime was not recreated", bindings, readErr)
		}
		if f.executor.calls[env] != 2 || f.executor.removals["instance-"+env] != 1 || f.executor.removals["candidate-"+env+"-api"] != 1 {
			t.Fatal("recreation skipped saved-input execution", f.executor.calls, f.executor.removals)
		}
	}
}

func TestSharedSyncCompensationRestoreRejectsForgeryAndRollsBackConflict(t *testing.T) {
	f := newForwardFixture(t)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	preview := reviewPoolChange(t, f.service)
	original, err := f.service.ApplyCloudChange(ctx, actor, "", preview.ID, preview.Revision, "atomic-forward")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "", original.ID, func([]byte) error { return nil }); err == nil {
		t.Fatal("expected retained cleanup")
	}
	preview, err = f.service.PreviewCloudRevert(ctx, actor, "", original.ID)
	if err != nil || len(preview.Blockers) > 0 {
		t.Fatal(preview, err)
	}
	compensation, err := f.service.ApplyCloudRevert(ctx, actor, "", preview.ID, preview.Revision, "atomic-restore")
	if err != nil {
		t.Fatal(err)
	}
	compensation, snapshot, err := f.clouds.GetSync(ctx, "org", "", compensation.ID)
	if err != nil {
		t.Fatal(err)
	}
	compensation.Status = cloudprofiles.SyncRunning
	if err = f.clouds.SaveSync(ctx, compensation, snapshot); err != nil {
		t.Fatal(err)
	}
	plan, err := f.plans.GetPlan(ctx, "org", compensation.ID+".env")
	if err != nil {
		t.Fatal(err)
	}
	action := plan.Actions[0]
	previous, err := f.store.BeginWorkloadAttempt(ctx, f.executor.attempts["env"], nil, true)
	if err != nil {
		t.Fatal(err)
	}
	next := cloudinfra.WorkloadAttempt{Org: previous.Org, Project: previous.Project, Environment: previous.Environment, Service: previous.Service, Plan: plan.ID, Deployment: "compensation-deployment"}
	capture := func() string {
		t.Helper()
		var raw string
		readErr := f.sql.QueryRow(ctx, `SELECT jsonb_build_array((SELECT to_jsonb(b) FROM loomplane_bindings b WHERE org_id='org' AND environment_id='env' AND name='api'),(SELECT jsonb_agg(to_jsonb(r) ORDER BY plan_id) FROM loomplane_capacity_reservations r WHERE org_id='org' AND environment_id='env'),(SELECT to_jsonb(w) FROM loomplane_workload_attempts w WHERE org_id='org' AND environment_id='env' AND service='api'))::text`).Scan(&raw)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return raw
	}
	before := capture()
	forged := action
	meta := *action.Compensation
	forged.Compensation = &meta
	meta.Source.ExternalID = "forged-instance"
	if _, err = f.plans.RestoreCloudCompensation(previous.Context(ctx), *previous, next, plan, forged); err == nil {
		t.Fatal("forged source accepted")
	}
	if capture() != before {
		t.Fatal("forgery changed durable state")
	}
	// Fail after the guard and reservation writes, proving their transaction rolls back.
	if _, err = f.sql.Exec(ctx, `CREATE FUNCTION reject_compensation_binding() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected binding conflict'; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(ctx, `CREATE TRIGGER reject_compensation_binding BEFORE UPDATE ON loomplane_bindings FOR EACH ROW EXECUTE FUNCTION reject_compensation_binding()`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plans.RestoreCloudCompensation(previous.Context(ctx), *previous, next, plan, action); err == nil {
		t.Fatal("injected conflict accepted")
	}
	if capture() != before {
		t.Fatal("conflict partially transferred binding, reservation or guard")
	}
	if _, err = f.sql.Exec(ctx, `DROP TRIGGER reject_compensation_binding ON loomplane_bindings`); err != nil {
		t.Fatal(err)
	}
	var reservationBody string
	if err = f.sql.QueryRow(ctx, `SELECT body::text FROM loomplane_capacity_reservations WHERE org_id='org' AND environment_id='env' AND plan_id=$1`, action.Compensation.Source.PlanID).Scan(&reservationBody); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_capacity_reservations SET body=jsonb_set(body,'{server_id}','"forged-server"') WHERE org_id='org' AND environment_id='env' AND plan_id=$1`, action.Compensation.Source.PlanID); err != nil {
		t.Fatal(err)
	}
	mismatch := capture()
	if _, err = f.plans.RestoreCloudCompensation(previous.Context(ctx), *previous, next, plan, action); err == nil {
		t.Fatal("mismatched source reservation accepted")
	}
	if capture() != mismatch {
		t.Fatal("reservation mismatch partially transferred state")
	}
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_capacity_reservations SET body=$2::jsonb WHERE org_id='org' AND environment_id='env' AND plan_id=$1`, action.Compensation.Source.PlanID, reservationBody); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plans.RestoreCloudCompensation(previous.Context(ctx), *previous, next, plan, action); err != nil {
		t.Fatal(err)
	}
	var afterBody string
	var count int
	if err = f.sql.QueryRow(ctx, `SELECT body::text FROM loomplane_capacity_reservations WHERE org_id='org' AND environment_id='env' AND plan_id=$1`, plan.ID).Scan(&afterBody); err != nil || afterBody != reservationBody {
		t.Fatal("retained allocation changed", err)
	}
	if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_capacity_reservations WHERE org_id='org' AND environment_id='env'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("capacity was released or third allocation created", count, err)
	}
}

func TestSharedSyncCompensationDeniesNewAuthorityButRetainsCleanupAndUnrelatedProject(t *testing.T) {
	f := newForwardFixture(t)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	unrelated := retainUnrelatedForwardProject(t, f)
	preview := reviewPoolChange(t, f.service)
	original, err := f.service.ApplyCloudChange(ctx, actor, "", preview.ID, preview.Revision, "authority-forward")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "", original.ID, nil); err == nil {
		t.Fatal("expected pending cleanup")
	}
	preview, err = f.service.PreviewCloudRevert(ctx, actor, "", original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Environments[0].CompensationMode != "restore_retained" {
		t.Fatal(preview)
	}
	compensation, err := f.service.ApplyCloudRevert(ctx, actor, "", preview.ID, preview.Revision, "authority-restore")
	if err != nil {
		t.Fatal(err)
	}
	f.auth.deniedUser = "usr"
	if err = f.service.ReconcileCloudSync(ctx, "org", "", compensation.ID, nil); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("revoked actor published", err)
	}
	b, err := f.plans.ListBindings(ctx, "org", "env")
	if err != nil || b[0].ExternalID != "candidate-env-api" {
		t.Fatal("revoked actor restored source", err)
	}
	f.auth.deniedUser = ""
	if _, err = f.service.RetryCloudSync(ctx, actor, "", compensation.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "", compensation.ID, nil); err == nil {
		t.Fatal("expected candidate cleanup failure")
	}
	saved, _, err := f.clouds.GetSync(ctx, "org", "", compensation.ID)
	if err != nil || saved.Status != cloudprofiles.SyncCleanup || saved.Environments[0].CompensationMode != "restore_retained" {
		t.Fatal(saved, err)
	}
	if _, err = f.clouds.SetProjectGrant(ctx, "org", "cloud", "proj", nil, true); err != nil {
		t.Fatal(err)
	}
	plan, err := f.plans.GetPlan(ctx, "org", compensation.ID+".env")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.ValidateCloudSyncExecution(ctx, plan); err == nil {
		t.Fatal("cleanup grants authorized new publication")
	}
	f.executor.failCleanup = false
	restarted := f.runner()
	f.service.BindCloudSyncRunner(restarted, restarted)
	if err = f.service.ReconcileCloudSync(ctx, "org", "", compensation.ID, nil); err != nil {
		t.Fatal(err)
	}
	actual, err := f.clouds.SnapshotBindings(ctx, "org", "second-env")
	if err != nil || string(actual) != string(unrelated) {
		t.Fatal("unrelated project changed", err)
	}
	if f.executor.calls["env"] != 1 || len(f.executor.removals) != 1 || f.executor.removals["candidate-env-api"] != 1 {
		t.Fatal(f.executor.calls, f.executor.removals)
	}
}

func TestSharedSyncCompensationKeepsResourcesWhenGrantOrFenceIsUnavailable(t *testing.T) {
	for _, reason := range []string{"grant", "legacy_unfenced"} {
		t.Run(reason, func(t *testing.T) {
			f := newForwardFixture(t)
			ctx := t.Context()
			actor := security.Principal{OrgID: "org", UserID: "usr"}
			preview := reviewPoolChange(t, f.service)
			original, err := f.service.ApplyCloudChange(ctx, actor, "", preview.ID, preview.Revision, "blocked-forward")
			if err != nil {
				t.Fatal(err)
			}
			if err = f.service.ReconcileCloudSync(ctx, "org", "", original.ID, nil); err == nil {
				t.Fatal("expected pending cleanup")
			}
			preview, err = f.service.PreviewCloudRevert(ctx, actor, "", original.ID)
			if err != nil {
				t.Fatal(err)
			}
			compensation, err := f.service.ApplyCloudRevert(ctx, actor, "", preview.ID, preview.Revision, "blocked-restore")
			if err != nil {
				t.Fatal(err)
			}
			if reason == "grant" {
				_, err = f.clouds.SetProjectGrant(ctx, "org", "cloud", "proj", nil, true)
			} else {
				_, err = f.sql.Exec(ctx, `UPDATE loomplane_workload_attempts SET legacy_unfenced=true WHERE org_id='org' AND environment_id='env'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = f.service.ReconcileCloudSync(ctx, "org", "", compensation.ID, nil); err == nil {
				t.Fatal("unsafe restore accepted")
			}
			bindings, err := f.plans.ListBindings(ctx, "org", "env")
			if err != nil || bindings[0].ExternalID != "candidate-env-api" || len(bindings[0].Replaced) != 1 || bindings[0].Replaced[0].ExternalID != "instance-env" {
				t.Fatal("resources changed after denied restore", bindings, err)
			}
			var owner string
			var settled bool
			if err = f.sql.QueryRow(ctx, `SELECT plan_id,settled FROM loomplane_workload_attempts WHERE org_id='org' AND environment_id='env'`).Scan(&owner, &settled); err != nil || owner != original.ID+".env" || settled {
				t.Fatal("ambiguous guard cleared", owner, settled, err)
			}
			if len(f.executor.removals) != 0 {
				t.Fatal("denied restore removed resources", f.executor.removals)
			}
		})
	}
}

func TestSharedSyncCompensationReopensOnlyExactReleasedHandoff(t *testing.T) {
	f := newForwardFixture(t)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	addForwardRoute(t, f)
	preview := reviewPoolChange(t, f.service)
	original, err := f.service.ApplyCloudChange(ctx, actor, "", preview.ID, preview.Revision, "closed-forward")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "", original.ID, nil); err == nil {
		t.Fatal("expected source cleanup failure")
	}
	old, err := f.plans.GetPlan(ctx, "org", original.ID+".env")
	if err != nil {
		t.Fatal(err)
	}
	source := old.Actions[0].Routes[0]
	candidate := source
	candidate.Revision++
	candidate.Binding.InstanceID = "candidate-env-api"
	candidate.Binding.ServerID = "server-2"
	candidate.Binding.ConnectionID = "runtime-2"
	raw, _ := json.Marshal(candidate)
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_domain_routes SET body=$1,revision=$2,server_id=$3,connection_id=$4 WHERE id='route-env'`, raw, candidate.Revision, candidate.Binding.ServerID, candidate.Binding.ConnectionID); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(old.ID + "/" + old.Actions[0].ID + "/" + source.ID))
	prepared := &cloudinfra.RouteOperation{ID: "routeop_" + hex.EncodeToString(hash[:16]), Kind: "prepare", Status: "succeeded", Revision: 1, Route: candidate, Source: &source}
	if err = f.store.PreviewRoute(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	cutover := &cloudinfra.RouteOperation{ID: "cutover", Kind: "cutover", Status: "succeeded", Revision: 1, Route: candidate, Source: &source, HandoffID: prepared.ID}
	if err = f.store.PreviewRoute(ctx, cutover); err != nil {
		t.Fatal(err)
	}
	release := &cloudinfra.RouteOperation{ID: "released", Kind: "release", Status: "waiting", Revision: 1, Route: candidate, Source: &source, HandoffID: prepared.ID}
	if err = f.store.PreviewRoute(ctx, release); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.BeginIngress(ctx, release); err != nil {
		t.Fatal(err)
	}
	if err = f.store.CompleteHandoff(ctx, release); err != nil {
		t.Fatal(err)
	}
	release.Status = "succeeded"
	if err = f.store.SaveRouteOperation(ctx, release); err != nil {
		t.Fatal(err)
	}
	preview, err = f.service.PreviewCloudRevert(ctx, actor, "", original.ID)
	if err != nil || len(preview.Blockers) > 0 {
		t.Fatal(preview, err)
	}
	compensation, err := f.service.ApplyCloudRevert(ctx, actor, "", preview.ID, preview.Revision, "closed-restore")
	if err != nil {
		t.Fatal(err)
	}
	compensation, snapshot, err := f.clouds.GetSync(ctx, "org", "", compensation.ID)
	if err != nil {
		t.Fatal(err)
	}
	compensation.Status = cloudprofiles.SyncRunning
	if err = f.clouds.SaveSync(ctx, compensation, snapshot); err != nil {
		t.Fatal(err)
	}
	previous, err := f.store.BeginWorkloadAttempt(ctx, f.executor.attempts["env"], nil, true)
	if err != nil {
		t.Fatal(err)
	}
	claimCtx := previous.Context(ctx)
	// The coordinator physically arms these generations before this store boundary.
	if _, err = f.store.BeginServerIngress(ctx, "org", source.Binding.ServerID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.BeginServerIngress(ctx, "org", candidate.Binding.ServerID); err != nil {
		t.Fatal(err)
	}
	assertClosed := func() {
		t.Helper()
		var released bool
		var holds int
		if readErr := f.sql.QueryRow(ctx, `SELECT released FROM loomplane_route_handoffs WHERE id=$1`, prepared.ID).Scan(&released); readErr != nil || !released {
			t.Fatal("partial handoff reopen", readErr)
		}
		if readErr := f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_connection_uses WHERE kind='route_handoff' AND owner_id=$1`, prepared.ID).Scan(&holds); readErr != nil || holds != 0 {
			t.Fatal("partial credential claim", holds, readErr)
		}
	}
	stale := *cutover
	stale.Revision--
	if err = f.store.ClaimCompensationHandoff(claimCtx, prepared, &candidate, &stale); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("stale cutover accepted", err)
	}
	assertClosed()
	if _, err = f.sql.Exec(ctx, `CREATE FUNCTION reject_handoff_claim() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='route_handoff' THEN RAISE EXCEPTION 'injected claim conflict'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(ctx, `CREATE TRIGGER reject_handoff_claim BEFORE INSERT ON loomplane_connection_uses FOR EACH ROW EXECUTE FUNCTION reject_handoff_claim()`); err != nil {
		t.Fatal(err)
	}
	if err = f.store.ClaimCompensationHandoff(claimCtx, prepared, &candidate, cutover); err == nil {
		t.Fatal("claim conflict accepted")
	}
	assertClosed()
	if _, err = f.sql.Exec(ctx, `DROP TRIGGER reject_handoff_claim ON loomplane_connection_uses`); err != nil {
		t.Fatal(err)
	}
	if err = f.store.ClaimCompensationHandoff(claimCtx, prepared, &candidate, cutover); err != nil {
		t.Fatal(err)
	}
	if err = f.store.ClaimCompensationHandoff(claimCtx, prepared, &candidate, cutover); err != nil {
		t.Fatal("exact reopen is not idempotent", err)
	}
	if err = f.store.CompleteHandoff(ctx, release); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("stale release reclosed compensation", err)
	}
	var released bool
	var owner string
	var holds int
	if err = f.sql.QueryRow(ctx, `SELECT released,compensation_plan_id FROM loomplane_route_handoffs WHERE id=$1`, prepared.ID).Scan(&released, &owner); err != nil || released || owner != compensation.ID+".env" {
		t.Fatal(released, owner, err)
	}
	if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_connection_uses WHERE kind='route_handoff' AND owner_id=$1`, prepared.ID).Scan(&holds); err != nil || holds != 2 {
		t.Fatal("recorded endpoint holds not restored", holds, err)
	}
}

// Direct server targets share the durable workload guard but own no pool reservation.
func TestSharedSyncCompensationRestoresDirectServerSource(t *testing.T) {
	f := newForwardFixture(t)
	ctx := t.Context()
	for _, query := range []string{
		`DELETE FROM loomplane_capacity_reservations`,
		`DELETE FROM loomplane_server_pool_members`,
	} {
		if _, err := f.sql.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	target, err := f.clouds.GetTarget(ctx, "org", "", "cloud", "pool-target")
	if err != nil {
		t.Fatal(err)
	}
	target.ID, target.Name, target.Runtime, target.PoolID, target.ConnectionID = "direct-target", "direct", "managed", "", "runtime-1"
	if err = f.clouds.CreateTarget(ctx, target); err != nil {
		t.Fatal(err)
	}

	for _, env := range []string{"env", "second-env"} {
		bindings, readErr := f.plans.ListBindings(ctx, "org", env)
		if readErr != nil {
			t.Fatal(readErr)
		}
		b := bindings[0]
		var cfg planning.ServiceConfig
		if err = json.Unmarshal(b.Config, &cfg); err != nil {
			t.Fatal(err)
		}
		old := cfg.CloudPlacement
		cfg.CloudPlacement = &planning.CloudPlacement{CloudID: old.CloudID, CloudRevision: old.CloudRevision, GrantRevision: old.GrantRevision, AssignmentRevision: old.AssignmentRevision, TargetID: target.ID, TargetRevision: target.Revision, TargetName: target.Name, Provider: old.Provider, ConnectionID: old.ConnectionID, AccountIdentity: old.AccountIdentity}
		b.Config, err = json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.plans.PutBinding(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	preview, err := f.service.PreviewCloudChange(ctx, actor, "", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "direct-target", Revision: 1, Provider: "server", ConnectionID: "runtime-2"})
	if err != nil || len(preview.Blockers) > 0 {
		t.Fatal(preview, err)
	}
	original, err := f.service.ApplyCloudChange(ctx, actor, "", preview.ID, preview.Revision, "direct-forward")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "", original.ID, func([]byte) error { return nil }); err == nil {
		t.Fatal("missing retained cleanup failure")
	}
	revert, err := f.service.PreviewCloudRevert(ctx, actor, "", original.ID)
	if err != nil || len(revert.Blockers) > 0 {
		t.Fatal(revert, err)
	}
	compensation, err := f.service.ApplyCloudRevert(ctx, actor, "", revert.ID, revert.Revision, "direct-restore")
	if err != nil {
		t.Fatal(err)
	}
	f.executor.failCleanup = false
	if err = f.service.ReconcileCloudSync(ctx, "org", "", compensation.ID, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"env", "second-env"} {
		bindings, readErr := f.plans.ListBindings(ctx, "org", env)
		if readErr != nil || len(bindings) != 1 || bindings[0].ExternalID != "instance-"+env || len(bindings[0].Replaced) != 0 {
			t.Fatal(bindings, readErr)
		}
		if f.executor.calls[env] != 1 || f.executor.removals["instance-"+env] != 0 || f.executor.removals["candidate-"+env+"-api"] != 1 {
			t.Fatal(f.executor.calls, f.executor.removals)
		}
		if err = f.store.FinishWorkloadAttempt(ctx, f.executor.attempts[env], true); !errors.Is(err, cloudinfra.ErrConflict) {
			t.Fatal("old direct attempt settled restored guard", err)
		}
	}
	var count int
	if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_capacity_reservations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("direct restore allocated pool capacity", count, err)
	}
}

func TestSharedSyncCompensationLegacyMetadataRefusesLateAttempt(t *testing.T) {
	f := newForwardFixture(t)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	preview := reviewPoolChange(t, f.service)
	original, err := f.service.ApplyCloudChange(ctx, actor, "", preview.ID, preview.Revision, "legacy-proof")
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`DROP TRIGGER loomplane_sync_snapshot_immutable ON loomplane_cloud_syncs`,
		`UPDATE loomplane_cloud_syncs SET snapshot=jsonb_set(snapshot,'{environments}',(SELECT jsonb_agg(e-'accepted_plan') FROM jsonb_array_elements(snapshot->'environments') e)),body=jsonb_set(body,'{environments}',(SELECT jsonb_agg(e-'plan_id') FROM jsonb_array_elements(body->'environments') e))`,
		`DELETE FROM loomplane_capacity_reservations WHERE plan_id LIKE 'csync_%'`,
		`DELETE FROM loomplane_plans WHERE coalesce(result->>'cloud_sync_id','')<>''`,
	} {
		if _, err = f.sql.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	applySyncUpgrade(t, f.poolFixture)
	if err = f.service.ReconcileCloudSync(ctx, "org", "", original.ID, func([]byte) error { return nil }); err == nil {
		t.Fatal("legacy pooled execution was admitted")
	}
	revert, err := f.service.PreviewCloudRevert(ctx, actor, "", original.ID)
	if err != nil || len(revert.Blockers) > 0 {
		t.Fatal(revert, err)
	}
	inject := func() {
		t.Helper()
		if _, e := f.sql.Exec(ctx, `INSERT INTO loomplane_workload_attempts(org_id,project_id,environment_id,service,plan_id,deployment_id,generation,legacy_unfenced) VALUES('org','proj','env','api',$1,'unknown-legacy',0,true)`, original.ID+".env"); e != nil {
			t.Fatal(e)
		}
	}
	inject()
	if _, err = f.service.PreviewCloudRevert(ctx, actor, "", original.ID); err == nil {
		t.Fatal("legacy unknown attempt was reviewable")
	}
	if _, err = f.service.ApplyCloudRevert(ctx, actor, "", revert.ID, revert.Revision, "late-accept"); err == nil {
		t.Fatal("late legacy attempt passed acceptance")
	}
	// Only the fixture removes its injected attempt, to exercise the later write boundary.
	if _, err = f.sql.Exec(ctx, `DELETE FROM loomplane_workload_attempts WHERE deployment_id='unknown-legacy'`); err != nil {
		t.Fatal(err)
	}
	compensation, err := f.service.ApplyCloudRevert(ctx, actor, "", revert.ID, revert.Revision, "safe-accept")
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.clouds.SnapshotBindings(ctx, "org", "env")
	if err != nil {
		t.Fatal(err)
	}
	inject()
	if err = f.service.ReconcileCloudSync(ctx, "org", "", compensation.ID, func([]byte) error { return nil }); err == nil {
		t.Fatal("late legacy attempt passed metadata publication")
	}
	after, err := f.clouds.SnapshotBindings(ctx, "org", "env")
	if err != nil || string(after) != string(before) {
		t.Fatal("refused metadata recovery changed source", err)
	}
	var retained bool
	if err = f.sql.QueryRow(ctx, `SELECT active AND NOT settled AND legacy_unfenced FROM loomplane_workload_attempts WHERE deployment_id='unknown-legacy'`).Scan(&retained); err != nil || !retained {
		t.Fatal("legacy guard was cleared", retained, err)
	}
	if len(f.executor.calls) != 0 || len(f.executor.removals) != 0 {
		t.Fatal("legacy metadata recovery made runtime effects")
	}
}
