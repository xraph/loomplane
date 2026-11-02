package firecracker

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
)

type Peer struct{ Fingerprint, Org, Connection string }
type Handler struct {
	Host  *Host
	Peers []Peer
}

func (a Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var peer *Peer
	if r.TLS != nil && len(r.TLS.VerifiedChains) > 0 && len(r.TLS.VerifiedChains[0]) > 0 {
		sum := sha256.Sum256(r.TLS.VerifiedChains[0][0].Raw)
		fp := hex.EncodeToString(sum[:])
		for i := range a.Peers {
			if a.Peers[i].Fingerprint == fp {
				peer = &a.Peers[i]
				break
			}
		}
	}
	if peer == nil {
		http.Error(w, "host client identity is not authorized", http.StatusForbidden)
		return
	}
	if r.URL.Path == "/v1/capabilities" && r.Method == http.MethodGet {
		c, err := a.Host.Driver.Check(r.Context())
		if err != nil {
			apiError(w, err)
			return
		}
		c.Version = ProtocolVersion
		c.Limits = a.Host.Limits
		WriteJSON(w, http.StatusOK, c)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var q Request
	if r.URL.Path == "/v1/source" {
		raw, err := base64.RawURLEncoding.DecodeString(r.Header.Get("Loomplane-Request"))
		if err != nil || len(raw) > 32<<10 || Decode(strings.NewReader(string(raw)), &q, 32<<10) != nil {
			apiError(w, ErrInvalid)
			return
		}
	} else if err := Decode(http.MaxBytesReader(w, r.Body, MaxControlBytes), &q, MaxControlBytes); err != nil {
		apiError(w, err)
		return
	}
	if !q.Scope.Valid() || q.Scope.Org != peer.Org || q.Scope.Connection != peer.Connection {
		http.Error(w, "scope is not authorized", http.StatusForbidden)
		return
	}
	var result any
	var err error
	switch r.URL.Path {
	case "/v1/tunnel":
		a.Host.tunnel(w, r, q)
		return
	case "/v1/route":
		result, err = a.Host.ApplyRoute(r.Context(), q)
	case "/v1/apply":
		result, err = a.Host.Apply(r.Context(), q)
	case "/v1/inspect":
		result, err = a.Host.Inspect(r.Context(), q.Scope)
	case "/v1/stop", "/v1/remove":
		result, err = a.Host.Mutate(r.Context(), q, strings.TrimPrefix(r.URL.Path, "/v1/"))
	case "/v1/status", "/v1/logs":
		result, err = a.Host.Guest(r.Context(), q, strings.TrimPrefix(r.URL.Path, "/v1/"), nil)
	case "/v1/source":
		result, err = a.Host.Guest(r.Context(), q, "source", http.MaxBytesReader(w, r.Body, MaxSourceBytes))
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		apiError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, result)
}
func apiError(w http.ResponseWriter, err error) {
	status, message := http.StatusServiceUnavailable, "Firecracker operation failed; inspect the host's operation journal"
	switch {
	case errors.Is(err, ErrInvalid):
		status, message = http.StatusBadRequest, ErrInvalid.Error()
	case errors.Is(err, ErrConflict):
		status, message = http.StatusConflict, ErrConflict.Error()
	case errors.Is(err, ErrMissing):
		status, message = http.StatusNotFound, ErrMissing.Error()
	case errors.Is(err, ErrCapacity):
		status, message = http.StatusConflict, ErrCapacity.Error()
	case errors.Is(err, ErrUnavailable):
		message = ErrUnavailable.Error()
	}
	http.Error(w, message, status)
}
