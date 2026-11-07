//go:build integration

package checkpoints

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	vaultpg "github.com/xraph/vault/store/postgres"

	"github.com/xraph/loomplane/internal/pglock"
)

// Replicas starting together against one database each run Ensure under
// the startup lock, as the identity extension does, and end on one key.
func TestKeysEnsureUnderTheStartupLock(t *testing.T) {
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the checkpoint key test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("checkpoint_keys_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name

	const replicas = 4
	type replica struct {
		pg   *pgdriver.PgDB
		keys *Keys
	}
	rs := make([]replica, replicas)
	for i := range rs {
		pg := pgdriver.New()
		if err = pg.Open(ctx, u.String()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pg.Close() })
		db, err := grove.Open(pg)
		if err != nil {
			t.Fatal(err)
		}
		store := vaultpg.New(db)
		if i == 0 {
			if err = store.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
		}
		rs[i] = replica{pg: pg, keys: NewKeys(vaultService(t, store))}
	}

	ids := make([]string, replicas)
	created := make([]bool, replicas)
	errs := make([]error, replicas)
	var wg sync.WaitGroup
	for i, r := range rs {
		wg.Go(func() {
			errs[i] = pglock.Do(ctx, r.pg, pglock.StartupSeed, func(ctx context.Context) error {
				var ensureErr error
				created[i], ensureErr = r.keys.Ensure(ctx)
				return ensureErr
			})
			if errs[i] == nil {
				_, ids[i], errs[i] = r.keys.PublicKey(ctx)
			}
		})
	}
	wg.Wait()
	made := 0
	for i := range rs {
		if errs[i] != nil {
			t.Fatalf("replica %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("replica %d signs with %q, replica 0 with %q", i, ids[i], ids[0])
		}
		if created[i] {
			made++
		}
	}
	if made != 1 {
		t.Fatalf("%d replicas created a key, want 1", made)
	}
}
