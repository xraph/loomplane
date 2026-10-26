package migrations

var deploymentWorkflowRunsUp = []string{`CREATE TABLE IF NOT EXISTS loomplane_deployment_workflow_runs (
 sequence BIGSERIAL UNIQUE, org_id TEXT NOT NULL, plan_id TEXT NOT NULL,
 workflow_id TEXT PRIMARY KEY, request JSONB NOT NULL, start_delivered BOOLEAN NOT NULL DEFAULT false, start_attempted_at TIMESTAMPTZ,
 CHECK(coalesce(request->>'build_id' IN ('deployment-v2','deployment-v3'),false))
)`, `CREATE INDEX IF NOT EXISTS loomplane_deployment_workflow_plan ON loomplane_deployment_workflow_runs(plan_id,sequence DESC)`, `CREATE INDEX IF NOT EXISTS loomplane_deployment_workflow_pending ON loomplane_deployment_workflow_runs(start_attempted_at,sequence) WHERE NOT start_delivered`, `CREATE TABLE IF NOT EXISTS loomplane_deployment_workflow_participants(org_id TEXT NOT NULL, plan_id TEXT NOT NULL, workflow_id TEXT NOT NULL REFERENCES loomplane_deployment_workflow_runs(workflow_id), PRIMARY KEY(org_id,plan_id,workflow_id))`}

var deploymentWorkflowRunsDown = []string{`DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_deployment_workflow_runs) THEN RAISE EXCEPTION 'workflow routing must be retained'; END IF; END $$`, `DROP TABLE loomplane_deployment_workflow_participants`, `DROP TABLE loomplane_deployment_workflow_runs`}
