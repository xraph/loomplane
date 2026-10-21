//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	fed "github.com/xraph/loomplane/internal/federation"
)

func TestFederationPreparationPersistsBeforeCreation(t *testing.T) {
	db, store, d, _ := installationDatabase(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	in := fed.PreparationIntent{ID: uuid.NewString(), Namespace: "apps", NamespaceUID: d.Identity.NamespaceUID, Image: "example.test/agent@sha256:" + strings.Repeat("a", 64)}
	record, err := store.ReservePreparation(ctx, *d, in, "operator", allow)
	if err != nil || record.State != fed.PreparationReserved || record.Generation != 1 {
		t.Fatal("creation intent missing", err)
	}
	if err = store.Delete(ctx, d.OrgID, d.ID, d.Generation); !errors.Is(err, fed.ErrInUse) {
		t.Fatal("preparation did not retain destination", err)
	}
	if _, err = store.GetPreparation(ctx, "foreign", in.ID); !errors.Is(err, fed.ErrNotFound) {
		t.Fatal("foreign preparation visible", err)
	}
	other := in
	other.ID = uuid.NewString()
	if _, err = store.ReservePreparation(ctx, *d, other, "operator", allow); !errors.Is(err, fed.ErrConflict) {
		t.Fatal("duplicate active preparation", err)
	}
	reopened := fed.NewPgStore(db)
	if record, err = reopened.CurrentPreparation(ctx, d.OrgID, d.ID); err != nil || record.ID != in.ID {
		t.Fatal("lost preparation after restart", err)
	}
	if _, err = store.AdvancePreparation(ctx, d.OrgID, in.ID, 1, fed.PreparationPrepared, "pod-uid", "operator", allow); !errors.Is(err, fed.ErrConflict) {
		t.Fatal("skipped creation evidence", err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			_, stepErr := store.AdvancePreparation(ctx, d.OrgID, in.ID, 1, fed.PreparationAccountPending, "", "operator", allow)
			results <- stepErr
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for stepErr := range results {
		if stepErr == nil {
			success++
		} else if !errors.Is(stepErr, fed.ErrConflict) {
			t.Fatal(stepErr)
		}
	}
	if success != 1 {
		t.Fatal("two controllers claimed same create", success)
	}
	if record, err = reopened.GetPreparation(ctx, d.OrgID, in.ID); err != nil || record.State != fed.PreparationAccountPending {
		t.Fatal("uncertain create not retained", err)
	}
	for _, step := range []struct{ state, uid string }{{fed.PreparationAccountCreated, "sa-uid"}, {fed.PreparationPodPending, ""}, {fed.PreparationPrepared, "pod-uid"}} {
		record, err = store.AdvancePreparation(ctx, d.OrgID, in.ID, record.Generation, step.state, step.uid, "operator", allow)
		if err != nil {
			t.Fatal(err)
		}
	}
	if record.AccountUID != "sa-uid" || record.PodUID != "pod-uid" || record.Generation != 5 {
		t.Fatal("resource ownership missing", record)
	}
}

func TestFederationPreparationGuardRollback(t *testing.T) {
	db, store, d, _ := installationDatabase(t)
	ctx := t.Context()
	in := fed.PreparationIntent{ID: uuid.NewString(), Namespace: "apps", NamespaceUID: d.Identity.NamespaceUID, Image: "example.test/agent@sha256:" + strings.Repeat("a", 64)}
	calls := 0
	revoked := func(context.Context) error {
		calls++
		if calls == 2 {
			return errors.New("permission revoked")
		}
		return nil
	}
	if _, err := store.ReservePreparation(ctx, *d, in, "operator", revoked); err == nil {
		t.Fatal("revoked reservation committed")
	}
	if _, err := store.CurrentPreparation(ctx, d.OrgID, d.ID); !errors.Is(err, fed.ErrNotFound) {
		t.Fatal("failed reservation retained", err)
	}
	allow := func(context.Context) error { return nil }
	if _, err := store.ReservePreparation(ctx, *d, in, "operator", allow); err != nil {
		t.Fatal(err)
	}
	calls = 0
	if _, err := store.AdvancePreparation(ctx, d.OrgID, in.ID, 1, fed.PreparationAccountPending, "", "operator", revoked); err == nil {
		t.Fatal("revoked create claim committed")
	}
	r, err := store.GetPreparation(ctx, d.OrgID, in.ID)
	if err != nil || r.State != fed.PreparationReserved || r.Generation != 1 {
		t.Fatal("failed claim changed state", err)
	}
	var events int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM loomplane_events WHERE org_id=$1 AND type LIKE 'federation.preparation.%'`, d.OrgID).Scan(&events); err != nil || events != 1 {
		t.Fatal("failed transition changed event history", err, events)
	}
}
