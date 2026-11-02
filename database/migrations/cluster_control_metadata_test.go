//go:build integration

package migrations

import "testing"

func TestClusterControlMetadataMigration(t *testing.T) {
	conn, executor := scratch(t)
	exec(t, conn,
		`CREATE TABLE loomplane_cluster_demand_cohorts(id text PRIMARY KEY,org_id text,cloud_id text,resource_id text,pool_id text,profile text,policy_revision bigint,budget_revision bigint,native boolean,operation_id text,issued_at timestamptz,created_at timestamptz,closed_at timestamptz)`,
		`CREATE TABLE loomplane_cluster_demand_members(cohort_id text REFERENCES loomplane_cluster_demand_cohorts(id),demand_id text)`,
		`CREATE TABLE loomplane_cluster_pool_growth(org_id text,cloud_id text,resource_id text,pool_id text,operation_id text,issued_at timestamptz,next_growth_at timestamptz,PRIMARY KEY(org_id,cloud_id,resource_id,pool_id))`,
		`CREATE TABLE loomplane_cluster_pool_advice(org_id text,cloud_id text,resource_id text,pool_id text,policy_revision bigint,observed_at timestamptz)`,
		`CREATE TABLE loomplane_cluster_capacity_inventory(org_id text,cloud_id text,resource_id text,saved_revision bigint,checked_at timestamptz,body jsonb)`,
		`INSERT INTO loomplane_cluster_demand_cohorts VALUES('6de5589a-96fe-46b5-b774-405c370464b5','org','cloud','cluster','pool','signed-profile',1,1,true,NULL,NULL,now()-interval '1 day',NULL)`,
		`INSERT INTO loomplane_cluster_demand_members VALUES('6de5589a-96fe-46b5-b774-405c370464b5','signed-demand')`,
		`CREATE TABLE original AS SELECT * FROM loomplane_cluster_demand_cohorts`)
	up(t, executor, "cluster_control_metadata")
	assert := func(query string) {
		t.Helper()
		var ok bool
		if err := conn.QueryRow(t.Context(), query).Scan(&ok); err != nil || !ok {
			t.Fatal(query, ok, err)
		}
	}
	assert(`SELECT (to_jsonb(c)-ARRAY['schema_version','revision','updated_at'])=to_jsonb(o) AND c.schema_version=1 AND c.revision=1 AND c.updated_at>=c.created_at FROM loomplane_cluster_demand_cohorts c JOIN original o USING(id)`)
	exec(t, conn, `INSERT INTO loomplane_cluster_demand_members SELECT id,'second' FROM original`)
	assert(`SELECT revision=2 FROM loomplane_cluster_demand_cohorts`)
	exec(t, conn, `UPDATE loomplane_cluster_demand_cohorts SET operation_id='operation',issued_at=clock_timestamp()`)
	assert(`SELECT revision=3 FROM loomplane_cluster_demand_cohorts`)
	exec(t, conn, `UPDATE loomplane_cluster_demand_cohorts SET operation_id='operation'`)
	assert(`SELECT revision=3 FROM loomplane_cluster_demand_cohorts`)
	exec(t, conn, `DELETE FROM loomplane_cluster_demand_members WHERE demand_id='second'`)
	assert(`SELECT revision=4 FROM loomplane_cluster_demand_cohorts`)
	exec(t, conn, `UPDATE loomplane_cluster_demand_cohorts SET closed_at=clock_timestamp()`)
	assert(`SELECT revision=5 AND created_at=(SELECT created_at FROM original) FROM loomplane_cluster_demand_cohorts`)
	assert(`SELECT m.cohort_id=o.id AND m.demand_id='signed-demand' FROM loomplane_cluster_demand_members m JOIN original o ON o.id=m.cohort_id`)
}
