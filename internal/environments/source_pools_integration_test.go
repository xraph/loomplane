//go:build integration

package environments

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
)

func TestSourcePoolsLifecycle(t *testing.T) {
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated PostgreSQL not configured")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	observer, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observer.Close(context.Background()) }()
	assertGone := func(pid int) {
		t.Helper()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			var exists bool
			if e := observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1)`, pid).Scan(&exists); e != nil {
				t.Fatal(e)
			}
			if !exists {
				return
			}
			select {
			case <-tick.C:
			case <-ctx.Done():
				t.Fatal("source connection leaked")
			}
		}
	}
	pools, err := OpenSourcePools(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var sqlPID, vaultPID int
	if err = pools.SQL.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&sqlPID); err != nil {
		t.Fatal(err)
	}
	if err = pgdriver.Unwrap(pools.Vault).QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&vaultPID); err != nil {
		t.Fatal(err)
	}
	if sqlPID == vaultPID {
		t.Fatal("source and Vault share a connection")
	}
	if err = pools.Close(); err != nil {
		t.Fatal(err)
	}
	if err = pools.Close(); err != nil {
		t.Fatal(err)
	}
	assertGone(sqlPID)
	assertGone(vaultPID)
	// A failure opening the second pool closes the already-open first one.
	calls := 0
	_, err = openSourcePools(func() (*pgdriver.PgDB, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("injected second pool failure")
		}
		db := pgdriver.New()
		if e := db.Open(ctx, dsn, driver.WithPoolSize(1)); e != nil {
			return nil, e
		}
		if e := db.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&sqlPID); e != nil {
			_ = db.Close()
			return nil, e
		}
		return db, nil
	})
	if err == nil || calls != 2 {
		t.Fatal("partial startup failure was not exercised")
	}
	assertGone(sqlPID)
}
