//go:build integration

package planning

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
)

// OrgResourceBindings names the environment and project each resource
// runs in, and lists only the organization's resources.
func TestPgStore_orgResourceBindings(t *testing.T) {
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the PostgreSQL organization resources test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("org_resources_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	if err = ops.Migrate(ctx, u.String(), migrations.All()); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	binding := func(id, org, project, env, subject, name string) string {
		return fmt.Sprintf(`INSERT INTO loomplane_bindings(id,org_id,project_id,environment_id,subject,name,provider,engine,created_at,updated_at) VALUES('%s','%s','%s','%s','%s','%s','server','fabriq',now(),now())`, id, org, project, env, subject, name)
	}
	for _, stmt := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('shop','org','Shop','shop','ready','usr',now(),now()),('blog','org','Blog','blog','ready','usr',now(),now()),('else','org-2','Else','else','ready','usr',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('env_prod','org','shop','production','production',now(),now()),('env_stage','org','shop','staging','staging',now(),now()),('env_blog','org','blog','live','production',now(),now()),('env_else','org-2','else','production','production',now(),now())`,
		binding("b1", "org", "shop", "env_stage", "resource", "orders"),
		binding("b2", "org", "blog", "env_blog", "resource", "posts"),
		binding("b3", "org", "shop", "env_prod", "service", "api"),
		binding("b4", "org-2", "else", "env_else", "resource", "theirs"),
	} {
		if _, err = pg.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	got, err := NewPgStore(pg).OrgResourceBindings(ctx, "org")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"b1 orders staging Shop", "b2 posts live Blog"}
	if len(got) != len(want) {
		t.Fatalf("got %d resources", len(got))
	}
	for i, r := range got {
		if line := r.ID + " " + r.Name + " " + r.Environment + " " + r.Project; line != want[i] {
			t.Fatalf("resource %d: %q, want %q", i, line, want[i])
		}
	}
}
