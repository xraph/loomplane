//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	fed "github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/workloadidentity"
)

func TestFederationLaunchRetirementRetainsAuthorityUntilRevoked(t *testing.T) {
	db, store, d, profile := installationDatabase(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	enrollments := workloadidentity.NewEnrollmentStore(db, "example.test")
	binding := workloadidentity.Binding{Destination: *d, Method: "kubernetes", Namespace: "apps", NamespaceUID: d.Identity.NamespaceUID, ServiceAccount: profile.ServiceAccount, PodUID: profile.PodUID, Container: profile.Container, ParentURI: "spiffe://example.test/spire/agent/k8s_psat/cluster/node", TTL: time.Minute, CheckedAt: time.Now()}
	enrollment, err := enrollments.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	in := fed.LaunchIntent{ID: id, EnrollmentID: enrollment.ID, Namespace: "apps", PodUID: profile.PodUID, ConfigMapName: "loomplane-agent-" + id, Digest: strings.Repeat("a", 64)}
	if _, err = store.ReserveLaunch(ctx, *d, in, "operator", allow); err != nil {
		t.Fatal(err)
	}
	retiring, err := store.BeginLaunchRetirement(ctx, d.OrgID, id, 1, "operator", allow)
	if err != nil || retiring.State != fed.LaunchRetiring || retiring.Generation != 2 {
		t.Fatal("retirement not durable", err)
	}
	if _, err = store.ConfirmLaunch(ctx, d.OrgID, id, 1, "cm-uid", "operator", allow); !errors.Is(err, fed.ErrConflict) {
		t.Fatal("retiring attempt confirmed", err)
	}
	if _, err = store.CompleteLaunchRetirement(ctx, d.OrgID, id, 2, "operator", allow); !errors.Is(err, fed.ErrInUse) {
		t.Fatal("live enrollment released", err)
	}
	if err = store.Delete(ctx, d.OrgID, d.ID, d.Generation); !errors.Is(err, fed.ErrInUse) {
		t.Fatal("cleanup reference lost", err)
	}
	if err = enrollments.Revoke(ctx, d.OrgID, enrollment.ID, &enrollmentIssuer{}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	guard := func(context.Context) error {
		calls++
		if calls == 2 {
			return errors.New("revoked before commit")
		}
		return nil
	}
	if _, err = store.CompleteLaunchRetirement(ctx, d.OrgID, id, 2, "operator", guard); err == nil {
		t.Fatal("failed guard released cleanup")
	}
	saved, err := fed.NewPgStore(db).GetLaunch(ctx, d.OrgID, id)
	if err != nil || saved.State != fed.LaunchRetiring {
		t.Fatal("cleanup lost after restart", err)
	}
	retired, err := store.CompleteLaunchRetirement(ctx, d.OrgID, id, 2, "operator", allow)
	if err != nil || retired.State != fed.LaunchRetired || retired.Generation != 3 {
		t.Fatal("cleanup not completed", err)
	}
	var count int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_destination_refs WHERE org_id=$1 AND owner_id=$2`, d.OrgID, "launch:"+id).Scan(&count); err != nil || count != 0 {
		t.Fatal("completed cleanup retained reference", err, count)
	}
	in.ID = uuid.NewString()
	in.ConfigMapName = "loomplane-agent-" + in.ID
	in.EnrollmentID = uuid.NewString()
	if _, err = store.ReserveLaunch(ctx, *d, in, "operator", allow); err != nil {
		t.Fatal("retired launch blocked fresh attempt", err)
	}
}

func TestFederationLaunchDeliveredRetirementPinsConfigUID(t *testing.T) {
	db, store, d, _ := installationDatabase(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	id := uuid.NewString()
	in := fed.LaunchIntent{ID: id, EnrollmentID: uuid.NewString(), Namespace: "apps", PodUID: "pod-uid", ConfigMapName: "loomplane-agent-" + id, Digest: strings.Repeat("a", 64)}
	if _, err := store.ReserveLaunch(ctx, *d, in, "operator", allow); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmLaunch(ctx, d.OrgID, id, 1, "original-cm", "operator", allow); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginLaunchRetirement(ctx, "foreign", id, 2, "operator", allow); !errors.Is(err, fed.ErrNotFound) {
		t.Fatal("foreign retirement", err)
	}
	r, err := store.BeginLaunchRetirement(ctx, d.OrgID, id, 2, "operator", allow)
	if err != nil || r.ConfigMapUID != "original-cm" || r.Generation != 3 {
		t.Fatal("retirement lost configuration identity", err)
	}
	if _, err = store.BeginLaunchRetirement(ctx, d.OrgID, id, 2, "operator", allow); !errors.Is(err, fed.ErrConflict) {
		t.Fatal("stale retirement allowed", err)
	}
	other := in
	other.ID = uuid.NewString()
	other.ConfigMapName = "loomplane-agent-" + other.ID
	other.EnrollmentID = uuid.NewString()
	if _, err = store.ReserveLaunch(ctx, *d, other, "operator", allow); !errors.Is(err, fed.ErrConflict) {
		t.Fatal("replacement allowed while retiring", err)
	}
	var retained int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_destination_refs WHERE org_id=$1 AND owner_id=$2`, d.OrgID, "launch:"+id).Scan(&retained); err != nil || retained != 1 {
		t.Fatal("delivered launch retirement not retained", err, retained)
	}
	if r, err = store.CompleteLaunchRetirement(ctx, d.OrgID, id, 3, "operator", allow); err != nil || r.State != fed.LaunchRetired || r.ConfigMapUID != "original-cm" {
		t.Fatal("retired history lost identity", err)
	}
	var events int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM loomplane_events WHERE org_id=$1 AND type IN ('federation.launch.retiring','federation.launch.retired')`, d.OrgID).Scan(&events); err != nil || events != 2 {
		t.Fatal("retirement events missing or repeated", err, events)
	}
}
