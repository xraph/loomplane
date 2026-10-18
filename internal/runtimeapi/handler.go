// Package runtimeapi serves the separate, mutually authenticated agent endpoint.
// It has no management routes and accepts no bearer or forwarded identity.
package runtimeapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

const maxRequests = 128

type EnrollmentAuthority interface {
	CheckPending(context.Context, string, domain.AgentIdentity) error
	Consume(context.Context, string, domain.AgentIdentity) error
}
type Catalog interface {
	Observe(context.Context, domain.AgentIdentity, domain.ObservationBatch) error
	Watch(context.Context, domain.AgentIdentity, runtimecatalog.WakeSource, func(context.Context, *domain.Snapshot) error) error
}
type Handler struct {
	org         string
	trust       workloadidentity.TrustSource
	registry    workloadidentity.Registry
	enrollments EnrollmentAuthority
	catalog     Catalog
	wake        runtimecatalog.WakeSource
	mux         *http.ServeMux
	slots       chan struct{}
	mu          sync.Mutex
	watches     map[string]bool
}

func NewHandler(org string, trust workloadidentity.TrustSource, registry workloadidentity.Registry, enrollments EnrollmentAuthority, catalog Catalog, wake runtimecatalog.WakeSource) (*Handler, error) {
	if trust == nil || registry == nil || enrollments == nil || catalog == nil || wake == nil {
		return nil, workloadidentity.ErrIdentity
	}
	if _, err := workloadidentity.AgentURI(trust.Domain(), domain.AgentIdentity{Scope: domain.Scope{OrgID: org, ProjectID: "check", EnvironmentID: "check"}, DestinationID: "check", AgentID: "check", Epoch: 1}); err != nil {
		return nil, err
	}
	h := &Handler{org: org, trust: trust, registry: registry, enrollments: enrollments, catalog: catalog, wake: wake, mux: http.NewServeMux(), slots: make(chan struct{}, maxRequests), watches: map[string]bool{}}
	h.mux.HandleFunc("POST "+domain.RuntimeEnrollmentPath+"{id}", h.enroll)
	h.mux.HandleFunc("POST "+domain.RuntimeObservationPath, h.observe)
	h.mux.HandleFunc("GET "+domain.RuntimeWatchPath, h.watch)
	return h, nil
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		http.Error(w, "runtime request limit", http.StatusServiceUnavailable)
		return
	}
	h.mux.ServeHTTP(w, r)
}
func (h *Handler) authenticate(ctx context.Context, r *http.Request, registry workloadidentity.Registry) (domain.AgentIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if r.TLS == nil || !r.TLS.HandshakeComplete || r.TLS.DidResume || r.TLS.Version < tls.VersionTLS13 {
		return domain.AgentIdentity{}, errTransport
	}
	a, err := workloadidentity.NewVerifier(h.trust, registry).VerifyPeer(ctx, r.TLS.PeerCertificates, h.trust.Domain())
	if err != nil || a.OrgID != h.org {
		return domain.AgentIdentity{}, workloadidentity.ErrIdentity
	}
	return a, nil
}

var errTransport = errors.New("mutual TLS required")

type pendingRegistry struct {
	authority EnrollmentAuthority
	id        string
}

func (p pendingRegistry) CheckAgent(ctx context.Context, a domain.AgentIdentity) error {
	return p.authority.CheckPending(ctx, p.id, a)
}
func (h *Handler) enroll(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r, 10*time.Second)
	defer cancel()
	a, err := h.authenticate(ctx, r, pendingRegistry{h.enrollments, r.PathValue("id")})
	if err != nil {
		writeError(w, err)
		return
	}
	if err = h.enrollments.Consume(ctx, r.PathValue("id"), a); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) observe(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r, 10*time.Second)
	defer cancel()
	a, err := h.authenticate(ctx, r, h.registry)
	if err != nil {
		writeError(w, err)
		return
	}
	deadline, _ := ctx.Deadline()
	if deadlineErr := http.NewResponseController(w).SetReadDeadline(deadline); deadlineErr != nil {
		writeError(w, deadlineErr)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, domain.MaxObservationBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var batch domain.ObservationBatch
	if err = decoder.Decode(&batch); err != nil {
		writeDecodeError(w, err)
		return
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		http.Error(w, "one observation batch required", http.StatusBadRequest)
		return
	}
	if a != batch.Agent {
		writeError(w, workloadidentity.ErrIdentity)
		return
	}
	// The body read may outlive a certificate or registry update. Check again.
	if _, err = h.authenticate(ctx, r, h.registry); err != nil {
		writeError(w, err)
		return
	}
	if err = h.catalog.Observe(ctx, a, batch); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) watch(w http.ResponseWriter, r *http.Request) {
	a, err := h.authenticate(r.Context(), r, h.registry)
	if err != nil {
		writeError(w, err)
		return
	}
	h.mu.Lock()
	busy := h.watches[a.URI]
	if !busy {
		h.watches[a.URI] = true
	}
	h.mu.Unlock()
	if busy {
		http.Error(w, "observer watch already active", http.StatusTooManyRequests)
		return
	}
	defer func() { h.mu.Lock(); delete(h.watches, a.URI); h.mu.Unlock() }()
	deadline := time.Now().Add(domain.RuntimeStreamLifetime)
	for _, cert := range r.TLS.PeerCertificates {
		if cert.NotAfter.Before(deadline) {
			deadline = cert.NotAfter
		}
	}
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	controller := http.NewResponseController(w)
	wrote := false
	err = h.catalog.Watch(ctx, a, h.wake, func(delivery context.Context, snapshot *domain.Snapshot) error {
		if snapshot == nil || snapshot.Scope != a.Scope || snapshot.DestinationID != a.DestinationID || snapshot.Epoch != a.Epoch || !snapshot.ExpiresAt.After(time.Now()) {
			return runtimecatalog.ErrInvalid
		}
		if _, verifyErr := h.authenticate(delivery, r, h.registry); verifyErr != nil {
			return verifyErr
		}
		payload, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			return marshalErr
		}
		if len(payload) > domain.MaxSnapshotBytes {
			return runtimecatalog.ErrLimit
		}
		until := deadline
		if d, ok := delivery.Deadline(); ok && d.Before(until) {
			until = d
		}
		if snapshot.ExpiresAt.Before(until) {
			until = snapshot.ExpiresAt
		}
		if deadlineErr := controller.SetWriteDeadline(until); deadlineErr != nil {
			return deadlineErr
		}
		if deliveryErr := delivery.Err(); deliveryErr != nil {
			return deliveryErr
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		wrote = true
		if _, writeErr := w.Write(append(payload, '\n')); writeErr != nil {
			return writeErr
		}
		return controller.Flush()
	})
	if !wrote && err != nil {
		writeError(w, err)
	}
}
func writeDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, "observation batch too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "invalid observation batch", http.StatusBadRequest)
}
func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, errTransport):
		status = http.StatusUnauthorized
	case errors.Is(err, workloadidentity.ErrIdentity), errors.Is(err, runtimecatalog.ErrUnauthorized):
		status = http.StatusForbidden
	case errors.Is(err, runtimecatalog.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, runtimecatalog.ErrStale), errors.Is(err, runtimecatalog.ErrCursorExpired):
		status = http.StatusConflict
	case errors.Is(err, runtimecatalog.ErrLimit):
		status = http.StatusTooManyRequests
	}
	http.Error(w, http.StatusText(status), status)
}

// requestContext never lets a read or mutation retain the peer's authority past
// the certificate chain's expiry, even while waiting for a database lock.
func requestContext(r *http.Request, limit time.Duration) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(limit)
	if r.TLS != nil {
		for _, cert := range r.TLS.PeerCertificates {
			if cert != nil && cert.NotAfter.Before(deadline) {
				deadline = cert.NotAfter
			}
		}
	}
	return context.WithDeadline(r.Context(), deadline)
}
