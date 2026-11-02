package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `
-- Older writers may omit metadata on these two v1 bodies. The trigger restores
-- it, but explicit unknown versions and updates of unknown bodies fail closed.
CREATE FUNCTION loomplane_cluster_aggregate_metadata() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF jsonb_typeof(NEW.body) IS DISTINCT FROM 'object' OR
    (NEW.body ? 'schema_version' AND NEW.body->'schema_version' IS DISTINCT FROM '1'::jsonb) THEN
  RAISE EXCEPTION 'unsupported cluster aggregate schema version';
 END IF;
 IF TG_OP = 'UPDATE' AND OLD.body->'schema_version' IS DISTINCT FROM '1'::jsonb THEN
  RAISE EXCEPTION 'unsupported stored cluster aggregate schema version';
 END IF;
 NEW.body := NEW.body || jsonb_build_object('schema_version', 1);
 -- Keep ordering across application/database clock skew and repeated mutations.
 IF TG_TABLE_NAME = 'loomplane_cluster_workload_holds' THEN
  IF TG_OP = 'INSERT' THEN
   NEW.body := NEW.body || jsonb_build_object('revision', 1, 'updated_at',
     greatest(clock_timestamp(), (NEW.body->>'created_at')::timestamptz));
  ELSE
   IF jsonb_typeof(OLD.body->'revision') IS DISTINCT FROM 'number' OR
      (OLD.body->>'revision')::bigint < 1 OR
      OLD.body->>'updated_at' IS NULL THEN
    RAISE EXCEPTION 'invalid stored workload hold metadata';
   END IF;
   IF NEW.body->'created_at' IS DISTINCT FROM OLD.body->'created_at' THEN
    RAISE EXCEPTION 'workload hold creation time is immutable';
   END IF;
   IF (NEW.body - 'schema_version' - 'revision' - 'updated_at') IS DISTINCT FROM
      (OLD.body - 'schema_version' - 'revision' - 'updated_at') OR NEW.released IS DISTINCT FROM OLD.released THEN
    NEW.body := NEW.body || jsonb_build_object('revision', (OLD.body->>'revision')::bigint + 1,
      'updated_at', greatest(clock_timestamp(), (OLD.body->>'updated_at')::timestamptz + interval '1 microsecond'));
   ELSE
    NEW.body := NEW.body || jsonb_build_object('revision', OLD.body->'revision', 'updated_at', OLD.body->'updated_at');
   END IF;
  END IF;
 END IF;
 RETURN NEW;
END $$;

-- Backfill only unversioned rows. Never relabel an explicit future format.
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM loomplane_cluster_capacity_envelopes WHERE jsonb_typeof(body) IS DISTINCT FROM 'object' OR (body ? 'schema_version' AND body->'schema_version' IS DISTINCT FROM '1'::jsonb)) OR
    EXISTS (SELECT 1 FROM loomplane_cluster_workload_holds WHERE jsonb_typeof(body) IS DISTINCT FROM 'object' OR (body ? 'schema_version' AND body->'schema_version' IS DISTINCT FROM '1'::jsonb)) THEN
  RAISE EXCEPTION 'unsupported cluster aggregate schema version';
 END IF;
END $$;
UPDATE loomplane_cluster_capacity_envelopes SET body=body || jsonb_build_object('schema_version',1);
UPDATE loomplane_cluster_workload_holds SET body=body || jsonb_build_object('schema_version',1,'revision',1,'updated_at',clock_timestamp());
CREATE TRIGGER cluster_envelope_metadata BEFORE INSERT OR UPDATE ON loomplane_cluster_capacity_envelopes FOR EACH ROW EXECUTE FUNCTION loomplane_cluster_aggregate_metadata();
CREATE TRIGGER cluster_hold_metadata BEFORE INSERT OR UPDATE ON loomplane_cluster_workload_holds FOR EACH ROW EXECUTE FUNCTION loomplane_cluster_aggregate_metadata();
`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DROP TRIGGER cluster_envelope_metadata ON loomplane_cluster_capacity_envelopes;
DROP TRIGGER cluster_hold_metadata ON loomplane_cluster_workload_holds;
DROP FUNCTION loomplane_cluster_aggregate_metadata();`)
		return err
	})
}
