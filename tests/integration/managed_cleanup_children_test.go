//go:build integration

package integration

import (
	"testing"

	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/database/migrations"
)

func TestManagedCleanupChildrenMigration(t *testing.T) {
	f := newCleanupIssuerFixture(t, false)
	g := f.open(t)
	r := f.request
	r.PropagationPolicy = "Orphan"
	issued, err := f.store.Journal("org", g.ID).Issue(f.ctx, f.scope, r)
	if err != nil {
		t.Fatal(err)
	}
	executor := pgmigrate.New(f.db)
	for _, m := range migrations.Group().Migrations() {
		if m.Name != "pending_cleanup_owner" {
			continue
		}
		if err = m.Down(f.ctx, executor); err != nil {
			t.Fatal(err)
		}
		if err = m.Up(f.ctx, executor); err != nil {
			t.Fatal(err)
		}
		insert := func(id, omit, patch string) error {
			_, e := f.sql.Exec(f.ctx, `INSERT INTO loomplane_managed_workload_issuances(id,org_id,release_id,epoch,scope,request,cleanup_grant_id,cleanup_grant_revision,cleanup_children)
 SELECT $2,org_id,release_id,epoch,scope,(request-$3::text)||$4::jsonb,cleanup_grant_id,cleanup_grant_revision,'[]'::jsonb FROM loomplane_managed_workload_issuances WHERE id=$1`, issued, id, omit, patch)
			return e
		}
		for _, tc := range []struct{ omit, patch string }{{"PropagationPolicy", "{}"}, {"", `{"PropagationPolicy":"Foreground"}`}, {"", `{"Method":"PUT"}`}, {"Group", "{}"}, {"Version", "{}"}, {"UID", "{}"}, {"ResourceVersion", "{}"}} {
			if err = insert("invalid", tc.omit, tc.patch); err == nil {
				t.Fatalf("accepted missing %s / %s", tc.omit, tc.patch)
			}
		}
		if err = insert("retained", "", "{}"); err != nil {
			t.Fatal(err)
		}
		if _, err = f.sql.Exec(f.ctx, `DELETE FROM loomplane_managed_workload_issuances WHERE id='retained'`); err == nil {
			t.Fatal("dropped retained ownership evidence")
		}
		if err = m.Down(f.ctx, executor); err == nil {
			t.Fatal("migration discarded retained ownership evidence")
		}
		return
	}
	t.Fatal("missing migration")
}
