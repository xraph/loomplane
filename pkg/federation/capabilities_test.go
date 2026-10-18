package federation

import (
	"testing"
	"time"
)

func TestCapabilitiesExpireClosed(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	valid := CapabilitySet{Revision: "r1", IdentityRevision: "physical1", CheckedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute), Results: map[Capability]CapabilityResult{StableDNS: {State: Supported}}}
	for _, tc := range []struct {
		name   string
		mutate func(*CapabilitySet)
		want   bool
	}{
		{"fresh", func(*CapabilitySet) {}, true},
		{"absent", func(c *CapabilitySet) { c.Results = nil }, false},
		{"unverified", func(c *CapabilitySet) { c.Results[StableDNS] = CapabilityResult{State: Unverified} }, false},
		{"unsupported", func(c *CapabilitySet) { c.Results[StableDNS] = CapabilityResult{State: Unsupported} }, false},
		{"expired", func(c *CapabilitySet) { c.ExpiresAt = now }, false},
		{"future", func(c *CapabilitySet) { c.CheckedAt = now.Add(time.Second) }, false},
		{"no revision", func(c *CapabilitySet) { c.Revision = "" }, false},
		{"wrong identity", func(c *CapabilitySet) { c.IdentityRevision = "other" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := valid
			c.Results = map[Capability]CapabilityResult{StableDNS: {State: Supported}}
			tc.mutate(&c)
			if got := c.Supports(StableDNS, "physical1", now); got != tc.want {
				t.Fatalf("Supports = %v, want %v", got, tc.want)
			}
		})
	}
	if (CapabilitySet{}).Supports(StableDNS, "", now) {
		t.Fatal("legacy empty capabilities must deny")
	}
}
