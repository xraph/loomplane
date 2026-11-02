//go:build integration

package migrations

import "testing"

func TestClusterAggregateMetadataMigration(t *testing.T) {
	conn, executor := scratch(t)
	exec(t, conn,
		`CREATE TABLE loomplane_cloud_profiles(org_id text,id text,PRIMARY KEY(org_id,id)); INSERT INTO loomplane_cloud_profiles VALUES('org','cloud')`,
		`CREATE TABLE loomplane_cloud_capacity_accounts(org_id text)`,
		`CREATE TABLE loomplane_managed_resources(org_id text,id text,PRIMARY KEY(org_id,id)); INSERT INTO loomplane_managed_resources VALUES('org','cluster')`)
	up(t, executor, "cluster_capacity_envelopes", "cluster_workload_holds")
	exec(t, conn,
		`INSERT INTO loomplane_cluster_capacity_envelopes(org_id,cloud_id,id,provider,account_id,revision,body) SELECT 'org','cloud',id,'provider','account',7,jsonb_build_object('id',id,'state',state,'revision',7,'requested_at','2026-01-01T00:00:00Z','maximum',jsonb_build_object('servers',3),'money',jsonb_build_object('micro_usd',125),'review',jsonb_build_object('request_digest','signed')) FROM (VALUES('active','active'),('retained','released')) v(id,state)`,
		`INSERT INTO loomplane_cluster_workload_holds(org_id,cloud_id,resource_id,id,plan_id,service,release_id,body,released) SELECT 'org','cloud','cluster',id,id,'api','release',jsonb_build_object('input',jsonb_build_object('id',id,'review_digest','signed','maximum',jsonb_build_object('pods',2)),'created_at','2026-01-01T00:00:00Z'),released FROM (VALUES('active',false),('retained',true)) v(id,released)`,
		`CREATE TABLE before_envelopes AS SELECT * FROM loomplane_cluster_capacity_envelopes`,
		`CREATE TABLE before_holds AS SELECT * FROM loomplane_cluster_workload_holds`,
		`CREATE TABLE signed_payloads(body jsonb); INSERT INTO signed_payloads VALUES('{"input":{"review_digest":"signed"},"digest":"approved"}'); CREATE TABLE before_signed AS SELECT * FROM signed_payloads`)
	up(t, executor, "cluster_aggregate_metadata")
	assert := func(query string) {
		t.Helper()
		var ok bool
		if err := conn.QueryRow(t.Context(), query).Scan(&ok); err != nil || !ok {
			t.Fatalf("%s: %v (%v)", query, ok, err)
		}
	}
	assert(`SELECT bool_and(e.body-'schema_version'=b.body AND e.revision=b.revision AND e.body->'schema_version'='1') FROM loomplane_cluster_capacity_envelopes e JOIN before_envelopes b USING(id)`)
	assert(`SELECT bool_and(h.body-'schema_version'-'revision'-'updated_at'=b.body AND h.released=b.released AND h.body->'schema_version'='1' AND h.body->'revision'='1' AND (h.body->>'updated_at')::timestamptz > (h.body->>'created_at')::timestamptz) FROM loomplane_cluster_workload_holds h JOIN before_holds b USING(id)`)
	assert(`SELECT (SELECT body FROM signed_payloads)=(SELECT body FROM before_signed)`)
	exec(t, conn, `CREATE TABLE migrated_holds AS SELECT * FROM loomplane_cluster_workload_holds`,
		`UPDATE loomplane_cluster_workload_holds SET body=body-'schema_version'-'revision'-'updated_at'`,
		`UPDATE loomplane_cluster_capacity_envelopes SET body=body-'schema_version'`)
	assert(`SELECT bool_and(h.body=m.body) FROM loomplane_cluster_workload_holds h JOIN migrated_holds m USING(id)`)
	assert(`SELECT bool_and(body->'schema_version'='1') FROM loomplane_cluster_capacity_envelopes`)
	exec(t, conn, `UPDATE loomplane_cluster_workload_holds SET body=(body-'schema_version'-'revision'-'updated_at')||'{"binding_id":"binding"}' WHERE id='active'`)
	assert(`SELECT body->'revision'='2' FROM loomplane_cluster_workload_holds WHERE id='active'`)
	exec(t, conn, `UPDATE loomplane_cluster_workload_holds SET released=true WHERE id='active'`)
	assert(`SELECT h.body->'revision'='3' AND h.body->'created_at'=m.body->'created_at' AND (h.body->>'updated_at')::timestamptz > (m.body->>'updated_at')::timestamptz FROM loomplane_cluster_workload_holds h JOIN migrated_holds m USING(id) WHERE h.id='active'`)
	exec(t, conn, `UPDATE loomplane_cluster_workload_holds SET released=true WHERE id='active'`)
	assert(`SELECT body->'revision'='3' FROM loomplane_cluster_workload_holds WHERE id='active'`)
	for _, table := range []string{"loomplane_cluster_capacity_envelopes", "loomplane_cluster_workload_holds"} {
		for _, version := range []string{`2`, `null`, `"1"`, `0`, `{}`} {
			if _, err := conn.Exec(t.Context(), `UPDATE `+table+` SET body=jsonb_set(body,'{schema_version}',$1::jsonb)`, version); err == nil {
				t.Fatalf("%s accepted version %s", table, version)
			}
		}
	}

	// A writer may observe a slightly later clock than PostgreSQL. Preserve its
	// creation time and keep subsequent mutation timestamps strictly increasing.
	exec(t, conn, `INSERT INTO loomplane_cluster_workload_holds(org_id,cloud_id,resource_id,id,plan_id,service,release_id,body) VALUES('org','cloud','cluster','skew','skew','api','release',jsonb_build_object('input',jsonb_build_object('id','skew'),'created_at',clock_timestamp()+interval '1 second'))`)
	assert(`SELECT (body->>'updated_at')::timestamptz=(body->>'created_at')::timestamptz AND body->'revision'='1' FROM loomplane_cluster_workload_holds WHERE id='skew'`)
	exec(t, conn, `UPDATE loomplane_cluster_workload_holds SET released=true WHERE id='skew'`)
	assert(`SELECT (body->>'updated_at')::timestamptz>(body->>'created_at')::timestamptz AND body->'revision'='2' FROM loomplane_cluster_workload_holds WHERE id='skew'`)
	exec(t, conn, `BEGIN`, `SELECT pg_sleep(0.02)`, `INSERT INTO loomplane_cluster_workload_holds(org_id,cloud_id,resource_id,id,plan_id,service,release_id,body) VALUES('org','cloud','cluster','delayed','delayed','api','release',jsonb_build_object('input',jsonb_build_object('id','delayed'),'created_at',clock_timestamp()))`)
	assert(`SELECT (body->>'updated_at')::timestamptz>= (body->>'created_at')::timestamptz AND (body->>'created_at')::timestamptz > transaction_timestamp() FROM loomplane_cluster_workload_holds WHERE id='delayed'`)
	exec(t, conn, `COMMIT`)
	down(t, executor, "cluster_aggregate_metadata")
	exec(t, conn, `UPDATE loomplane_cluster_workload_holds SET body=jsonb_set(body,'{schema_version}','2')`)
	if err := migration(t, "cluster_aggregate_metadata").Up(t.Context(), executor); err == nil {
		t.Fatal("migration relabeled a future aggregate")
	}
}
