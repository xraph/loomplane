//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"
	"github.com/xraph/trove/driver"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/recovery"
	api "github.com/xraph/loomplane/pkg/client"
)

func catalogCreateNativeBackup(t *testing.T, c *client, project, request string) recovery.Backup {
	t.Helper()
	var backup recovery.Backup
	c.must(http.StatusAccepted, http.MethodPost, "/api/v1/projects/"+project+"/service-backups", map[string]string{"environment": "production", "resource": "database", "request_id": request}, &backup)
	deadline := time.Now().Add(time.Minute)
	for backup.Status == "queued" || backup.Status == "running" {
		if time.Now().After(deadline) {
			t.Fatalf("native backup did not settle: %+v", backup)
		}
		time.Sleep(100 * time.Millisecond)
		c.must(http.StatusOK, http.MethodGet, "/api/v1/service-backups/"+backup.ID, nil, &backup)
	}
	if backup.Status != "succeeded" || backup.Bytes == 0 {
		t.Fatalf("native backup failed: %+v", backup)
	}
	return backup
}

func catalogAutomaticRetention(t *testing.T, c *client, db *pgx.Conn, project, witness, socket string, restart func() forge.App) {
	t.Helper()
	candidates := make([]recovery.Backup, 0, 3)
	objects := make([]backupfiles.Object, 0, 3)
	for n := range 3 {
		backup := catalogCreateNativeBackup(t, c, project, fmt.Sprintf("automatic-native-backup-%d", n))
		// Keep the actual native bytes. Only age metadata is changed for this drill.
		if _, err := db.Exec(t.Context(), `UPDATE loomplane_service_backups SET created_at=now()-$2*interval '1 day'-interval '1 minute',completed_at=now()-$2*interval '1 day' WHERE id=$1`, backup.ID, 41-n); err != nil {
			t.Fatal(err)
		}
		var raw []byte
		if err := db.QueryRow(t.Context(), `SELECT object FROM loomplane_service_backups WHERE id=$1`, backup.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var object backupfiles.Object
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, backup)
		objects = append(objects, object)
	}
	path := "/api/v1/service-backups/" + witness + "/retention-policy"
	var policy recovery.RetentionPolicy
	c.must(http.StatusOK, http.MethodPut, path, recovery.RetentionPolicyInput{Policy: recovery.RetentionInput{OlderThanDays: 7, KeepLast: 1}}, &policy)
	if policy.Enabled || policy.Revision != 1 {
		t.Fatalf("policy implicitly enabled: %+v", policy)
	}
	app := restart()
	var state recovery.RetentionPolicyState
	c.must(http.StatusOK, http.MethodGet, path, nil, &state)
	if !state.Configured || state.Policy == nil || state.Policy.ID != policy.ID || state.Policy.Revision != 1 || state.Policy.Enabled {
		t.Fatalf("restart lost paused policy: %+v", state)
	}
	files, err := forge.InjectType[*backupfiles.Store](app.Container())
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		if _, err = files.Trove.Head(t.Context(), files.Bucket, object.Key); err != nil {
			t.Fatalf("paused policy deleted native bytes: %v", err)
		}
	}
	run := catalogRecoveryCLI(t, c)
	var enabled api.RetentionPolicy
	if err = json.Unmarshal(run("backups", "retention-set", witness, "--revision", "1", "--older-than-days", "7", "--keep-last", "1", "--yes", "--json"), &enabled); err != nil || !enabled.Enabled || enabled.Revision != 2 || enabled.ID != policy.ID {
		t.Fatalf("native CLI enable: %+v %v", enabled, err)
	}
	for n := range 2 {
		deadline := time.Now().Add(3 * time.Minute)
		for {
			var current recovery.Backup
			c.must(http.StatusOK, http.MethodGet, "/api/v1/service-backups/"+candidates[n].ID, nil, &current)
			if current.Status == "retired" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("persisted minute cron did not retire native point%d: %+v", n, current)
			}
			time.Sleep(200 * time.Millisecond)
		}
		if _, err = files.Trove.Head(t.Context(), files.Bucket, objects[n].Key); !errors.Is(err, driver.ErrNotFound) {
			t.Fatalf("automatic native file remains: %v", err)
		}
		for remaining := n + 1; remaining < len(objects); remaining++ {
			if _, err = files.Trove.Head(t.Context(), files.Bucket, objects[remaining].Key); err != nil {
				t.Fatalf("evaluation selected more than the oldest native point: %v", err)
			}
		}
	}
	var list recovery.RetirementPage
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project+"/service-backup-retirements", nil, &list)
	selected := make([]recovery.Retirement, 0, 2)
	for _, operation := range list.Retirements {
		if operation.RetentionPolicyID == policy.ID {
			selected = append(selected, operation)
		}
	}
	if len(selected) != 2 || selected[0].BackupID != candidates[1].ID || selected[1].BackupID != candidates[0].ID {
		t.Fatalf("native policy ordering: %+v", selected)
	}
	for _, operation := range selected {
		if operation.Status != "succeeded" || operation.RetentionRevision != 2 || operation.WitnessID != witness || operation.Attempt != 1 {
			t.Fatalf("native policy provenance: %+v", operation)
		}
	}
	progressDeadline := time.Now().Add(10 * time.Second)
	for {
		c.must(http.StatusOK, http.MethodGet, path, nil, &state)
		if state.Policy != nil && state.Policy.Revision == 2 && state.Policy.LastRetirementID == selected[0].ID {
			break
		}
		if time.Now().After(progressDeadline) {
			t.Fatalf("scheduler did not persist the second deletion before pause: %+v", state)
		}
		time.Sleep(50 * time.Millisecond)
	}
	var paused api.RetentionPolicy
	if err = json.Unmarshal(run("backups", "retention-pause", witness, "--yes", "--json"), &paused); err != nil || paused.Enabled || paused.Revision != 3 || paused.LastRetirementID == "" {
		t.Fatalf("native CLI pause: %+v %v", paused, err)
	}
	var pauseCompleted time.Time
	if err = db.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&pauseCompleted); err != nil {
		t.Fatal(err)
	}
	if paused.LastRetirementID != selected[0].ID {
		t.Fatalf("policy did not link last automatic deletion: %+v", paused)
	}
	catalogVerifyRetainedRestore(t, c, db, witness, socket, "post-automatic-retention-restore")
	// Observe a completed real cron pass that started after pause, not just a
	// silent waiting period or an in-flight evaluation from before the pause.
	deadline := time.Now().Add(90 * time.Second)
	for {
		var afterPause bool
		if err = db.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM dispatch_jobs WHERE name='loomplane.service.backup.reconcile' AND state='completed' AND started_at>$1)`, pauseCompleted).Scan(&afterPause); err != nil {
			t.Fatal(err)
		}
		if afterPause {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no completed minute reconciliation observed after pause")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err = files.Trove.Head(t.Context(), files.Bucket, objects[2].Key); err != nil {
		t.Fatalf("paused policy removed remaining native point: %v", err)
	}
	var remaining recovery.Backup
	c.must(http.StatusOK, http.MethodGet, "/api/v1/service-backups/"+candidates[2].ID, nil, &remaining)
	if remaining.Status != "succeeded" {
		t.Fatalf("paused policy admitted another intent: %+v", remaining)
	}
	t.Logf("policy %s survived restart, retired two native dumps through real minute cron, paused before the third and preserved a SQL-restorable witness", policy.ID)
}
