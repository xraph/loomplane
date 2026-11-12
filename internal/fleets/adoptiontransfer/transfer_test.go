package adoptiontransfer

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (Intent, State, time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	in := IntentFields{SchemaVersion: 1, ID: "transfer", OperationID: "operation", OrgID: "org", ProjectID: "project", FleetID: "fleet", InstanceID: "instance", EnvironmentID: "env", Generation: 1, PriorEpoch: 4, Epoch: 5, Direction: Forward, ActorDigest: strings.Repeat("a", 64), ReviewID: "review", ReviewRevision: 2, ReviewDigest: strings.Repeat("b", 64), AuthorityID: "transfer-authority", AuthorityRevision: 3, OldAuthorityID: "old", NewAuthorityID: "new", OldWriterUID: "controller-uid", Source: Source{Provider: "provider", AccountIdentity: "account", ClusterID: "cluster", ClusterIncarnation: "cluster-incarnation", Namespace: "namespace", NamespaceUID: "namespace-uid", AttachmentID: "attachment", AttachmentRevision: 3, ConnectionID: "connection", ConnectionGeneration: 2, TrustRevision: 4}, ReleaseID: "release", ReleaseDigest: strings.Repeat("c", 64), InputDigest: strings.Repeat("d", 64), AcceptedAt: now, Deadline: now.Add(time.Hour)}
	intent, err := NewIntent(in)
	if err != nil {
		t.Fatal(err)
	}
	state, err := NewState(intent)
	if err != nil {
		t.Fatal(err)
	}
	return intent, state, now
}
func apply(t *testing.T, s State, c Command, now time.Time) State {
	t.Helper()
	c.ExpectedRevision = s.Revision()
	if c.Kind != Claim {
		c.LeaseToken = s.Lease().Token
	}
	next, err := Reduce(s, c, now)
	if err != nil {
		t.Fatalf("%s: %v", c.Kind, err)
	}
	return next
}
func claim(t *testing.T, s State, token string, now time.Time) State {
	t.Helper()
	return apply(t, s, Command{Kind: Claim, Lease: Lease{Token: token, ExpiresAt: now.Add(time.Minute)}}, now)
}
func receipt(t *testing.T, s State, step Step, outcome Outcome, now time.Time) Receipt {
	t.Helper()
	parent := ""
	parentDigest := ""
	history := s.Receipts()
	if len(history) > 0 {
		parent = history[len(history)-1].Fields().ID
		parentDigest = history[len(history)-1].Digest()
	}
	f := ReceiptFields{SchemaVersion: 1, ID: string(step) + "-" + string(outcome), IntentDigest: s.Intent().Digest(), AttemptID: s.Pending().AttemptID, Step: step, Outcome: outcome, EvidenceDigest: strings.Repeat("e", 64), ObservedAt: now, ParentReceiptID: parent, ParentReceiptDigest: parentDigest, InventoryDigest: strings.Repeat("f", 64), SnapshotDigest: strings.Repeat("1", 64), Complete: true}
	r, err := NewReceipt(f)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func step(t *testing.T, s State, k Step, now time.Time) State {
	t.Helper()
	s = apply(t, s, Command{Kind: Begin, Step: k, AttemptID: string(k) + "-attempt"}, now)
	return apply(t, s, Command{Kind: Record, Receipt: receipt(t, s, k, Confirmed, now)}, now)
}
func reject(t *testing.T, s State, c Command, now time.Time, want error) {
	t.Helper()
	c.ExpectedRevision = s.Revision()
	if c.LeaseToken == "" {
		c.LeaseToken = s.Lease().Token
	}
	next, err := Reduce(s, c, now)
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
	if !reflect.DeepEqual(next, s) {
		t.Fatal("rejected command changed state")
	}
}

func TestIntentPinsAndCopies(t *testing.T) {
	intent, _, _ := fixture(t)
	original := intent.Fields()
	changes := map[string]func(*IntentFields){"missing scope": func(f *IntentFields) { f.OrgID = "" }, "same authority": func(f *IntentFields) { f.NewAuthorityID = f.OldAuthorityID }, "epoch reuse": func(f *IntentFields) { f.Epoch = f.PriorEpoch }, "epoch jump": func(f *IntentFields) { f.Epoch += 2 }, "missing incarnation": func(f *IntentFields) { f.Source.ClusterIncarnation = "" }, "missing namespace uid": func(f *IntentFields) { f.Source.NamespaceUID = "" }, "bad digest": func(f *IntentFields) { f.ActorDigest = "caller" }, "expired": func(f *IntentFields) { f.Deadline = f.AcceptedAt }, "reverse without predecessor": func(f *IntentFields) { f.Direction = Reverse }}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f := original
			change(&f)
			if _, err := NewIntent(f); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid intent accepted: %v", err)
			}
		})
	}
	copy := intent.Fields()
	copy.Source.NamespaceUID = "changed"
	copy.ActorDigest = strings.Repeat("2", 64)
	if intent.Fields() != original {
		t.Fatal("caller mutated immutable intent")
	}
	changed, err := NewIntent(copy)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Digest() == intent.Digest() {
		t.Fatal("identity not bound into digest")
	}
	copy = original
	copy.Direction = Reverse
	copy.PredecessorID = "completed-forward"
	copy.PriorEpoch = 5
	copy.Epoch = 6
	if _, err = NewIntent(copy); err != nil {
		t.Fatal("new reverse intent rejected:", err)
	}
}
func TestEvidenceNeverEnablesTransfer(t *testing.T) {
	_, s, now := fixture(t)
	s = claim(t, s, "worker", now)
	for _, k := range []Step{Fence, Inventory, Import, Plan} {
		s = step(t, s, k, now)
	}
	if s.Phase() != EvidenceRecorded || len(s.Receipts()) != 4 {
		t.Fatalf("evidence not recorded: %s", s.Phase())
	}
	reject(t, s, Command{Kind: Activate}, now, ErrQualificationRequired)
	reject(t, s, Command{Kind: ReverseTransfer}, now, ErrQualificationRequired)
}
func TestLostFenceResultSurvivesLeaseRecoveryAndCancel(t *testing.T) {
	_, s, now := fixture(t)
	s = claim(t, s, "worker-1", now)
	s = apply(t, s, Command{Kind: Begin, Step: Fence, AttemptID: "original-attempt"}, now)
	s = apply(t, s, Command{Kind: Record, Receipt: receipt(t, s, Fence, Unknown, now)}, now)
	if !s.Pending().Unknown {
		t.Fatal("lost result not explicit")
	}
	reject(t, s, Command{Kind: Begin, Step: Fence, AttemptID: "duplicate"}, now, ErrUnresolved)
	later := now.Add(2 * time.Minute)
	s = claim(t, s, "worker-2", later)
	if s.Pending().AttemptID != "original-attempt" || !s.Pending().Unknown {
		t.Fatal("reclaim discarded original unknown outcome")
	}
	reject(t, s, Command{Kind: Record, LeaseToken: "worker-1", Receipt: receipt(t, s, Fence, Confirmed, later)}, later, ErrLease)
	s = apply(t, s, Command{Kind: Cancel}, later)
	if !s.Canceled() || !s.Pending().Unknown {
		t.Fatal("cancel erased uncertainty")
	}
	s = apply(t, s, Command{Kind: Record, Receipt: receipt(t, s, Fence, Confirmed, later)}, later)
	if s.Pending().AttemptID != "" || s.Phase() != FenceRecorded || !s.Canceled() {
		t.Fatal("late observation not preserved after cancel")
	}
	reject(t, s, Command{Kind: Begin, Step: Inventory, AttemptID: "inventory"}, later, ErrCanceled)
	reject(t, s, Command{Kind: Activate}, later, ErrQualificationRequired)
}
func TestReceiptBindingAndNoEffectRetry(t *testing.T) {
	_, s, now := fixture(t)
	s = claim(t, s, "worker", now)
	s = apply(t, s, Command{Kind: Begin, Step: Fence, AttemptID: "attempt"}, now)
	good := receipt(t, s, Fence, Confirmed, now)
	for name, change := range map[string]func(*ReceiptFields){"foreign transfer": func(f *ReceiptFields) { f.IntentDigest = strings.Repeat("9", 64) }, "wrong attempt": func(f *ReceiptFields) { f.AttemptID = "other" }, "wrong phase": func(f *ReceiptFields) { f.Step = Plan }, "future": func(f *ReceiptFields) { f.ObservedAt = now.Add(time.Second) }, "before attempt": func(f *ReceiptFields) { f.ObservedAt = now.Add(-time.Second) }, "broken chain": func(f *ReceiptFields) { f.ParentReceiptID = "other"; f.ParentReceiptDigest = strings.Repeat("9", 64) }} {
		t.Run(name, func(t *testing.T) {
			f := good.Fields()
			change(&f)
			r, err := NewReceipt(f)
			if err != nil {
				t.Fatal(err)
			}
			reject(t, s, Command{Kind: Record, Receipt: r}, now, ErrReceipt)
		})
	}
	s = apply(t, s, Command{Kind: Record, Receipt: receipt(t, s, Fence, NoEffect, now)}, now)
	if s.Phase() != Accepted || s.Pending().AttemptID != "" {
		t.Fatal("no-effect observation advanced fence")
	}
	reject(t, s, Command{Kind: Begin, Step: Fence, AttemptID: "attempt"}, now, ErrConflict)
	s = apply(t, s, Command{Kind: Begin, Step: Fence, AttemptID: "second-attempt"}, now)
	if s.Pending().AttemptID != "second-attempt" {
		t.Fatal("new attempt not recorded")
	}
}
func TestNoChangeRequiresCompleteOrderedBoundInventory(t *testing.T) {
	_, base, now := fixture(t)
	base = claim(t, base, "worker", now)
	reject(t, base, Command{Kind: Begin, Step: Plan, AttemptID: "plan"}, now, ErrTransition)
	base = step(t, base, Fence, now)
	base = step(t, base, Inventory, now)
	base = step(t, base, Import, now)
	base = apply(t, base, Command{Kind: Begin, Step: Plan, AttemptID: "plan"}, now)
	for name, change := range map[string]func(*ReceiptFields){"incomplete": func(f *ReceiptFields) { f.Complete = false }, "changes": func(f *ReceiptFields) { f.Changes = 1 }, "different inventory": func(f *ReceiptFields) { f.InventoryDigest = strings.Repeat("8", 64) }, "different snapshot": func(f *ReceiptFields) { f.SnapshotDigest = strings.Repeat("8", 64) }} {
		t.Run(name, func(t *testing.T) {
			f := receipt(t, base, Plan, Confirmed, now).Fields()
			change(&f)
			r, err := NewReceipt(f)
			if err != nil {
				t.Fatal(err)
			}
			reject(t, base, Command{Kind: Record, Receipt: r}, now, ErrReceipt)
		})
	}
}
func TestStaleLeaseRevisionAndTimeCannotAdvance(t *testing.T) {
	_, s, now := fixture(t)
	s = claim(t, s, "worker", now)
	old := s
	_, err := Reduce(s, Command{Kind: Cancel, ExpectedRevision: s.Revision() - 1, LeaseToken: "worker"}, now)
	if !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision accepted", err)
	}
	reject(t, s, Command{Kind: Begin, Step: Fence, AttemptID: "f"}, now.Add(time.Minute), ErrLease)
	reject(t, s, Command{Kind: Claim, Lease: Lease{Token: "steal", ExpiresAt: now.Add(time.Minute)}}, now, ErrLease)
	reject(t, s, Command{Kind: Cancel}, now.Add(-time.Second), ErrInvalid)
	if !reflect.DeepEqual(old, s) {
		t.Fatal("input state mutated")
	}
	s = step(t, s, Fence, now)
	s = claim(t, s, "recovery", now.Add(6*time.Minute))
	reject(t, s, Command{Kind: Begin, Step: Inventory, AttemptID: "inventory"}, now.Add(6*time.Minute), ErrStaleEvidence)
}
func TestHistoricalReceiptsAndIndependentStateCopies(t *testing.T) {
	_, s, now := fixture(t)
	s = claim(t, s, "worker", now)
	s = apply(t, s, Command{Kind: Begin, Step: Fence, AttemptID: "f"}, now)
	r := receipt(t, s, Fence, Confirmed, now)
	before := s
	s = apply(t, s, Command{Kind: Record, Receipt: r}, now)
	if len(before.Receipts()) != 0 || before.Pending().AttemptID != "f" {
		t.Fatal("reducer mutated prior snapshot")
	}
	replay := apply(t, s, Command{Kind: Record, Receipt: r}, now)
	if !reflect.DeepEqual(replay, s) {
		t.Fatal("exact receipt replay appended history")
	}
	copy := s.Receipts()
	copy[0] = Receipt{}
	if s.Receipts()[0].Fields().ID != r.Fields().ID {
		t.Fatal("returned history aliases state")
	}
	f := r.Fields()
	f.EvidenceDigest = strings.Repeat("7", 64)
	changed, _ := NewReceipt(f)
	reject(t, s, Command{Kind: Record, Receipt: changed}, now, ErrConflict)
	// Recovery can append an old observation after the accepted deadline; it cannot start more work.
	_, late, _ := fixture(t)
	late = claim(t, late, "first", now)
	late = apply(t, late, Command{Kind: Begin, Step: Fence, AttemptID: "late"}, now)
	r = receipt(t, late, Fence, Confirmed, now)
	later := now.Add(2 * time.Hour)
	late = claim(t, late, "recovery", later)
	late = apply(t, late, Command{Kind: Record, Receipt: r}, later)
	if late.Receipts()[0].Fields().ObservedAt != now {
		t.Fatal("historical evidence rewritten")
	}
	reject(t, late, Command{Kind: Begin, Step: Inventory, AttemptID: "next"}, later, ErrExpired)
}

func TestVersionedRecordsAndCanonicalDigest(t *testing.T) {
	intent, s, now := fixture(t)
	for _, version := range []int{0, 2} {
		f := intent.Fields()
		f.SchemaVersion = version
		if _, err := NewIntent(f); !errors.Is(err, ErrInvalid) {
			t.Errorf("unsupported intent schema %d accepted: %v", version, err)
		}
	}
	s = claim(t, s, "worker", now)
	s = apply(t, s, Command{Kind: Begin, Step: Fence, AttemptID: "attempt"}, now)
	r := receipt(t, s, Fence, Confirmed, now)
	for _, version := range []int{0, 2} {
		f := r.Fields()
		f.SchemaVersion = version
		if _, err := NewReceipt(f); !errors.Is(err, ErrInvalid) {
			t.Errorf("unsupported receipt schema %d accepted: %v", version, err)
		}
	}
	// A local year 1 timestamp can become year 0 in UTC, outside the supported range.
	boundary := time.Date(1, 1, 1, 1, 0, 0, 0, time.FixedZone("east", 14*60*60))
	f := intent.Fields()
	f.AcceptedAt = boundary
	f.Deadline = boundary.Add(time.Hour)
	if got, err := NewIntent(f); !errors.Is(err, ErrInvalid) {
		t.Errorf("unsupported UTC intent time accepted with digest %q: %v", got.Digest(), err)
	}
	rf := r.Fields()
	rf.ObservedAt = boundary
	if got, err := NewReceipt(rf); !errors.Is(err, ErrInvalid) {
		t.Errorf("unsupported UTC receipt time accepted with digest %q: %v", got.Digest(), err)
	}
}
func TestParentDigestAndExpiredPendingAttempt(t *testing.T) {
	_, s, now := fixture(t)
	s = claim(t, s, "one", now)
	s = step(t, s, Fence, now)
	s = apply(t, s, Command{Kind: Begin, Step: Inventory, AttemptID: "inventory-attempt"}, now)
	r := receipt(t, s, Inventory, Confirmed, now)
	f := r.Fields()
	f.ParentReceiptDigest = strings.Repeat("9", 64)
	bad, err := NewReceipt(f)
	if err != nil {
		t.Fatal(err)
	}
	reject(t, s, Command{Kind: Record, Receipt: bad}, now, ErrReceipt)
	later := now.Add(2 * time.Minute)
	s = claim(t, s, "two", later)
	if !s.Pending().Unknown || s.Pending().AttemptID != "inventory-attempt" {
		t.Fatal("expired pending attempt treated as no effect")
	}
	reject(t, s, Command{Kind: Claim, Lease: Lease{Token: "one", ExpiresAt: later.Add(3 * time.Minute)}}, later.Add(2*time.Minute), ErrLease)
}

func TestInvalidUTF8CannotAliasTransferIdentity(t *testing.T) {
	intent, s, now := fixture(t)
	original := intent.Fields()
	a, b := original, original
	a.Source.NamespaceUID = "namespace-" + string([]byte{0xff})
	b.Source.NamespaceUID = "namespace-" + string([]byte{0xfe})
	// Show why accepting either byte string is unsafe: JSON canonicalization loses the distinction.
	if a.Source.NamespaceUID == b.Source.NamespaceUID || hash("fleet-adoption-intent/v1", a) != hash("fleet-adoption-intent/v1", b) {
		t.Fatal("fixture does not reproduce JSON identity collapse")
	}
	for _, f := range []IntentFields{a, b} {
		if _, err := NewIntent(f); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid UTF-8 namespace identity accepted: %v", err)
		}
	}
	// Valid Unicode must remain supported and distinct from other valid identities.
	a.Source.NamespaceUID = "namespace-\uFFFD"
	b.Source.NamespaceUID = "namespace-é"
	ai, err := NewIntent(a)
	if err != nil {
		t.Fatal(err)
	}
	bi, err := NewIntent(b)
	if err != nil {
		t.Fatal(err)
	}
	if ai.Digest() == bi.Digest() {
		t.Fatal("distinct valid Unicode identities aliased")
	}
	reject(t, s, Command{Kind: Claim, Lease: Lease{Token: string([]byte{0xff}), ExpiresAt: now.Add(time.Minute)}}, now, ErrLease)
	s = claim(t, s, "worker", now)
	reject(t, s, Command{Kind: Begin, Step: Fence, AttemptID: string([]byte{0xff})}, now, ErrInvalid)
	s = apply(t, s, Command{Kind: Begin, Step: Fence, AttemptID: "attempt"}, now)
	f := receipt(t, s, Fence, Confirmed, now).Fields()
	f.ID = string([]byte{0xff})
	if _, err = NewReceipt(f); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid UTF-8 receipt ID accepted: %v", err)
	}
}

func TestReducerRejectsCanonicalUTCOverflowWithoutMutation(t *testing.T) {
	_, s, _ := fixture(t)
	west := time.FixedZone("west", -60*60)
	t.Run("clock", func(t *testing.T) {
		now := time.Date(9999, 12, 31, 23, 0, 0, 0, west)
		if now.Year() != 9999 || now.UTC().Year() != 10000 {
			t.Fatal("bad boundary fixture")
		}
		reject(t, s, Command{Kind: Claim, Lease: Lease{Token: "worker", ExpiresAt: now.Add(time.Minute)}}, now, ErrInvalid)
	})
	t.Run("expiry", func(t *testing.T) {
		now := time.Date(9999, 12, 31, 23, 59, 0, 0, time.UTC)
		expiry := now.Add(2 * time.Minute).In(west)
		if expiry.Year() != 9999 || expiry.UTC().Year() != 10000 {
			t.Fatal("bad expiry fixture")
		}
		reject(t, s, Command{Kind: Claim, Lease: Lease{Token: "worker", ExpiresAt: expiry}}, now, ErrLease)
	})
	t.Run("valid_offset", func(t *testing.T) {
		now := time.Date(9999, 12, 31, 23, 57, 0, 0, time.UTC).In(west)
		next := claim(t, s, "worker", now)
		if next.Lease().ExpiresAt.Location() != time.UTC || next.lastAt.Location() != time.UTC || next.Lease().ExpiresAt.Year() != 9999 {
			t.Fatal("valid times not stored canonically")
		}
	})
}
