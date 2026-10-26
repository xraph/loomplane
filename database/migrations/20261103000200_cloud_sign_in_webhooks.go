package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Webhook endpoints made before connection.sign_in_needed existed only hear
// deployments. Loomplane makes every endpoint with both deployment types, so
// those that have both and lack the lapse get it added. On a new database
// Relay's tables come after this group, so a missing table is skipped, and
// running it again changes nothing.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $$
BEGIN
	IF to_regclass('relay_endpoints') IS NOT NULL THEN
		UPDATE relay_endpoints
		   SET event_types = array_append(event_types, 'connection.sign_in_needed'), updated_at = clock_timestamp()
		 WHERE event_types @> ARRAY['deployment.succeeded','deployment.failed']::text[]
		   AND NOT ('connection.sign_in_needed' = ANY(event_types));
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("add connection.sign_in_needed to relay_endpoints: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $$
BEGIN
	IF to_regclass('relay_endpoints') IS NOT NULL THEN
		UPDATE relay_endpoints
		   SET event_types = array_remove(event_types, 'connection.sign_in_needed'), updated_at = clock_timestamp()
		 WHERE event_types @> ARRAY['deployment.succeeded','deployment.failed','connection.sign_in_needed']::text[];
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("remove connection.sign_in_needed from relay_endpoints: %w", err)
			}
			return nil
		},
	)
}
