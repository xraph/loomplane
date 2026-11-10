package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A platform host's client certificate can be rotated. The host row keeps
// when the current certificate expires, the next one while the host is
// being moved onto it, the previous one until the host stops trusting it,
// and how far the expiry warnings have gone. The keys stay in Vault.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `ALTER TABLE loomplane_platform_hosts
	ADD COLUMN IF NOT EXISTS client_expires_at TIMESTAMPTZ,
	ADD COLUMN IF NOT EXISTS next_fingerprint TEXT NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS next_expires_at TIMESTAMPTZ,
	ADD COLUMN IF NOT EXISTS previous_fingerprint TEXT NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS cert_notice_stage INTEGER NOT NULL DEFAULT 0 CHECK (cert_notice_stage BETWEEN 0 AND 4)`); err != nil {
				return fmt.Errorf("add host certificate rotation columns: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `ALTER TABLE loomplane_platform_hosts
	DROP COLUMN IF EXISTS cert_notice_stage,
	DROP COLUMN IF EXISTS previous_fingerprint,
	DROP COLUMN IF EXISTS next_expires_at,
	DROP COLUMN IF EXISTS next_fingerprint,
	DROP COLUMN IF EXISTS client_expires_at`); err != nil {
				return fmt.Errorf("drop host certificate rotation columns: %w", err)
			}
			return nil
		},
	)
}
