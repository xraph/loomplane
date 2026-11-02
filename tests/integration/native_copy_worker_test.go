//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

func TestNativeCopyPendingClaimsKeepOriginalCheckpointAndFenceWrites(t *testing.T) {
	f, records, request := nativeRestoreFixture(t)
	store, ok := any(f.store).(recovery.NativeCopyWorkStore)
	if !ok {
		t.Fatal("native copy workers cannot discover abandoned claims or check their lease")
	}
	r, err := records.CreateNativeRestore(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingNativeRestores(t.Context(), recovery.NativeCopyPage{})
	if err != nil || len(pending) != 1 || pending[0].ID != r.ID {
		t.Fatal("queued copy was lost before dispatch", pending, err)
	}
	claimed, err := records.ClaimNativeRestore(t.Context(), "org", r.ID, "first")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CheckNativeLease(t.Context(), claimed, "running"); err != nil {
		t.Fatal(err)
	}
	if err = store.CheckNativeLease(t.Context(), claimed, "deleting"); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("restore lease authorized cleanup", err)
	}
	if err = store.CheckNativeLease(t.Context(), claimed, "anything"); !errors.Is(err, recovery.ErrInvalid) {
		t.Fatal("invalid lease mode was admitted", err)
	}
	if pending, err = store.PendingNativeRestores(t.Context(), recovery.NativeCopyPage{}); err != nil || len(pending) != 0 {
		t.Fatal("active copy was dispatched again", pending, err)
	}
	if err = records.SaveNativeCheckpoint(t.Context(), claimed, json.RawMessage(`{"operation":"saved-operation"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.CheckNativeLease(t.Context(), claimed, "running"); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("expired worker retained authority", err)
	}
	if pending, err = store.PendingNativeRestores(t.Context(), recovery.NativeCopyPage{}); err != nil || len(pending) != 1 {
		t.Fatal("abandoned copy disappeared", pending, err)
	}
	second, err := records.ClaimNativeRestore(t.Context(), "org", r.ID, "second")
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint map[string]string
	if err = json.Unmarshal(second.Checkpoint, &checkpoint); err != nil || checkpoint["operation"] != "saved-operation" {
		t.Fatal("reclaimed copy lost captured operation", err)
	}
	if err = store.CheckNativeLease(t.Context(), claimed, "running"); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("replaced token retained authority", err)
	}
	foreign := *second
	foreign.OrgID = "foreign"
	if err = store.CheckNativeLease(t.Context(), &foreign, "running"); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("foreign lease admitted", err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET created_at=now()-interval '25 hours',expires_at=now()-interval '1 hour' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = records.SaveNativeCheckpoint(t.Context(), second, json.RawMessage(`{"operation":"too-late"}`)); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("copy checkpoint changed after its lifetime expired", err)
	}
	if err = records.FinishNativeRestore(t.Context(), second, recovery.NativeRestoreResult{ExternalID: "copy", PhysicalID: "physical-copy", SQLVerified: true}, time.Second, nil); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("copy published success after its lifetime expired", err)
	}
	if pending, err = store.PendingNativeRestores(t.Context(), recovery.NativeCopyPage{}); err != nil || len(pending) != 0 {
		t.Fatal("expired copy was queued for creation", pending, err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	cleanup, err := records.ClaimNativeCleanup(t.Context(), "org", r.ID, "cleanup-worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = records.SaveNativeCheckpoint(t.Context(), cleanup, json.RawMessage(`{"operation":"cleanup-operation"}`)); err != nil {
		t.Fatal("expiry blocked the cleanup checkpoint", err)
	}
}

type nativeCopyRunner interface {
	RunCopy(context.Context, string, string) error
	ReconcileCopies(context.Context) error
}
type nativeCopyExecution func(context.Context, security.Principal, recovery.NativeRestore, recovery.NativeCopyControl) (recovery.NativeRestoreResult, error)

func (f nativeCopyExecution) RestoreNative(ctx context.Context, p security.Principal, r recovery.NativeRestore, c recovery.NativeCopyControl) (recovery.NativeRestoreResult, error) {
	return f(ctx, p, r, c)
}

type nativeCopyAuthorization struct{ denied bool }

func (a *nativeCopyAuthorization) Require(_ context.Context, p security.Principal, permission security.Permission, project string) error {
	if a.denied || p.OrgID != "org" || p.UserID != "operator" || permission != security.RestoreManage || project != "project" {
		return security.ErrForbidden
	}
	return nil
}

type nativeCopySelection struct {
	recovery.NativeBackend
	changed bool
	calls   int
}

func (b *nativeCopySelection) CheckNativeSnapshot(_ context.Context, source *planning.Binding, point recovery.NativePoint) (*recovery.NativePoint, error) {
	b.calls++
	var cfg planning.ResourceConfig
	if source.OrgID != "org" || source.PhysicalID != "db-original" || json.Unmarshal(source.Config, &cfg) != nil || cfg.Kind != "postgres" || cfg.Version != "17" {
		return nil, recovery.ErrConflict
	}
	point.Status = "available"
	if b.changed {
		point.CreatedAt = point.CreatedAt.Add(time.Nanosecond)
	}
	return &point, nil
}

func TestNativeCopyWorkerRequiresFreshSelectionLeaseAndSQLProof(t *testing.T) {
	f, records, template := nativeRestoreFixture(t)
	store, ok := any(f.store).(recovery.NativeCopyWorkStore)
	if !ok {
		t.Fatal("native copy work store is missing")
	}
	for _, scenario := range []string{"verified", "missing-sql", "missing-pin", "provider-failure", "revoked-before", "revoked-during", "lease-lost", "revoked-on-return", "lease-lost-on-return", "changed-point", "mutated-input", "canceled", "resumed-captured"} {
		t.Run(scenario, func(t *testing.T) {
			r := *template
			r.ID = "copy-" + scenario
			r.RequestID = "request-" + scenario
			created, err := records.CreateNativeRestore(t.Context(), &r)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "resumed-captured" {
				first, claimErr := records.ClaimNativeRestore(t.Context(), "org", created.ID, "interrupted-worker")
				if claimErr != nil {
					t.Fatal(claimErr)
				}
				if err = records.SaveNativeCheckpoint(t.Context(), first, json.RawMessage(`{"operation":"original-operation"}`)); err != nil {
					t.Fatal(err)
				}
				if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET lease_until=now()-interval '1 second' WHERE id=$1`, created.ID); err != nil {
					t.Fatal(err)
				}
			}
			auth := &nativeCopyAuthorization{denied: scenario == "revoked-before"}
			selection := &nativeCopySelection{changed: scenario == "changed-point" || scenario == "resumed-captured"}
			called := false
			backend := nativeCopyExecution(func(ctx context.Context, p security.Principal, input recovery.NativeRestore, control recovery.NativeCopyControl) (recovery.NativeRestoreResult, error) {
				called = true
				if input.ID != created.ID || input.Token == "" || p.UserID != "operator" {
					t.Fatal("worker lost its claimed scope")
				}
				if scenario == "revoked-during" {
					auth.denied = true
				}
				if scenario == "lease-lost" {
					if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET token='replacement' WHERE id=$1`, created.ID); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "mutated-input" {
					input.ID = "foreign-copy"
					input.Token = "foreign-token"
					input.Source.Config[0] = '!'
				}
				if scenario == "canceled" {
					cancelCtx, cancel := context.WithCancel(ctx)
					cancel()
					ctx = cancelCtx
				}
				if guardErr := control.Check(ctx); guardErr != nil {
					return recovery.NativeRestoreResult{}, guardErr
				}
				if saveErr := control.Save(ctx, json.RawMessage(`{"operation":"original-operation"}`)); saveErr != nil {
					return recovery.NativeRestoreResult{}, saveErr
				}
				if scenario != "resumed-captured" {
					if _, selectErr := control.Selected(ctx); selectErr != nil {
						return recovery.NativeRestoreResult{}, selectErr
					}
				} else {
					var saved map[string]string
					if decodeErr := json.Unmarshal(input.Checkpoint, &saved); decodeErr != nil || saved["operation"] != "original-operation" {
						t.Fatal("resumed backend lost original operation", decodeErr)
					}
				}
				result := recovery.NativeRestoreResult{ExternalID: "native-copy", PhysicalID: "copy-incarnation", SQLVerified: scenario != "missing-sql"}
				if scenario == "missing-pin" {
					result.PhysicalID = ""
				}
				if scenario == "provider-failure" {
					return result, errors.New("private provider diagnostic")
				}
				if scenario == "revoked-on-return" {
					auth.denied = true
				}
				if scenario == "lease-lost-on-return" {
					if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET token='replacement' WHERE id=$1`, created.ID); err != nil {
						t.Fatal(err)
					}
				}
				return result, nil
			})
			s := &recovery.NativeService{Copies: store, Authz: auth, Projects: nativeSelectionProjects{}, Backend: selection, CopyBackend: backend}
			worker, exists := any(s).(nativeCopyRunner)
			if !exists {
				t.Fatal("native copy worker is missing")
			}
			err = worker.RunCopy(t.Context(), "org", created.ID)
			got, readErr := records.GetNativeRestore(t.Context(), "org", created.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if scenario == "verified" || scenario == "mutated-input" || scenario == "resumed-captured" {
				if err != nil || got.Status != "succeeded" || got.VerifiedAt == nil || got.PhysicalID != "copy-incarnation" {
					t.Fatalf("SQL verified copy was not recorded: %+v %v", got, err)
				}
			} else if err == nil || got.VerifiedAt != nil || got.Status == "succeeded" {
				t.Fatalf("unverified copy was published: %+v %v", got, err)
			}
			if scenario == "revoked-before" || scenario == "changed-point" {
				if called {
					t.Fatal("unavailable or denied copy reached provider mutation")
				}
			}
			if got.Error == "private provider diagnostic" {
				t.Fatal("private provider error leaked")
			}
			if scenario == "resumed-captured" && selection.calls != 0 {
				t.Fatal("accepted native copy depended on now-unavailable source backup")
			}
			if scenario == "mutated-input" {
				var state map[string]string
				if err = json.Unmarshal(got.Checkpoint, &state); err != nil || state["operation"] != "original-operation" {
					t.Fatal("backend input redirected checkpoint", err)
				}
			}
		})
	}
}

func TestNativeCopyReconcileRepairsQueueGapsAndContinuesAfterFailure(t *testing.T) {
	f, records, request := nativeRestoreFixture(t)
	store, ok := any(f.store).(recovery.NativeCopyWorkStore)
	if !ok {
		t.Fatal("native copy work store is missing")
	}
	for n := 0; n < 2; n++ {
		r := *request
		r.ID = fmt.Sprintf("copy-%d", n)
		r.RequestID = fmt.Sprintf("request-%d", n)
		if _, err := records.CreateNativeRestore(t.Context(), &r); err != nil {
			t.Fatal(err)
		}
	}
	queued := map[string]bool{}
	s := &recovery.NativeService{Copies: store, EnqueueCopy: func(_ context.Context, org, id string) error {
		if org != "org" {
			t.Fatal("queue lost organization")
		}
		queued[id] = true
		if id == "copy-0" {
			return errors.New("queue unavailable")
		}
		return nil
	}}
	worker, ok := any(s).(nativeCopyRunner)
	if !ok {
		t.Fatal("native copy worker is missing")
	}
	if err := worker.ReconcileCopies(t.Context()); err == nil || !queued["copy-0"] || !queued["copy-1"] {
		t.Fatal("queue gap stopped reconciliation", queued, err)
	}
}

func TestNativeCopyReconcileDoesNotStarvePastOneFailedPage(t *testing.T) {
	f, records, request := nativeRestoreFixture(t)
	store, ok := any(f.store).(recovery.NativeCopyWorkStore)
	if !ok {
		t.Fatal("native copy work store is missing")
	}
	for n := 0; n <= 100; n++ {
		r := *request
		r.ID = fmt.Sprintf("copy-%03d", n)
		if n == 100 {
			r.ID = "zzz-final"
		}
		r.RequestID = "request-" + r.ID
		if _, err := records.CreateNativeRestore(t.Context(), &r); err != nil {
			t.Fatal(err)
		}
	}
	queued := map[string]bool{}
	s := &recovery.NativeService{Copies: store, EnqueueCopy: func(_ context.Context, org, id string) error {
		if org != "org" {
			t.Fatal("queue lost organization")
		}
		queued[id] = true
		if id != "zzz-final" {
			return errors.New("copy queue rejected this payload")
		}
		return nil
	}}
	worker, ok := any(s).(nativeCopyRunner)
	if !ok {
		t.Fatal("native copy worker is missing")
	}
	if err := worker.ReconcileCopies(t.Context()); err == nil || len(queued) != 101 || !queued["zzz-final"] {
		t.Fatal("failed first page starved a later healthy copy", len(queued), err)
	}
}
