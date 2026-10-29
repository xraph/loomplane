//go:build integration

package migrations

import "testing"

func TestClusterMaintenanceAlreadyAppliedAdmissionRepair(t *testing.T) {
	conn, ex := scratch(t)
	exec(t, conn, `CREATE TABLE loomplane_plans(org_id text,id text,project_id text,environment_id text,result jsonb);
 CREATE TABLE loomplane_runtime_attachments(org_id text,project_id text,cluster_id text,deleted_at timestamptz,runtime_connection_id text);
 CREATE TABLE loomplane_bindings(org_id text,project_id text,environment_id text,subject text,name text,connection_id text);
 CREATE TABLE loomplane_deployments(org_id text,plan_id text,status text);
 INSERT INTO loomplane_runtime_attachments VALUES('org','project','cluster',NULL,'managed');
 INSERT INTO loomplane_plans VALUES('org','plan','project','env','{"actions":[{"op":"keep","connection_id":"managed"}]}');`)
	up(t, ex, "cluster_maintenance_admission")
	var matched bool
	if err := conn.QueryRow(t.Context(), `SELECT loomplane_plan_uses_cluster('org','plan','cluster')`).Scan(&matched); err != nil || !matched {
		t.Fatal("prior admission behavior missing", err)
	}
	up(t, ex, "cluster_maintenance_actions", "cluster_maintenance_actions")
	if err := conn.QueryRow(t.Context(), `SELECT loomplane_plan_uses_cluster('org','plan','cluster')`).Scan(&matched); err != nil || matched {
		t.Fatal("repair did not exclude plain keep", err)
	}
	exec(t, conn, `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions,0,adopt_cloud_placement}','true')`)
	if err := conn.QueryRow(t.Context(), `SELECT loomplane_plan_uses_cluster('org','plan','cluster')`).Scan(&matched); err != nil || !matched {
		t.Fatal("repair lost keep adoption fence", err)
	}
}
