//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/recovery"
)

type interruptedRetentionMigration struct {
	migrate.Executor
	calls, failAt int
}

func (e *interruptedRetentionMigration) Exec(ctx context.Context, query string, args ...any) (driver.Result, error) {
	e.calls++
	if e.failAt == e.calls {
		return nil, errors.New("interrupted migration")
	}
	return e.Executor.Exec(ctx, query, args...)
}

func TestRetentionPolicyMigrationPreservesManualWorkAndResumesEachStatement(t *testing.T) {
	groups := migrations.All()
	older := migrate.NewGroup(migrations.GroupName)
	var target *migrate.Migration
	for _, step := range groups[0].Migrations() {
		if step.Version == "20261021000200" {
			target = step
		} else {
			older.MustRegister(step)
		}
	}
	if target == nil {
		t.Fatal("retention policy migration is not centrally registered")
	}
	groups[0] = older
	conn, store := retirementFixtureSchema(t, groups)
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_backup_retirements(id,org_id,project_id,backup_id,source_key,witness_id,request_id,policy,object,witness_object,status,attempt,token,lease_until,created_by,execution_by)
 SELECT 'manual',b.org_id,b.project_id,b.id,'legacy-source',w.id,'manual-retirement','{"older_than_days":7,"keep_last":1}',b.object,w.object,'running',2,'manual-worker',now()+interval '50 minutes','u','u'
 FROM loomplane_service_backups b JOIN loomplane_service_backups w ON w.id='w' WHERE b.id='b'`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE loomplane_service_backups SET status='retiring' WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err := pg.Open(t.Context(), conn.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	executor := pgmigrate.New(pg)
	recording := &interruptedRetentionMigration{Executor: executor}
	if err := target.Up(t.Context(), recording); err != nil {
		t.Fatal(err)
	}
	if recording.calls == 0 {
		t.Fatal("empty policy migration")
	}
	for failAt := 1; failAt <= recording.calls; failAt++ {
		if err := target.Down(t.Context(), executor); err != nil {
			t.Fatal(err)
		}
		broken := &interruptedRetentionMigration{Executor: executor, failAt: failAt}
		if err := target.Up(t.Context(), broken); err == nil {
			t.Fatalf("migration did not interrupt at statement %d", failAt)
		}
		if err := target.Up(t.Context(), executor); err != nil {
			t.Fatalf("migration replay at statement %d: %v", failAt, err)
		}
		record, err := store.GetRetirement(t.Context(), "o", "manual")
		if err != nil || record.RetentionPolicyID != "" || record.RetentionRevision != 0 || record.Status != "running" || record.Attempt != 2 || record.Token != "manual-worker" || record.ExecutionBy != "u" {
			t.Fatalf("manual execution changed at statement %d: %+v %v", failAt, record, err)
		}
	}
	input := retentionPolicyRequest()
	input.Enabled = false
	policy, err := store.PutRetentionPolicy(t.Context(), input, 0, retentionGuard)
	if err != nil {
		t.Fatal(err)
	}
	if err = target.Up(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	if err = target.Down(t.Context(), executor); err == nil {
		t.Fatal("downgrade discarded policy authorization history")
	}
	found, err := store.GetRetentionPolicy(t.Context(), "o", policy.ID)
	if err != nil || found.Revision != 1 || found.Enabled {
		t.Fatalf("refused downgrade changed policy: %+v %v", found, err)
	}
	var count int
	if err = conn.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_retention_policy_revisions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("policy history changed: %d %v", count, err)
	}
}

func policyMigration(t *testing.T) *migrate.Migration {
	t.Helper()
	for _, step := range migrations.Group().Migrations() {
		if step.Version == "20261021000200" {
			return step
		}
	}
	t.Fatal("retention policy migration not found")
	return nil
}

func TestRetentionPolicyMigrationDownCanReplayAfterDrops(t *testing.T) {
	conn, _ := retirementFixture(t)
	pg := pgdriver.New()
	if err := pg.Open(t.Context(), conn.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	target, executor := policyMigration(t), pgmigrate.New(pg)
	if err := target.Down(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	if err := target.Down(t.Context(), executor); err != nil {
		t.Fatalf("replayed downgrade failed after interrupted bookkeeping: %v", err)
	}
}

type afterRetentionMigrationStatement struct {
	migrate.Executor
	once  sync.Once
	after func()
}

func (e *afterRetentionMigrationStatement) Exec(ctx context.Context, query string, args ...any) (driver.Result, error) {
	result, err := e.Executor.Exec(ctx, query, args...)
	if err == nil {
		e.once.Do(e.after)
	}
	return result, err
}

func TestRetentionPolicyMigrationDownCannotEraseInterleavedAuthorization(t *testing.T) {
	conn, store := retirementFixture(t)
	pg := pgdriver.New()
	if err := pg.Open(t.Context(), conn.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	var saved *recovery.RetentionPolicy
	var writeErr error
	executor := &afterRetentionMigrationStatement{Executor: pgmigrate.New(pg), after: func() {
		saved, writeErr = store.PutRetentionPolicy(t.Context(), retentionPolicyRequest(), 0, retentionGuard)
	}}
	downErr := policyMigration(t).Down(t.Context(), executor)
	if writeErr == nil {
		found, err := store.GetRetentionPolicy(t.Context(), "o", saved.ID)
		if err != nil || found.Revision != saved.Revision {
			t.Fatalf("downgrade erased a committed policy authorization: down=%v saved=%+v read=%v", downErr, saved, err)
		}
	} else {
		var sqlErr *pgconn.PgError
		if !errors.As(writeErr, &sqlErr) || sqlErr.Code != "42P01" {
			t.Fatalf("writer failed outside the expected removed-table boundary: %v", writeErr)
		}
		if downErr != nil {
			t.Fatalf("empty downgrade failed: %v", downErr)
		}
	}
}

func TestRetentionPolicyMigrationDownPreservesAConcurrentWriter(t *testing.T) {
	conn, store := retirementFixture(t)
	pg := pgdriver.New()
	if err := pg.Open(t.Context(), conn.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	written, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	type writeResult struct {
		policy *recovery.RetentionPolicy
		err    error
	}
	writes := make(chan writeResult, 1)
	go func() {
		calls := 0
		policy, err := store.PutRetentionPolicy(ctx, retentionPolicyRequest(), 0, func(ctx context.Context) error {
			calls++
			if calls == 2 {
				close(written)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
		writes <- writeResult{policy, err}
	}()
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	downs := make(chan error, 1)
	target := policyMigration(t)
	go func() { downs <- target.Down(ctx, pgmigrate.New(pg)) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%retention_policies%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("downgrade did not wait for the active policy transaction")
		}
		time.Sleep(10 * time.Millisecond)
	}
	unblock()
	result := <-writes
	if result.err != nil {
		t.Fatal(result.err)
	}
	if err := <-downs; err == nil || !strings.Contains(err.Error(), "retention policy authorization history must be preserved") {
		t.Fatalf("downgrade failed to preserve the committed writer: %v", err)
	}
	current, err := store.GetRetentionPolicy(ctx, "o", result.policy.ID)
	if err != nil || current.Revision != result.policy.Revision {
		t.Fatalf("concurrent authorization history lost: %+v %v", current, err)
	}
}
