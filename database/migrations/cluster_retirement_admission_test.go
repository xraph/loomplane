//go:build integration

package migrations

import "testing"

func TestClusterRetirementAdmissionAlreadyAppliedRepair(t *testing.T) {
	conn, ex := scratch(t)
	exec(t, conn, `CREATE TABLE loomplane_plans(org_id text,id text,project_id text,environment_id text,result jsonb);
 CREATE TABLE loomplane_runtime_attachments(org_id text,project_id text,cluster_id text,deleted_at timestamptz,runtime_connection_id text);
 CREATE TABLE loomplane_bindings(org_id text,project_id text,environment_id text,subject text,name text,connection_id text);
 CREATE TABLE loomplane_deployments(org_id text,project_id text,plan_id text,status text);
 CREATE TABLE loomplane_cluster_operations(org_id text,status text,body jsonb);
 INSERT INTO loomplane_runtime_attachments VALUES('org','project','cluster',NULL,'managed');
 INSERT INTO loomplane_plans VALUES('org','plan','project','env','{"actions":[{"op":"create","connection_id":"managed"}]}');
 INSERT INTO loomplane_cluster_operations VALUES('org','accepted','{"kind":"detach-runtime","input":{"resource_id":"cluster"}}');`)
	up(t, ex, "cluster_maintenance_admission", "cluster_maintenance_actions", "cluster_retirement_admission", "cluster_retirement_admission")
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_deployments VALUES('org','project','plan','running')`); err == nil {
		t.Fatal("deployment admitted while retirement is accepted")
	}
	exec(t, conn, `UPDATE loomplane_runtime_attachments SET deleted_at=now(); UPDATE loomplane_cluster_operations SET status='succeeded'`)
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_deployments VALUES('org','project','plan','running')`); err == nil {
		t.Fatal("saved plan admitted after attachment tombstone")
	}
	for _, tc := range []struct {
		name, result string
		blocked      bool
	}{
		{"plain_keep", `{"actions":[{"op":"keep","connection_id":"managed"}]}`, false},
		{"adopt_keep", `{"actions":[{"op":"keep","connection_id":"managed","adopt_cloud_placement":true}]}`, true},
		{"static", `{"actions":[{"op":"update","connection_id":"static"}]}`, false},
		{"default", `{"connections":{"server":"managed"},"actions":[{"op":"update","provider":"server"}]}`, true},
		{"legacy", `{"actions":null}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := conn.Exec(t.Context(), `UPDATE loomplane_plans SET result=$1::jsonb`, tc.result); err != nil {
				t.Fatal(err)
			}
			_, err := conn.Exec(t.Context(), `INSERT INTO loomplane_deployments VALUES('org','project','plan','running')`)
			if (err != nil) != tc.blocked {
				t.Fatalf("blocked=%v err=%v", tc.blocked, err)
			}
		})
	}
}
