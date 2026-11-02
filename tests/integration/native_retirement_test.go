//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

func nativeRetirementFixture(t *testing.T) (nativePointFixture, recovery.NativeRetirementStore, *recovery.NativeRetirement) {
	t.Helper()
	f := nativePointHistoryFixture(t)
	account := &connections.Connection{ID: "account", OrgID: "org", Cloud: connections.AWS, Account: "123456789012", Details: map[string]string{"region": "us-east-1"}, Status: connections.StatusReady, Name: "Native retirement test", CreatedBy: "operator", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := connections.NewPgStore(f.db).CreateConnection(t.Context(), account); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	points := make([]recovery.NativePoint, 0, 3)
	for i, age := range []int{30, 20, 1} {
		p := nativeIdentityPoint(t, now.Add(-time.Duration(age)*24*time.Hour).Format(time.RFC3339Nano))
		p.Name = []string{"old", "witness", "new"}[i]
		p.ProviderID = "arn:aws:rds:us-east-1:123456789012:snapshot:" + p.Name
		points = append(points, p)
	}
	if err := f.refresh(t.Context(), points); err != nil {
		t.Fatal(err)
	}
	inventory, err := f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, p := range inventory.Points {
		ids[p.Name] = p.ID
	}
	sourceRaw, err := json.Marshal(f.source.Binding)
	if err != nil {
		t.Fatal(err)
	}
	pointRaw, err := json.Marshal(inventory.Points[1])
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_native_restores(id,org_id,project_id,environment_id,source_id,point_id,request_id,source,point,target,status,created_by,created_at,expires_at,verified_at,physical_id,external_id) VALUES('witness-copy','org','project','environment',$1,$2,'verified-request',$3,$4,'{}','deleted','operator',now()-interval '2 hours',now()+interval '1 hour',now()-interval '1 hour','db-verified','verified-private')`, f.source.ID, ids["witness"], sourceRaw, pointRaw)
	if err != nil {
		t.Fatal(err)
	}
	store, ok := any(f.store).(recovery.NativeRetirementStore)
	if !ok {
		t.Fatal("native deletion has no durable guarded store")
	}
	r := &recovery.NativeRetirement{ID: "retirement", OrgID: "org", ProjectID: "project", SourceID: f.source.ID, PointID: ids["old"], RequestID: "delete-original", CreatedBy: "operator", Policy: recovery.RetentionInput{OlderThanDays: 7, KeepLast: 1}}
	return f, store, r
}

func TestNativeRetirementAdmissionFencesRestoreAndKeepsOriginalIntent(t *testing.T) {
	f, store, request := nativeRetirementFixture(t)
	guard := func(context.Context) error { return nil }
	admitted, err := store.AdmitNativeRetirement(t.Context(), request, guard)
	if err != nil || admitted.Status != "queued" || admitted.WitnessID == "" || admitted.Point.Name != "old" || admitted.Source.PhysicalID != "db-original" {
		t.Fatal("native deletion admission lost scope", admitted, err)
	}
	replay, err := store.AdmitNativeRetirement(t.Context(), request, guard)
	if err != nil || replay.ID != admitted.ID {
		t.Fatal("native deletion request replay diverged", err)
	}
	changed := *request
	changed.CreatedBy = "other-owner"
	if _, err = store.AdmitNativeRetirement(t.Context(), &changed, guard); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("native deletion requester changed", err)
	}
	if _, err = store.GetNativeRetirement(t.Context(), "other-org", admitted.ID); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("native deletion crossed organization", err)
	}
	claimed, err := store.ClaimNativeRetirement(t.Context(), "org", admitted.ID, "worker-one")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CheckNativeRetirement(t.Context(), claimed); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveNativeRetirementIntent(t.Context(), claimed, true); err != nil {
		t.Fatal(err)
	}
	if err = store.FinishNativeRetirement(t.Context(), claimed, recovery.ErrNativePending); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingNativeRetirements(t.Context(), recovery.NativeCopyPage{})
	if err != nil || len(pending) != 1 || !pending[0].Attempted {
		t.Fatal("lost delete intent cannot reconcile", pending, err)
	}
	// No account exists in this fixture: the permanent point barrier must reject
	// admission before target lookup, rather than falling through to not found.
	copyRequest := &recovery.NativeRestore{ID: "unsafe-copy", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", SourceID: f.source.ID, PointID: admitted.PointID, Point: admitted.Point, RequestID: "unsafe-copy-request", CreatedBy: "operator", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), Target: recovery.NativeRestoreTarget{Provider: "rds", ConnectionID: "missing-account"}}
	if _, err = f.store.CreateNativeRestore(t.Context(), copyRequest); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("retired native point reached restore admission", err)
	}
	next, err := store.ClaimNativeRetirement(t.Context(), "org", admitted.ID, "worker-two")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveNativeRetirementIntent(t.Context(), claimed, false); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("stale worker changed native intent", err)
	}
	if err = store.FinishNativeRetirement(t.Context(), next, nil); err != nil {
		t.Fatal(err)
	}
	done, err := store.GetNativeRetirement(t.Context(), "org", admitted.ID)
	if err != nil || done.Status != "succeeded" || done.CompletedAt == nil || !done.Attempted {
		t.Fatal("native deletion completion lost evidence", done, err)
	}
}

type nativeRetirementService interface {
	PreviewNativeRetention(context.Context, security.Principal, string, recovery.RetentionInput) (*recovery.RetentionPreview, error)
	CreatePointRetirement(context.Context, security.Principal, string, string, recovery.NativeRetirementInput) (*recovery.NativeRetirement, error)
	RunNativeRetirement(context.Context, string, string) error
	ReconcileRetirements(context.Context) error
}

type nativeRetirementBackend struct {
	points  []recovery.NativePoint
	deleted bool
	writes  int
	lose    bool
}

func (b *nativeRetirementBackend) NativeSnapshots(context.Context, *planning.Binding) ([]recovery.NativePoint, bool, error) {
	out := []recovery.NativePoint{}
	for _, p := range b.points {
		if p.Name != "old" || !b.deleted {
			out = append(out, p)
		}
	}
	return out, true, nil
}
func (b *nativeRetirementBackend) CheckNativeSnapshot(_ context.Context, _ *planning.Binding, p recovery.NativePoint) (*recovery.NativePoint, error) {
	return &p, nil
}
func (b *nativeRetirementBackend) DeleteNativePoint(ctx context.Context, _ security.Principal, r recovery.NativeRetirement, c recovery.NativeDeletionControl) (bool, error) {
	if err := c.Check(ctx); err != nil {
		return false, err
	}
	if !r.Attempted {
		if err := c.Before(); err != nil {
			return false, err
		}
		if b.deleted {
			return true, c.Check(ctx)
		}
		b.writes++
		b.deleted = true
		if b.lose {
			b.lose = false
			return false, recovery.ErrNativePending
		}
	}
	if !b.deleted {
		return false, recovery.ErrConflict
	}
	return true, c.Check(ctx)
}

func TestNativeRetirementServiceReconcilesLostResponseWithoutAnotherDelete(t *testing.T) {
	f, store, request := nativeRetirementFixture(t)
	inventory, err := f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil {
		t.Fatal(err)
	}
	backend := &nativeRetirementBackend{points: inventory.Points, lose: true}
	enqueued := 0
	service := &recovery.NativeService{Store: f.store, Backend: backend, Authz: nativeRetirementAccess{}, Projects: projects.NewPgStore(f.db), Retirements: store, DeletionBackend: backend, EnqueueDeletion: func(context.Context, string, string) error { enqueued++; return nil }}
	runner, ok := any(service).(nativeRetirementService)
	if !ok {
		t.Fatal("native retention has no public service/worker lifecycle")
	}
	p := security.Principal{OrgID: "org", UserID: "operator"}
	preview, err := runner.PreviewNativeRetention(t.Context(), p, f.source.ID, request.Policy)
	if err != nil || preview.EligibleCount != 1 {
		t.Fatal("native retention preview missing", preview, err)
	}
	input := recovery.NativeRetirementInput{RequestID: request.RequestID, Policy: request.Policy, AcknowledgeDeletion: true}
	admitted, err := runner.CreatePointRetirement(t.Context(), p, f.source.ID, request.PointID, input)
	if err != nil || enqueued != 1 {
		t.Fatal("native retirement did not enqueue", err)
	}
	if err = runner.RunNativeRetirement(t.Context(), "org", admitted.ID); !errors.Is(err, recovery.ErrNativePending) {
		t.Fatal("lost response discarded reconciliation", err)
	}
	pending, err := store.GetNativeRetirement(t.Context(), "org", admitted.ID)
	if err != nil || pending.Status != "queued" || !pending.Attempted {
		t.Fatal("native intent lost after worker finalization", pending, err)
	}
	if err = runner.ReconcileRetirements(t.Context()); err != nil || enqueued < 2 {
		t.Fatal("native deletion enqueue gap is unrecoverable", err)
	}
	if err = runner.RunNativeRetirement(t.Context(), "org", admitted.ID); err != nil {
		t.Fatal(err)
	}
	done, err := store.GetNativeRetirement(t.Context(), "org", admitted.ID)
	if err != nil || done.Status != "succeeded" || backend.writes != 1 {
		t.Fatal("native deletion replayed or incomplete", done, backend.writes, err)
	}
}

type nativeRetirementAccess struct{}

func (nativeRetirementAccess) Require(ctx context.Context, p security.Principal, permission security.Permission, project string) error {
	if permission == security.BackupRetire && p.OrgID == "org" && p.UserID == "operator" && project == "project" {
		return nil
	}
	return (nativeAccessAllow{}).Require(ctx, p, permission, project)
}

func TestNativeRetirementServiceConfirmsExternalAbsence(t *testing.T) {
	f, store, request := nativeRetirementFixture(t)
	inventory, err := f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil {
		t.Fatal(err)
	}
	backend := &nativeRetirementBackend{points: inventory.Points}
	service := &recovery.NativeService{Store: f.store, Backend: backend, Authz: nativeRetirementAccess{}, Projects: projects.NewPgStore(f.db), Retirements: store, DeletionBackend: backend, EnqueueDeletion: func(context.Context, string, string) error { return nil }}
	p := security.Principal{OrgID: "org", UserID: "operator"}
	admitted, err := service.CreatePointRetirement(t.Context(), p, f.source.ID, request.PointID, recovery.NativeRetirementInput{RequestID: request.RequestID, Policy: request.Policy, AcknowledgeDeletion: true})
	if err != nil {
		t.Fatal(err)
	}
	backend.deleted = true
	if err = service.RunNativeRetirement(t.Context(), "org", admitted.ID); err != nil {
		t.Fatal("external expiry froze admitted native retention", err)
	}
	done, err := store.GetNativeRetirement(t.Context(), "org", admitted.ID)
	if err != nil || done.Status != "succeeded" || !done.Attempted || backend.writes != 0 {
		t.Fatal("external absence was not durably confirmed", done, backend.writes, err)
	}
}
