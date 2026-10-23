//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

func nativeCleanupStore(t *testing.T, f nativePointFixture) recovery.NativeCleanupWorkStore {
	t.Helper()
	store, ok := any(f.store).(recovery.NativeCleanupWorkStore)
	if !ok {
		t.Fatal("native cleanup has no durable request and retry store")
	}
	return store
}

func TestNativeCleanupRequestRetainsAuthorizedActorAcrossRestart(t *testing.T) {
	f, records, request := nativeRestoreFixture(t)
	store := nativeCleanupStore(t, f)
	r, err := records.CreateNativeRestore(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	guardCalls := 0
	guard := func(context.Context) error { guardCalls++; return nil }
	queued, err := store.RequestNativeCleanup(t.Context(), "org", r.ID, "replacement-admin", guard)
	if err != nil || queued.Status != "cleanup_queued" || queued.CreatedBy != "operator" || queued.CleanupBy != "replacement-admin" || queued.CleanupRequestedAt == nil || guardCalls == 0 {
		t.Fatalf("cleanup request lost its actor: %+v %v", queued, err)
	}
	reopened := recovery.NewPgStore(f.db)
	got, err := reopened.GetNativeRestore(t.Context(), "org", r.ID)
	if err != nil || got.CleanupBy != "replacement-admin" || got.CleanupRequestedAt == nil || !got.CleanupRequestedAt.Equal(*queued.CleanupRequestedAt) {
		t.Fatal("restart lost cleanup request", got, err)
	}
	replayed, err := store.RequestNativeCleanup(t.Context(), "org", r.ID, "replacement-admin", guard)
	if err != nil || replayed.ID != r.ID || !replayed.CleanupRequestedAt.Equal(*queued.CleanupRequestedAt) {
		t.Fatal("replay replaced cleanup intent", replayed, err)
	}
	if _, err = records.ClaimNativeRestore(t.Context(), "org", r.ID, "late-copy"); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("cleanup intent allowed a new copy worker", err)
	}
	if _, err = store.RequestNativeCleanup(t.Context(), "org", r.ID, "denied-admin", func(context.Context) error { return security.ErrForbidden }); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("denied cleanup changed its actor", err)
	}
	if _, err = store.RequestNativeCleanup(t.Context(), "foreign", r.ID, "replacement-admin", guard); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("cleanup request escaped its organization", err)
	}
	if _, err = store.RequestNativeCleanup(t.Context(), "org", r.ID, "", guard); !errors.Is(err, recovery.ErrInvalid) {
		t.Fatal("cleanup without an actor was queued", err)
	}
	if _, err = store.RequestNativeCleanup(t.Context(), "org", r.ID, "replacement-admin", nil); !errors.Is(err, recovery.ErrInvalid) {
		t.Fatal("cleanup without a guard was queued", err)
	}
	current, err := store.GetNativeRestore(t.Context(), "org", r.ID)
	if err != nil || current.CleanupBy != "replacement-admin" {
		t.Fatal("denied request replaced retained actor", current, err)
	}
}

func TestNativeCleanupRespectsActiveWorkersAndRetryBackoff(t *testing.T) {
	f, records, request := nativeRestoreFixture(t)
	store := nativeCleanupStore(t, f)
	r, err := records.CreateNativeRestore(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = records.ClaimNativeCleanup(t.Context(), "org", r.ID, "early-cleanup"); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("unrequested unexpired copy was removed", err)
	}
	claimed, err := records.ClaimNativeRestore(t.Context(), "org", r.ID, "copy-worker")
	if err != nil {
		t.Fatal(err)
	}
	guard := func(context.Context) error { return nil }
	if _, err = store.RequestNativeCleanup(t.Context(), "org", r.ID, "admin", guard); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("cleanup took over an active copy worker", err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RequestNativeCleanup(t.Context(), "org", r.ID, "admin", guard); err != nil {
		t.Fatal(err)
	}
	if err = records.SaveNativeCheckpoint(t.Context(), claimed, json.RawMessage(`{"operation":"late-copy-operation"}`)); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("late copy changed cleanup checkpoint", err)
	}
	cleanup, err := records.ClaimNativeCleanup(t.Context(), "org", r.ID, "cleanup-one")
	if err != nil || cleanup.CleanupBy != "admin" {
		t.Fatal("queued cleanup was not claimed under its actor", cleanup, err)
	}
	if _, err = store.RequestNativeCleanup(t.Context(), "org", r.ID, "another-admin", guard); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("manual request replaced an active cleanup worker", err)
	}
	if err = records.SaveNativeCheckpoint(t.Context(), cleanup, json.RawMessage(`{"operation":"original-delete"}`)); err != nil {
		t.Fatal(err)
	}
	if err = records.FinishNativeCleanup(t.Context(), cleanup, errors.New("private deletion diagnostic")); err != nil {
		t.Fatal(err)
	}
	if _, err = records.ClaimNativeCleanup(t.Context(), "org", r.ID, "too-soon"); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("cleanup failure ignored retry backoff", err)
	}
	pending, err := store.PendingNativeCleanups(t.Context(), recovery.NativeCopyPage{})
	if err != nil || len(pending) != 0 {
		t.Fatal("failed cleanup bypassed its retry delay", pending, err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	pending, err = store.PendingNativeCleanups(t.Context(), recovery.NativeCopyPage{})
	if err != nil || len(pending) != 1 || pending[0].CleanupBy != "admin" {
		t.Fatal("cleanup retry lost its saved actor", pending, err)
	}
	retry, err := records.ClaimNativeCleanup(t.Context(), "org", r.ID, "cleanup-two")
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]string
	if err = json.Unmarshal(retry.Checkpoint, &state); err != nil || state["operation"] != "original-delete" {
		t.Fatal("retry lost captured delete operation", err)
	}
	if err = records.FinishNativeCleanup(t.Context(), cleanup, nil); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("old cleanup claim completed after takeover", err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	pending, err = store.PendingNativeCleanups(t.Context(), recovery.NativeCopyPage{})
	if err != nil || len(pending) != 1 || pending[0].ID != r.ID {
		t.Fatal("abandoned cleanup was not rediscovered", pending, err)
	}
	recovered, err := records.ClaimNativeCleanup(t.Context(), "org", r.ID, "cleanup-three")
	if err != nil || recovered.CleanupBy != "admin" {
		t.Fatal("recovered cleanup lost its caller", recovered, err)
	}
	if err = json.Unmarshal(recovered.Checkpoint, &state); err != nil || state["operation"] != "original-delete" {
		t.Fatal("recovered cleanup lost its captured operation", err)
	}
	if err = records.SaveNativeCheckpoint(t.Context(), retry, json.RawMessage(`{"operation":"late-delete"}`)); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("abandoned cleanup worker changed the new claim", err)
	}
}

type nativeCleanupRunner interface {
	RequestCopyCleanup(context.Context, security.Principal, string) (*recovery.NativeRestore, error)
	RunCleanup(context.Context, string, string) error
	ReconcileCleanups(context.Context) error
}
type nativeDeleteExecution func(context.Context, security.Principal, recovery.NativeRestore, recovery.NativeWorkerControl) (recovery.NativeCleanupResult, error)

func (f nativeDeleteExecution) DeleteNative(ctx context.Context, p security.Principal, r recovery.NativeRestore, c recovery.NativeWorkerControl) (recovery.NativeCleanupResult, error) {
	return f(ctx, p, r, c)
}

type nativeCleanupAuthorization struct {
	denied bool
	actor  string
}

func (a *nativeCleanupAuthorization) Require(_ context.Context, p security.Principal, permission security.Permission, project string) error {
	if a.denied || p.OrgID != "org" || p.UserID != a.actor || permission != security.RestoreManage || project != "project" {
		return security.ErrForbidden
	}
	return nil
}

func TestNativeCleanupWorkerRequiresAbsenceProofAndCurrentPermission(t *testing.T) {
	f, records, template := nativeRestoreFixture(t)
	store := nativeCleanupStore(t, f)
	for _, scenario := range []string{"confirmed", "not-confirmed", "provider-failure", "revoked-before", "revoked-during", "revoked-on-return", "lease-lost", "lease-lost-on-return", "canceled-on-return", "mutated-input", "expired-automatic"} {
		t.Run(scenario, func(t *testing.T) {
			r := *template
			r.ID = "copy-" + scenario
			r.RequestID = "request-" + scenario
			created, err := records.CreateNativeRestore(t.Context(), &r)
			if err != nil {
				t.Fatal(err)
			}
			actor := "replacement-admin"
			if scenario == "expired-automatic" {
				actor = "operator"
				if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET created_at=now()-interval '25 hours',expires_at=now()-interval '1 hour' WHERE id=$1`, created.ID); err != nil {
					t.Fatal(err)
				}
			} else if _, err = store.RequestNativeCleanup(t.Context(), "org", created.ID, actor, func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
			auth := &nativeCleanupAuthorization{denied: scenario == "revoked-before", actor: actor}
			runCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			called := false
			backend := nativeDeleteExecution(func(ctx context.Context, p security.Principal, input recovery.NativeRestore, control recovery.NativeWorkerControl) (recovery.NativeCleanupResult, error) {
				called = true
				if p.UserID != actor || input.ID != created.ID || input.Token == "" {
					t.Fatal("cleanup worker lost its caller or original copy")
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
					input.ID = "other-copy"
					input.Token = "other-token"
					input.Source.Config[0] = '!'
				}
				if err = control.Check(ctx); err != nil {
					return recovery.NativeCleanupResult{}, err
				}
				if err = control.Save(ctx, json.RawMessage(`{"operation":"original-delete"}`)); err != nil {
					return recovery.NativeCleanupResult{}, err
				}
				result := recovery.NativeCleanupResult{ConfirmedAbsent: scenario != "not-confirmed"}
				if scenario == "provider-failure" {
					return result, errors.New("private deletion diagnostic")
				}
				if scenario == "revoked-on-return" {
					auth.denied = true
				}
				if scenario == "lease-lost-on-return" {
					if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET token='replacement' WHERE id=$1`, created.ID); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "canceled-on-return" {
					cancel()
				}
				return result, nil
			})
			s := &recovery.NativeService{Cleanups: store, Authz: auth, Projects: nativeSelectionProjects{}, CleanupBackend: backend}
			worker, ok := any(s).(nativeCleanupRunner)
			if !ok {
				t.Fatal("native cleanup worker is missing")
			}
			err = worker.RunCleanup(runCtx, "org", created.ID)
			got, readErr := records.GetNativeRestore(t.Context(), "org", created.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if scenario == "confirmed" || scenario == "mutated-input" || scenario == "expired-automatic" {
				if err != nil || got.Status != "deleted" {
					t.Fatal("confirmed cleanup was not recorded", got, err)
				}
			} else if err == nil || got.Status == "deleted" {
				t.Fatal("unconfirmed or denied cleanup was published", got, err)
			}
			if scenario == "revoked-before" && called {
				t.Fatal("denied cleanup reached native deletion")
			}
			if got.Error == "private deletion diagnostic" {
				t.Fatal("private cleanup error leaked")
			}
			var holds int
			if readErr = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_connection_uses WHERE org_id='org' AND kind='native_restore' AND owner_id=$1`, created.ID).Scan(&holds); readErr != nil {
				t.Fatal(readErr)
			}
			if got.Status == "deleted" && holds != 0 || got.Status != "deleted" && holds != 2 {
				t.Fatal("cleanup released or retained the wrong connection references", got.Status, holds)
			}
			if got.Status != "deleted" {
				if deleteErr := connections.NewPgStore(f.db).DeleteConnection(t.Context(), "org", "recovery-account"); !errors.Is(deleteErr, connections.ErrInUse) {
					t.Fatal("unconfirmed cleanup released destination credentials", deleteErr)
				}
			}
		})
	}
}

func TestNativeCleanupRequestRepairsLostEnqueueAndUsesCurrentAdministrator(t *testing.T) {
	f, records, request := nativeRestoreFixture(t)
	store := nativeCleanupStore(t, f)
	r, err := records.CreateNativeRestore(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	queued := 0
	s := &recovery.NativeService{Cleanups: store, Authz: &nativeCleanupAuthorization{actor: "replacement-admin"}, Projects: nativeSelectionProjects{}, EnqueueCleanup: func(_ context.Context, org, id string) error {
		if org != "org" || id != r.ID {
			t.Fatal("cleanup queue scope changed")
		}
		queued++
		return errors.New("queue unavailable")
	}}
	worker, ok := any(s).(nativeCleanupRunner)
	if !ok {
		t.Fatal("native cleanup worker is missing")
	}
	if _, denyErr := worker.RequestCopyCleanup(t.Context(), security.Principal{OrgID: "org", UserID: "removed-member"}, r.ID); !errors.Is(denyErr, security.ErrForbidden) || queued != 0 {
		t.Fatal("denied cleanup reached enqueue", queued, denyErr)
	}
	got, err := worker.RequestCopyCleanup(t.Context(), security.Principal{OrgID: "org", UserID: "replacement-admin"}, r.ID)
	if err == nil || got == nil || got.Status != "cleanup_queued" || got.CleanupBy != "replacement-admin" || queued != 1 {
		t.Fatal("lost enqueue lost durable cleanup intent", got, queued, err)
	}
	if err = worker.ReconcileCleanups(t.Context()); err == nil || queued != 2 {
		t.Fatal("cleanup enqueue gap was not repaired", queued, err)
	}
}

func TestNativeCleanupReconcilePagesBeyondFailedEnqueueBatch(t *testing.T) {
	f, records, request := nativeRestoreFixture(t)
	store := nativeCleanupStore(t, f)
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
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET created_at=now()-interval '25 hours',expires_at=now()-interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	queued := map[string]bool{}
	s := &recovery.NativeService{Cleanups: store, EnqueueCleanup: func(_ context.Context, org, id string) error {
		if org != "org" {
			t.Fatal("cleanup queue scope changed")
		}
		queued[id] = true
		if id != "zzz-final" {
			return errors.New("queue rejected this payload")
		}
		return nil
	}}
	worker, ok := any(s).(nativeCleanupRunner)
	if !ok {
		t.Fatal("native cleanup worker is missing")
	}
	if err := worker.ReconcileCleanups(t.Context()); err == nil || len(queued) != 101 || !queued["zzz-final"] {
		t.Fatal("cleanup first page starved a later copy", len(queued), err)
	}
}
