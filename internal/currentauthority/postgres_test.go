package currentauthority

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xraph/grove/driver"
)

type pgFixture struct {
	coordinator *Coordinator
	main        *pgxpool.Pool
	native      *nativeBackend
	schema      string
}

var schemaSequence atomic.Int64

func newPGFixture(t *testing.T) *pgFixture {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_CURRENT_AUTHORITY_TEST_DSN")
	if dsn == "" {
		t.Skip("set LOOMPLANE_CURRENT_AUTHORITY_TEST_DSN for isolated PostgreSQL on port 55471")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	if cfg.ConnConfig.Port != 55471 || (cfg.ConnConfig.Host != "localhost" && cfg.ConnConfig.Host != "127.0.0.1") {
		t.Fatal("authority tests require isolated localhost PostgreSQL 55471")
	}
	cfg.MaxConns = 1
	cfg.MinConns = 0
	cfg.MinIdleConns = 0
	schema := fmt.Sprintf("authority_%d_%d", time.Now().UnixNano(), schemaSequence.Add(1))
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	main, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(main.Close)
	if _, err = main.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		dropCtx, stopDrop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopDrop()
		if _, dropErr := main.Exec(dropCtx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); dropErr != nil {
			t.Error(dropErr)
		}
	})
	for _, table := range authorityTables {
		_, err = main.Exec(ctx, "CREATE TABLE "+pgx.Identifier{schema, table}.Sanitize()+` (id text PRIMARY KEY, expires_at timestamptz, not_before timestamptz, not_after timestamptz, is_active boolean DEFAULT true, ban_expires timestamptz, banned boolean DEFAULT false)`)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = main.Exec(ctx, `CREATE TABLE candidates (id text PRIMARY KEY, attempt text UNIQUE NOT NULL, actor text NOT NULL, digest text NOT NULL, authorized_at timestamptz NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	authorityDSN, err := withDSNParams(dsn, map[string]string{"search_path": schema})
	if err != nil {
		t.Fatal(err)
	}
	native, err := openPools(ctx, authorityDSN, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{backend: native, schema: schema, profile: &profile{}, telemetry: native.telemetry, source: func(ctx context.Context, tx driver.Tx, _ Binding) error {
		_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, schema+"/source")
		return err
	}}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := c.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return &pgFixture{coordinator: c, main: main, native: native, schema: schema}
}

func (f *pgFixture) stage(ctx context.Context, a *Attempt, b Binding) error {
	d, err := a.Evaluate(ctx, func(context.Context) error { return nil })
	if err != nil {
		return err
	}
	return a.Stage(ctx, d, b, func(ctx context.Context, tx driver.Tx, at time.Time) error {
		_, err := tx.Exec(ctx, `INSERT INTO candidates(id,attempt,actor,digest,authorized_at) VALUES($1,$2,$3,$4,$5)`, b.RecordID, b.AttemptID, b.Actor.UserID, b.InputDigest, at)
		return err
	})
}

func (f *pgFixture) count(t *testing.T) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var count int
	if err := f.main.QueryRow(ctx, `SELECT count(*) FROM candidates`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f *pgFixture) cleanAttempt(t *testing.T) {
	t.Helper()
	b := testBinding()
	b.RecordID = "clean"
	b.AttemptID = "clean"
	if err := f.coordinator.Run(context.Background(), b, func(ctx context.Context, a *Attempt) error { return f.stage(ctx, a, b) }); err != nil {
		t.Fatalf("later attempt: %v", err)
	}
}

func TestPostgresCommitAndExpiredHandles(t *testing.T) {
	f := newPGFixture(t)
	b := testBinding()
	var retained *Attempt
	var saved context.Context
	var decision *Decision
	err := f.coordinator.Run(context.Background(), b, func(ctx context.Context, a *Attempt) error {
		retained = a
		saved = ctx
		var err error
		decision, err = a.Evaluate(ctx, func(ctx context.Context) error {
			var readonly string
			if readErr := f.native.reader.QueryRow(ctx, `SHOW transaction_read_only`).Scan(&readonly); readErr != nil {
				return readErr
			}
			if readonly != "on" {
				return errors.New("reader is not read-only")
			}
			return nil
		})
		if err != nil {
			return err
		}
		return a.Stage(ctx, decision, b, func(ctx context.Context, tx driver.Tx, at time.Time) error {
			if at.IsZero() {
				return errors.New("missing authorization timestamp")
			}
			_, err := tx.Exec(ctx, `INSERT INTO candidates VALUES($1,$2,$3,$4,$5)`, b.RecordID, b.AttemptID, b.Actor.UserID, b.InputDigest, at)
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.count(t) != 1 {
		t.Fatal("missing committed candidate")
	}
	if _, err := retained.Tx().Exec(saved, "SELECT 1"); !errors.Is(err, ErrExpired) {
		t.Fatalf("retained Tx: %v", err)
	}
	if err := retained.Stage(saved, decision, b, writeNothing); !errors.Is(err, ErrExpired) {
		t.Fatalf("retained decision: %v", err)
	}
	if f.main.Config().MaxConns != 1 || f.native.writer.Config().MaxConns != 1 {
		t.Fatal("connection budget changed")
	}
}

func TestPostgresFailureRollsBackAndReleases(t *testing.T) {
	cases := map[string]func(context.Context, *Attempt, *pgFixture, Binding) error{
		"incomplete": func(context.Context, *Attempt, *pgFixture, Binding) error { return nil },
		"callback error": func(ctx context.Context, a *Attempt, f *pgFixture, b Binding) error {
			if err := f.stage(ctx, a, b); err != nil {
				return err
			}
			return errors.New("callback failed")
		},
		"swallowed exec": func(ctx context.Context, a *Attempt, f *pgFixture, b Binding) error {
			if err := f.stage(ctx, a, b); err != nil {
				return err
			}
			_, _ = a.Tx().Exec(ctx, "SELECT 1/0")
			return nil
		},
		"swallowed scan": func(ctx context.Context, a *Attempt, f *pgFixture, b Binding) error {
			if err := f.stage(ctx, a, b); err != nil {
				return err
			}
			var n int
			_ = a.Tx().QueryRow(ctx, "SELECT 'bad'").Scan(&n)
			return nil
		},
		"swallowed deferred query": func(ctx context.Context, a *Attempt, f *pgFixture, b Binding) error {
			if err := f.stage(ctx, a, b); err != nil {
				return err
			}
			rows, err := a.Tx().Query(ctx, "SELECT 1 / n FROM (VALUES (1),(0)) input(n)")
			if err == nil {
				for rows.Next() {
					var n int
					_ = rows.Scan(&n)
				}
				_ = rows.Err()
				_ = rows.Close()
			}
			return nil
		},
		"unclosed rows": func(ctx context.Context, a *Attempt, f *pgFixture, b Binding) error {
			if err := f.stage(ctx, a, b); err != nil {
				return err
			}
			_, _ = a.Tx().Query(ctx, "SELECT 1")
			return nil
		},
		"unscanned row": func(ctx context.Context, a *Attempt, f *pgFixture, b Binding) error {
			if err := f.stage(ctx, a, b); err != nil {
				return err
			}
			_ = a.Tx().QueryRow(ctx, "SELECT 1")
			return nil
		},
		"commit lifecycle": func(ctx context.Context, a *Attempt, f *pgFixture, b Binding) error {
			if err := f.stage(ctx, a, b); err != nil {
				return err
			}
			_ = a.Tx().Commit()
			return nil
		},
		"rollback lifecycle": func(ctx context.Context, a *Attempt, f *pgFixture, b Binding) error {
			if err := f.stage(ctx, a, b); err != nil {
				return err
			}
			_ = a.Tx().Rollback()
			return nil
		},
		"wrong context": func(ctx context.Context, a *Attempt, f *pgFixture, b Binding) error {
			if err := f.stage(ctx, a, b); err != nil {
				return err
			}
			_, _ = a.Tx().Exec(context.Background(), "SELECT 1")
			return nil
		},
		"changed binding": func(ctx context.Context, a *Attempt, _ *pgFixture, b Binding) error {
			d, err := a.Evaluate(ctx, func(context.Context) error { return nil })
			if err != nil {
				return err
			}
			b.Actor.KeyID = "other"
			_ = a.Stage(ctx, d, b, writeNothing)
			return nil
		},
		"double stage": func(ctx context.Context, a *Attempt, f *pgFixture, b Binding) error {
			if err := f.stage(ctx, a, b); err != nil {
				return err
			}
			_ = a.Stage(ctx, a.decision, b, writeNothing)
			return nil
		},
		"changed profile": func(ctx context.Context, a *Attempt, f *pgFixture, b Binding) error {
			if err := f.stage(ctx, a, b); err != nil {
				return err
			}
			f.coordinator.mu.Lock()
			f.coordinator.profile = &profile{}
			f.coordinator.mu.Unlock()
			return nil
		},
		"close before seal": func(ctx context.Context, a *Attempt, f *pgFixture, b Binding) error {
			if err := f.stage(ctx, a, b); err != nil {
				return err
			}
			if err := f.coordinator.Close(); !errors.Is(err, ErrBusy) {
				return fmt.Errorf("Close: %w", err)
			}
			return nil
		},
	}
	for name, work := range cases {
		t.Run(name, func(t *testing.T) {
			f := newPGFixture(t)
			b := testBinding()
			err := f.coordinator.Run(context.Background(), b, func(ctx context.Context, a *Attempt) error { return work(ctx, a, f, b) })
			if err == nil {
				t.Fatal("failed attempt committed")
			}
			if f.count(t) != 0 {
				t.Fatal("candidate escaped rollback")
			}
			f.cleanAttempt(t)
		})
	}
}

func TestPostgresPanicCleanup(t *testing.T) {
	f := newPGFixture(t)
	b := testBinding()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("missing callback panic")
			}
		}()
		_ = f.coordinator.Run(context.Background(), b, func(ctx context.Context, a *Attempt) error {
			if err := f.stage(ctx, a, b); err != nil {
				t.Fatal(err)
			}
			panic("work panic")
		})
	}()
	if f.count(t) != 0 {
		t.Fatal("panic committed")
	}
	f.cleanAttempt(t)
}

func TestPostgresDeadlineCoversPoolAndQueries(t *testing.T) {
	for _, phase := range []string{"pool", "query"} {
		t.Run(phase, func(t *testing.T) {
			f := newPGFixture(t)
			if phase == "pool" {
				conn, err := f.native.writer.Acquire(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Release()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			start := time.Now()
			err := f.coordinator.Run(ctx, testBinding(), func(ctx context.Context, a *Attempt) error {
				_, err := a.Tx().Exec(ctx, "SELECT pg_sleep(1)")
				return err
			})
			cancel()
			if err == nil || time.Since(start) > time.Second {
				t.Fatalf("deadline: %v (%s)", err, time.Since(start))
			}
			if f.count(t) != 0 {
				t.Fatal("deadline committed")
			}
		})
	}
}

func TestPostgresFenceBlocksEveryAuthorityWriter(t *testing.T) {
	f := newPGFixture(t)
	b := testBinding()
	entered := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- f.coordinator.Run(context.Background(), b, func(ctx context.Context, a *Attempt) error { close(entered); <-release; return f.stage(ctx, a, b) })
	}()
	<-entered
	for _, table := range authorityTables {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		_, err := f.main.Exec(ctx, "INSERT INTO "+pgx.Identifier{f.schema, table}.Sanitize()+"(id) VALUES('blocked')")
		cancel()
		if err == nil {
			t.Errorf("writer bypassed fence: %s", table)
		}
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	for _, table := range authorityTables {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := f.main.Exec(ctx, "INSERT INTO "+pgx.Identifier{f.schema, table}.Sanitize()+"(id) VALUES('after')")
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgresEarlierWriterAndTemporalBoundary(t *testing.T) {
	for _, crossing := range []bool{false, true} {
		t.Run(fmt.Sprint(crossing), func(t *testing.T) {
			f := newPGFixture(t)
			b := testBinding()
			tx, beginErr := f.main.Begin(context.Background())
			if beginErr != nil {
				t.Fatal(beginErr)
			}
			if _, insertErr := tx.Exec(context.Background(), `INSERT INTO warden_assignments(id,expires_at) VALUES('earlier',$1)`, time.Now().Add(-time.Second)); insertErr != nil {
				t.Fatal(insertErr)
			}
			result := make(chan error, 1)
			checked := make(chan struct{})
			go func() {
				result <- f.coordinator.Run(context.Background(), b, func(ctx context.Context, a *Attempt) error {
					close(checked)
					start := time.Now().Add(-2 * time.Second)
					if crossing {
						times := []time.Time{start, start.Add(2 * time.Second)}
						f.coordinator.now = func() time.Time { v := times[0]; times = times[1:]; return v }
					}
					d, err := a.Evaluate(ctx, func(ctx context.Context) error {
						var n int
						if readErr := f.native.reader.QueryRow(ctx, `SELECT count(*) FROM warden_assignments WHERE id='earlier'`).Scan(&n); readErr != nil {
							return readErr
						}
						if n != 1 {
							return errors.New("stale reader")
						}
						return nil
					})
					if err != nil {
						return err
					}
					return a.Stage(ctx, d, b, writeNothing)
				})
			}()
			select {
			case <-checked:
				t.Fatal("fence passed uncommitted writer")
			case <-time.After(40 * time.Millisecond):
			}
			if commitErr := tx.Commit(context.Background()); commitErr != nil {
				t.Fatal(commitErr)
			}
			err := <-result
			if crossing && !errors.Is(err, ErrTemporal) {
				t.Fatalf("crossing: %v", err)
			}
			if !crossing && err != nil {
				t.Fatal(err)
			}
		})
	}
}

type interceptBackend struct {
	backend
	wrap func(nativeTx) nativeTx
}

func (b interceptBackend) begin(ctx context.Context) (nativeTx, error) {
	tx, err := b.backend.begin(ctx)
	if err != nil {
		return nil, err
	}
	return b.wrap(tx), nil
}

type interceptTx struct {
	nativeTx
	commit func(context.Context) error
}

func (t interceptTx) Commit(ctx context.Context) error { return t.commit(ctx) }

func TestPostgresAmbiguousCommitPublishesNoCapability(t *testing.T) {
	f := newPGFixture(t)
	b := testBinding()
	f.coordinator.backend = interceptBackend{backend: f.native, wrap: func(tx nativeTx) nativeTx {
		return interceptTx{nativeTx: tx, commit: func(ctx context.Context) error {
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			return errors.New("injected lost commit response")
		}}
	}}
	issue := func() (string, error) {
		err := f.coordinator.Run(context.Background(), b, func(ctx context.Context, a *Attempt) error { return f.stage(ctx, a, b) })
		if err != nil {
			return "", err
		}
		return b.RecordID, nil
	}
	capability, err := issue()
	if capability != "" || !errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("capability=%q error=%v", capability, err)
	}
	if f.count(t) != 1 {
		t.Fatal("missing committed record for same-identity reconciliation")
	}
}

func TestPostgresCloseAndRetainedGoroutineAfterSeal(t *testing.T) {
	f := newPGFixture(t)
	b := testBinding()
	committed := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	f.coordinator.backend = interceptBackend{backend: f.native, wrap: func(tx nativeTx) nativeTx {
		return interceptTx{nativeTx: tx, commit: func(ctx context.Context) error { close(committed); <-release; return tx.Commit(ctx) }}
	}}
	var saved context.Context
	var attempt *Attempt
	go func() {
		result <- f.coordinator.Run(context.Background(), b, func(ctx context.Context, a *Attempt) error { saved = ctx; attempt = a; return f.stage(ctx, a, b) })
	}()
	<-committed
	if err := f.coordinator.Close(); !errors.Is(err, ErrBusy) {
		t.Fatalf("Close during finalization: %v", err)
	}
	if _, err := attempt.Tx().Exec(saved, "SELECT 1"); !errors.Is(err, ErrExpired) {
		t.Fatalf("post-seal call: %v", err)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if f.count(t) != 1 {
		t.Fatal("post-seal call revoked commit")
	}
}

func TestPostgresPendingGoroutinePreventsSeal(t *testing.T) {
	f := newPGFixture(t)
	b := testBinding()
	result := make(chan error, 1)
	err := f.coordinator.Run(context.Background(), b, func(ctx context.Context, a *Attempt) error {
		if err := f.stage(ctx, a, b); err != nil {
			return err
		}
		go func() { _, err := a.Tx().Exec(ctx, "SELECT pg_sleep(2)"); result <- err }()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			f.coordinator.mu.Lock()
			pending := a.pending
			f.coordinator.mu.Unlock()
			if pending != 0 {
				return nil
			}
			time.Sleep(time.Millisecond)
		}
		return errors.New("SQL did not start")
	})
	if !errors.Is(err, ErrPending) {
		t.Fatalf("seal with pending SQL: %v", err)
	}
	if err := <-result; err == nil {
		t.Fatal("outstanding SQL wasn't canceled")
	}
	if f.count(t) != 0 {
		t.Fatal("pending goroutine committed")
	}
	f.cleanAttempt(t)
}

func TestPostgresReaderReadOnlyAndFixedLockTimeout(t *testing.T) {
	f := newPGFixture(t)
	if _, err := f.native.reader.Exec(context.Background(), `INSERT INTO warden_roles(id) VALUES('bad')`); err == nil {
		t.Fatal("reserved reader accepted write")
	}
	err := f.coordinator.Run(context.Background(), testBinding(), func(ctx context.Context, a *Attempt) error {
		var timeout string
		if err := a.Tx().QueryRow(ctx, "SHOW lock_timeout").Scan(&timeout); err != nil {
			return err
		}
		if timeout != "2s" {
			return fmt.Errorf("lock_timeout=%q", timeout)
		}
		var rows int
		if err := a.Tx().QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE pid=pg_backend_pid() AND mode='ShareLock' AND relation IN (SELECT oid FROM pg_class WHERE relnamespace=$1::regnamespace AND relname=ANY($2))`, f.schema, authorityTables[:]).Scan(&rows); err != nil {
			return err
		}
		if rows != len(authorityTables) {
			return fmt.Errorf("fenced tables=%d", rows)
		}
		return f.stage(ctx, a, testBinding())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPostgresSourceWaitIsBounded(t *testing.T) {
	f := newPGFixture(t)
	tx, err := f.main.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(context.Background(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, f.schema+"/source"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancel()
	err = f.coordinator.Run(ctx, testBinding(), func(context.Context, *Attempt) error { t.Error("source gate bypassed"); return nil })
	if err == nil {
		t.Fatal("source wait succeeded")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Logf("driver bounded failure: %v", err)
	}
}

func TestPostgresAbortedNativeCommitAndCommitDeadline(t *testing.T) {
	for _, mode := range []string{"aborted", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			f := newPGFixture(t)
			b := testBinding()
			f.coordinator.backend = interceptBackend{backend: f.native, wrap: func(tx nativeTx) nativeTx {
				return interceptTx{nativeTx: tx, commit: func(ctx context.Context) error {
					if mode == "aborted" {
						_, _ = tx.Exec(ctx, "SELECT 1/0")
					} else {
						<-ctx.Done()
					}
					return tx.Commit(ctx)
				}}
			}}
			ctx, cancel := context.WithTimeout(context.Background(), AttemptLimit)
			defer cancel()
			err := f.coordinator.Run(ctx, b, func(ctx context.Context, a *Attempt) error { return f.stage(ctx, a, b) })
			if mode == "aborted" && (!errors.Is(err, pgx.ErrTxCommitRollback) || errors.Is(err, ErrCommitUnknown)) {
				t.Fatalf("aborted commit=%v", err)
			}
			if mode == "deadline" && !errors.Is(err, ErrCommitUnknown) {
				t.Fatalf("commit deadline=%v", err)
			}
			if f.count(t) != 0 {
				t.Fatal("failed commit published candidate")
			}
			f.coordinator.backend = f.native
			f.cleanAttempt(t)
		})
	}
}

func TestPostgresQueuedDDLReaderWaitAbortsAndReleases(t *testing.T) {
	t.Run("normal_startup", func(t *testing.T) { queuedDDLQualification(t, 0) })
	// This exceeds the old 300ms whole-attempt deadline. Setup must finish
	// before this test can claim it exercised a queued DDL/reader schedule.
	t.Run("delayed_startup", func(t *testing.T) { queuedDDLQualification(t, 400*time.Millisecond) })
}

type ddlRollbackProbe struct {
	nativeTx
	result chan error
}

func (tx ddlRollbackProbe) Rollback(ctx context.Context) error {
	err := tx.nativeTx.Rollback(ctx)
	tx.result <- err
	return err
}

func queuedDDLQualification(t *testing.T, startupDelay time.Duration) {
	t.Helper()
	f := newPGFixture(t)
	if startupDelay > 0 {
		source := f.coordinator.source
		f.coordinator.source = func(ctx context.Context, tx driver.Tx, b Binding) error {
			timer := time.NewTimer(startupDelay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
			return source(ctx, tx, b)
		}
	}
	rollbackResult := make(chan error, 1)
	f.coordinator.backend = interceptBackend{backend: f.native, wrap: func(tx nativeTx) nativeTx {
		return ddlRollbackProbe{nativeTx: tx, result: rollbackResult}
	}}
	ddlResult := make(chan error, 1)
	ddlStarted := make(chan struct{})
	ddlDone := make(chan struct{})
	var stopDDL context.CancelFunc
	var ddlCtx context.Context
	launched, queued, readerWaiting := false, false, false
	var writerPID, readerPID uint32
	var readFailure error
	t.Cleanup(func() {
		if !launched {
			return
		}
		stopDDL()
		select {
		case <-ddlDone:
		case <-time.After(CleanupLimit):
			t.Error("DDL client did not drain after cancellation")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), AttemptLimit)
	defer cancel()
	err := f.coordinator.Run(ctx, testBinding(), func(ctx context.Context, a *Attempt) error {
		if err := a.Tx().QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); err != nil {
			return err
		}
		var oid uint32
		if err := f.native.reader.QueryRow(ctx, `SELECT 'warden_roles'::regclass::oid, pg_backend_pid()`).Scan(&oid, &readerPID); err != nil {
			return err
		}
		// DDL has its own bounded lifetime, covering the attempt and its cleanup.
		// Its completion is asserted separately from the writer's lock release.
		ddlCtx, stopDDL = context.WithTimeout(context.Background(), AttemptLimit+CleanupLimit)
		launched = true
		go func() {
			defer close(ddlDone)
			close(ddlStarted)
			_, ddlErr := f.main.Exec(ddlCtx, `ALTER TABLE warden_roles ADD COLUMN ddl_probe int`)
			ddlResult <- ddlErr
		}()
		<-ddlStarted
		if err := waitDDLQualificationLock(ctx, a, oid, 0, "AccessExclusiveLock"); err != nil {
			return fmt.Errorf("DDL did not reach its lock queue: %w", err)
		}
		queued = true
		_, evalErr := a.Evaluate(ctx, func(ctx context.Context) error {
			readerCtx, stopReader := context.WithCancel(ctx)
			defer stopReader()
			readDone := make(chan error, 1)
			go func() {
				var count int
				readDone <- f.native.reader.QueryRow(readerCtx, `SELECT count(*) FROM warden_roles`).Scan(&count)
			}()
			waitErr := waitDDLQualificationLock(ctx, a, oid, readerPID, "AccessShareLock")
			if waitErr != nil {
				stopReader()
			}
			readFailure = <-readDone
			if waitErr != nil {
				return fmt.Errorf("reader did not reach its intended lock wait: %w", waitErr)
			}
			readerWaiting = true
			return readFailure
		})
		return evalErr
	})
	if !launched || !queued || !readerWaiting {
		t.Fatalf("required schedule incomplete: launched=%t queued=%t reader_waiting=%t; Run=%v", launched, queued, readerWaiting, err)
	}
	var lockErr *pgconn.PgError
	expectedReadFailure := errors.Is(readFailure, context.DeadlineExceeded) || (errors.As(readFailure, &lockErr) && lockErr.Code == "55P03")
	if !expectedReadFailure || err == nil || !errors.Is(err, readFailure) {
		t.Fatalf("expected reader deadline or lock timeout: reader=%v; Run=%v", readFailure, err)
	}
	select {
	case rollbackErr := <-rollbackResult:
		if rollbackErr != nil {
			t.Fatalf("authority rollback failed independently of reader timeout: %v", rollbackErr)
		}
	default:
		t.Fatal("authority rollback did not complete before Run returned")
	}
	probeCtx, stopProbe := context.WithTimeout(context.Background(), CleanupLimit)
	var remaining int
	probeErr := f.native.reader.QueryRow(probeCtx, `SELECT count(*) FROM pg_locks l
 JOIN pg_class c ON c.oid=l.relation JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE l.pid=$1 AND n.nspname=$2 AND c.relname=ANY($3) AND l.mode='ShareLock' AND l.granted`, writerPID, f.schema, authorityTables[:]).Scan(&remaining)
	stopProbe()
	if probeErr != nil {
		t.Fatalf("could not observe authority lock release: %v", probeErr)
	}
	if remaining != 0 {
		t.Fatalf("writer still holds %d authority SHARE locks after rollback", remaining)
	}
	select {
	case ddlErr := <-ddlResult:
		if ddlErr != nil {
			t.Fatalf("DDL failed after verified authority lock release: %v", ddlErr)
		}
	case <-ddlCtx.Done():
		t.Fatalf("DDL did not finish within its own deadline after verified authority lock release: %v", ddlCtx.Err())
	}
	if f.count(t) != 0 {
		t.Fatal("reader timeout committed a candidate")
	}
	f.cleanAttempt(t)
}

func waitDDLQualificationLock(ctx context.Context, a *Attempt, oid, pid uint32, mode string) error {
	for {
		var waiting bool
		if err := a.Tx().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation=$1 AND ($2::bigint=0 OR pid=$2) AND mode=$3 AND NOT granted)`, oid, pid, mode).Scan(&waiting); err != nil {
			return err
		}
		if waiting {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}
