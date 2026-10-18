package planning

import (
	"net/netip"
	"slices"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// NetworkProof is persisted evidence for one directed, scoped network edge.
// An authenticated observer supplies it; manifest declarations cannot create it.
type NetworkProof struct {
	domain.Scope
	SourceIdentity, TargetIdentity string
	From, To                       string
	CheckedAt, ExpiresAt           time.Time
	SourceCIDRs, TargetCIDRs       []string
}

// PolicyProof confirms installation of one approved grant at both gateways.
type PolicyProof struct {
	domain.Scope
	SourceID, TargetID, GrantID, GrantRevision, ExportRevision string
	ExpiresAt                                                  time.Time
}

// ReachabilityInput contains observations and authority loaded from scoped stores.
// This pure evaluator never contacts a provider or turns a declaration into proof.
type ReachabilityInput struct {
	Scope           domain.Scope
	Source, Target  *domain.Destination
	SourceServiceID string
	Export          domain.ServiceExport
	Grant           *domain.AccessGrant
	Network         []NetworkProof
	Policy          PolicyProof
	Endpoints       []domain.Endpoint
	Now             time.Time
}

// ReachabilityResult separates the requirements that make a call available.
type ReachabilityResult struct {
	Network, DNS, Identity, Policy, Readiness, Available bool
	Findings                                             []Finding
}

// EvaluateReachability requires every enforcement layer, in the exact scope.
func EvaluateReachability(in ReachabilityInput) ReachabilityResult {
	out := ReachabilityResult{Findings: []Finding{}}
	fail := func(code, message string) { out.Findings = append(out.Findings, Finding{Code: code, Message: message}) }
	if in.Source == nil || in.Target == nil || in.Scope.OrgID == "" || in.Scope.ProjectID == "" || in.Scope.EnvironmentID == "" || in.Source.Scope != in.Scope || in.Target.Scope != in.Scope || in.Export.Scope != in.Scope || in.Now.IsZero() || in.Source.Quarantined || in.Target.Quarantined {
		fail("federation-scope", "Destinations and export must belong to the selected scope and be out of quarantine.")
		return out
	}
	supports := func(d *domain.Destination, c domain.Capability) bool {
		return d.Capabilities.Supports(c, d.Identity.Revision(), in.Now)
	}
	out.Network = directedPath(in.Network, in.Scope, in.Source, in.Target, in.Now) && supports(in.Source, domain.ImportedNetwork) && supports(in.Target, domain.ImportedNetwork)
	if !out.Network {
		fail("federation-network", "No fresh directed path with non-overlapping networks has been verified.")
	}
	out.DNS = supports(in.Source, domain.StableDNS) && supports(in.Target, domain.StableDNS)
	if !out.DNS {
		fail("federation-dns", "Stable service resolution has not been verified at both destinations.")
	}
	// The only grant principal currently modeled is an exact workload.
	// A gateway certificate cannot establish that workload identity.
	identity := domain.WorkloadAttestation
	protocol := map[domain.Protocol]domain.Capability{domain.HTTP: domain.HTTPProtocol, domain.GRPC: domain.GRPCProtocol, domain.TCP: domain.TCPProtocol}[in.Export.Protocol]
	out.Identity = in.Export.IdentityProfile == domain.WorkloadIdentity && protocol != "" && supports(in.Source, identity) && supports(in.Target, identity) && supports(in.Target, domain.BackendIsolation) && supports(in.Source, protocol) && supports(in.Target, protocol)
	if !out.Identity {
		fail("federation-identity", "The required identity, private backend isolation and protocol profile are not qualified.")
	}
	g := in.Grant
	out.Policy = g != nil && g.Scope == in.Scope && g.Source.Scope == in.Scope && g.Source.DestinationID == in.Source.ID && g.Source.ServiceID == in.SourceServiceID && in.SourceServiceID != "" && g.ExportRevision != "" && g.ExportRevision == ServiceExportRevision(in.Export) && g.ExportID == in.Export.ID && in.Export.ID != "" && g.Port == in.Export.Port && g.Port > 0 && g.Protocol == in.Export.Protocol && slices.Contains(in.Export.DestinationIDs, in.Target.ID) && g.ApprovedRevision != "" && g.ApprovedRevision == AccessGrantRevision(*g) && g.RevokedAt == nil && (g.ExpiresAt == nil || g.ExpiresAt.After(in.Now)) && in.Policy.Scope == in.Scope && in.Policy.SourceID == in.Source.ID && in.Policy.TargetID == in.Target.ID && in.Policy.GrantID == g.ID && in.Policy.GrantRevision == g.ApprovedRevision && in.Policy.ExportRevision == g.ExportRevision && in.Policy.ExpiresAt.After(in.Now) && supports(in.Source, domain.PolicyEnforcement) && supports(in.Target, domain.PolicyEnforcement)
	if !out.Policy {
		fail("federation-policy", "An unexpired approved grant and matching gateway acknowledgments are required; needs and exports grant no access.")
	}
	for _, endpoint := range in.Endpoints {
		address, addressErr := netip.ParseAddr(endpoint.Address)
		if addressErr == nil && !address.IsUnspecified() && !address.IsMulticast() && endpoint.DestinationID == in.Target.ID && endpoint.ObserverID != "" && endpoint.Scope == in.Scope && endpoint.ExportID == in.Export.ID && endpoint.Port == in.Export.Port && endpoint.Readiness == domain.Ready && !endpoint.ObservedAt.IsZero() && !endpoint.ObservedAt.After(in.Now) && endpoint.ExpiresAt.After(in.Now) {
			out.Readiness = true
			break
		}
	}
	if !out.Readiness {
		fail("federation-readiness", "No ready endpoint with a current observation lease exists.")
	}
	out.Available = out.Network && out.DNS && out.Identity && out.Policy && out.Readiness
	return out
}

// Only direct proofs are qualified in the foundation. Multi-hop routing needs
// consistent prefix ownership and an end-to-end forwarding/translation proof;
// concatenating independently valid tunnels cannot establish either.
func directedPath(edges []NetworkProof, scope domain.Scope, source, target *domain.Destination, now time.Time) bool {
	if source.ID == "" || target.ID == "" || source.ID == target.ID {
		return false
	}
	for _, e := range edges {
		if e.Scope == scope && e.From == source.ID && e.To == target.ID &&
			e.SourceIdentity == source.Identity.Revision() && e.TargetIdentity == target.Identity.Revision() &&
			!e.CheckedAt.IsZero() && !e.CheckedAt.After(now) && e.ExpiresAt.After(now) &&
			separateNetworks(e.SourceCIDRs, e.TargetCIDRs) {
			return true
		}
	}
	return false
}

func separateNetworks(source, target []string) bool {
	if len(source) == 0 || len(target) == 0 {
		return false
	}
	prefixes := func(values []string) ([]netip.Prefix, bool) {
		out := make([]netip.Prefix, 0, len(values))
		for _, value := range values {
			p, err := netip.ParsePrefix(value)
			if err != nil || p != p.Masked() {
				return nil, false
			}
			out = append(out, p)
		}
		return out, true
	}
	a, ok := prefixes(source)
	if !ok {
		return false
	}
	b, ok := prefixes(target)
	if !ok {
		return false
	}
	for _, left := range a {
		for _, right := range b {
			if left.Overlaps(right) {
				return false
			}
		}
	}
	return true
}

// AccessGrantRevision binds approval to the exact desired grant and generation.
// Audit timestamps and the approval itself do not participate in its digest.
func AccessGrantRevision(grant domain.AccessGrant) string {
	grant.ApprovedRevision = ""
	grant.CreatedAt = time.Time{}
	grant.UpdatedAt = time.Time{}
	grant.RevokedAt = nil
	revision, err := Digest(grant)
	if err != nil {
		return ""
	}
	return revision
}

// ServiceExportRevision binds a reviewed service identity, placement and protocol
// to its generation. Observation/audit timestamps do not change that contract.
func ServiceExportRevision(export domain.ServiceExport) string {
	export.CreatedAt = time.Time{}
	export.UpdatedAt = time.Time{}
	revision, err := Digest(export)
	if err != nil {
		return ""
	}
	return revision
}
