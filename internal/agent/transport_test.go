package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"

	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type credentialFixture struct{ svid *x509svid.SVID }

func (s *credentialFixture) GetX509SVID() (*x509svid.SVID, error) { return s.svid, nil }
func (s *credentialFixture) ClientTLS(string) (*tls.Config, error) {
	return &tls.Config{MinVersion: tls.VersionTLS13, SessionTicketsDisabled: true, GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &tls.Certificate{}, nil }, VerifyPeerCertificate: func([][]byte, [][]*x509.Certificate) error { return nil }}, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func transportFixture(t *testing.T) (*Transport, *credentialFixture, domain.AgentIdentity) {
	t.Helper()
	a := domain.AgentIdentity{Scope: domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, DestinationID: "destination", AgentID: "agent", Epoch: 1}
	var err error
	a.URI, err = workloadidentity.AgentURI("agent.test", a)
	if err != nil {
		t.Fatal(err)
	}
	id, err := spiffeid.FromString(a.URI)
	if err != nil {
		t.Fatal(err)
	}
	source := &credentialFixture{svid: &x509svid.SVID{ID: id, Certificates: []*x509.Certificate{{NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Minute)}}}}
	client, err := NewTransport("https://control.test", "spiffe://agent.test/loomplane/control/org/org", source, a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, source, a
}
func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, TLS: &tls.ConnectionState{HandshakeComplete: true, Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{{NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Minute)}}}, Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func TestAgentTransportPinsDestinationAndAudience(t *testing.T) {
	client, source, a := transportFixture(t)
	for _, endpoint := range []string{"http://control.test", "https://:443", "https://user:secret@control.test", "https://control.test/path", "https://control.test?token=x", "https://control.test/#fragment"} {
		if _, err := NewTransport(endpoint, "spiffe://agent.test/loomplane/control/org/org", source, a); err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", endpoint)
		}
	}
	if _, err := NewTransport("https://control.test", "spiffe://agent.test/loomplane/control/org/neighbor", source, a); err == nil {
		t.Fatal("foreign control audience accepted")
	}
	wrong := a
	wrong.DestinationID = "other"
	if _, err := NewTransport("https://control.test", "spiffe://agent.test/loomplane/control/org/org", source, wrong); err == nil {
		t.Fatal("caller changed credential identity")
	}
	tr, ok := client.http.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil || !tr.DisableKeepAlives || tr.TLSHandshakeTimeout == 0 || tr.ResponseHeaderTimeout == 0 {
		t.Fatal("unbounded or ambient-proxy transport")
	}
	if err := client.http.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatal("runtime redirect allowed")
	}
}
func TestAgentTransportDoesNotReplayEnrollment(t *testing.T) {
	client, source, a := transportFixture(t)
	calls := 0
	source.svid.Certificates[0].NotAfter = time.Now().Add(3 * time.Second)
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if deadline, ok := r.Context().Deadline(); !ok || deadline.After(source.svid.Certificates[0].NotAfter) {
			t.Fatal("request exceeds credential expiry")
		}
		return response(http.StatusForbidden, ""), nil
	})
	if err := client.Enroll(t.Context(), "01234567-89ab-cdef-0123-456789abcdef"); !errors.Is(err, ErrClosed) || calls != 1 {
		t.Fatalf("enrollment replay: calls=%d error=%v", calls, err)
	}
	if err := client.Enroll(t.Context(), "../management"); err == nil || calls != 1 {
		t.Fatal("enrollment path injection")
	}
	if err := client.Publish(t.Context(), domain.ObservationBatch{Agent: a, Sequence: 1}); !errors.Is(err, ErrClosed) {
		t.Fatalf("revocation not surfaced: %v", err)
	}
	wrong := a
	wrong.Epoch++
	if err := client.Publish(t.Context(), domain.ObservationBatch{Agent: wrong, Sequence: 1}); err == nil || calls != 2 {
		t.Fatal("spoofed observation sent")
	}
}
func TestAgentWatchBoundsAndScope(t *testing.T) {
	client, _, a := transportFixture(t)
	snapshot := domain.Snapshot{Scope: a.Scope, DestinationID: a.DestinationID, Epoch: a.Epoch, Full: true, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(20 * time.Second)}
	encode := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw) + "\n"
	}
	large := snapshot
	large.Revision = strings.Repeat("x", domain.MaxSnapshotBytes)
	future := snapshot
	future.IssuedAt = time.Now().Add(domain.MaxClockSkew + 10*time.Second)
	future.ExpiresAt = future.IssuedAt.Add(time.Second)
	cases := []struct {
		name, body string
		want       int
	}{
		{"future beyond clock bound", encode(future), 0},
		{"complete", encode(snapshot), 1},
		{"unknown field", `{"foreign":true}` + "\n", 0},
		{"too large", encode(large), 0},
		{"truncated", strings.TrimSuffix(encode(snapshot), "\n"), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return response(http.StatusOK, tc.body), nil })
			count := 0
			err := client.Watch(t.Context(), func(context.Context, domain.Snapshot) error { count++; return nil })
			if count != tc.want || (tc.want == 0 && err == nil) {
				t.Fatalf("untrusted watch: count=%d error=%v", count, err)
			}
		})
	}
	snapshot.EnvironmentID = "foreign"
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return response(http.StatusOK, encode(snapshot)), nil })
	if err := client.Watch(t.Context(), func(context.Context, domain.Snapshot) error { t.Fatal("foreign envelope delivered"); return nil }); err == nil {
		t.Fatal("foreign snapshot accepted")
	}
}

func TestAgentRejectsControlIdentityAfterHandshake(t *testing.T) {
	client, _, a := transportFixture(t)
	cert := &x509.Certificate{NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Minute)}
	snapshot := domain.Snapshot{Scope: a.Scope, DestinationID: a.DestinationID, Epoch: a.Epoch, Full: true, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(20 * time.Second)}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		r := response(http.StatusOK, string(raw)+"\n"+string(raw)+"\n")
		r.TLS = &tls.ConnectionState{HandshakeComplete: true, Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{cert}}
		return r, nil
	})
	count := 0
	err = client.Watch(t.Context(), func(context.Context, domain.Snapshot) error {
		count++
		cert.NotAfter = time.Now().Add(-time.Second)
		return nil
	})
	if !errors.Is(err, ErrClosed) || count != 1 {
		t.Fatalf("control expiry ignored: count=%d error=%v", count, err)
	}
}

func TestAgentAcknowledgmentTransport(t *testing.T) {
	client, source, identity := transportFixture(t)
	ack := domain.Acknowledgment{Agent: identity, Sequence: 3, Revision: "signed", AppliedAt: time.Now().UTC()}
	calls := 0
	code := http.StatusNoContent
	client.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method != http.MethodPost || request.URL.Path != domain.RuntimeAcknowledgmentPath || request.Header.Get("Authorization") != "" {
			t.Fatal("incorrect acknowledgment request")
		}
		deadline, ok := request.Context().Deadline()
		if !ok || deadline.After(source.svid.Certificates[0].NotAfter) {
			t.Fatal("unbounded acknowledgment")
		}
		var body domain.Acknowledgment
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body != ack {
			t.Fatal("receipt changed in transport")
		}
		return response(code, ""), nil
	})
	if err := client.Acknowledge(t.Context(), ack); err != nil {
		t.Fatal(err)
	}
	foreign := ack
	foreign.Agent.Epoch++
	if err := client.Acknowledge(t.Context(), foreign); err == nil || calls != 1 {
		t.Fatal("foreign acknowledgment sent")
	}
	code = http.StatusConflict
	if err := client.Acknowledge(t.Context(), ack); !errors.Is(err, ErrAcknowledgmentStale) {
		t.Fatalf("stale acknowledgment stopped enrollment: %v", err)
	}
	code = http.StatusForbidden
	if err := client.Acknowledge(t.Context(), ack); !errors.Is(err, ErrClosed) {
		t.Fatalf("revoked acknowledgment retried: %v", err)
	}
}

func TestAgentWatchConflictIsRetryableButObservationReplayIsTerminal(t *testing.T) {
	client, _, identity := transportFixture(t)
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return response(http.StatusConflict, ""), nil })
	receive := func(context.Context, domain.Snapshot) error { t.Fatal("conflicted snapshot delivered"); return nil }
	if err := client.Watch(t.Context(), receive); !errors.Is(err, ErrUnavailable) || terminal(err) {
		t.Fatal("snapshot assembly conflict terminated the agent", err)
	}
	if err := client.Publish(t.Context(), domain.ObservationBatch{Agent: identity, Sequence: 1}); !errors.Is(err, runtimecatalog.ErrStale) || !terminal(err) {
		t.Fatal("observation replay became retryable", err)
	}
	if err := client.Enroll(t.Context(), "01234567-89ab-cdef-0123-456789abcdef"); !terminal(err) {
		t.Fatal("enrollment replay became retryable", err)
	}
}
