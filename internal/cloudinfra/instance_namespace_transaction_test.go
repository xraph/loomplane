package cloudinfra

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func namespaceActionFixture() namespaceAction {
	at := time.Date(2026, 11, 4, 12, 0, 0, 123456789, time.UTC)
	actor := security.Principal{Kind: security.KindFleet, OrgID: "org", KeyID: "key", UserID: security.FleetSubject("key"), Fleet: &security.FleetScope{ProjectID: "project", FleetID: "fleet"}}
	snapshot := fleets.InstanceSnapshot{InstanceID: "instance", FleetID: "fleet", EnvironmentID: "environment", Generation: 1, OwnershipEpoch: 1, Digest: "sha256:snapshot", Values: map[string]map[string]string{"app": {"key": "value"}}}
	authority := fleets.OperationAuthority{ID: "authority", Scope: fleets.FleetScope{OrgID: "org", ProjectID: "project", FleetID: "fleet"}, Principal: actor, Request: fleets.OperationRequest{CloudID: "cloud", SnapshotDigest: snapshot.Digest}, ExpiresAt: at.Add(time.Hour)}
	pins := InstanceNamespacePins{FleetID: "fleet", InstanceID: "instance", EnvironmentID: "environment", Generation: 1, OwnershipEpoch: 1, SnapshotDigest: snapshot.Digest, QualificationID: "qualification"}
	record := InstanceNamespaceRecord{OrgID: "org", ProjectID: "project", CloudID: "cloud", ClusterID: "cluster", OperationID: "operation", Namespace: "namespace", Pins: pins, CurrentGeneration: 1, CurrentSnapshotDigest: snapshot.Digest, CreationAuthority: authority, CurrentAuthority: authority, Snapshot: snapshot, QualificationExpiresAt: at.Add(time.Hour)}
	input := ClusterInput{Kind: InstanceNamespaceKind, Attachment: &AttachmentInput{Instance: &pins, InstancePolicy: &provider.InstanceIsolationPolicy{Revision: provider.InstanceIsolationRevision}, ProjectID: "project", Namespace: "namespace"}}
	operation := ClusterOperation{ID: "operation", OrgID: "org", CloudID: "cloud", ResourceID: "cluster", Kind: InstanceNamespaceKind, SchemaVersion: 1, Input: input, Digest: clusterDigest(input), Revision: 1, Status: "accepted", Principal: actor, CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(time.Hour)}
	return namespaceAction{Kind: "reserve", AttemptID: "attempt", AppID: "app", Record: record, Snapshot: snapshot, Operation: operation, Principal: actor}
}

func TestNamespaceActionFreezesCallerData(t *testing.T) {
	in := namespaceActionFixture()
	frozen, digest, err := freezeNamespaceAction(in)
	if err != nil {
		t.Fatal(err)
	}
	in.Principal.Fleet.FleetID = "changed"
	in.Snapshot.Values["app"]["key"] = "changed"
	in.Operation.Input.Attachment.Instance.QualificationID = "changed"
	if frozen.Principal.Fleet.FleetID != "fleet" || frozen.Snapshot.Values["app"]["key"] != "value" || frozen.Operation.Input.Attachment.Instance.QualificationID != "qualification" {
		t.Fatal("caller mutation escaped into action")
	}
	_, again, err := freezeNamespaceAction(frozen)
	if err != nil || again != digest {
		t.Fatalf("unstable binding: %s %s %v", digest, again, err)
	}
}

func TestNamespaceActionDigestBindsAuthorityAndExpectedRevisions(t *testing.T) {
	in := namespaceActionFixture()
	_, original, err := freezeNamespaceAction(in)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*namespaceAction){
		"grant revision": func(in *namespaceAction) {
			in.Record.CurrentAuthority.GrantRevision++
			in.Record.CreationAuthority = in.Record.CurrentAuthority
		},
		"stage revision": func(in *namespaceAction) {
			in.Record.CurrentAuthority.StageRevision++
			in.Record.CreationAuthority = in.Record.CurrentAuthority
		},
		"attempt": func(in *namespaceAction) { in.AttemptID = "other" },
		"authority expiry": func(in *namespaceAction) {
			in.Record.CurrentAuthority.ExpiresAt = in.Record.CurrentAuthority.ExpiresAt.Add(time.Nanosecond)
			in.Record.CreationAuthority = in.Record.CurrentAuthority
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := namespaceActionFixture()
			mutate(&x)
			_, digest, err := freezeNamespaceAction(x)
			if err != nil || digest == original {
				t.Fatalf("binding lost %s: %v", name, err)
			}
		})
	}
	in.ExpectedNamespaceRevision = 1
	if _, _, err := freezeNamespaceAction(in); err == nil {
		t.Fatal("reserve accepted stale revision")
	}
}

func TestNamespaceAttemptReconcilesExactEvidence(t *testing.T) {
	in, digest, err := freezeNamespaceAction(namespaceActionFixture())
	if err != nil {
		t.Fatal(err)
	}
	exact := in.Operation.CreatedAt
	floor, ns, err := splitNamespaceAuthorizationInstant(exact)
	if err != nil {
		t.Fatal(err)
	}
	stamp := floor.Format("2006-01-02T15:04:05.000000Z")
	result := namespaceResult{Record: in.Record, Operation: in.Operation, Principal: in.Operation.Principal, Snapshot: in.Snapshot}
	result.Operation.NamespaceAttemptID = in.AttemptID
	result.Operation.NamespaceInputDigest = digest
	result.Record.Revision = 1
	result.Record.CreationAttemptID = in.AttemptID
	result.Record.CurrentAttemptID = in.AttemptID
	inputRaw, err := json.Marshal(namespaceEvidence[namespaceAction]{AuthorizedAt: stamp, AuthorizedAtNS: ns, Value: in})
	if err != nil {
		t.Fatal(err)
	}
	resultRaw, err := json.Marshal(namespaceEvidence[namespaceResult]{AuthorizedAt: stamp, AuthorizedAtNS: ns, Value: result})
	if err != nil {
		t.Fatal(err)
	}
	if _, decodeErr := decodeNamespaceAttempt(in, digest, inputRaw, resultRaw, floor, ns); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if _, nanosecondErr := decodeNamespaceAttempt(in, digest, inputRaw, resultRaw, floor, ns-1); nanosecondErr == nil {
		t.Fatal("accepted rounded timestamp")
	}
	other := in
	other.AttemptID = "other"
	if _, principalErr := decodeNamespaceAttempt(other, digest, inputRaw, resultRaw, floor, ns); principalErr == nil {
		t.Fatal("reconciled another attempt")
	}
	result.Record.Pins.OwnershipEpoch++
	resultRaw, err = json.Marshal(namespaceEvidence[namespaceResult]{AuthorizedAt: stamp, AuthorizedAtNS: ns, Value: result})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeNamespaceAttempt(in, digest, inputRaw, resultRaw, floor, ns); err == nil {
		t.Fatal("accepted changed ownership")
	}
}

func TestNamespaceTemporalFactsUseDecisionInstant(t *testing.T) {
	in := namespaceActionFixture()
	at := in.Record.QualificationExpiresAt.Add(-time.Nanosecond)
	proof := &lockedNamespaceAction{}
	if err := proof.validateAt(in, at); err != nil {
		t.Fatal(err)
	}
	if err := proof.validateAt(in, at.Add(time.Nanosecond)); err == nil {
		t.Fatal("accepted exact expiry")
	}
	in.Kind = "checkpoint"
	proof.operation = &ClusterOperation{ExpiresAt: at.Add(time.Hour), FencingEpoch: 1, LeaseExpiresAt: &at}
	if err := proof.validateAt(in, at); err == nil {
		t.Fatal("accepted expired lease")
	}
	in.Kind = "renew"
	proof.operation.Status = "succeeded"
	if err := proof.validateAt(in, at); err != nil {
		t.Fatalf("completed operation renewal requires a worker lease: %v", err)
	}
}

func TestNamespaceReceiptCannotAuthorizeAnotherEffect(t *testing.T) {
	before := kubernetes.EnrollmentState{Attempted: map[string]bool{"Namespace": true}, Objects: map[string]string{}}
	observed := kubernetes.EnrollmentState{NamespaceUID: "namespace-uid", Attempted: map[string]bool{"Namespace": true, "Role": true}, Objects: map[string]string{}}
	next, changed, err := namespaceOutcomeAdvance(before, observed)
	if err != nil || !changed || next.Attempted["Role"] {
		t.Fatalf("receipt changed effect permission: %+v %v", next, err)
	}
	observed.Objects["Role"] = "role-uid"
	if _, _, err := namespaceOutcomeAdvance(before, observed); err == nil {
		t.Fatal("accepted effect without attempted checkpoint")
	}
	before.NamespaceUID = "old"
	if _, _, err := namespaceOutcomeAdvance(before, observed); err == nil {
		t.Fatal("replaced observed identity")
	}
}

func TestNamespaceAttemptIdentitySurvivesIntentReload(t *testing.T) {
	in := namespaceActionFixture()
	want := namespaceAttemptIdentity(in)
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded namespaceAction
	if err = json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatal(err)
	}
	reloaded.Operation.CreatedAt = reloaded.Operation.CreatedAt.Add(time.Minute)
	reloaded.Operation.UpdatedAt = reloaded.Operation.UpdatedAt.Add(time.Minute)
	if namespaceAttemptIdentity(reloaded) != want {
		t.Fatal("presentation timestamp minted another attempt")
	}
	in.AttemptID = want
	reloaded.AttemptID = want
	_, before, err := freezeNamespaceAction(in)
	if err != nil {
		t.Fatal(err)
	}
	_, after, err := freezeNamespaceAction(reloaded)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("full binding lost changed command payload")
	}
	reloaded.ExpectedOperationRevision++
	if namespaceAttemptIdentity(reloaded) == want {
		t.Fatal("new revision reused command identity")
	}
}
