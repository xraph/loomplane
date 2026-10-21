//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	"github.com/xraph/grove/drivers/pgdriver"
	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	fed "github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/workloadidentity"
)

func preparedEnrollmentFixture(t *testing.T) (*pgdriver.PgDB, *fed.PgStore, *fed.Preparation, workloadidentity.Binding) {
	t.Helper()
	db, store, d, profile := installationDatabase(t)
	allow := func(context.Context) error { return nil }
	r, err := store.ReservePreparation(t.Context(), *d, preparationIntent(), "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	r = prepareResources(t, store, r)
	profile.PodName = "loomplane-agent-" + r.ID
	profile.ServiceAccount = profile.PodName
	if _, err = store.PutInstallation(t.Context(), *d, profile, 0, "operator", time.Now(), allow); err != nil {
		t.Fatal(err)
	}
	d, err = store.Get(t.Context(), d.OrgID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := workloadidentity.Binding{Destination: *d, Method: "kubernetes", Namespace: r.Namespace, NamespaceUID: r.NamespaceUID, ServiceAccount: profile.ServiceAccount, PodUID: profile.PodUID, Container: profile.Container, ParentURI: "spiffe://example.test/spire/agent/k8s_psat/cluster/node", TTL: time.Minute, CheckedAt: time.Now()}
	return db, store, r, b
}
func TestPreparedEnrollmentReservesExactlyOnce(t *testing.T) {
	db, store, r, b := preparedEnrollmentFixture(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	enrollments := workloadidentity.NewEnrollmentStore(db, "example.test")
	var wg sync.WaitGroup
	records := make([]*workloadidentity.EnrollmentRecord, 2)
	errs := make([]error, 2)
	for i := range records {
		wg.Go(func() { records[i], errs[i] = enrollments.ReservePrepared(ctx, b, nil, r.ID, "operator", allow) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if records[0].ID != records[1].ID || records[0].Binding.Agent != records[1].Binding.Agent {
		t.Fatal("duplicate reservation")
	}
	saved, err := store.GetPreparation(ctx, r.OrgID, r.ID)
	if err != nil || saved.EnrollmentID != records[0].ID || saved.Generation != r.Generation+1 {
		t.Fatal("missing atomic receipt", saved, err)
	}
	restarted := workloadidentity.NewEnrollmentStore(db, "example.test")
	recovered, err := restarted.ReservePrepared(ctx, b, nil, r.ID, "operator", allow)
	if err != nil || recovered.ID != saved.EnrollmentID {
		t.Fatal("lost response allocated new enrollment", err)
	}
	var count, epoch int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_enrollments`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate enrollment", count, err)
	}
	if err = db.QueryRow(ctx, `SELECT epoch FROM loomplane_federation_enrollment_epochs WHERE org_id=$1 AND destination_id=$2`, r.OrgID, r.DestinationID).Scan(&epoch); err != nil || epoch != 1 {
		t.Fatal("retry advanced epoch", epoch, err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM loomplane_events WHERE type='federation.preparation.enrollment_reserved'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("receipt audit mismatch", count, err)
	}
	if err = federationMigration(t, "20261023000100").Down(ctx, pgmigrate.New(db)); err == nil {
		t.Fatal("downgrade discarded receipt")
	}
	if err = federationMigration(t, "20261023000100").Up(ctx, pgmigrate.New(db)); err != nil {
		t.Fatal("upgrade replay failed", err)
	}
}
func TestPreparedEnrollmentRejectsChangedOrRetiredBinding(t *testing.T) {
	for _, mode := range []string{"foreign preparation", "foreign organization", "namespace", "pod", "account", "container", "unrecorded installation", "retiring", "receipt parent changed", "receipt stale attestation", "receipt zero attestation", "receipt superseded", "receipt deleted", "receipt expired"} {
		t.Run(mode, func(t *testing.T) {
			db, store, r, b := preparedEnrollmentFixture(t)
			ctx := t.Context()
			allow := func(context.Context) error { return nil }
			enrollments := workloadidentity.NewEnrollmentStore(db, "example.test")
			id := r.ID
			var receipt *workloadidentity.EnrollmentRecord
			if len(mode) >= 7 && mode[:7] == "receipt" {
				var err error
				receipt, err = enrollments.ReservePrepared(ctx, b, nil, id, "operator", allow)
				if err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "foreign preparation":
				id = "foreign"
			case "foreign organization":
				b.Destination.OrgID = "foreign"
			case "namespace":
				b.Namespace = "foreign"
			case "pod":
				b.PodUID = "foreign"
			case "account":
				b.ServiceAccount = "foreign"
			case "container":
				b.Container = "foreign"
			case "unrecorded installation":
				if err := store.RetireInstallation(ctx, r.OrgID, r.DestinationID, b.Destination.Generation, "operator", allow); err != nil {
					t.Fatal(err)
				}
				d, err := store.Get(ctx, r.OrgID, r.DestinationID)
				if err != nil {
					t.Fatal(err)
				}
				b.Destination = *d
			case "retiring":
				saved, err := store.BeginPreparationRetirement(ctx, r.OrgID, id, r.Generation, "operator", allow)
				if err != nil {
					t.Fatal(err)
				}
				r = saved
				d, err := store.Get(ctx, r.OrgID, r.DestinationID)
				if err != nil {
					t.Fatal(err)
				}
				b.Destination = *d
			case "receipt stale attestation":
				b.CheckedAt = time.Now().Add(-6 * time.Minute)
			case "receipt zero attestation":
				b.CheckedAt = time.Time{}
			case "receipt parent changed":
				b.ParentURI = "spiffe://example.test/spire/agent/k8s_psat/cluster/other"
			case "receipt superseded":
				if _, err := enrollments.Reserve(ctx, b, nil); err != nil {
					t.Fatal(err)
				}
			case "receipt deleted":
				if err := enrollments.Revoke(ctx, r.OrgID, receipt.ID, &enrollmentIssuer{}); err != nil {
					t.Fatal(err)
				}
			case "receipt expired":
				if _, err := db.Exec(ctx, `UPDATE loomplane_federation_enrollments SET created_at=now()-interval '6 minutes',expires_at=now()-interval '2 minutes' WHERE id=$1`, receipt.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := enrollments.ReservePrepared(ctx, b, nil, id, "operator", allow); err == nil {
				t.Fatal("unsafe reservation accepted")
			}
			if receipt != nil {
				saved, err := store.GetPreparation(ctx, r.OrgID, r.ID)
				if err != nil || saved.EnrollmentID != receipt.ID {
					t.Fatal("receipt changed", err)
				}
			}
		})
	}
}
func TestPreparedEnrollmentGuardRollsBackReceipt(t *testing.T) {
	db, store, r, b := preparedEnrollmentFixture(t)
	ctx := t.Context()
	calls := 0
	enrollments := workloadidentity.NewEnrollmentStore(db, "example.test")
	_, err := enrollments.ReservePrepared(ctx, b, nil, r.ID, "operator", func(context.Context) error {
		calls++
		if calls == 2 {
			return errors.New("permission revoked")
		}
		return nil
	})
	if err == nil {
		t.Fatal("revoked reservation committed")
	}
	saved, err := store.GetPreparation(ctx, r.OrgID, r.ID)
	if err != nil || saved.EnrollmentID != "" || saved.Generation != r.Generation {
		t.Fatal("revoked receipt retained", err)
	}
	var count int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_enrollments`).Scan(&count); err != nil || count != 0 {
		t.Fatal("revoked enrollment retained", count, err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_enrollment_epochs`).Scan(&count); err != nil || count != 0 {
		t.Fatal("revoked epoch retained", count, err)
	}
}

type preparedCountingIssuer struct{ calls int }

func (i *preparedCountingIssuer) Register(_ context.Context, b workloadidentity.Binding) (*spiretypes.Entry, error) {
	i.calls++
	return &spiretypes.Entry{Id: b.EntryID}, nil
}
func (*preparedCountingIssuer) Revoke(context.Context, string) error { return nil }
func TestPreparedEnrollmentDoesNotIssueSupersededEpoch(t *testing.T) {
	db, _, r, b := preparedEnrollmentFixture(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	enrollments := workloadidentity.NewEnrollmentStore(db, "example.test")
	first, err := enrollments.ReservePrepared(ctx, b, nil, r.ID, "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = enrollments.Reserve(ctx, b, nil); err != nil {
		t.Fatal(err)
	}
	issuer := &preparedCountingIssuer{}
	if err = enrollments.Issue(ctx, r.OrgID, first.ID, issuer); err == nil || issuer.calls != 0 {
		t.Fatal("superseded epoch reached issuer", issuer.calls, err)
	}
}
