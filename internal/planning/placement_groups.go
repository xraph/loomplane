package planning

import (
	"fmt"
	"slices"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
	"github.com/xraph/loomplane/pkg/manifest"
)

// GroupMemberFacts are resolved by the scoped store and live observer. API and
// manifest inputs cannot supply these facts.
type GroupMemberFacts struct {
	Destination           *domain.Destination
	Plan                  domain.GroupMemberPlan
	Prerequisite          domain.MemberPrerequisite
	Authority             domain.TLSRenewalAuthority
	Observation           MemberPrerequisiteObservation
	TimeoutConfigRevision string
}

// GroupProfileInput is the bounded review profile, without any runtime adapter.
type GroupProfileInput struct {
	Scope                  domain.Scope
	Enabled                bool
	Manifest               *manifest.Manifest
	Environment            string
	Name                   string
	GroupID                string
	Generation             int64
	ConfigRevision         string
	WritableVolumes        bool
	UnverifiedDependencies bool
	Members                map[string]GroupMemberFacts
	Now                    time.Time
}

// ValidatePlacementGroupProfile never grants permission to execute its result.
func ValidatePlacementGroupProfile(in GroupProfileInput) (domain.QualifiedGroupPlan, error) {
	var out domain.QualifiedGroupPlan
	fail := func(reason string) (domain.QualifiedGroupPlan, error) {
		return out, fmt.Errorf("%w: %s", domain.ErrGroupProfile, reason)
	}
	if !in.Enabled || in.Manifest == nil || in.Manifest.Version != manifest.FederationVersion || in.Manifest.Federation == nil || in.Now.IsZero() {
		return fail("multi_location_delivery is unavailable")
	}
	f := in.Manifest.Federation
	if len(manifest.ValidateFederation(in.Manifest)) != 0 || len(f.Placements) > 0 || len(f.NetworkLinks) > 0 || len(f.ServiceExports) > 0 || len(f.AccessGrants) > 0 || len(in.Manifest.Links) > 0 || len(in.Manifest.Resources) > 0 {
		return fail("unsupported federation intent")
	}
	covered := map[string]bool{}
	for _, group := range f.PlacementGroups {
		covered[group.Service] = true
	}
	for name, service := range in.Manifest.Services {
		if manifest.On(service.For(in.Environment, in.Manifest.Defaults).Enabled) && !covered[name] {
			return fail("every enabled service must use the qualified group profile")
		}
	}
	g, ok := f.PlacementGroups[in.Name]
	if !ok || in.GroupID == "" || in.Generation < 1 || in.ConfigRevision == "" || in.Scope.OrgID == "" || in.Scope.ProjectID == "" || in.Scope.EnvironmentID == "" {
		return fail("missing immutable group identity")
	}
	svc := in.Manifest.Services[g.Service]
	set := svc.For(in.Environment, in.Manifest.Defaults)
	if set.Commands != nil || set.Vercel != nil || svc.Kind != "http-service" || set.Worker != nil || set.PodGroup != nil || !manifest.On(set.Enabled) || set.Port < 1 || set.Build == nil || !domain.ValidPinnedImage(set.Build.Image) || in.WritableVolumes || in.UnverifiedDependencies {
		return fail("requires a stateless HTTP image with verified dependencies")
	}
	// These selectors have single-target semantics. Group members own placement.
	if set.Target != "" || set.CloudTarget != "" || len(set.CloudTags) > 0 || set.Farp != nil || set.Endpoint != nil || set.Region != "" || set.Placement != nil || set.CloudSQLNetwork != nil || set.Scale != nil || (set.Provider != "" && set.Provider != "kubernetes") || len(in.Manifest.Gateways) > 0 {
		return fail("single-target routing or placement cannot be mixed with a group")
	}
	out = domain.QualifiedGroupPlan{Scope: in.Scope, SchemaVersion: 1, GroupID: in.GroupID, Generation: in.Generation, Name: in.Name, Service: g.Service, ConfigRevision: in.ConfigRevision, Image: set.Build.Image, MinReady: g.MinReady, Routing: domain.GroupRoutingReview{Mode: g.Routing.Mode, Hostname: g.Routing.Hostname, ZoneConnectionID: g.Routing.ZoneConnectionID}}
	physical := map[string]bool{}
	locations := map[string]bool{}
	for _, name := range federationKeys(g.Members) {
		member := g.Members[name]
		facts, exists := in.Members[name]
		d := facts.Destination
		p := facts.Prerequisite
		pin := facts.Plan
		a := facts.Authority
		o := facts.Observation
		if !exists || d == nil || d.Scope != in.Scope || d.Quarantined || d.Identity.Provider != "kubernetes" || d.Identity.NamespaceUID == "" || d.Identity.ResourceID != d.Identity.NamespaceUID || d.Generation < 1 || d.ID != f.Destinations[member.Destination].For(in.Environment) {
			return fail("destination scope or identity changed")
		}
		for _, capability := range []domain.Capability{domain.Observe, domain.ImmutableArtifact, domain.HTTPProtocol} {
			if !d.Capabilities.Supports(capability, d.Identity.Revision(), in.Now) {
				return fail("destination capability is unverified")
			}
		}
		key := d.Identity.Account + "/" + d.Identity.NamespaceUID
		if physical[key] {
			return fail("duplicate physical destination")
		}
		physical[key] = true
		if pin.MappingRevision < 1 || pin.TargetID == "" || pin.CloudID == "" || pin.TargetRevision < 1 || pin.GrantRevision < 1 || pin.AttachmentRevision < 1 || pin.ConnectionGeneration < 1 || pin.ConnectionID != d.ConnectionID || pin.LocationID == "" || pin.LocationIdentity == "" || pin.LocationRevision < 1 || pin.LocationEvidenceRevision != pin.LocationRevision {
			return fail("current target grant and verified location are required")
		}
		if p.SchemaVersion != 1 || p.OrgID != in.Scope.OrgID || p.ProjectID != in.Scope.ProjectID || p.ID != member.PrerequisiteID || p.Revision != member.PrerequisiteRevision || p.AttachmentID == "" || p.NamespaceUID != d.Identity.NamespaceUID || p.NamespaceUID != o.NamespaceUID || p.Namespace != o.Namespace || p.TLSSecretUID != o.SecretUID || p.TLSSecretName != o.SecretName || !slices.Equal(p.Hostnames, o.Hostnames) {
			return fail("prerequisite scope or identity changed")
		}
		if a.SchemaVersion != 1 || a.OrgID != in.Scope.OrgID || a.ID != p.RenewalAuthorityID || a.Generation != p.RenewalAuthorityGeneration || !a.Active || a.VerifiedBy == "" || a.VerifiedAt.IsZero() || a.VerifiedAt.After(in.Now) || a.VerificationEvidenceRef == "" || a.Method != "external-dns01" && a.Method != "external-equivalent" {
			return fail("external renewal authority changed")
		}
		if err := p.DrainPolicy.Validate(); err != nil {
			return fail("invalid drain policy")
		}
		if p.DrainPolicy.Mode == "bounded" && (p.TimeoutVerificationID == "" || facts.TimeoutConfigRevision != in.ConfigRevision) {
			return fail("matching application timeout verification is required")
		}
		if err := o.VerifyHostname(g.Routing.Hostname, in.Now); err != nil {
			return fail("current TLS certificate does not qualify")
		}
		identity, err := domain.NewMemberIdentity(in.GroupID, in.Generation, name)
		if err != nil {
			return out, err
		}
		pin.Identity = identity
		pin.Replicas = member.Replicas
		pin.DestinationID = d.ID
		pin.DestinationGeneration = d.Generation
		pin.PhysicalIdentity = d.Identity
		pin.CapabilityRevision = d.Capabilities.Revision
		pin.Prerequisite = p.Pin()
		out.Members = append(out.Members, pin)
		locations[pin.LocationIdentity] = true
	}
	if len(locations) < 2 {
		return fail("at least two verified locations are required")
	}
	revision, err := Digest(out)
	if err != nil {
		return out, err
	}
	out.Revision = revision
	return out, nil
}
