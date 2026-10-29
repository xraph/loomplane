package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_cluster_enrollment_claims (
 org_id text NOT NULL, cluster_id text NOT NULL, namespace text NOT NULL, operation_id text NOT NULL, project_id text NOT NULL,
 PRIMARY KEY(org_id,cluster_id,namespace), UNIQUE(org_id,operation_id),
 FOREIGN KEY(org_id,operation_id) REFERENCES loomplane_cluster_operations(org_id,id));
 CREATE TABLE loomplane_cluster_credential_refresh (
 org_id text NOT NULL, attachment_id text NOT NULL, version bigint NOT NULL, vault_key text NOT NULL, lease_owner text NOT NULL, lease_until timestamptz NOT NULL, expires_at timestamptz, recovery_key text NOT NULL DEFAULT '', recovery_expiry timestamptz,
 PRIMARY KEY(org_id,attachment_id), FOREIGN KEY(org_id,attachment_id) REFERENCES loomplane_runtime_attachments(org_id,id));
 CREATE TABLE loomplane_cluster_credential_attempts (
 org_id text NOT NULL, attachment_id text NOT NULL, vault_key text NOT NULL, version bigint NOT NULL, lease_owner text NOT NULL, lease_until timestamptz NOT NULL,
 PRIMARY KEY(org_id,vault_key), FOREIGN KEY(org_id,attachment_id) REFERENCES loomplane_runtime_attachments(org_id,id));`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DROP TABLE loomplane_cluster_credential_attempts; DROP TABLE loomplane_cluster_credential_refresh; DROP TABLE loomplane_cluster_enrollment_claims;`)
		return err
	})
}
