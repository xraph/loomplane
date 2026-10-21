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
	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	fed "github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/workloadidentity"
)

func preparationIntent() fed.PreparationIntent {
	return fed.PreparationIntent{ID: uuid.NewString(), Namespace: "apps", NamespaceUID: "ns-uid", Image: "example.test/agent@sha256:" + strings.Repeat("a", 64)}
}
func prepareResources(t *testing.T, store *fed.PgStore, r *fed.Preparation) *fed.Preparation {
	t.Helper()
	for _, step := range []struct{ state, uid string }{{fed.PreparationAccountPending, ""}, {fed.PreparationAccountCreated, "sa-uid"}, {fed.PreparationPodPending, ""}, {fed.PreparationPrepared, "pod-uid"}} {
		var err error
		r, err = store.AdvancePreparation(t.Context(), r.OrgID, r.ID, r.Generation, step.state, step.uid, "operator", func(context.Context) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestFederationPreparationRetirementFencesInstallation(t *testing.T) {
	db, store, d, profile := installationDatabase(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	r, err := store.ReservePreparation(ctx, *d, preparationIntent(), "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	profile.PodName = "loomplane-agent-" + r.ID
	profile.ServiceAccount = profile.PodName
	if _, err = store.PutInstallation(ctx, *d, profile, 0, "operator", time.Now(), allow); !errors.Is(err, fed.ErrConflict) {
		t.Fatal("unprepared installation accepted", err)
	}
	r = prepareResources(t, store, r)
	installed, err := store.PutInstallation(ctx, *d, profile, 0, "operator", time.Now(), allow)
	if err != nil {
		t.Fatal(err)
	}
	d, err = store.Get(ctx, d.OrgID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	enrollments := workloadidentity.NewEnrollmentStore(db, "example.test")
	binding := workloadidentity.Binding{Destination: *d, Method: "kubernetes", Namespace: "apps", NamespaceUID: d.Identity.NamespaceUID, ServiceAccount: profile.ServiceAccount, PodUID: profile.PodUID, Container: profile.Container, ParentURI: "spiffe://example.test/spire/agent/k8s_psat/cluster/node", TTL: time.Minute, CheckedAt: time.Now()}
	pending, err := enrollments.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &enrollmentIssuer{}
	if err = enrollments.Issue(ctx, d.OrgID, pending.ID, issuer); err != nil {
		t.Fatal(err)
	}
	r, err = store.BeginPreparationRetirement(ctx, d.OrgID, r.ID, r.Generation, "operator", allow)
	if err != nil || r.State != fed.PreparationRetiring || r.CleanupFrom != fed.PreparationPrepared {
		t.Fatal("retirement missing", r, err)
	}
	if _, err = store.GetInstallation(ctx, d.OrgID, d.ID); !errors.Is(err, fed.ErrNotFound) {
		t.Fatal("installation survived retirement", err)
	}
	if err = enrollments.CheckPending(ctx, pending.ID, pending.Binding.Agent); err == nil {
		t.Fatal("pending enrollment retained authority")
	}
	d, err = store.Get(ctx, d.OrgID, d.ID)
	if err != nil || d.Generation != installed.Generation+1 {
		t.Fatal("generation not fenced", err)
	}
	if _, err = store.PutInstallation(ctx, *d, profile, 0, "operator", time.Now(), allow); !errors.Is(err, fed.ErrConflict) {
		t.Fatal("retiring resources rebound", err)
	}
	binding.Destination = *d
	if _, err = enrollments.Reserve(ctx, binding, nil); err == nil {
		t.Fatal("retiring account enrolled at new generation")
	}
	if _, err = store.CompletePreparationRetirement(ctx, d.OrgID, r.ID, r.Generation, "operator", allow); !errors.Is(err, fed.ErrInUse) {
		t.Fatal("retirement discarded enrollment", err)
	}
	if err = enrollments.Revoke(ctx, d.OrgID, pending.ID, issuer); err != nil {
		t.Fatal(err)
	}
	r, err = store.CompletePreparationRetirement(ctx, d.OrgID, r.ID, r.Generation, "operator", allow)
	if err != nil || r.State != fed.PreparationRetired {
		t.Fatal("retirement not completed", err)
	}
	if _, err = store.CurrentPreparation(ctx, d.OrgID, d.ID); !errors.Is(err, fed.ErrNotFound) {
		t.Fatal("retired preparation remains current", err)
	}
	if _, err = store.PutInstallation(ctx, *d, profile, 0, "operator", time.Now(), allow); !errors.Is(err, fed.ErrConflict) {
		t.Fatal("retired resources rebound", err)
	}
	if err = store.Delete(ctx, d.OrgID, d.ID, d.Generation); err != nil {
		t.Fatal("retired history blocked destination deletion", err)
	}
}

func TestFederationPreparationRetirementPreservesReplacement(t *testing.T) {
	_, store, d, profile := installationDatabase(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	r, err := store.ReservePreparation(ctx, *d, preparationIntent(), "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	r = prepareResources(t, store, r)
	installed, err := store.PutInstallation(ctx, *d, profile, 0, "operator", time.Now(), allow)
	if err != nil {
		t.Fatal(err)
	}
	r, err = store.BeginPreparationRetirement(ctx, d.OrgID, r.ID, r.Generation, "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.GetInstallation(ctx, d.OrgID, d.ID)
	if err != nil || current.Generation != installed.Generation || current.Profile != profile {
		t.Fatal("unrelated installation changed", err)
	}
	if _, err = store.CompletePreparationRetirement(ctx, d.OrgID, r.ID, r.Generation, "operator", allow); err != nil {
		t.Fatal(err)
	}
	d, err = store.Get(ctx, d.OrgID, d.ID)
	if err != nil || d.Generation != installed.Generation {
		t.Fatal("replacement fenced", err)
	}
	if _, err = store.ReservePreparation(ctx, *d, preparationIntent(), "operator", allow); err != nil {
		t.Fatal("retired preparation blocked replacement", err)
	}
}

func TestFederationPreparationRetirementCapturesUncertainOwnership(t *testing.T) {
	db, store, d, _ := installationDatabase(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	r, err := store.ReservePreparation(ctx, *d, preparationIntent(), "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	r, err = store.AdvancePreparation(ctx, d.OrgID, r.ID, r.Generation, fed.PreparationAccountPending, "", "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	r, err = store.BeginPreparationRetirement(ctx, d.OrgID, r.ID, r.Generation, "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompletePreparationRetirement(ctx, d.OrgID, r.ID, r.Generation, "operator", allow); !errors.Is(err, fed.ErrInUse) {
		t.Fatal("uncertain create discarded", err)
	}
	if _, err = store.CapturePreparationOwnership(ctx, d.OrgID, r.ID, r.Generation, "", "pod-uid", "operator", allow); !errors.Is(err, fed.ErrConflict) {
		t.Fatal("unclaimed pod adopted", err)
	}
	r, err = store.CapturePreparationOwnership(ctx, d.OrgID, r.ID, r.Generation, "sa-uid", "", "operator", allow)
	if err != nil || r.AccountUID != "sa-uid" {
		t.Fatal("observed ownership not captured", err)
	}
	if _, err = store.CapturePreparationOwnership(ctx, d.OrgID, r.ID, r.Generation, "replacement-uid", "", "operator", allow); !errors.Is(err, fed.ErrConflict) {
		t.Fatal("saved ownership overwritten", err)
	}
	if err = federationMigration(t, "20261022000200").Down(ctx, pgmigrate.New(db)); err == nil {
		t.Fatal("retirement history discarded")
	}
	if err = federationMigration(t, "20261022000200").Up(ctx, pgmigrate.New(db)); err != nil {
		t.Fatal("upgrade cannot replay", err)
	}
	r, err = store.CompletePreparationRetirement(ctx, d.OrgID, r.ID, r.Generation, "operator", allow)
	if err != nil || r.CleanupFrom != fed.PreparationAccountPending {
		t.Fatal("cleanup origin lost", err)
	}
}

func TestFederationPreparationRetirementGuardRollsBackFence(t *testing.T) {
	db, store, d, profile := installationDatabase(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	r, err := store.ReservePreparation(ctx, *d, preparationIntent(), "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	r = prepareResources(t, store, r)
	profile.PodName = "loomplane-agent-" + r.ID
	profile.ServiceAccount = profile.PodName
	installed, err := store.PutInstallation(ctx, *d, profile, 0, "operator", time.Now(), allow)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	denied := func(context.Context) error {
		calls++
		if calls == 2 {
			return errors.New("permission revoked")
		}
		return nil
	}
	if _, err = store.BeginPreparationRetirement(ctx, d.OrgID, r.ID, r.Generation, "operator", denied); err == nil {
		t.Fatal("revoked retirement committed")
	}
	current, err := store.GetInstallation(ctx, d.OrgID, d.ID)
	if err != nil || current.Generation != installed.Generation {
		t.Fatal("failed retirement removed installation", err)
	}
	r, err = store.GetPreparation(ctx, d.OrgID, r.ID)
	if err != nil || r.State != fed.PreparationPrepared || r.Generation != 5 {
		t.Fatal("failed retirement changed preparation", err)
	}
	var events int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM loomplane_events WHERE type IN ('federation.preparation.retiring','federation.installation.retired')`).Scan(&events); err != nil || events != 0 {
		t.Fatal("failed retirement emitted events", events, err)
	}
}

func TestFederationPreparationRetirementSerializesInstallation(t *testing.T) {
	_, store, d, profile := installationDatabase(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	r, err := store.ReservePreparation(ctx, *d, preparationIntent(), "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	r = prepareResources(t, store, r)
	profile.PodName = "loomplane-agent-" + r.ID
	profile.ServiceAccount = profile.PodName
	start := make(chan struct{})
	var wg sync.WaitGroup
	var installErr, retireErr error
	wg.Go(func() {
		<-start
		_, installErr = store.PutInstallation(ctx, *d, profile, 0, "operator", time.Now(), allow)
	})
	wg.Go(func() {
		<-start
		_, retireErr = store.BeginPreparationRetirement(ctx, d.OrgID, r.ID, r.Generation, "operator", allow)
	})
	close(start)
	wg.Wait()
	if retireErr != nil {
		t.Fatal("retirement lost race", retireErr)
	}
	if installErr != nil && !errors.Is(installErr, fed.ErrConflict) {
		t.Fatal(installErr)
	}
	if _, err = store.GetInstallation(ctx, d.OrgID, d.ID); !errors.Is(err, fed.ErrNotFound) {
		t.Fatal("concurrent installation survived fence", err)
	}
	if _, err = store.AdvancePreparation(ctx, d.OrgID, r.ID, 4, fed.PreparationPrepared, "pod-uid", "operator", allow); !errors.Is(err, fed.ErrConflict) {
		t.Fatal("stale preparation resumed", err)
	}
}

func TestFederationPreparationRetirementRetainsLaunch(t *testing.T) {
	_, store, d, _ := installationDatabase(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	r, err := store.ReservePreparation(ctx, *d, preparationIntent(), "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := store.ReserveLaunch(ctx, *d, fed.LaunchIntent{ID: r.ID, EnrollmentID: uuid.NewString(), Namespace: r.Namespace, PodUID: "pod-uid", ConfigMapName: "loomplane-agent-" + r.ID, Digest: strings.Repeat("a", 64)}, "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	r, err = store.BeginPreparationRetirement(ctx, d.OrgID, r.ID, r.Generation, "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompletePreparationRetirement(ctx, d.OrgID, r.ID, r.Generation, "operator", allow); !errors.Is(err, fed.ErrInUse) {
		t.Fatal("unresolved launch discarded", err)
	}
	launch, err = store.BeginLaunchRetirement(ctx, d.OrgID, launch.ID, launch.Generation, "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompleteLaunchRetirement(ctx, d.OrgID, launch.ID, launch.Generation, "operator", allow); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompletePreparationRetirement(ctx, d.OrgID, r.ID, r.Generation, "operator", allow); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReservePreparation(ctx, *d, preparationIntent(), "operator", allow); err != nil {
		t.Fatal("retired reservation blocked replacement", err)
	}
}

func TestFederationPreparationRetirementMigrationRoundTrip(t *testing.T) {
	db, store, d, _ := installationDatabase(t)
	ctx := t.Context()
	m := federationMigration(t, "20261022000200")
	if err := m.Down(ctx, pgmigrate.New(db)); err != nil {
		t.Fatal(err)
	}
	if err := m.Up(ctx, pgmigrate.New(db)); err != nil {
		t.Fatal(err)
	}
	allow := func(context.Context) error { return nil }
	r, err := store.ReservePreparation(ctx, *d, preparationIntent(), "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	r, err = store.BeginPreparationRetirement(ctx, d.OrgID, r.ID, r.Generation, "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Down(ctx, pgmigrate.New(db)); err == nil {
		t.Fatal("downgrade lost retirement")
	}
	if _, err = db.Exec(ctx, `UPDATE loomplane_federation_preparations SET state='invalid' WHERE id=$1`, r.ID); err == nil {
		t.Fatal("refused downgrade removed state constraint")
	}
	if _, err = db.Exec(ctx, `UPDATE loomplane_federation_preparations SET cleanup_from='prepared' WHERE id=$1`, r.ID); err == nil {
		t.Fatal("refused downgrade removed ownership constraint")
	}
	if _, err = store.ReservePreparation(ctx, *d, preparationIntent(), "operator", allow); !errors.Is(err, fed.ErrConflict) {
		t.Fatal("refused downgrade removed uniqueness", err)
	}
	if err = store.Delete(ctx, d.OrgID, d.ID, d.Generation); !errors.Is(err, fed.ErrInUse) {
		t.Fatal("retirement released reference", err)
	}
}
