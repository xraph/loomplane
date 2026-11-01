package planning

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
	"github.com/xraph/loomplane/pkg/manifest"
)

func groupProfileFixture(t *testing.T) GroupProfileInput {
	t.Helper()
	now := time.Now().UTC()
	scope := domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "environment"}
	m := &manifest.Manifest{Version: 2, Services: map[string]manifest.Service{"api": {Source: ".", Kind: "http-service", Settings: manifest.Settings{Port: 8080, Build: &manifest.Build{Image: "registry.example/api@sha256:" + strings.Repeat("a", 64)}}}}, Federation: &manifest.Federation{Destinations: map[string]manifest.DestinationRef{}, PlacementGroups: map[string]manifest.FederationPlacementGroup{}}}
	group := manifest.FederationPlacementGroup{Service: "api", MinReady: 2, Routing: manifest.GroupRouting{Mode: "external", Hostname: "api.example.com"}, Members: map[string]manifest.PlacementMember{}}
	in := GroupProfileInput{Scope: scope, Enabled: true, Manifest: m, Environment: "production", Name: "web", GroupID: "group", Generation: 1, ConfigRevision: "config", Now: now, Members: map[string]GroupMemberFacts{}}
	for i, name := range []string{"east", "west"} {
		id := "dest_0000000000000000000000000" + string(rune('1'+i))
		m.Federation.Destinations[name] = manifest.DestinationRef{ID: id}
		group.Members[name] = manifest.PlacementMember{Destination: name, Replicas: 1, PrerequisiteID: "prereq-" + name, PrerequisiteRevision: 1}
		identity := domain.PhysicalIdentity{Provider: "kubernetes", Account: "cluster-" + name, ResourceID: "namespace-" + name, NamespaceUID: "namespace-" + name}
		caps := domain.CapabilitySet{Revision: "cap-v1", IdentityRevision: identity.Revision(), CheckedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute), Results: map[domain.Capability]domain.CapabilityResult{domain.Observe: {State: domain.Supported}, domain.ImmutableArtifact: {State: domain.Supported}, domain.HTTPProtocol: {State: domain.Supported}}}
		prereq := domain.MemberPrerequisite{ID: "prereq-" + name, OrgID: scope.OrgID, ProjectID: scope.ProjectID, AttachmentID: "attach-" + name, SchemaVersion: 1, Revision: 1, Namespace: "apps", NamespaceUID: identity.NamespaceUID, TLSSecretName: "tls", TLSSecretUID: "secret-" + name, Hostnames: []string{"api.example.com"}, RenewalAuthorityID: "renewal-" + name, RenewalAuthorityGeneration: 1, DrainPolicy: domain.DrainPolicy{Mode: "attested"}}
		in.Members[name] = GroupMemberFacts{Destination: &domain.Destination{Scope: scope, ID: id, ConnectionID: "conn-" + name, Identity: identity, Generation: 1, Capabilities: caps}, Prerequisite: prereq, Authority: domain.TLSRenewalAuthority{ID: prereq.RenewalAuthorityID, OrgID: scope.OrgID, SchemaVersion: 1, Generation: 1, OwnerRef: "operator", Method: "external-dns01", VerificationEvidenceRef: "procedure", VerifiedBy: "user", VerifiedAt: now.Add(-time.Hour), Active: true}, Observation: MemberPrerequisiteObservation{Namespace: "apps", NamespaceUID: identity.NamespaceUID, SecretName: "tls", SecretUID: prereq.TLSSecretUID, Hostnames: prereq.Hostnames, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(30 * 24 * time.Hour), CheckedAt: now}, Plan: domain.GroupMemberPlan{LocationIdentity: "location-" + name, MappingRevision: 1, CloudID: "cloud", TargetID: "target-" + name, TargetRevision: 1, AttachmentRevision: 1, ConnectionID: "conn-" + name, ConnectionGeneration: 1, LocationID: "location-" + name, LocationRevision: 1, LocationEvidenceRevision: 1, GrantRevision: 1}}
	}
	m.Federation.PlacementGroups["web"] = group
	return in
}
func TestPlacementGroupRejectsStatefulAndWorker(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*GroupProfileInput)
	}{
		{"worker", func(in *GroupProfileInput) {
			s := in.Manifest.Services["api"]
			s.Kind = "worker"
			in.Manifest.Services["api"] = s
		}},
		{"volume", func(in *GroupProfileInput) { in.WritableVolumes = true }},
		{"pod-group", func(in *GroupProfileInput) {
			s := in.Manifest.Services["api"]
			s.PodGroup = &manifest.PodGroup{}
			in.Manifest.Services["api"] = s
		}},
		{"dependency", func(in *GroupProfileInput) { in.UnverifiedDependencies = true }},
		{"mutable-image", func(in *GroupProfileInput) { in.Manifest.Services["api"].Build.Image = "registry.example/api:latest" }},
		{"gate", func(in *GroupProfileInput) { in.Enabled = false }},
		{"ungrouped-service", func(in *GroupProfileInput) { in.Manifest.Services["other"] = in.Manifest.Services["api"] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := groupProfileFixture(t)
			tc.change(&in)
			if _, err := ValidatePlacementGroupProfile(in); err == nil {
				t.Fatal("unsupported profile accepted")
			}
		})
	}
}
func TestPlacementGroupPrerequisiteScopeAndRevision(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*GroupMemberFacts)
	}{
		{"foreign-project", func(f *GroupMemberFacts) { f.Prerequisite.ProjectID = "other" }},
		{"revision", func(f *GroupMemberFacts) { f.Prerequisite.Revision++ }},
		{"namespace", func(f *GroupMemberFacts) { f.Observation.NamespaceUID = "recreated" }},
		{"secret", func(f *GroupMemberFacts) { f.Observation.SecretUID = "recreated" }},
		{"authority", func(f *GroupMemberFacts) { f.Authority.Generation++ }},
		{"revoked", func(f *GroupMemberFacts) { f.Authority.Active = false }},
		{"expired", func(f *GroupMemberFacts) { f.Observation.NotAfter = time.Now().Add(time.Hour) }},
		{"grant", func(f *GroupMemberFacts) { f.Plan.GrantRevision = 0 }},
		{"unverified-location", func(f *GroupMemberFacts) { f.Plan.LocationEvidenceRevision = 0 }},
		{"unverified-timeout", func(f *GroupMemberFacts) {
			n := int64(30)
			f.Prerequisite.DrainPolicy = domain.DrainPolicy{Mode: "bounded", MaxConnectionLifetimeSeconds: &n, TerminationGraceSeconds: 30}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := groupProfileFixture(t)
			f := in.Members["east"]
			tc.change(&f)
			in.Members["east"] = f
			if _, err := ValidatePlacementGroupProfile(in); err == nil {
				t.Fatal("stale or untrusted prerequisite accepted")
			}
		})
	}
}
func TestPlacementGroupDuplicateDestination(t *testing.T) {
	in := groupProfileFixture(t)
	a, b := in.Members["east"], in.Members["west"]
	b.Destination.Identity = a.Destination.Identity
	b.Destination.Capabilities = a.Destination.Capabilities
	b.Prerequisite.NamespaceUID = a.Prerequisite.NamespaceUID
	b.Prerequisite.TLSSecretUID = a.Prerequisite.TLSSecretUID
	b.Observation.NamespaceUID = a.Observation.NamespaceUID
	b.Observation.SecretUID = a.Observation.SecretUID
	in.Members["west"] = b
	if _, err := ValidatePlacementGroupProfile(in); err == nil {
		t.Fatal("aliased destination accepted")
	}
}
func TestPlacementGroupRenewalDigestStable(t *testing.T) {
	in := groupProfileFixture(t)
	before, err := ValidatePlacementGroupProfile(in)
	if err != nil {
		t.Fatal(err)
	}
	in.Now = in.Now.Add(time.Second)
	for name, f := range in.Members {
		f.Observation.CheckedAt = in.Now
		f.Observation.Fingerprint = "new-leaf"
		f.Observation.NotAfter = in.Now.Add(60 * 24 * time.Hour)
		f.Prerequisite.UpdatedAt = in.Now
		in.Members[name] = f
	}
	after, err := ValidatePlacementGroupProfile(in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("same-identity renewal changed immutable review")
	}
	f := in.Members["east"]
	f.Observation.Hostnames = []string{"api.example.com", "extra.example.com"}
	in.Members["east"] = f
	if _, err = ValidatePlacementGroupProfile(in); err == nil {
		t.Fatal("SAN scope changed without review")
	}
}
func TestPlacementGroupGuardPreserved(t *testing.T) {
	result := Compute(Input{Manifest: &manifest.Manifest{Version: 2}})
	if result.Executable {
		t.Fatal("unsupported v2 executable")
	}
	found := false
	for _, p := range result.Problems {
		found = found || p.Code == "federation-activation-unavailable"
	}
	if !found {
		t.Fatal("legacy v2 guard removed")
	}
	in := groupProfileFixture(t)
	qualified, err := ValidatePlacementGroupProfile(in)
	if err != nil {
		t.Fatal(err)
	}
	result = federationResult(Input{Manifest: in.Manifest, Federation: &FederationInput{GroupPlans: []domain.QualifiedGroupPlan{qualified}}})
	if result.Executable || len(result.Actions) != 0 || len(result.PlacementGroups) != 1 {
		t.Fatalf("qualified review leaked legacy actions: %+v", result)
	}
}
func TestPlacementGroupResultRoundTrip(t *testing.T) {
	in := groupProfileFixture(t)
	g, err := ValidatePlacementGroupProfile(in)
	if err != nil {
		t.Fatal(err)
	}
	result := Result{PlacementGroups: []domain.QualifiedGroupPlan{g}}
	var plan Plan
	plan.setResult(&result)
	raw, err := json.Marshal(plan.result())
	if err != nil {
		t.Fatal(err)
	}
	var read Result
	if err = json.Unmarshal(raw, &read); err != nil {
		t.Fatal(err)
	}
	var reopened Plan
	reopened.setResult(&read)
	if !reflect.DeepEqual(plan.PlacementGroups, reopened.PlacementGroups) {
		t.Fatal("group pins lost on reload")
	}
	raw, err = json.Marshal(Result{})
	if err != nil || strings.Contains(string(raw), "placement_groups") {
		t.Fatalf("legacy JSON changed: %s %v", raw, err)
	}
}

func TestPlacementGroupLocationAliasesDoNotAddLocations(t *testing.T) {
	in := groupProfileFixture(t)
	east, west := in.Members["east"], in.Members["west"]
	west.Plan.LocationIdentity = east.Plan.LocationIdentity
	in.Members["west"] = west
	if _, err := ValidatePlacementGroupProfile(in); err == nil {
		t.Fatal("two aliases for the same location qualified")
	}
}

func TestPlacementGroupAcceptanceValidatorRejectsEmptyReview(t *testing.T) {
	group := domain.QualifiedGroupPlan{Scope: domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "environment"}}
	if err := ValidatePlacementGroupReviewTx(t.Context(), nil, "org", "project", "environment", group, time.Now()); err == nil {
		t.Fatal("empty review passed current-authority validation")
	}
}
