package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `ALTER TABLE loomplane_managed_resources ADD COLUMN observation_sequence bigint NOT NULL DEFAULT 0 CHECK(observation_sequence>=0);
CREATE OR REPLACE FUNCTION loomplane_managed_resource_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.id<>OLD.id OR NEW.org_id<>OLD.org_id OR NEW.provider<>OLD.provider OR NEW.account_id<>OLD.account_id OR NEW.kind<>OLD.kind OR NEW.external_id<>OLD.external_id OR NEW.incarnation<>OLD.incarnation OR (OLD.cloud_id IS NOT NULL AND NEW.cloud_id IS DISTINCT FROM OLD.cloud_id) OR OLD.deleted_at IS NOT NULL THEN
 RAISE EXCEPTION 'managed resource identity is immutable' USING ERRCODE='23514'; END IF;
 IF NEW.revision=OLD.revision THEN
 IF (to_jsonb(NEW)-ARRAY['body','observation_sequence']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['body','observation_sequence'])
 OR ((NEW.body #- '{observation,observed_at}') - ARRAY['observed_at','updated_at']) IS DISTINCT FROM ((OLD.body #- '{observation,observed_at}') - ARRAY['observed_at','updated_at'])
 OR NOT coalesce((NEW.body->'observation'->>'observed_at')::timestamptz >= (OLD.body->'observation'->>'observed_at')::timestamptz,false)
 OR NOT coalesce((NEW.body->'observation'->>'observed_at')::timestamptz<=clock_timestamp(),false)
 THEN RAISE EXCEPTION 'material managed resource change requires a revision' USING ERRCODE='23514'; END IF;
 ELSIF NEW.revision<>OLD.revision+1 THEN RAISE EXCEPTION 'managed resource revision must advance' USING ERRCODE='23514';
 END IF;
 NEW.observation_sequence:=OLD.observation_sequence+CASE WHEN NEW.body->'observation' IS DISTINCT FROM OLD.body->'observation' THEN 1 ELSE 0 END;
 RETURN NEW;
END $$;`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN RAISE EXCEPTION 'observation sequencing must be retained'; END $$`)
		return err
	})
}
