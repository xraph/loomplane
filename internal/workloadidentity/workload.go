package workloadidentity

import (
	"crypto/x509"
	"fmt"
	"strings"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// WorkloadURI identifies an application service. Agent and control identities
// use separate paths and cannot authenticate as this service.
func WorkloadURI(trustDomain string, selector domain.WorkloadSelector) (string, error) {
	td, err := spiffeid.TrustDomainFromString(trustDomain)
	if err != nil || td.Name() != trustDomain || !selector.Valid() || !domain.ValidIdentitySegment(selector.DestinationID) || !domain.ValidIdentitySegment(selector.ServiceID) {
		return "", ErrIdentity
	}
	id, err := spiffeid.FromPath(td, fmt.Sprintf("/loomplane/org/%s/project/%s/environment/%s/destination/%s/service/%s", selector.OrgID, selector.ProjectID, selector.EnvironmentID, selector.DestinationID, selector.ServiceID))
	if err != nil {
		return "", ErrIdentity
	}
	return id.String(), nil
}

func ParseWorkloadURI(uri string) (domain.WorkloadSelector, error) {
	id, err := spiffeid.FromString(uri)
	if err != nil {
		return domain.WorkloadSelector{}, ErrIdentity
	}
	p := strings.Split(id.Path(), "/")
	if len(p) != 12 || p[0] != "" || p[1] != "loomplane" || p[2] != "org" || p[4] != "project" || p[6] != "environment" || p[8] != "destination" || p[10] != "service" {
		return domain.WorkloadSelector{}, ErrIdentity
	}
	selector := domain.WorkloadSelector{Scope: domain.Scope{OrgID: p[3], ProjectID: p[5], EnvironmentID: p[7]}, DestinationID: p[9], ServiceID: p[11]}
	canonical, err := WorkloadURI(id.TrustDomain().Name(), selector)
	if err != nil || canonical != uri {
		return domain.WorkloadSelector{}, ErrIdentity
	}
	return selector, nil
}

// VerifyWorkloadPeer checks current private trust and organization scope on each
// request. The resulting identity still needs a current service access grant.
func VerifyWorkloadPeer(trust TrustSource, chain []*x509.Certificate, org string, now time.Time) (domain.WorkloadSelector, error) {
	if trust == nil || !domain.ValidIdentitySegment(org) || now.IsZero() {
		return domain.WorkloadSelector{}, ErrIdentity
	}
	id, err := verifyPeerSVID(trust, chain, trust.Domain(), now)
	if err != nil {
		return domain.WorkloadSelector{}, ErrIdentity
	}
	selector, err := ParseWorkloadURI(id.String())
	if err != nil || selector.OrgID != org {
		return domain.WorkloadSelector{}, ErrIdentity
	}
	return selector, nil
}
