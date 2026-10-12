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
