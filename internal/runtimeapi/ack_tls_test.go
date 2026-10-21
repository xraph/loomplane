package runtimeapi

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"

	"github.com/xraph/loomplane/internal/access"
	runtimeagent "github.com/xraph/loomplane/internal/agent"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type wireCredential struct {
	fixedSVID
	trust *workloadidentity.Trust
}

func (c wireCredential) ClientTLS(uri string) (*tls.Config, error) {
	id, err := spiffeid.FromString(uri)
	if err != nil {
		return nil, err
	}
	config := tlsconfig.MTLSClientConfig(c, c.trust, tlsconfig.AuthorizeID(id))
	config.MinVersion = tls.VersionTLS13
	config.SessionTicketsDisabled = true
	return config, nil
}

type wireCatalog struct {
	Catalog
	snapshot domain.Snapshot
}

func (c wireCatalog) Watch(ctx context.Context, _ domain.AgentIdentity, _ runtimecatalog.WakeSource, send func(context.Context, *domain.Snapshot) error) error {
	return send(ctx, &c.snapshot)
}

type wireReceipts chan domain.Acknowledgment

func (r wireReceipts) AcknowledgeSnapshot(ctx context.Context, _ domain.AgentIdentity, ack domain.Acknowledgment) error {
	select {
	case r <- ack:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestRuntimeSignedSnapshotAcknowledgmentOverTLS(t *testing.T) {
	identity := runtimeAgent(t)
	agentTrust, agentCert, agentPair := runtimeCredentials(t, identity)
	controlURI, err := workloadidentity.ControlURI(identity)
	if err != nil {
		t.Fatal(err)
	}
	controlTrust, controlCert, controlPair := runtimeCredentials(t, domain.AgentIdentity{URI: controlURI})
	agentID, err := spiffeid.FromString(identity.URI)
	if err != nil {
		t.Fatal(err)
	}
	controlID, err := spiffeid.FromString(controlURI)
	if err != nil {
		t.Fatal(err)
	}
	source := wireCredential{fixedSVID{&x509svid.SVID{ID: agentID, Certificates: []*x509.Certificate{agentCert}, PrivateKey: agentPair.PrivateKey.(crypto.Signer)}}, controlTrust}
	control := fixedSVID{&x509svid.SVID{ID: controlID, Certificates: []*x509.Certificate{controlCert}, PrivateKey: controlPair.PrivateKey.(crypto.Signer)}}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	signer, err := access.NewSnapshotSigner(identity.Scope, private, now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	trust, err := access.NewSnapshotTrust(identity.Scope, 1, []access.SnapshotKey{signer.Key()})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := access.CompileAt(domain.PolicyInput{Scope: identity.Scope, Generation: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := signer.Sign(domain.Snapshot{Scope: identity.Scope, DestinationID: identity.DestinationID, Epoch: identity.Epoch, Sequence: 1, Revision: "signed", Full: true, IssuedAt: now, ExpiresAt: now.Add(20 * time.Second), Policy: policy}, now)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &runtimeFixture{agent: identity, active: true}
	registry := &networkRegistry{agent: identity}
	registry.active.Store(true)
	receipts := make(wireReceipts, 1)
	handler, err := NewHandler(identity.OrgID, agentTrust, registry, fixture, wireCatalog{Catalog: fixture, snapshot: snapshot}, inertWake{}, receipts)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = tlsconfig.MTLSServerConfig(control, agentTrust, tlsconfig.AuthorizeID(agentID))
	server.TLS.Certificates = []tls.Certificate{controlPair}
	server.TLS.MinVersion = tls.VersionTLS13
	server.TLS.SessionTicketsDisabled = true
	server.StartTLS()
	defer server.Close()
	transport, err := runtimeagent.NewTransport(server.URL, controlURI, source, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	state, err := runtimeagent.OpenStore(filepath.Join(t.TempDir(), "agent", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := state.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if activateErr := state.Activate(identity); activateErr != nil {
		t.Fatal(activateErr)
	}
	receiver, err := runtimeagent.NewSnapshotReceiver(state, identity, trust)
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("snapshot received and acknowledged")
	err = transport.Watch(t.Context(), func(ctx context.Context, s domain.Snapshot) error {
		ack, applyErr := receiver.Apply(ctx, s)
		if applyErr != nil {
			return applyErr
		}
		if ackErr := transport.Acknowledge(ctx, ack); ackErr != nil {
			return ackErr
		}
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	select {
	case ack := <-receipts:
		if ack.Agent != identity || ack.Sequence != snapshot.Sequence || ack.Revision != snapshot.Revision {
			t.Fatal("wire acknowledgment did not match applied snapshot")
		}
		registry.active.Store(false)
		if ackErr := transport.Acknowledge(t.Context(), ack); !errors.Is(ackErr, runtimeagent.ErrClosed) {
			t.Fatal("revoked identity acknowledged", ackErr)
		}
	default:
		t.Fatal("acknowledgment never reached authenticated endpoint")
	}
}
