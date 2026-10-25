package migrations

func init() {
	App.MustRegister(statements("20261102000400", "binding_owned_resources", []string{
		`ALTER TABLE loomplane_bindings ADD COLUMN IF NOT EXISTS owned_resources JSONB NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(owned_resources)='object' AND (owned_resources='{}'::jsonb OR physical_id<>''))`,
		`CREATE OR REPLACE FUNCTION loomplane_binding_ownership_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.owned_resources<>'{}'::jsonb AND NOT NEW.owned_resources @> OLD.owned_resources
 AND (NEW.provider,NEW.region,NEW.external_id,NEW.physical_id)
 IS NOT DISTINCT FROM (OLD.provider,OLD.region,OLD.external_id,OLD.physical_id) THEN
  RAISE EXCEPTION 'binding update discarded existing resource ownership' USING ERRCODE='23514';
 END IF;
 IF OLD.owned_resources<>'{}'::jsonb
 AND (NEW.provider,NEW.region,NEW.external_id,NEW.physical_id)
 IS DISTINCT FROM (OLD.provider,OLD.region,OLD.external_id,OLD.physical_id)
 AND NOT EXISTS (
  SELECT 1 FROM jsonb_array_elements(NEW.replaced) AS prior
  WHERE COALESCE(prior->>'provider','')=OLD.provider
    AND COALESCE(prior->>'connection_id','')=OLD.connection_id
    AND COALESCE(prior->>'region','')=OLD.region
    AND COALESCE(prior->>'external_id','')=OLD.external_id
    AND COALESCE(prior->>'physical_id','')=OLD.physical_id
    AND prior->'owned_resources'=OLD.owned_resources
 ) THEN
  RAISE EXCEPTION 'binding replacement omitted original resource ownership' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$`,
		`CREATE OR REPLACE TRIGGER loomplane_binding_ownership_guard BEFORE UPDATE ON loomplane_bindings FOR EACH ROW EXECUTE FUNCTION loomplane_binding_ownership_guard()`,
	}, []string{
		`DROP TRIGGER IF EXISTS loomplane_binding_ownership_guard ON loomplane_bindings`,
		`DROP FUNCTION IF EXISTS loomplane_binding_ownership_guard()`,
		`ALTER TABLE loomplane_bindings DROP COLUMN IF EXISTS owned_resources`,
	}))
}
