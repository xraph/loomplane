//go:build integration

package migrations

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// scratch is an empty database of its own on the test server, dropped when
// the test ends.
func scratch(t *testing.T) *pgx.Conn {
	t.Helper()
	url := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("LOOMPLANE_TEST_DATABASE_URL isn't set; make test-integration sets it")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("loomplane_migrations_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = name
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func exec(t *testing.T, conn *pgx.Conn, stmts ...string) {
	t.Helper()
	for i, s := range stmts {
		if _, err := conn.Exec(context.Background(), s); err != nil {
			t.Fatalf("statement %d: %v", i+1, err)
		}
	}
}

func strs(t *testing.T, conn *pgx.Conn, query string) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSources_eachProjectsRepositoryBecomesItsFirstSource(t *testing.T) {
	conn := scratch(t)
	exec(t, conn, projectsUp...)
	exec(t, conn, analysesUp...)
	exec(t, conn, variablesUp...)
	exec(t, conn, projectStyleUp...)
	exec(t, conn,
		`INSERT INTO loomplane_projects (id, org_id, name, slug, repository_url, status, created_by, created_at, updated_at) VALUES
			('proj_a', 'org', 'A', 'a', 'https://github.com/acme/Storefront.git', 'ready', 'usr', now(), now()),
			('proj_b', 'org', 'B', 'b', 'git@github.com:acme/go.git', 'ready', 'usr', now(), now()),
			('proj_c', 'org', 'C', 'c', 'https://gitlab.com/acme/c', 'ready', 'usr', now(), now()),
			('proj_d', 'org', 'D', 'd', '', 'ready', 'usr', now(), now()),
			('proj_e', 'org', 'E', 'e', 'https://mygithub.com/o/r', 'ready', 'usr', now(), now()),
			('proj_f', 'org', 'F', 'f', 'https://github.com:443/acme/port', 'ready', 'usr', now(), now()),
			('proj_g', 'org', 'G', 'g', 'https://GitHub.com/acme/Upper', 'ready', 'usr', now(), now()),
			('proj_h', 'org', 'H', 'h', 'https://gist.github.com/user/abc123', 'ready', 'usr', now(), now()),
			('proj_i', 'org', 'I', 'i', 'https://gitlab.com/x/github.com/o/r', 'ready', 'usr', now(), now())`,
		`INSERT INTO loomplane_analyses (id, org_id, project_id, status, repository, created_by, created_at) VALUES
			('anl_a', 'org', 'proj_a', 'succeeded', 'acme/Storefront', 'usr', now())`,
		`INSERT INTO loomplane_variables (id, org_id, project_id, service, key, created_by, updated_by, created_at, updated_at) VALUES
			('var_a', 'org', 'proj_a', 'svc:go:cmd/api', 'PORT', 'usr', 'usr', now(), now()),
			('var_b', 'org', 'proj_a', '', 'LOG', 'usr', 'usr', now(), now())`,
	)
	// Twice: grove migrations aren't transactional, so one interrupted
	// halfway runs again from the top.
	exec(t, conn, sourcesUp...)
	exec(t, conn, sourcesUp...)

	got := strs(t, conn, `SELECT project_id || ' ' || alias || ' ' || repository || ' [' || root || '] [' || ref || ']'
		FROM loomplane_project_sources ORDER BY project_id`)
	want := []string{
		"proj_a storefront acme/Storefront [] []",
		"proj_b go-src acme/go [] []",
		"proj_f port acme/port [] []",
		"proj_g upper acme/Upper [] []",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sources %q, want %q", got, want)
	}
	got = strs(t, conn, `SELECT a.alias || ' ' || (a.source_id = s.id)::text
		FROM loomplane_analyses a JOIN loomplane_project_sources s ON s.project_id = a.project_id`)
	if !slices.Equal(got, []string{"storefront true"}) {
		t.Fatalf("analysis source %q", got)
	}
	got = strs(t, conn, `SELECT service FROM loomplane_variables ORDER BY id`)
	if !slices.Equal(got, []string{"svc:storefront:go:cmd/api", ""}) {
		t.Fatalf("variable services %q", got)
	}
}
