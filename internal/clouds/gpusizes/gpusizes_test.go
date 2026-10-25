package gpusizes

import (
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestTablesAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, cloud := range []string{"aws", "gcp", "azure", "digitalocean"} {
		rows := For(cloud)
		if len(rows) == 0 {
			t.Fatalf("%s has no rows", cloud)
		}
		for _, s := range rows {
			a := s.Accelerator
			if a == nil || a.Count < 1 || s.CPUs <= 0 || s.MemoryMiB <= 0 || s.HourlyUSD <= 0 || !s.HourlyPriceKnown || seen[s.ID] || !slices.Equal(s.Architectures, []string{"amd64"}) {
				t.Fatalf("%s row %+v", cloud, s)
			}
			k, ok := provider.LookupAcceleratorKind(a.Kind)
			if !ok || k.MemoryGiB != a.MemoryGiB || k.Vendor != a.Vendor {
				t.Fatalf("%s kind %+v", s.ID, a)
			}
			seen[s.ID] = true
		}
	}
	if _, err := time.Parse(time.DateOnly, PricesAsOf); err != nil {
		t.Fatal("PricesAsOf must be a date")
	}
}

func TestLookup(t *testing.T) {
	for _, tc := range []struct {
		id, cloud, kind string
		count           int
	}{
		{"g5.xlarge", "aws", "nvidia-a10g", 1},
		{"p4d.24xlarge", "aws", "nvidia-a100-40gb", 8},
		{"g2-standard-4", "gcp", "nvidia-l4", 1},
		{"a3-highgpu-8g", "gcp", "nvidia-h100-80gb", 8},
		{"Standard_NC24ads_A100_v4", "azure", "nvidia-a100-80gb", 1},
		{"gpu-h100x1-80gb", "digitalocean", "nvidia-h100-80gb", 1},
	} {
		s, cloud, ok := Lookup(tc.id)
		if !ok || cloud != tc.cloud || s.Accelerator.Kind != tc.kind || s.Accelerator.Count != tc.count {
			t.Errorf("%s: %+v %s %v", tc.id, s, cloud, ok)
		}
	}
	if _, _, ok := Lookup("s-1vcpu-2gb"); ok {
		t.Fatal("a CPU Droplet is not a GPU size")
	}
}

func TestForReturnsACopy(t *testing.T) {
	rows := For("aws")
	rows[0].ID = "changed"
	rows[0].Accelerator.Count = 99
	if again := For("aws"); again[0].ID == "changed" || again[0].Accelerator.Count == 99 {
		t.Fatal("For must not expose the table")
	}
	if For("nope") != nil {
		t.Fatal("unknown cloud has no rows")
	}
}
