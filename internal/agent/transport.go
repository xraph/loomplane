package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"

	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

var ErrUnavailable = errors.New("agent control plane unavailable")

// CredentialSource is implemented by the rotating local Workload API source.
type CredentialSource interface {
	x509svid.Source
	ClientTLS(string) (*tls.Config, error)
}
type Transport struct {
	http     *http.Client
	endpoint string
	source   CredentialSource
	agent    domain.AgentIdentity
	tls      *tls.Config
}

func NewTransport(endpoint, controlURI string, source CredentialSource, agent domain.AgentIdentity) (*Transport, error) {
	u, err := parseControlEndpoint(endpoint)
	if err != nil || source == nil {
		return nil, ErrClosed
	}
	expected, err := workloadidentity.ControlURI(agent)
	if err != nil || expected != controlURI {
		return nil, ErrClosed
	}
	config, err := source.ClientTLS(controlURI)
	if err != nil {
		return nil, err
	}
	if config == nil || config.MinVersion < tls.VersionTLS13 || !config.SessionTicketsDisabled || config.GetClientCertificate == nil || (config.VerifyPeerCertificate == nil && config.VerifyConnection == nil) {
		return nil, ErrClosed
	}
	tr := &http.Transport{TLSClientConfig: config.Clone(), DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16 << 10, DisableCompression: true, DisableKeepAlives: true, MaxConnsPerHost: 4}
	u.Path = ""
	client := &Transport{http: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, endpoint: u.String(), source: source, agent: agent, tls: config.Clone()}
	if _, err = client.credentialDeadline(10 * time.Second); err != nil {
		return nil, err
	}
	return client, nil
}
func parseControlEndpoint(endpoint string) (*url.URL, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrClosed
	}
	return u, nil
}
func (t *Transport) Close() { t.http.CloseIdleConnections() }
func (t *Transport) credentialDeadline(limit time.Duration) (time.Time, error) {
	svid, err := t.source.GetX509SVID()
	if err != nil {
		return time.Time{}, err
	}
	if svid == nil || svid.ID.String() != t.agent.URI || len(svid.Certificates) == 0 || len(svid.Certificates) > 8 {
		return time.Time{}, ErrClosed
	}
	now := time.Now()
	deadline := now.Add(limit)
	for _, cert := range svid.Certificates {
		if cert == nil || cert.NotBefore.After(now) || !cert.NotAfter.After(now) {
			return time.Time{}, ErrClosed
		}
		if cert.NotAfter.Before(deadline) {
			deadline = cert.NotAfter
		}
	}
	if svid.Certificates[0].NotAfter.After(now.Add(workloadidentity.MaxCredentialLifetime)) {
		return time.Time{}, ErrClosed
	}
	return deadline, nil
}
func (t *Transport) request(ctx context.Context, method, path string, payload []byte, limit time.Duration) (*http.Response, context.CancelFunc, error) {
	deadline, err := t.credentialDeadline(limit)
	if err != nil {
		return nil, func() {}, err
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	request, err := http.NewRequestWithContext(ctx, method, t.endpoint+path, bytes.NewReader(payload))
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := t.http.Do(request)
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	if peerErr := t.verifyControl(response.TLS); peerErr != nil {
		cancel()
		return nil, func() {}, errors.Join(peerErr, response.Body.Close())
	}
	response.Request = request
	return response, cancel, nil
}

// Enroll deliberately makes one attempt. An uncertain response requires a new
// server enrollment; blindly retrying cannot establish whether a handle was used.
func (t *Transport) Enroll(ctx context.Context, id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return ErrClosed
	}
	response, cancel, err := t.request(ctx, http.MethodPost, domain.RuntimeEnrollmentPath+id, nil, 10*time.Second)
	if err != nil {
		return err
	}
	defer cancel()
	defer response.Body.Close()
	return checkStatus(response.StatusCode, http.StatusNoContent)
}
func (t *Transport) Publish(ctx context.Context, batch domain.ObservationBatch) error {
	if batch.Agent != t.agent || batch.Sequence == 0 || len(batch.Endpoints)+len(batch.Tombstones) > runtimecatalog.MaxBatch {
		return runtimecatalog.ErrInvalid
	}
	payload, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	if len(payload) > domain.MaxObservationBytes {
		return runtimecatalog.ErrLimit
	}
	response, cancel, err := t.request(ctx, http.MethodPost, domain.RuntimeObservationPath, payload, 10*time.Second)
	if err != nil {
		return err
	}
	defer cancel()
	defer response.Body.Close()
	return checkStatus(response.StatusCode, http.StatusNoContent)
}

// Watch delivers complete envelopes to the policy verifier. Transport checks
// are not signature or grant verification and cannot authorize routing. Receive
// must honor its context and verify the signed envelope before any activation.
func (t *Transport) Watch(ctx context.Context, receive func(context.Context, domain.Snapshot) error) error {
	if receive == nil {
		return runtimecatalog.ErrInvalid
	}
	response, cancel, err := t.request(ctx, http.MethodGet, domain.RuntimeWatchPath, nil, domain.RuntimeStreamLifetime)
	if err != nil {
		return err
	}
	defer cancel()
	defer response.Body.Close()
	// A watch can lose its catalog/policy version while the signed snapshot is
	// assembled. Reconnect without renewing the installed lease. Observation
	// and enrollment conflicts still use the terminal replay classification.
	if response.StatusCode == http.StatusConflict {
		return ErrUnavailable
	}
	if statusErr := checkStatus(response.StatusCode, http.StatusOK); statusErr != nil {
		return statusErr
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/x-ndjson" {
		return runtimecatalog.ErrInvalid
	}
	reader := bufio.NewReaderSize(response.Body, 64<<10)
	for range 128 {
		frame, err := readFrame(reader)
		if err != nil {
			return err
		}
		if peerErr := t.verifyControl(response.TLS); peerErr != nil {
			return peerErr
		}
		if _, credentialErr := t.credentialDeadline(domain.RuntimeStreamLifetime); credentialErr != nil {
			return credentialErr
		}
		var snapshot domain.Snapshot
		decoder := json.NewDecoder(bytes.NewReader(frame))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&snapshot); err != nil {
			return runtimecatalog.ErrInvalid
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return runtimecatalog.ErrInvalid
		}
		now := time.Now()
		if !snapshot.Full || snapshot.NextCursor != "" || snapshot.Scope != t.agent.Scope || snapshot.DestinationID != t.agent.DestinationID || snapshot.Epoch != t.agent.Epoch || !snapshot.ExpiresAt.After(now) || snapshot.IssuedAt.After(now.Add(domain.MaxClockSkew)) || !snapshot.ExpiresAt.After(snapshot.IssuedAt) || snapshot.ExpiresAt.Sub(snapshot.IssuedAt) > runtimecatalog.EndpointLease || len(snapshot.Endpoints)+len(snapshot.Tombstones) > runtimecatalog.MaxSnapshot {
			return runtimecatalog.ErrInvalid
		}
		delivery, stop := context.WithDeadline(response.Request.Context(), snapshot.ExpiresAt)
		receiveErr := delivery.Err()
		if receiveErr == nil {
			receiveErr = receive(delivery, snapshot)
		}
		stop()
		if receiveErr != nil {
			return receiveErr
		}
	}
	return runtimecatalog.ErrLimit
}
func readFrame(reader *bufio.Reader) ([]byte, error) {
	var frame []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(frame)+len(part) > domain.MaxSnapshotBytes+1 {
			return nil, runtimecatalog.ErrLimit
		}
		frame = append(frame, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if len(frame) > 0 {
				return nil, runtimecatalog.ErrInvalid
			}
			return nil, err
		}
		return frame[:len(frame)-1], nil
	}
}
func checkStatus(got, want int) error {
	if got == want {
		return nil
	}
	switch got {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrClosed
	case http.StatusConflict:
		return runtimecatalog.ErrStale
	case http.StatusRequestEntityTooLarge:
		return runtimecatalog.ErrLimit
	case http.StatusTooManyRequests:
		return ErrUnavailable
	}
	if got >= 500 {
		return fmt.Errorf("%w: HTTP %d", ErrUnavailable, got)
	}
	return runtimecatalog.ErrInvalid
}

// verifyControl rechecks trust and validity for each delivered frame, including
// on a connection established before a trust-bundle update.
func (t *Transport) verifyControl(state *tls.ConnectionState) error {
	if state == nil || !state.HandshakeComplete || state.DidResume || state.Version < tls.VersionTLS13 || len(state.PeerCertificates) == 0 || len(state.PeerCertificates) > 8 {
		return ErrClosed
	}
	now := time.Now()
	raw := make([][]byte, 0, len(state.PeerCertificates))
	for _, cert := range state.PeerCertificates {
		if cert == nil || cert.NotBefore.After(now) || !cert.NotAfter.After(now) {
			return ErrClosed
		}
		raw = append(raw, cert.Raw)
	}
	if state.PeerCertificates[0].NotAfter.After(now.Add(workloadidentity.MaxCredentialLifetime)) {
		return ErrClosed
	}
	if t.tls.VerifyPeerCertificate != nil {
		if err := t.tls.VerifyPeerCertificate(raw, [][]*x509.Certificate{state.PeerCertificates}); err != nil {
			return errors.Join(ErrClosed, err)
		}
	}
	if t.tls.VerifyConnection != nil {
		if err := t.tls.VerifyConnection(*state); err != nil {
			return errors.Join(ErrClosed, err)
		}
	}
	return nil
}

// ErrAcknowledgmentStale asks the agent to reconnect for a newer snapshot. It
// does not revoke enrollment or extend the policy already installed locally.
var ErrAcknowledgmentStale = errors.New("snapshot acknowledgment is no longer current")

func (t *Transport) Acknowledge(ctx context.Context, ack domain.Acknowledgment) error {
	if ack.Agent != t.agent || ack.Sequence == 0 || ack.Revision == "" || ack.AppliedAt.IsZero() {
		return runtimecatalog.ErrInvalid
	}
	payload, err := json.Marshal(ack)
	if err != nil {
		return err
	}
	if len(payload) > domain.MaxAcknowledgmentBytes {
		return runtimecatalog.ErrLimit
	}
	response, cancel, err := t.request(ctx, http.MethodPost, domain.RuntimeAcknowledgmentPath, payload, 10*time.Second)
	if err != nil {
		return err
	}
	defer cancel()
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		return ErrAcknowledgmentStale
	}
	return checkStatus(response.StatusCode, http.StatusNoContent)
}
