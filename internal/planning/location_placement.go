package planning

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/pkg/manifest"
)

// LocationReason is a safe explanation code, without account or network details.
type LocationReason string

const (
	LocationEligible               LocationReason = "location-eligible"
	LocationScopeInvalid           LocationReason = "location-scope-invalid"
	LocationGrantChanged           LocationReason = "location-grant-changed"
	LocationExactTarget            LocationReason = "location-exact-target-mismatch"
	LocationNotAllowed             LocationReason = "location-not-allowed"
	LocationResidencyUnverified    LocationReason = "location-residency-unverified"
	LocationRuntimeUnsupported     LocationReason = "location-runtime-unsupported"
	LocationArchitectureUnverified LocationReason = "location-architecture-unverified"
	LocationGPUUnsupported         LocationReason = "location-gpu-unsupported"
	LocationNetworkUnverified      LocationReason = "location-network-unverified"
	LocationStorageUnverified      LocationReason = "location-storage-unverified"
	LocationAvailabilityUnverified LocationReason = "location-availability-unverified"
	LocationInventoryStale         LocationReason = "location-inventory-stale"
	LocationCapacityUnavailable    LocationReason = "location-capacity-unavailable"
	LocationMovementRequired       LocationReason = "location-movement-required"
	LocationTaintsUnsupported      LocationReason = "location-taints-unsupported"
	LocationPriceUnknown           LocationReason = "location-price-unknown"
	LocationLatencyUnverified      LocationReason = "location-latency-unverified"
	LocationNoCandidates           LocationReason = "location-no-candidates"
)

// PlacementCandidate is server-owned evidence. Raw inventory alone must never
// set qualified runtime or GPU capabilities. Expiry follows the source's bound.
type PlacementCandidate struct {
	TargetID, LocationID, OrgID, ProjectID, EnvironmentID                            string
	ServerID, RuntimeConnectionID, CapacitySource                                    string
	ConnectionGeneration                                                             int64
	Provider, Runtime, Architecture                                                  string
	TargetRevision, LocationRevision, EvidenceRevision, GrantRevision                int64
	Granted, RuntimeQualified, ReadyCapacity, ExpansionAuthorized, UnsupportedTaints bool
	CheckedAt, ExpiresAt                                                             time.Time
	Residency, Network, Storage                                                      []string
	ResidencySource                                                                  string
	GPU                                                                              *manifest.GPU
	AvailabilityEvidenceSource                                                       string
	AvailabilityVerified, UnknownCorrelations                                        bool
	Availability                                                                     string
	IncrementalMicroUSD                                                              *int64
	PriceCheckedAt, PriceExpiresAt                                                   time.Time
	LatencyMicros                                                                    *int64
	LatencyCheckedAt, LatencyExpiresAt                                               time.Time
	Rejections                                                                       []LocationReason
}

type LocationPlacementInput struct {
	Now                             time.Time
	OrgID, ProjectID, EnvironmentID string
	GrantRevision                   int64
	Constraints                     manifest.PlacementConstraints
	CurrentTargetID, ExactTargetID  string
	Policy                          cloudprofiles.LocationPolicy
	Candidates                      []PlacementCandidate
	AutomatedPurchase               bool
}

type LocationCandidateExplanation struct {
	TargetID        string           `json:"target_id"`
	LocationID      string           `json:"location_id,omitempty"`
	Eligible        bool             `json:"eligible"`
	Reasons         []LocationReason `json:"reasons"`
	Warnings        []LocationReason `json:"warnings,omitempty"`
	ResidencySource string           `json:"residency_source,omitempty"`
}

type LocationPlacementDecision struct {
	Policy            cloudprofiles.LocationPolicy         `json:"policy"`
	GrantRevision     int64                                `json:"grant_revision"`
	TargetID          string                               `json:"target_id,omitempty"`
	MovementRequired  bool                                 `json:"movement_required,omitempty"`
	ExpansionRequired bool                                 `json:"expansion_required,omitempty"`
	Candidates        []LocationCandidateExplanation       `json:"candidates"`
	Blockers          []LocationReason                     `json:"blockers,omitempty"`
	Pins              []cloudprofiles.LocationCandidatePin `json:"pins"`
}

// EvaluateLocations filters hard requirements before deterministic ranking. It
// reads no store or provider and uses only the supplied clock.
func EvaluateLocations(in LocationPlacementInput) (LocationPlacementDecision, error) {
	out := LocationPlacementDecision{Policy: in.Policy, GrantRevision: in.GrantRevision, Candidates: []LocationCandidateExplanation{}, Pins: []cloudprofiles.LocationCandidatePin{}}
	if in.Now.IsZero() || in.OrgID == "" || in.ProjectID == "" || in.EnvironmentID == "" || in.GrantRevision < 1 {
		return out, fmt.Errorf("location placement requires a clock and scoped grant")
	}
	if err := in.Constraints.Validate(); err != nil {
		return out, err
	}
	if err := in.Policy.Validate(); err != nil {
		return out, err
	}
	candidates := slices.Clone(in.Candidates)
	slices.SortFunc(candidates, func(a, b PlacementCandidate) int { return cmp.Compare(a.TargetID, b.TargetID) })
	var eligible []PlacementCandidate
	for i, c := range candidates {
		if c.TargetID == "" || i > 0 && c.TargetID == candidates[i-1].TargetID || c.IncrementalMicroUSD != nil && *c.IncrementalMicroUSD < 0 || c.LatencyMicros != nil && *c.LatencyMicros < 0 {
			return out, fmt.Errorf("invalid or duplicate location candidate")
		}
		if c.IncrementalMicroUSD != nil && !freshLocationEvidence(in.Now, c.PriceCheckedAt, c.PriceExpiresAt) {
			c.IncrementalMicroUSD = nil
		}
		reasons := locationRejections(in, c)
		e := LocationCandidateExplanation{TargetID: c.TargetID, LocationID: c.LocationID, Eligible: len(reasons) == 0, Reasons: reasons}
		if len(in.Constraints.Residency) > 0 && c.ResidencySource == "operator-attested" {
			e.ResidencySource = c.ResidencySource
		}
		if c.IncrementalMicroUSD == nil {
			e.Warnings = []LocationReason{LocationPriceUnknown}
		}
		if e.Eligible {
			e.Reasons = []LocationReason{LocationEligible}
			eligible = append(eligible, c)
		} else {
			out.Blockers = append(out.Blockers, reasons...)
		}
		out.Candidates = append(out.Candidates, e)
		out.Pins = append(out.Pins, cloudprofiles.LocationCandidatePin{RuntimeConnectionID: c.RuntimeConnectionID, ConnectionGeneration: c.ConnectionGeneration, CapacitySource: c.CapacitySource, TargetID: c.TargetID, TargetRevision: c.TargetRevision, LocationID: c.LocationID, LocationRevision: c.LocationRevision, EvidenceRevision: c.EvidenceRevision})
	}
	if len(eligible) == 0 {
		if len(candidates) == 0 {
			out.Blockers = []LocationReason{LocationNoCandidates}
		}
		slices.Sort(out.Blockers)
		out.Blockers = slices.Compact(out.Blockers)
		return out, nil
	}
	slices.SortFunc(eligible, func(a, b PlacementCandidate) int { return rankLocations(in, a, b) })
	chosen := eligible[0]
	out.TargetID = chosen.TargetID
	out.Blockers = nil
	out.MovementRequired = in.CurrentTargetID != "" && in.CurrentTargetID != chosen.TargetID
	out.ExpansionRequired = !chosen.ReadyCapacity
	return out, nil
}

func freshLocationEvidence(now, checked, expires time.Time) bool {
	return !checked.IsZero() && !checked.After(now.Add(time.Minute)) && expires.After(now) && expires.After(checked)
}

func locationRejections(in LocationPlacementInput, c PlacementCandidate) []LocationReason {
	r := slices.Clone(c.Rejections)
	add := func(ok bool, reason LocationReason) {
		if !ok {
			r = append(r, reason)
		}
	}
	p := in.Constraints
	add(c.OrgID == in.OrgID && c.ProjectID == in.ProjectID && c.EnvironmentID == in.EnvironmentID && c.TargetRevision > 0, LocationScopeInvalid)
	add(c.Granted && c.GrantRevision == in.GrantRevision, LocationGrantChanged)
	add(in.ExactTargetID == "" || c.TargetID == in.ExactTargetID, LocationExactTarget)
	add(len(p.AllowedLocations) == 0 || c.LocationRevision > 0 && slices.Contains(p.AllowedLocations, c.LocationID), LocationNotAllowed)
	if len(p.Residency) > 0 {
		add(c.LocationRevision > 0 && c.EvidenceRevision > 0 && c.ResidencySource == "operator-attested" && slices.ContainsFunc(p.Residency, func(v string) bool { return slices.Contains(c.Residency, v) }), LocationResidencyUnverified)
	}
	// Loomplane Cloud chooses its own hosts, so location policy never
	// places a workload there.
	add(c.Runtime != platformRuntime && c.RuntimeQualified && (p.Runtime == "" || p.Runtime == c.Runtime), LocationRuntimeUnsupported)
	add(p.Architecture == "" || p.Architecture == c.Architecture, LocationArchitectureUnverified)
	if p.GPU != nil {
		add(c.GPU != nil && c.GPU.Kind == p.GPU.Kind && c.GPU.Count >= p.GPU.Count, LocationGPUUnsupported)
	}
	for _, v := range p.Network {
		add(slices.Contains(c.Network, v), LocationNetworkUnverified)
	}
	for _, v := range p.Storage {
		add(slices.Contains(c.Storage, v), LocationStorageUnverified)
	}
	add(p.Availability == "" || p.Availability == "single" || c.Availability == p.Availability && c.AvailabilityVerified && c.AvailabilityEvidenceSource == "provider" && !c.UnknownCorrelations, LocationAvailabilityUnverified)
	add(freshLocationEvidence(in.Now, c.CheckedAt, c.ExpiresAt), LocationInventoryStale)
	add(c.ReadyCapacity || c.ExpansionAuthorized, LocationCapacityUnavailable)
	add(!c.UnsupportedTaints, LocationTaintsUnsupported)
	if in.AutomatedPurchase {
		add(c.IncrementalMicroUSD != nil && freshLocationEvidence(in.Now, c.PriceCheckedAt, c.PriceExpiresAt), LocationPriceUnknown)
	}
	if in.Policy.Ranking == "latency" {
		add(c.LatencyMicros != nil && freshLocationEvidence(in.Now, c.LatencyCheckedAt, c.LatencyExpiresAt), LocationLatencyUnverified)
	}
	slices.Sort(r)
	return slices.Compact(r)
}

func preference(values []string, value string) int {
	i := slices.Index(values, value)
	if i < 0 {
		return len(values)
	}
	return i
}
func rankLocations(in LocationPlacementInput, a, b PlacementCandidate) int {
	for _, pair := range [][2]int{{boolOrder(a.TargetID == in.CurrentTargetID), boolOrder(b.TargetID == in.CurrentTargetID)}, {boolOrder(a.ReadyCapacity), boolOrder(b.ReadyCapacity)}, {preference(in.Policy.PreferredLocations, a.LocationID), preference(in.Policy.PreferredLocations, b.LocationID)}, {preference(in.Policy.PreferredProviders, a.Provider), preference(in.Policy.PreferredProviders, b.Provider)}} {
		if n := cmp.Compare(pair[0], pair[1]); n != 0 {
			return n
		}
	}
	if in.Policy.Ranking == "latency" && a.LatencyMicros != nil && b.LatencyMicros != nil {
		if n := cmp.Compare(*a.LatencyMicros, *b.LatencyMicros); n != 0 {
			return n
		}
	}
	if n := cmp.Compare(boolOrder(a.IncrementalMicroUSD != nil), boolOrder(b.IncrementalMicroUSD != nil)); n != 0 {
		return n
	}
	if a.IncrementalMicroUSD != nil && b.IncrementalMicroUSD != nil {
		if n := cmp.Compare(*a.IncrementalMicroUSD, *b.IncrementalMicroUSD); n != 0 {
			return n
		}
	}
	return cmp.Compare(a.TargetID, b.TargetID)
}
func boolOrder(v bool) int {
	if v {
		return 0
	}
	return 1
}
