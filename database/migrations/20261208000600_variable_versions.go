package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `ALTER TABLE loomplane_variables
 ADD COLUMN source_revision bigint NOT NULL DEFAULT 1 CHECK(source_revision>0),
 ADD COLUMN secret_identity text NOT NULL DEFAULT '',
 ADD COLUMN secret_version bigint NOT NULL DEFAULT 0 CHECK(secret_version>=0),
 ADD CONSTRAINT variable_version_pair CHECK((secret_identity='' AND secret_version=0) OR (secret AND secret_identity<>'' AND secret_version>0));
CREATE FUNCTION loomplane_variable_source_gate() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('variable-sources/v1',0));
 RETURN NULL;
END $$;
CREATE TRIGGER variable_source_gate BEFORE INSERT OR UPDATE OR DELETE ON loomplane_variables FOR EACH STATEMENT EXECUTE FUNCTION loomplane_variable_source_gate();
CREATE TRIGGER variable_source_gate BEFORE INSERT OR UPDATE OR DELETE ON loomplane_environments FOR EACH STATEMENT EXECUTE FUNCTION loomplane_variable_source_gate();
CREATE TRIGGER variable_source_gate BEFORE INSERT OR UPDATE OR DELETE ON loomplane_projects FOR EACH STATEMENT EXECUTE FUNCTION loomplane_variable_source_gate();
CREATE FUNCTION loomplane_variable_source_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  RETURN OLD;
 END IF;
 IF TG_OP='UPDATE' THEN
  IF NEW.org_id<>OLD.org_id OR NEW.id<>OLD.id THEN RAISE EXCEPTION 'variable source identity is immutable' USING ERRCODE='23514'; END IF;
  IF ROW(NEW.value,NEW.secret,NEW.project_id,NEW.environment_id,NEW.environment,NEW.service,NEW.key) IS DISTINCT FROM ROW(OLD.value,OLD.secret,OLD.project_id,OLD.environment_id,OLD.environment,OLD.service,OLD.key)
   OR (OLD.secret_identity<>'' AND ROW(NEW.secret_identity,NEW.secret_version) IS DISTINCT FROM ROW(OLD.secret_identity,OLD.secret_version)) THEN
   NEW.source_revision:=OLD.source_revision+1;
  END IF;
  IF NEW.source_revision NOT IN (OLD.source_revision,OLD.source_revision+1) THEN RAISE EXCEPTION 'variable source revision changed' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER variable_source_guard BEFORE INSERT OR UPDATE OR DELETE ON loomplane_variables FOR EACH ROW EXECUTE FUNCTION loomplane_variable_source_guard();`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DROP TRIGGER variable_source_gate ON loomplane_variables; DROP TRIGGER variable_source_gate ON loomplane_environments; DROP TRIGGER variable_source_gate ON loomplane_projects; DROP FUNCTION loomplane_variable_source_gate(); DROP TRIGGER variable_source_guard ON loomplane_variables; DROP FUNCTION loomplane_variable_source_guard(); ALTER TABLE loomplane_variables DROP CONSTRAINT variable_version_pair, DROP COLUMN source_revision, DROP COLUMN secret_identity, DROP COLUMN secret_version;`)
		return err
	})
}
