//go:build integration

package migrations

import (
	"slices"
	"testing"
)

func TestEnvironmentManifestsBackfill(t *testing.T) {
	conn, db := scratch(t)
	up(t, db, "create_projects", "create_analyses", "create_variables", "add_project_style", "create_planning",
		"create_deployments", "add_binding_url", "create_project_sources", "create_plan_sources", "add_deployment_sources",
		"standalone_catalog_plans")
	exec(t, conn,
		`INSERT INTO loomplane_projects (id, org_id, name, slug, repository_url, status, created_by, created_at, updated_at) VALUES
			('proj_a', 'org', 'A', 'a', 'https://github.com/acme/mono', 'ready', 'usr', now(), now())`,
		`INSERT INTO loomplane_environments (id, org_id, project_id, name, kind, created_at, updated_at) VALUES
			('env_a', 'org', 'proj_a', 'staging', 'staging', now(), now()),
			('env_b', 'org', 'proj_a', 'production', 'production', now(), now())`,
		`INSERT INTO loomplane_manifests (id, org_id, project_id, revision, body, digest, created_by, created_at) VALUES
			('man_3', 'org', 'proj_a', 3, 'version: 3', 'd3', 'usr', now()),
			('man_5', 'org', 'proj_a', 5, 'version: 5', 'd5', 'usr', now()),
			('man_6', 'org', 'proj_a', 6, 'version: 6', 'd6', 'usr', now())`,
		`INSERT INTO loomplane_plans (id, org_id, project_id, environment_id, environment, status, manifest_id, manifest_revision,
			result, created_by, created_at) VALUES
			('plan_3', 'org', 'proj_a', 'env_a', 'staging', 'approved', 'man_3', 3, '{}', 'usr', now()),
			('plan_5', 'org', 'proj_a', 'env_a', 'staging', 'approved', 'man_5', 5, '{}', 'usr', now()),
			('plan_6', 'org', 'proj_a', 'env_a', 'staging', 'approved', 'man_6', 6, '{}', 'usr', now()),
			('plan_sync', 'org', 'proj_a', 'env_b', 'production', 'approved', 'man_6', 6, '{"cloud_sync_id":"sync_1"}', 'usr', now())`,
		`INSERT INTO loomplane_deployments (id, org_id, project_id, environment_id, environment, plan_id, commit_sha, status,
			started_by, started_at, finished_at) VALUES
			('dep_3', 'org', 'proj_a', 'env_a', 'staging', 'plan_3', 'c', 'succeeded', 'usr', now(), now() - interval '3 hours'),
			('dep_5', 'org', 'proj_a', 'env_a', 'staging', 'plan_5', 'c', 'succeeded', 'usr', now(), now() - interval '2 hours'),
			('dep_6', 'org', 'proj_a', 'env_a', 'staging', 'plan_6', 'c', 'failed', 'usr', now(), now() - interval '1 hour'),
			('dep_sync', 'org', 'proj_a', 'env_b', 'production', 'plan_sync', 'c', 'succeeded', 'usr', now(), now())`,
	)
	up(t, db, "plan_promotion_rollback")
	got := strs(t, conn, `SELECT environment_id || ' ' || manifest_revision || ' ' || plan_id || ' ' || manifest_id
		FROM loomplane_environment_manifests ORDER BY environment_id`)
	if want := []string{"env_a 5 plan_5 man_5"}; !slices.Equal(got, want) {
		t.Fatalf("environment manifests %q, want %q", got, want)
	}
	// Plans made before the migration read as releases.
	if got := strs(t, conn, `SELECT kind || '|' || from_environment || '|' || coalesce(rollback::text, 'null') FROM loomplane_plans WHERE id = 'plan_3'`); !slices.Equal(got, []string{"release||null"}) {
		t.Fatalf("old plan %q", got)
	}
	down(t, db, "plan_promotion_rollback")
	up(t, db, "plan_promotion_rollback")
}
