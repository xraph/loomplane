//go:build integration

package projects_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	_ "github.com/xraph/grove/drivers/pgdriver/pgmigrate" // registers the Postgres migration executor
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/projects"
)

// scratchStore is a PgStore over a fully migrated database of its own,
// dropped when the test ends.
func scratchStore(t *testing.T) *projects.PgStore {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the project store test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("projects_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	ex, err := migrate.NewExecutorFor(pg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = migrate.NewOrchestrator(ex, migrations.All()...).Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return projects.NewPgStore(pg)
}

// A project's kind is stored as created, a project made without one is an
// app, and an update doesn't touch it.
func TestPgStoreKind(t *testing.T) {
	ctx := context.Background()
	s := scratchStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	for _, p := range []*projects.Project{
		{ID: "proj_app", Slug: "app", Icon: "database"},
		{ID: "proj_svc", Slug: "svc", Icon: "rocket", Kind: projects.ProjectKindService},
	} {
		p.OrgID, p.Name, p.Color, p.Status, p.CreatedBy, p.CreatedAt, p.UpdatedAt = "org_a", p.Slug, "blue", projects.StatusReady, "usr_ada", now, now
		env := &projects.Environment{ID: "env_" + p.Slug, OrgID: "org_a", ProjectID: p.ID, Name: "production", Kind: projects.KindProduction, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateProject(ctx, p, []*projects.Environment{env}); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := s.GetProject(ctx, "org_a", "proj_svc")
	if err != nil {
		t.Fatal(err)
	}
	svc.Icon = "database"
	if err = s.UpdateProject(ctx, svc); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListProjects(ctx, "org_a")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range list {
		got[p.ID] = p.Kind
	}
	if got["proj_app"] != projects.ProjectKindApp || got["proj_svc"] != projects.ProjectKindService {
		t.Fatalf("kinds: %v", got)
	}
}

func TestPgStoreAutonomy(t *testing.T) {
	ctx := context.Background()
	s := scratchStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	p := &projects.Project{ID: "proj_a", OrgID: "org_a", Name: "Engine", Slug: "engine", Icon: "box", Color: "blue",
		Status: projects.StatusReady, CreatedBy: "usr_ada", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateProject(ctx, p, []*projects.Environment{{ID: "env_a", OrgID: "org_a", ProjectID: "proj_a", Name: "production", Kind: projects.KindProduction, CreatedAt: now, UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProject(ctx, "org_a", "proj_a")
	if err != nil {
		t.Fatal(err)
	}
	if got.AutonomousPromotion || got.MinStagingSoakSeconds != 0 {
		t.Fatalf("a new project: %+v", got)
	}

	on, soak := true, int64(1800)
	later := now.Add(time.Minute)
	got, before, err := s.SetAutonomy(ctx, "org_a", "proj_a", projects.AutonomyInput{AutonomousPromotion: &on, MinStagingSoakSeconds: &soak}, later)
	if err != nil {
		t.Fatal(err)
	}
	if before != (projects.Autonomy{}) {
		t.Fatalf("before the first change: %+v", before)
	}
	if !got.AutonomousPromotion || got.MinStagingSoakSeconds != 1800 || !got.UpdatedAt.Equal(later) {
		t.Fatalf("after SetAutonomy: %+v", got)
	}
	// A nil field keeps its value.
	off := false
	if got, before, err = s.SetAutonomy(ctx, "org_a", "proj_a", projects.AutonomyInput{AutonomousPromotion: &off}, later); err != nil {
		t.Fatal(err)
	}
	if before != (projects.Autonomy{AutonomousPromotion: true, MinStagingSoakSeconds: 1800}) {
		t.Fatalf("before turning it off: %+v", before)
	}
	if got.AutonomousPromotion || got.MinStagingSoakSeconds != 1800 {
		t.Fatalf("after turning it off: %+v", got)
	}
	if _, _, err = s.SetAutonomy(ctx, "org_a", "proj_a", projects.AutonomyInput{AutonomousPromotion: &on}, later); err != nil {
		t.Fatal(err)
	}

	// UpdateProject with a copy that predates the change leaves it alone.
	stale := *p
	stale.Name = "Analytical Engine"
	if err = s.UpdateProject(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetProject(ctx, "org_a", "proj_a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Analytical Engine" || !got.AutonomousPromotion || got.MinStagingSoakSeconds != 1800 {
		t.Fatalf("after a stale UpdateProject: %+v", got)
	}
	list, err := s.ListProjects(ctx, "org_a")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].AutonomousPromotion || list[0].MinStagingSoakSeconds != 1800 {
		t.Fatalf("list: %+v", list)
	}

	// Another organization's project reads as not found, and the database
	// refuses a soak beyond a week even if the service didn't.
	if _, _, err = s.SetAutonomy(ctx, "org_b", "proj_a", projects.AutonomyInput{AutonomousPromotion: &off}, later); !errors.Is(err, projects.ErrNotFound) {
		t.Fatalf("another organization: %v", err)
	}
	tooLong := int64(604860)
	if _, _, err = s.SetAutonomy(ctx, "org_a", "proj_a", projects.AutonomyInput{MinStagingSoakSeconds: &tooLong}, later); err == nil {
		t.Fatal("a soak beyond a week was stored")
	}
}

// Each change's before and after come from its own statement: a field it
// didn't send reads the same before and after, even while another change
// to that field lands at the same time.
func TestPgStoreAutonomyBeforeAndAfterAreOneChange(t *testing.T) {
	ctx := context.Background()
	s := scratchStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	p := &projects.Project{ID: "proj_a", OrgID: "org_a", Name: "Engine", Slug: "engine", Icon: "box", Color: "blue",
		Status: projects.StatusReady, CreatedBy: "usr_ada", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateProject(ctx, p, []*projects.Environment{{ID: "env_a", OrgID: "org_a", ProjectID: "proj_a", Name: "production", Kind: projects.KindProduction, CreatedAt: now, UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	const rounds = 60
	errs := make(chan error, 2*rounds)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range rounds {
			on := i%2 == 0
			got, before, err := s.SetAutonomy(ctx, "org_a", "proj_a", projects.AutonomyInput{AutonomousPromotion: &on}, now)
			switch {
			case err != nil:
				errs <- err
			case got.AutonomousPromotion != on || got.MinStagingSoakSeconds != before.MinStagingSoakSeconds:
				errs <- fmt.Errorf("promotion change: before %+v, after promotion %v soak %d", before, got.AutonomousPromotion, got.MinStagingSoakSeconds)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := range rounds {
			soak := int64(60 * (i + 1))
			got, before, err := s.SetAutonomy(ctx, "org_a", "proj_a", projects.AutonomyInput{MinStagingSoakSeconds: &soak}, now)
			switch {
			case err != nil:
				errs <- err
			case got.MinStagingSoakSeconds != soak || got.AutonomousPromotion != before.AutonomousPromotion:
				errs <- fmt.Errorf("soak change: before %+v, after promotion %v soak %d", before, got.AutonomousPromotion, got.MinStagingSoakSeconds)
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
