package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_managed_cleanup_grants (
 id text NOT NULL, org_id text NOT NULL, plan_id text NOT NULL, action_id text NOT NULL,
 deployment_id text NOT NULL, target_index integer NOT NULL CHECK(target_index>=0),
 release_id text NOT NULL, authority_revision bigint NOT NULL CHECK(authority_revision>0),
 schema_version integer NOT NULL DEFAULT 1 CHECK(schema_version=1), revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), updated_at timestamptz NOT NULL DEFAULT clock_timestamp(), revoked_at timestamptz,
 PRIMARY KEY(org_id,id), UNIQUE(org_id,plan_id,action_id,deployment_id,target_index),
 FOREIGN KEY(org_id,plan_id,action_id) REFERENCES loomplane_managed_cleanup_intents(org_id,plan_id,action_id),
 FOREIGN KEY(org_id,release_id) REFERENCES loomplane_managed_workload_authority(org_id,release_id));
ALTER TABLE loomplane_managed_workload_issuances ADD COLUMN cleanup_grant_id text, ADD COLUMN cleanup_grant_revision bigint,
 ADD CONSTRAINT loomplane_cleanup_issuance_grant FOREIGN KEY(org_id,cleanup_grant_id) REFERENCES loomplane_managed_cleanup_grants(org_id,id),
 ADD CONSTRAINT loomplane_cleanup_issuance_revision CHECK((cleanup_grant_id IS NULL AND cleanup_grant_revision IS NULL) OR (cleanup_grant_id IS NOT NULL AND cleanup_grant_revision IS NOT NULL AND cleanup_grant_revision>0));
CREATE FUNCTION loomplane_managed_cleanup_grant_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'cleanup grant history must be retained' USING ERRCODE='23514'; END IF;
 IF (NEW.id,NEW.org_id,NEW.plan_id,NEW.action_id,NEW.deployment_id,NEW.target_index,NEW.release_id,NEW.authority_revision,NEW.schema_version,NEW.created_at) IS DISTINCT FROM
 (OLD.id,OLD.org_id,OLD.plan_id,OLD.action_id,OLD.deployment_id,OLD.target_index,OLD.release_id,OLD.authority_revision,OLD.schema_version,OLD.created_at)
 OR (OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at) OR NEW.revision<>OLD.revision+1
 THEN RAISE EXCEPTION 'cleanup grant identity is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW; END $$;
CREATE TRIGGER loomplane_managed_cleanup_grant_guard BEFORE UPDATE OR DELETE ON loomplane_managed_cleanup_grants FOR EACH ROW EXECUTE FUNCTION loomplane_managed_cleanup_grant_guard();`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_managed_cleanup_grants) THEN RAISE EXCEPTION 'cleanup grant history must be retained'; END IF; END $$;
ALTER TABLE loomplane_managed_workload_issuances DROP COLUMN cleanup_grant_id, DROP COLUMN cleanup_grant_revision;
DROP TABLE loomplane_managed_cleanup_grants; DROP FUNCTION loomplane_managed_cleanup_grant_guard();`)
		return err
	})
}
