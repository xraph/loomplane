//go:build integration

package providerresources

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
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

func inventoryDatabase(t *testing.T) (*PgStore, *pgdriver.PgDB) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for provider resource persistence checks")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("provider_resources_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db := pgdriver.New()
	if err = db.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	found := false
	for _, m := range migrations.Group().Migrations() {
		if m.Name == "provider_resources" {
			if err = m.Up(ctx, pgmigrate.New(db)); err != nil {
				t.Fatal(err)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("provider resource migration not registered")
	}
	if _, err = db.Exec(ctx, `CREATE TABLE loomplane_cloud_connections(id text PRIMARY KEY,org_id text NOT NULL,cloud text NOT NULL,generation bigint NOT NULL,status text NOT NULL); INSERT INTO loomplane_cloud_connections VALUES ('one','org','digitalocean',1,'ready'),('two','org','digitalocean',1,'ready')`); err != nil {
		t.Fatal(err)
	}
	return NewPgStore(db), db
}
func TestPgInventoryMigrationRestartAndGenerationFence(t *testing.T) {
	store, db := inventoryDatabase(t)
	s, _, _, _ := fixture(t)
	s.deps.Store = store
	for _, id := range []string{"one", "two"} {
		if _, err := s.Sync(t.Context(), principal, id); err != nil {
			t.Fatal(err)
		}
	}
	// A fresh store/service reads the persisted identity and both safe attempts.
	restarted := NewService(s.deps)
	restarted.deps.Store = NewPgStore(db)
	list, err := restarted.List(t.Context(), principal)
	if err != nil || len(list.Resources) != 1 || len(list.Syncs) != 2 {
		t.Fatalf("restart: %+v %v", list, err)
	}
	r := list.Resources[0]
	if _, err = store.Get(t.Context(), "other", r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross organization: %v", err)
	}
	if _, err = db.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET generation=2 WHERE id='two'`); err != nil {
		t.Fatal(err)
	}
	mutated := false
	err = store.Update(t.Context(), "org", r.Provider, r.AccountIdentity, "two", 1, func(state *AccountState) error { mutated = true; state.Resources[0].Name = "changed"; return nil })
	if !errors.Is(err, ErrChanged) || mutated {
		t.Fatalf("stale generation ran mutation: %v %v", err, mutated)
	}
	got, err := store.Get(t.Context(), "org", r.ID)
	if err != nil || got.Name != "db" {
		t.Fatalf("fenced row: %+v %v", got, err)
	}
	err = store.Update(t.Context(), "org", r.Provider, r.AccountIdentity, "", 0, func(state *AccountState) error { state.Resources[0].Name = "rollback"; return ErrInvalid })
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	got, _ = store.Get(t.Context(), "org", r.ID)
	if got.Name != "db" {
		t.Fatal("failed transaction changed observation")
	}
}
func TestPgInventoryConcurrentAccountFence(t *testing.T) {
	store, _ := inventoryDatabase(t)
	s, _, reader, _ := fixture(t)
	s.deps.Store = store
	entered, release := make(chan struct{}), make(chan struct{})
	original := reader.fn
	reader.fn = func(c *connections.Connection) (provider.ResourceInventory, error) {
		if c.ID == "one" {
			close(entered)
			<-release
			return provider.ResourceInventory{Scope: "account", Complete: true}, nil
		}
		return original(c)
	}
	done := make(chan error, 1)
	go func() { _, err := s.Sync(context.Background(), principal, "one"); done <- err }()
	<-entered
	if _, err := s.Sync(t.Context(), principal, "two"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrChanged) {
		t.Fatalf("old run survived: %v", err)
	}
	got, err := store.List(t.Context(), "org")
	if err != nil || len(got.Resources) != 1 || got.Resources[0].Status == "missing" {
		t.Fatalf("concurrent persisted inventory: %+v %v", got, err)
	}
	statuses := map[string]int{}
	for _, run := range got.Syncs {
		statuses[run.Status]++
	}
	if statuses["superseded"] != 1 || statuses["complete"] != 1 {
		t.Fatalf("durable run history: %+v", statuses)
	}
}

func TestPgInventoryPublishRejectsRotationAfterLastServiceRead(t *testing.T) {
	store, db := inventoryDatabase(t)
	s, _, reader, _ := fixture(t)
	s.deps.Store = store
	original := reader.fn
	reader.fn = func(c *connections.Connection) (provider.ResourceInventory, error) {
		out, err := original(c)
		if _, dbErr := db.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET generation=2 WHERE id='one'`); dbErr != nil {
			t.Fatal(dbErr)
		}
		return out, err
	}
	run, err := s.Sync(t.Context(), principal, "one")
	if !errors.Is(err, ErrChanged) || run == nil || run.Status != "superseded" {
		t.Fatalf("publication after credential rotation: %+v %v", run, err)
	}
	list, err := store.List(t.Context(), "org")
	if err != nil || len(list.Resources) != 0 || len(list.Syncs) != 1 || list.Syncs[0].Status != "superseded" {
		t.Fatalf("rotation history: %+v %v", list, err)
	}
}
