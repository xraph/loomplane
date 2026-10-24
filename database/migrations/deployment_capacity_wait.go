package migrations

import "strings"

var deploymentCapacityWaitUp = append([]string{
	`ALTER TABLE loomplane_deployments ADD COLUMN capacity_revision BIGINT NOT NULL DEFAULT 0 CHECK(capacity_revision>=0), ADD COLUMN capacity_wait JSONB`,
	`ALTER TABLE loomplane_deployments ADD CONSTRAINT loomplane_deployment_status CHECK(status IN ('running','paused','succeeded','failed','cancelled')),
 ADD CONSTRAINT loomplane_deployment_wait CHECK(coalesce((status='paused' AND capacity_wait IS NOT NULL AND finished_at IS NULL AND capacity_revision>0 AND (capacity_wait->>'revision')::bigint=capacity_revision AND jsonb_array_length(capacity_wait->'blocks')>0) OR (status<>'paused' AND capacity_wait IS NULL),false)),
 ADD CONSTRAINT loomplane_deployment_cancelled CHECK(status<>'cancelled' OR finished_at IS NOT NULL)`,
	`CREATE TABLE loomplane_deployment_capacity_events (
 org_id TEXT NOT NULL, deployment_id TEXT NOT NULL REFERENCES loomplane_deployments(id) ON DELETE RESTRICT,
 revision BIGINT NOT NULL CHECK(revision>0), body JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL,
 workflow_delivered BOOLEAN NOT NULL DEFAULT false, notification_delivered BOOLEAN NOT NULL DEFAULT false,
 PRIMARY KEY(org_id,deployment_id,revision))`,
	`CREATE INDEX loomplane_capacity_workflow_pending ON loomplane_deployment_capacity_events(org_id,created_at) WHERE NOT workflow_delivered`,
	`CREATE INDEX loomplane_capacity_notification_pending ON loomplane_deployment_capacity_events(org_id,created_at) WHERE NOT notification_delivered`,
}, deploymentCapacityGuard(true))
var deploymentCapacityWaitDown = append([]string{
	`DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_deployment_capacity_events) THEN RAISE EXCEPTION 'capacity transition history must be retained'; END IF; END $$`,
	`DROP TABLE loomplane_deployment_capacity_events`,
	`ALTER TABLE loomplane_deployments DROP CONSTRAINT loomplane_deployment_status, DROP CONSTRAINT loomplane_deployment_wait, DROP CONSTRAINT loomplane_deployment_cancelled, DROP COLUMN capacity_revision, DROP COLUMN capacity_wait`,
}, deploymentCapacityGuard(false))

func deploymentCapacityGuard(paused bool) string {
	for _, sql := range cloudSyncUp {
		if !strings.HasPrefix(sql, "CREATE OR REPLACE FUNCTION loomplane_deployment_guard()") {
			continue
		}
		if paused {
			sql = strings.ReplaceAll(sql, "NEW.status<>'running'", "NEW.status NOT IN ('running','paused')")
			sql = strings.ReplaceAll(sql, "status='running'", "status IN ('running','paused')")
		}
		return sql
	}
	panic("deployment guard migration is missing")
}
