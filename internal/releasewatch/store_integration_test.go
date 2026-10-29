//go:build integration

package releasewatch

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/releasehealth"
)

// watchDB is a fresh, migrated database with one project, its production
// environment and n succeeded deployments, dep_0 to dep_<n-1>, each of
// its own plan.
func watchDB(t *testing.T, n int) (context.Context, *PgStore, *pgdriver.PgDB) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the release watch store tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("release_watches_%d", time.Now().UnixNano())
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
			('env_p', 'org', 'proj', 'production', 'production', now(), now())`,
		`INSERT INTO loomplane_manifests (id, org_id, project_id, revision, body, digest, created_by, created_at)
			VALUES ('man_1', 'org', 'proj', 1, 'version: 1', 'd', 'usr', now())`,
	} {
		if _, err = pg.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	for i := range n {
		plan := fmt.Sprintf("plan_%d", i)
		if _, err = pg.Exec(ctx, `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at)
			VALUES($1,'org','proj','env_p','production','approved','man_1',1,'usr',now())`, plan); err != nil {
			t.Fatal(err)
		}
		if _, err = pg.Exec(ctx, `INSERT INTO loomplane_deployments (id, org_id, project_id, environment_id, environment, plan_id, commit_sha, status, started_by, started_at, finished_at)
			VALUES ($1, 'org', 'proj', 'env_p', 'production', $2, 'c', 'succeeded', 'usr', now(), now())`, fmt.Sprintf("dep_%d", i), plan); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, NewPgStore(pg), pg
}

func storedWatch(i int, starts time.Time) *Watch {
	at := starts.UTC().Truncate(time.Microsecond)
	return &Watch{
		ID: fmt.Sprintf("watch_%d", i), OrgID: "org", ProjectID: "proj", EnvironmentID: "env_p", Environment: "production",
		DeploymentID: fmt.Sprintf("dep_%d", i), PlanID: fmt.Sprintf("plan_%d", i), Status: StatusWatching, Thresholds: releasehealth.DefaultThresholds(),
		Services:  []WatchedService{{Name: "api", Provider: "render", BindingID: "bind_api", Op: "update", Revision: "rev_1"}},
		Unwatched: []UnwatchedService{{Name: "site", Provider: "vercel", Reason: "not watched: no metrics."}}, Unverified: true,
		StartsAt: at, EndsAt: at.Add(15 * time.Minute), BoundAt: at.Add(30 * time.Minute), CreatedAt: at, UpdatedAt: at,
	}
}

func TestPgStore_insertsOneWatchPerDeployment(t *testing.T) {
	ctx, store, _ := watchDB(t, 1)
	start := time.Now()
	w := storedWatch(0, start)
	if ok, err := store.Insert(ctx, w); err != nil || !ok || w.Revision != 1 {
		t.Fatalf("insert %v %v revision %d", ok, err, w.Revision)
	}
	again := storedWatch(0, start)
	again.ID = "watch_again"
	if ok, err := store.Insert(ctx, again); err != nil || ok {
		t.Fatalf("second insert %v %v", ok, err)
	}
	got, err := store.ForDeployment(ctx, "org", "dep_0")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "watch_0" || got.Thresholds != w.Thresholds || !slices.Equal(got.Services, w.Services) ||
		!slices.Equal(got.Unwatched, w.Unwatched) || got.Held == nil || len(got.Held) != 0 || !got.Unverified ||
		!got.StartsAt.Equal(w.StartsAt) || got.CheckedAt != nil || got.Revision != 1 {
		t.Fatalf("read %+v", got)
	}
	if _, err = store.ForDeployment(ctx, "other", "dep_0"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another organization read it: %v", err)
	}
	if latest, err := store.Latest(ctx, "org", "env_p"); err != nil || latest.ID != "watch_0" {
		t.Fatalf("latest %+v %v", latest, err)
	}
}

func TestPgStore_dueWatchesAreClaimedOnce(t *testing.T) {
	const n = 12
	ctx, store, _ := watchDB(t, n)
	start := time.Now().Add(-time.Hour)
	for i := range n {
		w := storedWatch(i, start.Add(time.Duration(i)*time.Second))
		if i == n-1 {
			w.Status = StatusPassed
		}
		if _, err := store.Insert(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	var mu sync.Mutex
	var wg sync.WaitGroup
	claimed := map[string]int{}
	for range 4 {
		wg.Go(func() {
			got, err := store.Due(ctx, now, 3)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, w := range got {
				claimed[w.ID]++
				if w.CheckedAt == nil || !w.CheckedAt.Equal(now) {
					t.Errorf("%s claimed without its stamp: %v", w.ID, w.CheckedAt)
				}
			}
		})
	}
	wg.Wait()
	if len(claimed) != n-1 {
		t.Fatalf("claimed %d watches, want %d: %v", len(claimed), n-1, claimed)
	}
	for id, times := range claimed {
		if times != 1 {
			t.Fatalf("%s claimed %d times", id, times)
		}
	}
	if _, ok := claimed[fmt.Sprintf("watch_%d", n-1)]; ok {
		t.Fatal("a settled watch was claimed")
	}
	// Nothing is due again until it rests 50 seconds.
	if got, err := store.Due(ctx, now.Add(49*time.Second), 50); err != nil || len(got) != 0 {
		t.Fatalf("claimed again %d %v", len(got), err)
	}
	if got, err := store.Due(ctx, now.Add(50*time.Second), 50); err != nil || len(got) != n-1 {
		t.Fatalf("claimed after the rest %d %v", len(got), err)
	}
}

func TestPgStore_saveRefusesAStaleRevision(t *testing.T) {
	ctx, store, _ := watchDB(t, 1)
	w := storedWatch(0, time.Now())
	if _, err := store.Insert(ctx, w); err != nil {
		t.Fatal(err)
	}
	first, _ := store.ForDeployment(ctx, "org", "dep_0")
	second, _ := store.ForDeployment(ctx, "org", "dep_0")
	settled := time.Now().UTC().Truncate(time.Microsecond)
	first.Status, first.Verdict, first.Reason, first.SettledAt, first.RollbackPlanID = StatusRollingBack, "fail", "api: too many errors.", &settled, "plan_rb"
	first.RollbackApprovedAt = &settled
	first.Services[0].Verdict = "fail"
	if err := store.Save(ctx, first); err != nil || first.Revision != 2 {
		t.Fatalf("save %v revision %d", err, first.Revision)
	}
	second.Status = StatusPassed
	if err := store.Save(ctx, second); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale save %v", err)
	}
	got, _ := store.ForDeployment(ctx, "org", "dep_0")
	if got.Status != StatusRollingBack || got.Reason != "api: too many errors." || got.RollbackPlanID != "plan_rb" ||
		got.RollbackApprovedAt == nil || !got.SettledAt.Equal(settled) || got.Services[0].Verdict != "fail" || got.Revision != 2 {
		t.Fatalf("read %+v", got)
	}
}

func TestReleaseWatchEvents(t *testing.T) {
	ctx, store, pg := watchDB(t, 1)
	w := storedWatch(0, time.Now())
	if _, err := store.Insert(ctx, w); err != nil {
		t.Fatal(err)
	}
	w.Status = StatusPassed
	if err := store.Save(ctx, w); err != nil {
		t.Fatal(err)
	}
	rows, err := pg.Query(ctx, `SELECT type, project_id, entity_id, data->>'status' FROM loomplane_events WHERE type LIKE 'release_watch.%' ORDER BY sequence`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var typ, project, entity, status string
		if err = rows.Scan(&typ, &project, &entity, &status); err != nil {
			t.Fatal(err)
		}
		got = append(got, typ+"|"+project+"|"+entity+"|"+status)
	}
	want := []string{"release_watch.updated|proj|watch_0|watching", "release_watch.updated|proj|watch_0|passed"}
	if !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if _, err = pg.Exec(ctx, `DELETE FROM loomplane_deployments WHERE id='dep_0'`); err != nil {
		t.Fatal(err)
	}
	var deleted int
	if err = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_events WHERE type='release_watch.deleted' AND project_id='proj'`).Scan(&deleted); err != nil || deleted != 1 {
		t.Fatalf("deleted events %d %v", deleted, err)
	}
}
