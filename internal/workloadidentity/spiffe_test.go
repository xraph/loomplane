package workloadidentity

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/url"
	"testing"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"

	domain "github.com/xraph/loomplane/pkg/federation"
)

type agentRegistry struct {
	active   bool
	identity domain.AgentIdentity
}

func (r *agentRegistry) CheckAgent(_ context.Context, a domain.AgentIdentity) error {
	if !r.active || a != r.identity {
		return ErrIdentity
	}
	return nil
}
func identityFixture() domain.AgentIdentity {
	return domain.AgentIdentity{Scope: domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, DestinationID: "destination", AgentID: "agent", Epoch: 1}
}

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newCA(t *testing.T, now time.Time) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	return testCA{cert, key}
}
func (c testCA) issue(t *testing.T, now time.Time, uri string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), NotBefore: now.Add(-time.Second), NotAfter: now.Add(5 * time.Minute), URIs: []*url.URL{u}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	raw, err := x509.CreateCertificate(rand.Reader, template, c.cert, &key.PublicKey, c.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
func TestForeignTrustBundleCannotImpersonate(t *testing.T) {
	now := time.Now()
	owner := newCA(t, now)
	foreign := newCA(t, now)
	a := identityFixture()
	uri, err := AgentURI("loomplane.test", a)
	if err != nil {
		t.Fatal(err)
	}
	a.URI = uri
	reg := &agentRegistry{active: true, identity: a}
	trust, err := NewTrust("loomplane.test", []*x509.Certificate{owner.cert})
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewVerifier(trust, reg)
	good := owner.issue(t, now, uri)
	got, err := verifier.VerifyPeer(t.Context(), []*x509.Certificate{good}, "loomplane.test")
	if err != nil || got != a {
		t.Fatalf("valid peer: %+v %v", got, err)
	}
	if _, err := verifier.VerifyPeer(t.Context(), []*x509.Certificate{foreign.issue(t, now, uri)}, "loomplane.test"); err == nil {
		t.Fatal("foreign root impersonated registered peer")
	}
	if _, err := verifier.VerifyPeer(t.Context(), []*x509.Certificate{good}, "foreign.test"); err == nil {
		t.Fatal("caller changed expected trust domain")
	}
	reg.active = false
	if _, err := verifier.VerifyPeer(t.Context(), []*x509.Certificate{good}, "loomplane.test"); err == nil {
		t.Fatal("revoked identity accepted")
	}
}
func TestWorkloadCannotChooseIdentity(t *testing.T) {
	now := time.Now()
	ca := newCA(t, now)
	a := identityFixture()
	uri, err := AgentURI("loomplane.test", a)
	if err != nil {
		t.Fatal(err)
	}
	a.URI = uri
	trust, err := NewTrust("loomplane.test", []*x509.Certificate{ca.cert})
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewVerifier(trust, &agentRegistry{active: true, identity: a})
	for _, change := range []func(*domain.AgentIdentity){func(a *domain.AgentIdentity) { a.OrgID = "foreign" }, func(a *domain.AgentIdentity) { a.DestinationID = "neighbor" }, func(a *domain.AgentIdentity) { a.Epoch++ }, func(a *domain.AgentIdentity) { a.AgentID = "other" }} {
		b := a
		change(&b)
		claimed, err := AgentURI("loomplane.test", b)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifier.VerifyPeer(t.Context(), []*x509.Certificate{ca.issue(t, now, claimed)}, "loomplane.test"); err == nil {
			t.Fatal("unregistered identity accepted")
		}
	}
	a.AgentID = "agent/../../admin"
	if _, err := AgentURI("loomplane.test", a); err == nil {
		t.Fatal("path injection accepted")
	}
}
func TestCredentialRotation(t *testing.T) {
	now := time.Now()
	old := newCA(t, now)
	next := newCA(t, now)
	a := identityFixture()
	uri, err := AgentURI("loomplane.test", a)
	if err != nil {
		t.Fatal(err)
	}
	a.URI = uri
	trust, err := NewTrust("loomplane.test", []*x509.Certificate{old.cert})
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewVerifier(trust, &agentRegistry{active: true, identity: a})
	if err := trust.Replace("loomplane.test", []*x509.Certificate{old.cert, next.cert}); err != nil {
		t.Fatal(err)
	}
	for _, ca := range []testCA{old, next} {
		if _, err := verifier.VerifyPeer(t.Context(), []*x509.Certificate{ca.issue(t, now, uri)}, "loomplane.test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := trust.Replace("loomplane.test", []*x509.Certificate{next.cert}); err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyPeer(t.Context(), []*x509.Certificate{old.issue(t, now, uri)}, "loomplane.test"); err == nil {
		t.Fatal("removed root remained trusted")
	}
	if err := trust.Replace("foreign.test", []*x509.Certificate{old.cert}); err == nil {
		t.Fatal("foreign domain replaced pinned trust")
	}
	verifier.now = func() time.Time { return now.Add(6 * time.Minute) }
	if _, err := verifier.VerifyPeer(t.Context(), []*x509.Certificate{next.issue(t, now, uri)}, "loomplane.test"); err == nil {
		t.Fatal("expired SVID accepted")
	}
}

func TestInvalidTrustReplacementClosesVerification(t *testing.T) {
	now := time.Now()
	ca := newCA(t, now)
	a := identityFixture()
	uri, err := AgentURI("loomplane.test", a)
	if err != nil {
		t.Fatal(err)
	}
	a.URI = uri
	trust, err := NewTrust("loomplane.test", []*x509.Certificate{ca.cert})
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewVerifier(trust, &agentRegistry{active: true, identity: a})
	if err := trust.Replace("loomplane.test", nil); err == nil {
		t.Fatal("empty root set accepted")
	}
	if _, err := verifier.VerifyPeer(t.Context(), []*x509.Certificate{ca.issue(t, now, uri)}, "loomplane.test"); err == nil {
		t.Fatal("invalid replacement retained stale trust")
	}
}

func TestWorkloadSourceRequiresLocalSocket(t *testing.T) {
	a := identityFixture()
	uri, err := AgentURI("loomplane.test", a)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"tcp://127.0.0.1:8081", "unix://remote/tmp/agent.sock", "unix:relative.sock", ""} {
		if source, err := OpenSource(t.Context(), address, uri); err == nil {
			source.Close()
			t.Fatalf("untrusted Workload API accepted: %s", address)
		}
	}
}

func TestAgentTLSRejectsOtherControlPlane(t *testing.T) {
	a := identityFixture()
	uri, err := AgentURI("loomplane.test", a)
	if err != nil {
		t.Fatal(err)
	}
	id, err := spiffeid.FromString(uri)
	if err != nil {
		t.Fatal(err)
	}
	source := &Source{expected: id}
	for _, peer := range []string{"spiffe://foreign.test/loomplane/control/org/org", "spiffe://loomplane.test/loomplane/control/org/neighbor", "spiffe://loomplane.test/admin"} {
		if _, tlsErr := source.ClientTLS(peer); tlsErr == nil {
			t.Fatalf("wrong control audience accepted: %s", peer)
		}
	}
	config, err := source.ClientTLS("spiffe://loomplane.test/loomplane/control/org/org")
	if err != nil || config.MinVersion != tls.VersionTLS13 || !config.SessionTicketsDisabled || config.ClientSessionCache != nil {
		t.Fatalf("unsafe transport: %v", err)
	}
}

func TestControlTLSScopesRuntimePeers(t *testing.T) {
	now := time.Now()
	ca := newCA(t, now)
	root, err := NewTrust("loomplane.test", []*x509.Certificate{ca.cert})
	if err != nil {
		t.Fatal(err)
	}
	control, err := spiffeid.FromString("spiffe://loomplane.test/loomplane/control/org/org")
	if err != nil {
		t.Fatal(err)
	}
	source := &Source{expected: control}
	config, err := source.ServerTLS(root)
	if err != nil {
		t.Fatal(err)
	}
	if config.MinVersion != tls.VersionTLS13 || !config.SessionTicketsDisabled {
		t.Fatal("runtime TLS can reuse stale authentication")
	}
	a := identityFixture()
	uri, err := AgentURI("loomplane.test", a)
	if err != nil {
		t.Fatal(err)
	}
	peer := ca.issue(t, now, uri)
	if err = config.VerifyPeerCertificate([][]byte{peer.Raw}, nil); err != nil {
		t.Fatal(err)
	}
	a.OrgID = "neighbor"
	uri, err = AgentURI("loomplane.test", a)
	if err != nil {
		t.Fatal(err)
	}
	peer = ca.issue(t, now, uri)
	if err = config.VerifyPeerCertificate([][]byte{peer.Raw}, nil); err == nil {
		t.Fatal("neighboring organization authenticated")
	}
	agentID, err := spiffeid.FromString(uri)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (&Source{expected: agentID}).ServerTLS(root); err == nil {
		t.Fatal("agent identity became control-plane identity")
	}
}
