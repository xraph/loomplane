//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/workloadidentity"
)

func TestPreparationEnrollmentRevocationIsScopedAndRetryable(t *testing.T) {
	db, store, d, _ := installationDatabase(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	r, err := store.ReservePreparation(ctx, *d, preparationIntent(), "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	r = prepareResources(t, store, r)
	enrollments := workloadidentity.NewEnrollmentStore(db, "example.test")
	binding := workloadidentity.Binding{Destination: *d, Method: "kubernetes", Namespace: "apps", NamespaceUID: d.Identity.NamespaceUID, ServiceAccount: "loomplane-agent-" + r.ID, PodUID: r.PodUID, Container: "agent", ParentURI: "spiffe://example.test/spire/agent/k8s_psat/cluster/node", TTL: time.Minute, CheckedAt: time.Now()}
	first, err := enrollments.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := enrollments.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding.ServiceAccount = "unrelated"
	other, err := enrollments.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &enrollmentIssuer{failEntry: first.Binding.EntryID}
	if err = enrollments.RevokePreparation(ctx, d.OrgID, d.ID, r.ID, issuer); err == nil {
		t.Fatal("live preparation revoked")
	}
	r, err = store.BeginPreparationRetirement(ctx, d.OrgID, r.ID, r.Generation, "operator", allow)
	if err != nil {
		t.Fatal(err)
	}
	if err = enrollments.RevokePreparation(ctx, "foreign", d.ID, r.ID, issuer); err == nil {
		t.Fatal("foreign cleanup accepted")
	}
	if err = enrollments.RevokePreparation(ctx, d.OrgID, "foreign", r.ID, issuer); err == nil {
		t.Fatal("foreign destination accepted")
	}
	if err = enrollments.RevokePreparation(ctx, d.OrgID, d.ID, r.ID, issuer); err == nil {
		t.Fatal("failed issuer released cleanup")
	}
	var phase string
	if err = db.QueryRow(ctx, `SELECT phase FROM loomplane_federation_enrollments WHERE id=$1`, first.ID).Scan(&phase); err != nil || phase != "revoking" {
		t.Fatal("failed entry not retained and fenced", phase, err)
	}
	var count int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_enrollments WHERE id=$1`, second.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("one failure stopped other revocations", count, err)
	}
	if err = db.QueryRow(ctx, `SELECT phase FROM loomplane_federation_enrollments WHERE id=$1`, other.ID).Scan(&phase); err != nil || phase != "pending" {
		t.Fatal("unrelated enrollment changed", phase, err)
	}
	issuer.failEntry = ""
	if err = enrollments.RevokePreparation(ctx, d.OrgID, d.ID, r.ID, issuer); err != nil {
		t.Fatal("retry failed", err)
	}
	if err = enrollments.RevokePreparation(ctx, d.OrgID, d.ID, r.ID, issuer); err != nil {
		t.Fatal("empty retry failed", err)
	}
	if _, err = store.CompletePreparationRetirement(ctx, d.OrgID, r.ID, r.Generation, "operator", allow); err != nil {
		t.Fatal("unrelated enrollment blocked completion", err)
	}
}
