//go:build integration

package migrations

import "testing"

func TestObservedAccountingMetadataMigration(t *testing.T) {
	conn, executor := scratch(t)
	exec(t, conn,
		`CREATE TABLE loomplane_cluster_workload_observed_accounting(org_id text NOT NULL,cloud_id text NOT NULL,resource_id text NOT NULL,revision bigint NOT NULL,checked_at timestamptz NOT NULL,totals jsonb NOT NULL,PRIMARY KEY(org_id,resource_id))`,
		`CREATE FUNCTION loomplane_cluster_control_supported(version integer) RETURNS boolean LANGUAGE plpgsql AS $$ BEGIN IF version IS DISTINCT FROM 1 THEN RAISE EXCEPTION 'unsupported'; END IF;RETURN true;END $$`,
		`CREATE TABLE loomplane_cluster_demand_cohorts(org_id text,resource_id text,schema_version int); CREATE TABLE loomplane_cluster_pool_growth(LIKE loomplane_cluster_demand_cohorts); CREATE TABLE loomplane_cluster_pool_advice(LIKE loomplane_cluster_demand_cohorts); CREATE TABLE loomplane_cluster_capacity_inventory(LIKE loomplane_cluster_demand_cohorts)`,
		`INSERT INTO loomplane_cluster_workload_observed_accounting VALUES('org','cloud','cluster',7,now()-interval '1 day','{"charged":{"cpu_millis":300},"actual":{"cpu_millis":200},"running":{"cpu_millis":100},"maximum":{"cpu_millis":300}}');CREATE TABLE original AS SELECT * FROM loomplane_cluster_workload_observed_accounting`,
		`CREATE TABLE signed(body jsonb);INSERT INTO signed VALUES('{"digest":"original","demand":{"maximum":300}}');CREATE TABLE original_signed AS SELECT * FROM signed`)
	up(t, executor, "observed_accounting_metadata")
	assert := func(query string) {
		t.Helper()
		var ok bool
		if err := conn.QueryRow(t.Context(), query).Scan(&ok); err != nil || !ok {
			t.Fatal(query, ok, err)
		}
	}
	assert(`SELECT to_jsonb(a)-ARRAY['schema_version','created_at','updated_at']=to_jsonb(o) AND a.schema_version=1 AND a.created_at=a.checked_at AND a.updated_at=a.checked_at FROM loomplane_cluster_workload_observed_accounting a JOIN original o USING(org_id,resource_id)`)
	exec(t, conn, `UPDATE loomplane_cluster_workload_observed_accounting SET revision=8,checked_at=clock_timestamp(),totals=jsonb_set(totals,'{running,cpu_millis}','150')`)
	assert(`SELECT a.revision=8 AND a.totals->'running'->>'cpu_millis'='150' AND a.created_at=o.checked_at AND a.updated_at>o.checked_at FROM loomplane_cluster_workload_observed_accounting a JOIN original o USING(org_id,resource_id)`)
	exec(t, conn, `CREATE TABLE advanced AS SELECT * FROM loomplane_cluster_workload_observed_accounting`,
		`UPDATE loomplane_cluster_workload_observed_accounting SET totals=totals,updated_at=clock_timestamp()`,
		`INSERT INTO loomplane_cluster_workload_observed_accounting(org_id,cloud_id,resource_id,revision,checked_at,totals) SELECT org_id,cloud_id,resource_id,revision,checked_at,totals FROM original ON CONFLICT(org_id,resource_id) DO UPDATE SET revision=excluded.revision,checked_at=excluded.checked_at,totals=excluded.totals WHERE loomplane_cluster_workload_observed_accounting.revision<excluded.revision`)
	assert(`SELECT to_jsonb(a)=to_jsonb(b) FROM loomplane_cluster_workload_observed_accounting a JOIN advanced b USING(org_id,resource_id)`)
	for _, q := range []string{`UPDATE loomplane_cluster_workload_observed_accounting SET revision=6`, `UPDATE loomplane_cluster_workload_observed_accounting SET totals='{}'`, `UPDATE loomplane_cluster_workload_observed_accounting SET schema_version=2`} {
		if _, err := conn.Exec(t.Context(), q); err == nil {
			t.Fatal("invalid accounting update accepted", q)
		}
	}
	exec(t, conn, `INSERT INTO loomplane_cluster_workload_observed_accounting(org_id,cloud_id,resource_id,revision,checked_at,totals,schema_version) VALUES('org','cloud','future',9,clock_timestamp(),'{"future":true}',2)`)
	for _, q := range []string{`SELECT loomplane_cluster_controls_current('org','future')`, `UPDATE loomplane_cluster_workload_observed_accounting SET revision=10,totals='{}' WHERE resource_id='future'`} {
		if _, err := conn.Exec(t.Context(), q); err == nil {
			t.Fatal("future accounting accepted", q)
		}
	}
	assert(`SELECT (SELECT body FROM signed)=(SELECT body FROM original_signed)`)
	assert(`SELECT revision=9 AND schema_version=2 AND totals='{"future":true}' FROM loomplane_cluster_workload_observed_accounting WHERE resource_id='future'`)
}

func TestObservedAccountingMetadataRejectsLegacy(t *testing.T) {
	conn, executor := scratch(t)
	exec(t, conn, `CREATE TABLE loomplane_cluster_workload_observed_accounting(org_id text,cloud_id text,resource_id text,revision bigint,checked_at timestamptz,totals jsonb)`,
		`INSERT INTO loomplane_cluster_workload_observed_accounting VALUES('org','cloud','cluster',7,now(),'{"unrecognized":true}')`)
	if err := migration(t, "observed_accounting_metadata").Up(t.Context(), executor); err == nil {
		t.Fatal("unknown legacy totals relabeled as supported")
	}
	var untouched bool
	if err := conn.QueryRow(t.Context(), `SELECT totals='{"unrecognized":true}'::jsonb AND NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='loomplane_cluster_workload_observed_accounting' AND column_name='schema_version') FROM loomplane_cluster_workload_observed_accounting`).Scan(&untouched); err != nil || !untouched {
		t.Fatal("failed migration changed legacy state", err)
	}
}
