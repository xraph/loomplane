package migrations

var nativeInventoryUp = []string{
	`ALTER TABLE loomplane_native_sources ADD COLUMN IF NOT EXISTS state TEXT NOT NULL DEFAULT 'never',
 ADD COLUMN IF NOT EXISTS complete BOOLEAN NOT NULL DEFAULT false,
 ADD COLUMN IF NOT EXISTS checked_at TIMESTAMPTZ, ADD COLUMN IF NOT EXISTS observed_at TIMESTAMPTZ,
 ADD COLUMN IF NOT EXISTS error TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE IF NOT EXISTS loomplane_native_points (
 id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
 org_id TEXT NOT NULL, source_id TEXT NOT NULL, provider_id TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL, body JSONB NOT NULL CHECK(jsonb_typeof(body)='object'),
 present BOOLEAN NOT NULL DEFAULT true, last_seen_at TIMESTAMPTZ NOT NULL,
 UNIQUE(source_id,provider_id,created_at),
 FOREIGN KEY(org_id,source_id) REFERENCES loomplane_native_sources(org_id,id) ON DELETE CASCADE
 )`,
	`CREATE INDEX IF NOT EXISTS loomplane_native_points_source ON loomplane_native_points(org_id,source_id,created_at DESC,id DESC)`,
}
var nativeInventoryDown = []string{`DO $native_inventory_down$ BEGIN
 IF to_regclass('loomplane_native_points') IS NOT NULL THEN
 LOCK TABLE loomplane_native_points IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM loomplane_native_points) THEN RAISE EXCEPTION 'native recovery points are still retained' USING ERRCODE='55000'; END IF;
 DROP TABLE loomplane_native_points;
 END IF; END $native_inventory_down$`}
