package planning

import (
	"testing"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
)

func reachabilityFixture() ReachabilityInput {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	scope := domain.Scope{OrgID: "o", ProjectID: "p", EnvironmentID: "e"}
	destination := func(id string) *domain.Destination {
		d := &domain.Destination{Scope: scope, ID: id, Identity: domain.PhysicalIdentity{Provider: "kubernetes", Account: "cluster-" + id, ResourceID: "ns-" + id}, Generation: 1}
		d.Capabilities = domain.CapabilitySet{Revision: "qualified", IdentityRevision: d.Identity.Revision(), CheckedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute), Results: map[domain.Capability]domain.CapabilityResult{}}
		for _, c := range []domain.Capability{domain.ImportedNetwork, domain.StableDNS, domain.WorkloadAttestation, domain.BackendIsolation, domain.PolicyEnforcement, domain.HTTPProtocol} {
			d.Capabilities.Results[c] = domain.CapabilityResult{State: domain.Supported}
		}
		return d
	}
	g := &domain.AccessGrant{Metadata: domain.Metadata{Scope: scope, ID: "g", Generation: 1}, Source: domain.WorkloadSelector{Scope: scope, DestinationID: "a", ServiceID: "caller"}, ExportID: "export", Port: 8080, Protocol: domain.HTTP, ApprovedRevision: "approved"}
	in := ReachabilityInput{Scope: scope, Now: now, Source: destination("a"), Target: destination("b"), SourceServiceID: "caller", Export: domain.ServiceExport{Metadata: domain.Metadata{Scope: scope, ID: "export", Generation: 1}, ServiceID: "backend", DestinationIDs: []string{"b"}, Port: 8080, Protocol: domain.HTTP, IdentityProfile: domain.WorkloadIdentity}, Grant: g,
		Network:   []NetworkProof{{Scope: scope, From: "a", To: "b", CheckedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute), SourceCIDRs: []string{"10.0.0.0/24"}, TargetCIDRs: []string{"10.1.0.0/24"}}},
		Policy:    PolicyProof{Scope: scope, SourceID: "a", TargetID: "b", GrantID: "g", GrantRevision: "approved", ExpiresAt: now.Add(time.Minute)},
		Endpoints: []domain.Endpoint{{Metadata: domain.Metadata{Scope: scope}, ExportID: "export", Port: 8080, Readiness: domain.Ready, ObservedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute)}}}
	in.Grant.ExportRevision = ServiceExportRevision(in.Export)
	in.Policy.ExportRevision = in.Grant.ExportRevision
	in.Grant.ApprovedRevision = AccessGrantRevision(*in.Grant)
	in.Policy.GrantRevision = in.Grant.ApprovedRevision
	in.Network[0].SourceIdentity = in.Source.Identity.Revision()
	in.Network[0].TargetIdentity = in.Target.Identity.Revision()
	in.Endpoints[0].DestinationID = "b"
	in.Endpoints[0].Address = "10.1.0.2"
	in.Endpoints[0].ObserverID = "agent-b"
	return in
}

func TestReachabilityRequiresEveryLayer(t *testing.T) {
	if got := EvaluateReachability(reachabilityFixture()); !got.Available {
		t.Fatalf("qualified: %+v", got)
	}
	for _, tc := range []struct {
		name   string
		change func(*ReachabilityInput)
	}{
		{"no network", func(in *ReachabilityInput) { in.Network = nil }},
		{"reverse only", func(in *ReachabilityInput) { in.Network[0].From = "b"; in.Network[0].To = "a" }},
		{"overlap", func(in *ReachabilityInput) { in.Network[0].TargetCIDRs = []string{"10.0.0.0/25"} }},
		{"ipv6 overlap", func(in *ReachabilityInput) {
			in.Network[0].SourceCIDRs = []string{"fd00::/64"}
			in.Network[0].TargetCIDRs = []string{"fd00::/80"}
		}},
		{"changed export service", func(in *ReachabilityInput) { in.Export.ServiceID = "replacement" }},
		{"changed export generation", func(in *ReachabilityInput) { in.Export.Generation++ }},
		{"gateway export cannot authenticate workload", func(in *ReachabilityInput) {
			in.Export.IdentityProfile = domain.GatewayIdentity
			in.Grant.ExportRevision = ServiceExportRevision(in.Export)
			in.Grant.ApprovedRevision = AccessGrantRevision(*in.Grant)
			in.Policy.GrantRevision = in.Grant.ApprovedRevision
			in.Policy.ExportRevision = in.Grant.ExportRevision
			for _, d := range []*domain.Destination{in.Source, in.Target} {
				delete(d.Capabilities.Results, domain.WorkloadAttestation)
				d.Capabilities.Results[domain.GatewayAttestation] = domain.CapabilityResult{State: domain.Supported}
			}
		}},
		{"multi hop overlapping endpoints", func(in *ReachabilityInput) {
			middle := reachabilityFixture().Source
			middle.ID = "middle"
			first, second := in.Network[0], in.Network[0]
			first.To, first.TargetIdentity = middle.ID, middle.Identity.Revision()
			second.From, second.SourceIdentity = middle.ID, middle.Identity.Revision()
			second.SourceCIDRs, second.TargetCIDRs = first.TargetCIDRs, first.SourceCIDRs
			in.Network = []NetworkProof{first, second}
		}},
		{"changed grant", func(in *ReachabilityInput) { in.Grant.Generation++ }},
		{"wrong endpoint destination", func(in *ReachabilityInput) { in.Endpoints[0].DestinationID = "neighbor" }},
		{"old network identity", func(in *ReachabilityInput) { in.Network[0].TargetIdentity = "old" }},
		{"no grant", func(in *ReachabilityInput) { in.Grant = nil }},
		{"stale export acknowledgment", func(in *ReachabilityInput) { in.Policy.ExportRevision = "old" }},
		{"stale approval", func(in *ReachabilityInput) { in.Policy.GrantRevision = "old" }},
		{"expired policy", func(in *ReachabilityInput) { in.Policy.ExpiresAt = in.Now }},
		{"no dns", func(in *ReachabilityInput) { delete(in.Target.Capabilities.Results, domain.StableDNS) }},
		{"gateway only", func(in *ReachabilityInput) {
			delete(in.Source.Capabilities.Results, domain.WorkloadAttestation)
			in.Source.Capabilities.Results[domain.GatewayAttestation] = domain.CapabilityResult{State: domain.Supported}
		}},
		{"public bypass", func(in *ReachabilityInput) { delete(in.Target.Capabilities.Results, domain.BackendIsolation) }},
		{"stale endpoints", func(in *ReachabilityInput) { in.Endpoints[0].ExpiresAt = in.Now }},
		{"cross tenant", func(in *ReachabilityInput) { in.Source.OrgID = "foreign" }},
		{"quarantined", func(in *ReachabilityInput) { in.Source.Quarantined = true }},
		{"unknown source service", func(in *ReachabilityInput) { in.SourceServiceID = "neighbor" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := reachabilityFixture()
			tc.change(&in)
			if got := EvaluateReachability(in); got.Available || len(got.Findings) == 0 {
				t.Fatalf("accepted: %+v", got)
			}
		})
	}
}

func TestNeedsDoesNotGrantAccess(t *testing.T) {
	in := reachabilityFixture()
	in.Grant = nil
	if got := EvaluateReachability(in); got.Policy || got.Available {
		t.Fatal("network or export granted access")
	}
}
