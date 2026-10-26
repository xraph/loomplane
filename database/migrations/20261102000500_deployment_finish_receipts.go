package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.MustRegister(&migrate.Migration{
		Name: "deployment_finish_receipts", Version: "20261102000500",
		Up: func(ctx context.Context, exec migrate.Executor) error {
			_, err := exec.Exec(ctx, `CREATE TABLE IF NOT EXISTS loomplane_deployment_finishes (
 org_id TEXT NOT NULL,
 deployment_id TEXT NOT NULL REFERENCES loomplane_deployments(id) ON DELETE CASCADE,
 activity_key TEXT NOT NULL,
 retries INTEGER NOT NULL CHECK(retries >= 0),
 created_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(org_id,deployment_id,activity_key)
)`)
			return err
		},
		Down: func(ctx context.Context, exec migrate.Executor) error {
			_, err := exec.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_deployment_finishes) THEN RAISE EXCEPTION 'deployment finish receipts must be retained'; END IF; END $$`)
			if err != nil {
				return err
			}
			_, err = exec.Exec(ctx, `DROP TABLE IF EXISTS loomplane_deployment_finishes`)
			return err
		},
	})
}
