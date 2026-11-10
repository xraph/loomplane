package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
	"gopkg.in/yaml.v3"
)

// A project's kind says what it is: an app project builds from its
// repositories, and a service project is the one a catalog service, such as
// a database, gets of its own. Every project so far starts as an app.
//
// The backfill then finds the service projects already made, by their
// shape rather than their icon, which anyone can pick. Both the dashboard
// and loomctl services create make one the same way: a project with no
// repository whose manifest holds a single resource named "database" and
// no services. A project with that shape is a service project whoever made
// it. One the catalog created but never got as far as saving a manifest
// for has no shape to go by, so it stays an app.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_projects").
				AddColumn("kind", migrate.Text, migrate.NotNull(), migrate.Default("app"),
					migrate.Check("kind IN ('app','service')")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}
			return backfillServiceProjects(ctx, db)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_projects").
				DropColumn("kind").IfExists().
				Exec(ctx)
		},
	)
}

// backfillServiceProjects marks each app project shaped like a service
// project as one. It reads the latest manifest of every project without a
// live source, and parses it here rather than with pkg/manifest, so the
// migration keeps doing what it did when it shipped.
func backfillServiceProjects(ctx context.Context, db *migrate.DB) error {
	rows, err := db.Query(ctx, `SELECT p.id, m.body FROM loomplane_projects p
	JOIN LATERAL (SELECT body FROM loomplane_manifests WHERE project_id = p.id ORDER BY revision DESC LIMIT 1) m ON true
	WHERE p.kind = 'app' AND p.deployment_mode = 'application'
		AND NOT EXISTS (SELECT 1 FROM loomplane_project_sources s WHERE s.project_id = p.id AND s.removed_at IS NULL)`)
	if err != nil {
		return fmt.Errorf("read projects without sources: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			rows.Close()
			return fmt.Errorf("scan project manifest: %w", err)
		}
		if serviceShaped(body) {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read projects without sources: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := db.Exec(ctx, `UPDATE loomplane_projects SET kind = 'service' WHERE id = ANY($1)`, ids); err != nil {
		return fmt.Errorf("mark service projects: %w", err)
	}
	return nil
}

// serviceShaped reports whether a manifest is a service project's: one
// resource, named "database", with a kind, and no services. A manifest that
// doesn't parse isn't.
func serviceShaped(body string) bool {
	var m struct {
		Services  map[string]yaml.Node `yaml:"services"`
		Resources map[string]struct {
			Kind string `yaml:"kind"`
		} `yaml:"resources"`
	}
	if err := yaml.Unmarshal([]byte(body), &m); err != nil {
		return false
	}
	db, ok := m.Resources["database"]
	return ok && len(m.Resources) == 1 && len(m.Services) == 0 && db.Kind != ""
}
