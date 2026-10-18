package runtimeapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type runtimeFixture struct {
	active    bool
	consumed  bool
	agent     domain.AgentIdentity
	observed  int
	sends     int
	afterSend func()
	deadline  time.Time
}

func (f *runtimeFixture) CheckAgent(_ context.Context, a domain.AgentIdentity) error {
	if !f.active || a != f.agent {
		return workloadidentity.ErrIdentity
	}
	return nil
}
func (f *runtimeFixture) CheckPending(_ context.Context, id string, a domain.AgentIdentity) error {
	if id != "ticket" || f.consumed || a != f.agent {
		return workloadidentity.ErrIdentity
	}
	return nil
}
func (f *runtimeFixture) Consume(ctx context.Context, id string, a domain.AgentIdentity) error {
	if err := f.CheckPending(ctx, id, a); err != nil {
		return err
	}
	f.consumed = true
	f.active = true
	return nil
}
func (f *runtimeFixture) Observe(ctx context.Context, a domain.AgentIdentity, b domain.ObservationBatch) error {
	if a != b.Agent {
		return runtimecatalog.ErrUnauthorized
	}
	f.deadline, _ = ctx.Deadline()
	f.observed++
	return nil
}
func (f *runtimeFixture) Watch(ctx context.Context, a domain.AgentIdentity, _ runtimecatalog.WakeSource, send func(context.Context, *domain.Snapshot) error) error {
	for range 2 {
		f.sends++
		if err := send(ctx, &domain.Snapshot{Scope: a.Scope, DestinationID: a.DestinationID, Epoch: a.Epoch, ExpiresAt: time.Now().Add(time.Second)}); err != nil {
			return err
		}
		if f.afterSend != nil {
			f.afterSend()
		} else {
			f.active = false
		}
	}
	return nil
}

type inertWake struct{}

func (inertWake) Watch(string) (<-chan struct{}, func()) { return nil, func() {} }
func runtimeCertificate(t *testing.T, a domain.AgentIdentity, lifetime ...time.Duration) (*workloadidentity.Trust, *x509.Certificate) {
	trust, cert, _ := runtimeCredentials(t, a, lifetime...)
	return trust, cert
}
func runtimeCredentials(t *testing.T, a domain.AgentIdentity, lifetime ...time.Duration) (*workloadidentity.Trust, *x509.Certificate, tls.Certificate) {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ttl := time.Minute
	if len(lifetime) > 0 {
		ttl = lifetime[0]
	}
	root := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	raw, err := x509.CreateCertificate(rand.Reader, root, root, public, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err = x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(a.URI)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, URIs: []*url.URL{uri}, NotBefore: now.Add(-time.Second), NotAfter: now.Add(ttl)}
	raw, err = x509.CreateCertificate(rand.Reader, leaf, root, public, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err = x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	trust, err := workloadidentity.NewTrust("runtime.test", []*x509.Certificate{root})
	if err != nil {
		t.Fatal(err)
	}
	return trust, leaf, tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: key, Leaf: leaf}
}
func TestRuntimeEnrollmentAndRequestAuthority(t *testing.T) {
	a := runtimeAgent(t)
	trust, cert := runtimeCertificate(t, a)
	f := &runtimeFixture{agent: a}
	handler, err := NewHandler("org", trust, f, f, f, inertWake{})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, tlsOn bool) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		if tlsOn {
			r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, HandshakeComplete: true, PeerCertificates: []*x509.Certificate{cert}}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(deadlineRecorder{w}, r)
		return w
	}
	if w := request("POST", "/runtime/v1/enroll/ticket", "", false); w.Code != http.StatusUnauthorized {
		t.Fatalf("plaintext enrollment: %d", w.Code)
	}
	if w := request("POST", "/runtime/v1/observations", `{}`, true); w.Code != http.StatusForbidden {
		t.Fatalf("pending identity observed: %d", w.Code)
	}
	if w := request("POST", "/runtime/v1/enroll/ticket", "", true); w.Code != http.StatusNoContent {
		t.Fatalf("enroll: %d %s", w.Code, w.Body.String())
	}
	if w := request("POST", "/runtime/v1/enroll/ticket", "", true); w.Code != http.StatusForbidden {
		t.Fatalf("replayed enrollment: %d", w.Code)
	}
	if w := request("POST", "/runtime/v1/observations", `{"bogus":true}`, true); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", w.Code)
	}
	if w := request("POST", "/runtime/v1/observations", `{} {}`, true); w.Code != http.StatusBadRequest {
		t.Fatalf("multiple JSON values: %d", w.Code)
	}
	if w := request("POST", "/runtime/v1/observations", `{}`, true); w.Code != http.StatusForbidden || f.observed != 0 {
		t.Fatalf("body spoofing: %d %d", w.Code, f.observed)
	}
	if w := request("GET", "/runtime/v1/watch", "", true); w.Code != http.StatusOK || strings.Count(w.Body.String(), "\n") != 1 {
		t.Fatalf("revocation during stream: %d %s", w.Code, w.Body.String())
	}
	if w := request("GET", "/api/projects", "", true); w.Code != http.StatusNotFound {
		t.Fatalf("management route reachable: %d", w.Code)
	}
}

type deadlineRecorder struct{ *httptest.ResponseRecorder }

func (deadlineRecorder) SetWriteDeadline(time.Time) error { return nil }

func TestRuntimeRejectsOversizedAndUnboundedBody(t *testing.T) {
	a := runtimeAgent(t)
	trust, cert := runtimeCertificate(t, a)
	f := &runtimeFixture{agent: a, active: true}
	h, err := NewHandler("org", trust, f, f, f, inertWake{})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(t.Context(), "POST", "/runtime/v1/observations", strings.NewReader(`{"endpoints":[{"address":"`+strings.Repeat("a", domain.MaxObservationBytes)+`"}]}`))
	r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, HandshakeComplete: true, PeerCertificates: []*x509.Certificate{cert}}
	w := httptest.NewRecorder()
	h.ServeHTTP(deadlineRecorder{w}, r)
	if w.Code != http.StatusRequestEntityTooLarge || f.observed != 0 {
		t.Fatalf("oversized request: %d", w.Code)
	}
	// A deadline setter is mandatory; otherwise a slow body can hold a slot forever.
	r = httptest.NewRequestWithContext(t.Context(), "POST", "/runtime/v1/observations", strings.NewReader(`{}`))
	r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, HandshakeComplete: true, PeerCertificates: []*x509.Certificate{cert}}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("unbounded body reader accepted: %d", w.Code)
	}
}

func (deadlineRecorder) SetReadDeadline(time.Time) error { return nil }

func TestRuntimeStreamCannotOutliveCredential(t *testing.T) {
	a := runtimeAgent(t)
	trust, cert := runtimeCertificate(t, a, 3*time.Second)
	f := &runtimeFixture{agent: a, active: true, afterSend: func() { time.Sleep(time.Until(cert.NotAfter) + 10*time.Millisecond) }}
	h, err := NewHandler("org", trust, f, f, f, inertWake{})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(t.Context(), "GET", "/runtime/v1/watch", nil)
	r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, HandshakeComplete: true, PeerCertificates: []*x509.Certificate{cert}}
	w := httptest.NewRecorder()
	h.ServeHTTP(deadlineRecorder{w}, r)
	if w.Code != http.StatusOK || strings.Count(w.Body.String(), "\n") != 1 {
		t.Fatalf("stream survived expiry: %d %s", w.Code, w.Body.String())
	}
}

func TestRuntimeServerRequiresBoundedMutualTLS(t *testing.T) {
	for _, config := range []*tls.Config{nil, {}, {MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAnyClientCert, SessionTicketsDisabled: true}, {MinVersion: tls.VersionTLS13, ClientAuth: tls.VerifyClientCertIfGiven, SessionTicketsDisabled: true}, {MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAnyClientCert}} {
		if _, err := NewServer("127.0.0.1:0", &Handler{}, config); err == nil {
			t.Fatal("unsafe runtime TLS configured")
		}
	}
	config := &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAnyClientCert, SessionTicketsDisabled: true}
	server, err := NewServer("127.0.0.1:0", &Handler{}, config)
	if err != nil {
		t.Fatal(err)
	}
	if server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 10*time.Second || server.WriteTimeout != 35*time.Second || server.IdleTimeout != 30*time.Second || server.MaxHeaderBytes != 16<<10 {
		t.Fatal("unbounded runtime HTTP server")
	}
	if server.TLSConfig == config {
		t.Fatal("mutable caller TLS configuration retained")
	}
}

func TestRuntimeMutationDeadlineUsesCredentialExpiry(t *testing.T) {
	a := runtimeAgent(t)
	trust, cert := runtimeCertificate(t, a, 3*time.Second)
	f := &runtimeFixture{agent: a, active: true}
	h, err := NewHandler("org", trust, f, f, f, inertWake{})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(domain.ObservationBatch{Agent: a})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(t.Context(), "POST", "/runtime/v1/observations", strings.NewReader(string(payload)))
	r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, HandshakeComplete: true, PeerCertificates: []*x509.Certificate{cert}}
	w := httptest.NewRecorder()
	h.ServeHTTP(deadlineRecorder{w}, r)
	if w.Code != http.StatusNoContent || f.deadline.IsZero() || f.deadline.After(cert.NotAfter) {
		t.Fatalf("mutation can commit after peer expiry: status %d deadline %s credential %s", w.Code, f.deadline, cert.NotAfter)
	}
}

func runtimeAgent(t *testing.T) domain.AgentIdentity {
	t.Helper()
	a := domain.AgentIdentity{Scope: domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, AgentID: "agent", DestinationID: "destination", Epoch: 1}
	var err error
	a.URI, err = workloadidentity.AgentURI("runtime.test", a)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
