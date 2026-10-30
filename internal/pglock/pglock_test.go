package pglock

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
)

// scratchDB makes a database of its own on LOOMPLANE_TEST_DATABASE_URL's
// server, drops it when the test ends, and returns its URL.
func scratchDB(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the advisory lock tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("pglock_%d", time.Now().UnixNano())
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
	return u.String()
}

// open connects a pool of its own, the way each pod has one.
func open(t *testing.T, dsn string) *pgdriver.PgDB {
	t.Helper()
	pg := pgdriver.New()
	if err := pg.Open(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return pg
}

// Two replicas seeding at once is the case this exists for: each looks for
// a row and creates it when it's missing. Under the lock exactly one row
// gets created, and the replicas never run their look-then-create at the
// same time.
func TestDo_replicasTakeTurns(t *testing.T) {
	ctx := t.Context()
	dsn := scratchDB(t)
	first, second := open(t, dsn), open(t, dsn)
	if _, err := first.Exec(ctx, `CREATE TABLE seeds (name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	var inside, overlaps atomic.Int32
	seed := func(pg *pgdriver.PgDB) error {
		return Do(ctx, pg, "loomplane-test-seed", func(ctx context.Context) error {
			if inside.Add(1) > 1 {
				overlaps.Add(1)
			}
			defer inside.Add(-1)
			var n int
			if err := pg.QueryRow(ctx, `SELECT count(*) FROM seeds`).Scan(&n); err != nil {
				return err
			}
			// Long enough that, without the lock, the other replica would
			// look in this window too.
			time.Sleep(50 * time.Millisecond)
			if n == 0 {
				_, err := pg.Exec(ctx, `INSERT INTO seeds(name) VALUES('app')`)
				return err
			}
			return nil
		})
	}

	const rounds = 4
	var wg sync.WaitGroup
	errs := make(chan error, 2*rounds)
	for range rounds {
		for _, pg := range []*pgdriver.PgDB{first, second} {
			wg.Go(func() { errs <- seed(pg) })
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := overlaps.Load(); n != 0 {
		t.Fatalf("%d seeds ran while another held the lock", n)
	}
	var n int
	if err := first.QueryRow(ctx, `SELECT count(*) FROM seeds`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("seeded %d rows, want 1", n)
	}
}

// A failed seed returns its own error and still lets the next replica in.
func TestDo_failureReleasesLock(t *testing.T) {
	ctx := t.Context()
	pg := open(t, scratchDB(t))
	boom := errors.New("seed failed")
	if err := Do(ctx, pg, "loomplane-test-fail", func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("Do() = %v, want the seed's error", err)
	}
	waited, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ran := false
	if err := Do(waited, pg, "loomplane-test-fail", func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("lock still held after a failed seed: ran=%v err=%v", ran, err)
	}
}

func TestDo_rejectsMissingArguments(t *testing.T) {
	if err := Do(t.Context(), nil, "x", func(context.Context) error { return nil }); err == nil {
		t.Fatal("Do accepted a nil database")
	}
}

// For holds the named lock while fn runs, as Do does, so another replica
// can't take it in that time.
func TestFor_holdsTheNamedLock(t *testing.T) {
	ctx := t.Context()
	dsn := scratchDB(t)
	pg, other := open(t, dsn), open(t, dsn)
	var free bool
	err := For(pg)(ctx, "digitalocean-app/app-1", func(ctx context.Context) error {
		return other.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, "digitalocean-app/app-1").Scan(&free)
	})
	if err != nil || free {
		t.Fatalf("another replica took the lock: %v %v", free, err)
	}
}

// TryShared runs alongside other shared holders, but never while Do holds
// the same lock, and it says so rather than wait.
func TestTryShared_neverRunsDuringDo(t *testing.T) {
	ctx := t.Context()
	dsn := scratchDB(t)
	first, second := open(t, dsn), open(t, dsn)
	const name = "loomplane-test-shared"

	// Two shared holders don't keep each other out.
	ran, err := TryShared(ctx, first, name, func(ctx context.Context) error {
		inner, ierr := TryShared(ctx, second, name, func(context.Context) error { return nil })
		if ierr != nil || !inner {
			return fmt.Errorf("a second shared holder was kept out: ran=%v err=%w", inner, ierr)
		}
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("TryShared with nobody holding the lock: ran=%v err=%v", ran, err)
	}

	// While Do holds it, TryShared doesn't run fn and doesn't wait.
	err = Do(ctx, first, name, func(ctx context.Context) error {
		start := time.Now()
		inner, terr := TryShared(ctx, second, name, func(context.Context) error { return nil })
		if terr != nil || inner {
			return fmt.Errorf("TryShared during Do: ran=%v err=%w, want not run", inner, terr)
		}
		if waited := time.Since(start); waited > 2*time.Second {
			return fmt.Errorf("TryShared waited %s for Do", waited)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// An error from fn comes back, and the lock is let go.
	boom := errors.New("judge failed")
	if ran, err = TryShared(ctx, second, name, func(context.Context) error { return boom }); !ran || !errors.Is(err, boom) {
		t.Fatalf("TryShared with a failing fn: ran=%v err=%v", ran, err)
	}
	waited, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = Do(waited, first, name, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("lock still held after TryShared: %v", err)
	}
}
