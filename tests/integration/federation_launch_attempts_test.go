//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	fed "github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/workloadidentity"
)

func TestFederationLaunchIntentSurvivesRestartAndReservesOwnership(t *testing.T) {
	db, store, d, _ := installationDatabase(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	id := uuid.NewString()
	intent := fed.LaunchIntent{ID: id, EnrollmentID: uuid.NewString(), Namespace: "apps", PodUID: "pod-uid", ConfigMapName: "loomplane-agent-" + id, Digest: strings.Repeat("a", 64)}
	saved, err := store.ReserveLaunch(ctx, *d, intent, "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != fed.LaunchPending || saved.Generation != 1 || saved.Scope != d.Scope || saved.DestinationGeneration != d.Generation {
		t.Fatal("intent not bound to destination")
	}
	if _, err = store.GetLaunch(ctx, "foreign", id); !errors.Is(err, fed.ErrNotFound) {
		t.Fatal("foreign attempt visible", err)
	}
	reopened := fed.NewPgStore(db)
	loaded, err := reopened.GetLaunch(ctx, d.OrgID, id)
	if err != nil || loaded.Digest != intent.Digest || loaded.PodUID != intent.PodUID {
		t.Fatal("lost uncertain write intent", err)
	}
	if err = store.Delete(ctx, d.OrgID, d.ID, d.Generation); !errors.Is(err, fed.ErrInUse) {
		t.Fatal("pending write lost destination reference", err)
	}
	if _, err = store.ReserveLaunch(ctx, *d, intent, "operator", allow); !errors.Is(err, fed.ErrConflict) {
		t.Fatal("duplicate attempt reserved", err)
	}
	changed := intent
	changed.ID = uuid.NewString()
	changed.ConfigMapName = "loomplane-agent-" + changed.ID
	if _, err = store.ReserveLaunch(ctx, *d, changed, "operator", allow); !errors.Is(err, fed.ErrConflict) {
		t.Fatal("same enrollment reserved for another attempt", err)
	}
	changed.EnrollmentID = uuid.NewString()
	if _, err = store.ReserveLaunch(ctx, *d, changed, "operator", allow); !errors.Is(err, fed.ErrConflict) {
		t.Fatal("second unresolved destination write reserved", err)
	}
	denied := func(context.Context) error { return errors.New("permission revoked") }
	if _, err = store.ConfirmLaunch(ctx, d.OrgID, id, 1, "config-uid", "operator", denied); err == nil {
		t.Fatal("revoked caller confirmed launch")
	}
	calls := 0
	revokeBeforeCommit := func(context.Context) error {
		calls++
		if calls == 2 {
			return errors.New("revoked before commit")
		}
		return nil
	}
	if _, err = store.ConfirmLaunch(ctx, d.OrgID, id, 1, "config-uid", "operator", revokeBeforeCommit); err == nil {
		t.Fatal("confirmation ignored final guard")
	}
	stillPending, err := store.GetLaunch(ctx, d.OrgID, id)
	if err != nil || stillPending.State != fed.LaunchPending || stillPending.Generation != 1 {
		t.Fatal("failed confirmation persisted", err)
	}
	var events int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM loomplane_events WHERE org_id=$1 AND type='federation.launch.delivered'`, d.OrgID).Scan(&events); err != nil || events != 0 {
		t.Fatal("failed confirmation left event", err, events)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			_, confirmErr := reopened.ConfirmLaunch(ctx, d.OrgID, id, 1, "config-uid", "operator", allow)
			results <- confirmErr
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for result := range results {
		if result == nil {
			success++
		} else if !errors.Is(result, fed.ErrConflict) {
			t.Fatal(result)
		}
	}
	if success != 1 {
		t.Fatal("confirmation did not compare generations", success)
	}
	complete, err := store.GetLaunch(ctx, d.OrgID, id)
	if err != nil || complete.State != fed.LaunchDelivered || complete.ConfigMapUID != "config-uid" || complete.Generation != 2 {
		t.Fatal("confirmation not persisted", err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM loomplane_events WHERE org_id=$1 AND type IN ('federation.launch.reserved','federation.launch.delivered')`, d.OrgID).Scan(&events); err != nil || events != 2 {
		t.Fatal("launch evidence history missing or duplicated", err, events)
	}
	var count int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_destination_refs WHERE org_id=$1 AND destination_id=$2 AND owner_id=$3`, d.OrgID, d.ID, "launch:"+id).Scan(&count); err != nil || count != 0 {
		t.Fatal("resolved uncertainty retained pending reference", err, count)
	}
}

func TestFederationLaunchReservationGuardsRollback(t *testing.T) {
	_, store, d, _ := installationDatabase(t)
	id := uuid.NewString()
	intent := fed.LaunchIntent{ID: id, EnrollmentID: uuid.NewString(), Namespace: "apps", PodUID: "pod-uid", ConfigMapName: "loomplane-agent-" + id, Digest: strings.Repeat("b", 64)}
	calls := 0
	guard := func(context.Context) error {
		calls++
		if calls == 2 {
			return errors.New("revoked before commit")
		}
		return nil
	}
	if _, err := store.ReserveLaunch(t.Context(), *d, intent, "operator", guard); err == nil {
		t.Fatal("revoked reservation committed")
	}
	if _, err := store.GetLaunch(t.Context(), d.OrgID, id); !errors.Is(err, fed.ErrNotFound) {
		t.Fatal("rolled back attempt remains", err)
	}
	d.Generation++
	if _, err := store.ReserveLaunch(t.Context(), *d, intent, "operator", func(context.Context) error { return nil }); err == nil {
		t.Fatal("stale destination reserved launch")
	}
}

func TestFederationLaunchReservationAllowsReadyGuard(t *testing.T) {
	db, attempts, d, profile := installationDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	enrollments := workloadidentity.NewEnrollmentStore(db, "example.test")
	binding := workloadidentity.Binding{Destination: *d, Method: "kubernetes", Namespace: "apps", NamespaceUID: d.Identity.NamespaceUID, ServiceAccount: profile.ServiceAccount, PodUID: profile.PodUID, Container: profile.Container, ParentURI: "spiffe://example.test/spire/agent/k8s_psat/cluster/node", TTL: time.Minute, CheckedAt: time.Now()}
	record, err := enrollments.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = enrollments.Issue(ctx, d.OrgID, record.ID, &enrollmentIssuer{}); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	intent := fed.LaunchIntent{ID: id, EnrollmentID: record.ID, Namespace: "apps", PodUID: profile.PodUID, ConfigMapName: "loomplane-agent-" + id, Digest: strings.Repeat("c", 64)}
	checks := 0
	guard := func(ctx context.Context) error {
		checks++
		_, readErr := enrollments.Ready(ctx, d.OrgID, record.ID)
		return readErr
	}
	if _, err = attempts.ReserveLaunch(ctx, *d, intent, "operator", guard); err != nil {
		t.Fatal("reservation blocked enrollment authority read", err)
	}
	if checks != 2 {
		t.Fatal("ready authority was not checked around reservation", checks)
	}
}
