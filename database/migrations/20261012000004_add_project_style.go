package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

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
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_projects").
				AddColumn("icon", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("color", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `UPDATE loomplane_projects p
				SET icon = n.names[1 + mod(abs(hashtext(p.id)::bigint), cardinality(n.names))]
				FROM (SELECT ARRAY[
					'store', 'bag', 'card', 'receipt', 'wallet', 'truck', 'bell', 'mail',
					'chat', 'megaphone', 'users', 'calendar', 'database', 'chart', 'search', 'sparkles',
					'bot', 'radar', 'server', 'cpu', 'webhook', 'braces', 'terminal', 'workflow',
					'package', 'layers', 'smartphone', 'globe', 'pin', 'health', 'learning', 'book',
					'film', 'music', 'image', 'gamepad', 'shield', 'key', 'fingerprint', 'rocket',
					'zap', 'compass', 'anchor', 'leaf', 'flame', 'mountain', 'atom', 'hexagon'
				] AS names) n
				WHERE p.icon = ''`)
			if err != nil {
				return fmt.Errorf("backfill project icons: %w", err)
			}

			_, err = db.Exec(ctx, `UPDATE loomplane_projects p
				SET color = n.names[1 + mod(abs(hashtext('color:' || p.id)::bigint), cardinality(n.names))]
				FROM (SELECT ARRAY[
					'gold', 'orange', 'red', 'rose', 'violet', 'indigo', 'blue', 'teal', 'green', 'slate'
				] AS names) n
				WHERE p.color = ''`)
			if err != nil {
				return fmt.Errorf("backfill project colors: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_projects").
				DropColumn("color").IfExists().
				DropColumn("icon").IfExists().
				Exec(ctx)
		},
	)
}
