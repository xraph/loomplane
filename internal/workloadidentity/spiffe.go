// Package workloadidentity verifies SPIFFE credentials and binds them to active
// Loomplane enrollment. Certificates authenticate a peer, not service access.
package workloadidentity

import (
	"context"
	"crypto/x509"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"

	domain "github.com/xraph/loomplane/pkg/federation"
)

const MaxCredentialLifetime = time.Hour

// AgentURI is derived from the enrollment record, never from a requested selector.
func AgentURI(trustDomain string, a domain.AgentIdentity) (string, error) {
	td, err := spiffeid.TrustDomainFromString(trustDomain)
	if err != nil || td.Name() != trustDomain || a.Epoch == 0 {
		return "", ErrIdentity
	}
	for _, part := range []string{a.OrgID, a.ProjectID, a.EnvironmentID, a.DestinationID, a.AgentID} {
		if !domain.ValidIdentitySegment(part) {
			return "", ErrIdentity
		}
	}
	id, err := spiffeid.FromPath(td, fmt.Sprintf("/loomplane/org/%s/project/%s/environment/%s/destination/%s/agent/%s/epoch/%d", a.OrgID, a.ProjectID, a.EnvironmentID, a.DestinationID, a.AgentID, a.Epoch))
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
func ParseAgentURI(uri string) (domain.AgentIdentity, error) {
	id, err := spiffeid.FromString(uri)
	if err != nil {
		return domain.AgentIdentity{}, ErrIdentity
	}
	parts := strings.Split(id.Path(), "/")
	if len(parts) != 14 || parts[0] != "" || parts[1] != "loomplane" || parts[2] != "org" || parts[4] != "project" || parts[6] != "environment" || parts[8] != "destination" || parts[10] != "agent" || parts[12] != "epoch" {
		return domain.AgentIdentity{}, ErrIdentity
	}
	epoch, err := strconv.ParseUint(parts[13], 10, 64)
	if err != nil || epoch == 0 {
		return domain.AgentIdentity{}, ErrIdentity
	}
	a := domain.AgentIdentity{Scope: domain.Scope{OrgID: parts[3], ProjectID: parts[5], EnvironmentID: parts[7]}, DestinationID: parts[9], AgentID: parts[11], Epoch: epoch, URI: uri}
	canonical, err := AgentURI(id.TrustDomain().Name(), a)
	if err != nil || canonical != uri {
		return domain.AgentIdentity{}, ErrIdentity
	}
	return a, nil
}

// Registry must reject inactive, quarantined, replaced and stale-epoch agents.
type Registry interface {
	CheckAgent(context.Context, domain.AgentIdentity) error
}

// TrustSource supplies the current bundle, including Workload API rotations.
type TrustSource interface {
	x509bundle.Source
	Domain() string
}
type Verifier struct {
	trust    TrustSource
	registry Registry
	now      func() time.Time
}

func NewVerifier(trust TrustSource, registry Registry) *Verifier {
	return &Verifier{trust: trust, registry: registry, now: time.Now}
}

func (v *Verifier) VerifyPeer(ctx context.Context, chain []*x509.Certificate, expectedDomain string) (domain.AgentIdentity, error) {
	if v.trust == nil || v.registry == nil || v.trust.Domain() != expectedDomain || len(chain) == 0 || len(chain) > 8 {
		return domain.AgentIdentity{}, ErrIdentity
	}
	for _, cert := range chain {
		if cert == nil {
			return domain.AgentIdentity{}, ErrIdentity
		}
	}
	now := v.now()
	leaf := chain[0]
	if leaf.NotAfter.After(now.Add(MaxCredentialLifetime)) || leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 || !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		return domain.AgentIdentity{}, ErrIdentity
	}
	id, _, err := x509svid.Verify(chain, v.trust, x509svid.WithTime(now))
	if err != nil || id.TrustDomain().Name() != expectedDomain {
		return domain.AgentIdentity{}, ErrIdentity
	}
	agent, err := ParseAgentURI(id.String())
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	if err := v.registry.CheckAgent(ctx, agent); err != nil {
		return domain.AgentIdentity{}, ErrIdentity
	}
	return agent, nil
}

const controlPathPrefix = "/loomplane/control/org/"

// ControlURI derives the only control-plane audience this agent may contact.
func ControlURI(a domain.AgentIdentity) (string, error) {
	parsed, err := ParseAgentURI(a.URI)
	if err != nil || parsed != a {
		return "", ErrIdentity
	}
	id, err := spiffeid.FromString(a.URI)
	if err != nil {
		return "", ErrIdentity
	}
	return ControlURIForOrganization(id.TrustDomain().Name(), a.OrgID)
}

// ControlURIForOrganization pins the runtime audience to one trust domain and organization.
func ControlURIForOrganization(trustDomain, org string) (string, error) {
	td, err := spiffeid.TrustDomainFromString(trustDomain)
	if err != nil || td.Name() != trustDomain || !domain.ValidIdentitySegment(org) {
		return "", ErrIdentity
	}
	control, err := spiffeid.FromPath(td, controlPathPrefix+org)
	if err != nil {
		return "", err
	}
	return control.String(), nil
}
