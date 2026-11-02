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
	policy, err := st.GetCapacityPolicy(ctx, p.OrgID, cloudID)
	if err != nil {
		return nil, err
	}
	return withPolicyLists(policy), nil
}

// withPolicyLists gives clients empty lists, never null, for a Cloud with no
// pools or frontend limits. The stored body is unchanged.
func withPolicyLists(policy *CapacityPolicy) *CapacityPolicy {
	if policy.Pools == nil {
		policy.Pools = []PoolExpansionPolicy{}
	}
	if policy.FrontendLimits == nil {
		policy.FrontendLimits = []FrontendLimit{}
	}
	return policy
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
	policy, err := s.prepareCapacityPolicy(ctx, p, cloudID, in)
	if err != nil {
		return nil, err
	}
	if checkErr := st.SaveCapacityPolicy(ctx, policy, in.ExpectedRevision); checkErr != nil {
		return nil, checkErr
	}
	s.capacityPolicySaved(ctx, p, policy)
	return withPolicyLists(policy), nil
}

// prepareCapacityPolicy checks a policy the caller is about to save over
// in.ExpectedRevision. It reads each enabled pool's provider options before
// any transaction opens, picks the bootstrap sizes, and captures p as the
// policy's authority.
func (s *Service) prepareCapacityPolicy(ctx context.Context, p security.Principal, cloudID string, in CapacityPolicyInput) (*CapacityPolicy, error) {
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
	return &policy, nil
}

// capacityPolicySaved records a saved policy and wakes the Cloud's capacity work.
func (s *Service) capacityPolicySaved(ctx context.Context, p security.Principal, policy *CapacityPolicy) {
	if s.Audit != nil {
		s.Audit.Record(ctx, projects.AuditEvent{Action: "cloud.capacity_policy.write", Severity: "info", Principal: p, Attributes: map[string]any{"cloud_id": policy.CloudID, "revision": policy.Revision, "enabled": policy.Enabled, "require_expansion_approval": policy.RequireExpansionApproval}})
	}
	s.wakeCapacityCloud(ctx, p.OrgID, policy.CloudID, fmt.Sprintf("policy:%d", policy.Revision))
}

func validateCapacityPolicy(policy CapacityPolicy, options map[string]provider.ServerOptions) error {
	if err := validateCapacityPolicyShape(policy); err != nil {
		return err
	}
	return validateCapacityPolicyChoices(&policy, options)
}

// capacityCeilingNames names each capacityValues dimension in error messages.
var capacityCeilingNames = [4]string{"server", "CPU", "memory", "storage"}

func capacityValues(v ResourceVector) []int64 {
	return []int64{v.Servers, v.CPUMillis, v.MemoryBytes, v.StorageBytes}
}
func nonnegativeCapacity(v ResourceVector) bool {
	for _, n := range capacityValues(v) {
		if n < 0 {
			return false
		}
	}
	return gpuShapeValid(v)
}
func capacityFits(v, limit ResourceVector) bool {
	a, b := capacityValues(v), capacityValues(limit)
	for i, n := range a {
		if n > b[i] {
			return false
		}
	}
	return gpusFit(v, limit)
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
	kind, gpus, err := addGPUs(a, b)
	if err != nil {
		return ResourceVector{}, err
	}
	return ResourceVector{Servers: out[0], CPUMillis: out[1], MemoryBytes: out[2], StorageBytes: out[3], GPUKind: kind, GPUs: gpus}, nil
}
func multiplyCapacity(v ResourceVector, n int64) (ResourceVector, error) {
	values := append(capacityValues(v), v.GPUs)
	out := [5]int64{}
	for i, value := range values {
		if value < 0 || n < 0 || (value > 0 && n > math.MaxInt64/value) {
			return ResourceVector{}, ErrInvalid
		}
		out[i] = value * n
	}
	if !gpuShapeValid(v) {
		return ResourceVector{}, ErrInvalid
	}
	kind := v.GPUKind
	if out[4] == 0 {
		kind = ""
	}
	return ResourceVector{Servers: out[0], CPUMillis: out[1], MemoryBytes: out[2], StorageBytes: out[3], GPUKind: kind, GPUs: out[4]}, nil
}
func validateCapacityPolicyShape(policy CapacityPolicy) error {
	if policy.Budget != nil {
		b := policy.Budget
		if b.MaxHourlyMicroUSD < 0 || b.MaxHorizonMicroUSD < 0 || b.HorizonSeconds < 0 {
			return ErrInvalid
		}
		for _, n := range vectorValues(policy.Limits) {
			if n <= 0 {
				return ErrInvalid
			}
		}
	}
	if !nonnegativeCapacity(policy.Limits) || len(policy.Pools) > 100 || len(policy.FrontendLimits) > 1000 {
		return ErrInvalid
	}
	if policy.Enabled {
		for i, n := range capacityValues(policy.Limits) {
			if n <= 0 {
				return fmt.Errorf("%w: enablement needs a positive %s ceiling", ErrInvalid, capacityCeilingNames[i])
			}
		}
	}
	pools := map[string]bool{}
	for _, pool := range policy.Pools {
		if strings.TrimSpace(pool.PoolID) == "" || pools[pool.PoolID] || pool.MinServers < 0 || pool.MaxServers < 0 || pool.MinServers > pool.MaxServers || !nonnegativeCapacity(pool.SystemReserve) || pool.SystemReserve.Servers != 0 {
			return ErrInvalid
		}
		pools[pool.PoolID] = true
		if pool.Limits != nil && (!nonnegativeCapacity(*pool.Limits) || !capacityFits(withoutGPUs(*pool.Limits), policy.Limits)) {
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
	return validateGPUPolicyShape(policy)
}

type bootstrapChoice struct {
	size string
	// resources holds the four counted dimensions; gpus counts the pool's kind.
	resources ResourceVector
	gpus      int64
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
		if err := validateGPUPoolCeiling(policy, pool); err != nil {
			return err
		}
		catalog := options[pool.PoolID]
		sizes := slices.Clone(pool.AllowedSizes)
		slices.Sort(sizes)
		for _, id := range sizes {
			var size provider.ServerSize
			for _, candidate := range catalog.Sizes {
				if candidate.ID == id {
					size = candidate
					break
				}
			}
			// GPU rules come first so a GPU size on a plain image says why.
			if size.ID != "" {
				if err := validatePoolSizeAccelerator(pool, catalog, size); err != nil {
					return err
				}
			}
			if err := validateOptions(catalog, ServerInput{Region: pool.Region, Size: id, Image: pool.Image}); err != nil {
				return fmt.Errorf("%w: unavailable expansion template", ErrInvalid)
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
			_, perServer := sizeGPUs(size)
			gpus, err := multiplyCapacity(ResourceVector{GPUKind: pool.AcceleratorKind, GPUs: perServer}, pool.MinServers)
			if err != nil {
				return err
			}
			if capacityFits(total, policy.Limits) && (pool.Limits == nil || capacityFits(total, withoutGPUs(*pool.Limits))) && bootstrapGPUsFit(policy, pool, gpus.GPUs) {
				choices[i] = append(choices[i], bootstrapChoice{id, total, gpus.GPUs})
			}
		}
		if len(choices[i]) == 0 {
			return fmt.Errorf("%w: minimum needs a supported homogeneous bootstrap allocation", ErrInvalid)
		}
	}
	// Bound work for large catalogs. Failure means the configuration is unproven.
	attempts := 0
	gpuTotals := map[string]int64{}
	var fit func(int, ResourceVector) bool
	fit = func(i int, total ResourceVector) bool {
		if i == len(choices) {
			return true
		}
		kind := policy.Pools[i].AcceleratorKind
		for _, choice := range choices[i] {
			attempts++
			if attempts > 10000 {
				return false
			}
			next, err := addCapacity(total, choice.resources)
			if err != nil || !capacityFits(next, policy.Limits) {
				continue
			}
			before := gpuTotals[kind]
			if choice.gpus > 0 {
				limit, _ := gpuLimit(policy, kind)
				if choice.gpus > limit-before {
					continue
				}
			}
			gpuTotals[kind] = before + choice.gpus
			policy.Pools[i].BootstrapSize = choice.size
			if fit(i+1, next) {
				return true
			}
			gpuTotals[kind] = before
		}
		return false
	}
	if !fit(0, ResourceVector{}) {
		return fmt.Errorf("%w: minimum needs a supported homogeneous bootstrap allocation within Cloud ceilings", ErrInvalid)
	}
	return nil
}

// bootstrapGPUsFit checks one pool's minimum against its own and the Cloud's GPU ceilings.
func bootstrapGPUsFit(policy *CapacityPolicy, pool PoolExpansionPolicy, gpus int64) bool {
	if gpus == 0 {
		return true
	}
	if pool.Limits != nil && pool.Limits.GPUs > 0 && gpus > pool.Limits.GPUs {
		return false
	}
	limit, _ := gpuLimit(policy, pool.AcceleratorKind)
	return gpus <= limit
}
