package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// modelBudgetEvents are the webhook events a GPU model's budget notices
// send.
var modelBudgetEvents = []string{"model.budget_projected", "model.budget_crossed"}

// Webhook endpoints made before the model budget events existed don't hear
// them. Loomplane makes every endpoint with both deployment types, so
// those that have both get each budget event they lack. On a new database
// Relay's tables come after this group, so a missing table is skipped, and
// running it again changes nothing.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			for _, event := range modelBudgetEvents {
				_, err := db.Exec(ctx, `DO $$
BEGIN
	IF to_regclass('relay_endpoints') IS NOT NULL THEN
		UPDATE relay_endpoints
		   SET event_types = array_append(event_types, '`+event+`'), updated_at = clock_timestamp()
		 WHERE event_types @> ARRAY['deployment.succeeded','deployment.failed']::text[]
		   AND NOT ('`+event+`' = ANY(event_types));
	END IF;
END $$`)
				if err != nil {
					return fmt.Errorf("add %s to relay_endpoints: %w", event, err)
				}
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			for _, event := range modelBudgetEvents {
				_, err := db.Exec(ctx, `DO $$
BEGIN
	IF to_regclass('relay_endpoints') IS NOT NULL THEN
		UPDATE relay_endpoints
		   SET event_types = array_remove(event_types, '`+event+`'), updated_at = clock_timestamp()
		 WHERE '`+event+`' = ANY(event_types);
	END IF;
END $$`)
				if err != nil {
					return fmt.Errorf("remove %s from relay_endpoints: %w", event, err)
				}
			}
			return nil
		},
	)
}
