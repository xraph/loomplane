//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/pkg/provider"
)

// frontendRows counts the frontend capacity rows left for the project.
func frontendRows(t *testing.T, f *frontendFixture) (slots, uses, intents int) {
	t.Helper()
	ctx := t.Context()
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM loomplane_frontend_slots WHERE project_id='proj'`).Scan(&slots); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM loomplane_frontend_uses u JOIN loomplane_frontend_slots s ON s.org_id=u.org_id AND s.cloud_id=u.cloud_id AND s.id=u.slot_id WHERE s.project_id='proj'`).Scan(&uses); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM loomplane_frontend_intents WHERE plan_id IN (SELECT id FROM loomplane_plans WHERE project_id='proj')`).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	return slots, uses, intents
}

// releaseByAbsence binds the reserved frontend to project-x and records
// that Vercel no longer has it, which releases its slot.
func releaseByAbsence(t *testing.T, f *frontendFixture, d cloudinfra.FrontendDemand) {
	t.Helper()
	ctx := t.Context()
	cfg, _ := json.Marshal(planning.ServiceConfig{Provider: "vercel", Workload: provider.WebApp, Execution: provider.Serverless, FrontendDemand: &d})
	b := &planning.Binding{ID: "binding-" + d.Service, OrgID: "org", ProjectID: "proj", EnvironmentID: d.EnvironmentID, Subject: planning.ServiceSubject, Name: d.Service, Provider: "vercel", ConnectionID: d.ConnectionID, ExternalID: "project-x", PlanID: d.PlanID, Config: cfg, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := f.plans.PutBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := f.plans.RecordFrontendAbsence(ctx, cloudinfra.FrontendAbsence{OrgID: "org", ProjectID: "proj", EnvironmentID: d.EnvironmentID, Service: d.Service, Provider: "vercel", ConnectionID: d.ConnectionID, AccountID: d.AccountID, ExternalID: "project-x", PlanID: d.PlanID}); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := f.db.QueryRow(ctx, `SELECT status FROM loomplane_frontend_slots WHERE service=$1`, d.Service).Scan(&status); err != nil || status != "released" {
		t.Fatalf("slot after absence: %s %v", status, err)
	}
}

// A frontend that may still run on Vercel keeps its project: deleting it
// is refused with ErrFrontendsHeld, not a bare restrict violation.
func TestDeleteProjectRefusesHeldFrontend(t *testing.T) {
	f := newFrontendFixture(t, frontendMax(1))
	d := f.plan(t, "source", "env", "web")
	if b, err := f.reserve(t.Context(), d); err != nil || b != nil {
		t.Fatalf("reserve: %+v %v", b, err)
	}
	store := projects.NewPgStore(f.db)
	err := store.DeleteProject(t.Context(), "org", "proj")
	if !errors.Is(err, projects.ErrFrontendsHeld) {
		t.Fatalf("delete with a held frontend: %v", err)
	}
	if _, err = store.GetProject(t.Context(), "org", "proj"); err != nil {
		t.Fatalf("refused delete lost the project: %v", err)
	}
	err = store.DeleteEnvironment(t.Context(), "org", "proj", "env")
	if !errors.Is(err, projects.ErrFrontendsHeld) {
		t.Fatalf("environment delete with a held frontend: %v", err)
	}
}

// Once Vercel has confirmed a frontend gone, its released slot is only
// bookkeeping, and the project's deletion takes it along.
func TestDeleteProjectClearsReleasedFrontends(t *testing.T) {
	f := newFrontendFixture(t, frontendMax(1))
	d := f.plan(t, "source", "env", "web")
	if b, err := f.reserve(t.Context(), d); err != nil || b != nil {
		t.Fatalf("reserve: %+v %v", b, err)
	}
	releaseByAbsence(t, f, d)
	// An approval stays immutable while its plan is there.
	if _, err := f.db.Exec(t.Context(), `DELETE FROM loomplane_frontend_intents WHERE plan_id='source'`); err == nil {
		t.Fatal("a frontend intent was deleted under its plan")
	}
	if err := projects.NewPgStore(f.db).DeleteProject(t.Context(), "org", "proj"); err != nil {
		t.Fatalf("delete with only released frontends: %v", err)
	}
	if slots, uses, intents := frontendRows(t, f); slots+uses+intents != 0 {
		t.Fatalf("left behind: %d slots, %d uses, %d intents", slots, uses, intents)
	}
}

// An environment's deletion clears its own released frontends and leaves
// another environment's held one alone.
func TestDeleteEnvironmentClearsOnlyItsReleasedFrontends(t *testing.T) {
	f := newFrontendFixture(t, nil)
	gone := f.plan(t, "gone", "stage", "web")
	kept := f.plan(t, "kept", "env", "web")
	for _, d := range []cloudinfra.FrontendDemand{gone, kept} {
		if b, err := f.reserve(t.Context(), d); err != nil || b != nil {
			t.Fatalf("reserve %s: %+v %v", d.PlanID, b, err)
		}
	}
	cfg, _ := json.Marshal(planning.ServiceConfig{Provider: "vercel", Workload: provider.WebApp, Execution: provider.Serverless, FrontendDemand: &gone})
	if err := f.plans.PutBinding(t.Context(), &planning.Binding{ID: "binding-stage", OrgID: "org", ProjectID: "proj", EnvironmentID: "stage", Subject: planning.ServiceSubject, Name: "web", Provider: "vercel", ConnectionID: gone.ConnectionID, ExternalID: "project-stage", PlanID: "gone", Config: cfg, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := f.plans.RecordFrontendAbsence(t.Context(), cloudinfra.FrontendAbsence{OrgID: "org", ProjectID: "proj", EnvironmentID: "stage", Service: "web", Provider: "vercel", ConnectionID: gone.ConnectionID, AccountID: gone.AccountID, ExternalID: "project-stage", PlanID: "gone"}); err != nil {
		t.Fatal(err)
	}
	if err := projects.NewPgStore(f.db).DeleteEnvironment(t.Context(), "org", "proj", "stage"); err != nil {
		t.Fatalf("environment delete with a released frontend: %v", err)
	}
	var left int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_frontend_slots WHERE environment_id='stage'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("stage slots left: %d %v", left, err)
	}
	var status string
	if err := f.db.QueryRow(t.Context(), `SELECT status FROM loomplane_frontend_slots WHERE environment_id='env'`).Scan(&status); err != nil || status != "reserved" {
		t.Fatalf("other environment's slot: %s %v", status, err)
	}
}

// A deploy that fails after Vercel made its project still names that
// project in the ledger, so absence of exactly it releases the slot.
func TestRecordFrontendResourceNamesTheLeakedProject(t *testing.T) {
	f := newFrontendFixture(t, frontendMax(1))
	ctx := t.Context()
	d := f.plan(t, "source", "env", "web")
	if b, err := f.reserve(ctx, d); err != nil || b != nil {
		t.Fatalf("reserve: %+v %v", b, err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE loomplane_frontend_uses SET status='accepted' WHERE plan_id='source'`); err != nil {
		t.Fatal(err)
	}
	if err := f.plans.RecordFrontendResource(ctx, "org", "source", "web", d.ConnectionID, "native-ref", ""); err != nil {
		t.Fatal(err)
	}
	// A retry naming the same thing again is fine.
	if err := f.plans.RecordFrontendResource(ctx, "org", "source", "web", d.ConnectionID, "native-ref", ""); err != nil {
		t.Fatalf("a retry: %v", err)
	}
	// A different identity never overwrites the first, and stops the deploy.
	if err := f.plans.RecordFrontendResource(ctx, "org", "source", "web", d.ConnectionID, "prj_other", ""); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("an unrelated identity: %v", err)
	}
	// A fallback replaces exactly the native name it undid.
	if err := f.plans.RecordFrontendResource(ctx, "org", "source", "web", d.ConnectionID, "prj_leaked", "native-ref"); err != nil {
		t.Fatalf("the fallback's replacement: %v", err)
	}
	// A plan with no use for the service records nothing.
	if err := f.plans.RecordFrontendResource(ctx, "org", "source", "other", d.ConnectionID, "prj_x", ""); err != nil {
		t.Fatalf("no use: %v", err)
	}
	held, err := f.store.HeldFrontends(ctx, "org", "proj", "")
	if err != nil || len(held) != 1 || held[0].ExternalID != "prj_leaked" || held[0].Status != "accepted" || held[0].Service != "web" || held[0].PlanID != "source" {
		t.Fatalf("held: %+v %v", held, err)
	}
	if err = f.plans.RecordFrontendAbsence(ctx, cloudinfra.FrontendAbsence{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Service: "web", Provider: "vercel", ConnectionID: d.ConnectionID, AccountID: d.AccountID, ExternalID: "prj_leaked", PlanID: "source"}); err != nil {
		t.Fatal(err)
	}
	if held, err = f.store.HeldFrontends(ctx, "org", "proj", ""); err != nil || len(held) != 0 {
		t.Fatalf("held after absence: %+v %v", held, err)
	}
	// A deploy whose use was settled under it can't name what it made.
	if err = f.plans.RecordFrontendResource(ctx, "org", "source", "web", d.ConnectionID, "prj_late", "prj_leaked"); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("recording into a settled use: %v", err)
	}
}

// A frontend whose plan is still deploying says so, so nothing settles it
// under the deploy.
func TestHeldFrontendsFlagsADeployInFlight(t *testing.T) {
	f := newFrontendFixture(t, frontendMax(1))
	ctx := t.Context()
	d := f.plan(t, "source", "env", "web")
	if b, err := f.reserve(ctx, d); err != nil || b != nil {
		t.Fatalf("reserve: %+v %v", b, err)
	}
	held, err := f.store.HeldFrontends(ctx, "org", "proj", "env")
	if err != nil || len(held) != 1 || held[0].Deploying {
		t.Fatalf("before a deployment: %+v %v", held, err)
	}
	if _, err = f.db.Exec(ctx, `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES('dep','org','proj','env','production','source','abc','running','usr',now())`); err != nil {
		t.Fatal(err)
	}
	if held, err = f.store.HeldFrontends(ctx, "org", "proj", "env"); err != nil || len(held) != 1 || !held[0].Deploying {
		t.Fatalf("while deploying: %+v %v", held, err)
	}
}

// The store says up front which deletions it would refuse whatever a
// preparer did, so nothing is removed from a provider for nothing.
func TestDeletionRefusalBeforeAnythingRuns(t *testing.T) {
	f := newFrontendFixture(t, nil)
	ctx := t.Context()
	store := projects.NewPgStore(f.db)
	for _, tc := range []struct {
		project, env string
		want         error
	}{
		{"proj", "", nil},
		{"proj", "stage", nil},
		{"missing", "", projects.ErrNotFound},
		{"proj", "missing", projects.ErrEnvironmentNotFound},
	} {
		if err := store.DeletionRefusal(ctx, "org", tc.project, tc.env); !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
			t.Fatalf("%s/%s: %v, want %v", tc.project, tc.env, err, tc.want)
		}
	}
	for _, env := range []string{"stage", "preview"} {
		if err := store.DeleteEnvironment(ctx, "org", "proj", env); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeletionRefusal(ctx, "org", "proj", "env"); !errors.Is(err, projects.ErrLastEnvironment) {
		t.Fatalf("the last environment: %v", err)
	}
}
