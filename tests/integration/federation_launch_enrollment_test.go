//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/workloadidentity"
)

func TestFederationLaunchRequiresCurrentReadyEnrollment(t *testing.T) {
	db, _, d, p := installationDatabase(t)
	ctx := t.Context()
	store := workloadidentity.NewEnrollmentStore(db, "example.test")
	binding := workloadidentity.Binding{Destination: *d, Method: "kubernetes", Namespace: "apps", NamespaceUID: d.Identity.NamespaceUID, ServiceAccount: p.ServiceAccount, PodUID: p.PodUID, Container: p.Container, ParentURI: "spiffe://example.test/spire/agent/k8s_psat/cluster/node", TTL: time.Minute, CheckedAt: time.Now()}
	first, err := store.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Ready(ctx, d.OrgID, first.ID); err == nil {
		t.Fatal("pending enrollment delivered")
	}
	issuer := &enrollmentIssuer{}
	if err = store.Issue(ctx, d.OrgID, first.ID, issuer); err != nil {
		t.Fatal(err)
	}
	loaded, err := workloadidentity.NewEnrollmentStore(db, "example.test").Ready(ctx, d.OrgID, first.ID)
	if err != nil || loaded.Binding.Agent != first.Binding.Agent || loaded.Registration.Agent != first.Registration.Agent {
		t.Fatal("ready enrollment did not survive restart", err)
	}
	if _, err = store.Ready(ctx, "foreign", first.ID); err == nil {
		t.Fatal("cross-tenant enrollment read")
	}
	second, err := store.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Ready(ctx, d.OrgID, first.ID); err == nil {
		t.Fatal("superseded enrollment delivered")
	}
	if err = store.CheckPending(ctx, first.ID, first.Binding.Agent); err == nil {
		t.Fatal("superseded enrollment authenticated")
	}
	if err = store.Issue(ctx, d.OrgID, second.ID, issuer); err != nil {
		t.Fatal(err)
	}
	if err = store.Consume(ctx, second.ID, second.Binding.Agent); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Ready(ctx, d.OrgID, second.ID); err == nil {
		t.Fatal("consumed enrollment delivered")
	}
	for _, state := range []string{"expired", "revoking", "destination changed", "quarantined"} {
		t.Run(state, func(t *testing.T) {
			record, reserveErr := store.Reserve(ctx, binding, nil)
			if reserveErr != nil {
				t.Fatal(reserveErr)
			}
			if issueErr := store.Issue(ctx, d.OrgID, record.ID, issuer); issueErr != nil {
				t.Fatal(issueErr)
			}
			switch state {
			case "expired":
				_, err = db.Exec(ctx, `UPDATE loomplane_federation_enrollments SET created_at=now()-interval '2 minutes',expires_at=now()-interval '1 second' WHERE id=$1`, record.ID)
			case "revoking":
				_, err = db.Exec(ctx, `UPDATE loomplane_federation_enrollments SET phase='revoking' WHERE id=$1`, record.ID)
			case "destination changed":
				_, err = db.Exec(ctx, `UPDATE loomplane_federation_destinations SET generation=generation+1 WHERE id=$1`, d.ID)
				defer func() {
					_, _ = db.Exec(ctx, `UPDATE loomplane_federation_destinations SET generation=generation-1 WHERE id=$1`, d.ID)
				}()
			case "quarantined":
				_, err = db.Exec(ctx, `UPDATE loomplane_federation_destinations SET quarantined=true WHERE id=$1`, d.ID)
				defer func() {
					_, _ = db.Exec(ctx, `UPDATE loomplane_federation_destinations SET quarantined=false WHERE id=$1`, d.ID)
				}()
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, readErr := store.Ready(ctx, d.OrgID, record.ID); readErr == nil {
				t.Fatal("invalid enrollment delivered")
			}
		})
	}
}

func TestFederationLaunchRechecksExpiryAfterDatabaseWait(t *testing.T) {
	db, _, d, p := installationDatabase(t)
	ctx := t.Context()
	store := workloadidentity.NewEnrollmentStore(db, "example.test")
	b := workloadidentity.Binding{Destination: *d, Method: "kubernetes", Namespace: "apps", NamespaceUID: d.Identity.NamespaceUID, ServiceAccount: p.ServiceAccount, PodUID: p.PodUID, Container: p.Container, ParentURI: "spiffe://example.test/spire/agent/k8s_psat/cluster/node", TTL: time.Minute, CheckedAt: time.Now()}
	r, err := store.Reserve(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Issue(ctx, d.OrgID, r.ID, &enrollmentIssuer{}); err != nil {
		t.Fatal(err)
	}
	var expiry time.Time
	if err = db.QueryRow(ctx, `UPDATE loomplane_federation_enrollments SET created_at=clock_timestamp()-interval '1 minute',expires_at=clock_timestamp()+interval '1 second' WHERE id=$1 RETURNING expires_at`, r.ID).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	var epoch int64
	if err = blocker.QueryRow(ctx, `SELECT epoch FROM loomplane_federation_enrollment_epochs WHERE org_id=$1 AND destination_id=$2 FOR UPDATE`, d.OrgID, d.ID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, readErr := store.Ready(ctx, d.OrgID, r.ID); done <- readErr }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND query LIKE '%enrollment_epochs%FOR SHARE%' AND cardinality(pg_blocking_pids(pid))>0)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ready read did not reach the held epoch lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if wait := time.Until(expiry) + 50*time.Millisecond; wait > 0 {
		time.Sleep(wait)
	}
	if err = blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case readErr := <-done:
		if readErr == nil {
			t.Fatal("enrollment expired while waiting for its epoch but was returned ready")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ready read did not finish after releasing the epoch")
	}
}
