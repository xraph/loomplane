//go:build integration

package nodes

import (
	"slices"
	"testing"
)

func TestBindingsOnAndBackupPolicies(t *testing.T) {
	s, conn := testStore(t)
	ctx := t.Context()
	for _, stmt := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('prj_1','org_1','One','one','ready','usr',now(),now()),('prj_2','org_2','Two','two','ready','usr',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('env_1','org_1','prj_1','production','production',now(),now()),('env_2','org_2','prj_2','production','production',now(),now())`,
		`INSERT INTO loomplane_bindings(id,org_id,project_id,environment_id,subject,name,provider,engine,connection_id,config,created_at,updated_at) VALUES
			('bnd_api','org_1','prj_1','env_1','service','api','server','','con_1','{}',now(),now()),
			('bnd_db','org_1','prj_1','env_1','resource','db','server','postgres','con_other','{"kind":"postgres","cloud_placement":{"server_id":"srv_1"}}',now(),now()),
			('bnd_far','org_1','prj_1','env_1','service','far','server','','con_other','{}',now(),now()),
			('bnd_theirs','org_2','prj_2','env_2','service','api','server','','con_1','{}',now(),now())`,
		`INSERT INTO loomplane_backup_policies(id,org_id,project_id,environment_id,environment,resource,every_hours,enabled,next_at,created_by) VALUES
			('bp_1','org_1','prj_1','env_1','production','db',24,true,now(),'usr'),
			('bp_2','org_1','prj_1','env_1','production','api',24,false,now(),'usr'),
			('bp_3','org_2','prj_2','env_2','production','api',24,true,now(),'usr')`,
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	got, err := s.BindingsOn(ctx, "org_1", []string{"con_1"}, "srv_1")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(got))
	for _, b := range got {
		ids = append(ids, b.ID)
		if b.Environment != "production" || b.ProjectID != "prj_1" || len(b.Config) == 0 {
			t.Fatalf("binding %+v", b)
		}
	}
	// Through the node's connection, or pinned to its server; never another
	// org's binding on a connection id that happens to match.
	if !slices.Equal(ids, []string{"bnd_db", "bnd_api"}) {
		t.Fatalf("bindings %v", ids)
	}
	if got, err = s.BindingsOn(ctx, "org_1", nil, ""); err != nil || len(got) != 0 {
		t.Fatalf("nothing asked: %v %v", got, err)
	}
	pols, err := s.BackupPolicies(ctx, "org_1", []string{"env_1"})
	if err != nil || !slices.Equal(pols, []BackupPolicyRef{{EnvironmentID: "env_1", Resource: "db"}}) {
		t.Fatalf("policies %+v %v", pols, err)
	}
}
