//go:build integration

package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/access"
	fed "github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func statusEnrollment(t *testing.T) (*pgdriver.PgDB, *fed.PgStore, *fed.Preparation, *workloadidentity.EnrollmentStore, *workloadidentity.EnrollmentRecord) {
	t.Helper()
	db, store, p, b := preparedEnrollmentFixture(t)
	enrollments := workloadidentity.NewEnrollmentStore(db, "example.test")
	r, err := enrollments.ReservePrepared(t.Context(), b, nil, p.ID, "operator", func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = enrollments.Issue(t.Context(), p.OrgID, r.ID, &enrollmentIssuer{}); err != nil {
		t.Fatal(err)
	}
	in := fed.LaunchIntent{ID: p.ID, EnrollmentID: r.ID, Namespace: p.Namespace, PodUID: p.PodUID, ConfigMapName: "loomplane-agent-" + p.ID, Digest: strings.Repeat("a", 64)}
	a, err := store.ReserveLaunch(t.Context(), b.Destination, in, "operator", func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ConfirmLaunch(t.Context(), p.OrgID, a.ID, a.Generation, "config-uid", "operator", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	return db, store, p, enrollments, r
}
func statusPolicy(t *testing.T, db *pgdriver.PgDB, a domain.AgentIdentity) (*access.PolicyStore, func() domain.Snapshot) {
	t.Helper()
	policies := access.NewPolicyStore(db)
	catalog := runtimecatalog.NewPgStore(db)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := access.NewSnapshotSigner(a.Scope, private, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return policies, func() domain.Snapshot {
		t.Helper()
		input, readErr := policies.Input(t.Context(), a.Scope)
		if readErr != nil {
			t.Fatal(readErr)
		}
		bundle, compileErr := access.CompileForAgent(input, a)
		if compileErr != nil {
			t.Fatal(compileErr)
		}
		_, seq, readErr := catalog.Read(t.Context(), a)
		if readErr != nil {
			t.Fatal(readErr)
		}
		signed, issueErr := policies.IssueSnapshot(t.Context(), a, seq, domain.Snapshot{Scope: a.Scope, DestinationID: a.DestinationID, Epoch: a.Epoch, Full: true, IssuedAt: input.EvaluatedAt, ExpiresAt: input.EvaluatedAt.Add(30 * time.Second), Policy: bundle}, signer)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		return signed
	}
}
func TestPreparationStatusDurableLifecycle(t *testing.T) {
	db, store, p, enrollments, r := statusEnrollment(t)
	ctx := t.Context()
	status := func(want string) {
		t.Helper()
		s, err := fed.NewPgStore(db).ReadPreparationStatus(ctx, p.OrgID, p.DestinationID, p.ID)
		if err != nil || s.State != want {
			t.Fatalf("want %s: %+v %v", want, s, err)
		}
	}
	status("enrolling")
	if _, err := store.ReadPreparationStatus(ctx, "foreign", p.DestinationID, p.ID); !errors.Is(err, fed.ErrNotFound) {
		t.Fatal("foreign organization read", err)
	}
	if _, err := store.ReadPreparationStatus(ctx, p.OrgID, "foreign", p.ID); !errors.Is(err, fed.ErrNotFound) {
		t.Fatal("foreign destination read", err)
	}
	if err := enrollments.Consume(ctx, r.ID, r.Binding.Agent); err != nil {
		t.Fatal(err)
	}
	status("awaiting_policy")
	policies, issue := statusPolicy(t, db, r.Binding.Agent)
	first := issue()
	status("policy_pending")
	ack := func(s domain.Snapshot) {
		t.Helper()
		if err := policies.AcknowledgeSnapshot(ctx, r.Binding.Agent, domain.Acknowledgment{Agent: r.Binding.Agent, Sequence: s.Sequence, Revision: s.Revision, AppliedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	ack(first)
	status("policy_current")
	var before, after uint64
	if err := db.QueryRow(ctx, `SELECT next_seq FROM loomplane_event_cursors WHERE org_id=$1`, p.OrgID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		status("policy_current")
	}
	if err := db.QueryRow(ctx, `SELECT next_seq FROM loomplane_event_cursors WHERE org_id=$1`, p.OrgID).Scan(&after); err != nil || before != after {
		t.Fatal("status polling changed event history", err)
	}
	second := issue()
	status("policy_pending")
	ack(second)
	status("policy_current")
	if _, err := policies.PutExport(ctx, domain.ServiceExport{Metadata: domain.Metadata{Scope: r.Binding.Agent.Scope, ID: "status-export"}, ServiceID: "service", DestinationIDs: []string{p.DestinationID}, Port: 8080, Protocol: domain.HTTP, IdentityProfile: domain.WorkloadIdentity}, 0, "operator", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	status("policy_pending")
	updated := issue()
	ack(updated)
	status("policy_current")
	// An agent with a consumed bootstrap remains current after that bootstrap's
	// deadline. The signed policy lease is the status deadline.
	if _, err := db.Exec(ctx, `UPDATE loomplane_federation_enrollments SET created_at=now()-interval '6 minutes',expires_at=now()-interval '1 minute' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	status("policy_current")
	if _, err := db.Exec(ctx, `UPDATE loomplane_federation_deliveries SET state=jsonb_set(state,'{expires_at}',to_jsonb(now()+interval '4 seconds')) WHERE org_id=$1`, p.OrgID); err != nil {
		t.Fatal(err)
	}
	status("lease_expired")
	saved, err := store.GetPreparation(ctx, p.OrgID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginPreparationRetirement(ctx, p.OrgID, p.ID, saved.Generation, "operator", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	status("retiring")
}
func TestPreparationStatusConsistentPolicySnapshot(t *testing.T) {
	db, store, p, enrollments, r := statusEnrollment(t)
	ctx := t.Context()
	if err := enrollments.Consume(ctx, r.ID, r.Binding.Agent); err != nil {
		t.Fatal(err)
	}
	policies, issue := statusPolicy(t, db, r.Binding.Agent)
	s := issue()
	if err := policies.AcknowledgeSnapshot(ctx, r.Binding.Agent, domain.Acknowledgment{Agent: r.Binding.Agent, Sequence: s.Sequence, Revision: s.Revision, AppliedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	change := func(generation, receipt int) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if _, err = tx.Exec(ctx, `UPDATE loomplane_federation_policy_versions SET generation=$2 WHERE org_id=$1`, p.OrgID, generation); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE loomplane_federation_deliveries SET state=jsonb_set(jsonb_set(state,'{policy_generation}',to_jsonb($2::bigint)),'{acknowledged_policy_generation}',to_jsonb($2::bigint)) WHERE org_id=$1`, p.OrgID, receipt); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := change(1, 2); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failure := make(chan error, 1)
	wg.Go(func() {
		for i := range 60 {
			if err := change(1+i%2, 2-i%2); err != nil {
				failure <- err
				return
			}
		}
	})
	for range 60 {
		observed, err := store.ReadPreparationStatus(ctx, p.OrgID, p.DestinationID, p.ID)
		if err != nil {
			wg.Wait()
			t.Fatal(err)
		}
		if observed.State != "policy_pending" {
			wg.Wait()
			t.Fatalf("mixed separately committed policy evidence: %+v", observed)
		}
	}
	wg.Wait()
	select {
	case err := <-failure:
		t.Fatal(err)
	default:
	}
}
