package access

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
)

func snapshotFixture(t *testing.T) (*SnapshotSigner, *SnapshotTrust, domain.Snapshot, domain.AgentIdentity, time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	scope := domain.Scope{OrgID: "o", ProjectID: "p", EnvironmentID: "e"}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSnapshotSigner(scope, private, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	trust, err := NewSnapshotTrust(scope, 1, []SnapshotKey{signer.Key()})
	if err != nil {
		t.Fatal(err)
	}
	agent := domain.AgentIdentity{Scope: scope, DestinationID: "destination", AgentID: "agent", Epoch: 2, URI: "spiffe://example.test/loomplane/org/o/project/p/environment/e/destination/destination/agent/agent/epoch/2"}
	snapshot := domain.Snapshot{Scope: scope, DestinationID: agent.DestinationID, Epoch: agent.Epoch, Sequence: 3, Revision: "revision", Full: true, IssuedAt: now, ExpiresAt: now.Add(time.Minute), Policy: domain.PolicyBundle{Scope: scope, Generation: 1, Revision: "policy", ExpiresAt: now.Add(time.Minute), Grants: []domain.AccessGrant{{Metadata: domain.Metadata{Scope: scope, ID: "grant", Generation: 1}, ExportID: "export", Source: domain.WorkloadSelector{Scope: scope, DestinationID: "source", ServiceID: "caller"}, Port: 443, Protocol: domain.HTTP, HTTPMethods: []string{"GET"}}}}, Endpoints: []domain.Endpoint{{Metadata: domain.Metadata{Scope: scope, ID: "endpoint", Generation: 1}, Address: "10.20.0.2", Port: 443}}}
	return signer, trust, snapshot, agent, now
}
func TestSnapshotSignatureCoversPayload(t *testing.T) {
	signer, trust, snapshot, agent, now := snapshotFixture(t)
	signed, err := signer.Sign(snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	var transported domain.Snapshot
	if err = json.Unmarshal(raw, &transported); err != nil {
		t.Fatal(err)
	}
	verified, err := trust.Verify(transported, agent, now)
	if err != nil {
		t.Fatal(err)
	}
	view, err := verified.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	view.Endpoints[0].Address = "10.20.0.99"
	next, err := verified.Snapshot()
	if err != nil || next.Endpoints[0].Address != "10.20.0.2" {
		t.Fatal("caller mutated verified state")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*domain.Snapshot)
	}{
		{"endpoint", func(s *domain.Snapshot) { s.Endpoints[0].Address = "10.20.0.99" }},
		{"grant", func(s *domain.Snapshot) { s.Policy.Grants[0].HTTPMethods = []string{"POST"} }},
		{"policy revision", func(s *domain.Snapshot) { s.Policy.Revision = "other" }},
		{"epoch", func(s *domain.Snapshot) { s.Epoch++ }},
		{"sequence", func(s *domain.Snapshot) { s.Sequence++ }},
		{"revision", func(s *domain.Snapshot) { s.Revision = "other" }},
		{"expiry", func(s *domain.Snapshot) { s.ExpiresAt = s.ExpiresAt.Add(time.Second) }},
		{"signature", func(s *domain.Snapshot) { s.Signature[0] ^= 1 }},
		{"key", func(s *domain.Snapshot) { s.KeyID = "other" }},
		{"tombstone", func(s *domain.Snapshot) {
			s.Tombstones = []domain.EndpointTombstone{{Metadata: domain.Metadata{ID: "removed"}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var changed domain.Snapshot
			if err := json.Unmarshal(raw, &changed); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&changed)
			if _, err := trust.Verify(changed, agent, now); err == nil {
				t.Fatal("tampered snapshot verified")
			}
		})
	}
}
func TestSnapshotScopeEpochAndTimeBounds(t *testing.T) {
	signer, trust, snapshot, agent, now := snapshotFixture(t)
	signed, err := signer.Sign(snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*domain.AgentIdentity)
	}{
		{"organization", func(a *domain.AgentIdentity) { a.OrgID = "other" }},
		{"project", func(a *domain.AgentIdentity) { a.ProjectID = "other" }},
		{"environment", func(a *domain.AgentIdentity) { a.EnvironmentID = "other" }},
		{"destination", func(a *domain.AgentIdentity) { a.DestinationID = "other" }},
		{"epoch", func(a *domain.AgentIdentity) { a.Epoch++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := agent
			tc.mutate(&other)
			if _, err := trust.Verify(signed, other, now); err == nil {
				t.Fatal("foreign snapshot verified")
			}
		})
	}
	if _, err := trust.Verify(signed, agent, signed.ExpiresAt); err == nil {
		t.Fatal("expired snapshot verified")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*domain.Snapshot)
	}{
		{"partial", func(s *domain.Snapshot) { s.Full = false }},
		{"cursor", func(s *domain.Snapshot) { s.NextCursor = "next" }},
		{"long lease", func(s *domain.Snapshot) { s.ExpiresAt = now.Add(5*time.Minute + time.Nanosecond) }},
		{"future", func(s *domain.Snapshot) { s.IssuedAt = now.Add(domain.MaxClockSkew + time.Second) }},
		{"zero sequence", func(s *domain.Snapshot) { s.Sequence = 0 }},
		{"missing revision", func(s *domain.Snapshot) { s.Revision = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := snapshot
			tc.mutate(&changed)
			if _, err := signer.Sign(changed, now); err == nil {
				t.Fatal("invalid snapshot signed")
			}
		})
	}
}
func TestSnapshotKeyRotationClosesRemovedKeys(t *testing.T) {
	signer, trust, snapshot, agent, now := snapshotFixture(t)
	signed, err := signer.Sign(snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := trust.Verify(signed, agent, now)
	if err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := NewSnapshotSigner(agent.Scope, private, now.Add(-time.Minute), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err = trust.Replace(agent.Scope, 2, []SnapshotKey{signer.Key(), replacement.Key()}); err != nil {
		t.Fatal(err)
	}
	if err = verified.ValidAt(now); err != nil {
		t.Fatalf("overlap revoked retained key: %v", err)
	}
	if err = trust.Replace(agent.Scope, 3, []SnapshotKey{replacement.Key()}); err != nil {
		t.Fatal(err)
	}
	if err = verified.ValidAt(now); err == nil {
		t.Fatal("removed key retained authority")
	}
	if err = trust.Replace(agent.Scope, 2, []SnapshotKey{signer.Key()}); err == nil {
		t.Fatal("trust rollback accepted")
	}
	if _, err = trust.Verify(signed, agent, now); err == nil {
		t.Fatal("removed key accepted again")
	}
	fresh, err := replacement.Sign(snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = trust.Verify(fresh, agent, now); err != nil {
		t.Fatal(err)
	}
	// An invalid newer update closes stale trust and fences replay of older sets.
	if err = trust.Replace(agent.Scope, 4, []SnapshotKey{{PublicKey: []byte("invalid")}}); err == nil {
		t.Fatal("invalid trust accepted")
	}
	if _, err = trust.Verify(fresh, agent, now); err == nil {
		t.Fatal("invalid update kept stale trust")
	}
	if err = trust.Replace(agent.Scope, 3, []SnapshotKey{replacement.Key()}); err == nil {
		t.Fatal("invalid update rolled back generation")
	}
}

func TestSnapshotSigningRejectsForeignRecordScopes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*domain.Snapshot)
	}{
		{"endpoint", func(s *domain.Snapshot) { s.Endpoints[0].OrgID = "foreign" }},
		{"grant", func(s *domain.Snapshot) { s.Policy.Grants[0].OrgID = "foreign" }},
		{"source", func(s *domain.Snapshot) { s.Policy.Grants[0].Source.OrgID = "foreign" }},
		{"tombstone", func(s *domain.Snapshot) {
			s.Tombstones = []domain.EndpointTombstone{{Metadata: domain.Metadata{Scope: domain.Scope{OrgID: "foreign", ProjectID: "p", EnvironmentID: "e"}}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signer, _, snapshot, _, now := snapshotFixture(t)
			tc.mutate(&snapshot)
			if _, err := signer.Sign(snapshot, now); err == nil {
				t.Fatal("foreign record scope was signed")
			}
		})
	}
}
func TestSnapshotLeaseClosesConservatively(t *testing.T) {
	signer, trust, snapshot, agent, now := snapshotFixture(t)
	snapshot.ExpiresAt = now.Add(domain.MaxPolicyLease)
	snapshot.Policy.ExpiresAt = snapshot.ExpiresAt
	signed, err := signer.Sign(snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := trust.Verify(signed, agent, now)
	if err != nil {
		t.Fatal(err)
	}
	if !verified.Deadline().Equal(snapshot.ExpiresAt.Add(-domain.MaxClockSkew)) {
		t.Fatal("clock uncertainty extended lease")
	}
	if err = verified.ValidAt(verified.Deadline()); err == nil {
		t.Fatal("snapshot remained valid at deadline")
	}
	if err = verified.ValidAt(now.Add(-domain.MaxClockSkew - time.Nanosecond)); err == nil {
		t.Fatal("wall clock rollback accepted")
	}
	shortened := signer.Key()
	shortened.NotAfter = now.Add(20 * time.Second)
	if err = trust.Replace(agent.Scope, 2, []SnapshotKey{shortened}); err != nil {
		t.Fatal(err)
	}
	if err = verified.ValidAt(now.Add(15 * time.Second)); err == nil {
		t.Fatal("shortened key lifetime ignored")
	}
}
func TestSnapshotTrustOwnsKeyMaterial(t *testing.T) {
	signer, trust, snapshot, agent, now := snapshotFixture(t)
	key := signer.Key()
	if err := trust.Replace(agent.Scope, 2, []SnapshotKey{key}); err != nil {
		t.Fatal(err)
	}
	key.PublicKey[0] ^= 1
	signed, err := signer.Sign(snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = trust.Verify(signed, agent, now); err != nil {
		t.Fatal("caller mutated pinned trust")
	}
	foreign := agent.Scope
	foreign.OrgID = "other"
	if err = trust.Replace(foreign, 3, nil); err == nil {
		t.Fatal("foreign trust update accepted")
	}
	if _, err = trust.Verify(signed, agent, now); err != nil {
		t.Fatal("foreign update removed local trust")
	}
	if err = trust.Replace(agent.Scope, 3, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = trust.Verify(signed, agent, now); err == nil {
		t.Fatal("empty trust permitted a signature")
	}
	var zero VerifiedSnapshot
	if err = zero.ValidAt(now); err == nil {
		t.Fatal("zero proof authorized")
	}
	if _, err = zero.Snapshot(); err == nil {
		t.Fatal("zero proof exposed configuration")
	}
}

func TestSnapshotSigningBoundsAndCopies(t *testing.T) {
	signer, trust, snapshot, agent, now := snapshotFixture(t)
	signed, err := signer.Sign(snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Endpoints[0].Address = "changed-after-signing"
	if _, err = trust.Verify(signed, agent, now); err != nil {
		t.Fatal("caller mutated signed output")
	}
	snapshot.Policy.Grants[0].HTTPPaths = []string{"/" + strings.Repeat("x", domain.MaxSnapshotBytes)}
	if _, err = signer.Sign(snapshot, now); err == nil {
		t.Fatal("oversized configuration signed")
	}
	if _, err = NewSnapshotSigner(agent.Scope, []byte("invalid"), now, now.Add(time.Hour)); err == nil {
		t.Fatal("invalid private key accepted")
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private[len(private)-1] ^= 1
	if _, err = NewSnapshotSigner(agent.Scope, private, now, now.Add(time.Hour)); err == nil {
		t.Fatal("inconsistent private key accepted")
	}
}
func TestSnapshotAllowsSameOrganizationCrossEnvironmentRecords(t *testing.T) {
	signer, trust, snapshot, agent, now := snapshotFixture(t)
	snapshot.Endpoints[0].EnvironmentID = "remote"
	snapshot.Policy.Grants[0].EnvironmentID = "remote"
	snapshot.Policy.Grants[0].Source.ProjectID = "caller-project"
	signed, err := signer.Sign(snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = trust.Verify(signed, agent, now); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotTrustConcurrentRefresh(t *testing.T) {
	signer, trust, snapshot, agent, now := snapshotFixture(t)
	signed, err := signer.Sign(snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for range 50 {
				verified, verifyErr := trust.Verify(signed, agent, now)
				if verifyErr != nil {
					t.Error(verifyErr)
					return
				}
				if verifyErr = verified.ValidAt(now); verifyErr != nil {
					t.Error(verifyErr)
					return
				}
			}
		})
	}
	for generation := uint64(2); generation < 50; generation++ {
		if err = trust.Replace(agent.Scope, generation, []SnapshotKey{signer.Key()}); err != nil {
			t.Fatal(err)
		}
	}
	readers.Wait()
}

func TestSnapshotRejectsUnscopedPolicyContracts(t *testing.T) {
	for _, mutate := range []func(*domain.Snapshot){
		func(s *domain.Snapshot) { s.Policy.ProjectID = "other" },
		func(s *domain.Snapshot) { s.Policy.Generation = 0 },
		func(s *domain.Snapshot) {
			s.Policy.Exports = []domain.ServiceExport{{Metadata: domain.Metadata{Scope: domain.Scope{OrgID: "foreign", ProjectID: "p", EnvironmentID: "e"}}}}
		},
	} {
		signer, _, snapshot, _, now := snapshotFixture(t)
		snapshot.Policy.Scope = snapshot.Scope
		snapshot.Policy.Generation = 1
		mutate(&snapshot)
		if _, err := signer.Sign(snapshot, now); err == nil {
			t.Fatal("unscoped policy signed")
		}
	}
}
