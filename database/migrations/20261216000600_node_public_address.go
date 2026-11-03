package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A node's public address is the one Loomplane fetched a nonce from, through
// the node's own proxy, and when. Routes reach a node there only once it is
// confirmed. ingress_applied is the mode the node last said it runs,
// ingress_sent_at when the stored mode was last sent to it, and
// ingress_detail why ingress isn't what was asked. The node.updated payload
// gains public_address and is otherwise unchanged.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `ALTER TABLE loomplane_nodes
	ADD COLUMN IF NOT EXISTS public_address TEXT NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS public_address_confirmed_at TIMESTAMPTZ,
	ADD COLUMN IF NOT EXISTS ingress_applied TEXT NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS ingress_sent_at TIMESTAMPTZ,
	ADD COLUMN IF NOT EXISTS ingress_detail TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("add node public address columns: %w", err)
			}
			if _, err := db.Exec(ctx, nodeEventTrigger(`'public_address',NEW.public_address`, `'public_address',OLD.public_address`)); err != nil {
				return fmt.Errorf("create function loomplane_node_event_trigger: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, nodeEventTrigger("", "")); err != nil {
				return fmt.Errorf("restore function loomplane_node_event_trigger: %w", err)
			}
			_, err := db.Exec(ctx, `ALTER TABLE loomplane_nodes
	DROP COLUMN IF EXISTS ingress_detail,
	DROP COLUMN IF EXISTS ingress_sent_at,
	DROP COLUMN IF EXISTS ingress_applied,
	DROP COLUMN IF EXISTS public_address_confirmed_at,
	DROP COLUMN IF EXISTS public_address`)
			if err != nil {
				return fmt.Errorf("drop node public address columns: %w", err)
			}
			return nil
		},
	)
}

// nodeEventTrigger is 20261216000200's node.updated trigger function, with
// extraNew and extraOld appended to its payloads when they aren't empty.
func nodeEventTrigger(extraNew, extraOld string) string {
	if extraNew != "" {
		extraNew, extraOld = ","+extraNew, ","+extraOld
	}
	return `CREATE OR REPLACE FUNCTION loomplane_node_event_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
	payload JSONB;
	previous JSONB;
BEGIN
	payload:=jsonb_build_object('id',NEW.id,'name',NEW.name,'status',NEW.status,'version',NEW.version,'endpoints',NEW.endpoints,'ingress_mode',NEW.ingress_mode,'labels',NEW.labels,'role',NEW.role,'identity_generation',NEW.identity_generation` + extraNew + `);
	IF TG_OP='UPDATE' THEN
		previous:=jsonb_build_object('id',OLD.id,'name',OLD.name,'status',OLD.status,'version',OLD.version,'endpoints',OLD.endpoints,'ingress_mode',OLD.ingress_mode,'labels',OLD.labels,'role',OLD.role,'identity_generation',OLD.identity_generation` + extraOld + `);
	END IF;
	IF previous IS NOT DISTINCT FROM payload THEN RETURN NULL; END IF;

	PERFORM loomplane_append_event(NEW.org_id,'','node.updated',NEW.id,payload);
	RETURN NULL;
END $$`
}
