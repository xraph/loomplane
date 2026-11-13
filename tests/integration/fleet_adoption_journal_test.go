//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/fleets/adoptiontransfer"
)

type transferAcceptance struct{ intent adoptiontransfer.Intent }

func (a transferAcceptance) ReadTransferAcceptance(_ context.Context, org, id string) (adoptiontransfer.Intent, error) {
	if a.intent.Fields().OrgID != org || a.intent.Fields().ID != id {
		return adoptiontransfer.Intent{}, adoptiontransfer.ErrNotFound
	}
	return a.intent, nil
}
func transferIntent(t *testing.T, f fleetFixture, id string) adoptiontransfer.Intent {
	t.Helper()
	now := time.Now().UTC().Add(-time.Second)
	fields := adoptiontransfer.IntentFields{SchemaVersion: 1, ID: id, OperationID: "operation-" + id, OrgID: f.scope.OrgID, ProjectID: f.scope.ProjectID, FleetID: f.scope.FleetID, InstanceID: "reserved-instance-" + id, EnvironmentID: "reserved-environment-" + id, Generation: 1, PriorEpoch: 3, Epoch: 4, Direction: adoptiontransfer.Forward, ActorDigest: strings.Repeat("a", 64), ReviewID: "review-" + id, ReviewDigest: strings.Repeat("b", 64), ReviewRevision: 1, AuthorityID: "reviewed-transfer", AuthorityRevision: 1, OldAuthorityID: "old", NewAuthorityID: "new", OldWriterUID: "old-controller", Source: adoptiontransfer.Source{Provider: "provider", AccountIdentity: "account", ClusterID: "cluster", ClusterIncarnation: "cluster-uid", Namespace: "namespace", NamespaceUID: "namespace-uid", AttachmentID: "attachment", ConnectionID: "connection", AttachmentRevision: 1, ConnectionGeneration: 1, TrustRevision: 1}, ReleaseID: "release", ReleaseDigest: strings.Repeat("c", 64), InputDigest: strings.Repeat("d", 64), AcceptedAt: now, Deadline: now.Add(time.Hour)}
	intent, err := adoptiontransfer.NewIntent(fields)
	if err != nil {
		t.Fatal(err)
	}
	return intent
}
func TestFleetAdoptionJournalReplayAndImmutableHistory(t *testing.T) {
	f := newFleetFixture(t, 2)
	intent := transferIntent(t, f, "transfer")
	store := adoptiontransfer.NewPgStore(f.db, transferAcceptance{intent}, nil)
	first, err := store.Accept(t.Context(), f.scope.OrgID, intent.Fields().ID)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.Accept(t.Context(), f.scope.OrgID, intent.Fields().ID)
	if err != nil || first.State().Intent().Digest() != replay.State().Intent().Digest() {
		t.Fatal("replay", err)
	}
	fields := intent.Fields()
	fields.ActorDigest = strings.Repeat("e", 64)
	changed, _ := adoptiontransfer.NewIntent(fields)
	if _, err = adoptiontransfer.NewPgStore(f.db, transferAcceptance{changed}, nil).Accept(t.Context(), f.scope.OrgID, fields.ID); !errors.Is(err, adoptiontransfer.ErrConflict) {
		t.Fatal("changed actor", err)
	}
	if _, err = store.Get(t.Context(), "other", fields.ID); !errors.Is(err, adoptiontransfer.ErrNotFound) {
		t.Fatal("foreign scope", err)
	}
	claimed, err := store.Apply(t.Context(), f.scope.OrgID, fields.ID, adoptiontransfer.Command{Kind: adoptiontransfer.Claim, ExpectedRevision: 1, Lease: adoptiontransfer.Lease{Token: "worker", ExpiresAt: time.Now().UTC().Add(time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	if claimed.State().Revision() != 2 {
		t.Fatal("revision")
	}
	for _, query := range []string{
		`UPDATE loomplane_fleet_transfer_journals SET intent_digest=repeat('f',64) WHERE id='transfer'`,
		`DELETE FROM loomplane_fleet_transfer_journals WHERE id='transfer'`,
		`UPDATE loomplane_fleet_transfer_entries SET payload='{}' WHERE transfer_id='transfer'`,
		`DELETE FROM loomplane_fleet_transfer_entries WHERE transfer_id='transfer'`,
	} {
		if _, err = f.db.Exec(t.Context(), query); err == nil {
			t.Fatal("history mutation accepted")
		}
	}
	restarted := adoptiontransfer.NewPgStore(f.db, nil, nil)
	loaded, err := restarted.Get(t.Context(), f.scope.OrgID, fields.ID)
	if err != nil || loaded.State().Revision() != 2 || loaded.State().Lease().Token != "worker" {
		t.Fatal("restart", err)
	}
}
func TestFleetAdoptionJournalSourceExclusionAndCAS(t *testing.T) {
	f := newFleetFixture(t, 2)
	intent := transferIntent(t, f, "one")
	store := adoptiontransfer.NewPgStore(f.db, transferAcceptance{intent}, nil)
	if _, err := store.Accept(t.Context(), f.scope.OrgID, "one"); err != nil {
		t.Fatal(err)
	}
	other := transferIntent(t, f, "two")
	fields := other.Fields()
	fields.Source.ConnectionID = "rotated"
	if _, err := f.db.Exec(t.Context(), `INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('second-project','org','Second','second','ready','usr',now(),now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(t.Context(), `INSERT INTO loomplane_fleets(id,org_id,project_id,revision,status,created_at,updated_at) VALUES('second-fleet','org','second-project',1,'active',now(),now())`); err != nil {
		t.Fatal(err)
	}
	fields.ProjectID = "second-project"
	fields.FleetID = "second-fleet"
	other, _ = adoptiontransfer.NewIntent(fields)
	if _, err := adoptiontransfer.NewPgStore(f.db, transferAcceptance{other}, nil).Accept(t.Context(), f.scope.OrgID, "two"); !errors.Is(err, adoptiontransfer.ErrConflict) {
		t.Fatal("source exclusion", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, token := range []string{"worker-a", "worker-b"} {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			_, err := store.Apply(t.Context(), f.scope.OrgID, "one", adoptiontransfer.Command{Kind: adoptiontransfer.Claim, ExpectedRevision: 1, Lease: adoptiontransfer.Lease{Token: token, ExpiresAt: time.Now().UTC().Add(time.Minute)}})
			results <- err
		}(token)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, adoptiontransfer.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", successes, conflicts)
	}
}
func TestFleetAdoptionJournalCancellationRetainsSource(t *testing.T) {
	f := newFleetFixture(t, 2)
	intent := transferIntent(t, f, "transfer")
	store := adoptiontransfer.NewPgStore(f.db, transferAcceptance{intent}, nil)
	if _, err := store.Accept(t.Context(), f.scope.OrgID, "transfer"); err != nil {
		t.Fatal(err)
	}
	commands := []adoptiontransfer.Command{
		{Kind: adoptiontransfer.Claim, ExpectedRevision: 1, Lease: adoptiontransfer.Lease{Token: "worker", ExpiresAt: time.Now().UTC().Add(time.Minute)}},
		{Kind: adoptiontransfer.Begin, ExpectedRevision: 2, LeaseToken: "worker", Step: adoptiontransfer.Fence, AttemptID: "original"},
		{Kind: adoptiontransfer.Cancel, ExpectedRevision: 3, LeaseToken: "worker"},
	}
	for _, command := range commands {
		if _, err := store.Apply(t.Context(), f.scope.OrgID, "transfer", command); err != nil {
			t.Fatal(err)
		}
	}
	recovered, err := adoptiontransfer.NewPgStore(f.db, nil, nil).Get(t.Context(), f.scope.OrgID, "transfer")
	if err != nil || !recovered.State().Canceled() || recovered.State().Pending().AttemptID != "original" {
		t.Fatal("lost unresolved cancellation", err)
	}
	for _, kind := range []adoptiontransfer.CommandKind{adoptiontransfer.Activate, adoptiontransfer.ReverseTransfer} {
		if _, err = store.Apply(t.Context(), f.scope.OrgID, "transfer", adoptiontransfer.Command{Kind: kind}); !errors.Is(err, adoptiontransfer.ErrQualificationRequired) {
			t.Fatal(err)
		}
	}
	if _, err = store.Record(t.Context(), f.scope.OrgID, "transfer", 4, "worker", "invented-proof"); !errors.Is(err, adoptiontransfer.ErrQualificationRequired) {
		t.Fatal("untrusted evidence", err)
	}
	other := transferIntent(t, f, "other")
	if _, err = adoptiontransfer.NewPgStore(f.db, transferAcceptance{other}, nil).Accept(t.Context(), f.scope.OrgID, "other"); !errors.Is(err, adoptiontransfer.ErrConflict) {
		t.Fatal("cancel released claim", err)
	}
}

type transferProofs map[string]adoptiontransfer.Receipt

func (p transferProofs) ReadTransferReceipt(_ context.Context, _ adoptiontransfer.Intent, id string) (adoptiontransfer.Receipt, error) {
	receipt, ok := p[id]
	if !ok {
		return adoptiontransfer.Receipt{}, adoptiontransfer.ErrNotFound
	}
	return receipt, nil
}
func TestFleetAdoptionJournalTrustedReceiptContinuity(t *testing.T) {
	f := newFleetFixture(t, 2)
	intent := transferIntent(t, f, "transfer")
	proofs := transferProofs{}
	store := adoptiontransfer.NewPgStore(f.db, transferAcceptance{intent}, proofs)
	state, err := store.Accept(t.Context(), f.scope.OrgID, "transfer")
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.Apply(t.Context(), f.scope.OrgID, "transfer", adoptiontransfer.Command{Kind: adoptiontransfer.Claim, ExpectedRevision: state.State().Revision(), Lease: adoptiontransfer.Lease{Token: "worker", ExpiresAt: time.Now().UTC().Add(5*time.Minute - time.Second)}})
	if err != nil {
		t.Fatal(err)
	}
	var lastReceipt adoptiontransfer.Receipt
	for _, step := range []adoptiontransfer.Step{adoptiontransfer.Fence, adoptiontransfer.Inventory, adoptiontransfer.Import, adoptiontransfer.Plan} {
		state, err = store.Apply(t.Context(), f.scope.OrgID, "transfer", adoptiontransfer.Command{Kind: adoptiontransfer.Begin, ExpectedRevision: state.State().Revision(), LeaseToken: "worker", Step: step, AttemptID: string(step) + "-attempt"})
		if err != nil {
			t.Fatal(err)
		}
		fields := adoptiontransfer.ReceiptFields{SchemaVersion: 1, ID: string(step) + "-receipt", IntentDigest: intent.Digest(), AttemptID: state.State().Pending().AttemptID, Step: step, Outcome: adoptiontransfer.Confirmed, EvidenceDigest: strings.Repeat("e", 64), ObservedAt: time.Now().UTC(), Complete: true, InventoryDigest: strings.Repeat("f", 64), SnapshotDigest: strings.Repeat("1", 64)}
		if lastReceipt.Digest() != "" {
			fields.ParentReceiptID = lastReceipt.Fields().ID
			fields.ParentReceiptDigest = lastReceipt.Digest()
		}
		receipt, receiptErr := adoptiontransfer.NewReceipt(fields)
		if receiptErr != nil {
			t.Fatal(receiptErr)
		}
		proofs[fields.ID] = receipt
		if _, err = store.Apply(t.Context(), f.scope.OrgID, "transfer", adoptiontransfer.Command{Kind: adoptiontransfer.Record, ExpectedRevision: state.State().Revision(), LeaseToken: "worker", Receipt: receipt}); !errors.Is(err, adoptiontransfer.ErrReceipt) {
			t.Fatal("request supplied receipt", err)
		}
		state, err = store.Record(t.Context(), f.scope.OrgID, "transfer", state.State().Revision(), "worker", fields.ID)
		if err != nil {
			t.Fatal(err)
		}
		replay, replayErr := store.Record(t.Context(), f.scope.OrgID, "transfer", state.State().Revision(), "worker", fields.ID)
		if replayErr != nil || replay.State().Revision() != state.State().Revision() {
			t.Fatal("receipt replay", replayErr)
		}
		lastReceipt = receipt
	}
	restored, err := adoptiontransfer.NewPgStore(f.db, nil, nil).Get(t.Context(), f.scope.OrgID, "transfer")
	if err != nil || restored.State().Phase() != adoptiontransfer.EvidenceRecorded || len(restored.State().Receipts()) != 4 {
		t.Fatal("proof continuity", err)
	}
	if _, err = store.Apply(t.Context(), f.scope.OrgID, "transfer", adoptiontransfer.Command{Kind: adoptiontransfer.Activate}); !errors.Is(err, adoptiontransfer.ErrQualificationRequired) {
		t.Fatal("journal activated ownership", err)
	}
}

func TestFleetAdoptionJournalPagedRecovery(t *testing.T) {
	f := newFleetFixture(t, 2)
	intent := transferIntent(t, f, "transfer")
	proofs := transferProofs{}
	store := adoptiontransfer.NewPgStore(f.db, transferAcceptance{intent}, proofs)
	state, err := store.Accept(t.Context(), f.scope.OrgID, "transfer")
	if err != nil {
		t.Fatal(err)
	}
	if got := state.State().Revision(); got != 1 {
		t.Fatalf("accepted transfer at revision %d, want 1", got)
	}
	state, err = store.Apply(t.Context(), f.scope.OrgID, "transfer", adoptiontransfer.Command{Kind: adoptiontransfer.Claim, ExpectedRevision: 1, Lease: adoptiontransfer.Lease{Token: "worker", ExpiresAt: time.Now().UTC().Add(4 * time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.State().Revision(); got != 2 {
		t.Fatalf("claimed transfer at revision %d, want 2", got)
	}
	state, err = store.Apply(t.Context(), f.scope.OrgID, "transfer", adoptiontransfer.Command{Kind: adoptiontransfer.Begin, ExpectedRevision: 2, LeaseToken: "worker", Step: adoptiontransfer.Fence, AttemptID: "original"})
	if err != nil {
		t.Fatal(err)
	}
	var parent adoptiontransfer.Receipt
	for n := 0; n < 130; n++ {
		fields := adoptiontransfer.ReceiptFields{SchemaVersion: 1, ID: fmt.Sprintf("receipt-%d", n), IntentDigest: intent.Digest(), AttemptID: "original", Step: adoptiontransfer.Fence, Outcome: adoptiontransfer.Unknown, EvidenceDigest: strings.Repeat("e", 64), ObservedAt: time.Now().UTC()}
		if n > 0 {
			fields.ParentReceiptID = parent.Fields().ID
			fields.ParentReceiptDigest = parent.Digest()
		}
		if n == 129 {
			state, err = store.Apply(t.Context(), f.scope.OrgID, "transfer", adoptiontransfer.Command{Kind: adoptiontransfer.Cancel, ExpectedRevision: state.State().Revision(), LeaseToken: "worker"})
			if err != nil {
				t.Fatal(err)
			}
			fields.Outcome = adoptiontransfer.Confirmed
			fields.Complete = true
		}
		proof, proofErr := adoptiontransfer.NewReceipt(fields)
		if proofErr != nil {
			t.Fatal(proofErr)
		}
		proofs[fields.ID] = proof
		state, err = store.Record(t.Context(), f.scope.OrgID, "transfer", state.State().Revision(), "worker", fields.ID)
		if err != nil {
			t.Fatal(err)
		}
		parent = proof
	}
	restored, err := adoptiontransfer.NewPgStore(f.db, nil, nil).Get(t.Context(), f.scope.OrgID, "transfer")
	if err != nil || !restored.State().Canceled() || restored.State().Pending().AttemptID != "" || len(restored.State().Receipts()) != 130 {
		t.Fatal("paged recovery", err)
	}
}
