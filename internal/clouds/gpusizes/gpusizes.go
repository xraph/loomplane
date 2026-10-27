// Package gpusizes holds reference GPU instance types and list prices for the
// clouds Loomplane can run GPU nodes on. The rows are not a live account
// catalog: they have no regions or disk, and the prices are estimates copied
// from each provider's public pricing page on PricesAsOf.
package gpusizes

import (
	"slices"

	"github.com/xraph/loomplane/pkg/provider"
)

// PricesAsOf is the day the prices in these tables were copied.
const PricesAsOf = "2026-10-25"

// row builds one reference size. memGiB is the instance memory in GiB.
func row(id, kind string, count, cpus, memGiB int, hourly float64) provider.ServerSize {
	k, ok := provider.LookupAcceleratorKind(kind)
	if !ok {
		panic("gpusizes: unknown accelerator kind " + kind)
	}
	return provider.ServerSize{
		ID:               id,
		CPUs:             cpus,
		MemoryMiB:        memGiB * 1024,
		Architectures:    []string{"amd64"},
		HourlyUSD:        hourly,
		HourlyPriceKnown: true,
		Accelerator:      &provider.Accelerator{Kind: k.ID, Vendor: k.Vendor, Count: count, MemoryGiB: k.MemoryGiB},
	}
}

var clouds = []struct {
	name  string
	sizes []provider.ServerSize
}{
	{"aws", awsSizes},
	{"gcp", gcpSizes},
	{"azure", azureSizes},
	{"digitalocean", digitalOceanSizes},
}

// For returns one cloud's GPU instance types: "aws", "gcp", "azure" or
// "digitalocean". It returns nil for any other name. Callers get a copy.
func For(cloud string) []provider.ServerSize {
	for _, c := range clouds {
		if c.name == cloud {
			return cloneSizes(c.sizes)
		}
	}
	return nil
}

// Lookup finds an instance type, machine type, VM size or Droplet slug in
// every table.
func Lookup(instanceType string) (size provider.ServerSize, cloud string, ok bool) {
	for _, c := range clouds {
		for _, s := range c.sizes {
			if s.ID == instanceType {
				return cloneSizes([]provider.ServerSize{s})[0], c.name, true
			}
		}
	}
	return provider.ServerSize{}, "", false
}

func cloneSizes(in []provider.ServerSize) []provider.ServerSize {
	out := slices.Clone(in)
	for i := range out {
		out[i].Architectures = slices.Clone(out[i].Architectures)
		out[i].Regions = slices.Clone(out[i].Regions)
		if a := out[i].Accelerator; a != nil {
			c := *a
			out[i].Accelerator = &c
		}
	}
	return out
}
