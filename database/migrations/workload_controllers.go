package migrations

import "github.com/xraph/grove/migrate"

// WorkloadControllersMigration retains mutation receipts separately from service
// registrations. Receipts contain no credentials and are scoped to their parents.
func WorkloadControllersMigration() *migrate.Migration {
	return statements("20261025000200", "workload_controllers", []string{
		`CREATE TABLE IF NOT EXISTS loomplane_workload_controllers (
 org_id TEXT NOT NULL,project_id TEXT NOT NULL,environment_id TEXT NOT NULL,
 connection_id TEXT NOT NULL,instance_id TEXT NOT NULL,runtime_ref TEXT NOT NULL,
 body JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(org_id,connection_id,instance_id,runtime_ref),
 FOREIGN KEY(org_id,project_id,environment_id) REFERENCES loomplane_environments(org_id,project_id,id) ON DELETE CASCADE,
 FOREIGN KEY(org_id,connection_id) REFERENCES loomplane_cloud_connections(org_id,id) ON DELETE CASCADE
 )`,
	}, []string{`DO $controllers_down$ BEGIN
 IF to_regclass('loomplane_workload_controllers') IS NOT NULL THEN
 LOCK TABLE loomplane_workload_controllers IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM loomplane_workload_controllers) THEN RAISE EXCEPTION 'controller receipts are still retained' USING ERRCODE='55000'; END IF;
 DROP TABLE loomplane_workload_controllers;
 END IF; END $controllers_down$`})
}
