//go:build integration

package integration

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/job"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/platform"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

type nativeDispatchApplication struct {
	forge.App
	cancel context.CancelFunc
}

func (a nativeDispatchApplication) Stop(ctx context.Context) error {
	// Match signal-driven shutdown: cancel the app's long-lived start context
	// before waiting for extensions and their listeners to release connections.
	a.cancel()
	return a.App.Stop(ctx)
}

func startNativeDispatch(t *testing.T, cfg config.Config) *running {
	t.Helper()
	app, err := platform.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err = app.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	return &running{t: t, srv: httptest.NewServer(app.Router().Handler()), app: nativeDispatchApplication{App: app, cancel: cancel}}
}

func TestNativeDispatchReconcilesRetainedIntentsAfterRestart(t *testing.T) {
	f, records, request := nativeRestoreFixture(t)
	r, err := records.CreateNativeRestore(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	expiredRequest := *request
	expiredRequest.ID, expiredRequest.RequestID = "expired-copy", "expired-request"
	expiredRequest.CreatedAt = time.Now().Add(-2 * time.Hour)
	expiredRequest.ExpiresAt = expiredRequest.CreatedAt.Add(time.Hour)
	expired, err := records.CreateNativeRestore(t.Context(), &expiredRequest)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Database.URL = f.sql.Config().ConnString()
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Orchestration.PollInterval = 50 * time.Millisecond
	running := startNativeDispatch(t, cfg)
	defer func() { running.stop() }()

	reconcile := func() {
		t.Helper()
		app, ok := running.app.(forge.App)
		if !ok {
			t.Fatal("running application has no container")
		}
		ext, lookupErr := app.GetExtension("dispatch")
		if lookupErr != nil {
			t.Fatal(lookupErr)
		}
		owner, ok := ext.(interface{ Engine() *engine.Engine })
		if !ok || owner.Engine() == nil {
			t.Fatal("registered Dispatch extension has no engine")
		}
		if _, enqueueErr := engine.Enqueue(t.Context(), owner.Engine(), "loomplane.service.backup.reconcile", orchestration.DriftPayload{}, job.WithQueue(orchestration.Queue), job.WithMaxRetries(0)); enqueueErr != nil {
			t.Fatal(enqueueErr)
		}
	}
	wait := func(id, status string, budget time.Duration) *recovery.NativeRestore {
		t.Helper()
		deadline := time.Now().Add(budget)
		for {
			current, readErr := records.GetNativeRestore(t.Context(), r.OrgID, id)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if current.Status == status {
				return current
			}
			if time.Now().After(deadline) {
				t.Fatalf("production reconciliation did not reach %s for %s: status=%s error=%s", status, id, current.Status, current.Error)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	// No Dispatch job was submitted with this durable record. The production
	// reconciler must discover it, then the real worker must reject the fixture's
	// nonexistent principal before any provider operation can run.
	reconcile()
	failed := wait(r.ID, "failed", 15*time.Second)
	if failed.Error == "" || failed.VerifiedAt != nil || failed.ExternalID != "" {
		t.Fatal("denied worker lost its failure or claimed a recovery proof", failed)
	}
	retired := wait(expired.ID, "cleanup_failed", 15*time.Second)
	if retired.CleanupBy != "" || retired.VerifiedAt != nil || retired.Error == "" {
		t.Fatal("expiry invented a manual actor or recovery proof", retired.Status)
	}
	cleanup := nativeCleanupStore(t, f)
	running.stop()
	if _, err = cleanup.RequestNativeCleanup(t.Context(), r.OrgID, r.ID, "replacement-admin", func(_ context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	queued, err := records.GetNativeRestore(t.Context(), r.OrgID, r.ID)
	if err != nil || queued.Status != "cleanup_queued" {
		t.Fatal("offline cleanup intent was not retained", err)
	}
	var beforeRestart int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM dispatch_jobs WHERE name='loomplane.service.native.cleanup' AND convert_from(payload,'UTF8')::jsonb->>'org_id'=$1 AND convert_from(payload,'UTF8')::jsonb->>'id'=$2`, r.OrgID, r.ID).Scan(&beforeRestart); err != nil || beforeRestart != 0 {
		t.Fatalf("cleanup dispatched before restart: count=%d error=%v", beforeRestart, err)
	}
	running = startNativeDispatch(t, cfg)
	// Submit no job after restart. The registered minute cron must find the
	// retained manual cleanup intent and enqueue the actual cleanup worker.
	failed = wait(r.ID, "cleanup_failed", 90*time.Second)
	if failed.CleanupBy != "replacement-admin" || failed.CleanupRequestedAt == nil || failed.Error == "" {
		t.Fatal("restarted cleanup lost its retained actor or failure", failed)
	}
	// Both jobs are durable attempts with no automatic mutation replay. Native
	// reconciliation, checkpoints and leases govern subsequent recovery.
	for _, name := range []string{"loomplane.service.native.restore", "loomplane.service.native.cleanup"} {
		deadline := time.Now().Add(10 * time.Second)
		for {
			var count, denied int
			if err = f.sql.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE position($4 in last_error)>0) FROM dispatch_jobs WHERE name=$1 AND convert_from(payload,'UTF8')::jsonb->>'org_id'=$2 AND convert_from(payload,'UTF8')::jsonb->>'id'=$3 AND max_retries=0`, name, r.OrgID, r.ID, security.ErrForbidden.Error()+" (service_restore:manage)").Scan(&count, &denied); err != nil {
				t.Fatal(err)
			}
			if count == 1 && denied == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("native job lost its scoped, non-replaying authorization denial: %s count=%d denied=%d", name, count, denied)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
}
