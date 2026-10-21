//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

func TestRetirementRetryPreservesCreatorAndLifetimeAttempts(t *testing.T) {
	_, store := retirementFixture(t)
	r, err := store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimRetirement(t.Context(), "o", r.ID, "old-worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishRetirement(t.Context(), first, errors.New("temporary failure")); err != nil {
		t.Fatal(err)
	}
	retried, err := store.RetryRetirement(t.Context(), "o", r.ID, "replacement-owner", "retry-request-1", func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if retried.CreatedBy != "u" || retried.ExecutionBy != "replacement-owner" || retried.Attempt != 1 || retried.AttemptLimit != 4 || retried.Status != "queued" {
		t.Fatalf("retry erased authority or attempts: %+v", retried)
	}
	if err = store.FinishRetirement(t.Context(), first, nil); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("old worker published across retry: %v", err)
	}
	again, err := store.RetryRetirement(t.Context(), "o", r.ID, "replacement-owner", "retry-request-1", func(context.Context) error { return nil })
	if err != nil || again.AttemptLimit != 4 {
		t.Fatalf("idempotent retry changed budget: %+v %v", again, err)
	}
	if _, err = store.RetryRetirement(t.Context(), "o", r.ID, "another-owner", "retry-request-1", func(context.Context) error { return nil }); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("request key reassigned execution authority: %v", err)
	}
	claim, err := store.ClaimRetirement(t.Context(), "o", r.ID, "replacement-worker")
	if err != nil || claim.ExecutionBy != "replacement-owner" || claim.Attempt != 2 {
		t.Fatalf("claim lost retry authority: %+v %v", claim, err)
	}
	if err = store.FinishRetirement(t.Context(), claim, nil); err != nil {
		t.Fatal(err)
	}
	page, err := store.RetirementHistory(t.Context(), "o", r.ID, recovery.RetirementHistoryInput{Limit: 2})
	if err != nil || len(page.Events) != 2 || page.NextBefore == "" {
		t.Fatalf("history page: %+v %v", page, err)
	}
	if page.Events[0].Kind != "succeeded" || page.Events[0].Actor != "replacement-owner" || page.Events[1].Kind != "claimed" || page.Events[1].Attempt != 2 {
		t.Fatalf("history lost execution authority: %+v", page)
	}
	next, err := store.RetirementHistory(t.Context(), "o", r.ID, recovery.RetirementHistoryInput{Before: page.NextBefore, Limit: 10})
	if err != nil || len(next.Events) != 4 || next.NextBefore != "" || next.Events[0].Kind != "retry" || next.Events[0].PreviousActor != "u" || next.Events[0].PreviousError == "" {
		t.Fatalf("history lost previous failure or pagination: %+v %v", next, err)
	}
	if _, err = store.RetirementHistory(t.Context(), "foreign", r.ID, recovery.RetirementHistoryInput{Limit: 10}); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatalf("foreign history disclosed: %v", err)
	}
	if _, err = store.RetryRetirement(t.Context(), "o", r.ID, "replacement-owner", "retry-after-success", func(context.Context) error { return nil }); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("completed retirement retried: %v", err)
	}
}

func TestRetirementRetryRollsBackWhenOwnerRevokedAtCommit(t *testing.T) {
	_, store := retirementFixture(t)
	r, err := store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRetirement(t.Context(), "o", r.ID, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishRetirement(t.Context(), claim, errors.New("failed")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = store.RetryRetirement(t.Context(), "o", r.ID, "replacement", "revoked-retry-key", func(context.Context) error {
		calls++
		if calls >= 2 {
			return security.ErrForbidden
		}
		return nil
	})
	if !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("revoked owner retry: %v", err)
	}
	current, err := store.GetRetirement(t.Context(), "o", r.ID)
	if err != nil || current.ExecutionBy != "u" || current.Status != "failed" || current.AttemptLimit != 3 {
		t.Fatalf("revoked retry changed durable state: %+v %v", current, err)
	}
	history, err := store.RetirementHistory(t.Context(), "o", r.ID, recovery.RetirementHistoryInput{Limit: 100})
	if err != nil || len(history.Events) != 3 {
		t.Fatalf("revoked retry left an audit event: %+v %v", history, err)
	}
	if _, err = store.CreateRestore(t.Context(), restoreAdmission("retry-rollback-restore")); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("retry rollback released restore barrier: %v", err)
	}
}

func TestRetirementLeaseReclaimRecordsExpiredAttempt(t *testing.T) {
	conn, store := retirementFixture(t)
	r, err := store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimRetirement(t.Context(), "o", r.ID, "worker-one"); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(t.Context(), `UPDATE loomplane_backup_retirements SET lease_until=now()-interval '1 minute' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimRetirement(t.Context(), "o", r.ID, "worker-two"); err != nil {
		t.Fatal(err)
	}
	history, err := store.RetirementHistory(t.Context(), "o", r.ID, recovery.RetirementHistoryInput{Limit: 10})
	if err != nil || len(history.Events) != 4 || history.Events[0].Kind != "claimed" || history.Events[0].Attempt != 2 || history.Events[1].Kind != "expired" || history.Events[1].Attempt != 1 || history.Events[1].Error == "" {
		t.Fatalf("lost expired execution: %+v %v", history, err)
	}
}

func TestRetirementRetryCannotBypassLiveLeaseOrBackupBarrier(t *testing.T) {
	conn, store := retirementFixture(t)
	r, err := store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRetirement(t.Context(), "o", r.ID, "live-worker")
	if err != nil {
		t.Fatal(err)
	}
	retry := func() error {
		_, retryErr := store.RetryRetirement(t.Context(), "o", r.ID, "replacement", "barrier-retry-key", func(context.Context) error { return nil })
		return retryErr
	}
	if err = retry(); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("retry stole a live worker: %v", err)
	}
	if err = store.FinishRetirement(t.Context(), claim, errors.New("failed")); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(t.Context(), `UPDATE loomplane_service_backups SET status='succeeded' WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	if err = retry(); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("retry accepted missing backup barrier: %v", err)
	}
	history, err := store.RetirementHistory(t.Context(), "o", r.ID, recovery.RetirementHistoryInput{Limit: 100})
	if err != nil || len(history.Events) != 3 {
		t.Fatalf("rejected retries changed history: %+v %v", history, err)
	}
}

func TestRetirementRetryGrantsBoundedBudgetAfterExhaustion(t *testing.T) {
	conn, store := retirementFixture(t)
	r, err := store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		claim, claimErr := store.ClaimRetirement(t.Context(), "o", r.ID, "worker")
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		if err = store.FinishRetirement(t.Context(), claim, errors.New("temporary failure")); err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Exec(t.Context(), `UPDATE loomplane_backup_retirements SET next_at=now()-interval '1 minute' WHERE id=$1`, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := store.PendingRetirements(t.Context())
	if err != nil || len(pending) != 0 {
		t.Fatalf("exhausted retirement requeued: %+v %v", pending, err)
	}
	if _, err = store.ClaimRetirement(t.Context(), "o", r.ID, "exhausted-worker"); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("worker exceeded budget: %v", err)
	}
	retry, err := store.RetryRetirement(t.Context(), "o", r.ID, "replacement", "exhausted-retry-key", func(context.Context) error { return nil })
	if err != nil || retry.Attempt != 3 || retry.AttemptLimit != 6 {
		t.Fatalf("retry did not grant bounded budget: %+v %v", retry, err)
	}
	claim, err := store.ClaimRetirement(t.Context(), "o", r.ID, "new-worker")
	if err != nil || claim.Attempt != 4 || claim.ExecutionBy != "replacement" {
		t.Fatalf("retry did not preserve total attempts: %+v %v", claim, err)
	}
}

func TestConcurrentRetirementRetriesHaveOneWinner(t *testing.T) {
	_, store := retirementFixture(t)
	r, err := store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRetirement(t.Context(), "o", r.ID, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishRetirement(t.Context(), claim, errors.New("failed")); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 8)
	for i := range 8 {
		go func() {
			_, retryErr := store.RetryRetirement(t.Context(), "o", r.ID, "replacement", fmt.Sprintf("retry-request-%d", i), func(context.Context) error { return nil })
			results <- retryErr
		}()
	}
	wins := 0
	for range 8 {
		select {
		case err = <-results:
			if err == nil {
				wins++
			} else if !errors.Is(err, recovery.ErrConflict) {
				t.Fatalf("concurrent retry leaked an error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent retry deadlocked")
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent retries granted %d budgets", wins)
	}
	history, err := store.RetirementHistory(t.Context(), "o", r.ID, recovery.RetirementHistoryInput{Limit: 100})
	if err != nil || len(history.Events) != 4 || history.Events[0].Kind != "retry" || history.Events[0].AttemptLimit != 4 {
		t.Fatalf("concurrent retry history: %+v %v", history, err)
	}
}

func TestRetirementFinishFencesAttemptEvenIfTokenIsReused(t *testing.T) {
	conn, store := retirementFixture(t)
	r, err := store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimRetirement(t.Context(), "o", r.ID, "reused-worker-token")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishRetirement(t.Context(), first, errors.New("failed")); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(t.Context(), `UPDATE loomplane_backup_retirements SET next_at=now()-interval '1 minute' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimRetirement(t.Context(), "o", r.ID, "reused-worker-token"); err != nil {
		t.Fatal(err)
	}
	if err = store.FinishRetirement(t.Context(), first, nil); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("old attempt published with a reused token: %v", err)
	}
}

func TestRetirementHistoryMigrationPreservesExistingExecution(t *testing.T) {
	groups := migrations.All()
	older := migrate.NewGroup(migrations.GroupName)
	var historyMigration *migrate.Migration
	for _, migration := range groups[0].Migrations() {
		if migration.Version >= "20261020000700" {
			if migration.Version == "20261020000700" {
				historyMigration = migration
			}
			continue
		}
		older.MustRegister(migration)
	}
	if historyMigration == nil {
		t.Fatal("history migration missing")
	}
	groups[0] = older
	conn, store := retirementFixtureSchema(t, groups)
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_backup_retirements(id,org_id,project_id,backup_id,source_key,witness_id,request_id,policy,object,witness_object,status,attempt,token,lease_until,created_by)
 SELECT 'legacy-retirement',b.org_id,b.project_id,b.id,'legacy-source',w.id,'legacy-request','{"older_than_days":7,"keep_last":1}',b.object,w.object,'running',2,'legacy-worker',now()+interval '50 minutes','u'
 FROM loomplane_service_backups b JOIN loomplane_service_backups w ON w.id='w' WHERE b.id='b'`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE loomplane_service_backups SET status='retiring' WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	if err := ops.Migrate(t.Context(), conn.Config().ConnString(), migrations.All()); err != nil {
		t.Fatal(err)
	}
	r, err := store.GetRetirement(t.Context(), "o", "legacy-retirement")
	if err != nil || r.ExecutionBy != "u" || r.CreatedBy != "u" || r.Attempt != 2 || r.AttemptLimit != 3 || r.Status != "running" || r.Token != "legacy-worker" {
		t.Fatalf("migration changed active execution: %+v %v", r, err)
	}
	history, err := store.RetirementHistory(t.Context(), "o", r.ID, recovery.RetirementHistoryInput{Limit: 10})
	if err != nil || len(history.Events) != 1 || history.Events[0].Kind != "imported" || history.Events[0].Attempt != 2 || history.Events[0].Status != "running" {
		t.Fatalf("migration invented earlier history: %+v %v", history, err)
	}
	if err = store.FinishRetirement(t.Context(), r, nil); err != nil {
		t.Fatalf("migrated live worker could not finish: %v", err)
	}
	pg := pgdriver.New()
	if err = pg.Open(t.Context(), conn.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err = historyMigration.Down(t.Context(), pgmigrate.New(pg)); err == nil {
		t.Fatal("downgrade erased durable execution history")
	}
	if _, err = store.RetirementHistory(t.Context(), "o", r.ID, recovery.RetirementHistoryInput{Limit: 10}); err != nil {
		t.Fatalf("refused downgrade damaged history: %v", err)
	}
}

func TestConcurrentRetirementRetryKeyCannotCrossSources(t *testing.T) {
	conn, store := retirementFixture(t)
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_service_backups(id,org_id,project_id,environment_id,connection_id,name,status,request_id,source,object,token,created_by,created_at,completed_at,restored_at)
 SELECT id||'2',org_id,project_id,environment_id,connection_id,name,status,request_id||'2',
 jsonb_set(source,'{external_id}','"second-instance"'),jsonb_set(object,'{key}',to_jsonb('o/p/e/'||id||'2/attempt.dump')),token,created_by,created_at,completed_at,restored_at FROM loomplane_service_backups`); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 2)
	for _, id := range []string{"b", "b2"} {
		request := retirementRequest()
		request.ID, request.BackupID, request.RequestID = "retire-"+id, id, "retirement-request-"+id
		r, err := store.BeginRetirement(t.Context(), request, func(context.Context) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		claim, err := store.ClaimRetirement(t.Context(), "o", r.ID, "worker-"+id)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.FinishRetirement(t.Context(), claim, errors.New("failed")); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	// Both sources reach their retry after the request lookup, then race insertion.
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(t.Context(), `LOCK TABLE loomplane_backup_retirement_events IN SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, id := range ids {
		go func() {
			_, retryErr := store.RetryRetirement(t.Context(), "o", id, "replacement", "shared-retry-key", func(context.Context) error { return nil })
			results <- retryErr
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err = tx.Exec(t.Context(), `SELECT pg_stat_clear_snapshot()`); err != nil {
			t.Fatal(err)
		}
		var blocked int
		if err = tx.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name='loomplane-retirement-admission' AND wait_event_type='Lock'`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("both retries did not reach the event insertion")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	wins := 0
	for range 2 {
		select {
		case err = <-results:
			if err == nil {
				wins++
			} else if !errors.Is(err, recovery.ErrConflict) {
				t.Fatalf("retry key race leaked database error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cross-source retries deadlocked")
		}
	}
	if wins != 1 {
		t.Fatalf("one retry request granted %d authorities", wins)
	}
}
