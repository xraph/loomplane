package runtimeapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/access"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type receiptFixture struct {
	calls int
	agent domain.AgentIdentity
	ack   domain.Acknowledgment
	err   error
}

func (f *receiptFixture) AcknowledgeSnapshot(ctx context.Context, a domain.AgentIdentity, ack domain.Acknowledgment) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("receipt has no deadline")
	}
	f.calls++
	f.agent = a
	f.ack = ack
	return f.err
}

type revokeOnRead struct {
	io.Reader
	revoke func()
}

func (r *revokeOnRead) Read(p []byte) (int, error) { r.revoke(); return r.Reader.Read(p) }
func TestRuntimeAcknowledgmentAuthority(t *testing.T) {
	agent := runtimeAgent(t)
	trust, cert := runtimeCertificate(t, agent)
	fixture := &runtimeFixture{agent: agent, active: true}
	receipts := &receiptFixture{}
	handler, err := NewHandler("org", trust, fixture, fixture, fixture, inertWake{}, receipts)
	if err != nil {
		t.Fatal(err)
	}
	ack := domain.Acknowledgment{Agent: agent, Sequence: 1, Revision: "revision", AppliedAt: time.Now().UTC()}
	encode := func(v any) string {
		t.Helper()
		raw, encodeErr := json.Marshal(v)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		return string(raw)
	}
	request := func(body io.Reader, tlsOn bool) int {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, domain.RuntimeAcknowledgmentPath, body)
		r.Header.Set("Authorization", "Bearer management-token")
		if tlsOn {
			r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, HandshakeComplete: true, PeerCertificates: []*x509.Certificate{cert}}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(deadlineRecorder{w}, r)
		return w.Code
	}
	if got := request(strings.NewReader(encode(ack)), false); got != http.StatusUnauthorized {
		t.Fatalf("management token accepted: %d", got)
	}
	if got := request(strings.NewReader(encode(ack)), true); got != http.StatusNoContent || receipts.calls != 1 || receipts.agent != agent || receipts.ack != ack {
		t.Fatalf("receipt: %d %+v", got, receipts)
	}
	foreign := ack
	foreign.Agent.Epoch++
	if got := request(strings.NewReader(encode(foreign)), true); got != http.StatusForbidden {
		t.Fatalf("forged epoch: %d", got)
	}
	for _, body := range []string{`{"unknown":true}`, encode(ack) + ` {}`, strings.Repeat("x", domain.MaxAcknowledgmentBytes+1)} {
		status := request(strings.NewReader(body), true)
		if status != http.StatusBadRequest && status != http.StatusRequestEntityTooLarge {
			t.Fatalf("invalid body: %d", status)
		}
	}
	if receipts.calls != 1 {
		t.Fatal("invalid request reached receipt store")
	}
	receipts.err = access.ErrPolicyConflict
	if got := request(strings.NewReader(encode(ack)), true); got != http.StatusConflict {
		t.Fatalf("stale receipt: %d", got)
	}
	receipts.err = nil
	calls := receipts.calls
	if got := request(&revokeOnRead{Reader: strings.NewReader(encode(ack)), revoke: func() { fixture.active = false }}, true); got != http.StatusForbidden || receipts.calls != calls {
		t.Fatalf("revoked during body read: %d", got)
	}
}
