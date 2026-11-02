package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_cloud_location_policies (
   org_id text NOT NULL,cloud_id text NOT NULL,revision bigint NOT NULL CHECK(revision>0),body jsonb NOT NULL,
   schema_version integer NOT NULL DEFAULT 1 CHECK(schema_version>0),
   created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
   CHECK(jsonb_typeof(body)='object' AND coalesce((body->>'revision')::bigint=revision,false)),
   PRIMARY KEY(org_id,cloud_id),FOREIGN KEY(org_id,cloud_id) REFERENCES loomplane_cloud_profiles(org_id,id) ON DELETE CASCADE
  );
CREATE FUNCTION loomplane_location_policy_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||OLD.org_id,0));
  RETURN OLD;
 END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
 IF TG_OP='UPDATE' AND (NEW.org_id<>OLD.org_id OR NEW.cloud_id<>OLD.cloud_id OR NEW.created_at<>OLD.created_at OR NEW.revision<>OLD.revision+1) THEN
  RAISE EXCEPTION 'location policy identity or revision changed' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_location_policy_guard BEFORE INSERT OR UPDATE OR DELETE ON loomplane_cloud_location_policies FOR EACH ROW EXECUTE FUNCTION loomplane_location_policy_guard();`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DROP TABLE loomplane_cloud_location_policies; DROP FUNCTION loomplane_location_policy_guard()`)
		return err
	})
}
