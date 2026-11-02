package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_managed_workload_authority (
 org_id text NOT NULL, release_id text NOT NULL, epoch bigint NOT NULL CHECK(epoch>0),
 schema_version integer NOT NULL DEFAULT 1, revision bigint NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 body jsonb NOT NULL, revoked_at timestamptz, created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(org_id,release_id));
CREATE TABLE loomplane_managed_workload_issuances (
 id text PRIMARY KEY, org_id text NOT NULL, release_id text NOT NULL, epoch bigint NOT NULL,
 schema_version integer NOT NULL DEFAULT 1, revision bigint NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 scope jsonb NOT NULL, request jsonb NOT NULL, outcome jsonb,
 issued_at timestamptz NOT NULL DEFAULT clock_timestamp(), resolved_at timestamptz,
 FOREIGN KEY(org_id,release_id) REFERENCES loomplane_managed_workload_authority(org_id,release_id),
 CHECK ((outcome IS NULL AND resolved_at IS NULL) OR (coalesce(outcome->>'State' IN ('accepted','rejected'),false) AND resolved_at IS NOT NULL)));
CREATE INDEX loomplane_managed_workload_unresolved ON loomplane_managed_workload_issuances(org_id,release_id) WHERE outcome IS NULL;`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_managed_workload_authority) THEN RAISE EXCEPTION 'managed workload authority and issuance history must be retained'; END IF; END $$;
DROP TABLE loomplane_managed_workload_issuances;
DROP TABLE loomplane_managed_workload_authority;`)
		return err
	})
}
