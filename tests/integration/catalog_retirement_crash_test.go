//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/trove"
	"github.com/xraph/trove/driver"
	"github.com/xraph/trove/drivers/localdriver"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/recovery"
)

func catalogRetirementCrash(t *testing.T, c *client, current **running, db *pgx.Conn, cfg config.Config, project, witness, socket string) {
	t.Helper()
	for _, abortCompletion := range []bool{false, true} {
		catalogRetirementCrashCase(t, c, current, db, cfg, project, witness, socket, abortCompletion)
	}
}

func catalogRetirementCrashCase(t *testing.T, c *client, current **running, db *pgx.Conn, cfg config.Config, project, witness, socket string, abortCompletion bool) {
	t.Helper()
	mode := "process-loss"
	if abortCompletion {
		mode = "connection-loss"
	}
	application := "catalog-crash-" + mode
	candidate := catalogCreateNativeBackup(t, c, project, "crash-native-"+mode)
	if _, err := db.Exec(t.Context(), `UPDATE loomplane_service_backups SET created_at=now()-interval '41 days 1 minute',completed_at=now()-interval '41 days' WHERE id=$1`, candidate.ID); err != nil {
		t.Fatal(err)
	}
	object := catalogBackupObject(t, db, candidate.ID)
	retained := catalogBackupObject(t, db, witness)

	// This database belongs only to this fixture. Hold successful completion
	// after the physical deletion, without a production failure hook.
	for _, statement := range []string{
		`SELECT pg_advisory_lock(727444,1)`,
		`CREATE FUNCTION catalog_hold_retirement_finish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='succeeded' THEN PERFORM pg_advisory_xact_lock(727444,1); END IF; RETURN NEW; END $$`,
		`CREATE TRIGGER catalog_retirement_finish BEFORE UPDATE ON loomplane_backup_retirements FOR EACH ROW EXECUTE FUNCTION catalog_hold_retirement_finish()`,
	} {
		if _, err := db.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := db.Exec(ctx, `SELECT pg_advisory_unlock(727444,1)`); err != nil {
			t.Error(err)
		}
		if _, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS catalog_retirement_finish ON loomplane_backup_retirements; DROP FUNCTION IF EXISTS catalog_hold_retirement_finish()`); err != nil {
			t.Error(err)
		}
	}()
	(*current).stop()
	*current = nil
	files := catalogLocalBackupFiles(t, cfg)
	first := startCatalogProcess(t, cfg, application)
	c.base = first.url
	policyPath := "/api/v1/service-backups/" + witness + "/retention-policy"
	var state recovery.RetentionPolicyState
	c.must(http.StatusOK, http.MethodGet, policyPath, nil, &state)
	if !state.Configured || state.Policy == nil || state.Policy.Enabled {
		t.Fatalf("crash drill requires a saved paused policy: %+v", state)
	}
	var policy recovery.RetentionPolicy
	rule := recovery.RetentionInput{OlderThanDays: 7, KeepLast: 2}
	c.must(http.StatusOK, http.MethodPut, policyPath, recovery.RetentionPolicyInput{Revision: state.Policy.Revision, Enabled: true, Policy: rule}, &policy)
	var backendPID int
	var backendStart time.Time
	catalogWaitForCrash(t, "worker blocked after native file deletion", func(ctx context.Context) bool {
		err := db.QueryRow(ctx, `SELECT pid,backend_start FROM pg_stat_activity WHERE datname=current_database() AND application_name=$1 AND wait_event='advisory' AND query LIKE '%WITH locked AS%'`, application).Scan(&backendPID, &backendStart)
		if errors.Is(err, pgx.ErrNoRows) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		return true
	})
	if _, err := files.Trove.Head(t.Context(), files.Bucket, object.Key); !errors.Is(err, driver.ErrNotFound) {
		t.Fatalf("worker did not reach physical native deletion before crash: %v", err)
	}
	if err := files.CopyVerified(t.Context(), retained, io.Discard); err != nil {
		t.Fatalf("crash boundary lost retained native bytes: %v", err)
	}
	var operation recovery.Retirement
	var page recovery.RetirementPage
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project+"/service-backup-retirements", nil, &page)
	for _, item := range page.Retirements {
		if item.BackupID == candidate.ID {
			operation = item
		}
	}
	if operation.ID == "" || operation.Status != "running" || operation.Attempt != 1 || operation.AttemptLimit != 3 || operation.RetentionPolicyID != policy.ID || operation.RetentionRevision != policy.Revision || operation.WitnessID != witness {
		t.Fatalf("uncommitted crash boundary: %+v", operation)
	}
	catalogWaitForCrash(t, "saved policy references accepted deletion", func(ctx context.Context) bool {
		bounded := *c
		bounded.ctx = ctx
		bounded.must(http.StatusOK, http.MethodGet, policyPath, nil, &state)
		return state.Policy != nil && state.Policy.LastRetirementID == operation.ID
	})
	first.stop(t, true)
	if abortCompletion {
		// The database may finish a submitted statement after the client dies.
		// This distinct fault case aborts only that captured completion session.
		var terminated bool
		err := db.QueryRow(t.Context(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid=$1 AND backend_start=$2 AND datname=current_database() AND application_name=$3`, backendPID, backendStart, application).Scan(&terminated)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if err == nil && !terminated {
			t.Fatal("could not abort owned completion connection")
		}
		catalogWaitForCrash(t, "aborted completion backend disconnected", func(ctx context.Context) bool {
			var count int
			if checkErr := db.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE pid=$1 AND backend_start=$2 AND datname=current_database() AND application_name=$3`, backendPID, backendStart, application).Scan(&count); checkErr != nil {
				t.Fatal(checkErr)
			}
			return count == 0
		})
		var status string
		var attempt, successes int
		if err = db.QueryRow(t.Context(), `SELECT status,attempt,(SELECT count(*) FROM loomplane_backup_retirement_events WHERE retirement_id=$1 AND kind='succeeded') FROM loomplane_backup_retirements WHERE id=$1`, operation.ID).Scan(&status, &attempt, &successes); err != nil || status != "running" || attempt != 1 || successes != 0 {
			t.Fatalf("completion was not rolled back before releasing gate: %s attempt%d successes%d %v", status, attempt, successes, err)
		}
	}
	if _, err := db.Exec(t.Context(), `SELECT pg_advisory_unlock(727444,1)`); err != nil {
		t.Fatal(err)
	}
	catalogWaitForCrash(t, "killed process completion connection drained", func(ctx context.Context) bool {
		var count int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE pid=$1 AND backend_start=$2 AND datname=current_database() AND application_name=$3`, backendPID, backendStart, application).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count == 0
	})
	if _, err := db.Exec(t.Context(), `DROP TRIGGER catalog_retirement_finish ON loomplane_backup_retirements; DROP FUNCTION catalog_hold_retirement_finish()`); err != nil {
		t.Fatal(err)
	}

	second := startCatalogProcess(t, cfg, application+"-recovered")
	c.base = second.url
	var observed recovery.Retirement
	c.must(http.StatusOK, http.MethodGet, "/api/v1/service-backup-retirements/"+operation.ID, nil, &observed)
	if observed.Attempt != 1 || observed.Status != "running" && observed.Status != "succeeded" || abortCompletion && (observed.Status != "running" || observed.CompletedAt != nil) {
		t.Fatalf("process loss falsely completed or replayed the live claim: %+v", observed)
	}
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/service-backups/"+candidate.ID+"/restore", map[string]string{"request_id": "crashed-candidate-restore"}, nil)
	// Pause fences new selections but retains this already accepted operation.
	var paused recovery.RetentionPolicy
	c.must(http.StatusOK, http.MethodPut, policyPath, recovery.RetentionPolicyInput{Revision: policy.Revision, Enabled: false, Policy: rule}, &paused)
	if paused.Enabled || paused.Policy != rule || paused.LastRetirementID != operation.ID {
		t.Fatalf("pause lost accepted crash recovery: %+v", paused)
	}
	expectedAttempt := 1
	if observed.Status == "running" {
		expectedAttempt = 2
		if _, err := db.Exec(t.Context(), `UPDATE loomplane_backup_retirements SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1 AND status='running' AND attempt=1`, operation.ID); err != nil {
			t.Fatal(err)
		}
		catalogWaitForCrash(t, "restarted cron recovers expired accepted deletion", func(ctx context.Context) bool {
			bounded := *c
			bounded.ctx = ctx
			bounded.must(http.StatusOK, http.MethodGet, "/api/v1/service-backup-retirements/"+operation.ID, nil, &observed)
			return observed.Status == "succeeded"
		})
	}
	if observed.Attempt != expectedAttempt || observed.AttemptLimit != 3 || observed.RetentionPolicyID != policy.ID || observed.RetentionRevision != policy.Revision || observed.CreatedBy != operation.CreatedBy || observed.ExecutionBy != operation.ExecutionBy || observed.Policy != rule || observed.WitnessID != witness {
		t.Fatalf("crash recovery changed the grant or attempt budget: %+v", observed)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_backup_retirements WHERE retention_policy_id=$1 AND retention_revision=$2`, policy.ID, policy.Revision).Scan(&count); err != nil || count != 1 {
		t.Fatalf("process recovery created another intent: %d %v", count, err)
	}
	var history recovery.RetirementHistory
	c.must(http.StatusOK, http.MethodGet, "/api/v1/service-backup-retirements/"+operation.ID+"/history?limit=10", nil, &history)
	expectedHistory := []string{"succeeded", "claimed", "requested"}
	if expectedAttempt == 2 {
		expectedHistory = []string{"succeeded", "claimed", "expired", "claimed", "requested"}
	}
	if len(history.Events) != len(expectedHistory) || history.Events[0].Attempt != expectedAttempt {
		t.Fatalf("crash history lost or duplicated a transition: %+v", history)
	}
	for i, kind := range expectedHistory {
		if history.Events[i].Kind != kind || history.Events[i].Actor != operation.ExecutionBy {
			t.Fatalf("crash history lost transition or authority: %+v", history)
		}
	}
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/service-backups/"+candidate.ID+"/restore", map[string]string{"request_id": "crash-retired-candidate-restore"}, nil)
	catalogVerifyRetainedRestore(t, c, db, witness, socket, "post-"+mode+"-restore")
	second.stop(t, false)
	*current = start(t, cfg)
	c.base = (*current).srv.URL
	t.Logf("%s after Trove native deletion: retirement %s completed on attempt%d with original policy revision%d; restart preserved pause, retained SQL and exact copy cleanup", mode, operation.ID, expectedAttempt, policy.Revision)
}

func catalogWaitForCrash(t *testing.T, description string, ready func(context.Context) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if err := awaitCatalogProcess(ctx, ready); err != nil {
		t.Fatalf("%s: %v", description, err)
	}
}

func catalogBackupObject(t *testing.T, db *pgx.Conn, id string) backupfiles.Object {
	t.Helper()
	var raw []byte
	if err := db.QueryRow(t.Context(), `SELECT object FROM loomplane_service_backups WHERE id=$1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var object backupfiles.Object
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func catalogLocalBackupFiles(t *testing.T, cfg config.Config) *backupfiles.Store {
	t.Helper()
	url := cfg.Storage.BackupURL
	if url == "" {
		url = cfg.Storage.URL
	}
	local := localdriver.New()
	if err := local.Open(t.Context(), url); err != nil {
		t.Fatal(err)
	}
	files, err := trove.Open(local)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.Close(context.Background()) })
	bucket := cfg.Storage.BackupBucket
	if bucket == "" {
		bucket = "loomplane-backups"
	}
	store, err := backupfiles.New(files, bucket, url)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
