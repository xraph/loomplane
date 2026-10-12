// Package migrations holds Loomplane's schema as a grove migration group,
// and lists every library group Loomplane runs in its one migration pass.
//
// grove migrations aren't transactional, so each statement is written to be
// safe to re-run (IF NOT EXISTS) and runs in its own Exec call.
package migrations

import (
	"context"
	"fmt"

	authpg "github.com/xraph/authsome/store/postgres"
	chronpg "github.com/xraph/chronicle/store/postgres"
	cppg "github.com/xraph/ctrlplane/store/postgres"
	dispatchpg "github.com/xraph/dispatch/store/postgres"
	"github.com/xraph/grove/migrate"
	vaultpg "github.com/xraph/vault/store/postgres"
	wardenpg "github.com/xraph/warden/store/postgres"
)

// GroupName is the migration group for Loomplane's own tables.
const GroupName = "loomplane"

// Group builds Loomplane's migration group. A constructor rather than a
// package variable filled by init(), so tests and the app each get a fresh
// group.
func Group() *migrate.Group {
	g := migrate.NewGroup(GroupName)
	g.MustRegister(
		statements("20261011000001", "create_projects", projectsUp, projectsDown),
		statements("20261012000001", "create_analyses", analysesUp, analysesDown),
		statements("20261012000002", "create_github_installations", githubUp, githubDown),
		statements("20261012000003", "create_variables", variablesUp, variablesDown),
		statements("20261012000004", "add_project_style", projectStyleUp, projectStyleDown),
		statements("20261012000005", "create_planning", planningUp, planningDown),
		statements("20261012000006", "create_cloud_connections", cloudConnectionsUp, cloudConnectionsDown),
		statements("20261012000007", "create_environment_connections", environmentConnectionsUp, environmentConnectionsDown),
		statements("20261012000008", "create_deployments", deploymentsUp, deploymentsDown),
		statements("20261012000009", "add_binding_url", bindingURLUp, bindingURLDown),
		statements("20261013000001", "add_deployment_retries", deploymentRetriesUp, deploymentRetriesDown),
	)
	return g
}

// All is every group Loomplane migrates: its own, then each library that
// keeps tables in Loomplane's database. Each library's own extension has
// its self-migration turned off, so this one pass (Forge's central
// migrations, run by the grove extension) owns the schema.
func All() []*migrate.Group {
	return []*migrate.Group{
		Group(),
		authpg.Migrations,
		wardenpg.Migrations,
		chronpg.Migrations,
		dispatchpg.Migrations,
		cppg.Migrations,
		vaultpg.Migrations,
	}
}

func statements(version, name string, up, down []string) *migrate.Migration {
	run := func(stmts []string) migrate.MigrateFunc {
		return func(ctx context.Context, exec migrate.Executor) error {
			for i, s := range stmts {
				if _, err := exec.Exec(ctx, s); err != nil {
					return fmt.Errorf("%s statement %d: %w", name, i+1, err)
				}
			}
			return nil
		}
	}
	return &migrate.Migration{Name: name, Version: version, Up: run(up), Down: run(down)}
}

// Projects belong to an organization, an authsome organization ID. There's
// no foreign key to authsome's tables: they're authsome's, and Loomplane
// never writes them.
var projectsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_projects (
		id             TEXT        PRIMARY KEY,
		org_id         TEXT        NOT NULL,
		name           TEXT        NOT NULL,
		slug           TEXT        NOT NULL,
		description    TEXT        NOT NULL DEFAULT '',
		repository_url TEXT        NOT NULL DEFAULT '',
		status         TEXT        NOT NULL,
		created_by     TEXT        NOT NULL,
		created_at     TIMESTAMPTZ NOT NULL,
		updated_at     TIMESTAMPTZ NOT NULL,
		CONSTRAINT loomplane_projects_org_slug UNIQUE (org_id, slug)
	)`,
	`CREATE INDEX IF NOT EXISTS loomplane_projects_org_created
		ON loomplane_projects (org_id, created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS loomplane_environments (
		id         TEXT        PRIMARY KEY,
		org_id     TEXT        NOT NULL,
		project_id TEXT        NOT NULL REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		name       TEXT        NOT NULL,
		kind       TEXT        NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		CONSTRAINT loomplane_environments_project_name UNIQUE (project_id, name)
	)`,
	`CREATE INDEX IF NOT EXISTS loomplane_environments_org_project
		ON loomplane_environments (org_id, project_id)`,
}

var projectsDown = []string{
	`DROP TABLE IF EXISTS loomplane_environments`,
	`DROP TABLE IF EXISTS loomplane_projects`,
}

// An analysis is one run of repository discovery; a succeeded one holds a
// version of the project's application graph, whole, as JSON. attempts
// counts deliveries of its job, so the worker knows when a transient
// failure has become the result.
var analysesUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_analyses (
		id          TEXT        PRIMARY KEY,
		org_id      TEXT        NOT NULL,
		project_id  TEXT        NOT NULL REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		status      TEXT        NOT NULL,
		repository  TEXT        NOT NULL,
		ref         TEXT        NOT NULL DEFAULT '',
		commit_sha  TEXT        NOT NULL DEFAULT '',
		error       TEXT        NOT NULL DEFAULT '',
		summary     JSONB       NOT NULL DEFAULT '{}',
		graph       JSONB,
		attempts    INTEGER     NOT NULL DEFAULT 0,
		created_by  TEXT        NOT NULL,
		created_at  TIMESTAMPTZ NOT NULL,
		started_at  TIMESTAMPTZ,
		finished_at TIMESTAMPTZ
	)`,
	`CREATE INDEX IF NOT EXISTS loomplane_analyses_project_created
		ON loomplane_analyses (org_id, project_id, created_at DESC)`,
}

var analysesDown = []string{
	`DROP TABLE IF EXISTS loomplane_analyses`,
}

// A GitHub App installation an organization has connected. The same
// installation may be connected to several organizations, each by someone
// GitHub confirmed can see it. Nothing secret is stored: tokens are minted
// per analysis.
var githubUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_github_installations (
		org_id          TEXT        NOT NULL,
		installation_id BIGINT      NOT NULL,
		account         TEXT        NOT NULL,
		account_type    TEXT        NOT NULL DEFAULT '',
		created_by      TEXT        NOT NULL,
		created_at      TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (org_id, installation_id)
	)`,
}

var githubDown = []string{
	`DROP TABLE IF EXISTS loomplane_github_installations`,
}

// A variable set on one layer: organization-wide (no project), a project,
// or one of its services; for every environment or for one. A project's
// variable for one environment references it, so deleting either deletes
// the variable. An organization-wide one names its environment instead,
// since it applies to that environment in every project. A secret's value
// isn't here at all: it's in Vault, encrypted.
var variablesUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_variables (
		id             TEXT        PRIMARY KEY,
		org_id         TEXT        NOT NULL,
		project_id     TEXT        REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		environment_id TEXT        REFERENCES loomplane_environments (id) ON DELETE CASCADE,
		environment    TEXT        NOT NULL DEFAULT '',
		service        TEXT        NOT NULL DEFAULT '',
		key            TEXT        NOT NULL,
		value          TEXT        NOT NULL DEFAULT '',
		secret         BOOLEAN     NOT NULL DEFAULT FALSE,
		created_by     TEXT        NOT NULL,
		updated_by     TEXT        NOT NULL,
		created_at     TIMESTAMPTZ NOT NULL,
		updated_at     TIMESTAMPTZ NOT NULL,
		CONSTRAINT loomplane_variables_service_in_project CHECK (project_id IS NOT NULL OR service = ''),
		CONSTRAINT loomplane_variables_environment_in_project CHECK (project_id IS NOT NULL OR environment_id IS NULL),
		CONSTRAINT loomplane_variables_secret_not_stored CHECK (NOT secret OR value = '')
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_variables_layer_key
		ON loomplane_variables (org_id, COALESCE(project_id, ''), service, environment, key)`,
	`CREATE INDEX IF NOT EXISTS loomplane_variables_project
		ON loomplane_variables (org_id, project_id)`,
}

var variablesDown = []string{
	`DROP TABLE IF EXISTS loomplane_variables`,
}

// A project's icon and color draw its tile in the dashboard. The service
// picks both for new projects (internal/projects/style.go); the backfill
// gives every older project a pair. It hashes the ID rather than rolling
// dice, so a project comes out the same on every database it runs on, and
// it only fills empty values, so a re-run leaves earlier picks and later
// edits alone. Color hashes a prefixed ID: with the bare one, 48 and 10
// share a factor and an even icon would always get an even color. The
// bigint cast is for abs, which overflows on the one int4 with no positive
// twin.
//
// The arrays copy the lists in style.go, in the same order, as they stood
// when this was written.
var projectStyleUp = []string{
	`ALTER TABLE loomplane_projects ADD COLUMN IF NOT EXISTS icon TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE loomplane_projects ADD COLUMN IF NOT EXISTS color TEXT NOT NULL DEFAULT ''`,
	`UPDATE loomplane_projects p
		SET icon = n.names[1 + mod(abs(hashtext(p.id)::bigint), cardinality(n.names))]
		FROM (SELECT ARRAY[
			'store', 'bag', 'card', 'receipt', 'wallet', 'truck', 'bell', 'mail',
			'chat', 'megaphone', 'users', 'calendar', 'database', 'chart', 'search', 'sparkles',
			'bot', 'radar', 'server', 'cpu', 'webhook', 'braces', 'terminal', 'workflow',
			'package', 'layers', 'smartphone', 'globe', 'pin', 'health', 'learning', 'book',
			'film', 'music', 'image', 'gamepad', 'shield', 'key', 'fingerprint', 'rocket',
			'zap', 'compass', 'anchor', 'leaf', 'flame', 'mountain', 'atom', 'hexagon'
		] AS names) n
		WHERE p.icon = ''`,
	`UPDATE loomplane_projects p
		SET color = n.names[1 + mod(abs(hashtext('color:' || p.id)::bigint), cardinality(n.names))]
		FROM (SELECT ARRAY[
			'gold', 'orange', 'red', 'rose', 'violet', 'indigo', 'blue', 'teal', 'green', 'slate'
		] AS names) n
		WHERE p.color = ''`,
}

var projectStyleDown = []string{
	`ALTER TABLE loomplane_projects DROP COLUMN IF EXISTS color`,
	`ALTER TABLE loomplane_projects DROP COLUMN IF EXISTS icon`,
}

// Planning keeps three things. A manifest revision is one saved version of
// a project's loomplane.yaml; saving adds one and never edits one, so a
// plan can name exactly what it was made from. A plan is pinned to the
// analysis and manifest revision it was made from, with its result whole,
// as JSON. The partial unique index allows one open plan per environment,
// so of two started at once, one fails instead of both waiting for a
// decision. A binding records what runs one service or resource in one
// environment; deployments write them and the next plan reads them.
var planningUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_manifests (
		id         TEXT        PRIMARY KEY,
		org_id     TEXT        NOT NULL,
		project_id TEXT        NOT NULL REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		revision   INTEGER     NOT NULL,
		body       TEXT        NOT NULL,
		digest     TEXT        NOT NULL,
		created_by TEXT        NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		CONSTRAINT loomplane_manifests_project_revision UNIQUE (project_id, revision)
	)`,
	`CREATE TABLE IF NOT EXISTS loomplane_plans (
		id                TEXT        PRIMARY KEY,
		org_id            TEXT        NOT NULL,
		project_id        TEXT        NOT NULL REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		environment_id    TEXT        NOT NULL REFERENCES loomplane_environments (id) ON DELETE CASCADE,
		environment       TEXT        NOT NULL,
		status            TEXT        NOT NULL,
		revision          TEXT        NOT NULL DEFAULT '',
		analysis_id       TEXT        NOT NULL REFERENCES loomplane_analyses (id) ON DELETE CASCADE,
		commit_sha        TEXT        NOT NULL DEFAULT '',
		manifest_id       TEXT        NOT NULL REFERENCES loomplane_manifests (id) ON DELETE CASCADE,
		manifest_revision INTEGER     NOT NULL,
		result            JSONB,
		executable        BOOLEAN     NOT NULL DEFAULT FALSE,
		error             TEXT        NOT NULL DEFAULT '',
		created_by        TEXT        NOT NULL,
		created_at        TIMESTAMPTZ NOT NULL,
		planned_at        TIMESTAMPTZ,
		expires_at        TIMESTAMPTZ,
		decided_by        TEXT        NOT NULL DEFAULT '',
		decided_at        TIMESTAMPTZ,
		decision_note     TEXT        NOT NULL DEFAULT ''
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_plans_one_open
		ON loomplane_plans (environment_id) WHERE status IN ('planning', 'ready')`,
	`CREATE INDEX IF NOT EXISTS loomplane_plans_project_created
		ON loomplane_plans (org_id, project_id, created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS loomplane_bindings (
		id             TEXT        PRIMARY KEY,
		org_id         TEXT        NOT NULL,
		project_id     TEXT        NOT NULL REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		environment_id TEXT        NOT NULL REFERENCES loomplane_environments (id) ON DELETE CASCADE,
		subject        TEXT        NOT NULL,
		name           TEXT        NOT NULL,
		provider       TEXT        NOT NULL,
		region         TEXT        NOT NULL DEFAULT '',
		engine         TEXT        NOT NULL,
		external_id    TEXT        NOT NULL DEFAULT '',
		config         JSONB       NOT NULL DEFAULT '{}',
		plan_id        TEXT        NOT NULL DEFAULT '',
		created_at     TIMESTAMPTZ NOT NULL,
		updated_at     TIMESTAMPTZ NOT NULL,
		CONSTRAINT loomplane_bindings_subject UNIQUE (environment_id, subject, name)
	)`,
}

var planningDown = []string{
	`DROP TABLE IF EXISTS loomplane_bindings`,
	`DROP TABLE IF EXISTS loomplane_plans`,
	`DROP TABLE IF EXISTS loomplane_manifests`,
}

// A cloud account an organization deploys to. The credential isn't here:
// it's in Vault, encrypted, under the organization's and the connection's
// IDs. details holds what's safe to show about it, such as a service
// account's email.
var cloudConnectionsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_cloud_connections (
		id         TEXT        PRIMARY KEY,
		org_id     TEXT        NOT NULL,
		cloud      TEXT        NOT NULL,
		name       TEXT        NOT NULL,
		account    TEXT        NOT NULL DEFAULT '',
		details    JSONB       NOT NULL DEFAULT '{}',
		status     TEXT        NOT NULL,
		missing    JSONB       NOT NULL DEFAULT '[]',
		message    TEXT        NOT NULL DEFAULT '',
		checked_at TIMESTAMPTZ,
		created_by TEXT        NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		CONSTRAINT loomplane_cloud_connections_org_name UNIQUE (org_id, name)
	)`,
}

var cloudConnectionsDown = []string{
	`DROP TABLE IF EXISTS loomplane_cloud_connections`,
}

// Which connection an environment deploys to each cloud with, when the
// organization has more than one. No foreign key to the connection: a
// removed one stays named here, so the next plan says so instead of
// quietly picking another account.
var environmentConnectionsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_environment_connections (
		org_id         TEXT NOT NULL,
		project_id     TEXT NOT NULL REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		environment_id TEXT NOT NULL REFERENCES loomplane_environments (id) ON DELETE CASCADE,
		cloud          TEXT NOT NULL,
		connection_id  TEXT NOT NULL,
		PRIMARY KEY (environment_id, cloud)
	)`,
}

var environmentConnectionsDown = []string{
	`DROP TABLE IF EXISTS loomplane_environment_connections`,
}

// A deployment is one run of an approved plan, at most one per plan, with
// a step per action of the plan. source is the key of the commit's
// tarball in artifact storage.
var deploymentsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_deployments (
		id             TEXT        PRIMARY KEY,
		org_id         TEXT        NOT NULL,
		project_id     TEXT        NOT NULL REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		environment_id TEXT        NOT NULL REFERENCES loomplane_environments (id) ON DELETE CASCADE,
		environment    TEXT        NOT NULL,
		plan_id        TEXT        NOT NULL REFERENCES loomplane_plans (id) ON DELETE CASCADE,
		commit_sha     TEXT        NOT NULL,
		status         TEXT        NOT NULL,
		source         TEXT        NOT NULL DEFAULT '',
		error          TEXT        NOT NULL DEFAULT '',
		steps          JSONB       NOT NULL DEFAULT '[]',
		started_by     TEXT        NOT NULL,
		started_at     TIMESTAMPTZ NOT NULL,
		finished_at    TIMESTAMPTZ,
		CONSTRAINT loomplane_deployments_plan UNIQUE (plan_id)
	)`,
	`CREATE INDEX IF NOT EXISTS loomplane_deployments_project_started
		ON loomplane_deployments (org_id, project_id, started_at DESC)`,
}

var deploymentsDown = []string{
	`DROP TABLE IF EXISTS loomplane_deployments`,
}

// Where a deployed service answers, so the services that need it can be
// given its address.
var bindingURLUp = []string{
	`ALTER TABLE loomplane_bindings ADD COLUMN IF NOT EXISTS url TEXT NOT NULL DEFAULT ''`,
}

var bindingURLDown = []string{
	`ALTER TABLE loomplane_bindings DROP COLUMN IF EXISTS url`,
}

// How often a failed deployment was retried.
var deploymentRetriesUp = []string{
	`ALTER TABLE loomplane_deployments ADD COLUMN IF NOT EXISTS retries INTEGER NOT NULL DEFAULT 0`,
}

var deploymentRetriesDown = []string{
	`ALTER TABLE loomplane_deployments DROP COLUMN IF EXISTS retries`,
}
