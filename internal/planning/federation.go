package planning

import (
	"slices"
	"strings"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// Federation action subjects cannot be executed by the legacy deployment runner.
const (
	NetworkSubject Subject = "network"
	PolicySubject  Subject = "policy"
)

// FederationInput pins desired intent and persisted destination observations.
type FederationInput struct {
	Scope        domain.Scope
	Environment  string
	Manifest     *manifest.Manifest
	Destinations map[string]*domain.Destination
	Held         map[string]bool
	Now          time.Time
}

// FederationRevision excludes observation timing and credential material while
// retaining physical identities and capability contract revisions for approval.
func FederationRevision(in FederationInput) string {
	type pin struct {
		Identity           domain.PhysicalIdentity
		CapabilityRevision string
		ConnectionID       string
		Quarantined        bool
	}
	pins := map[string]pin{}
	for id, d := range in.Destinations {
		if d != nil {
			pins[id] = pin{d.Identity, d.Capabilities.Revision, d.ConnectionID, d.Quarantined}
		}
	}
	value := struct {
		Scope        domain.Scope
		Environment  string
		Intent       *manifest.Federation
		Destinations map[string]pin
		Held         map[string]bool
	}{Scope: in.Scope, Environment: in.Environment, Destinations: pins, Held: in.Held}
	if in.Manifest != nil {
		value.Intent = in.Manifest.Federation
	}
	digest, err := Digest(value)
	if err != nil {
		return ""
	}
	return digest
}

// PlanFederation produces a review-only network/policy preview. Activation is
// always blocked until qualified runtime drivers and approval enforcement exist.
// These actions retain connection references but cannot mutate external resources.
func PlanFederation(in FederationInput) ([]Action, []Finding) {
	actions := []Action{}
	findings := []Finding{{Code: "federation-activation-unavailable", Path: "version", Message: "Version 2 execution requires qualified agents, network drivers and access enforcement. This plan cannot deploy."}}
	revision := FederationRevision(in)
	intent := &manifest.Federation{}
	if in.Manifest != nil && in.Manifest.Federation != nil {
		intent = in.Manifest.Federation
	}
	affected := make([]string, 0, len(intent.Placements))
	for _, p := range intent.Placements {
		affected = append(affected, p.Service)
	}
	slices.Sort(affected)
	affected = slices.Compact(affected)
	changes := []Change{{Field: "federation_revision", To: revision}, {Field: "affected_services", To: strings.Join(affected, ", ")}, {Field: "cost_and_disruption", To: "unqualified; execution blocked"}}
	add := func(subject Subject, name, connection string, dependencies []string) {
		actions = append(actions, Action{ID: string(subject) + ":" + name, Subject: subject, Name: name, Op: Keep, Provider: "federation", Engine: provider.Loomplane, ConnectionID: connection, Changes: slices.Clone(changes), Reason: "Review proposed federation intent; activation is unavailable.", DependsOn: dependencies})
	}
	destinations := map[string]*domain.Destination{}
	for _, name := range federationKeys(intent.Destinations) {
		ref := intent.Destinations[name]
		d := in.Destinations[ref.For(in.Environment)]
		if d == nil || d.Scope != in.Scope || in.Scope.OrgID == "" || in.Scope.ProjectID == "" || in.Scope.EnvironmentID == "" {
			findings = append(findings, Finding{Code: "federation-destination-not-found", Path: "federation.destinations." + name, Message: "The destination is unavailable in this environment."})
			continue
		}
		destinations[name] = d
		if d.Quarantined || !d.Capabilities.Fresh(d.Identity.Revision(), in.Now) {
			findings = append(findings, Finding{Code: "federation-capabilities-unavailable", Path: "federation.destinations." + name, Message: "Recheck this destination's physical identity and capabilities."})
		}
		add(PolicySubject, "destination:"+name, d.ConnectionID, nil)
	}
	for _, name := range federationKeys(intent.NetworkLinks) {
		link := intent.NetworkLinks[name]
		source, target := destinations[link.From], destinations[link.To]
		connection := ""
		if source != nil {
			connection = source.ConnectionID
		}
		add(NetworkSubject, name, connection, []string{"policy:destination:" + link.From, "policy:destination:" + link.To})
		if source == nil || target == nil || !source.Capabilities.Supports(domain.ImportedNetwork, source.Identity.Revision(), in.Now) || !target.Capabilities.Supports(domain.ImportedNetwork, target.Identity.Revision(), in.Now) || link.Transport != "imported" {
			findings = append(findings, Finding{Code: "federation-network-unqualified", Path: "federation.network_links." + name, Message: "Network ownership, route scope, cost and disruption require qualification."})
		}
	}
	for _, name := range federationKeys(intent.ServiceExports) {
		e := intent.ServiceExports[name]
		connection := ""
		if d := destinations[e.Destination]; d != nil {
			connection = d.ConnectionID
		}
		add(PolicySubject, "export:"+name, connection, []string{"policy:destination:" + e.Destination})
	}
	for _, name := range federationKeys(intent.AccessGrants) {
		g := intent.AccessGrants[name]
		// Calls may be cyclic. Grant installation depends on exports, never on another call.
		add(PolicySubject, "grant:"+name, "", []string{"policy:export:" + g.Export, "policy:destination:" + g.SourceDestination})
		findings = append(findings, Finding{Code: "federation-grant-unapproved", Path: "federation.access_grants." + name, Message: "A manifest grant is a proposal. Separate approval and gateway acknowledgment are required."})
	}
	// Pin even an empty version 2 intent so it never falls into legacy execution.
	add(PolicySubject, "activation", "", nil)
	return actions, findings
}

func federationKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func federationResult(in Input) *Result {
	f := FederationInput{Environment: in.Environment, Manifest: in.Manifest, Held: in.Held}
	if in.Federation != nil {
		f = *in.Federation
		f.Manifest = in.Manifest
		f.Environment = in.Environment
		f.Held = in.Held
	}
	actions, findings := PlanFederation(f)
	return &Result{Actions: actions, Problems: findings, Warnings: []Finding{}, Executable: false, Connections: map[string]string{}, Summary: Summary{Keep: len(actions)}}
}
