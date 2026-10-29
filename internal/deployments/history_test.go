//go:build integration

package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// historyDB is a fresh, migrated database holding one project with a
// staging (env_a) and a production (env_b) environment, and the History
// over it.
func historyDB(t *testing.T) (context.Context, *History, *pgdriver.PgDB) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the release history tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("release_history_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	if err = ops.Migrate(ctx, u.String(), migrations.All()); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Close() })
	for _, stmt := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('proj','org','One','one','ready','usr',now(),now())`,
		`INSERT INTO loomplane_environments (id, org_id, project_id, name, kind, created_at, updated_at) VALUES
			('env_a', 'org', 'proj', 'staging', 'staging', now(), now()),
			('env_b', 'org', 'proj', 'production', 'production', now(), now())`,
		`INSERT INTO loomplane_manifests (id, org_id, project_id, revision, body, digest, created_by, created_at)
			VALUES ('man_1', 'org', 'proj', 1, 'version: 1', 'd', 'usr', now())`,
	} {
		if _, err = pg.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	reg, err := provider.NewRegistry(providers.Catalog()...)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, &History{Store: NewPgStore(pg), Plans: planning.NewPgStore(pg), Providers: reg}, pg
}

// historyRelease is one plan of an environment and its deployment, which
// finishes with status at finished, or stays running when finished is
// zero. A rollback plan carries what it rolls back; canary is the
// deployment's canary status, "" without one.
type historyRelease struct {
	plan, deployment, env, kind string
	catalog                     bool
	syncID                      string
	rollback                    *planning.Rollback
	canary                      string
	actions                     []planning.Action
	steps                       []Step
	status                      string
	finished                    time.Time
}

func (r historyRelease) record(ctx context.Context, t *testing.T, h *History, pg *pgdriver.PgDB) {
	t.Helper()
	env := map[string]string{"env_a": "staging", "env_b": "production"}[r.env]
	kind := r.kind
	if kind == "" {
		kind = planning.KindRelease
	}
	actions := r.actions
	if actions == nil {
		actions = []planning.Action{}
	}
	result, err := json.Marshal(planning.Result{
		CloudSyncID: r.syncID, Actions: actions, Connections: map[string]string{"gcp": "conn_g", "aws": "conn_a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var spec []byte
	if r.rollback != nil {
		if spec, err = json.Marshal(r.rollback); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pg.Exec(ctx, `INSERT INTO loomplane_plans (id, org_id, project_id, environment_id, environment, status, manifest_id,
		manifest_revision, result, catalog, kind, rollback, created_by, created_at)
		VALUES ($1, 'org', 'proj', $2, $3, 'approved', 'man_1', 1, $4, $5, $6, $7, 'usr', now())`,
		r.plan, r.env, env, result, r.catalog, kind, spec); err != nil {
		t.Fatal(err)
	}
	started := r.finished.Add(-5 * time.Minute)
	if r.finished.IsZero() {
		started = time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC)
	}
	var canary *Canary
	if r.canary != "" {
		canary = &Canary{Status: r.canary}
	}
	if _, err = h.Store.CreateDeployment(ctx, &Deployment{
		ID: r.deployment, OrgID: "org", ProjectID: "proj", EnvironmentID: r.env, Environment: env, PlanID: r.plan,
		Status: StatusRunning, Steps: r.steps, StartedBy: "usr", StartedAt: started, Canary: canary,
	}); err != nil {
		t.Fatal(err)
	}
	if !r.finished.IsZero() {
		if err = h.Store.Finish(ctx, "org", r.deployment, r.status, "", r.finished); err != nil {
			t.Fatal(err)
		}
	}
}

func at(hour, minute int) time.Time { return time.Date(2026, 10, 24, hour, minute, 0, 0, time.UTC) }

func serviceAction(name, op, prov, region string, cfg *planning.ServiceConfig) planning.Action {
	return planning.Action{ID: "service:" + name, Op: planning.Op(op), Subject: planning.ServiceSubject, Name: name,
		Provider: prov, Region: region, Service: cfg}
}

func serviceStep(name, op, status, image, revision string) Step {
	return Step{ActionID: "service:" + name, Name: name, Subject: string(planning.ServiceSubject), Op: op, Status: status, Image: image, Revision: revision}
}

func TestHistory_releasesNewestFirst(t *testing.T) {
	ctx, h, pg := historyDB(t)
	for _, r := range []historyRelease{
		{plan: "plan_1", deployment: "dep_1", env: "env_b", status: StatusSucceeded, finished: at(10, 0)},
		{plan: "plan_2", deployment: "dep_2", env: "env_b", status: StatusFailed, finished: at(11, 0)},
		{plan: "plan_3", deployment: "dep_3", env: "env_b", kind: planning.KindPromotion, status: StatusSucceeded, finished: at(12, 0)},
		// A Cloud sync, a catalog plan and another environment's release
		// aren't production's releases.
		{plan: "plan_sync", deployment: "dep_sync", env: "env_b", syncID: "sync_1", status: StatusSucceeded, finished: at(12, 10)},
		{plan: "plan_cat", deployment: "dep_cat", env: "env_b", catalog: true, status: StatusSucceeded, finished: at(12, 20)},
		{plan: "plan_a", deployment: "dep_a", env: "env_a", status: StatusSucceeded, finished: at(12, 30)},
		{plan: "plan_4", deployment: "dep_4", env: "env_b"},
	} {
		r.record(ctx, t, h, pg)
	}

	got, err := h.Releases(ctx, "org", "env_b", time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []planning.ReleaseRecord{
		{DeploymentID: "dep_3", PlanID: "plan_3", Kind: planning.KindPromotion, Status: StatusSucceeded, StartedAt: at(11, 55), FinishedAt: at(12, 0)},
		{DeploymentID: "dep_1", PlanID: "plan_1", Kind: planning.KindRelease, Status: StatusSucceeded, StartedAt: at(9, 55), FinishedAt: at(10, 0)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("releases\n%+v\nwant\n%+v", got, want)
	}
	if got, err = h.Releases(ctx, "org", "env_b", at(12, 0), 10); err != nil || !reflect.DeepEqual(got, want[1:]) {
		t.Fatalf("releases before noon %+v, %v", got, err)
	}
	if got, err = h.Releases(ctx, "org", "env_b", time.Time{}, 1); err != nil || !reflect.DeepEqual(got, want[:1]) {
		t.Fatalf("the newest release %+v, %v", got, err)
	}
	if got, err = h.Releases(ctx, "org-2", "env_b", time.Time{}, 10); err != nil || len(got) != 0 {
		t.Fatalf("another organization's releases %+v, %v", got, err)
	}
}

func TestHistory_releasedImagesWalkBack(t *testing.T) {
	ctx, h, pg := historyDB(t)
	digest := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	apiImage1 := "europe-west1-docker.pkg.dev/shop/loomplane/api-x@" + digest("1")
	apiImage3 := "europe-west1-docker.pkg.dev/shop/loomplane/api-x@" + digest("3")
	webImage := "registry.example.com/acme/web@" + digest("b")
	api := func(commit string) *planning.ServiceConfig {
		return &planning.ServiceConfig{Provider: "cloud-run", CommitSHA: commit, Source: "services/api", Image: "services/api/Dockerfile"}
	}
	web := &planning.ServiceConfig{Provider: "server", CommitSHA: "aaa1111", Source: "apps/web", Ecosystem: "node", Start: "npm start",
		TargetSettings: map[string]string{"architecture": "amd64"},
		BuildTarget:    &planning.BuildTarget{Registry: "conn_reg", BuilderSettings: map[string]string{"architecture": "arm64"}}}
	webAction := serviceAction("web", "create", "server", "", web)
	webAction.ConnectionID = "conn_s"
	keptWeb := serviceAction("web", "keep", "server", "", web)
	keptWeb.AdoptCloudPlacement = true
	db := planning.Action{ID: "resource:db", Op: planning.Create, Subject: planning.ResourceSubject, Name: "db", Provider: "cloud-sql"}
	for _, r := range []historyRelease{
		{plan: "plan_1", deployment: "dep_1", env: "env_b", status: StatusSucceeded, finished: at(10, 0),
			actions: []planning.Action{serviceAction("api", "create", "cloud-run", "europe-west1", api("aaa1111")), webAction, db},
			steps: []Step{
				serviceStep("api", "create", StepSucceeded, apiImage1, apiImage1),
				serviceStep("web", "create", StepSucceeded, webImage, "web-rev-1"),
				{ActionID: "resource:db", Name: "db", Subject: string(planning.ResourceSubject), Op: "create", Status: StepSucceeded, Image: "not-an-image"},
			}},
		// A failed release ran nothing anyone returns to.
		{plan: "plan_2", deployment: "dep_2", env: "env_b", status: StatusFailed, finished: at(11, 0),
			actions: []planning.Action{serviceAction("api", "update", "cloud-run", "europe-west1", api("bbb2222"))},
			steps:   []Step{serviceStep("api", "update", StepFailed, "europe-west1-docker.pkg.dev/shop/loomplane/api-x@"+digest("2"), "")}},
		// web is kept as it was. Adopting its placement is a step that
		// deploys nothing.
		{plan: "plan_3", deployment: "dep_3", env: "env_b", status: StatusSucceeded, finished: at(12, 0),
			actions: []planning.Action{serviceAction("api", "update", "cloud-run", "europe-west1", api("ccc3333")), keptWeb},
			steps:   []Step{serviceStep("api", "update", StepSucceeded, apiImage3, apiImage3), serviceStep("web", "keep", StepSucceeded, "", "")}},
	} {
		r.record(ctx, t, h, pg)
	}

	got, err := h.ReleasedImages(ctx, "org", "env_b", at(12, 0))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]planning.ReleasedImage{
		"api": {Service: "api", Provider: "cloud-run", ConnectionID: "conn_g", Region: "europe-west1", CommitSHA: "ccc3333",
			Inputs: planning.BuildInputs{Source: "services/api", Image: "services/api/Dockerfile"}, Image: apiImage3, Revision: apiImage3, DeploymentID: "dep_3", PlanID: "plan_3", FinishedAt: at(12, 0)},
		// web was built on an arm64 builder, whatever its server runs.
		"web": {Service: "web", Provider: "server", ConnectionID: "conn_s", Registry: "conn_reg", CommitSHA: "aaa1111",
			Architecture: "arm64", Inputs: planning.BuildInputs{Source: "apps/web", Ecosystem: "node", Start: "npm start"}, Image: webImage, Revision: "web-rev-1", DeploymentID: "dep_1", PlanID: "plan_1", FinishedAt: at(10, 0)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("released images\n%+v\nwant\n%+v", got, want)
	}

	// Just before the third release, api is still what the first ran.
	got, err = h.ReleasedImages(ctx, "org", "env_b", at(11, 59))
	if err != nil {
		t.Fatal(err)
	}
	if a := got["api"]; len(got) != 2 || a.Image != apiImage1 || a.CommitSHA != "aaa1111" || a.DeploymentID != "dep_1" {
		t.Fatalf("released images before the third release %+v", got)
	}
	if got, err = h.ReleasedImages(ctx, "org", "env_b", at(9, 0)); err != nil || len(got) != 0 {
		t.Fatalf("released images before any release %+v, %v", got, err)
	}
}

// A release that deleted a service leaves nothing of it to return to,
// whatever an older release ran.
func TestHistory_aDeletedServiceRanNothing(t *testing.T) {
	ctx, h, pg := historyDB(t)
	image := "europe-west1-docker.pkg.dev/shop/loomplane/api-x@sha256:" + strings.Repeat("1", 64)
	for _, r := range []historyRelease{
		{plan: "plan_1", deployment: "dep_1", env: "env_b", status: StatusSucceeded, finished: at(10, 0),
			actions: []planning.Action{serviceAction("api", "create", "cloud-run", "europe-west1", &planning.ServiceConfig{Provider: "cloud-run"})},
			steps:   []Step{serviceStep("api", "create", StepSucceeded, image, image)}},
		{plan: "plan_2", deployment: "dep_2", env: "env_b", status: StatusSucceeded, finished: at(11, 0),
			actions: []planning.Action{serviceAction("api", "delete", "cloud-run", "europe-west1", nil)},
			steps:   []Step{serviceStep("api", "delete", StepSucceeded, "", "")}},
	} {
		r.record(ctx, t, h, pg)
	}
	got, err := h.ReleasedImages(ctx, "org", "env_b", at(11, 0))
	if err != nil || len(got) != 0 {
		t.Fatalf("released images %+v, %v", got, err)
	}
	if got, err = h.ReleasedImages(ctx, "org", "env_b", at(10, 30)); err != nil || got["api"].Image != image {
		t.Fatalf("released images before the delete %+v, %v", got, err)
	}
}

// Without the provider registry, History can't name the connection an
// image was deployed with, so it refuses rather than leave it empty.
func TestHistory_releasedImagesNeedTheProviders(t *testing.T) {
	ctx, h, _ := historyDB(t)
	h.Providers = nil
	if _, err := h.ReleasedImages(ctx, "org", "env_b", at(12, 0)); err == nil || !strings.Contains(err.Error(), "provider registry") {
		t.Fatalf("err %v", err)
	}
}

func TestHistory_carriesCanaryStatus(t *testing.T) {
	ctx, h, pg := historyDB(t)
	for _, r := range []historyRelease{
		{plan: "plan_1", deployment: "dep_1", env: "env_b", status: StatusSucceeded, finished: at(10, 0)},
		{plan: "plan_2", deployment: "dep_2", env: "env_b", canary: CanaryPromoted, status: StatusSucceeded, finished: at(11, 0)},
		{plan: "plan_3", deployment: "dep_3", env: "env_b", canary: CanaryRolledBack, status: StatusFailed, finished: at(12, 0)},
	} {
		r.record(ctx, t, h, pg)
	}
	got, err := h.Releases(ctx, "org", "env_b", time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].CanaryStatus != CanaryPromoted || got[1].CanaryStatus != "" {
		t.Fatalf("releases %+v", got)
	}
	latest, err := h.Latest(ctx, "org", "env_b")
	if err != nil {
		t.Fatal(err)
	}
	if latest.DeploymentID != "dep_3" || latest.CanaryStatus != CanaryRolledBack {
		t.Fatalf("latest %+v", latest)
	}
}

func TestHistory_latestIsTheNewestDeploymentWhateverItsStatus(t *testing.T) {
	ctx, h, pg := historyDB(t)
	if _, err := h.Latest(ctx, "org", "env_b"); !errors.Is(err, planning.ErrNotFound) {
		t.Fatalf("an environment nothing deployed: %v", err)
	}
	for _, r := range []historyRelease{
		{plan: "plan_1", deployment: "dep_1", env: "env_b", status: StatusSucceeded, finished: at(10, 0)},
		{plan: "plan_2", deployment: "dep_2", env: "env_b", status: StatusFailed, finished: at(11, 0)},
	} {
		r.record(ctx, t, h, pg)
	}
	want := planning.ReleaseRecord{DeploymentID: "dep_2", PlanID: "plan_2", Kind: planning.KindRelease, Status: StatusFailed, StartedAt: at(10, 55), FinishedAt: at(11, 0)}
	if got, err := h.Latest(ctx, "org", "env_b"); err != nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("latest %+v, %v", got, err)
	}
	// A Cloud sync, a catalog plan and another environment's deployment
	// aren't production's releases; a running deployment is, and has no
	// finish yet.
	for _, r := range []historyRelease{
		{plan: "plan_sync", deployment: "dep_sync", env: "env_b", syncID: "sync_1", status: StatusSucceeded, finished: at(12, 10)},
		{plan: "plan_cat", deployment: "dep_cat", env: "env_b", catalog: true, status: StatusSucceeded, finished: at(12, 20)},
		{plan: "plan_a", deployment: "dep_a", env: "env_a", status: StatusSucceeded, finished: at(12, 30)},
	} {
		r.record(ctx, t, h, pg)
	}
	if got, err := h.Latest(ctx, "org", "env_b"); err != nil || got.DeploymentID != "dep_2" {
		t.Fatalf("latest after a sync and a catalog plan %+v, %v", got, err)
	}
	historyRelease{plan: "plan_4", deployment: "dep_4", env: "env_b"}.record(ctx, t, h, pg)
	want = planning.ReleaseRecord{DeploymentID: "dep_4", PlanID: "plan_4", Kind: planning.KindRelease, Status: StatusRunning, StartedAt: at(13, 0)}
	if got, err := h.Latest(ctx, "org", "env_b"); err != nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("latest while one runs %+v, %v", got, err)
	}
	if _, err := h.Latest(ctx, "org-2", "env_b"); !errors.Is(err, planning.ErrNotFound) {
		t.Fatalf("another organization's latest: %v", err)
	}
}

// TestRollbackTarget_rollingBackTwice: R1, R2, a rollback of R2, then R3.
// Rolling R3 back restores the rollback of R2, which is R1's state, never
// R2; once that rollback succeeded, both R2 and R3 count as rolled back.
func TestRollbackTarget_rollingBackTwice(t *testing.T) {
	ctx, h, pg := historyDB(t)
	plans := planning.NewPgStore(pg)
	svc := planning.NewService(planning.Deps{
		Store: plans, Projects: projects.NewPgStore(pg), History: h, Providers: providers.Registry(), Authz: allowAll{},
	})
	for _, r := range []historyRelease{
		{plan: "plan_1", deployment: "dep_1", env: "env_b", status: StatusSucceeded, finished: at(10, 0)},
		{plan: "plan_2", deployment: "dep_2", env: "env_b", status: StatusSucceeded, finished: at(11, 0)},
		{plan: "plan_2r", deployment: "dep_2r", env: "env_b", kind: planning.KindRollback, status: StatusSucceeded, finished: at(11, 30),
			rollback: &planning.Rollback{Trigger: planning.TriggerManual, TargetDeploymentID: "dep_2", TargetPlanID: "plan_2",
				RestoreDeploymentID: "dep_1", RestorePlanID: "plan_1", RestoreFinishedAt: at(10, 0)}},
		{plan: "plan_3", deployment: "dep_3", env: "env_b", status: StatusSucceeded, finished: at(12, 0)},
	} {
		r.record(ctx, t, h, pg)
	}
	p := security.Principal{UserID: "usr", OrgID: "org"}
	got, err := svc.RollbackTarget(ctx, p, "proj", "production", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Target.DeploymentID != "dep_3" || got.Restore.DeploymentID != "dep_2r" || got.Restore.Kind != planning.KindRollback {
		t.Fatalf("target %+v restore %+v, want dep_3 and dep_2r", got.Target, got.Restore)
	}
	restore, err := plans.GetPlan(ctx, "org", got.Restore.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if restore.Rollback == nil || restore.Rollback.RestorePlanID != "plan_1" {
		t.Fatalf("the restored rollback %+v doesn't hold R1's state", restore.Rollback)
	}

	historyRelease{plan: "plan_3r", deployment: "dep_3r", env: "env_b", kind: planning.KindRollback, status: StatusSucceeded, finished: at(12, 30),
		rollback: &planning.Rollback{Trigger: planning.TriggerManual, TargetDeploymentID: "dep_3", TargetPlanID: "plan_3",
			RestoreDeploymentID: "dep_2r", RestorePlanID: "plan_2r", RestoreFinishedAt: at(11, 30)}}.record(ctx, t, h, pg)
	rolledBack, err := plans.RolledBackPlans(ctx, "org", "env_b")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]bool{"plan_2": true, "plan_3": true}; !reflect.DeepEqual(rolledBack, want) {
		t.Fatalf("rolled back %v, want %v", rolledBack, want)
	}
}
