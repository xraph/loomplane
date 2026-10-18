package runtimeapi

import (
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"

	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type fixedSVID struct{ value *x509svid.SVID }

func (s fixedSVID) GetX509SVID() (*x509svid.SVID, error) { return s.value, nil }

type networkRegistry struct {
	agent  domain.AgentIdentity
	active atomic.Bool
}

func (r *networkRegistry) CheckAgent(_ context.Context, a domain.AgentIdentity) error {
	if !r.active.Load() || a != r.agent {
		return workloadidentity.ErrIdentity
	}
	return nil
}
func TestRuntimeRechecksReusedTLSConnection(t *testing.T) {
	a := runtimeAgent(t)
	agentTrust, agentCert, agentPair := runtimeCredentials(t, a)
	const controlURI = "spiffe://runtime.test/loomplane/control/org/org"
	controlTrust, controlCert, controlPair := runtimeCredentials(t, domain.AgentIdentity{URI: controlURI})
	agentID, err := spiffeid.FromString(a.URI)
	if err != nil {
		t.Fatal(err)
	}
	controlID, err := spiffeid.FromString(controlURI)
	if err != nil {
		t.Fatal(err)
	}
	agentSource := fixedSVID{&x509svid.SVID{ID: agentID, Certificates: []*x509.Certificate{agentCert}, PrivateKey: agentPair.PrivateKey.(crypto.Signer)}}
	controlSource := fixedSVID{&x509svid.SVID{ID: controlID, Certificates: []*x509.Certificate{controlCert}, PrivateKey: controlPair.PrivateKey.(crypto.Signer)}}
	registry := &networkRegistry{agent: a}
	registry.active.Store(true)
	f := &runtimeFixture{agent: a, active: true}
	h, err := NewHandler("org", agentTrust, registry, f, f, inertWake{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(h)
	server.TLS = tlsconfig.MTLSServerConfig(controlSource, agentTrust, tlsconfig.AuthorizeID(agentID))
	server.TLS.Certificates = []tls.Certificate{controlPair}
	server.TLS.MinVersion = tls.VersionTLS13
	server.TLS.SessionTicketsDisabled = true
	server.StartTLS()
	defer server.Close()
	config := tlsconfig.MTLSClientConfig(agentSource, controlTrust, tlsconfig.AuthorizeID(controlID))
	config.MinVersion = tls.VersionTLS13
	transport := &http.Transport{TLSClientConfig: config}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	payload, err := json.Marshal(domain.ObservationBatch{Agent: a, Sequence: 1, DestinationGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{http.StatusNoContent, http.StatusForbidden} {
		var reused atomic.Bool
		trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) }}
		ctx := httptrace.WithClientTrace(t.Context(), trace)
		request, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/runtime/v1/observations", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if err = response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("request %d: got %d want %d", i, response.StatusCode, want)
		}
		if i == 1 && !reused.Load() {
			t.Fatal("test did not exercise connection reuse")
		}
		registry.active.Store(false)
	}
}
