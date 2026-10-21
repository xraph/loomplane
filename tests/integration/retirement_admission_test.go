//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recovery"
)

func retirementFixture(t *testing.T) (*pgx.Conn, *recovery.PgStore) {
	t.Helper()
	ctx := t.Context()
	dsn := freshDatabase(t)
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	for _, sql := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p','o','p','p','ready','u',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('e','o','p','prod','production',now(),now())`,
		`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('c','o','server','c','daemon','ready','u',now(),now())`,
		`INSERT INTO loomplane_service_backups(id,org_id,project_id,environment_id,connection_id,name,status,request_id,source,created_by,created_at) VALUES('b','o','p','e','c','db','succeeded','backup','{}','u',now())`,
	} {
		if _, err = conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	recipe, err := recipes.Lookup("postgres", "17", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(planning.ResourceConfig{Provider: "server", Kind: "postgres", Target: "c", Catalog: recipe})
	if err != nil {
		t.Fatal(err)
	}
	source, err := json.Marshal(planning.Binding{ID: "binding", OrgID: "o", ProjectID: "p", EnvironmentID: "e", Name: "db", Subject: planning.ResourceSubject, ExternalID: "instance", Provider: "server", ConnectionID: "c", Config: config})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `UPDATE loomplane_service_backups SET source=$1,token='attempt',created_at=now()-interval '40 days 1 minute',completed_at=now()-interval '40 days',
 object=jsonb_build_object('key','o/p/e/b/attempt.dump','size',3,'sha256',repeat('a',64),'destination_id',repeat('d',64)) WHERE id='b'`, source); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `INSERT INTO loomplane_service_backups(id,org_id,project_id,environment_id,connection_id,name,status,request_id,source,object,token,created_by,created_at,completed_at,restored_at)
 SELECT 'w',org_id,project_id,environment_id,connection_id,name,'succeeded','witness',source,
 jsonb_build_object('key','o/p/e/w/attempt.dump','size',3,'sha256',repeat('a',64),'destination_id',repeat('d',64)),token,created_by,now()-interval '1 day 1 minute',now()-interval '1 day',now() FROM loomplane_service_backups WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	settings, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := settings.Query()
	query.Set("application_name", "loomplane-retirement-admission")
	settings.RawQuery = query.Encode()
	if err = pg.Open(ctx, settings.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return conn, recovery.NewPgStore(pg)
}

func retirementRequest() *recovery.Retirement {
	return &recovery.Retirement{ID: "retire-1", OrgID: "o", ProjectID: "p", BackupID: "b", RequestID: "retirement-request", CreatedBy: "u", Policy: recovery.RetentionInput{OlderThanDays: 7, KeepLast: 1}}
}

func TestRetirementIntentAndRestoreAdmissionExcludeEachOther(t *testing.T) {
	_, store := retirementFixture(t)
	r, err := store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error { return nil })
	if err != nil || r.Status != "queued" || r.WitnessID != "w" || r.Object.DestinationID == "" {
		t.Fatalf("retirement did not pin candidate and witness: %+v %v", r, err)
	}
	if _, err = store.CreateRestore(t.Context(), restoreAdmission("retiring-restore")); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("restore admitted after retirement intent: %v", err)
	}
	current, err := store.Get(t.Context(), "o", "b")
	if err != nil || current.Status != "retiring" {
		t.Fatalf("retirement barrier was not persisted: %+v %v", current, err)
	}
	if _, err = store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("same retirement request was not idempotent: %v", err)
	}
}

func TestRetirementCannotClaimPointWithRemainingRestore(t *testing.T) {
	_, store := retirementFixture(t)
	if _, err := store.CreateRestore(t.Context(), restoreAdmission("active-restore")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error { return nil }); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("retirement admitted an in-use backup: %v", err)
	}
	current, err := store.Get(t.Context(), "o", "b")
	if err != nil || current.Status != "succeeded" {
		t.Fatalf("rejected retirement changed backup: %+v %v", current, err)
	}
}

func TestRetirementLeaseReclaimFencesLateWorkers(t *testing.T) {
	conn, store := retirementFixture(t)
	r, err := store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimRetirement(t.Context(), "o", r.ID, "first-worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimRetirement(t.Context(), "o", r.ID, "duplicate-worker"); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("live lease was stolen: %v", err)
	}
	if _, err = conn.Exec(t.Context(), `UPDATE loomplane_backup_retirements SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	reopened := pgdriver.New()
	if err = reopened.Open(t.Context(), conn.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	resumed := recovery.NewPgStore(reopened)
	second, err := resumed.ClaimRetirement(t.Context(), "o", r.ID, "replacement-worker")
	if err != nil || second.Attempt != 2 || second.Object != r.Object || second.WitnessObject != r.WitnessObject {
		t.Fatalf("restart lost pinned retirement data: %+v %v", second, err)
	}
	if err = store.FinishRetirement(t.Context(), first, nil); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("late worker published over its replacement: %v", err)
	}
	if err = resumed.FinishRetirement(t.Context(), second, nil); err != nil {
		t.Fatal(err)
	}
	current, err := resumed.Get(t.Context(), "o", "b")
	if err != nil || current.Status != "retired" || current.Object != r.Object {
		t.Fatalf("retirement lost its evidence: %+v %v", current, err)
	}
}

func TestRetirementDeniedAtCommitRollsBackItsBarrier(t *testing.T) {
	conn, store := retirementFixture(t)
	denied := errors.New("permission revoked")
	calls := 0
	_, err := store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error {
		calls++
		if calls == 2 {
			return denied
		}
		return nil
	})
	if !errors.Is(err, denied) || calls != 2 {
		t.Fatalf("retirement skipped the final permission check: calls=%d err=%v", calls, err)
	}
	var count int
	if err = conn.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_backup_retirements`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("denied retirement left an intent: %d %v", count, err)
	}
	current, err := store.Get(t.Context(), "o", "b")
	if err != nil || current.Status != "succeeded" {
		t.Fatalf("denied retirement changed backup state: %+v %v", current, err)
	}
}

func TestRetirementFailureKeepsRestoreAndSourceBarriers(t *testing.T) {
	conn, store := retirementFixture(t)
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_service_backups(id,org_id,project_id,environment_id,connection_id,name,status,request_id,source,object,token,created_by,created_at,completed_at)
 SELECT 'older',org_id,project_id,environment_id,connection_id,name,'succeeded','older',source,
 jsonb_set(object,'{key}','"o/p/e/older/attempt.dump"'),token,created_by,created_at-interval '1 day',completed_at-interval '1 day' FROM loomplane_service_backups WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	r, err := store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimRetirement(t.Context(), "o", r.ID, "failing-worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishRetirement(t.Context(), claimed, errors.New("delete response was lost")); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateRestore(t.Context(), restoreAdmission("after-failed-delete")); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("ambiguous deletion released restore admission: %v", err)
	}
	if _, err = store.ClaimRetirement(t.Context(), "o", r.ID, "too-soon"); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("failed retirement ignored retry backoff: %v", err)
	}
	other := retirementRequest()
	other.ID, other.BackupID, other.RequestID = "retire-other", "older", "other-request"
	if _, err = store.BeginRetirement(t.Context(), other, func(context.Context) error { return nil }); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("another retirement bypassed the retained witness hold: %v", err)
	}
}

func TestRetirementFinishRequiresItsLiveBackupBarrier(t *testing.T) {
	conn, store := retirementFixture(t)
	r, err := store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimRetirement(t.Context(), "o", r.ID, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(t.Context(), `UPDATE loomplane_service_backups SET status='succeeded' WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	if err = store.FinishRetirement(t.Context(), claimed, nil); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("retirement published without its backup barrier: %v", err)
	}
	var status string
	if err = conn.QueryRow(t.Context(), `SELECT status FROM loomplane_backup_retirements WHERE id=$1`, r.ID).Scan(&status); err != nil || status != "running" {
		t.Fatalf("conflicting finish changed retirement state: %q %v", status, err)
	}
}

func TestRetirementFinalWorkerLossIsVisibleAndKeepsAdmissionBlocked(t *testing.T) {
	conn, store := retirementFixture(t)
	r, err := store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimRetirement(t.Context(), "o", r.ID, "lost-worker"); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(t.Context(), `UPDATE loomplane_backup_retirements SET attempt=3,lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingRetirements(t.Context())
	if err != nil || len(pending) != 0 {
		t.Fatalf("exhausted retirement was re-enqueued: %+v %v", pending, err)
	}
	current, err := store.GetRetirement(t.Context(), "o", r.ID)
	if err != nil || current.Status != "failed" || current.Error == "" {
		t.Fatalf("lost worker did not become a visible failure: %+v %v", current, err)
	}
	if _, err = store.CreateRestore(t.Context(), restoreAdmission("lost-worker-restore")); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("lost worker released the retirement barrier: %v", err)
	}
}

func TestConcurrentRetirementAndRestoreHaveExactlyOneWinner(t *testing.T) {
	conn, store := retirementFixture(t)
	for i := range 12 {
		if _, err := conn.Exec(t.Context(), `DELETE FROM loomplane_backup_retirements; DELETE FROM loomplane_service_restores; UPDATE loomplane_service_backups SET status='succeeded' WHERE id='b'`); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			_, err := store.BeginRetirement(t.Context(), retirementRequest(), func(context.Context) error { return nil })
			results <- err
		}()
		go func() {
			<-start
			_, err := store.CreateRestore(t.Context(), restoreAdmission(fmt.Sprintf("parallel-%d", i)))
			results <- err
		}()
		close(start)
		wins := 0
		for range 2 {
			select {
			case err := <-results:
				if err == nil {
					wins++
				} else if !errors.Is(err, recovery.ErrConflict) {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("restore and retirement deadlocked")
			}
		}
		if wins != 1 {
			t.Fatalf("iteration %d admitted %d operations", i, wins)
		}
	}
}

func TestConcurrentRetirementRequestCannotCrossPhysicalSources(t *testing.T) {
	conn, store := retirementFixture(t)
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_service_backups(id,org_id,project_id,environment_id,connection_id,name,status,request_id,source,object,token,created_by,created_at,completed_at,restored_at)
 SELECT id||'2',org_id,project_id,environment_id,connection_id,name,status,request_id||'2',jsonb_set(source,'{external_id}','"other-instance"'),
 jsonb_set(object,'{key}',to_jsonb('o/p/e/'||id||'2/attempt.dump')),token,created_by,created_at,completed_at,restored_at FROM loomplane_service_backups`); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(t.Context(), `SELECT id FROM loomplane_service_backups WHERE id IN ('b','b2') FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, backupID := range []string{"b", "b2"} {
		go func() {
			request := retirementRequest()
			request.ID, request.BackupID = "retire-"+backupID, backupID
			_, createErr := store.BeginRetirement(t.Context(), request, func(context.Context) error { return nil })
			results <- createErr
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
			t.Fatal("both retirements did not reach their backup locks")
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
				t.Fatalf("cross-source request leaked a database error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("retirement requests deadlocked")
		}
	}
	if wins != 1 {
		t.Fatalf("one request admitted %d retirements", wins)
	}
}

func restoreAdmission(id string) *recovery.Restore {
	now := time.Now().UTC()
	return &recovery.Restore{ID: id, OrgID: "o", ProjectID: "p", BackupID: "b", RequestID: id, CreatedBy: "u", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
}

func TestRestoreAdmissionRequiresCurrentSuccessfulBackup(t *testing.T) {
	conn, store := retirementFixture(t)
	if _, err := conn.Exec(t.Context(), `UPDATE loomplane_service_backups SET status='failed' WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRestore(t.Context(), restoreAdmission("blocked-request")); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("restore admitted for unavailable backup: %v", err)
	}
	var count int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_service_restores`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected restore left a row: %d %v", count, err)
	}
}

func TestRestoreAdmissionRechecksAfterWaitingForBackupLock(t *testing.T) {
	conn, store := retirementFixture(t)
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(t.Context(), `SELECT id FROM loomplane_service_backups WHERE id='b' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `UPDATE loomplane_service_backups SET status='failed' WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, createErr := store.CreateRestore(t.Context(), restoreAdmission("racing-request"))
		result <- createErr
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked bool
		if _, err = tx.Exec(t.Context(), `SELECT pg_stat_clear_snapshot()`); err != nil {
			t.Fatal(err)
		}
		if err = tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND application_name='loomplane-retirement-admission' AND wait_event_type='Lock')`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err = <-result:
			t.Fatalf("restore bypassed the backup lock: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("restore did not reach the database lock")
		}
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
		if !errors.Is(err, recovery.ErrConflict) {
			t.Fatalf("restore used stale status after the lock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restore admission did not finish after releasing the lock")
	}
}
