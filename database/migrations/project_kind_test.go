//go:build integration

package migrations

import (
	"strings"
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestProjectKind(t *testing.T) {
	// Databases record the version and name, so the filename can't drift.
	if m := migration(t, "project_kind"); m.Version != "20261225001100" {
		t.Fatalf("version %q", m.Version)
	}
	conn, ex := scratch(t)
	if _, err := migrate.NewOrchestrator(ex, All()...).Migrate(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}

	// Projects made before the column, to show what the backfill decides.
	down(t, ex, "project_kind")
	exec(t, conn,
		`INSERT INTO loomplane_projects(id,org_id,name,slug,icon,status,created_by,created_at,updated_at) VALUES
			('p_dash','o','Dashboard database','dash','database','ready','u',now(),now()),
			('p_cli','o','CLI database','cli','box','ready','u',now(),now()),
			('p_icon','o','Picked the icon','icon','database','ready','u',now(),now()),
			('p_repo','o','Has a repository','repo','database','ready','u',now(),now()),
			('p_more','o','Database and a cache','more','database','ready','u',now(),now()),
			('p_none','o','No manifest','none','database','ready','u',now(),now()),
			('p_later','o','Grew a service','later','database','ready','u',now(),now()),
			('p_bad','o','Broken manifest','bad','database','ready','u',now(),now())`,
		`INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES
			('m_dash','o','p_dash',1,'version: 1
resources:
  database:
    kind: postgres
    provider: server
    version: "17"
    target: "conn_1"
','d','u',now()),
			('m_cli','o','p_cli',1,'version: 1
defaults: {}
resources:
  database:
    kind: redis
    provider: kubernetes
','d','u',now()),
			('m_icon','o','p_icon',1,'version: 1
services:
  web:
    path: .
','d','u',now()),
			('m_repo','o','p_repo',1,'version: 1
resources:
  database:
    kind: postgres
','d','u',now()),
			('m_more','o','p_more',1,'version: 1
resources:
  database:
    kind: postgres
  cache:
    kind: redis
','d','u',now()),
			('m_later1','o','p_later',1,'version: 1
resources:
  database:
    kind: postgres
','d','u',now()),
			('m_later2','o','p_later',2,'version: 1
services:
  api:
    path: api
resources:
  database:
    kind: postgres
','d','u',now()),
			('m_bad','o','p_bad',1,'resources: [','d','u',now())`,
		`INSERT INTO loomplane_project_sources(id,org_id,project_id,alias,repository,created_by,created_at,updated_at) VALUES
			('s_repo','o','p_repo','app','acme/app','u',now(),now())`,
	)
	up(t, ex, "project_kind")
	// Running the up again changes nothing.
	up(t, ex, "project_kind")

	got := strs(t, conn, `SELECT id||' '||kind FROM loomplane_projects ORDER BY id`)
	want := []string{
		"p_bad app", "p_cli service", "p_dash service", "p_icon app",
		"p_later app", "p_more app", "p_none app", "p_repo app",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("kinds:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// A new project is an app, and a kind is one of the two.
	exec(t, conn, `INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p_new','o','New','new','ready','u',now(),now())`)
	if got := strs(t, conn, `SELECT kind FROM loomplane_projects WHERE id='p_new'`); got[0] != "app" {
		t.Fatalf("default kind: %v", got)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE loomplane_projects SET kind='database' WHERE id='p_new'`); err == nil {
		t.Fatal("an unknown kind was accepted")
	}

	// The realtime project event carries it, under the API's name.
	exec(t, conn, `UPDATE loomplane_projects SET kind='service' WHERE id='p_new'`)
	if got := strs(t, conn, `SELECT data->>'kind' FROM loomplane_events
		WHERE type='project.updated' AND entity_id='p_new' ORDER BY sequence DESC LIMIT 1`); len(got) != 1 || got[0] != "service" {
		t.Fatalf("project event: %v", got)
	}

	// The down drops the column, and the up brings it back.
	down(t, ex, "project_kind")
	if got := strs(t, conn, `SELECT count(*)::text FROM information_schema.columns WHERE table_name='loomplane_projects' AND column_name='kind'`); got[0] != "0" {
		t.Fatalf("column after down: %v", got)
	}
	up(t, ex, "project_kind")
	if got := strs(t, conn, `SELECT kind FROM loomplane_projects WHERE id='p_dash'`); got[0] != "service" {
		t.Fatalf("after down and up: %v", got)
	}
}
