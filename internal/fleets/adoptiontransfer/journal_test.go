package adoptiontransfer

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestJournalRestartPreservesUnknownAttemptAndCancellation(t *testing.T) {
	intent, _, now := fixture(t)
	j, err := NewJournal(intent)
	if err != nil {
		t.Fatal(err)
	}
	commands := []Command{
		{Kind: Claim, ExpectedRevision: 1, Lease: Lease{Token: "worker", ExpiresAt: now.Add(time.Minute)}},
		{Kind: Begin, ExpectedRevision: 2, LeaseToken: "worker", Step: Fence, AttemptID: "original"},
		{Kind: Cancel, ExpectedRevision: 3, LeaseToken: "worker"},
	}
	for _, c := range commands {
		j, err = j.Apply(c, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := j.Encode()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeJournal(raw)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restored.Apply(Command{Kind: Claim, ExpectedRevision: 4, Lease: Lease{Token: "replacement", ExpiresAt: now.Add(3 * time.Minute)}}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	state := recovered.State()
	if !state.Canceled() || !state.Pending().Unknown || state.Pending().AttemptID != "original" || state.Intent().Digest() != intent.Digest() {
		t.Fatal("restart discarded unresolved intent")
	}
	if _, err = recovered.Apply(Command{Kind: Activate, ExpectedRevision: state.Revision()}, now.Add(2*time.Minute)); !errors.Is(err, ErrQualificationRequired) {
		t.Fatal(err)
	}
}

func TestJournalRejectsTamperedHistory(t *testing.T) {
	intent, _, now := fixture(t)
	j, _ := NewJournal(intent)
	j, _ = j.Apply(Command{Kind: Claim, ExpectedRevision: 1, Lease: Lease{Token: "worker", ExpiresAt: now.Add(time.Minute)}}, now)
	raw, _ := j.Encode()
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { v["version"] = 2 },
		func(v map[string]any) { v["digest"] = "changed" },
		func(v map[string]any) { v["extra"] = true },
		func(v map[string]any) {
			v["entries"].([]any)[0].(map[string]any)["Command"].(map[string]any)["Step"] = "injected"
		},
		func(v map[string]any) { v["entries"] = []any{} },
		func(v map[string]any) { v["intent"].(map[string]any)["ActorDigest"] = "changed" },
	} {
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		mutate(v)
		corrupted, _ := json.Marshal(v)
		if _, err := DecodeJournal(corrupted); err == nil {
			t.Fatal("accepted corrupted history")
		}
	}
	if _, err := DecodeJournal(append(raw, []byte("{}")...)); err == nil {
		t.Fatal("accepted trailing document")
	}
}

func TestJournalReceiptRoundTripAndIsolation(t *testing.T) {
	intent, _, now := fixture(t)
	j, _ := NewJournal(intent)
	j, _ = j.Apply(Command{Kind: Claim, ExpectedRevision: 1, Lease: Lease{Token: "worker", ExpiresAt: now.Add(time.Minute)}}, now)
	j, _ = j.Apply(Command{Kind: Begin, ExpectedRevision: 2, LeaseToken: "worker", Step: Fence, AttemptID: "fence"}, now)
	r := receipt(t, j.State(), Fence, Unknown, now)
	next, err := j.Apply(Command{Kind: Record, ExpectedRevision: 3, LeaseToken: "worker", Receipt: r}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.State().Receipts()) != 0 {
		t.Fatal("append changed original journal")
	}
	raw, _ := next.Encode()
	restored, err := DecodeJournal(raw)
	if err != nil || !reflect.DeepEqual(next.State(), restored.State()) {
		t.Fatalf("round trip: %v", err)
	}
	if restored.State().Receipts()[0].Digest() != r.Digest() {
		t.Fatal("receipt digest lost")
	}
}

func TestSourceClaimIgnoresFleetAndRotatableAttachment(t *testing.T) {
	intent, _, _ := fixture(t)
	f := intent.Fields()
	key := SourceKey(f.Source)
	f.FleetID = "other-fleet"
	f.Source.AttachmentID = "new-attachment"
	f.Source.ConnectionID = "new-connection"
	f.Source.AttachmentRevision++
	f.Source.ConnectionGeneration++
	if SourceKey(f.Source) != key {
		t.Fatal("rotated connection bypasses physical-source exclusion")
	}
	f.Source.NamespaceUID = "recreated"
	if SourceKey(f.Source) == key {
		t.Fatal("new incarnation shares old source identity")
	}
}

func TestJournalRecoveryAfterLongUnknownHistory(t *testing.T) {
	intent, _, now := fixture(t)
	j, _ := NewJournal(intent)
	var err error
	j, err = j.Apply(Command{Kind: Claim, ExpectedRevision: 1, Lease: Lease{Token: "initial", ExpiresAt: now.Add(time.Minute)}}, now)
	if err != nil {
		t.Fatal(err)
	}
	j, err = j.Apply(Command{Kind: Begin, ExpectedRevision: 2, LeaseToken: "initial", Step: Fence, AttemptID: "original"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 1030; n++ {
		now = now.Add(2 * time.Minute)
		j, err = j.Apply(Command{Kind: Claim, ExpectedRevision: j.State().Revision(), Lease: Lease{Token: fmt.Sprintf("worker-%d", n), ExpiresAt: now.Add(time.Minute)}}, now)
		if err != nil {
			t.Fatalf("recovery claim %d: %v", n, err)
		}
	}
	j, err = j.Apply(Command{Kind: Cancel, ExpectedRevision: j.State().Revision(), LeaseToken: j.State().Lease().Token}, now)
	if err != nil {
		t.Fatal(err)
	}
	r := receipt(t, j.State(), Fence, Confirmed, now)
	j, err = j.Apply(Command{Kind: Record, ExpectedRevision: j.State().Revision(), LeaseToken: j.State().Lease().Token, Receipt: r}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := j.Encode()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeJournal(raw)
	if err != nil || restored.State().Pending().AttemptID != "" || !restored.State().Canceled() {
		t.Fatal("late result lost", err)
	}
}

func TestJournalDoesNotPersistIrrelevantCommandFields(t *testing.T) {
	intent, _, now := fixture(t)
	j, _ := NewJournal(intent)
	next, err := j.Apply(Command{Kind: Claim, ExpectedRevision: 1, Lease: Lease{Token: "worker", ExpiresAt: now.Add(time.Minute)}, Step: Step(strings.Repeat("x", 5<<20)), AttemptID: "irrelevant"}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := next.Encode()
	if err != nil || len(raw) > 10000 {
		t.Fatalf("unused fields entered durable history: bytes=%d error=%v", len(raw), err)
	}
	restored, err := DecodeJournal(raw)
	if err != nil || restored.State().Lease().Token != "worker" {
		t.Fatal(err)
	}
}
