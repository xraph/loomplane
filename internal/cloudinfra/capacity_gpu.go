package cloudinfra

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/gpusizes"
	"github.com/xraph/loomplane/pkg/provider"
)

// A ResourceVector holds one GPU kind at most, so it stays comparable. These
// helpers keep the GPU dimension beside the four counted ones: capacityValues
// and vectorValues stay four wide and GPU ceilings live in GPULimits.

// sentenceError is an ErrInvalid whose text is a full sentence for the person
// editing the policy, shown as written.
type sentenceError struct{ text string }

func (e *sentenceError) Error() string { return e.text }
func (e *sentenceError) Unwrap() error { return ErrInvalid }

func invalidSentence(format string, args ...any) error {
	return &sentenceError{text: fmt.Sprintf(format, args...)}
}

// gpuShapeValid holds when GPUs are not negative and a count has exactly one kind.
func gpuShapeValid(v ResourceVector) bool {
	return v.GPUs >= 0 && (v.GPUs == 0) == (v.GPUKind == "")
}

// addGPUs sums two counts of one kind. A side without GPUs takes the other's kind.
func addGPUs(a, b ResourceVector) (kind string, gpus int64, err error) {
	if !gpuShapeValid(a) || !gpuShapeValid(b) {
		return "", 0, ErrInvalid
	}
	if a.GPUs > 0 && b.GPUs > 0 && a.GPUKind != b.GPUKind {
		return "", 0, fmt.Errorf("%w: a capacity vector holds one GPU kind", ErrInvalid)
	}
	if a.GPUs > math.MaxInt64-b.GPUs {
		return "", 0, ErrInvalid
	}
	kind = a.GPUKind
	if kind == "" {
		kind = b.GPUKind
	}
	return kind, a.GPUs + b.GPUs, nil
}

// gpusFit checks only a vector that asks for GPUs: same kind, enough of them.
func gpusFit(v, limit ResourceVector) bool {
	return v.GPUs == 0 || (v.GPUKind == limit.GPUKind && v.GPUs <= limit.GPUs)
}

// withoutGPUs keeps the four counted dimensions, for checks against Cloud
// ceilings whose GPU part lives in GPULimits.
func withoutGPUs(v ResourceVector) ResourceVector {
	v.GPUKind, v.GPUs = "", 0
	return v
}

// sizeGPUs reads the GPUs one server of size carries. CPU sizes return "", 0.
func sizeGPUs(size provider.ServerSize) (kind string, count int64) {
	if size.Accelerator == nil || size.Accelerator.Count <= 0 || size.Accelerator.Kind == "" {
		return "", 0
	}
	return size.Accelerator.Kind, int64(size.Accelerator.Count)
}

// nvidiaKind reports whether kind is a known NVIDIA accelerator kind. Pools
// and ceilings take only these: AMD GPU workloads aren't supported yet.
func nvidiaKind(kind string) bool {
	k, ok := provider.LookupAcceleratorKind(kind)
	return ok && k.Vendor == provider.VendorNVIDIA
}

// sizeAcceleratorError names why a pool can't count a size's accelerator, or
// returns nil for a CPU size or a known NVIDIA kind with devices.
func sizeAcceleratorError(size provider.ServerSize) error {
	a := size.Accelerator
	switch {
	case a == nil:
		return nil
	case a.Vendor == provider.VendorAMD || strings.HasPrefix(a.Kind, provider.VendorAMD+"-"):
		return fmt.Errorf("%w: AMD GPU servers aren't supported yet", ErrInvalid)
	case a.Count <= 0:
		return fmt.Errorf("%w: size %s reports no GPUs", ErrInvalid, size.ID)
	case a.Vendor != provider.VendorNVIDIA || !nvidiaKind(a.Kind):
		return fmt.Errorf("%w: Loomplane doesn't know the GPU kind %q on size %s yet", ErrInvalid, a.Kind, size.ID)
	}
	return nil
}

func sizeAcceleratorValid(size provider.ServerSize) bool { return sizeAcceleratorError(size) == nil }

// gpuReadyImage reports whether the pool's image ships drivers for size's GPUs.
func gpuReadyImage(options provider.ServerOptions, image string, size provider.ServerSize) bool {
	if size.Accelerator == nil {
		return true
	}
	for _, v := range options.Images {
		if v.ID == image && slices.Contains(v.Accelerators, size.Accelerator.Vendor) {
			return true
		}
	}
	return false
}

// gpuImageError names the first bootstrap image in region that suits size.
func gpuImageError(options provider.ServerOptions, region string, size provider.ServerSize) error {
	for _, v := range options.Images {
		if v.Bootstrap && slices.Contains(v.Regions, region) && size.Accelerator != nil && slices.Contains(v.Accelerators, size.Accelerator.Vendor) {
			return invalidSentence("A GPU server needs a GPU-ready image such as %s.", v.ID)
		}
	}
	return invalidSentence("A GPU server needs a GPU-ready image, and this region offers none.")
}

func gpuLimit(p *CapacityPolicy, kind string) (int64, bool) {
	for _, l := range p.GPULimits {
		if l.Kind == kind {
			return l.Maximum, true
		}
	}
	return 0, false
}

// checkGPULimit blocks requested GPUs of kind that would take the Cloud past
// its GPU ceiling. A kind without a ceiling allows none. A nil count means the
// inventory is unknown, which is never read as zero usage.
func checkGPULimit(p *CapacityPolicy, d CapacityDemand, kind string, counted *int64, requested int64) *CapacityBlock {
	if requested <= 0 {
		return nil
	}
	resource := "gpus:" + kind
	limit, ok := gpuLimit(p, kind)
	if !ok || limit <= 0 {
		b := capacityBlock(p, d, "hard_limit", resource)
		b.Requested = requested
		if counted != nil {
			b.Counted = *counted
		}
		return b
	}
	if counted == nil {
		return capacityBlock(p, d, "stale_inventory", resource)
	}
	if *counted > limit || requested > limit-*counted {
		b := capacityBlock(p, d, "hard_limit", resource)
		b.Limit, b.Counted, b.Requested = limit, *counted, requested
		return b
	}
	return nil
}

// checkPoolGPULimit blocks GPUs past a pool's own ceiling, in checkGPULimit's
// style. A pool ceiling of zero means none, which leaves the Cloud's ceiling in
// charge, and a ceiling only covers its own kind. A nil count is unknown.
func checkPoolGPULimit(p *CapacityPolicy, d CapacityDemand, pool PoolExpansionPolicy, kind string, counted *int64, requested int64) *CapacityBlock {
	if requested <= 0 || pool.Limits == nil || pool.Limits.GPUs <= 0 || pool.Limits.GPUKind != kind {
		return nil
	}
	resource := "gpus:" + kind
	if counted == nil {
		return capacityBlock(p, d, "stale_inventory", resource)
	}
	limit := pool.Limits.GPUs
	if *counted > limit || requested > limit-*counted {
		b := capacityBlock(p, d, "hard_limit", resource)
		b.Limit, b.Counted, b.Requested = limit, *counted, requested
		return b
	}
	return nil
}

// poolInventoryGPUs counts the GPUs of kind on a pool's member servers. A
// member without an inventory record, or with an unread count, makes it nil.
func poolInventoryGPUs(records []CapacityRecord, members map[string]bool, kind string) *int64 {
	var total int64
	for id := range members {
		found := false
		for _, r := range records {
			if r.Kind != "server" || r.ServerID != id {
				continue
			}
			if r.GPUs == nil || !recordGPUsValid(r) {
				return nil
			}
			found = true
			if r.GPUKind == kind {
				total += *r.GPUs
			}
			break
		}
		if !found {
			return nil
		}
	}
	return &total
}

// addGPUCount adds n to a known count. An unknown count stays unknown.
func addGPUCount(count *int64, n int64) (*int64, error) {
	if count == nil || n <= 0 {
		return count, nil
	}
	if *count > math.MaxInt64-n {
		return nil, ErrInvalid
	}
	return number(*count + n), nil
}

// validateGPUPolicyShape checks GPU ceilings and each pool's accelerator kind.
func validateGPUPolicyShape(policy CapacityPolicy) error {
	if policy.Limits.GPUs != 0 || policy.Limits.GPUKind != "" {
		return fmt.Errorf("%w: GPU ceilings go in gpu_limits", ErrInvalid)
	}
	if len(policy.GPULimits) > 100 {
		return ErrInvalid
	}
	kinds := map[string]int64{}
	for _, l := range policy.GPULimits {
		if _, dup := kinds[l.Kind]; dup || l.Maximum < 0 || !nvidiaKind(l.Kind) {
			return fmt.Errorf("%w: GPU ceilings need distinct known NVIDIA kinds and a maximum of zero or more", ErrInvalid)
		}
		kinds[l.Kind] = l.Maximum
	}
	for _, pool := range policy.Pools {
		if pool.SystemReserve.GPUs != 0 || pool.SystemReserve.GPUKind != "" {
			return fmt.Errorf("%w: a system reserve holds no GPUs", ErrInvalid)
		}
		if pool.AcceleratorKind == "" {
			if pool.Limits != nil && pool.Limits.GPUs != 0 {
				return fmt.Errorf("%w: a CPU pool has no GPU ceiling", ErrInvalid)
			}
			continue
		}
		if !nvidiaKind(pool.AcceleratorKind) {
			return fmt.Errorf("%w: a GPU pool needs a known NVIDIA kind", ErrInvalid)
		}
		if pool.Limits == nil || pool.Limits.GPUs == 0 {
			continue
		}
		if pool.Limits.GPUKind != pool.AcceleratorKind {
			return fmt.Errorf("%w: a pool's GPU ceiling must be for its own kind", ErrInvalid)
		}
		if cloud, ok := kinds[pool.AcceleratorKind]; ok && pool.Limits.GPUs > cloud {
			return fmt.Errorf("%w: pool ceilings exceed Cloud ceilings", ErrInvalid)
		}
	}
	return nil
}

// validatePoolSizeAccelerator keeps GPU sizes in GPU pools of their own kind,
// on an image with the drivers, and CPU sizes out of GPU pools.
func validatePoolSizeAccelerator(pool PoolExpansionPolicy, catalog provider.ServerOptions, size provider.ServerSize) error {
	if err := sizeAcceleratorError(size); err != nil {
		return err
	}
	kind, _ := sizeGPUs(size)
	if pool.AcceleratorKind == "" {
		if kind != "" {
			return fmt.Errorf("%w: GPU sizes need a GPU pool", ErrInvalid)
		}
		return nil
	}
	if kind != pool.AcceleratorKind {
		return fmt.Errorf("%w: every size in this GPU pool needs %s GPUs, and %s doesn't have them", ErrInvalid, pool.AcceleratorKind, size.ID)
	}
	if !gpuReadyImage(catalog, pool.Image, size) {
		return gpuImageError(catalog, pool.Region, size)
	}
	return nil
}

// validateGPUPoolCeiling asks an enabled policy for a ceiling on every GPU pool's kind.
func validateGPUPoolCeiling(policy *CapacityPolicy, pool PoolExpansionPolicy) error {
	if pool.AcceleratorKind == "" {
		return nil
	}
	if limit, ok := gpuLimit(policy, pool.AcceleratorKind); !ok || limit <= 0 {
		return invalidSentence("A GPU pool needs a GPU ceiling for %s.", pool.AcceleratorKind)
	}
	return nil
}

// gpuDemandFitsPool reports whether demand's GPUs belong in the pool. A pool
// minimum or catalog recheck passes an empty demand, which names no workload.
func gpuDemandFitsPool(pool PoolExpansionPolicy, demand ResourceVector) bool {
	if demand == (ResourceVector{}) {
		return true
	}
	return demand.GPUKind == pool.AcceleratorKind
}

// sizeServesDemand keeps CPU work off GPU sizes and GPU work on sizes with
// enough devices of its kind. An empty demand on a GPU pool takes the pool's kind.
func sizeServesDemand(pool PoolExpansionPolicy, demand ResourceVector, size provider.ServerSize) bool {
	kind, count := sizeGPUs(size)
	if size.Accelerator != nil && kind == "" {
		return false
	}
	want := demand.GPUKind
	if demand == (ResourceVector{}) {
		want = pool.AcceleratorKind
	}
	if want == "" {
		return kind == ""
	}
	return kind == want && count >= demand.GPUs
}

// serverGPUFacts reads the GPUs an inventory record counts for a server. A
// health check that listed devices wins, then one that found no GPU at all.
// Without either, the reviewed size counts: the create operation's capacity
// target when it bought the server's current size, else a reference GPU size.
// Anything else returns a nil count, which callers never read as zero.
func serverGPUFacts(v *Server, created *Operation) (kind string, gpus *int64) {
	if devices := v.Health.GPUs; len(devices) > 0 {
		kind = devices[0].Kind
		for _, d := range devices {
			if d.Kind == "" || d.Kind != kind {
				return "", nil
			}
		}
		return kind, number(int64(len(devices)))
	}
	if v.Health.GPURuntime == provider.GPUStatusNone {
		return "", number(0)
	}
	if created != nil && created.Input.Kind == "create" && created.Input.Size == v.Size && created.CapacityTarget != nil && gpuShapeValid(*created.CapacityTarget) {
		return created.CapacityTarget.GPUKind, number(created.CapacityTarget.GPUs)
	}
	if size, _, ok := gpusizes.Lookup(v.Size); ok && v.Size != "" {
		if k, n := sizeGPUs(size); k != "" {
			return k, number(n)
		}
	}
	return "", nil
}

// recordGPUsValid keeps a record's GPU facts in the shape the inventory table checks.
func recordGPUsValid(r CapacityRecord) bool {
	if r.GPUs == nil {
		return r.GPUKind == ""
	}
	return *r.GPUs >= 0 && (*r.GPUs == 0) == (r.GPUKind == "")
}

// inventoryGPUs counts the GPUs of each kind on the Cloud's inventoried
// servers, once per server. known is false when any server's count is unknown.
func inventoryGPUs(records []CapacityRecord) (counts map[string]int64, known bool, err error) {
	counts, known = map[string]int64{}, true
	seen := map[string]bool{}
	for _, r := range records {
		if r.Kind != "server" {
			continue
		}
		key := r.ServerID
		if key == "" {
			key = recordKey(r)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if !recordGPUsValid(r) {
			return nil, false, ErrInvalid
		}
		if r.GPUs == nil {
			known = false
			continue
		}
		if *r.GPUs > 0 {
			if counts[r.GPUKind] > math.MaxInt64-*r.GPUs {
				return nil, false, ErrInvalid
			}
			counts[r.GPUKind] += *r.GPUs
		}
	}
	return counts, known, nil
}

// cloudGPUs counts a Cloud's GPUs of kind for its ceiling: inventoried servers
// plus infrastructure reservations not yet in inventory. It returns nil when a
// server's count is unknown.
func cloudGPUs(records []CapacityRecord, reservations []scopedCapacityReservation, cloud, kind string) (*int64, error) {
	counts, known, err := inventoryGPUs(records)
	if err != nil || !known {
		return nil, err
	}
	total := counts[kind]
	for _, r := range reservations {
		if r.cloud != cloud || r.reservation.Delta.GPUKind != kind || r.reservation.Delta.GPUs <= 0 {
			continue
		}
		if total > math.MaxInt64-r.reservation.Delta.GPUs {
			return nil, ErrInvalid
		}
		total += r.reservation.Delta.GPUs
	}
	return &total, nil
}

// summarizeGPUs lists every GPU kind the inventory holds or the policy caps.
// Provisioned is null for every kind while any server's count is unknown.
func summarizeGPUs(records []CapacityRecord, policy CapacityPolicy) ([]GPUUsage, error) {
	counts, known, err := inventoryGPUs(records)
	if err != nil {
		return nil, err
	}
	var out []GPUUsage
	for kind, n := range counts {
		out = withGPUKind(out, kind)
		if known {
			gpuUsageFor(out, kind).Provisioned = number(n)
		}
	}
	for _, l := range policy.GPULimits {
		out = withGPUKind(out, l.Kind)
		gpuUsageFor(out, l.Kind).Limit = number(l.Maximum)
	}
	if known {
		for i := range out {
			if out[i].Provisioned == nil {
				out[i].Provisioned = number(0)
			}
		}
	}
	slices.SortFunc(out, func(a, b GPUUsage) int { return strings.Compare(a.Kind, b.Kind) })
	return out, nil
}

// withGPUKind adds an empty row for kind unless one is already there.
func withGPUKind(rows []GPUUsage, kind string) []GPUUsage {
	if gpuUsageFor(rows, kind) != nil {
		return rows
	}
	return append(rows, GPUUsage{Kind: kind})
}

func gpuUsageFor(rows []GPUUsage, kind string) *GPUUsage {
	for i := range rows {
		if rows[i].Kind == kind {
			return &rows[i]
		}
	}
	return nil
}
