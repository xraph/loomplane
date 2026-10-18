package runtimeapi

import (
	"crypto/tls"
	"net/http"
	"time"

	"github.com/xraph/loomplane/internal/workloadidentity"
)

// NewServer supplies transport limits for the dedicated agent listener. Call
// ServeTLS with the control-plane Workload API source's dynamic TLS config.
func NewServer(address string, handler *Handler, config *tls.Config) (*http.Server, error) {
	if handler == nil || config == nil || config.MinVersion < tls.VersionTLS13 || !config.SessionTicketsDisabled || (config.ClientAuth != tls.RequireAnyClientCert && config.ClientAuth != tls.RequireAndVerifyClientCert) {
		return nil, workloadidentity.ErrIdentity
	}
	return &http.Server{Addr: address, Handler: handler, TLSConfig: config.Clone(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}, nil
}
