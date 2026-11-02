package manifest

import (
	"fmt"
	"slices"
	"strings"

	"golang.org/x/text/language"
)

// PlacementConstraints limits a service to destinations with checked evidence.
// Residency uses uppercase ISO 3166 alpha-2 jurisdiction codes. It requires a
// saved operator attestation; a region name never establishes jurisdiction.
// An environment's placement block replaces the complete inherited block.
type PlacementConstraints struct {
	AllowedLocations []string `json:"allowed_locations,omitempty" yaml:"allowed_locations,omitempty"`
	Residency        []string `json:"residency,omitempty" yaml:"residency,omitempty"`
	Runtime          string   `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	Architecture     string   `json:"architecture,omitempty" yaml:"architecture,omitempty"`
	GPU              *GPU     `json:"gpu,omitempty" yaml:"gpu,omitempty"`
	Network          []string `json:"network,omitempty" yaml:"network,omitempty"`
	Storage          []string `json:"storage,omitempty" yaml:"storage,omitempty"`
	Availability     string   `json:"availability,omitempty" yaml:"availability,omitempty"`
}

func (p PlacementConstraints) Validate() error {
	for _, values := range [][]string{p.AllowedLocations, p.Residency, p.Network, p.Storage} {
		if len(values) > 32 {
			return fmt.Errorf("placement lists allow at most 32 entries")
		}
		seen := map[string]bool{}
		for _, v := range values {
			if v == "" || len(v) > 128 || strings.TrimSpace(v) != v || seen[v] {
				return fmt.Errorf("placement entries must be unique nonempty names of at most 128 characters")
			}
			seen[v] = true
		}
	}
	for _, v := range p.Residency {
		if !ResidencyCode(v) {
			return fmt.Errorf("residency requires uppercase two-letter jurisdiction codes")
		}
	}
	if !slices.Contains([]string{"", "server", "kubernetes", "managed"}, p.Runtime) {
		return fmt.Errorf("runtime must be server, kubernetes or managed")
	}
	if !slices.Contains([]string{"", "amd64", "arm64"}, p.Architecture) {
		return fmt.Errorf("architecture must be amd64 or arm64")
	}
	if !slices.Contains([]string{"", "single", "independent-zone", "independent-datacenter"}, p.Availability) {
		return fmt.Errorf("availability must be single, independent-zone or independent-datacenter")
	}
	if p.GPU != nil && (p.GPU.Kind == "" || len(p.GPU.Kind) > 64 || p.GPU.Count < 1 || p.GPU.Count > MaxGPUs) {
		return fmt.Errorf("GPU requires a kind and a count between 1 and %d", MaxGPUs)
	}
	return nil
}

// ResidencyCode checks the representation, without deriving jurisdiction from geography.
func ResidencyCode(v string) bool {
	if len(v) != 2 || strings.ToUpper(v) != v {
		return false
	}
	r, err := language.ParseRegion(v)
	return err == nil && r.IsCountry() && !r.IsPrivateUse() && r.ISO3() != "ZZZ" && r.String() == v
}
