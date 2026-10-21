//go:build integration

package migrations

import "testing"

func TestNativeSourcesBackfillKeepsPendingReplacementHistory(t *testing.T) {
	conn := scratch(t)
	exec(t, conn,
		`CREATE TABLE loomplane_environments(org_id TEXT,project_id TEXT,id TEXT,UNIQUE(org_id,project_id,id))`,
		`INSERT INTO loomplane_environments VALUES('org','project','environment')`,
		`CREATE TABLE loomplane_bindings(id TEXT,org_id TEXT,project_id TEXT,environment_id TEXT,subject TEXT,name TEXT,provider TEXT,connection_id TEXT,region TEXT,physical_id TEXT,external_id TEXT,config JSONB,replaced JSONB,url TEXT,drift_message TEXT)`,
		`INSERT INTO loomplane_bindings VALUES('binding','org','project','environment','resource','database','rds','account','us-east-1','db-current','database','{}',
 '[{"id":"binding","org_id":"org","project_id":"project","environment_id":"environment","subject":"resource","name":"database","provider":"rds","connection_id":"account","region":"us-east-1","physical_id":"db-older","external_id":"older","config":{},"replaced":[{"id":"binding","org_id":"org","project_id":"project","environment_id":"environment","subject":"resource","name":"database","provider":"rds","connection_id":"account","region":"us-west-2","physical_id":"db-oldest","external_id":"oldest","config":{}}]}]',
 'secret-address','private message')`,
	)
	exec(t, conn, `INSERT INTO loomplane_bindings SELECT 'moved',org_id,project_id,environment_id,subject,name,'server',connection_id,region,'',external_id,config,
 jsonb_build_array(jsonb_set(jsonb_set((replaced->0)-'replaced','{physical_id}','"db-moved"'),'{id}','"moved"')),url,drift_message FROM loomplane_bindings WHERE id='binding'`)
	exec(t, conn, nativeSourcesUp...)
	exec(t, conn, nativeSourcesUp...)
	got := strs(t, conn, `SELECT physical_id FROM loomplane_native_sources ORDER BY physical_id`)
	if len(got) != 4 {
		t.Fatalf("backfill lost pending original incarnations: %v", got)
	}
	if leaked := strs(t, conn, `SELECT id FROM loomplane_native_sources WHERE source ? 'url' OR source ? 'replaced' OR source ? 'drift_message'`); len(leaked) != 0 {
		t.Fatal("source archive retained unrelated private state")
	}
	// Every writer, including one bypassing the application store, retains the
	// old source before replacement and captures the new incarnation atomically.
	exec(t, conn, `UPDATE loomplane_bindings SET physical_id='db-next',url='new-private-address' WHERE id='binding'`)
	if got := strs(t, conn, `SELECT physical_id FROM loomplane_native_sources ORDER BY physical_id`); len(got) != 5 {
		t.Fatalf("binding write missed shared source capture: %v", got)
	}
}
