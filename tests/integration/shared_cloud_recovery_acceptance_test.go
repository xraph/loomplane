//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	apiclient "github.com/xraph/loomplane/pkg/client"
)

func TestAcceptanceSharedCloudRecovery(t *testing.T) {
	t.Run("historical_forward_migration", recoveryHistoricalUpgrade)
	f := newRecoveryFixture(t)
	t.Run("connection_drift_before_effect", func(t *testing.T) { f.t = t; f.c.t = t; f.connectionDrift() })
	if t.Failed() {
		return
	}
	t.Run("direct_caller_loss", func(t *testing.T) { f.t = t; f.c.t = t; f.callerLoss() })
	if t.Failed() {
		return
	}
	t.Run("accept_route_race", func(t *testing.T) { f.t = t; f.c.t = t; f.acceptRouteRace() })
	if t.Failed() {
		return
	}
	t.Run("cross_organization_denial", func(t *testing.T) { f.t = t; f.c.t = t; f.crossOrganization() })
	f.t = t
	f.c.t = t
}

func (f *recoveryFixture) connectionDrift() {
	t := f.t
	before := f.physical()
	f.c.must(http.StatusOK, http.MethodPatch, "/api/v1/variables/"+f.variable, map[string]string{"value": f.state.RunID + "-drift"}, nil)
	f.marker = f.state.RunID + "-drift"
	plan := f.plan()
	persisted, err := planning.NewPgStore(f.db).GetPlan(t.Context(), f.org, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := json.Marshal(persisted.Actions)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := pgx.Connect(t.Context(), f.sql.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Close(context.Background())
	tx, err := barrier.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	key := "route-workload/" + f.org + "/" + f.environment + "/web"
	if _, err = tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		t.Fatal(err)
	}
	f.approve(plan)
	recoveryWait(t, 10*time.Second, func() bool {
		var waiting bool
		if err = f.sql.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, barrier.PgConn().PID()).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		return waiting
	})
	old := f.connections["host1"]
	settings := map[string]string{}
	for k, v := range old.Details {
		settings[k] = v
	}
	alias := "/var/run/loomplane-recovery-drift.sock"
	combinedExec(t, f.ssh, nil, "ln -s /tmp/loomplane-recovery-relay.sock "+combinedQuote(alias))
	settings["docker_socket"] = alias
	rejected := recoveryHTTP(f.c, http.MethodPut, "/api/v1/connections/"+old.ID+"/credential", map[string]any{"credential": string(f.private), "settings": settings})
	if rejected.Err != nil || rejected.Status != http.StatusUnprocessableEntity || !strings.Contains(rejected.Body, "verified account changed") {
		t.Fatalf("socket identity boundary: %+v", rejected)
	}
	f.record("public_socket_change_refused", rejected)
	// Controlled external-state fault injection changes only this owned connection.
	// The store advances its generation; the accepted plan and attempt stay intact.
	changed := old
	changed.Details = settings
	if err = connections.NewPgStore(f.db).UpdateConnection(t.Context(), &changed); err != nil {
		t.Fatal(err)
	}
	if changed.Generation != old.Generation+1 || changed.Details["docker_id"] != old.Details["docker_id"] {
		t.Fatal("replacement changed identity or failed to advance generation")
	}
	f.connections["host1"] = changed
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	failed := f.deployment(plan)
	if failed.Status != "failed" {
		t.Fatalf("drift executed: %+v", failed)
	}
	raw := f.c.raw("/api/v1/deployments/" + failed.ID)
	if !strings.Contains(raw, "target settings") {
		t.Fatalf("wrong pre-effect refusal: %s", raw)
	}
	var active, settled, effects bool
	var generation int64
	if err = f.sql.QueryRow(t.Context(), `SELECT active,settled,effects_started,generation FROM loomplane_workload_attempts WHERE org_id=$1 AND plan_id=$2 AND service='web'`, f.org, plan.ID).Scan(&active, &settled, &effects, &generation); err != nil {
		t.Fatal(err)
	}
	if active || !settled || effects || f.physical() != before {
		t.Fatalf("no-effect claim not settled: active=%v settled=%v effects=%v", active, settled, effects)
	}
	stored, err := planning.NewPgStore(f.db).GetPlan(t.Context(), f.org, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(stored.Actions)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(pin) {
		t.Fatal("accepted action pin was rewritten")
	}
	fresh := f.plan()
	f.approve(fresh)
	succeeded := f.deployment(fresh)
	if succeeded.Status != "succeeded" {
		t.Fatalf("fresh review failed: %+v", succeeded)
	}
	f.record("connection_drift", map[string]any{"refusal": raw, "old_generation": old.Generation, "new_generation": changed.Generation, "attempt_generation": generation, "no_effect_settled": true, "fresh_plan": fresh.ID, "container": f.container()})
}

func recoveryHistoricalUpgrade(t *testing.T) {
	// This fixture reconstructs historical database rows. It does not claim that
	// an old worker touched a remote host; the physical case records that separately.
	f := newPoolFixture(t)
	ctx := t.Context()
	request := f.request("env", "web")
	placement, err := f.store.PreviewPlacement(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	f.plan(t, "newer-pooled", request, placement)
	f.accept(t, "newer-pooled")
	for _, query := range []string{
		`INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at) VALUES('older-direct','org','proj','env','production','applied','review','pool-manifest-proj',1,'{"actions":[{"id":"service:web","name":"web","provider":"server","op":"update"}]}',true,'usr',now())`,
		`INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at,finished_at,steps) VALUES('older-worker','org','proj','env','production','older-direct','reviewed','failed','usr',now()-interval '3 hours',now()-interval '2 hours','[{"action_id":"service:web","status":"failed"}]'),('newer-worker','org','proj','env','production','newer-pooled','reviewed','failed','usr',now()-interval '1 hour',now(),'[{"action_id":"service:web","status":"failed"}]')`,
		`DROP TABLE loomplane_workload_attempts`,
	} {
		if _, err = f.sql.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"20261028000300", "20261028000400"} {
		if err = federationMigration(t, id).Up(ctx, pgmigrate.New(f.db)); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func() string {
		t.Helper()
		var raw string
		if err = f.sql.QueryRow(ctx, `SELECT row_to_json(w)::text FROM loomplane_workload_attempts w WHERE org_id='org' AND environment_id='env' AND service='web'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := snapshot()
	var plan, deployment string
	var generation int64
	var legacy, settled bool
	if err = f.sql.QueryRow(ctx, `SELECT plan_id,deployment_id,generation,legacy_unfenced,settled FROM loomplane_workload_attempts WHERE org_id='org' AND environment_id='env' AND service='web'`).Scan(&plan, &deployment, &generation, &legacy, &settled); err != nil {
		t.Fatal(err)
	}
	if !legacy || settled {
		t.Fatal("upgrade erased older direct uncertainty", before)
	}
	actor := security.Principal{OrgID: "org", UserID: "manager"}
	svc := cloudinfra.NewService(cloudinfra.Deps{Store: f.store, Clouds: f.clouds, Authz: &syncAuth{}})
	input := cloudinfra.RecoveryInput{PlanID: "newer-pooled", Service: "web"}
	review, err := svc.PreviewReservationRecovery(ctx, actor, "cloud", input)
	if err != nil {
		t.Fatal(err)
	}
	input.Review = review.Review
	recovered, err := svc.RecoverReservation(ctx, actor, "cloud", input)
	if err != nil || recovered.Phase != "released" {
		t.Fatal("newer no-effect reservation recovery", recovered, err)
	}
	if snapshot() != before {
		t.Fatal("newer pooled recovery changed legacy attempt", snapshot())
	}
	stale := cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "web", Plan: plan, Deployment: deployment, Generation: generation}
	if err = f.store.FinishWorkloadAttempt(ctx, stale, true); err == nil {
		t.Fatal("legacy completion fabricated quiescence")
	}
	for _, candidate := range []cloudinfra.WorkloadAttempt{stale, {Org: "org", Project: "proj", Environment: "env", Service: "web", Plan: "newer-pooled", Deployment: "newer-worker"}} {
		if _, err = f.store.BeginWorkloadAttempt(ctx, candidate, nil, false); err == nil || !strings.Contains(err.Error(), "pre-upgrade unfenced worker") {
			t.Fatal("legacy admission reopened", err)
		}
	}
	if snapshot() != before {
		t.Fatal("stale completion changed legacy history")
	}
	t.Logf("Historical database %s: migrations 20261028000300 and 20261028000400; preserved %s after newer pooled recovery and stale completion", f.sql.Config().Database, before)
}

func (f *recoveryFixture) callerLoss() {
	t := f.t
	oldContainer := f.container()
	f.record("caller_loss_old_container", oldContainer)
	f.c.must(http.StatusOK, http.MethodPatch, "/api/v1/variables/"+f.variable, map[string]string{"value": f.state.RunID + "-after-caller-loss"}, nil)
	f.marker = f.state.RunID + "-after-caller-loss"
	plan := f.plan()
	combinedExec(t, f.ssh, nil, "docker kill --signal STOP "+combinedQuote(oldContainer))
	// Keep failure teardown bounded, even if a later observation fails.
	defer combinedExec(t, f.ssh, nil, "docker kill --signal CONT "+combinedQuote(oldContainer)+" 2>/dev/null || true")
	f.approve(plan)
	var receipt struct {
		Method, Path, Container string
		Helper                  int
		Ancestry                []struct {
			PID, Parent int
			Command     string
		}
	}
	recoveryWait(t, 30*time.Second, func() bool {
		raw := combinedExec(t, f.ssh, nil, "cat /tmp/loomplane-recovery-stop.jsonl 2>/dev/null || true")
		for _, line := range strings.Split(raw, "\n") {
			if line == "" {
				continue
			}
			if err := json.Unmarshal([]byte(line), &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Container == oldContainer {
				return true
			}
		}
		return false
	})
	var attempt cloudinfra.WorkloadAttempt
	attempt.Org, attempt.Project, attempt.Environment, attempt.Service, attempt.Plan = f.org, f.project, f.environment, "web", plan.ID
	var active, effects, settled bool
	if err := f.sql.QueryRow(t.Context(), `SELECT deployment_id,generation,active,effects_started,settled FROM loomplane_workload_attempts WHERE org_id=$1 AND plan_id=$2 AND service='web'`, f.org, plan.ID).Scan(&attempt.Deployment, &attempt.Generation, &active, &effects, &settled); err != nil {
		t.Fatal(err)
	}
	if !active || !effects || settled {
		t.Fatal("stop was not bound to an active direct attempt")
	}
	var pooled bool
	if err := f.sql.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM loomplane_pool_execution WHERE org_id=$1 AND plan_id=$2)`, f.org, plan.ID).Scan(&pooled); err != nil {
		t.Fatal(err)
	}
	if pooled {
		t.Fatal("caller-loss case accidentally used pooled execution")
	}
	journal := func() (map[string]int64, bool) {
		t.Helper()
		var state struct {
			Epochs map[string]int64
			Dirty  bool
		}
		raw := combinedExec(t, f.ssh, nil, "cat /var/tmp/loomplane-fence-*/state")
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			t.Fatal(err)
		}
		return state.Epochs, state.Dirty
	}
	operationHash := fmt.Sprintf("%x", sha256.Sum256([]byte(f.org+"/"+plan.ID+"/web")))
	epochs, dirty := journal()
	if !dirty || epochs[operationHash] != attempt.Generation {
		t.Fatal("physical stop lacks durable generation", epochs, dirty)
	}
	if len(receipt.Ancestry) < 2 || receipt.Ancestry[0].PID != receipt.Helper {
		t.Fatal("missing exact helper ancestry", receipt)
	}
	session := receipt.Ancestry[len(receipt.Ancestry)-1]
	if !strings.Contains(session.Command, "sshd") || session.Parent <= 1 || session.PID == receipt.Helper {
		t.Fatal("refusing to kill an unproven SSH session", receipt)
	}
	f.record("caller_loss", map[string]any{"attempt": attempt, "stop_receipt": receipt, "physical_generation": epochs[operationHash]})
	// Recheck the selected process immediately before terminating that session.
	script := fmt.Sprintf(`import os,signal
pid=%d
command=open('/proc/'+str(pid)+'/cmdline','rb').read().replace(b'\0',b' ').decode().split(' ',1)[0]
assert command==%q
os.kill(pid,signal.SIGKILL)
`, session.PID, session.Command)
	combinedExec(t, f.ssh, []byte(script), "python3 -")
	pending := recoveryHTTP(f.c, http.MethodPost, "/api/v1/deployments/"+attempt.Deployment+"/retry", nil)
	f.record("caller_loss_inflight_retry", pending)
	if pending.Err != nil || pending.Status != http.StatusConflict {
		t.Fatalf("retry overlapped an active physical stop: %+v", pending)
	}
	before := f.physical()
	for i := 0; i < 5; i++ {
		current, isDirty := journal()
		if !isDirty || current[operationHash] != attempt.Generation {
			t.Fatal("unresolved stop did not retain its physical generation", current, isDirty)
		}
		if f.physical() != before {
			t.Fatal("physical state changed while suspended stop was in flight")
		}
		time.Sleep(40 * time.Millisecond)
	}
	result := f.deployment(plan)
	f.record("caller_loss_recorded_outcome", result)
	if result.Status != "succeeded" {
		t.Fatalf("natural caller-loss recovery failed: %+v", result)
	}
	current, isDirty := journal()
	var currentGeneration int64
	if err := f.sql.QueryRow(t.Context(), `SELECT active,settled,generation FROM loomplane_workload_attempts WHERE org_id=$1 AND plan_id=$2 AND service='web'`, f.org, plan.ID).Scan(&active, &settled, &currentGeneration); err != nil {
		t.Fatal(err)
	}
	if isDirty || current[operationHash] != currentGeneration {
		t.Fatal("recovered deployment lacks physical quiescence", current, isDirty)
	}
	recoveredContainer := f.container()
	if recoveredContainer == oldContainer {
		t.Fatal("recovery did not publish its new release")
	}
	if combinedExec(t, f.ssh, nil, "docker ps -aq --no-trunc --filter id="+combinedQuote(oldContainer)) != "" {
		t.Fatal("old exact container remains after natural recovery")
	}
	if active || !settled || currentGeneration <= attempt.Generation {
		t.Fatal("natural recovery did not advance and settle its attempt generation")
	}
	if err := cloudinfra.NewPgStore(f.db).FinishWorkloadAttempt(t.Context(), attempt, true); err == nil {
		t.Fatal("stale completion settled the recovered generation")
	} else {
		f.record("caller_loss_recovery_stale_completion_refused", err.Error())
	}
	f.record("caller_loss_recovered", map[string]any{"deployment": result.ID, "plan": plan.ID, "old_container": oldContainer, "new_container": recoveredContainer, "old_generation": attempt.Generation, "generation": currentGeneration, "settled": settled, "recovery": "production reconnect within original deployment"})
	// The original deployment recovered successfully. A normal fresh release
	// supplies the next generation without inventing a failed attempt for retry.
	successor := f.plan()
	f.approve(successor)
	var generation int64
	recoveryWait(t, 5*time.Second, func() bool {
		if err := f.sql.QueryRow(t.Context(), `SELECT generation FROM loomplane_workload_attempts WHERE org_id=$1 AND environment_id=$2 AND service='web'`, f.org, f.environment).Scan(&generation); err != nil {
			t.Fatal(err)
		}
		return generation > currentGeneration
	})
	f.record("caller_loss_successor_generation", generation)
	if err := cloudinfra.NewPgStore(f.db).FinishWorkloadAttempt(t.Context(), attempt, true); err == nil {
		t.Fatal("stale completion settled the new generation")
	} else {
		f.record("caller_loss_stale_completion_refused", err.Error())
	}
	result = f.deployment(successor)
	if result.Status != "succeeded" {
		t.Fatalf("fresh public release failed: %+v", result)
	}
	current, isDirty = journal()
	successorHash := fmt.Sprintf("%x", sha256.Sum256([]byte(f.org+"/"+successor.ID+"/web")))
	if isDirty || current[successorHash] != generation {
		t.Fatal("successor failed to establish physical quiescence", current, isDirty)
	}
	if err := f.sql.QueryRow(t.Context(), `SELECT active,settled FROM loomplane_workload_attempts WHERE org_id=$1 AND plan_id=$2 AND service='web'`, f.org, successor.ID).Scan(&active, &settled); err != nil {
		t.Fatal(err)
	}
	if active || !settled || f.container() == recoveredContainer {
		t.Fatal("fresh release did not settle and publish its new container")
	}
	f.record("caller_loss_successor", map[string]any{"deployment": result.ID, "plan": successor.ID, "container": f.container(), "generation": generation, "settled": settled})
}

func (f *recoveryFixture) acceptRouteRace() {
	t := f.t
	route := f.routeReview()
	base := "/api/v1/projects/" + f.project
	var nextPool cloudinfra.Pool
	f.c.must(http.StatusOK, http.MethodPost, "/api/v1/clouds/"+f.cloud.ID+"/pools", cloudinfra.PoolInput{Name: "Reviewed pool", Members: []cloudinfra.PoolMember{{ServerID: f.servers["host2"], CPUMillis: 2000, MemoryBytes: 1 << 30}}}, &nextPool)
	change := cloudprofiles.ChangeInput{CloudID: f.cloud.ID, TargetID: f.target.ID, Revision: f.target.Revision, Provider: "server", Runtime: "server", PoolID: nextPool.ID}
	var preview cloudprofiles.Preview
	f.c.must(http.StatusOK, http.MethodPost, "/api/v1/clouds/changes/preview", change, &preview)
	if len(preview.Blockers) > 0 {
		t.Fatal("race preview blocked", preview.Blockers)
	}
	var beforeSnapshot cloudprofiles.Snapshot
	var raw []byte
	if err := f.sql.QueryRow(t.Context(), `SELECT snapshot FROM loomplane_cloud_previews WHERE id=$1`, preview.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &beforeSnapshot); err != nil {
		t.Fatal(err)
	}
	if !beforeSnapshot.Shared || len(beforeSnapshot.Environments) != 2 {
		t.Fatalf("expected both shared environments, got %d", len(beforeSnapshot.Environments))
	}
	found := false
	reviewedEnvironments := make(map[string]string, len(beforeSnapshot.Environments))
	for _, environment := range beforeSnapshot.Environments {
		reviewedEnvironments[environment.EnvironmentID] = environment.ProjectID
		if string(environment.Routes) != "[]" {
			t.Fatalf("environment %s already has routes", environment.EnvironmentID)
		}
		if environment.EnvironmentID == f.environment && environment.ProjectID == f.project {
			found = true
		}
	}
	if !found {
		t.Fatal("shared review omitted the routed environment")
	}
	directBefore := f.physical()
	barrier, err := pgx.Connect(t.Context(), f.sql.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Close(context.Background())
	tx, err := barrier.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	// The route owns organization admission before its zone read blocks here.
	// The test never holds that organization lock itself.
	if _, err = tx.Exec(t.Context(), `SELECT id FROM loomplane_dns_zones WHERE id=$1 FOR UPDATE`, route.Route.Input.ZoneID); err != nil {
		t.Fatal(err)
	}
	routeDone := make(chan recoveryResponse, 1)
	go func() {
		routeDone <- recoveryHTTP(f.c, http.MethodPost, base+"/route-operations/"+route.ID+"/apply", cloudinfra.ApplyInput{Review: route.Review})
	}()
	var routePID uint32
	recoveryWait(t, 10*time.Second, func() bool {
		err = f.sql.QueryRow(t.Context(), `SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%loomplane_dns_zones%'`, barrier.PgConn().PID()).Scan(&routePID)
		return err == nil
	})
	cloudDone := make(chan recoveryResponse, 1)
	go func() {
		cloudDone <- recoveryHTTP(f.c, http.MethodPost, "/api/v1/clouds/changes/apply", apiclient.ApplyCloudInput{PreviewID: preview.ID, Revision: preview.Revision, IdempotencyKey: "race-stale"})
	}()
	var acceptPID uint32
	recoveryWait(t, 10*time.Second, func() bool {
		err = f.sql.QueryRow(t.Context(), `SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%pg_advisory_xact_lock%'`, routePID).Scan(&acceptPID)
		return err == nil
	})
	f.record("accept_route_interleaving", map[string]any{"barrier_pid": barrier.PgConn().PID(), "route_pid_holding_org": routePID, "accept_pid_waiting_org": acceptPID, "preview": preview.ID, "route": route.Route.ID})
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	inserted, refused := <-routeDone, <-cloudDone
	if inserted.Err != nil || inserted.Status != http.StatusAccepted || refused.Err != nil || refused.Status != http.StatusConflict {
		t.Fatalf("race outcomes: route=%+v cloud=%+v", inserted, refused)
	}
	var count int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cloud_syncs WHERE preview_id=$1`, preview.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("losing Cloud acceptance persisted work", count, err)
	}
	var targetConnection string
	var revision int64
	if err = f.sql.QueryRow(t.Context(), `SELECT pool_id,revision FROM loomplane_cloud_targets WHERE id=$1`, f.target.ID).Scan(&targetConnection, &revision); err != nil || targetConnection != f.target.PoolID || revision != f.target.Revision {
		t.Fatal("losing acceptance changed placement", targetConnection, revision, err)
	}
	host2 := combinedSSH(t, f.state.Hosts["host2"], f.private)
	if combinedExec(t, host2, nil, "docker ps -aq --no-trunc --filter label=loomplane.project="+combinedQuote(f.project)) != f.container() {
		t.Fatal("losing Cloud acceptance mutated destination")
	}
	if f.physical() != directBefore {
		t.Fatal("losing Cloud acceptance changed the unrelated direct workload")
	}
	if err = json.Unmarshal([]byte(inserted.Body), route); err != nil {
		t.Fatal(err)
	}
	f.routeReady()
	// Fresh approval must capture the inserted route in its durable authority.
	// Stop at external-routing review; the main acceptance owns the cutover flow.
	f.c.must(http.StatusOK, http.MethodPost, "/api/v1/clouds/changes/preview", change, &preview)
	var sync cloudprofiles.Sync
	f.c.must(http.StatusAccepted, http.MethodPost, "/api/v1/clouds/changes/apply", apiclient.ApplyCloudInput{PreviewID: preview.ID, Revision: preview.Revision, IdempotencyKey: "race-fresh"}, &sync)
	if err = f.sql.QueryRow(t.Context(), `SELECT snapshot FROM loomplane_cloud_syncs WHERE id=$1`, sync.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var accepted cloudprofiles.Snapshot
	if err = json.Unmarshal(raw, &accepted); err != nil {
		t.Fatal(err)
	}
	if !accepted.Shared || len(accepted.Environments) != 2 {
		t.Fatalf("accepted snapshot has %d environments", len(accepted.Environments))
	}
	var routes []cloudinfra.Route
	for _, environment := range accepted.Environments {
		if reviewedEnvironments[environment.EnvironmentID] != environment.ProjectID {
			t.Fatalf("accepted snapshot changed environment %s ownership", environment.EnvironmentID)
		}
		delete(reviewedEnvironments, environment.EnvironmentID)
		if environment.EnvironmentID == f.environment && environment.ProjectID == f.project {
			if err = json.Unmarshal(environment.Routes, &routes); err != nil {
				t.Fatal(err)
			}
		} else if string(environment.Routes) != "[]" {
			t.Fatalf("unrelated environment %s acquired routes", environment.EnvironmentID)
		}
	}
	if len(reviewedEnvironments) != 0 {
		t.Fatal("accepted snapshot omitted a reviewed environment")
	}
	if len(routes) != 1 || routes[0].ID != route.Route.ID || routes[0].Revision != route.Route.Revision {
		t.Fatalf("accepted authority omitted inserted route: %+v", routes)
	}
	var result cloudprofiles.Sync
	recoveryWait(t, 70*time.Second, func() bool {
		f.c.must(http.StatusOK, http.MethodGet, "/api/v1/clouds/syncs/"+sync.ID, nil, &result)
		if result.Status == cloudprofiles.SyncFailed {
			t.Fatalf("fresh shared acceptance failed: %+v", result)
		}
		return result.Status == cloudprofiles.SyncRouting || result.Status == cloudprofiles.SyncSucceeded
	})
	f.record("accept_route_result", map[string]any{"loser": refused, "accepted_sync": sync.ID, "accepted_routes": routes, "status": result.Status})
}

func (f *recoveryFixture) crossOrganization() {
	t := f.t
	f.routeReady()
	stranger := &client{t: t, base: f.c.base, key: f.c.key}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	stranger.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "foreign-recovery@example.test", "password": "Correct-horse-battery-9"}, &auth)
	stranger.token = auth.SessionToken
	var org struct{ ID string }
	stranger.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Foreign organization", "slug": "foreign-recovery"}, &org)
	stranger.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var project struct{ ID string }
	stranger.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Foreign project", "repository_url": "https://github.com/acme/mono"}, &project)
	routes := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/clouds/" + f.cloud.ID, nil},
		{http.MethodGet, "/api/v1/clouds/" + f.cloud.ID + "/servers", nil},
		{http.MethodGet, "/api/v1/clouds/" + f.cloud.ID + "/servers/" + f.servers["host1"] + "/usage", nil},
		{http.MethodPost, "/api/v1/clouds/" + f.cloud.ID + "/servers/" + f.servers["host1"] + "/drain/preview", nil},
		{http.MethodGet, "/api/v1/projects/" + f.project + "/domain-routes", nil},
		{http.MethodGet, "/api/v1/projects/proj_00000000000000000000000000/domain-routes", nil},
		{http.MethodGet, "/api/v1/projects/" + f.project + "/route-operations/" + f.route.ID, nil},
		{http.MethodGet, "/api/v1/projects/" + project.ID + "/route-operations/" + f.route.ID, nil},
		{http.MethodPost, "/api/v1/projects/" + project.ID + "/route-operations/" + f.route.ID + "/apply", cloudinfra.ApplyInput{Review: f.route.Review}},
		{http.MethodGet, "/api/v1/projects/" + f.project + "/variables", nil},
		{http.MethodGet, "/api/v1/projects/" + f.project + "/bindings?environment=production", nil},
	}
	before := f.physical()
	host2 := combinedSSH(t, f.state.Hosts["host2"], f.private)
	routedPhysical := func() string {
		return combinedExec(t, host2, nil, "docker ps -aq --no-trunc | xargs -r docker inspect --format '{{.Id}} {{.State.Running}} {{.RestartCount}}' | sort")
	}
	routedBefore := routedPhysical()
	receipts := map[string]recoveryResponse{}
	for _, request := range routes {
		result := recoveryHTTP(stranger, request.method, request.path, request.body)
		// Tenant-scoped route lists return the same empty response for foreign
		// and nonexistent projects. Detail and mutation requests must be denied.
		if strings.HasSuffix(request.path, "/domain-routes") {
			if result.Err != nil || result.Status != http.StatusOK || strings.TrimSpace(result.Body) != `{"routes":[]}` {
				t.Errorf("cross-org list disclosed project existence: %+v", result)
			}
		} else if result.Err != nil || (result.Status != http.StatusForbidden && result.Status != http.StatusNotFound) {
			t.Errorf("cross-org request %s %s: %+v", request.method, request.path, result)
		}
		for _, secret := range []string{f.marker, f.route.Route.Input.Hostname, f.container(), "Recovery fixture", string(f.private)} {
			if secret != "" && strings.Contains(result.Body, secret) {
				t.Fatal("cross-org denial leaked private project data", request.path)
			}
		}
		receipts[request.method+" "+request.path] = result
	}
	if f.physical() != before || routedPhysical() != routedBefore {
		t.Fatal("denied cross-org requests mutated physical compute")
	}
	f.record("cross_organization_responses", receipts)
}
