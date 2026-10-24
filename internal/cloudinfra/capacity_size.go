package cloudinfra

import (
	"fmt"
	"math"
	"slices"

	"github.com/xraph/loomplane/pkg/provider"
)

type CapacityPlacementError struct{ Reason string }

func (e *CapacityPlacementError) Error() string {
	return e.Reason + ": no permitted server size fits the workload"
}

// SelectExpansionSize compares complete sizes after reserve. Price only breaks
// equal-resource ties, and a missing price sorts behind a known estimate.
func SelectExpansionSize(options provider.ServerOptions, policy PoolExpansionPolicy, demand CapacityDemand) (string, error) {
	if !nonnegativeCapacity(demand.Resources) || !nonnegativeCapacity(policy.SystemReserve) || demand.Resources.Servers != 0 || policy.SystemReserve.Servers != 0 {
		return "", ErrInvalid
	}
	if demand.LocalData && demand.Resources.StorageBytes > 0 {
		return "", provider.ErrInfrastructureUnsupported
	}
	required, err := addCapacity(demand.Resources, policy.SystemReserve)
	if err != nil {
		return "", err
	}
	architecture := policy.Architecture
	if architecture != "" && demand.Architecture != "" && architecture != demand.Architecture {
		return "", &CapacityPlacementError{Reason: "placement_constraint"}
	}
	if architecture == "" {
		architecture = demand.Architecture
	}
	candidates := make([]provider.ServerSize, 0, len(options.Sizes))
	seen := map[string]bool{}
	for _, size := range options.Sizes {
		if !slices.Contains(policy.AllowedSizes, size.ID) {
			continue
		}
		if seen[size.ID] {
			return "", fmt.Errorf("%w: duplicate catalog size", ErrInvalid)
		}
		seen[size.ID] = true
		if validateOptions(options, ServerInput{Region: policy.Region, Image: policy.Image, Size: size.ID}) != nil {
			continue
		}
		if architecture != "" {
			if len(size.Architectures) > 0 && !slices.Contains(size.Architectures, architecture) {
				continue
			}
			imageCompatible := false
			for _, image := range options.Images {
				if image.ID == policy.Image && image.Architecture == architecture {
					imageCompatible = true
				}
			}
			if !imageCompatible {
				continue
			}
		}
		host, e := sizeResources(size)
		if e != nil {
			return "", e
		}
		if !capacityFits(required, host) {
			continue
		}
		if math.IsNaN(size.HourlyUSD) || math.IsInf(size.HourlyUSD, 0) || size.HourlyUSD < 0 {
			return "", ErrInvalid
		}
		candidates = append(candidates, size)
	}
	slices.SortFunc(candidates, func(a, b provider.ServerSize) int {
		for _, pair := range [][2]int{{a.CPUs, b.CPUs}, {a.MemoryMiB, b.MemoryMiB}, {a.DiskGB, b.DiskGB}} {
			if pair[0] < pair[1] {
				return -1
			}
			if pair[0] > pair[1] {
				return 1
			}
		}
		ak, bk := a.HourlyPriceKnown || a.HourlyUSD > 0, b.HourlyPriceKnown || b.HourlyUSD > 0
		if ak != bk {
			if ak {
				return -1
			}
			return 1
		}
		if ak {
			if a.HourlyUSD < b.HourlyUSD {
				return -1
			}
			if a.HourlyUSD > b.HourlyUSD {
				return 1
			}
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	if len(candidates) == 0 {
		return "", &CapacityPlacementError{Reason: "placement_constraint"}
	}
	return candidates[0].ID, nil
}
func sizeResources(size provider.ServerSize) (ResourceVector, error) {
	cpu, err := multiplyCapacity(ResourceVector{CPUMillis: int64(size.CPUs)}, 1000)
	if err != nil {
		return ResourceVector{}, err
	}
	memory, err := multiplyCapacity(ResourceVector{MemoryBytes: int64(size.MemoryMiB)}, 1<<20)
	if err != nil {
		return ResourceVector{}, err
	}
	disk, err := multiplyCapacity(ResourceVector{StorageBytes: int64(size.DiskGB)}, 1_000_000_000)
	if err != nil {
		return ResourceVector{}, err
	}
	if cpu.CPUMillis <= 0 || memory.MemoryBytes <= 0 || disk.StorageBytes <= 0 {
		return ResourceVector{}, ErrInvalid
	}
	return ResourceVector{Servers: 1, CPUMillis: cpu.CPUMillis, MemoryBytes: memory.MemoryBytes, StorageBytes: disk.StorageBytes}, nil
}
