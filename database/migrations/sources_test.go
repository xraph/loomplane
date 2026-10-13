//go:build integration

package migrations

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
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

func TestPlanSources_eachPlanKeepsItsCommit(t *testing.T) {
	conn := scratch(t)
	exec(t, conn, projectsUp...)
	exec(t, conn, analysesUp...)
	exec(t, conn, variablesUp...)
	exec(t, conn, projectStyleUp...)
	exec(t, conn, planningUp...)
	exec(t, conn,
		`INSERT INTO loomplane_projects (id, org_id, name, slug, repository_url, status, created_by, created_at, updated_at) VALUES
			('proj_a', 'org', 'A', 'a', 'https://github.com/acme/mono', 'ready', 'usr', now(), now())`,
		`INSERT INTO loomplane_environments (id, org_id, project_id, name, kind, created_at, updated_at) VALUES
			('env_a', 'org', 'proj_a', 'production', 'production', now(), now())`,
		`INSERT INTO loomplane_analyses (id, org_id, project_id, status, repository, commit_sha, created_by, created_at) VALUES
			('anl_a', 'org', 'proj_a', 'succeeded', 'acme/mono', 'abc', 'usr', now())`,
		`INSERT INTO loomplane_manifests (id, org_id, project_id, revision, body, digest, created_by, created_at) VALUES
			('man_a', 'org', 'proj_a', 1, 'version: 1', 'd', 'usr', now())`,
		`INSERT INTO loomplane_plans (id, org_id, project_id, environment_id, environment, status, analysis_id, commit_sha,
			manifest_id, manifest_revision, created_by, created_at) VALUES
			('plan_a', 'org', 'proj_a', 'env_a', 'production', 'approved', 'anl_a', 'abc', 'man_a', 1, 'usr', now())`,
	)
	exec(t, conn, sourcesUp...)
	exec(t, conn, planSourcesUp...)
	exec(t, conn, planSourcesUp...)
	got := strs(t, conn, `SELECT plan_id || ' ' || alias || ' ' || analysis_id || ' ' || commit_sha || ' ' || in_scope::text FROM loomplane_plan_sources`)
	if !slices.Equal(got, []string{"plan_a mono anl_a abc true"}) {
		t.Fatalf("plan sources %q", got)
	}
}

func TestDeploymentSources_aStoredSourceBecomesTheOnlyOne(t *testing.T) {
	conn := scratch(t)
	exec(t, conn, projectsUp...)
	exec(t, conn, analysesUp...)
	exec(t, conn, variablesUp...)
	exec(t, conn, projectStyleUp...)
	exec(t, conn, planningUp...)
	exec(t, conn, deploymentsUp...)
	exec(t, conn,
		`INSERT INTO loomplane_projects (id, org_id, name, slug, repository_url, status, created_by, created_at, updated_at) VALUES
			('proj_a', 'org', 'A', 'a', 'https://github.com/acme/mono', 'ready', 'usr', now(), now())`,
		`INSERT INTO loomplane_environments (id, org_id, project_id, name, kind, created_at, updated_at) VALUES
			('env_a', 'org', 'proj_a', 'production', 'production', now(), now())`,
		`INSERT INTO loomplane_analyses (id, org_id, project_id, status, repository, commit_sha, created_by, created_at) VALUES
			('anl_a', 'org', 'proj_a', 'succeeded', 'acme/mono', 'abc', 'usr', now())`,
		`INSERT INTO loomplane_manifests (id, org_id, project_id, revision, body, digest, created_by, created_at) VALUES
			('man_a', 'org', 'proj_a', 1, 'version: 1', 'd', 'usr', now())`,
		`INSERT INTO loomplane_plans (id, org_id, project_id, environment_id, environment, status, analysis_id, commit_sha,
			manifest_id, manifest_revision, created_by, created_at) VALUES
			('plan_a', 'org', 'proj_a', 'env_a', 'production', 'approved', 'anl_a', 'abc', 'man_a', 1, 'usr', now()),
			('plan_b', 'org', 'proj_a', 'env_a', 'production', 'approved', 'anl_a', 'abc', 'man_a', 1, 'usr', now())`,
		`INSERT INTO loomplane_deployments (id, org_id, project_id, environment_id, environment, plan_id, commit_sha, status,
			source, started_by, started_at) VALUES
			('dep_a', 'org', 'proj_a', 'env_a', 'production', 'plan_a', 'abc', 'running', 'k', 'usr', now()),
			('dep_b', 'org', 'proj_a', 'env_a', 'production', 'plan_b', 'abc', 'running', '', 'usr', now())`,
	)
	exec(t, conn, deploymentSourcesUp...)
	exec(t, conn, deploymentSourcesUp...)
	got := strs(t, conn, `SELECT id || ' ' || sources::text FROM loomplane_deployments ORDER BY id`)
	want := []string{
		`dep_a [{"key": "k", "alias": "", "commit_sha": "abc", "repository": ""}]`,
		`dep_b []`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("deployment sources %q, want %q", got, want)
	}
}

func TestEnvironmentSourcesBackfill_eachEnvironmentKeepsItsNewestSucceededDeployment(t *testing.T) {
	conn := scratch(t)
	for _, up := range [][]string{projectsUp, analysesUp, variablesUp, projectStyleUp, planningUp,
		deploymentsUp, bindingURLUp} {
		exec(t, conn, up...)
	}
	exec(t, conn,
		`INSERT INTO loomplane_projects (id, org_id, name, slug, repository_url, status, created_by, created_at, updated_at) VALUES
			('proj_a', 'org', 'A', 'a', 'https://github.com/acme/mono', 'ready', 'usr', now(), now())`,
		`INSERT INTO loomplane_environments (id, org_id, project_id, name, kind, created_at, updated_at) VALUES
			('env_a', 'org', 'proj_a', 'production', 'production', now(), now()),
			('env_b', 'org', 'proj_a', 'staging', 'staging', now(), now())`,
		`INSERT INTO loomplane_analyses (id, org_id, project_id, status, repository, commit_sha, created_by, created_at) VALUES
			('anl_old', 'org', 'proj_a', 'succeeded', 'acme/mono', 'old', 'usr', now()),
			('anl_bad', 'org', 'proj_a', 'succeeded', 'acme/mono', 'bad', 'usr', now()),
			('anl_new', 'org', 'proj_a', 'succeeded', 'acme/mono', 'new', 'usr', now()),
			('anl_late', 'org', 'proj_a', 'succeeded', 'acme/mono', 'late', 'usr', now())`,
		`INSERT INTO loomplane_manifests (id, org_id, project_id, revision, body, digest, created_by, created_at) VALUES
			('man_a', 'org', 'proj_a', 1, 'version: 1', 'd', 'usr', now())`,
		`INSERT INTO loomplane_plans (id, org_id, project_id, environment_id, environment, status, analysis_id, commit_sha,
			manifest_id, manifest_revision, created_by, created_at) VALUES
			('plan_old', 'org', 'proj_a', 'env_a', 'production', 'approved', 'anl_old', 'old', 'man_a', 1, 'usr', now()),
			('plan_bad', 'org', 'proj_a', 'env_a', 'production', 'approved', 'anl_bad', 'bad', 'man_a', 1, 'usr', now()),
			('plan_new', 'org', 'proj_a', 'env_a', 'production', 'approved', 'anl_new', 'new', 'man_a', 1, 'usr', now()),
			('plan_late', 'org', 'proj_a', 'env_a', 'production', 'approved', 'anl_late', 'late', 'man_a', 1, 'usr', now())`,
	)
	exec(t, conn, sourcesUp...)
	exec(t, conn, planSourcesUp...)
	exec(t, conn, deploymentSourcesUp...)
	// A second repository moved by the older plan and held by the newest
	// one: what's deployed of it is what the older plan moved.
	exec(t, conn,
		`INSERT INTO loomplane_project_sources (id, org_id, project_id, alias, repository, created_by, created_at, updated_at) VALUES
			('src_web', 'org', 'proj_a', 'web', 'acme/web', 'usr', now(), now())`,
		`INSERT INTO loomplane_analyses (id, org_id, project_id, status, repository, commit_sha, source_id, alias, created_by, created_at) VALUES
			('anl_web1', 'org', 'proj_a', 'succeeded', 'acme/web', 'w1', 'src_web', 'web', 'usr', now()),
			('anl_web2', 'org', 'proj_a', 'succeeded', 'acme/web', 'w2', 'src_web', 'web', 'usr', now())`,
		`INSERT INTO loomplane_plan_sources (plan_id, source_id, alias, repository, analysis_id, commit_sha, in_scope) VALUES
			('plan_old', 'src_web', 'web', 'acme/web', 'anl_web1', 'w1', TRUE),
			('plan_new', 'src_web', 'web', 'acme/web', 'anl_web2', 'w2', FALSE)`,
	)
	// An older success, a failure, the newest success, then a later failure
	// that mustn't count. staging was never deployed, so it gets no row.
	exec(t, conn,
		`INSERT INTO loomplane_deployments (id, org_id, project_id, environment_id, environment, plan_id, commit_sha, status,
			started_by, started_at, finished_at) VALUES
			('dep_old', 'org', 'proj_a', 'env_a', 'production', 'plan_old', 'old', 'succeeded', 'usr', now(), now() - interval '4 hours'),
			('dep_bad', 'org', 'proj_a', 'env_a', 'production', 'plan_bad', 'bad', 'failed', 'usr', now(), now() - interval '3 hours'),
			('dep_new', 'org', 'proj_a', 'env_a', 'production', 'plan_new', 'new', 'succeeded', 'usr', now(), now() - interval '2 hours'),
			('dep_late', 'org', 'proj_a', 'env_a', 'production', 'plan_late', 'late', 'failed', 'usr', now(), now() - interval '1 hour')`,
	)
	exec(t, conn, environmentSourcesBackfillUp...)
	exec(t, conn, environmentSourcesBackfillUp...)

	got := strs(t, conn, `SELECT e.environment_id || ' ' || s.alias || ' ' || e.org_id || ' ' || e.analysis_id || ' ' ||
			e.commit_sha || ' ' || e.plan_id || ' ' || (e.deployed_at = d.finished_at)::text
		FROM loomplane_environment_sources e
		JOIN loomplane_project_sources s ON s.id = e.source_id
		JOIN loomplane_deployments d ON d.plan_id = e.plan_id
		ORDER BY e.environment_id, s.alias`)
	want := []string{
		"env_a mono org anl_new new plan_new true",
		"env_a web org anl_web1 w1 plan_old true",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("environment sources %q", got)
	}
}

func TestSourceRepositoryIndex_findsARepositoryWhateverItsCase(t *testing.T) {
	conn := scratch(t)
	exec(t, conn, projectsUp...)
	exec(t, conn, analysesUp...)
	exec(t, conn, variablesUp...)
	exec(t, conn, projectStyleUp...)
	exec(t, conn,
		`INSERT INTO loomplane_projects (id, org_id, name, slug, repository_url, status, created_by, created_at, updated_at) VALUES
			('proj_a', 'org', 'A', 'a', 'https://github.com/Acme/Storefront', 'ready', 'usr', now(), now())`)
	exec(t, conn, sourcesUp...)
	exec(t, conn, sourceRepositoryIndexUp...)
	exec(t, conn, sourceRepositoryIndexUp...)
	indexes := `SELECT indexname FROM pg_indexes WHERE tablename = 'loomplane_project_sources'
		AND indexname LIKE 'loomplane_project_sources_repository%' ORDER BY indexname`
	if got := strs(t, conn, indexes); !slices.Equal(got, []string{"loomplane_project_sources_repository_lower"}) {
		t.Fatalf("indexes %q", got)
	}
	// The webhook's lookup (discovery.PgStore.SourcesByRepository) finds
	// the source whatever case GitHub names it in, through the index.
	const lookup = `SELECT id FROM loomplane_project_sources
		WHERE lower(repository) = lower('acme/storefront') AND removed_at IS NULL`
	if got := strs(t, conn, lookup); len(got) != 1 {
		t.Fatalf("found %q", got)
	}
	exec(t, conn, `SET enable_seqscan = off`)
	plan := strings.Join(strs(t, conn, `EXPLAIN `+lookup), "\n")
	exec(t, conn, `SET enable_seqscan = on`)
	if !strings.Contains(plan, "loomplane_project_sources_repository_lower") {
		t.Fatalf("the lookup doesn't use the index:\n%s", plan)
	}
	exec(t, conn, sourceRepositoryIndexDown...)
	if got := strs(t, conn, indexes); !slices.Equal(got, []string{"loomplane_project_sources_repository"}) {
		t.Fatalf("indexes after down %q", got)
	}
}
