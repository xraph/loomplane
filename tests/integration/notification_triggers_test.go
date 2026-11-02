//go:build integration

package integration

import "testing"

// A notice trigger that cannot find its recipients must not fail the write
// that fired it. A database built from the Loomplane migrations alone has no
// authsome_members table, which is the case the triggers have to survive.
func TestNotificationTriggersNeverFailTheirWrite(t *testing.T) {
	_, conn, _ := cloudSyncDatabase(t)
	ctx := t.Context()

	if _, err := conn.Exec(ctx, `INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('manifest','org','proj',1,'version: 1','digest','usr',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at) VALUES('trigger-plan','org','proj','env','production','planning','manifest',1,'usr',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE loomplane_plans SET status='ready' WHERE id='trigger-plan'`); err != nil {
		t.Fatalf("plan planning to ready: %v", err)
	}

	if _, err := conn.Exec(ctx, `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES('trigger-dep','org','proj','env','production','trigger-plan','reviewed','running','usr',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE loomplane_deployments SET status='failed', finished_at=now() WHERE id='trigger-dep'`); err != nil {
		t.Fatalf("production deployment marked failed: %v", err)
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_notification_outbox WHERE deployment_id='trigger-dep'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the starter's own notice survives the missing admin lookup: %d %v", n, err)
	}
}
