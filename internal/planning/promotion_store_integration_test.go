//go:build integration

package planning

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
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/ops"
)

// promotionDB returns a store over a fresh, migrated database holding one
// project with two environments, "staging" (env_a) and "production" (env_b),
// and one manifest.
func promotionDB(t *testing.T) (context.Context, *PgStore, *pgdriver.PgDB) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the PostgreSQL plan kind tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("plan_kinds_%d", time.Now().UnixNano())
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
		`INSERT INTO loomplane_project_sources (id, org_id, project_id, alias, repository, created_by, created_at, updated_at)
			VALUES ('src_1', 'org', 'proj', 'mono', 'acme/mono', 'usr', now(), now())`,
		`INSERT INTO loomplane_analyses (id, org_id, project_id, status, repository, commit_sha, source_id, alias, created_by, created_at)
			VALUES ('anl_1', 'org', 'proj', 'succeeded', 'acme/mono', 'abc', 'src_1', 'mono', 'usr', now())`,
		`INSERT INTO loomplane_plans (id, org_id, project_id, environment_id, environment, status, manifest_id, manifest_revision, created_by, created_at)
			VALUES ('plan_old', 'org', 'proj', 'env_b', 'production', 'approved', 'man_1', 1, 'usr', now())`,
	} {
		if _, err = pg.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return ctx, NewPgStore(pg), pg
}

func TestPgStore_planKindsRoundTrip(t *testing.T) {
	ctx, store, _ := promotionDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	p := &Plan{
		ID: "plan_promo", OrgID: "org", ProjectID: "proj", EnvironmentID: "env_b", Environment: "production",
		Status: StatusPlanning, ManifestID: "man_1", ManifestRevision: 1, CreatedBy: "usr", CreatedAt: now,
		Kind: KindPromotion, From: "staging", FromEnvironmentID: "env_a",
		Sources: []PlanSource{{
			SourceID: "src_1", Alias: "mono", Repository: "acme/mono", AnalysisID: "anl_1", CommitSHA: "abc",
			InScope: true, FromPlanID: "plan_staging",
		}},
	}
	if _, err := store.CreatePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Status, p.Revision, p.Executable, p.PlannedAt, p.ExpiresAt = StatusReady, "rev", true, &now, &now
	p.Connections = map[string]string{}
	p.Leftovers = []Leftover{{Subject: ResourceSubject, Name: "db", Op: Delete, DataBearing: true, Reason: "kept"}}
	p.Artifacts = []ArtifactChoice{{Service: "api", Reused: true, Reference: "repo@sha256:abc", From: "staging"}}
	if ok, err := store.FinishPlanning(ctx, p); err != nil || !ok {
		t.Fatalf("finish: %v %v", ok, err)
	}
	got, err := store.GetPlan(ctx, "org", "plan_promo")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindPromotion || got.From != "staging" || got.FromEnvironmentID != "env_a" {
		t.Fatalf("kind %q from %q/%q", got.Kind, got.From, got.FromEnvironmentID)
	}
	if !reflect.DeepEqual(got.Sources, p.Sources) {
		t.Fatalf("sources %+v", got.Sources)
	}
	if !reflect.DeepEqual(got.Leftovers, p.Leftovers) || !reflect.DeepEqual(got.Artifacts, p.Artifacts) {
		t.Fatalf("leftovers %+v artifacts %+v", got.Leftovers, got.Artifacts)
	}
	if got.Rollback != nil {
		t.Fatalf("rollback %+v", got.Rollback)
	}

	// A plan made before the migration has none of these columns set.
	old, err := store.GetPlan(ctx, "org", "plan_old")
	if err != nil {
		t.Fatal(err)
	}
	if old.Kind != KindRelease || old.From != "" || old.FromEnvironmentID != "" || old.Rollback != nil {
		t.Fatalf("old plan: %+v", old)
	}
	if old.Leftovers == nil || len(old.Leftovers) != 0 || old.Artifacts == nil || len(old.Artifacts) != 0 {
		t.Fatalf("old plan leftovers %#v artifacts %#v", old.Leftovers, old.Artifacts)
	}
}

func TestPgStore_rollbackSpecRoundTrip(t *testing.T) {
	ctx, store, _ := promotionDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	spec := &Rollback{
		Reason: "error rate", Trigger: TriggerWatch, TargetDeploymentID: "dep_2", TargetPlanID: "plan_2",
		RestoreDeploymentID: "dep_1", RestorePlanID: "plan_1",
		RestoreFinishedAt: now.In(time.FixedZone("x", 3600)), WatchID: "watch_1",
	}
	p := &Plan{
		ID: "plan_rb", OrgID: "org", ProjectID: "proj", EnvironmentID: "env_b", Environment: "production",
		Status: StatusPlanning, ManifestID: "man_1", ManifestRevision: 1, CreatedBy: "usr", CreatedAt: now,
		Kind: KindRollback, Rollback: spec,
	}
	if _, err := store.CreatePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetPlan(ctx, "org", "plan_rb")
	if err != nil {
		t.Fatal(err)
	}
	want := *spec
	want.RestoreFinishedAt = now
	if got.Kind != KindRollback || got.Rollback == nil || *got.Rollback != want {
		t.Fatalf("rollback %+v, want %+v", got.Rollback, want)
	}
	if got.Rollback.RestoreFinishedAt.Location() != time.UTC {
		t.Fatalf("restore_finished_at in %s", got.Rollback.RestoreFinishedAt.Location())
	}
}

// RolledBackPlans names the plans a rollback in the environment undid:
// a rollback whose deployment succeeded or failed counts, since a failed
// one still undid some of its target.
func TestPgStore_rolledBackPlans(t *testing.T) {
	ctx, store, pg := promotionDB(t)
	if got, err := store.RolledBackPlans(ctx, "org", "env_b"); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("before any rollback: %v %v", got, err)
	}
	plan := func(id, env, kind, target string) {
		t.Helper()
		var spec []byte
		if kind == KindRollback {
			var err error
			if spec, err = json.Marshal(Rollback{Reason: "bad", Trigger: TriggerManual, TargetPlanID: target}); err != nil {
				t.Fatal(err)
			}
		}
		envName := map[string]string{"env_a": "staging", "env_b": "production"}[env]
		if _, err := pg.Exec(ctx, `INSERT INTO loomplane_plans (id, org_id, project_id, environment_id, environment, status, manifest_id,
			manifest_revision, kind, rollback, created_by, created_at)
			VALUES ($1, 'org', 'proj', $2, $3, 'approved', 'man_1', 1, $4, $5, 'usr', now())`, id, env, envName, kind, spec); err != nil {
			t.Fatal(err)
		}
	}
	deploy := func(id, planID, env, status string) {
		t.Helper()
		envName := map[string]string{"env_a": "staging", "env_b": "production"}[env]
		var finished *time.Time
		if status != "running" {
			now := time.Now()
			finished = &now
		}
		if _, err := pg.Exec(ctx, `INSERT INTO loomplane_deployments (id, org_id, project_id, environment_id, environment, plan_id,
			commit_sha, status, started_by, started_at, finished_at)
			VALUES ($1, 'org', 'proj', $2, $3, $4, 'abc', $5, 'usr', now(), $6)`, id, env, envName, planID, status, finished); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"plan_r1", "plan_r2", "plan_r3", "plan_r4", "plan_r5"} {
		plan(id, "env_b", KindRelease, "")
	}
	plan("plan_rb_ok", "env_b", KindRollback, "plan_r1")
	deploy("dep_rb_ok", "plan_rb_ok", "env_b", "succeeded")
	plan("plan_rb_failed", "env_b", KindRollback, "plan_r2")
	deploy("dep_rb_failed", "plan_rb_failed", "env_b", "failed")
	plan("plan_rb_running", "env_b", KindRollback, "plan_r3")
	deploy("dep_rb_running", "plan_rb_running", "env_b", "running")
	// A rollback plan nobody approved has no deployment.
	plan("plan_rb_open", "env_b", KindRollback, "plan_r4")
	// A rollback refused as stale deployed nothing, so it undid nothing.
	plan("plan_rb_stale", "env_b", KindRollback, "plan_r4")
	deploy("dep_rb_stale", "plan_rb_stale", "env_b", "failed")
	if _, err := pg.Exec(ctx, `UPDATE loomplane_deployments SET error=$1 WHERE id='dep_rb_stale'`, StaleRollback); err != nil {
		t.Fatal(err)
	}
	// Staging's rollback is staging's.
	plan("plan_rb_staging", "env_a", KindRollback, "plan_r5")
	deploy("dep_rb_staging", "plan_rb_staging", "env_a", "succeeded")
	// A release's deployment succeeding undoes nothing.
	deploy("dep_r5", "plan_r5", "env_b", "succeeded")

	got, err := store.RolledBackPlans(ctx, "org", "env_b")
	if err != nil {
		t.Fatal(err)
	}
	// A failed rollback still undid some of its target.
	if want := map[string]bool{"plan_r1": true, "plan_r2": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("production rolled back %v, want %v", got, want)
	}
	if got, err = store.RolledBackPlans(ctx, "org", "env_a"); err != nil || !reflect.DeepEqual(got, map[string]bool{"plan_r5": true}) {
		t.Fatalf("staging rolled back %v, %v", got, err)
	}
	if got, err = store.RolledBackPlans(ctx, "org-2", "env_b"); err != nil || len(got) != 0 {
		t.Fatalf("another organization rolled back %v, %v", got, err)
	}
}

func TestRealtimePlanEventCarriesPromotionFields(t *testing.T) {
	ctx, store, pg := promotionDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	p := &Plan{
		ID: "plan_ev", OrgID: "org", ProjectID: "proj", EnvironmentID: "env_b", Environment: "production",
		Status: StatusPlanning, ManifestID: "man_1", ManifestRevision: 1, CreatedBy: "usr", CreatedAt: now,
		Kind: KindRollback, From: "staging", FromEnvironmentID: "env_a",
		Rollback: &Rollback{Reason: "bad", Trigger: TriggerManual, RestoreFinishedAt: now},
	}
	if _, err := store.CreatePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Status, p.Revision, p.PlannedAt, p.ExpiresAt = StatusReady, "rev", &now, &now
	p.Connections = map[string]string{}
	p.Leftovers = []Leftover{{Subject: ResourceSubject, Name: "db", Op: Delete, DataBearing: true, Reason: "kept"}}
	p.Artifacts = []ArtifactChoice{{Service: "api", Reused: true, From: "staging"}}
	if ok, err := store.FinishPlanning(ctx, p); err != nil || !ok {
		t.Fatalf("finish: %v %v", ok, err)
	}
	latest := func(id string) map[string]json.RawMessage {
		var data []byte
		if err := pg.QueryRow(ctx, `SELECT data FROM loomplane_events WHERE org_id = 'org' AND type = 'plan.updated' AND entity_id = $1
			ORDER BY sequence DESC LIMIT 1`, id).Scan(&data); err != nil {
			t.Fatal(err)
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	doc := latest("plan_ev")
	var artifacts []ArtifactChoice
	if err := json.Unmarshal(doc["artifacts"], &artifacts); err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts %s: %v", doc["artifacts"], err)
	}

	// A plan with neither still carries both keys, as empty lists.
	q := &Plan{
		ID: "plan_bare", OrgID: "org", ProjectID: "proj", EnvironmentID: "env_a", Environment: "staging",
		Status: StatusPlanning, ManifestID: "man_1", ManifestRevision: 1, CreatedBy: "usr", CreatedAt: now,
	}
	if _, err := store.CreatePlan(ctx, q); err != nil {
		t.Fatal(err)
	}
	q.Status, q.Revision, q.PlannedAt, q.ExpiresAt, q.Connections = StatusReady, "rev", &now, &now, map[string]string{}
	if ok, err := store.FinishPlanning(ctx, q); err != nil || !ok {
		t.Fatalf("finish bare: %v %v", ok, err)
	}
	bare := latest("plan_bare")
	for _, key := range []string{"leftovers", "artifacts"} {
		if string(bare[key]) != "[]" {
			t.Fatalf("bare plan event %s = %s", key, bare[key])
		}
	}
	for _, key := range []string{"kind", "from_environment", "rollback", "leftovers"} {
		if _, ok := doc[key]; !ok {
			t.Fatalf("event data has no %q: %v", key, doc)
		}
	}
	if string(doc["kind"]) != `"rollback"` || string(doc["from_environment"]) != `"staging"` {
		t.Fatalf("kind %s from_environment %s", doc["kind"], doc["from_environment"])
	}
	var leftovers []Leftover
	if err := json.Unmarshal(doc["leftovers"], &leftovers); err != nil || len(leftovers) != 1 {
		t.Fatalf("leftovers %s: %v", doc["leftovers"], err)
	}
}

// A release's manifest and every source it moved are written together, so
// a reader sees all of one release or all of another, never a mix.
func TestPgStore_recordReleaseIsOneSnapshot(t *testing.T) {
	ctx, store, pg := promotionDB(t)
	for _, stmt := range []string{
		`INSERT INTO loomplane_project_sources (id, org_id, project_id, alias, repository, created_by, created_at, updated_at) VALUES
			('src_web', 'org', 'proj', 'web', 'acme/web', 'usr', now(), now()),
			('src_api', 'org', 'proj', 'api', 'acme/api', 'usr', now(), now())`,
		`INSERT INTO loomplane_analyses (id, org_id, project_id, status, repository, commit_sha, source_id, alias, created_by, created_at) VALUES
			('anl_web', 'org', 'proj', 'succeeded', 'acme/web', 'w', 'src_web', 'web', 'usr', now()),
			('anl_api', 'org', 'proj', 'succeeded', 'acme/api', 'a', 'src_api', 'api', 'usr', now())`,
	} {
		if _, err := pg.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	manifest, sources, err := store.DeployedState(ctx, "org", "env_b")
	if err != nil || manifest != nil || len(sources) != 0 {
		t.Fatalf("before any release: %+v %+v %v", manifest, sources, err)
	}

	const releases = 200
	done := make(chan error, 1)
	go func() {
		for n := 1; n <= releases; n++ {
			sha := fmt.Sprint(n)
			if werr := store.RecordRelease(ctx, "org", &DeployedRelease{
				EnvironmentID: "env_b", PlanID: "plan_old", ManifestID: "man_1", ManifestRevision: n, DeployedAt: time.Now(),
				Sources: []*DeployedSource{
					{SourceID: "src_web", AnalysisID: "anl_web", CommitSHA: sha},
					{SourceID: "src_api", AnalysisID: "anl_api", CommitSHA: sha},
				},
			}); werr != nil {
				done <- werr
				return
			}
		}
		done <- nil
	}()
	reads := 0
	for finished := false; !finished; {
		select {
		case err = <-done:
			if err != nil {
				t.Fatal(err)
			}
			finished = true
		default:
		}
		manifest, sources, err = store.DeployedState(ctx, "org", "env_b")
		if err != nil {
			t.Fatal(err)
		}
		reads++
		if manifest == nil {
			if len(sources) != 0 {
				t.Fatalf("sources %+v without a manifest", sources)
			}
			continue
		}
		want := fmt.Sprint(manifest.ManifestRevision)
		if len(sources) != 2 || sources[0].CommitSHA != want || sources[1].CommitSHA != want {
			got := make([]string, 0, len(sources))
			for _, d := range sources {
				got = append(got, d.SourceID+"@"+d.CommitSHA)
			}
			t.Fatalf("read %d: manifest revision %d with sources %v", reads, manifest.ManifestRevision, got)
		}
	}
	if manifest == nil || manifest.ManifestRevision != releases || manifest.PlanID != "plan_old" || manifest.ManifestID != "man_1" {
		t.Fatalf("last read %+v", manifest)
	}
	for _, d := range sources {
		if d.EnvironmentID != "env_b" || d.PlanID != "plan_old" || d.CommitSHA != fmt.Sprint(releases) {
			t.Fatalf("source %+v", d)
		}
	}
	t.Logf("%d reads", reads)
}

// A promotion's pinned analysis round-trips, and never counts as the
// source's running analysis, so Start and pushes still analyze the branch.
func TestDiscoveryPgStore_pinnedAnalysesDontBlockTheBranch(t *testing.T) {
	ctx, _, pg := promotionDB(t)
	analyses := discovery.NewPgStore(pg)
	now := time.Now().UTC()
	pinned := &discovery.Analysis{ID: "anl_pinned", OrgID: "org", ProjectID: "proj", SourceID: "src_1", Source: "mono",
		Status: discovery.StatusQueued, Repository: "acme/mono", Ref: strings.Repeat("a", 40), Pinned: true, CreatedBy: "usr", CreatedAt: now}
	if err := analyses.CreateAnalysis(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	got, err := analyses.GetAnalysis(ctx, "org", "anl_pinned")
	if err != nil || !got.Pinned {
		t.Fatalf("stored %+v %v", got, err)
	}
	if a, activeErr := analyses.ActiveAnalysis(ctx, "org", "src_1", now.Add(-time.Hour)); !errors.Is(activeErr, discovery.ErrNotFound) {
		t.Fatalf("a pinned analysis blocks the branch: %+v %v", a, activeErr)
	}
	branch := &discovery.Analysis{ID: "anl_branch", OrgID: "org", ProjectID: "proj", SourceID: "src_1", Source: "mono",
		Status: discovery.StatusQueued, Repository: "acme/mono", CreatedBy: "usr", CreatedAt: now.Add(-time.Minute)}
	if err = analyses.CreateAnalysis(ctx, branch); err != nil {
		t.Fatal(err)
	}
	if a, activeErr := analyses.ActiveAnalysis(ctx, "org", "src_1", now.Add(-time.Hour)); activeErr != nil || a.ID != "anl_branch" || a.Pinned {
		t.Fatalf("running branch analysis %+v %v", a, activeErr)
	}
}

// TestPgStore_releaseRoutesAllowsTheTargetsHandoffs: a route whose handoff
// a release still holds blocks every plan, except a rollback of the plan
// that prepared that handoff.
func TestPgStore_releaseRoutesAllowsTheTargetsHandoffs(t *testing.T) {
	ctx, store, pg := promotionDB(t)
	route := map[string]any{"id": "route_1", "org_id": "org", "project_id": "proj", "status": "ready",
		"input": map[string]any{"environment_id": "env_b", "service": "web", "hostname": "shop.example.com"}}
	body, err := json.Marshal(route)
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(Result{Connections: map[string]string{}, Problems: []Finding{}, Warnings: []Finding{}, Actions: []Action{{ID: "service:web", Op: Replace, Subject: ServiceSubject, Name: "web",
		Routes: []cloudinfra.Route{{ID: "route_1"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		// The route's zone, server and connection don't matter here; this
		// database is the test's own.
		{`ALTER TABLE loomplane_domain_routes DISABLE TRIGGER USER`, nil},
		{`INSERT INTO loomplane_domain_routes(id,org_id,project_id,environment_id,cloud_id,server_id,connection_id,zone_id,hostname,status,revision,body)
			VALUES('route_1','org','proj','env_b','cloud','server','conn','zone','shop.example.com','ready',1,$1)`, []any{body}},
		{`UPDATE loomplane_plans SET result=$1 WHERE id='plan_old'`, []any{result}},
		{`INSERT INTO loomplane_route_handoffs(id,org_id,route_id,source_connection_id,destination_connection_id) VALUES($1,'org','route_1','conn','conn')`,
			[]any{cloudinfra.ReleasePrepareID("plan_old/service:web", "route_1")}},
	} {
		if _, err = pg.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.sql, err)
		}
	}
	if _, err = store.ReleaseRoutes(ctx, "org", "proj", "env_b", ""); err == nil || !strings.Contains(err.Error(), "finish the retained route handoff for shop.example.com") {
		t.Fatalf("a release while plan_old holds the handoff: %v", err)
	}
	routes, err := store.ReleaseRoutes(ctx, "org", "proj", "env_b", "plan_old")
	if err != nil || len(routes) != 1 || routes[0].ID != "route_1" {
		t.Fatalf("a rollback of plan_old: %+v, %v", routes, err)
	}
	// Another plan's handoff blocks the rollback too.
	if _, err = pg.Exec(ctx, `UPDATE loomplane_route_handoffs SET id='routeop_other' WHERE route_id='route_1'`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReleaseRoutes(ctx, "org", "proj", "env_b", "plan_old"); err == nil {
		t.Fatal("a rollback of plan_old passed another plan's handoff")
	}
}
