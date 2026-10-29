package cloudinfra

import (
	"strings"
	"time"
)

// DomainState is what the Domains page shows for a hostname's DNS,
// certificate and route.
type DomainState string

const (
	DomainAwaitingVerification DomainState = "awaiting_verification"
	DomainProvisioning         DomainState = "provisioning"
	DomainReady                DomainState = "ready"
	DomainFailed               DomainState = "failed"
	DomainRenewing             DomainState = "renewing"
	DomainCleanupPending       DomainState = "cleanup_pending"
	// DomainProofMissing is a verified zone whose record a check found gone.
	DomainProofMissing DomainState = "proof_missing"
)

const (
	RouteBackendServer     = "server"
	RouteBackendKubernetes = "kubernetes"
	// RouteBackendPlatform is a hostname on an app the provider's platform
	// routes itself, such as DigitalOcean App Platform.
	RouteBackendPlatform = "platform"
)

// certificateRenewalWindow is when a valid certificate starts reading as renewing.
const certificateRenewalWindow = 14 * 24 * time.Hour

// BackendName reads a route written before backends existed as a server route.
func (b RouteBinding) BackendName() string {
	if b.Backend == "" {
		return RouteBackendServer
	}
	return b.Backend
}

type RouteStatus struct {
	RouteID              string      `json:"route_id"`
	Hostname             string      `json:"hostname"`
	Wildcard             bool        `json:"wildcard"`
	Backend              string      `json:"backend"`
	DNS                  DomainState `json:"dns"`
	Certificate          DomainState `json:"certificate"`
	Route                DomainState `json:"route"`
	Message              string      `json:"message,omitempty"`
	CertificateExpiresAt time.Time   `json:"certificate_expires_at"`
	CheckedAt            time.Time   `json:"checked_at"`
	OperationID          string      `json:"operation_id,omitempty"`
	Retryable            bool        `json:"retryable"`
}

// DeriveRouteStatus reads a route's states from its last check and newest
// operation. A nil zone reads as unverified.
func DeriveRouteStatus(route Route, zone *Zone, latest *RouteOperation, now time.Time) RouteStatus {
	v := route.Verification
	out := RouteStatus{RouteID: route.ID, Hostname: route.Input.Hostname, Wildcard: strings.HasPrefix(route.Input.Hostname, "*."), Backend: route.Binding.BackendName(),
		CertificateExpiresAt: v.CertExpires, CheckedAt: v.CheckedAt, OperationID: route.OperationID}
	message := ""
	if latest != nil {
		out.OperationID, message = latest.ID, latest.Message
	}
	for _, next := range []string{v.DNSError, v.TLSError, v.BackendError} {
		if message == "" {
			message = next
		}
	}
	out.Message = message
	all := func(state DomainState) RouteStatus {
		out.DNS, out.Certificate, out.Route = state, state, state
		return out
	}
	switch {
	case latest != nil && latest.Kind == "remove" && latest.Status != "succeeded":
		return all(DomainCleanupPending)
	case zone == nil || !zone.verified():
		return all(DomainAwaitingVerification)
	case latest != nil && (latest.Status == "pending" || latest.Status == "running") && len(latest.receipts()) == 0 && zone.Mode == "managed":
		return all(DomainProvisioning)
	}
	out.DNS, out.Certificate, out.Route = DomainProvisioning, DomainProvisioning, DomainProvisioning
	switch {
	case v.DNS:
		out.DNS = DomainReady
	case zone.Mode == "external":
		// Nobody but the zone's owner can create the record.
		out.DNS = DomainAwaitingVerification
	}
	if v.TLS {
		out.Certificate = DomainReady
		// A certificate whose expiry wasn't read isn't renewing.
		if v.CertState == "issuing" || (!v.CertExpires.IsZero() && v.CertExpires.Before(now.Add(certificateRenewalWindow))) {
			out.Certificate = DomainRenewing
		}
	}
	if route.Status == "ready" {
		out.Route = DomainReady
	}
	if latest != nil && latest.Status == "failed" {
		if v.DNSError != "" {
			out.DNS = DomainFailed
		}
		if v.TLSError != "" {
			out.Certificate = DomainFailed
		}
		out.Route, out.Retryable = DomainFailed, true
	}
	return out
}
