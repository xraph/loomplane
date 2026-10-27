//go:build integration

package integration

import (
	"errors"
	"strings"
	"testing"
	"time"

	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/security"
)

func routeAttemptFixture(t *testing.T) (*poolFixture, *cloudinfra.RouteOperation) {
	t.Helper()
	f := newPoolFixture(t)
	ctx := t.Context()
	for _, q := range []string{
		`CREATE TABLE cp_instances(id TEXT PRIMARY KEY,tenant_id TEXT,current_release TEXT,service_refs JSONB,services JSONB,labels JSONB,state TEXT)`,
		`CREATE TABLE cp_deployments(id TEXT PRIMARY KEY,tenant_id TEXT,instance_id TEXT,release_id TEXT,state TEXT,created_at TIMESTAMPTZ DEFAULT now())`,
		`INSERT INTO cp_instances VALUES('instance','org',NULL,'{"main":"container"}','[{"name":"main","ports":[{"container":8080}]}]','{"loomplane.revision":"deployment/action"}','running')`,
		`UPDATE loomplane_cloud_connections SET details=details||'{"host":"192.0.2.42"}'::jsonb WHERE id='runtime-1'`,
		`UPDATE loomplane_cloud_targets SET provider='server',connection_id='runtime-1',revision=revision+1 WHERE id='target'`,
		`INSERT INTO loomplane_bindings(id,org_id,project_id,environment_id,subject,name,provider,engine,external_id,connection_id,config,created_at,updated_at) VALUES('binding','org','proj','env','service','web','server','http','instance','runtime-1','{"cloud_placement":{"cloud_id":"cloud","target_id":"target","server_id":"server-1"},"port":8080}',now(),now())`,
	} {
		if _, err := f.sql.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	resolver := &domainDNS{}
	svc := cloudinfra.NewService(cloudinfra.Deps{Store: f.store, Clouds: f.clouds, Authz: domainAuth{}, Resolver: resolver, Workflow: &domainWorkflow{}})
	p := security.Principal{OrgID: "org", UserID: "manager"}
	zone, err := svc.CreateZone(ctx, p, cloudinfra.ZoneInput{Name: "example.com", Mode: "external", ProjectIDs: []string{"proj"}})
	if err != nil {
		t.Fatal(err)
	}
	resolver.proof = zone.ProofValue
	if _, err = svc.VerifyZone(ctx, p, zone.ID); err != nil {
		t.Fatal(err)
	}
	op, err := svc.PreviewRoute(ctx, p, "proj", cloudinfra.RouteInput{ZoneID: zone.ID, CloudID: "cloud", EnvironmentID: "env", Service: "web", Hostname: "app.example.com", Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	return f, op
}

// approvedWorkloadPlan saves an approved plan with no actions. Effects
// only start under an approved plan, which they check for frontend demand.
func approvedWorkloadPlan(t *testing.T, f *poolFixture, id string) {
	t.Helper()
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at) VALUES($1,'org','proj','env','production','ready','review','pool-manifest-proj',1,'{"executable":true,"connections":{},"actions":[]}',true,'usr',now())`, id); err != nil {
		t.Fatal(err)
	}
	f.accept(t, id)
}

func TestRouteWorkloadAttachmentSurvivesRestart(t *testing.T) {
	f, op := routeAttemptFixture(t)
	ctx := t.Context()
	approvedWorkloadPlan(t, f, "direct")
	in := cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "web", Plan: "direct", Deployment: "deployment"}
	first, err := f.store.BeginWorkloadAttempt(ctx, in, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.AcceptRoute(ctx, op); !errors.Is(err, cloudinfra.ErrBusy) {
		t.Fatal("attachment passed active direct attempt", err)
	}
	if err = f.store.MarkWorkloadEffects(ctx, *first); err != nil {
		t.Fatal(err)
	}
	reopened := cloudinfra.NewPgStore(f.db)
	next, err := reopened.BeginWorkloadAttempt(ctx, in, nil, false)
	if err != nil || next.Generation != first.Generation+1 {
		t.Fatal("restart did not advance attempt", next, err)
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = cloudinfra.GuardPoolWrite(first.Context(ctx), tx); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("stale direct attempt may publish", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = reopened.FinishWorkloadAttempt(ctx, *first, true); err == nil {
		t.Fatal("old attempt settled successor")
	}
	if err = reopened.FinishWorkloadAttempt(ctx, *next, false); err != nil {
		t.Fatal(err)
	}
	if err = f.store.AcceptRoute(ctx, op); !errors.Is(err, cloudinfra.ErrBusy) {
		t.Fatal("attachment passed unresolved failed attempt", err)
	}
	foreign := in
	foreign.Project = "second"
	foreign.Environment = "second-env"
	other, err := reopened.BeginWorkloadAttempt(ctx, foreign, nil, false)
	if err != nil {
		t.Fatal("shared service name leaked across projects", err)
	}
	if err = reopened.FinishWorkloadAttempt(ctx, *other, true); err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.BeginWorkloadAttempt(ctx, in, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.FinishWorkloadAttempt(ctx, *recovered, true); err != nil {
		t.Fatal(err)
	}
	if err = f.store.AcceptRoute(ctx, op); err != nil {
		t.Fatal("quiescent direct runtime refused route", err)
	}
	in.Plan = "new-plan"
	in.Deployment = "new-deployment"
	if _, err = reopened.BeginWorkloadAttempt(ctx, in, nil, false); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("new route was not checked before effects", err)
	}
	if err = cloudinfra.NewService(cloudinfra.Deps{Store: f.store}).ValidateRuntimeRemoval(ctx, "org", "proj", "instance"); err == nil {
		t.Fatal("active route allowed runtime deletion")
	}
}

func TestRouteWorkloadAttachmentWaitsForOrganizationAcceptance(t *testing.T) {
	f, op := routeAttemptFixture(t)
	ctx := t.Context()
	// Shared acceptance holds this lock through comparison and commit, including
	// when the accepted route set is empty. Pause at that exact empty-set boundary.
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('cloud-organization/org',0))`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM loomplane_domain_routes`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	done := make(chan error, 1)
	go func() { done <- f.store.AcceptRoute(ctx, op) }()
	select {
	case err = <-done:
		t.Fatal("new route inserted after comparison before commit", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("attachment did not resume after commit")
	}
	// A plan accepted with that empty snapshot is still rejected at execution,
	// before a provider call, if attachment wins after acceptance commits.
	_, err = f.store.BeginWorkloadAttempt(ctx, cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "web", Plan: "accepted", Deployment: "deployment"}, nil, false)
	if !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("accepted empty route set was not invalidated", err)
	}
}

func assertBusyRouteAttachment(t *testing.T, f *poolFixture, service string) {
	t.Helper()
	op := &cloudinfra.RouteOperation{Kind: "apply", Route: cloudinfra.Route{OrgID: "org", ProjectID: "proj", Input: cloudinfra.RouteInput{EnvironmentID: "env", Service: service}}}
	if err := f.store.AcceptRoute(t.Context(), op); !errors.Is(err, cloudinfra.ErrBusy) {
		t.Fatal("route attachment passed live workload", err)
	}
}

func TestRouteWorkloadUpgradeRefusesUnfencedLegacyAttempt(t *testing.T) {
	f, op := routeAttemptFixture(t)
	ctx := t.Context()
	for _, q := range []string{
		`INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at) VALUES('legacy-direct','org','proj','env','production','ready','review','pool-manifest-proj',1,'{"actions":[{"id":"service:web","name":"web","provider":"server","op":"update"}]}',true,'usr',now())`,
		`INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES('legacy-worker','org','proj','env','production','legacy-direct','reviewed','running','usr',now())`,
		`DROP TABLE loomplane_workload_attempts`,
	} {
		if _, err := f.sql.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	migration := federationMigration(t, "20261028000300")
	if err := migration.Up(ctx, pgmigrate.New(f.db)); err != nil {
		t.Fatal("upgrade", err)
	}
	if err := f.store.AcceptRoute(ctx, op); !errors.Is(err, cloudinfra.ErrBusy) {
		t.Fatal("legacy in-flight runtime accepted route", err)
	}
	if err := federationMigration(t, "20261028000400").Up(ctx, pgmigrate.New(f.db)); err != nil {
		t.Fatal(err)
	}
	_, err := f.store.BeginWorkloadAttempt(ctx, cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "web", Plan: "legacy-direct", Deployment: "legacy-worker"}, nil, false)
	if err == nil || !strings.Contains(err.Error(), "pre-upgrade unfenced worker") {
		t.Fatal("unfenced legacy worker treated as drained", err)
	}
	// A settled legacy binding needs no backfilled active guard.
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_deployments SET status='succeeded' WHERE id='legacy-worker'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(ctx, `DROP TABLE loomplane_workload_attempts`); err != nil {
		t.Fatal(err)
	}
	if err = migration.Up(ctx, pgmigrate.New(f.db)); err != nil {
		t.Fatal(err)
	}
	if err = f.store.AcceptRoute(ctx, op); err != nil {
		t.Fatal("settled legacy binding cannot be routed", err)
	}
}

func TestRouteWorkloadUpgradeAllowsSupersededFailure(t *testing.T) {
	for _, state := range []string{"failed", "ambiguous", "running", "retained", "unowned"} {
		t.Run(state, func(t *testing.T) {
			f, op := routeAttemptFixture(t)
			ctx := t.Context()
			for _, q := range []string{
				`INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at) VALUES('old-plan','org','proj','env','production','applied','review','pool-manifest-proj',1,'{"actions":[{"id":"service:web","name":"web","provider":"server","op":"update"}]}',true,'usr',now()),('settled-plan','org','proj','env','production','applied','review','pool-manifest-proj',1,'{"actions":[]}',true,'usr',now())`,
				`INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at,finished_at) VALUES('old-worker','org','proj','env','production','old-plan','reviewed','failed','usr',now()-interval '3 hours',now()-interval '2 hours'),('settled-worker','org','proj','env','production','settled-plan','reviewed','succeeded','usr',now()-interval '1 hour',now())`,
				`UPDATE loomplane_bindings SET plan_id='settled-plan' WHERE id='binding'`,
				`UPDATE loomplane_deployments SET steps=jsonb_build_array(jsonb_build_object('action_id','service:web','status','succeeded','finished_at',finished_at)) WHERE id='old-worker'`,
			} {
				if _, err := f.sql.Exec(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			switch state {
			case "ambiguous":
				if _, err := f.sql.Exec(ctx, `UPDATE loomplane_deployments SET steps='[ {"action_id":"service:web","status":"failed"} ]' WHERE id='old-worker'`); err != nil {
					t.Fatal(err)
				}
			case "running":
				if _, err := f.sql.Exec(ctx, `UPDATE loomplane_deployments SET status='running',finished_at=NULL WHERE id='old-worker'`); err != nil {
					t.Fatal(err)
				}
			case "retained":
				if _, err := f.sql.Exec(ctx, `UPDATE loomplane_bindings SET replaced='[{"plan_id":"old-plan"}]' WHERE id='binding'`); err != nil {
					t.Fatal(err)
				}
			case "unowned":
				if _, err := f.sql.Exec(ctx, `UPDATE loomplane_bindings SET plan_id='old-plan' WHERE id='binding'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.sql.Exec(ctx, `DROP TABLE loomplane_workload_attempts`); err != nil {
				t.Fatal(err)
			}
			if err := federationMigration(t, "20261028000300").Up(ctx, pgmigrate.New(f.db)); err != nil {
				t.Fatal(err)
			}
			err := f.store.AcceptRoute(ctx, op)
			if state == "failed" {
				if err != nil {
					t.Fatal("settled successor stranded by historical failure", err)
				}
			} else if !errors.Is(err, cloudinfra.ErrBusy) {
				t.Fatal("ambiguous legacy attempt allowed attachment", err)
			}
		})
	}
}
