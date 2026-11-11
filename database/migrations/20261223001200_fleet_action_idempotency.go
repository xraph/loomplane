package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewCreateIndex("loomplane_fleet_create_key").Unique().On("loomplane_fleet_operations").Columns("org_id", "fleet_id", "idempotency_key").Where("kind='create'").Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateIndex("loomplane_fleet_action_key").Unique().On("loomplane_fleet_operations").Columns("org_id", "fleet_id", "instance_id", "kind", "idempotency_key").Where("kind<>'create'").Exec(ctx); err != nil {
			return err
		}
		// Discover the generated constraint by columns because PostgreSQL may
		// truncate its name. The new indexes cover every row before it is removed.
		_, err := db.Exec(ctx, `DO $$ DECLARE item RECORD; found_count INTEGER := 0; BEGIN
            FOR item IN SELECT c.conname FROM pg_constraint c
            WHERE c.conrelid='loomplane_fleet_operations'::regclass AND c.contype='u'
              AND ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum, ord)
                        JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.ord)
                    = ARRAY['org_id','fleet_id','kind','idempotency_key']
            LOOP
                found_count := found_count + 1;
                EXECUTE format('ALTER TABLE loomplane_fleet_operations DROP CONSTRAINT %I', item.conname);
            END LOOP;
            IF found_count <> 1 THEN RAISE EXCEPTION 'expected one fleet operation idempotency constraint'; END IF;
        END $$`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		// Restoring the old constraint fails if distinct targets now share a key.
		// Keep those accepted operations and both indexes intact on failure.
		if err := db.NewAlterTable("loomplane_fleet_operations").AddUnique("org_id", "fleet_id", "kind", "idempotency_key").Exec(ctx); err != nil {
			return err
		}
		if err := db.NewDropIndex("loomplane_fleet_action_key").Exec(ctx); err != nil {
			return err
		}
		return db.NewDropIndex("loomplane_fleet_create_key").Exec(ctx)
	})
}
