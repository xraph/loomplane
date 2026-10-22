//go:build integration

package integration

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/database/migrations"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type acceptanceInputs struct{}

func (acceptanceInputs) CheckAppliedInputs(context.Context, string, string, *planning.ServiceConfig) error {
	return nil
}

func sharedAcceptanceFixture(t *testing.T) (*poolFixture, *planning.Service) {
	t.Helper()
	f := newPoolFixture(t)
	ctx := t.Context()
	next := &cloudinfra.Pool{ID: "next-pool", OrgID: "org", CloudID: "cloud", Name: "Next", Revision: 1, Members: []cloudinfra.PoolMember{{ServerID: "server-2", CPUMillis: 2000, MemoryBytes: 2 << 30}}}
	if err := f.store.SavePool(ctx, next, 0); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"env", "second-env"} {
		req := f.request(env, "api")
		req.StickyServerID = "server-1"
		placement, err := f.store.PreviewPlacement(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		id := "source-" + env
		pin := f.plan(t, id, req, placement)
		f.accept(t, id)
		if _, err = f.sql.Exec(ctx, `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES($1,'org',$2,$3,'production',$1,'reviewed','succeeded','usr',now())`, id, req.ProjectID, env); err != nil {
			t.Fatal(err)
		}
		cfg := planning.ServiceConfig{BuildTarget: &planning.BuildTarget{Image: "ghcr.io/test/api@sha256:" + strings.Repeat("a", 64)}, Image: "Dockerfile", Provider: "server", Target: placement.ConnectionID, TargetSettings: map[string]string{"docker_id": placement.ConnectionID, "architecture": "amd64"}, CloudPlacement: pin, Workload: provider.Worker, Execution: provider.Container, Service: "api", Repo: "app", CommitSHA: "reviewed", AppliedInputs: &planning.AppliedInputs{Variables: "retained-vars", ArtifactKey: "retained-artifact", Source: &planning.PlanSource{Alias: "app", CommitSHA: "reviewed", Repository: "owner/app"}}}
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if err = f.plans.PutBinding(ctx, &planning.Binding{ID: "binding-" + env, OrgID: "org", ProjectID: req.ProjectID, EnvironmentID: env, Subject: planning.ServiceSubject, Name: "api", Provider: "server", ConnectionID: placement.ConnectionID, ExternalID: "instance-" + env, Config: raw, PlanID: id, CommitSHA: "reviewed", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	svc := planning.NewService(planning.Deps{Store: f.plans, Projects: projects.NewPgStore(f.db), CloudProfiles: f.clouds, Connections: &syncAccounts{store: connections.NewPgStore(f.db)}, Providers: providers.Registry(), Authz: &syncAuth{}})
	svc.BindCloudSyncRunner(nil, acceptanceInputs{})
	return f, svc
}
func reviewPoolChange(t *testing.T, s *planning.Service) *cloudprofiles.Preview {
	t.Helper()
	p, err := s.PreviewCloudChange(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, "", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "pool-target", Revision: 1, PoolID: "next-pool"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Blockers) > 0 {
		t.Fatalf("preview blockers: %v", p.Blockers)
	}
	return p
}
func TestSharedSyncAcceptanceAtomicPlansAndReplay(t *testing.T) {
	f, svc := sharedAcceptanceFixture(t)
	p := reviewPoolChange(t, svc)
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	accepted, err := svc.ApplyCloudChange(t.Context(), actor, "", p.ID, p.Revision, "atomic")
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.ApplyCloudChange(t.Context(), actor, "", p.ID, p.Revision, "lost-response")
	if err != nil || again.ID != accepted.ID {
		t.Fatalf("replay: %+v %v", again, err)
	}
	var plans, reservations int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_plans WHERE result->>'cloud_sync_id'=$1`, accepted.ID).Scan(&plans); err != nil {
		t.Fatal(err)
	}
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_capacity_reservations WHERE plan_id LIKE $1`, accepted.ID+".%").Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if plans != 2 || reservations != 2 {
		t.Fatalf("plans=%d reservations=%d", plans, reservations)
	}
	target, err := f.clouds.GetTarget(t.Context(), "org", "", "cloud", "pool-target")
	if err != nil || target.ID != "pool-target" || target.Revision != 2 || target.PoolID != "next-pool" {
		t.Fatalf("target: %+v %v", target, err)
	}
	_, snapshot, err := f.clouds.GetSync(t.Context(), "org", "", accepted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_syncs SET snapshot=jsonb_set(snapshot,'{target_revisions}','{}') WHERE id=$1`, accepted.ID); err == nil {
		t.Fatal("accepted authority snapshot was mutable")
	}
	for _, env := range snapshot.Environments {
		if env.AcceptedPlan == nil {
			continue
		}
		saved, err := f.plans.GetPlan(t.Context(), "org", env.AcceptedPlan.ID)
		if err != nil {
			t.Fatal(err)
		}
		if saved.ProjectID != env.ProjectID || saved.EnvironmentID != env.EnvironmentID {
			t.Fatalf("participant identity: %+v", saved)
		}
		if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET revision='fabricated' WHERE id=$1`, saved.ID); err == nil {
			t.Fatal("accepted plan revision was mutable")
		}
		for _, a := range saved.Actions {
			if a.Service != nil && (a.Service.CommitSHA != "reviewed" || a.Service.AppliedInputs == nil || a.Service.AppliedInputs.Variables != "retained-vars" || a.Service.AppliedInputs.ArtifactKey != "retained-artifact") {
				t.Fatal("accepted source inputs changed")
			}
			if a.Service != nil && a.Service.CloudPlacement != nil {
				if err = f.store.ValidatePlacement(t.Context(), "org", env.ProjectID, env.EnvironmentID, saved.ID, a.Name, a.Service.CloudPlacement.PoolPlacement()); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
func TestSharedSyncAcceptanceCapacityRollsBackAllParticipants(t *testing.T) {
	f, svc := sharedAcceptanceFixture(t)
	p := reviewPoolChange(t, svc)
	req := f.request("stage", "competitor")
	req.StickyServerID = "server-2"
	placement, err := f.store.PreviewPlacement(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	f.plan(t, "competitor", req, placement)
	f.accept(t, "competitor")
	_, err = svc.ApplyCloudChange(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, "", p.ID, p.Revision, "conflict")
	if err == nil {
		t.Fatal("over-capacity acceptance succeeded")
	}
	var count int
	for _, q := range []string{`SELECT count(*) FROM loomplane_cloud_syncs`, `SELECT count(*) FROM loomplane_environment_sync_guards`, `SELECT count(*) FROM loomplane_plans WHERE coalesce(result->>'cloud_sync_id','')<>''`, `SELECT count(*) FROM loomplane_capacity_reservations WHERE plan_id NOT IN ('source-env','source-second-env','competitor')`} {
		if err = f.sql.QueryRow(t.Context(), q).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rollback count=%d error=%v query=%s", count, err, q)
		}
	}
	target, err := f.clouds.GetTarget(t.Context(), "org", "", "cloud", "pool-target")
	if err != nil || target.Revision != 1 || target.PoolID != "pool" {
		t.Fatalf("desired state escaped rollback: %+v %v", target, err)
	}
}
func TestSharedSyncAcceptanceRejectsFabricatedAuthority(t *testing.T) {
	f, svc := sharedAcceptanceFixture(t)
	p := reviewPoolChange(t, svc)
	_, snap, err := f.clouds.GetPreview(t.Context(), "org", "", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	snap.Environments[0].ProjectID = "second"
	now := time.Now().UTC()
	_, err = f.clouds.Accept(t.Context(), p, snap, &cloudprofiles.Sync{ID: "fabricated", OrgID: "org", PreviewID: p.ID, CloudID: "cloud", TargetID: "pool-target", Status: cloudprofiles.SyncPending, CreatedAt: now, UpdatedAt: now}, "fabricated")
	if err == nil {
		t.Fatal("fabricated participant accepted")
	}
	req := f.request("env", "api")
	req.StickyServerID = "server-2"
	placement, err := f.store.PreviewPlacement(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	f.plan(t, "bare-sync-id", req, placement)
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(result,'{cloud_sync_id}','"fabricated"') WHERE id='bare-sync-id'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plans.Decide(t.Context(), "org", "bare-sync-id", "approved", "usr", "", now); err == nil || !strings.Contains(err.Error(), "exact immutable") {
		t.Fatalf("bare sync id admitted: %v", err)
	}
}
func TestSharedSyncPreviewRejectsMissingPoolWithoutWorkloads(t *testing.T) {
	f := newPoolFixture(t)
	svc := planning.NewService(planning.Deps{Store: f.plans, Projects: projects.NewPgStore(f.db), CloudProfiles: f.clouds, Connections: &syncAccounts{store: connections.NewPgStore(f.db)}, Providers: providers.Registry(), Authz: &syncAuth{}})
	p, err := svc.PreviewCloudChange(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, "", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "pool-target", Revision: 1, PoolID: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Blockers) == 0 {
		t.Fatal("missing pool accepted for empty environments")
	}
}

func TestSharedSyncConcurrentOrdinaryApproval(t *testing.T) {
	f, svc := sharedAcceptanceFixture(t)
	p := reviewPoolChange(t, svc)
	req := f.request("stage", "competitor")
	req.StickyServerID = "server-2"
	placement, err := f.store.PreviewPlacement(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	f.plan(t, "ordinary-concurrent", req, placement)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, e := f.plans.Decide(ctx, "org", "ordinary-concurrent", "approved", "usr", "", time.Now())
		results <- e
	}()
	go func() {
		<-start
		_, e := svc.ApplyCloudChange(ctx, security.Principal{OrgID: "org", UserID: "usr"}, "", p.ID, p.Revision, "concurrent")
		results <- e
	}()
	close(start)
	success := 0
	for range 2 {
		select {
		case e := <-results:
			if e == nil {
				success++
			}
		case <-ctx.Done():
			t.Fatal("approval and shared acceptance did not finish within lock bound")
		}
	}
	if success != 1 {
		t.Fatalf("competing acceptances succeeded=%d", success)
	}
	var cpu int64
	if err = f.sql.QueryRow(t.Context(), `SELECT cpu FROM loomplane_server_capacity('server-2')`).Scan(&cpu); err != nil || cpu > 2000 {
		t.Fatalf("capacity=%d error=%v", cpu, err)
	}
}

func TestSharedSyncAdmissionRejectsMalformedAllocations(t *testing.T) {
	for _, allocation := range []string{`123`, `null`, `{}`, `[]`, `" invalid "`} {
		t.Run(allocation, func(t *testing.T) {
			f := newPoolFixture(t)
			req := f.request("env", "api")
			placement, err := f.store.PreviewPlacement(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			f.plan(t, "malformed", req, placement)
			if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions,0,service,cloud_placement,allocation}',$1::jsonb) WHERE id='malformed'`, allocation); err != nil {
				t.Fatal(err)
			}
			if _, err = f.plans.Decide(t.Context(), "org", "malformed", "approved", "usr", "", time.Now()); err == nil {
				t.Fatal("malformed allocation reached admission")
			}
			var count int
			if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_capacity_reservations`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("reservation escaped rejection: %d %v", count, err)
			}
		})
	}
}

func TestSharedSyncAdmissionRejectsSourceAllocationMismatch(t *testing.T) {
	for _, candidate := range []string{"", "source-allocation", "candidate-allocation"} {
		t.Run(candidate, func(t *testing.T) {
			f, _ := sharedAcceptanceFixture(t)
			// Pin a source allocation that has no matching retained reservation.
			if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_bindings SET config=jsonb_set(config,'{cloud_placement,allocation}','"source-allocation"') WHERE id='binding-env'`); err != nil {
				t.Fatal(err)
			}
			req := f.request("env", "api")
			req.StickyServerID = "server-2"
			req.Allocation = candidate
			placement, err := f.store.PreviewPlacement(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			f.plan(t, "invalid-source", req, placement)
			if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions,0,routes}','[{"id":"reviewed-route"}]') WHERE id='invalid-source'`); err != nil {
				t.Fatal(err)
			}
			if _, err = f.plans.Decide(t.Context(), "org", "invalid-source", "approved", "usr", "", time.Now()); err == nil || !strings.Contains(err.Error(), "exact source") {
				t.Fatalf("mismatching source/candidate admitted: %v", err)
			}
		})
	}
}

// These two trigger definitions are from the pre-allocation schema at 52baa1d.
//
//go:embed testdata/pre_allocation_pool_functions.sql
var preAllocationPoolFunctions string

func applySyncUpgrade(t *testing.T, f *poolFixture) {
	t.Helper()
	for _, migration := range migrations.Group().Migrations() {
		if migration.Name == "routed_releases" || migration.Name == "shared_sync_admission" || migration.Name == "shared_sync_capacity_ledger" {
			if err := migration.Up(t.Context(), pgmigrate.New(f.db)); err != nil {
				t.Fatal(err)
			}
		}
	}
}
func TestSharedSyncForwardUpgradeRetainsLegacyPlansAndBindings(t *testing.T) {
	f := newPoolFixture(t)
	ctx := t.Context()
	if _, err := f.sql.Exec(ctx, preAllocationPoolFunctions); err != nil {
		t.Fatal(err)
	}
	req := f.request("env", "api")
	req.StickyServerID = "server-1"
	placement, err := f.store.PreviewPlacement(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	pin := f.plan(t, "legacy-source", req, placement)
	f.accept(t, "legacy-source")
	raw, _ := json.Marshal(planning.ServiceConfig{Provider: "server", CloudPlacement: pin})
	source := planning.Binding{ID: "legacy-binding", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "api", Provider: "server", ConnectionID: placement.ConnectionID, ExternalID: "legacy-source", Config: raw, PlanID: "legacy-source", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = f.plans.PutBinding(ctx, &source); err != nil {
		t.Fatal(err)
	}
	f.plan(t, "legacy-candidate", req, placement)
	f.accept(t, "legacy-candidate")
	current := source
	current.PlanID = "legacy-candidate"
	current.ExternalID = "legacy-candidate"
	current.Replaced = []planning.Binding{source}
	if err = f.plans.PutBinding(ctx, &current); err != nil {
		t.Fatal(err)
	}
	var before string
	if err = f.sql.QueryRow(ctx, `SELECT jsonb_agg(result ORDER BY id)::text FROM loomplane_plans`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(ctx, `DROP FUNCTION loomplane_server_capacity(TEXT,TEXT,JSONB)`); err != nil {
		t.Fatal(err)
	}
	applySyncUpgrade(t, f)
	var after string
	var cpu int64
	if err = f.sql.QueryRow(ctx, `SELECT jsonb_agg(result ORDER BY id)::text FROM loomplane_plans`).Scan(&after); err != nil || before != after {
		t.Fatalf("upgrade rewrote accepted legacy plans: %v", err)
	}
	if err = f.sql.QueryRow(ctx, `SELECT cpu FROM loomplane_server_capacity('server-1')`).Scan(&cpu); err != nil || cpu != 1000 {
		t.Fatalf("legacy accounting: %d %v", cpu, err)
	}
	if err = f.plans.SetReplaced(ctx, "org", current.ID, nil); err != nil {
		t.Fatal(err)
	}
	var sourceCount, candidateCount int
	if err = f.sql.QueryRow(ctx, `SELECT count(*) FILTER(WHERE plan_id='legacy-source'),count(*) FILTER(WHERE plan_id='legacy-candidate') FROM loomplane_capacity_reservations`).Scan(&sourceCount, &candidateCount); err != nil || sourceCount != 0 || candidateCount != 1 {
		t.Fatalf("legacy exact cleanup: %d %d %v", sourceCount, candidateCount, err)
	}
	if err = f.store.ValidatePlacement(ctx, "org", "proj", "env", "legacy-candidate", "api", *placement); err != nil {
		t.Fatal(err)
	}
}

type acceptanceRunner struct{ calls int }

func (r *acceptanceRunner) ReconcileCloudPlan(context.Context, string, string, func([]byte) error) (string, error) {
	r.calls++
	return "", errors.New("acceptance test does not execute runtimes")
}
func (r *acceptanceRunner) CleanupCloudPlan(context.Context, string, string, func([]byte) error) error {
	r.calls++
	return errors.New("unexpected cleanup")
}
func (r *acceptanceRunner) CleanupRevertedCloudPlan(context.Context, string, string, string, func([]byte) error) error {
	r.calls++
	return errors.New("unexpected compensation")
}

func TestSharedSyncLegacyMissingPlanCanAcceptReviewedRevert(t *testing.T) {
	f, svc := sharedAcceptanceFixture(t)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	p := reviewPoolChange(t, svc)
	accepted, err := svc.ApplyCloudChange(ctx, actor, "", p.ID, p.Revision, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	// Recreate a pre-upgrade acceptance that committed desired state and snapshots
	// before materializing any participant plan. No runtime has started.
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
	applySyncUpgrade(t, f)
	runner := &acceptanceRunner{}
	svc.BindCloudSyncRunner(runner, acceptanceInputs{})
	if err = svc.ReconcileCloudSync(ctx, "org", "", accepted.ID, func([]byte) error { return nil }); err == nil || !strings.Contains(err.Error(), "review and accept a revert") {
		t.Fatalf("legacy recovery: %v", err)
	}
	failed, retained, err := f.clouds.GetSync(ctx, "org", "", accepted.ID)
	if err != nil || failed.Status != cloudprofiles.SyncFailed || runner.calls != 0 {
		t.Fatalf("legacy fail closed: %+v calls=%d %v", failed, runner.calls, err)
	}
	for _, env := range retained.Environments {
		raw, readErr := f.clouds.SnapshotBindings(ctx, "org", env.EnvironmentID)
		if readErr != nil || string(raw) != string(env.Bindings) {
			t.Fatalf("source snapshot changed: %v", readErr)
		}
	}
	revert, err := svc.PreviewCloudRevert(ctx, actor, "", accepted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revert.Blockers) > 0 || revert.After.PoolID != "pool" || revert.After.Runtime != "server" {
		t.Fatalf("revert: %+v", revert)
	}
	recovery, err := svc.ApplyCloudRevert(ctx, actor, "", revert.ID, revert.Revision, "recover-legacy")
	if err != nil {
		t.Fatal(err)
	}
	var guards int
	if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_environment_sync_guards WHERE sync_id=$1`, recovery.ID).Scan(&guards); err != nil || guards != 3 {
		t.Fatalf("new review did not replace guards: %d %v", guards, err)
	}
	failed, _, err = f.clouds.GetSync(ctx, "org", "", accepted.ID)
	if err != nil || failed.Status != cloudprofiles.SyncReverting {
		t.Fatalf("legacy supersession: %+v %v", failed, err)
	}
	if runner.calls != 0 {
		t.Fatalf("acceptance caused %d runtime effects", runner.calls)
	}
	// The existing runner can settle this revert through metadata adoption only.
	// No provider executor is installed, so any runtime request fails the test.
	recoveryRunner := deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(f.db), Plans: f.plans, Credentials: &syncAccounts{store: connections.NewPgStore(f.db)}, Providers: providers.Registry(), CloudProfiles: f.clouds, CloudSyncAuth: svc})
	svc.BindCloudSyncRunner(recoveryRunner, acceptanceInputs{})
	if err = svc.ReconcileCloudSync(ctx, "org", "", recovery.ID, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	settled, _, err := f.clouds.GetSync(ctx, "org", "", recovery.ID)
	if err != nil || settled.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("metadata recovery: %+v %v", settled, err)
	}
	next, err := svc.PreviewCloudChange(ctx, actor, "", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "pool-target", Revision: 3, PoolID: "next-pool"})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Blockers) > 0 {
		t.Fatalf("new review remained blocked: %v", next.Blockers)
	}
	if _, err = svc.ApplyCloudChange(ctx, actor, "", next.ID, next.Revision, "after-recovery"); err != nil {
		t.Fatalf("new acceptance remained blocked: %v", err)
	}
}

func TestSharedSyncRejectsRouteChangedAfterReview(t *testing.T) {
	f, svc := sharedAcceptanceFixture(t)
	p := reviewPoolChange(t, svc)
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_dns_zones(id,org_id,name,revision,body) VALUES('zone','org','example.test',1,'{}')`); err != nil {
		t.Fatal(err)
	}
	_, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_domain_routes(id,org_id,project_id,environment_id,cloud_id,server_id,connection_id,zone_id,hostname,status,revision,body) VALUES('new-route','org','proj','env','cloud','server-1','runtime-1','zone','new.example.test','ready',1,'{"id":"new-route"}')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ApplyCloudChange(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, "", p.ID, p.Revision, "route-changed")
	if !errors.Is(err, cloudprofiles.ErrConflict) {
		t.Fatalf("changed routes accepted: %v", err)
	}
	target, err := f.clouds.GetTarget(t.Context(), "org", "", "cloud", "pool-target")
	if err != nil || target.Revision != 1 || target.PoolID != "pool" {
		t.Fatalf("target changed: %+v %v", target, err)
	}
}

func TestSharedSyncDistributesIdenticalServicesAcrossParticipants(t *testing.T) {
	f, svc := sharedAcceptanceFixture(t)
	ctx := t.Context()
	server, err := f.store.GetServer(ctx, "org", "cloud", "server-2")
	if err != nil {
		t.Fatal(err)
	}
	cs := connections.NewPgStore(f.db)
	now := time.Now().UTC()
	for _, suffix := range []string{"3", "4"} {
		server.ID = "server-" + suffix
		id := "runtime-" + suffix
		server.ConnectionID = id
		server.RuntimeConnectionID = id
		server.AccountID = id
		server.Health.DockerID = id
		if err = cs.CreateConnection(ctx, &connections.Connection{ID: id, OrgID: "org", Cloud: "server", Name: id, Account: id, Details: map[string]string{"docker_id": id, "architecture": "amd64"}, Status: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err = f.store.SaveServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	next, err := f.store.GetPool(ctx, "org", "cloud", "next-pool")
	if err != nil {
		t.Fatal(err)
	}
	next.Revision++
	next.Members = []cloudinfra.PoolMember{{ServerID: "server-3", CPUMillis: 1000, MemoryBytes: 1 << 30}, {ServerID: "server-4", CPUMillis: 1000, MemoryBytes: 1 << 30}}
	if err = f.store.SavePool(ctx, next, 1); err != nil {
		t.Fatal(err)
	}
	preview := reviewPoolChange(t, svc)
	sync, err := svc.ApplyCloudChange(ctx, security.Principal{OrgID: "org", UserID: "usr"}, "", preview.ID, preview.Revision, "distributed")
	if err != nil {
		t.Fatal(err)
	}
	var hosts, projects, services int
	if err = f.sql.QueryRow(ctx, `SELECT count(DISTINCT server_id),count(DISTINCT project_id),count(DISTINCT service) FROM loomplane_capacity_reservations WHERE plan_id LIKE $1`, sync.ID+".%").Scan(&hosts, &projects, &services); err != nil {
		t.Fatal(err)
	}
	if hosts != 2 || projects != 2 || services != 1 {
		t.Fatalf("hosts=%d projects=%d service names=%d", hosts, projects, services)
	}
}

func TestSharedSyncCapacityLedgerForwardUpgrade(t *testing.T) {
	f := newPoolFixture(t)
	ctx := t.Context()
	// Install the previous capacity function so only the new forward migration
	// can add participant identity to hypothetical demand.
	if _, err := f.sql.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_server_capacity(host_id TEXT, extra_environment TEXT DEFAULT '', extra JSONB DEFAULT '[]') RETURNS TABLE(cpu BIGINT,memory BIGINT) LANGUAGE SQL STABLE AS $$
 SELECT coalesce(sum(c),0)::bigint,coalesce(sum(m),0)::bigint FROM (
 SELECT max(cpu_millis) c,max(memory_bytes) m FROM (
 SELECT environment_id,service,coalesce(body->>'allocation','') allocation,cpu_millis,memory_bytes FROM loomplane_capacity_reservations WHERE server_id=host_id
 UNION ALL SELECT extra_environment,p->>'service',coalesce(p->>'allocation',''),(p->>'cpu_millis')::bigint,(p->>'memory_bytes')::bigint FROM jsonb_array_elements(extra) p WHERE p->>'server_id'=host_id
 ) demands GROUP BY environment_id,service,allocation) allocated $$`); err != nil {
		t.Fatal(err)
	}
	ledger := `[{"org_id":"org","project_id":"proj","environment_id":"env","service":"api","server_id":"server-1","cpu_millis":1000,"memory_bytes":536870912},{"org_id":"org","project_id":"second-project","environment_id":"second-env","service":"api","server_id":"server-1","cpu_millis":1000,"memory_bytes":536870912}]`
	var cpu int64
	if err := f.sql.QueryRow(ctx, `SELECT cpu FROM loomplane_server_capacity('server-1','env',$1::jsonb)`, ledger).Scan(&cpu); err != nil || cpu != 1000 {
		t.Fatalf("legacy CPU=%d: %v", cpu, err)
	}
	for _, migration := range migrations.Group().Migrations() {
		if migration.Name == "shared_sync_capacity_ledger" {
			if err := migration.Up(ctx, pgmigrate.New(f.db)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := f.sql.QueryRow(ctx, `SELECT cpu FROM loomplane_server_capacity('server-1','env',$1::jsonb)`, ledger).Scan(&cpu); err != nil || cpu != 2000 {
		t.Fatalf("upgraded CPU=%d: %v", cpu, err)
	}
	req := f.request("env", "api")
	req.StickyServerID = "server-1"
	placed, err := f.store.PreviewPlacement(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	f.plan(t, "legacy-ledger", req, placed)
	f.accept(t, "legacy-ledger")
	// A legacy caller omits scope fields and must still share the existing
	// allocation's maximum, rather than counting a second workload.
	if err = f.sql.QueryRow(ctx, `SELECT cpu FROM loomplane_server_capacity('server-1','env','[{"service":"api","server_id":"server-1","cpu_millis":1000,"memory_bytes":536870912}]')`).Scan(&cpu); err != nil || cpu != 1000 {
		t.Fatalf("legacy caller CPU=%d: %v", cpu, err)
	}
}
