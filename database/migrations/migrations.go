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
