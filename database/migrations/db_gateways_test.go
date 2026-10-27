//go:build integration

package migrations

import (
	"strings"
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestMigrationReplay(t *testing.T) {
	conn, ex := scratch(t)
	ctx := t.Context()
	// Apply everything else first, so db_gateways is the last applied
	// migration and one rollback undoes exactly it, whatever lands above it.
	others := migrate.NewGroup(GroupName)
	for _, step := range Group().Migrations() {
		if step.Name != "db_gateways" {
			others.MustRegister(step)
		}
	}
	if _, err := migrate.NewOrchestrator(ex, others).Migrate(ctx); err != nil {
		t.Fatalf("up others: %v", err)
	}
	o := migrate.NewOrchestrator(ex, Group())

	tables := func() []string {
		return strs(t, conn, `SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name LIKE 'loomplane_db_gateway%' ORDER BY 1`)
	}
	want := []string{"loomplane_db_gateway_instances", "loomplane_db_gateway_routes", "loomplane_db_gateway_snapshots", "loomplane_db_gateways"}
	same := func(got []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	if _, err := o.Migrate(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	if got := tables(); !same(got) {
		t.Fatalf("after up: %v", got)
	}

	res, err := o.Rollback(ctx)
	if err != nil || len(res.Rollback) != 1 || res.Rollback[0].Name != "db_gateways" {
		t.Fatalf("down: %+v %v", res, err)
	}
	if got := tables(); len(got) != 0 {
		t.Fatalf("after down: %v", got)
	}

	if _, err = o.Migrate(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if got := tables(); !same(got) {
		t.Fatalf("after up again: %v", got)
	}
}

func TestDBGatewaysConstraints(t *testing.T) {
	conn, ex := scratch(t)
	// Running the up again changes nothing.
	up(t, ex, "create_projects", "db_gateways", "db_gateways")
	exec(t, conn, `INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p','o','P','p','active','u',now(),now())`)
	exec(t, conn, `INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('e','o','p','prod','production',now(),now())`)

	key32 := `decode(repeat('ab',32),'hex')`
	key31 := `decode(repeat('ab',31),'hex')`
	gateway := func(id, keys string) string {
		return `INSERT INTO loomplane_db_gateways(org_id,id,project_id,environment_id,name,provider,region,suffix,signing_public_key,sealing_public_key) VALUES('o','` + id + `','p','e','n-` + id + `','cloud-run','r','s',` + keys + `)`
	}
	exec(t, conn, gateway("g1", key32+","+key32))
	exec(t, conn, `INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p2','o2','P','p2','active','u',now(),now())`)
	exec(t, conn, `INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('e2','o2','p2','prod','production',now(),now())`)
	if got := strs(t, conn, `SELECT indexdef FROM pg_indexes WHERE indexname='loomplane_db_gateways_id_unique'`); len(got) != 1 || !strings.Contains(got[0], "UNIQUE") || !strings.Contains(got[0], "(id)") {
		t.Fatalf("id index: %v", got)
	}
	for name, sql := range map[string]string{
		"id taken in another organization": `INSERT INTO loomplane_db_gateways(org_id,id,project_id,environment_id,name,provider,region,suffix,signing_public_key,sealing_public_key) VALUES('o2','g1','p2','e2','other','c','r','s',` + key32 + `,` + key32 + `)`,
		"short signing key":                gateway("g2", key31+","+key32),
		"short sealing key":                gateway("g3", key32+","+key31),
		"space in id":                      gateway("g 4", key32+","+key32),
		"duplicate name":                   `INSERT INTO loomplane_db_gateways(org_id,id,project_id,environment_id,name,provider,region,suffix,signing_public_key,sealing_public_key) VALUES('o','g5','p','e','n-g1','c','r','s',` + key32 + `,` + key32 + `)`,
		"unknown environment":              `INSERT INTO loomplane_db_gateways(org_id,id,project_id,environment_id,name,provider,region,suffix,signing_public_key,sealing_public_key) VALUES('o','g6','p','nope','x','c','r','s',` + key32 + `,` + key32 + `)`,
		"short salt":                       `INSERT INTO loomplane_db_gateway_routes(org_id,gateway_id,id,resource,salt) VALUES('o','g1','r1','res',decode(repeat('ab',15),'hex'))`,
		"unknown state":                    `INSERT INTO loomplane_db_gateway_routes(org_id,gateway_id,id,resource,salt,state) VALUES('o','g1','r1','res',decode(repeat('ab',16),'hex'),'asleep')`,
		"route without gateway":            `INSERT INTO loomplane_db_gateway_routes(org_id,gateway_id,id,resource,salt) VALUES('o','nope','r1','res',decode(repeat('ab',16),'hex'))`,
		"zero snapshot version":            `INSERT INTO loomplane_db_gateway_snapshots(org_id,gateway_id,version,digest) VALUES('o','g1',0,'d')`,
	} {
		if _, err := conn.Exec(t.Context(), sql); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
	exec(t, conn, `INSERT INTO loomplane_db_gateway_routes(org_id,gateway_id,id,resource,salt) VALUES('o','g1','r1','res',decode(repeat('ab',16),'hex'))`)
	exec(t, conn, `DELETE FROM loomplane_db_gateways WHERE id='g1'`)
	if got := strs(t, conn, `SELECT id FROM loomplane_db_gateway_routes`); len(got) != 0 {
		t.Fatalf("routes outlived their gateway: %v", got)
	}
	down(t, ex, "db_gateways", "db_gateways")
	up(t, ex, "db_gateways")
}
