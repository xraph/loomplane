//go:build integration

package projects

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
)

// teamDB is a fresh database migrated with every group Loomplane runs,
// holding projects proj_a in org o and proj_x in org x, and a team in each.
func teamDB(t *testing.T) *pgdriver.PgDB {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the project store tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("projects_team_%d", time.Now().UnixNano())
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
	t.Cleanup(func() { _ = pg.Close() })
	for _, stmt := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES
			('proj_a','o','A','a','ready','ausr_ada',now(),now()),
			('proj_x','x','X','x','ready','ausr_ada',now(),now())`,
		`INSERT INTO loomplane_teams(id,org_id,name,slug,created_by) VALUES
			('team_o','o','Payments','payments','ausr_ada'),
			('team_x','x','Payments','payments','ausr_ada')`,
	} {
		if _, err = pg.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return pg
}

func TestSetResponsibleTeam(t *testing.T) {
	ctx := context.Background()
	s := NewPgStore(teamDB(t))

	p, err := s.SetResponsibleTeam(ctx, "o", "proj_a", "team_o")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if p.ResponsibleTeamID != "team_o" || p.ID != "proj_a" {
		t.Fatalf("after set: %+v", p)
	}
	if got, gerr := s.GetProject(ctx, "o", "proj_a"); gerr != nil || got.ResponsibleTeamID != "team_o" {
		t.Fatalf("GetProject reads %q, %v", got.ResponsibleTeamID, gerr)
	}
	list, err := s.ListProjects(ctx, "o")
	if err != nil || len(list) != 1 || list[0].ResponsibleTeamID != "team_o" {
		t.Fatalf("ListProjects reads %+v, %v", list, err)
	}

	// Another workspace's team is refused and changes nothing.
	if _, err = s.SetResponsibleTeam(ctx, "o", "proj_a", "team_x"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("another org's team: %v", err)
	}
	if _, err = s.SetResponsibleTeam(ctx, "o", "proj_a", "team_missing"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("a missing team: %v", err)
	}
	if got, _ := s.GetProject(ctx, "o", "proj_a"); got.ResponsibleTeamID != "team_o" {
		t.Fatalf("a refused change wrote %q", got.ResponsibleTeamID)
	}

	// A project in another workspace, or none, is not found, team or not.
	for _, tc := range []struct{ id, team string }{{"proj_x", "team_o"}, {"proj_x", ""}, {"proj_none", "team_o"}, {"proj_none", ""}} {
		if _, err = s.SetResponsibleTeam(ctx, "o", tc.id, tc.team); !errors.Is(err, ErrNotFound) {
			t.Fatalf("project %s with team %q: %v", tc.id, tc.team, err)
		}
	}

	// "" clears it.
	if p, err = s.SetResponsibleTeam(ctx, "o", "proj_a", ""); err != nil || p.ResponsibleTeamID != "" {
		t.Fatalf("clear: %+v, %v", p, err)
	}
	if got, _ := s.GetProject(ctx, "o", "proj_a"); got.ResponsibleTeamID != "" {
		t.Fatalf("after clearing GetProject reads %q", got.ResponsibleTeamID)
	}
	var null bool
	if err = s.db.QueryRow(ctx, `SELECT responsible_team_id IS NULL FROM loomplane_projects WHERE id = 'proj_a'`).Scan(&null); err != nil || !null {
		t.Fatalf("cleared column is NULL: %v, %v", null, err)
	}

	// Deleting the team clears it too.
	if _, err = s.SetResponsibleTeam(ctx, "o", "proj_a", "team_o"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(ctx, `DELETE FROM loomplane_teams WHERE id = 'team_o'`); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetProject(ctx, "o", "proj_a"); got.ResponsibleTeamID != "" {
		t.Fatalf("after deleting the team GetProject reads %q", got.ResponsibleTeamID)
	}
}
