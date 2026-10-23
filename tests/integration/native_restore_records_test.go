//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/recovery"
)

type nativeRestoreRecords interface {
	CreateNativeRestore(context.Context, *recovery.NativeRestore) (*recovery.NativeRestore, error)
	GetNativeRestore(context.Context, string, string) (*recovery.NativeRestore, error)
	ClaimNativeRestore(context.Context, string, string, string) (*recovery.NativeRestore, error)
	SaveNativeCheckpoint(context.Context, *recovery.NativeRestore, json.RawMessage) error
	FinishNativeRestore(context.Context, *recovery.NativeRestore, recovery.NativeRestoreResult, time.Duration, error) error
	ClaimNativeCleanup(context.Context, string, string, string) (*recovery.NativeRestore, error)
	FinishNativeCleanup(context.Context, *recovery.NativeRestore, error) error
}

func nativeRestoreFixture(t *testing.T) (nativePointFixture, nativeRestoreRecords, *recovery.NativeRestore) {
	t.Helper()
	f := nativePointHistoryFixture(t)
	now := time.Now().UTC()
	for _, id := range []string{"account", "recovery-account", "other-target"} {
		c := &connections.Connection{ID: id, OrgID: "org", Cloud: "aws", Name: id, Account: "123456789012", Details: map[string]string{"region": "us-east-1"}, Status: connections.StatusReady, CreatedBy: "operator", CreatedAt: now, UpdatedAt: now}
		if err := connections.NewPgStore(f.db).CreateConnection(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	point := nativeIdentityPoint(t, "2026-10-22T00:00:00.000000001Z")
	if err := f.refresh(t.Context(), []recovery.NativePoint{point}); err != nil {
		t.Fatal(err)
	}
	points, err := f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil || len(points.Points) != 1 {
		t.Fatal("missing copy point fixture", err)
	}
	store, ok := any(f.store).(nativeRestoreRecords)
	if !ok {
		t.Fatal("native copies have no durable fenced record store")
	}
	r := &recovery.NativeRestore{ID: "copy-one", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", SourceID: f.source.ID, PointID: points.Points[0].ID, Point: points.Points[0], RequestID: "request-one", CreatedBy: "operator", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), Target: recovery.NativeRestoreTarget{Provider: "rds", ConnectionID: "recovery-account", AccountIdentity: connections.AccountIdentity("aws", "123456789012", map[string]string{"region": "us-east-1"}), Region: "us-east-1"}}
	return f, store, r
}

func TestNativeRestoreRecordsKeepExactAdmissionAcrossRestart(t *testing.T) {
	f, store, request := nativeRestoreFixture(t)
	r, err := store.CreateNativeRestore(t.Context(), request)
	if err != nil || r == nil || r.Status != "queued" || r.ID != "copy-one" || !r.Point.CreatedAt.Equal(request.Point.CreatedAt) || r.Source.PhysicalID != "db-original" {
		t.Fatalf("copy admission lost original native identity: %+v %v", r, err)
	}
	if _, err = f.sql.Exec(t.Context(), `DELETE FROM loomplane_bindings WHERE org_id='org' AND id='binding'`); err != nil {
		t.Fatal(err)
	}
	reopened, ok := any(recovery.NewPgStore(f.db)).(nativeRestoreRecords)
	if !ok {
		t.Fatal("reopened store lost native copy support")
	}
	got, err := reopened.GetNativeRestore(t.Context(), "org", r.ID)
	if err != nil || got == nil || got.Source.ExternalID != "database" || got.Source.PhysicalID != "db-original" || got.Point.ProviderID != request.Point.ProviderID || !got.Point.CreatedAt.Equal(request.Point.CreatedAt) || got.Target.AccountIdentity != request.Target.AccountIdentity {
		t.Fatalf("copy lost source/target review after binding removal: %+v %v", got, err)
	}
	replay := *request
	replay.ID = "copy-two"
	got, err = reopened.CreateNativeRestore(t.Context(), &replay)
	if err != nil || got.ID != r.ID {
		t.Fatal("request replay created another copy", got, err)
	}
	for _, change := range []string{"target", "account", "region", "point-time", "environment", "organization"} {
		modified := *request
		modified.ID = "copy-conflict"
		switch change {
		case "target":
			modified.Target.ConnectionID = "other-target"
		case "account":
			modified.Target.AccountIdentity = "another-account"
		case "region":
			modified.Target.Region = "us-central1"
		case "point-time":
			modified.Point.CreatedAt = modified.Point.CreatedAt.Add(time.Nanosecond)
		case "environment":
			modified.EnvironmentID = "other-environment"
		case "organization":
			modified.OrgID = "foreign"
		}
		if _, createErr := reopened.CreateNativeRestore(t.Context(), &modified); createErr == nil {
			t.Fatal("changed copy request was admitted", change)
		}
	}
	if _, err = reopened.GetNativeRestore(t.Context(), "foreign", r.ID); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("copy escaped its organization", err)
	}
}

func TestNativeRestoreCheckpointAndCompletionFenceExpiredWorkers(t *testing.T) {
	f, store, request := nativeRestoreFixture(t)
	r, err := store.CreateNativeRestore(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimNativeRestore(t.Context(), "org", r.ID, "first-worker")
	if err != nil || first.Status != "running" {
		t.Fatal("copy could not be claimed", err)
	}
	proof := json.RawMessage(`{"version":1,"stage":"restore","attempted":true,"operation":"original-operation"}`)
	if err = store.SaveNativeCheckpoint(t.Context(), first, proof); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimNativeRestore(t.Context(), "org", r.ID, "parallel-worker"); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("parallel worker obtained a live lease", err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	result := recovery.NativeRestoreResult{ExternalID: "recovery-project:europe-west1:copy-one", PhysicalID: "copy-physical", SQLVerified: true}
	if err = store.SaveNativeCheckpoint(t.Context(), first, proof); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("expired worker changed its checkpoint before takeover", err)
	}
	if err = store.FinishNativeRestore(t.Context(), first, result, time.Second, nil); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("expired worker published success before takeover", err)
	}
	second, err := store.ClaimNativeRestore(t.Context(), "org", r.ID, "second-worker")
	if err != nil || second.Token != "second-worker" || string(second.Checkpoint) == "" {
		t.Fatal("takeover lost the original operation", second, err)
	}
	var checkpoint map[string]any
	if err = json.Unmarshal(second.Checkpoint, &checkpoint); err != nil || checkpoint["operation"] != "original-operation" {
		t.Fatal("takeover changed native operation proof", checkpoint, err)
	}
	if err = store.SaveNativeCheckpoint(t.Context(), first, json.RawMessage(`{"operation":"replacement"}`)); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("stale worker replaced checkpoint", err)
	}
	if err = store.FinishNativeRestore(t.Context(), first, result, time.Second, nil); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("stale worker published success after takeover", err)
	}
	if err = store.FinishNativeRestore(t.Context(), second, recovery.NativeRestoreResult{ExternalID: result.ExternalID, PhysicalID: result.PhysicalID}, time.Second, nil); !errors.Is(err, recovery.ErrInvalid) {
		t.Fatal("native availability counted as SQL verification", err)
	}
	if err = store.FinishNativeRestore(t.Context(), second, result, time.Second, nil); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetNativeRestore(t.Context(), "org", r.ID)
	if err != nil || got.Status != "succeeded" || got.VerifiedAt == nil || got.PhysicalID != "copy-physical" {
		t.Fatal("current worker could not publish SQL verification", got, err)
	}
}

func TestNativeRestoreAdmissionSerializesReplaysAndChangedMetadata(t *testing.T) {
	f, store, request := nativeRestoreFixture(t)
	finished := make(chan *recovery.NativeRestore, 8)
	errorsOut := make(chan error, 8)
	for i := range 8 {
		copyRequest := *request
		copyRequest.ID = fmt.Sprintf("concurrent-copy-%d", i)
		go func() {
			r, err := store.CreateNativeRestore(t.Context(), &copyRequest)
			finished <- r
			errorsOut <- err
		}()
	}
	id := ""
	for range 8 {
		r, err := <-finished, <-errorsOut
		if err != nil || r == nil {
			t.Fatal("concurrent request replay failed", err)
		}
		if id != "" && id != r.ID {
			t.Fatal("concurrent replay admitted a second native copy", id, r.ID)
		}
		id = r.ID
	}
	changed := request.Point
	changed.KMSKey = "replacement-key"
	if err := f.refresh(t.Context(), []recovery.NativePoint{changed}); err != nil {
		t.Fatal(err)
	}
	freshRequest := *request
	freshRequest.ID, freshRequest.RequestID = "changed-copy", "changed-request"
	if _, err := store.CreateNativeRestore(t.Context(), &freshRequest); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("point metadata changed between native check and durable admission", err)
	}
}

func TestNativeRestoreAdmissionPreservesCancellationWhileTargetIsLocked(t *testing.T) {
	f, store, request := nativeRestoreFixture(t)
	lock, err := f.sql.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if _, err = lock.Exec(t.Context(), `SELECT id FROM loomplane_cloud_connections WHERE id='recovery-account' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, cause := store.CreateNativeRestore(ctx, request)
		finished <- cause
	}()
	observe, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	monitor, err := pgx.Connect(observe, f.sql.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close(context.Background())
	for {
		var waiting bool
		if err = monitor.QueryRow(observe, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT cloud,account,status,details FROM loomplane_cloud_connections%')`).Scan(&waiting); err != nil {
			t.Fatal("native admission never waited on its destination review", err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err = <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal("canceled destination review was reported as invalid input", err)
	}
}

func TestNativeRestoreCleanupProtectsAccountsUntilConfirmedDeletion(t *testing.T) {
	f, store, request := nativeRestoreFixture(t)
	r, err := store.CreateNativeRestore(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"account", "recovery-account"} {
		if deleteErr := connections.NewPgStore(f.db).DeleteConnection(t.Context(), "org", id); !errors.Is(deleteErr, connections.ErrInUse) {
			t.Fatal("active copy lost its cloud account", id, deleteErr)
		}
	}
	claimed, err := store.ClaimNativeRestore(t.Context(), "org", r.ID, "copy-worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishNativeRestore(t.Context(), claimed, recovery.NativeRestoreResult{}, time.Second, errors.New("SQL probe failed")); err != nil {
		t.Fatal(err)
	}
	cleanupRequests := nativeCleanupStore(t, f)
	if _, err = cleanupRequests.RequestNativeCleanup(t.Context(), "org", r.ID, "operator", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	cleanup, err := store.ClaimNativeCleanup(t.Context(), "org", r.ID, "cleanup-one")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishNativeCleanup(t.Context(), cleanup, errors.New("native deletion refused")); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetNativeRestore(t.Context(), "org", r.ID)
	if err != nil || got.Status != "cleanup_failed" || got.VerifiedAt != nil {
		t.Fatal("failed native cleanup was hidden", got, err)
	}
	if deleteErr := connections.NewPgStore(f.db).DeleteConnection(t.Context(), "org", "recovery-account"); !errors.Is(deleteErr, connections.ErrInUse) {
		t.Fatal("failed cleanup released target credentials", deleteErr)
	}
	if _, err = cleanupRequests.RequestNativeCleanup(t.Context(), "org", r.ID, "operator", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	current, err := store.ClaimNativeCleanup(t.Context(), "org", r.ID, "cleanup-two")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishNativeCleanup(t.Context(), cleanup, nil); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("old cleanup claim published deletion", err)
	}
	if err = store.FinishNativeCleanup(t.Context(), current, nil); err != nil {
		t.Fatal(err)
	}
	if deleteErr := connections.NewPgStore(f.db).DeleteConnection(t.Context(), "org", "recovery-account"); deleteErr != nil {
		t.Fatal("confirmed cleanup kept the account reserved", deleteErr)
	}
	got, err = store.GetNativeRestore(t.Context(), "org", r.ID)
	if err != nil || got.Status != "deleted" || got.Source.PhysicalID != "db-original" {
		t.Fatal("confirmed deletion erased copy history", got, err)
	}
}
