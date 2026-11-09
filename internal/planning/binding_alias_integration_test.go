//go:build integration

package planning

import (
	"context"
	"errors"
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

// A binding's alias survives a redeploy, is unique within its environment
// only, and clears to empty.
func TestPgStore_bindingAlias(t *testing.T) {
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the PostgreSQL binding alias test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("binding_alias_%d", time.Now().UnixNano())
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
	for _, stmt := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('shop','org','Shop','shop','ready','usr',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('env_prod','org','shop','production','production',now(),now()),('env_stage','org','shop','staging','staging',now(),now())`,
	} {
		if _, err = pg.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	store := NewPgStore(pg)

	now := time.Now().UTC()
	put := func(id, env, bname string) *Binding {
		b := &Binding{ID: id, OrgID: "org", ProjectID: "shop", EnvironmentID: env, Subject: ResourceSubject, Name: bname,
			Provider: "server", Engine: "fabriq", CreatedAt: now, UpdatedAt: now}
		if putErr := store.PutBinding(ctx, b); putErr != nil {
			t.Fatalf("put %s: %v", id, putErr)
		}
		return b
	}
	alias := func(id string) string {
		b, getErr := store.GetBinding(ctx, "org", id)
		if getErr != nil {
			t.Fatal(getErr)
		}
		return b.Alias
	}
	api := put("b1", "env_prod", "api")
	put("b2", "env_prod", "orders")
	put("b3", "env_stage", "api")

	if err = store.SetBindingAlias(ctx, "org", "b1", "shop-api"); err != nil {
		t.Fatal(err)
	}
	api.Revision = "v2"
	if err = store.PutBinding(ctx, api); err != nil {
		t.Fatal(err)
	}
	if got := alias("b1"); got != "shop-api" {
		t.Fatalf("after a redeploy the alias is %q, want shop-api", got)
	}
	if err = store.SetBindingAlias(ctx, "org", "b2", "shop-api"); !errors.Is(err, ErrAliasTaken) {
		t.Fatalf("a second shop-api in production: %v, want ErrAliasTaken", err)
	}
	if err = store.SetBindingAlias(ctx, "org", "b3", "shop-api"); err != nil {
		t.Fatalf("shop-api in staging: %v", err)
	}
	if err = store.SetBindingAlias(ctx, "org", "b1", ""); err != nil {
		t.Fatal(err)
	}
	if err = store.SetBindingAlias(ctx, "org", "b2", ""); err != nil {
		t.Fatalf("two empty aliases in production: %v", err)
	}
	if got := alias("b1"); got != "" {
		t.Fatalf("cleared alias is %q", got)
	}
	if err = store.SetBindingAlias(ctx, "other-org", "b1", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another organization's binding: %v, want ErrNotFound", err)
	}
}
