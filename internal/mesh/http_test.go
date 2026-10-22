package mesh

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type testIdentity struct{ svid *x509svid.SVID }

func (s testIdentity) GetX509SVID() (*x509svid.SVID, error) { return s.svid, nil }

type gatewayFixture struct {
	trust          *workloadidentity.Trust
	root           *x509.Certificate
	key            *ecdsa.PrivateKey
	source, target domain.WorkloadSelector
	policy         *access.PolicyState
	agent          domain.AgentIdentity
	signer         *access.SnapshotSigner
	signingTrust   *access.SnapshotTrust
	input          domain.PolicyInput
}

func newGatewayFixture(t *testing.T) *gatewayFixture {
	t.Helper()
	now := time.Now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	raw, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	trust, err := workloadidentity.NewTrust("loomplane.test", []*x509.Certificate{root})
	if err != nil {
		t.Fatal(err)
	}
	scope := domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}
	f := &gatewayFixture{trust: trust, root: root, key: key, source: domain.WorkloadSelector{Scope: scope, DestinationID: "east", ServiceID: "checkout"}, target: domain.WorkloadSelector{Scope: scope, DestinationID: "west", ServiceID: "orders"}}
	f.agent = domain.AgentIdentity{Scope: scope, DestinationID: "west", AgentID: "gateway", Epoch: 1}
	f.agent.URI, err = workloadidentity.AgentURI(trust.Domain(), f.agent)
	if err != nil {
		t.Fatal(err)
	}
	f.policy, err = access.NewPolicyState(f.agent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.policy.Close)
	_, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.signer, err = access.NewSnapshotSigner(scope, signingKey, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	f.signingTrust, err = access.NewSnapshotTrust(scope, 1, []access.SnapshotKey{f.signer.Key()})
	if err != nil {
		t.Fatal(err)
	}
	export := domain.ServiceExport{Metadata: domain.Metadata{Scope: scope, ID: "orders", Generation: 1}, ServiceID: f.target.ServiceID, DestinationIDs: []string{f.target.DestinationID}, Port: 8080, Protocol: domain.HTTP, IdentityProfile: domain.WorkloadIdentity}
	rev, err := access.ExportRevision(export)
	if err != nil {
		t.Fatal(err)
	}
	grant := domain.AccessGrant{Metadata: domain.Metadata{Scope: scope, ID: "checkout-orders", Generation: 1}, Source: f.source, ExportID: export.ID, ExportRevision: rev, Port: 8080, Protocol: domain.HTTP, HTTPMethods: []string{"GET"}, HTTPPaths: []string{"/orders"}, OwnerID: "owner"}
	grant.ApprovedRevision, err = access.GrantRevision(grant)
	if err != nil {
		t.Fatal(err)
	}
	f.input = domain.PolicyInput{Scope: scope, Generation: 1, Exports: []domain.ServiceExport{export}, Grants: []domain.AccessGrant{grant}}
	return f
}
func (f *gatewayFixture) apply(t *testing.T, seq uint64, lease time.Duration) {
	t.Helper()
	now := time.Now()
	bundle, err := access.CompileAt(f.input, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.Snapshot{Scope: f.agent.Scope, DestinationID: f.agent.DestinationID, Epoch: f.agent.Epoch, Sequence: seq, Revision: "routes", Full: true, IssuedAt: now, ExpiresAt: now.Add(lease), Policy: bundle}
	signed, err := f.signer.Sign(snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := f.signingTrust.Verify(signed, f.agent, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.policy.Apply(t.Context(), verified); err != nil {
		t.Fatal(err)
	}
}
func (f *gatewayFixture) issue(t *testing.T, uri string, expiry time.Time) testIdentity {
	t.Helper()
	now := time.Now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), NotBefore: now.Add(-time.Minute), NotAfter: expiry, URIs: []*url.URL{parsed}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	raw, err := x509.CreateCertificate(rand.Reader, template, f.root, &key.PublicKey, f.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	id, err := spiffeid.FromString(uri)
	if err != nil {
		t.Fatal(err)
	}
	return testIdentity{&x509svid.SVID{ID: id, Certificates: []*x509.Certificate{cert}, PrivateKey: key}}
}
func (f *gatewayFixture) server(t *testing.T, handler http.Handler) (*httptest.Server, *http.Client) {
	t.Helper()
	guard, err := NewHTTPGuard(HTTPConfig{Trust: f.trust, Policy: f.policy, Target: f.target, ExportID: "orders", Port: f.input.Exports[0].Port, Authority: "orders.internal", Handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	targetURI, err := workloadidentity.WorkloadURI(f.trust.Domain(), f.target)
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, err := ServerTLS(f.issue(t, targetURI, time.Now().Add(time.Minute)), f.trust, f.target)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(guard)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { return serverTLS, nil }}
	server.StartTLS()
	t.Cleanup(server.Close)
	sourceURI, err := workloadidentity.WorkloadURI(f.trust.Domain(), f.source)
	if err != nil {
		t.Fatal(err)
	}
	return server, f.client(t, f.issue(t, sourceURI, time.Now().Add(time.Minute)))
}

func (f *gatewayFixture) client(t *testing.T, identity testIdentity) *http.Client {
	t.Helper()
	targetURI, err := workloadidentity.WorkloadURI(f.trust.Domain(), f.target)
	if err != nil {
		t.Fatal(err)
	}
	targetID, err := spiffeid.FromString(targetURI)
	if err != nil {
		t.Fatal(err)
	}
	cfg := tlsconfig.MTLSClientConfig(identity, f.trust, tlsconfig.AuthorizeID(targetID))
	cfg.MinVersion = tls.VersionTLS13
	transport := &http.Transport{TLSClientConfig: cfg}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func request(t *testing.T, client *http.Client, endpoint string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "orders.internal"
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err = io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode
}
func TestGatewayHTTPDefaultDenyAndRevocation(t *testing.T) {
	f := newGatewayFixture(t)
	var calls atomic.Int32
	server, client := f.server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }))
	if code := request(t, client, server.URL+"/orders"); code != http.StatusForbidden {
		t.Fatalf("no policy: %d", code)
	}
	f.apply(t, 1, time.Minute)
	if code := request(t, client, server.URL+"/orders"); code != http.StatusNoContent {
		t.Fatalf("allowed: %d", code)
	}
	if code := request(t, client, server.URL+"/private"); code != http.StatusForbidden {
		t.Fatalf("path escaped grant: %d", code)
	}
	f.input.Grants = nil
	f.input.Generation++
	f.apply(t, 2, time.Minute)
	if code := request(t, client, server.URL+"/orders"); code != http.StatusForbidden || calls.Load() != 1 {
		t.Fatalf("revocation: %d calls %d", code, calls.Load())
	}
}
func TestGatewayClosesActiveSession(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		t.Run(map[bool]string{false: "revocation", true: "lease"}[expiry], func(t *testing.T) {
			f := newGatewayFixture(t)
			lease := time.Minute
			if expiry {
				lease = domain.MaxClockSkew + time.Second
			}
			f.apply(t, 1, lease)
			entered, closed := make(chan struct{}), make(chan struct{})
			server, client := f.server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-r.Context().Done()
				close(closed)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			done := make(chan struct{})
			go func() {
				defer close(done)
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/orders", nil)
				req.Host = "orders.internal"
				res, err := client.Do(req)
				if err == nil {
					res.Body.Close()
				}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("request did not enter")
			}
			if !expiry {
				f.input.Grants = nil
				f.input.Generation++
				f.apply(t, 2, time.Minute)
			}
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("active request survived revoked or expired policy")
			}
			<-done
		})
	}
}

func TestGatewayRejectsForgedIdentity(t *testing.T) {
	f := newGatewayFixture(t)
	f.apply(t, 1, time.Minute)
	var calls atomic.Int32
	server, client := f.server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		for key := range r.Header {
			lower := strings.ToLower(key)
			if strings.HasPrefix(lower, "x-spiffe-") || strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "x-loomplane-") || lower == "forwarded" {
				t.Errorf("spoofed identity reached backend: %s", key)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/orders", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "orders.internal"
	for _, header := range []string{"X-Spiffe-ID", "X-Loomplane-Service", "X-Forwarded-Client-Cert", "Forwarded"} {
		req.Header.Set(header, "admin")
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatal(res.StatusCode)
	}
	neighbor := f.source
	neighbor.ServiceID = "neighbor"
	uri, err := workloadidentity.WorkloadURI(f.trust.Domain(), neighbor)
	if err != nil {
		t.Fatal(err)
	}
	other := f.client(t, f.issue(t, uri, time.Now().Add(time.Minute)))
	if code := request(t, other, server.URL+"/orders"); code != http.StatusForbidden {
		t.Fatalf("neighbor authorized: %d", code)
	}
	for _, identity := range []testIdentity{f.issue(t, f.agent.URI, time.Now().Add(time.Minute)), newGatewayFixture(t).issue(t, uri, time.Now().Add(time.Minute))} {
		denied := f.client(t, identity)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/orders", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "orders.internal"
		response, callErr := denied.Do(req)
		if callErr == nil {
			response.Body.Close()
			t.Fatalf("agent or foreign CA completed handshake: %d", response.StatusCode)
		}
	}
	for _, path := range []string{"/%6frders", "//orders", "/orders/../orders"} {
		if code := request(t, client, server.URL+path); code != http.StatusForbidden {
			t.Fatalf("ambiguous path %s: %d", path, code)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("denied requests reached handler: %d", calls.Load())
	}
}

func TestGatewayRechecksTrustOnReusedConnection(t *testing.T) {
	f := newGatewayFixture(t)
	f.apply(t, 1, time.Minute)
	server, client := f.server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if code := request(t, client, server.URL+"/orders"); code != http.StatusNoContent {
		t.Fatal(code)
	}
	foreign := newGatewayFixture(t)
	if err := f.trust.Replace(f.trust.Domain(), []*x509.Certificate{foreign.root}); err != nil {
		t.Fatal(err)
	}
	// The original HTTP connection is reused, so the handler must recheck trust.
	if code := request(t, client, server.URL+"/orders"); code != http.StatusForbidden {
		t.Fatalf("removed CA retained authority: %d", code)
	}
}

func TestGatewayRejectsUnqualifiedHTTPForms(t *testing.T) {
	f := newGatewayFixture(t)
	f.apply(t, 1, time.Minute)
	var calls atomic.Int32
	server, client := f.server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct{ method, host, header, value string }{
		{http.MethodConnect, "orders.internal", "", ""},
		{http.MethodTrace, "orders.internal", "", ""},
		{http.MethodGet, "neighbor.internal", "", ""},
		{http.MethodGet, "orders.internal", "Upgrade", "websocket"},
		{http.MethodGet, "orders.internal", "Content-Type", "application/grpc"},
	} {
		req, err := http.NewRequestWithContext(t.Context(), tc.method, server.URL+"/orders", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = tc.host
		if tc.header != "" {
			req.Header.Set(tc.header, tc.value)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("accepted %+v: %d", tc, res.StatusCode)
		}
	}
	guard, err := NewHTTPGuard(HTTPConfig{Trust: f.trust, Policy: f.policy, Target: f.target, ExportID: "orders", Port: 8080, Authority: "orders.internal", Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) })})
	if err != nil {
		t.Fatal(err)
	}
	plain := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/orders", nil)
	plain.Host = "orders.internal"
	plain.Header.Set("X-Spiffe-ID", "checkout")
	recorder := httptest.NewRecorder()
	guard.ServeHTTP(recorder, plain)
	if recorder.Code != http.StatusForbidden || calls.Load() != 0 {
		t.Fatal("unauthenticated or unsupported HTTP reached the backend")
	}
}

func TestGatewayServerIdentityMustMatchCertificate(t *testing.T) {
	f := newGatewayFixture(t)
	expected, err := workloadidentity.WorkloadURI(f.trust.Domain(), f.target)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := workloadidentity.WorkloadURI(f.trust.Domain(), f.source)
	if err != nil {
		t.Fatal(err)
	}
	identity := f.issue(t, wrong, time.Now().Add(time.Minute))
	identity.svid.ID, err = spiffeid.FromString(expected)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ServerTLS(identity, f.trust, f.target); err == nil {
		t.Fatal("accepted claimed server identity with a different certificate")
	}
}

func TestGatewayCertificateDeadlineClosesSession(t *testing.T) {
	f := newGatewayFixture(t)
	f.apply(t, 1, time.Minute)
	entered, closed := make(chan struct{}), make(chan struct{})
	server, _ := f.server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(closed)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	uri, err := workloadidentity.WorkloadURI(f.trust.Domain(), f.source)
	if err != nil {
		t.Fatal(err)
	}
	client := f.client(t, f.issue(t, uri, time.Now().Add(domain.MaxClockSkew+2*time.Second)))
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/orders", nil)
		req.Host = "orders.internal"
		res, err := client.Do(req)
		if err == nil {
			res.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("short-lived identity never entered")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("session outlived certificate deadline")
	}
	<-done
}

func (f *gatewayFixture) setPort(t *testing.T, port uint16) {
	t.Helper()
	f.input.Exports[0].Port = port
	revision, err := access.ExportRevision(f.input.Exports[0])
	if err != nil {
		t.Fatal(err)
	}
	f.input.Grants[0].Port = port
	f.input.Grants[0].ExportRevision = revision
	f.input.Grants[0].ApprovedRevision, err = access.GrantRevision(f.input.Grants[0])
	if err != nil {
		t.Fatal(err)
	}
}
