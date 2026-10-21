package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type receiverHarness struct {
	store    *Store
	path     string
	identity domain.AgentIdentity
	trust    *access.SnapshotTrust
	signer   *access.SnapshotSigner
	receiver *SnapshotReceiver
	request  access.Request
	input    domain.PolicyInput
	now      time.Time
}

func newReceiverHarness(t *testing.T) *receiverHarness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent", "state.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	scope := domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}
	identity := domain.AgentIdentity{Scope: scope, DestinationID: "source", AgentID: "agent", Epoch: 1}
	identity.URI, err = workloadidentity.AgentURI("loomplane.test", identity)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := access.NewSnapshotSigner(scope, private, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	trust, err := access.NewSnapshotTrust(scope, 1, []access.SnapshotKey{signer.Key()})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewSnapshotReceiver(store, identity, trust)
	if err != nil {
		t.Fatal(err)
	}
	export := domain.ServiceExport{Metadata: domain.Metadata{Scope: scope, ID: "api", Generation: 1}, ServiceID: "orders", DestinationIDs: []string{"target"}, Port: 8443, Protocol: domain.HTTP, IdentityProfile: domain.WorkloadIdentity}
	revision, err := access.ExportRevision(export)
	if err != nil {
		t.Fatal(err)
	}
	grant := domain.AccessGrant{Metadata: domain.Metadata{Scope: scope, ID: "grant", Generation: 1}, Source: domain.WorkloadSelector{Scope: scope, DestinationID: "source", ServiceID: "checkout"}, ExportID: "api", ExportRevision: revision, Port: 8443, Protocol: domain.HTTP, HTTPMethods: []string{"GET"}, HTTPPaths: []string{"/orders"}, OwnerID: "owner"}
	grant.ApprovedRevision, err = access.GrantRevision(grant)
	if err != nil {
		t.Fatal(err)
	}
	request := access.Request{Source: grant.Source, Target: domain.WorkloadSelector{Scope: scope, DestinationID: "target", ServiceID: "orders"}, ExportID: "api", Port: 8443, Protocol: domain.HTTP, IdentityProfile: domain.WorkloadIdentity, HTTPMethod: "GET", HTTPPath: "/orders"}
	return &receiverHarness{store: store, path: path, identity: identity, trust: trust, signer: signer, receiver: receiver, request: request, input: domain.PolicyInput{Scope: scope, Generation: 1, Exports: []domain.ServiceExport{export}, Grants: []domain.AccessGrant{grant}}, now: now}
}
func (h *receiverHarness) snapshot(t *testing.T, sequence uint64) domain.Snapshot {
	t.Helper()
	bundle, err := access.CompileAt(h.input, h.now)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := h.signer.Sign(domain.Snapshot{Scope: h.identity.Scope, DestinationID: h.identity.DestinationID, Epoch: h.identity.Epoch, Sequence: sequence, Revision: "routes", Full: true, IssuedAt: h.now, ExpiresAt: h.now.Add(time.Minute), Policy: bundle}, h.now)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
func (h *receiverHarness) activate(t *testing.T) *access.Session {
	t.Helper()
	if err := h.store.Activate(h.identity); err != nil {
		t.Fatal(err)
	}
	if _, err := h.receiver.Apply(t.Context(), h.snapshot(t, 1)); err != nil {
		t.Fatal(err)
	}
	session, err := h.receiver.Open(t.Context(), h.request)
	if err != nil {
		t.Fatal(err)
	}
	return session
}
func TestPolicyReceiverRequiresEnrollment(t *testing.T) {
	h := newReceiverHarness(t)
	snapshot := h.snapshot(t, 1)
	if _, err := h.receiver.Apply(t.Context(), snapshot); !errors.Is(err, ErrClosed) {
		t.Fatal("unenrolled policy activated")
	}
	if _, err := h.receiver.Open(t.Context(), h.request); !errors.Is(err, ErrClosed) {
		t.Fatal("unenrolled session opened")
	}
	session := h.activate(t)
	h.store.Deactivate()
	if session.Context().Err() == nil {
		t.Fatal("deactivation retained session")
	}
	if _, err := h.receiver.Apply(t.Context(), h.snapshot(t, 2)); !errors.Is(err, ErrClosed) {
		t.Fatal("deactivated receiver reopened")
	}
}
func TestPolicyReceiverReplacementCannotResetReplay(t *testing.T) {
	h := newReceiverHarness(t)
	session := h.activate(t)
	defer session.Close()
	replacement, err := NewSnapshotReceiver(h.store, h.identity, h.trust)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replacement.Apply(t.Context(), h.snapshot(t, 1)); !errors.Is(err, access.ErrPolicyReplay) {
		t.Fatalf("receiver reset replay: %v", err)
	}
	h.input.Generation = 2
	h.input.Grants = nil
	if _, err := replacement.Apply(t.Context(), h.snapshot(t, 2)); err != nil {
		t.Fatal(err)
	}
	if session.Context().Err() == nil {
		t.Fatal("replacement receiver failed to revoke original session")
	}
	h.input.Generation = 1
	if _, err := h.receiver.Apply(t.Context(), h.snapshot(t, 3)); !errors.Is(err, access.ErrPolicyReplay) {
		t.Fatal("original receiver reset generation")
	}
}
func TestRestoredPolicyReceiverStartsClosed(t *testing.T) {
	h := newReceiverHarness(t)
	session := h.activate(t)
	defer session.Close()
	restored, err := OpenStore(h.path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	receiver, err := NewSnapshotReceiver(restored, h.identity, h.trust)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Apply(t.Context(), h.snapshot(t, 2)); !errors.Is(err, ErrClosed) {
		t.Fatal("restored receiver reused authority")
	}
	if err := restored.Activate(h.identity); !errors.Is(err, ErrClosed) {
		t.Fatal("restore accepted old epoch")
	}
}
func TestPolicyReceiverReplacementEpochClosesIdleSessions(t *testing.T) {
	h := newReceiverHarness(t)
	h.store.policyCheckInterval = 10 * time.Millisecond
	session := h.activate(t)
	replacement, err := OpenStore(h.path)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	next := h.identity
	next.Epoch++
	next.URI, err = workloadidentity.AgentURI("loomplane.test", next)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Activate(next); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("replacement left idle session open")
	}
	if _, err := h.receiver.Open(t.Context(), h.request); !errors.Is(err, ErrClosed) {
		t.Fatal("old process authorized after replacement")
	}
}
func TestPolicyReceiverBadSignaturePreservesOnlyCurrentPolicy(t *testing.T) {
	h := newReceiverHarness(t)
	session := h.activate(t)
	defer session.Close()
	bad := h.snapshot(t, 2)
	bad.Signature[0] ^= 1
	if _, err := h.receiver.Apply(t.Context(), bad); err == nil {
		t.Fatal("bad signature accepted")
	}
	if session.Context().Err() != nil {
		t.Fatal("bad update closed valid current policy")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := h.receiver.Open(ctx, h.request); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled admission accepted")
	}
}
func TestPolicyReceiverFailedReenrollmentClosesSessions(t *testing.T) {
	h := newReceiverHarness(t)
	session := h.activate(t)
	if err := h.store.Activate(h.identity); !errors.Is(err, ErrClosed) {
		t.Fatal("duplicate enrollment accepted")
	}
	if session.Context().Err() == nil {
		t.Fatal("failed reenrollment retained policy")
	}
}

func TestPolicyReceiverPinsTrustForEnrollment(t *testing.T) {
	h := newReceiverHarness(t)
	session := h.activate(t)
	defer session.Close()
	otherTrust, err := access.NewSnapshotTrust(h.identity.Scope, 1, []access.SnapshotKey{h.signer.Key()})
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewSnapshotReceiver(h.store, h.identity, otherTrust)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Apply(t.Context(), h.snapshot(t, 2)); !errors.Is(err, ErrClosed) {
		t.Fatal("receiver replaced enrolled trust")
	}
	if _, err := other.Open(t.Context(), h.request); !errors.Is(err, access.ErrDenied) {
		t.Fatal("different trust admitted session")
	}
	if session.Context().Err() != nil {
		t.Fatal("foreign trust closed current policy")
	}
}

func TestPolicyReceiverDatabaseFailureClosesSessions(t *testing.T) {
	h := newReceiverHarness(t)
	session := h.activate(t)
	if err := h.store.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.receiver.Open(t.Context(), h.request); !errors.Is(err, ErrClosed) {
		t.Fatal("database failure admitted session")
	}
	if session.Context().Err() == nil {
		t.Fatal("database failure retained sessions")
	}
}

func TestAgentExitClosesVerifiedPolicySessions(t *testing.T) {
	h := newReceiverHarness(t)
	opened := make(chan *access.Session, 1)
	stop := errors.New("observer stopped")
	snapshot := h.snapshot(t, 1)
	client := &sessionClient{watch: func(ctx context.Context, receive func(context.Context, domain.Snapshot) error) error {
		if err := receive(ctx, snapshot); err != nil {
			return err
		}
		session, err := h.receiver.Open(ctx, h.request)
		if err != nil {
			return err
		}
		opened <- session
		<-ctx.Done()
		return ctx.Err()
	}}
	var session *access.Session
	agent := &Agent{State: h.store, Identity: h.identity, Transport: client, EnrollmentID: "01234567-89ab-cdef-0123-456789abcdef", Receive: h.receiver.Apply, Observe: func(ctx context.Context, _ func(context.Context) (uint64, error), _ func(context.Context, domain.ObservationBatch) error) error {
		select {
		case session = <-opened:
			return stop
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	if err := agent.Run(t.Context()); !errors.Is(err, stop) {
		t.Fatalf("agent exit: %v", err)
	}
	if session == nil || session.Context().Err() == nil {
		t.Fatal("stopped agent retained session")
	}
	if _, err := h.receiver.Apply(t.Context(), h.snapshot(t, 2)); !errors.Is(err, ErrClosed) {
		t.Fatal("stopped agent retained activation")
	}
}

func TestPolicyAdmissionWaitsForEnrollmentTransaction(t *testing.T) {
	h := newReceiverHarness(t)
	session := h.activate(t)
	defer session.Close()
	other, err := OpenStore(h.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := other.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(t.Context(), `UPDATE agent_state SET epoch=epoch+1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		opened, err := h.receiver.Open(t.Context(), h.request)
		if opened != nil {
			opened.Close()
		}
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("admitted before enrollment transaction finished: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("old epoch admitted: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("admission did not resolve")
	}
	if session.Context().Err() == nil {
		t.Fatal("old session survived fenced admission")
	}
}

func TestAgentRejectedPolicyKeepsUnexpiredStateAcrossReconnect(t *testing.T) {
	h := newReceiverHarness(t)
	good := h.snapshot(t, 1)
	bad := h.snapshot(t, 2)
	bad.Signature[0] ^= 1
	renewed := h.snapshot(t, 3)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var session *access.Session
	client := &sessionClient{}
	client.watch = func(delivery context.Context, receive func(context.Context, domain.Snapshot) error) error {
		if client.watches.Load() == 1 {
			if err := receive(delivery, good); err != nil {
				return err
			}
			var openErr error
			session, openErr = h.receiver.Open(ctx, h.request)
			if openErr != nil {
				return openErr
			}
			return receive(delivery, bad)
		}
		if session == nil || session.Context().Err() != nil {
			return errors.New("rejected update discarded current policy")
		}
		if err := receive(delivery, renewed); err != nil {
			return err
		}
		cancel()
		return ctx.Err()
	}
	agent := &Agent{State: h.store, Identity: h.identity, Transport: client, EnrollmentID: "01234567-89ab-cdef-0123-456789abcdef", Receive: h.receiver.Apply, retryMin: time.Millisecond, Observe: func(ctx context.Context, _ func(context.Context) (uint64, error), _ func(context.Context, domain.ObservationBatch) error) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	if err := agent.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("rejected policy stopped the agent: %v", err)
	}
	if client.watches.Load() != 2 {
		t.Fatal("receiver did not reconnect")
	}
	if session.Context().Err() == nil {
		t.Fatal("agent exit retained session")
	}
}

func TestAgentEnrollmentFailureOverridesRetryablePolicyError(t *testing.T) {
	h := newReceiverHarness(t)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	failure := errors.Join(ErrClosed, access.ErrPolicy)
	client := &sessionClient{watch: func(ctx context.Context, receive func(context.Context, domain.Snapshot) error) error {
		return receive(ctx, domain.Snapshot{})
	}}
	agent := &Agent{State: h.store, Identity: h.identity, Transport: client, EnrollmentID: "01234567-89ab-cdef-0123-456789abcdef", Receive: func(context.Context, domain.Snapshot) (domain.Acknowledgment, error) {
		return domain.Acknowledgment{}, failure
	}, retryMin: time.Millisecond, Observe: func(ctx context.Context, _ func(context.Context) (uint64, error), _ func(context.Context, domain.ObservationBatch) error) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	if err := agent.Run(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("enrollment failure was retried: %v", err)
	}
	if client.watches.Load() != 1 {
		t.Fatal("terminal enrollment failure reconnected")
	}
}
