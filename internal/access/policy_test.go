package access

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type policyHarness struct {
	state   *PolicyState
	signer  *SnapshotSigner
	trust   *SnapshotTrust
	agent   domain.AgentIdentity
	input   domain.PolicyInput
	request Request
	now     time.Time
}

func newPolicyHarness(t *testing.T) *policyHarness {
	t.Helper()
	in, now := policyFixture(t)
	agent := domain.AgentIdentity{Scope: in.Scope, DestinationID: "caller-cluster", AgentID: "agent", Epoch: 1}
	var err error
	agent.URI, err = workloadidentity.AgentURI("loomplane.test", agent)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSnapshotSigner(in.Scope, key, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	trust, err := NewSnapshotTrust(in.Scope, 1, []SnapshotKey{signer.Key()})
	if err != nil {
		t.Fatal(err)
	}
	state, err := NewPolicyState(agent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(state.Close)
	return &policyHarness{state: state, signer: signer, trust: trust, agent: agent, input: in, now: now, request: Request{Source: in.Grants[0].Source, Target: domain.WorkloadSelector{Scope: in.Exports[0].Scope, DestinationID: "cluster-a", ServiceID: "orders"}, ExportID: "api", Port: 8443, Protocol: domain.HTTP, IdentityProfile: domain.WorkloadIdentity, HTTPMethod: "GET", HTTPPath: "/orders"}}
}
func (h *policyHarness) snapshot(t *testing.T, seq uint64, mutate func(*domain.Snapshot)) VerifiedSnapshot {
	t.Helper()
	bundle, err := CompileAt(h.input, h.now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.Snapshot{Scope: h.agent.Scope, DestinationID: h.agent.DestinationID, Epoch: h.agent.Epoch, Sequence: seq, Revision: "routes", Full: true, IssuedAt: h.now, ExpiresAt: h.now.Add(domain.MaxPolicyLease), Policy: bundle}
	if mutate != nil {
		mutate(&snapshot)
	}
	signed, err := h.signer.Sign(snapshot, h.now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := h.trust.Verify(signed, h.agent, h.now)
	if err != nil {
		t.Fatal(err)
	}
	return verified
}
func TestPolicyDefaultDenyAndExactRequest(t *testing.T) {
	h := newPolicyHarness(t)
	if _, err := h.state.Open(t.Context(), h.request); !errors.Is(err, ErrDenied) {
		t.Fatal("new state authorized")
	}
	if _, err := h.state.Apply(t.Context(), h.snapshot(t, 1, nil)); err != nil {
		t.Fatal(err)
	}
	session, err := h.state.Open(t.Context(), h.request)
	if err != nil {
		t.Fatal(err)
	}
	session.Close()
	for _, mutate := range []func(*Request){
		func(r *Request) { r.Source.ServiceID = "other" }, func(r *Request) { r.Source.OrgID = "other" },
		func(r *Request) { r.Target.DestinationID = "other" }, func(r *Request) { r.Target.ProjectID = "other" },
		func(r *Request) { r.Target.ServiceID = "other" }, func(r *Request) { r.Port++ },
		func(r *Request) { r.HTTPMethod = "DELETE" }, func(r *Request) { r.HTTPPath = "/orders/1" },
		func(r *Request) { r.IdentityProfile = domain.GatewayIdentity }, func(r *Request) { r.Protocol = domain.TCP },
	} {
		r := h.request
		mutate(&r)
		if session, err := h.state.Open(t.Context(), r); err == nil {
			session.Close()
			t.Fatal("mismatched request authorized")
		}
	}
}
func TestPolicyReplayRejectedAndRollbackKeepsRevocation(t *testing.T) {
	h := newPolicyHarness(t)
	old := h.snapshot(t, 1, nil)
	if _, err := h.state.Apply(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	session, err := h.state.Open(t.Context(), h.request)
	if err != nil {
		t.Fatal(err)
	}
	h.input.Generation++
	revoked := h.now
	h.input.Grants[0].RevokedAt = &revoked
	if _, err := h.state.Apply(t.Context(), h.snapshot(t, 2, nil)); err != nil {
		t.Fatal(err)
	}
	if session.Context().Err() == nil {
		t.Fatal("revoked session still open")
	}
	if _, err := h.state.Apply(t.Context(), old); !errors.Is(err, ErrPolicyReplay) {
		t.Fatal("old snapshot replayed")
	}
	h.input.Generation = 1
	h.input.Grants[0].RevokedAt = nil
	if _, err := h.state.Apply(t.Context(), h.snapshot(t, 3, nil)); !errors.Is(err, ErrPolicyReplay) {
		t.Fatal("new sequence rolled back policy")
	}
	if _, err := h.state.Open(t.Context(), h.request); err == nil {
		t.Fatal("rollback restored access")
	}
}
func TestPolicyGenerationAtomic(t *testing.T) {
	h := newPolicyHarness(t)
	good := h.snapshot(t, 1, nil)
	if _, err := h.state.Apply(t.Context(), good); err != nil {
		t.Fatal(err)
	}
	bad := h.snapshot(t, 2, func(s *domain.Snapshot) { s.Policy.Grants[0].HTTPMethods = []string{"DELETE"} })
	if _, err := h.state.Apply(t.Context(), bad); err == nil {
		t.Fatal("unapproved signed policy installed")
	}
	s, err := h.state.Open(t.Context(), h.request)
	if err != nil {
		t.Fatal("invalid update replaced unexpired policy")
	}
	s.Close()
	h.input.Grants = nil
	if _, applyErr := h.state.Apply(t.Context(), h.snapshot(t, 2, nil)); !errors.Is(applyErr, ErrPolicyReplay) {
		t.Fatal("same generation changed rules")
	}
	h.input.Generation++
	ack, err := h.state.Apply(t.Context(), h.snapshot(t, 2, nil))
	if err != nil || ack.Agent != h.agent || ack.Sequence != 2 {
		t.Fatalf("activation failed: %+v %v", ack, err)
	}
}
func TestOfflineLeaseExpiresAndClosesSessions(t *testing.T) {
	h := newPolicyHarness(t)
	h.now = time.Now()
	verified := h.snapshot(t, 1, func(s *domain.Snapshot) { s.ExpiresAt = h.now.Add(domain.MaxClockSkew + 150*time.Millisecond) })
	if _, err := h.state.Apply(t.Context(), verified); err != nil {
		t.Fatal(err)
	}
	session, err := h.state.Open(t.Context(), h.request)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("lease did not close session")
	}
	if _, err := h.state.Open(t.Context(), h.request); err == nil {
		t.Fatal("expired policy authorized")
	}
	if _, err := h.state.Apply(t.Context(), verified); err == nil {
		t.Fatal("expired replay accepted")
	}
}
func TestPolicyKeyRemovalClosesSessions(t *testing.T) {
	h := newPolicyHarness(t)
	if _, err := h.state.Apply(t.Context(), h.snapshot(t, 1, nil)); err != nil {
		t.Fatal(err)
	}
	session, err := h.state.Open(t.Context(), h.request)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.trust.Replace(h.agent.Scope, 2, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("key removal retained session")
	}
}
func TestPolicyConcurrentActivation(t *testing.T) {
	h := newPolicyHarness(t)
	snapshots := make([]VerifiedSnapshot, 20)
	for i := range snapshots {
		h.input.Generation = uint64(i + 1)
		snapshots[i] = h.snapshot(t, uint64(i+1), nil)
	}
	var wg sync.WaitGroup
	for _, snapshot := range snapshots {
		wg.Go(func() {
			_, err := h.state.Apply(context.Background(), snapshot)
			if err != nil && !errors.Is(err, ErrPolicyReplay) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if _, err := h.state.Apply(t.Context(), snapshots[19]); !errors.Is(err, ErrPolicyReplay) {
		t.Fatal("latest activation did not win")
	}
	h.state.Close()
	if _, err := h.state.Apply(t.Context(), h.snapshot(t, 21, nil)); err == nil {
		t.Fatal("closed receiver reopened")
	}
}

func TestGrantExpiryClosesSessions(t *testing.T) {
	h := newPolicyHarness(t)
	h.now = time.Now()
	expiry := h.now.Add(domain.MaxClockSkew + 150*time.Millisecond)
	h.input.Grants[0].ExpiresAt = &expiry
	var err error
	h.input.Grants[0].ApprovedRevision, err = GrantRevision(h.input.Grants[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.state.Apply(t.Context(), h.snapshot(t, 1, nil)); err != nil {
		t.Fatal(err)
	}
	session, err := h.state.Open(t.Context(), h.request)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("grant expiry retained session")
	}
}

func TestPolicyRenewalReplacesExpiryTimer(t *testing.T) {
	h := newPolicyHarness(t)
	h.now = time.Now()
	first := h.snapshot(t, 1, func(s *domain.Snapshot) { s.ExpiresAt = h.now.Add(domain.MaxClockSkew + 200*time.Millisecond) })
	if _, err := h.state.Apply(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	session, err := h.state.Open(t.Context(), h.request)
	if err != nil {
		t.Fatal(err)
	}
	second := h.snapshot(t, 2, func(s *domain.Snapshot) { s.ExpiresAt = h.now.Add(domain.MaxClockSkew + 600*time.Millisecond) })
	if _, err := h.state.Apply(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(time.Until(first.Deadline().Add(50 * time.Millisecond)))
	defer timer.Stop()
	select {
	case <-session.Context().Done():
		t.Fatal("renewal kept old deadline")
	case <-timer.C:
	}
	select {
	case <-session.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("renewed lease never expired")
	}
}

func TestPolicyShortenedKeyLifetimeClosesSessions(t *testing.T) {
	h := newPolicyHarness(t)
	if _, err := h.state.Apply(t.Context(), h.snapshot(t, 1, nil)); err != nil {
		t.Fatal(err)
	}
	session, err := h.state.Open(t.Context(), h.request)
	if err != nil {
		t.Fatal(err)
	}
	key := h.signer.Key()
	key.NotAfter = time.Now().Add(domain.MaxClockSkew + 150*time.Millisecond)
	if err := h.trust.Replace(h.agent.Scope, 2, []SnapshotKey{key}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("shortened key lease retained session")
	}
}

func TestPolicyProtocolSelectors(t *testing.T) {
	for _, protocol := range []domain.Protocol{domain.GRPC, domain.TCP} {
		t.Run(string(protocol), func(t *testing.T) {
			h := newPolicyHarness(t)
			h.input.Exports[0].Protocol = protocol
			grant := &h.input.Grants[0]
			grant.Protocol = protocol
			grant.HTTPMethods = nil
			grant.HTTPPaths = nil
			h.request.Protocol = protocol
			h.request.HTTPMethod = ""
			h.request.HTTPPath = ""
			if protocol == domain.GRPC {
				grant.GRPCMethods = []string{"/shop.Orders/Get"}
				h.request.GRPCMethod = "/shop.Orders/Get"
			}
			var err error
			grant.ExportRevision, err = ExportRevision(h.input.Exports[0])
			if err != nil {
				t.Fatal(err)
			}
			grant.ApprovedRevision, err = GrantRevision(*grant)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = h.state.Apply(t.Context(), h.snapshot(t, 1, nil)); err != nil {
				t.Fatal(err)
			}
			session, err := h.state.Open(t.Context(), h.request)
			if err != nil {
				t.Fatal(err)
			}
			session.Close()
			h.request.GRPCMethod = "/shop.Orders/Delete"
			if s, err := h.state.Open(t.Context(), h.request); err == nil {
				s.Close()
				t.Fatal("unsupported protocol selector accepted")
			}
		})
	}
}

func TestPolicyCancellationAndInvalidUpdateLease(t *testing.T) {
	h := newPolicyHarness(t)
	h.now = time.Now()
	verified := h.snapshot(t, 1, func(s *domain.Snapshot) { s.ExpiresAt = h.now.Add(domain.MaxClockSkew + 150*time.Millisecond) })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := h.state.Apply(ctx, verified); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled apply accepted")
	}
	if _, err := h.state.Apply(t.Context(), verified); err != nil {
		t.Fatal(err)
	}
	session, err := h.state.Open(t.Context(), h.request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.state.Apply(t.Context(), h.snapshot(t, 2, func(s *domain.Snapshot) { s.Policy.Revision = "invalid" })); err == nil {
		t.Fatal("invalid revision accepted")
	}
	select {
	case <-session.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("failed update extended old policy")
	}
}
