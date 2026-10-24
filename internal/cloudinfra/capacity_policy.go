package cloudinfra

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func (in *CapacityPolicyInput) UnmarshalJSON(raw []byte) error {
	type input CapacityPolicyInput
	var value input
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	*in = CapacityPolicyInput(value)
	return nil
}

func (s *Service) GetCapacityPolicy(ctx context.Context, p security.Principal, cloudID string) (*CapacityPolicy, error) {
	if _, err := s.authorize(ctx, p, cloudID); err != nil {
		return nil, err
	}
	st, ok := s.Store.(CapacityPolicyStore)
	if !ok {
		return nil, ErrInvalid
	}
	return st.GetCapacityPolicy(ctx, p.OrgID, cloudID)
}

func (s *Service) SaveCapacityPolicy(ctx context.Context, p security.Principal, cloudID string, in CapacityPolicyInput) (*CapacityPolicy, error) {
	if _, err := s.authorize(ctx, p, cloudID); err != nil {
		return nil, err
	}
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	st, ok := s.Store.(CapacityPolicyStore)
	if !ok {
		return nil, ErrInvalid
	}
	if in.ExpectedRevision < 0 || in.ExpectedRevision == math.MaxInt64 {
		return nil, ErrInvalid
	}
	policy := in.Policy
	policy.OrgID, policy.CloudID, policy.Revision = p.OrgID, cloudID, in.ExpectedRevision+1
	// Clone input slices before assigning the server-derived bootstrap choices.
	raw, err := json.Marshal(policy)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := json.Unmarshal(raw, &policy); err != nil {
		return nil, err
	}
	if err := validateCapacityPolicyShape(policy); err != nil {
		return nil, err
	}
	options := map[string]provider.ServerOptions{}
	for _, pool := range policy.Pools {
		if policy.Enabled {
			catalog, lookupErr := s.ServerOptions(ctx, p, cloudID, pool.ConnectionID)
			if lookupErr != nil {
				return nil, lookupErr
			}
			options[pool.PoolID] = catalog
		}
	}
	if err := validateCapacityPolicyChoices(&policy, options); err != nil {
		return nil, err
	}
	policy.authority = &p
	if err := st.SaveCapacityPolicy(ctx, &policy, in.ExpectedRevision); err != nil {
		return nil, err
	}
	if s.Audit != nil {
		s.Audit.Record(ctx, projects.AuditEvent{Action: "cloud.capacity_policy.write", Severity: "info", Principal: p, Attributes: map[string]any{"cloud_id": cloudID, "revision": policy.Revision, "enabled": policy.Enabled, "require_expansion_approval": policy.RequireExpansionApproval}})
	}
	return &policy, nil
}

func validateCapacityPolicy(policy CapacityPolicy, options map[string]provider.ServerOptions) error {
	if err := validateCapacityPolicyShape(policy); err != nil {
		return err
	}
	return validateCapacityPolicyChoices(&policy, options)
}
func capacityValues(v ResourceVector) []int64 {
	return []int64{v.Servers, v.CPUMillis, v.MemoryBytes, v.StorageBytes}
}
func nonnegativeCapacity(v ResourceVector) bool {
	for _, n := range capacityValues(v) {
		if n < 0 {
			return false
		}
	}
	return true
}
func capacityFits(v, limit ResourceVector) bool {
	a, b := capacityValues(v), capacityValues(limit)
	for i, n := range a {
		if n > b[i] {
			return false
		}
	}
	return true
}
func addCapacity(a, b ResourceVector) (ResourceVector, error) {
	av, bv := capacityValues(a), capacityValues(b)
	out := [4]int64{}
	for i, n := range av {
		if n < 0 || bv[i] < 0 || n > math.MaxInt64-bv[i] {
			return ResourceVector{}, ErrInvalid
		}
		out[i] = n + bv[i]
	}
	return ResourceVector{out[0], out[1], out[2], out[3]}, nil
}
func multiplyCapacity(v ResourceVector, n int64) (ResourceVector, error) {
	values := capacityValues(v)
	out := [4]int64{}
	for i, value := range values {
		if value < 0 || n < 0 || (value > 0 && n > math.MaxInt64/value) {
			return ResourceVector{}, ErrInvalid
		}
		out[i] = value * n
	}
	return ResourceVector{out[0], out[1], out[2], out[3]}, nil
}
func validateCapacityPolicyShape(policy CapacityPolicy) error {
	if !nonnegativeCapacity(policy.Limits) || len(policy.Pools) > 100 || len(policy.FrontendLimits) > 1000 {
		return ErrInvalid
	}
	if policy.Enabled {
		for _, n := range capacityValues(policy.Limits) {
			if n <= 0 {
				return fmt.Errorf("%w: enablement needs positive finite ceilings", ErrInvalid)
			}
		}
	}
	pools := map[string]bool{}
	for _, pool := range policy.Pools {
		if strings.TrimSpace(pool.PoolID) == "" || pools[pool.PoolID] || pool.MinServers < 0 || pool.MaxServers < 0 || pool.MinServers > pool.MaxServers || !nonnegativeCapacity(pool.SystemReserve) || pool.SystemReserve.Servers != 0 {
			return ErrInvalid
		}
		pools[pool.PoolID] = true
		if pool.Limits != nil && (!nonnegativeCapacity(*pool.Limits) || !capacityFits(*pool.Limits, policy.Limits)) {
			return fmt.Errorf("%w: pool ceilings exceed Cloud ceilings", ErrInvalid)
		}
		if policy.Enabled && (pool.ConnectionID == "" || pool.Region == "" || pool.Image == "" || (pool.Architecture != "amd64" && pool.Architecture != "arm64") || len(pool.AllowedSizes) == 0 || len(pool.AllowedSizes) > 100 || pool.MaxServers > policy.Limits.Servers) {
			return ErrInvalid
		}
		seen := map[string]bool{}
		for _, size := range pool.AllowedSizes {
			if strings.TrimSpace(size) == "" || seen[size] {
				return ErrInvalid
			}
			seen[size] = true
		}
	}
	targets := map[string]bool{}
	for _, limit := range policy.FrontendLimits {
		if limit.TargetID == "" || targets[limit.TargetID] || (limit.Maximum != nil && *limit.Maximum < 0) {
			return ErrInvalid
		}
		targets[limit.TargetID] = true
	}
	return nil
}

type bootstrapChoice struct {
	size      string
	resources ResourceVector
}

// Bootstrap minima use one complete offered size per pool. The bounded search
// never combines resource dimensions from different sizes into an invented host.
func validateCapacityPolicyChoices(policy *CapacityPolicy, options map[string]provider.ServerOptions) error {
	for i := range policy.Pools {
		policy.Pools[i].BootstrapSize = ""
	}
	if !policy.Enabled {
		return nil
	}
	choices := make([][]bootstrapChoice, len(policy.Pools))
	for i, pool := range policy.Pools {
		catalog := options[pool.PoolID]
		sizes := slices.Clone(pool.AllowedSizes)
		slices.Sort(sizes)
		for _, id := range sizes {
			if err := validateOptions(catalog, ServerInput{Region: pool.Region, Size: id, Image: pool.Image}); err != nil {
				return fmt.Errorf("%w: unavailable expansion template", ErrInvalid)
			}
			var size provider.ServerSize
			for _, candidate := range catalog.Sizes {
				if candidate.ID == id {
					size = candidate
					break
				}
			}
			cpu, err := multiplyCapacity(ResourceVector{CPUMillis: int64(size.CPUs)}, 1000)
			if err != nil {
				return err
			}
			memory, err := multiplyCapacity(ResourceVector{MemoryBytes: int64(size.MemoryMiB)}, 1<<20)
			if err != nil {
				return err
			}
			disk, err := multiplyCapacity(ResourceVector{StorageBytes: int64(size.DiskGB)}, 1_000_000_000)
			if err != nil {
				return err
			}
			host := ResourceVector{Servers: 1, CPUMillis: cpu.CPUMillis, MemoryBytes: memory.MemoryBytes, StorageBytes: disk.StorageBytes}
			if host.CPUMillis <= 0 || host.MemoryBytes <= 0 || host.StorageBytes <= 0 {
				return fmt.Errorf("%w: size needs positive resource facts", ErrInvalid)
			}
			if !capacityFits(pool.SystemReserve, host) {
				return fmt.Errorf("%w: reserve exceeds allowed size", ErrInvalid)
			}
			total, err := multiplyCapacity(host, pool.MinServers)
			if err != nil {
				return err
			}
			if capacityFits(total, policy.Limits) && (pool.Limits == nil || capacityFits(total, *pool.Limits)) {
				choices[i] = append(choices[i], bootstrapChoice{id, total})
			}
		}
		if len(choices[i]) == 0 {
			return fmt.Errorf("%w: minimum needs a supported homogeneous bootstrap allocation", ErrInvalid)
		}
	}
	// Bound work for large catalogs. Failure means the configuration is unproven.
	attempts := 0
	var fit func(int, ResourceVector) bool
	fit = func(i int, total ResourceVector) bool {
		if i == len(choices) {
			return true
		}
		for _, choice := range choices[i] {
			attempts++
			if attempts > 10000 {
				return false
			}
			next, err := addCapacity(total, choice.resources)
			if err != nil || !capacityFits(next, policy.Limits) {
				continue
			}
			policy.Pools[i].BootstrapSize = choice.size
			if fit(i+1, next) {
				return true
			}
		}
		return false
	}
	if !fit(0, ResourceVector{}) {
		return fmt.Errorf("%w: minimum needs a supported homogeneous bootstrap allocation within Cloud ceilings", ErrInvalid)
	}
	return nil
}
