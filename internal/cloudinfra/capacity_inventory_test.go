package cloudinfra

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestExpansionSize(t *testing.T) {
	catalog := provider.ServerOptions{Regions: []provider.ServerRegion{{ID: "region"}}, Images: []provider.ServerImage{{ID: "ubuntu", Bootstrap: true, Regions: []string{"region"}}}, Sizes: []provider.ServerSize{
		{ID: "small", CPUs: 1, MemoryMiB: 1024, DiskGB: 10, HourlyUSD: 0.01, Regions: []string{"region"}},
		{ID: "unknown-price", CPUs: 2, MemoryMiB: 2048, DiskGB: 20, Regions: []string{"region"}},
		{ID: "b", CPUs: 2, MemoryMiB: 2048, DiskGB: 20, HourlyUSD: 0.02, Regions: []string{"region"}},
		{ID: "a", CPUs: 2, MemoryMiB: 2048, DiskGB: 20, HourlyUSD: 0.02, Regions: []string{"region"}},
		{ID: "large", CPUs: 4, MemoryMiB: 4096, DiskGB: 40, HourlyUSD: 0.001, Regions: []string{"region"}},
	}}
	p := PoolExpansionPolicy{Region: "region", Image: "ubuntu", AllowedSizes: []string{"small", "unknown-price", "a", "b", "large"}, SystemReserve: ResourceVector{CPUMillis: 200}}
	demand := CapacityDemand{Resources: ResourceVector{CPUMillis: 1000, MemoryBytes: 1 << 30}}
	if id, err := SelectExpansionSize(catalog, p, demand); err != nil || id != "a" {
		t.Fatalf("selection %q: %v", id, err)
	}
	p.AllowedSizes = []string{"small"}
	if _, err := SelectExpansionSize(catalog, p, demand); err == nil {
		t.Fatal("accepted insufficient single host")
	}
	demand.Resources.CPUMillis = math.MaxInt64
	if _, err := SelectExpansionSize(catalog, p, demand); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overflow: %v", err)
	}
}

func TestCapacityDistinctHostsAndStaleSamples(t *testing.T) {
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	sample := CapacityObservation{CPUMillis: capacityNumber(1000), MemoryBytes: capacityNumber(1 << 30), StorageBytes: capacityNumber(10), FreeStorageBytes: capacityNumber(5), CheckedAt: now}
	record := CapacityRecord{Kind: "server", Provider: "digitalocean", AccountID: "account", ResourceID: "1", ServerID: "host", Resources: capacityResources(ResourceVector{Servers: 1, CPUMillis: 1000, MemoryBytes: 1 << 30, StorageBytes: 20}), Observation: &sample, CheckedAt: now}
	pools := []*Pool{{ID: "a", Members: []PoolMember{{ServerID: "host", CPUMillis: 1000, MemoryBytes: 1 << 30}}}, {ID: "b", Members: []PoolMember{{ServerID: "host", CPUMillis: 1000, MemoryBytes: 1 << 30}}}}
	usage, err := summarizeCapacity([]CapacityRecord{record, record}, pools, CapacityPolicy{}, now)
	if err != nil || usage.Provisioned.Servers == nil || *usage.Provisioned.Servers != 1 || *usage.Observed.CPUMillis != 1000 {
		t.Fatalf("host counted twice: %+v %v", usage, err)
	}

	other := record
	other.ResourceID = "2"
	other.ServerID = "other"
	otherSample := sample
	other.Observation = &otherSample
	pools[0].Members = append(pools[0].Members, PoolMember{ServerID: "other", CPUMillis: 1000, MemoryBytes: 1 << 30})
	usage, err = summarizeCapacity([]CapacityRecord{record, other}, pools, CapacityPolicy{}, now)
	if err != nil || *usage.Provisioned.CPUMillis != 2000 || *usage.Allocatable.CPUMillis != 2000 {
		t.Fatalf("two physical host totals: %+v %v", usage, err)
	}
	demand := CapacityDemand{Resources: ResourceVector{CPUMillis: 2000, MemoryBytes: 1}}
	for _, host := range []CapacityRecord{record, other} {
		if CapacityHostFits(*host.Observation, ResourceVector{}, demand, now) {
			t.Fatal("aggregate totals admitted a workload that fits no host")
		}
	}
	sample.CheckedAt = now.Add(-5*time.Minute - time.Nanosecond)
	if CapacityHostFits(sample, ResourceVector{}, CapacityDemand{Resources: ResourceVector{CPUMillis: 1}}, now) {
		t.Fatal("stale sample admitted")
	}
	usage, err = summarizeCapacity([]CapacityRecord{record}, pools, CapacityPolicy{}, now)
	if err != nil || usage.Fresh || usage.Allocatable.CPUMillis != nil {
		t.Fatalf("stale capacity looked usable: %+v %v", usage, err)
	}
}
func capacityNumber(n int64) *int64 { return &n }

func TestExpansionSizeCompatibilityAndPricing(t *testing.T) {
	catalog := provider.ServerOptions{Regions: []provider.ServerRegion{{ID: "region"}}, Images: []provider.ServerImage{{ID: "ubuntu", Architecture: "amd64", Bootstrap: true, Regions: []string{"region"}}}, Sizes: []provider.ServerSize{
		{ID: "unknown", CPUs: 2, MemoryMiB: 2048, DiskGB: 20, Regions: []string{"region"}},
		{ID: "zero", CPUs: 2, MemoryMiB: 2048, DiskGB: 20, HourlyPriceKnown: true, Regions: []string{"region"}},
		{ID: "foreign-arch", CPUs: 1, MemoryMiB: 1024, DiskGB: 10, Architectures: []string{"arm64"}, Regions: []string{"region"}},
		{ID: "foreign-region", CPUs: 1, MemoryMiB: 1024, DiskGB: 10, Regions: []string{"other"}},
	}}
	pool := PoolExpansionPolicy{Region: "region", Architecture: "amd64", Image: "ubuntu", AllowedSizes: []string{"unknown", "zero", "foreign-arch", "foreign-region"}}
	demand := CapacityDemand{Architecture: "amd64", Resources: ResourceVector{CPUMillis: 1, MemoryBytes: 1}}
	if id, err := SelectExpansionSize(catalog, pool, demand); err != nil || id != "zero" {
		t.Fatalf("compatibility: %s %v", id, err)
	}
	demand.Architecture = "arm64"
	var constraint *CapacityPlacementError
	if _, err := SelectExpansionSize(catalog, pool, demand); !errors.As(err, &constraint) || constraint.Reason != "placement_constraint" {
		t.Fatalf("wrong block: %v", err)
	}
	demand.Architecture = "amd64"
	demand.LocalData = true
	demand.Resources.StorageBytes = 1
	if _, err := SelectExpansionSize(catalog, pool, demand); !errors.Is(err, provider.ErrInfrastructureUnsupported) {
		t.Fatalf("local quota: %v", err)
	}
	demand.LocalData = false
	demand.Resources.StorageBytes = 0
	catalog.Images[0].MinDiskGB = 21
	if _, err := SelectExpansionSize(catalog, pool, demand); err == nil {
		t.Fatal("size smaller than image minimum accepted")
	}
	catalog.Images[0].MinDiskGB = 0
	catalog.Images[0].Architecture = ""
	if _, err := SelectExpansionSize(catalog, pool, demand); err == nil {
		t.Fatal("unknown image architecture accepted")
	}
}

func TestCapacityUnknownAndOverflow(t *testing.T) {
	now := time.Now()
	r := CapacityRecord{Kind: "server", Provider: "server", AccountID: "daemon", ResourceID: "server:daemon", ServerID: "host", CheckedAt: now, Resources: CapacityResources{Servers: capacityNumber(1)}, Observation: &CapacityObservation{CheckedAt: now}}
	usage, err := summarizeCapacity([]CapacityRecord{r}, nil, CapacityPolicy{}, now)
	if err != nil || usage.Provisioned.CPUMillis != nil || usage.Observed.CPUMillis != nil || usage.Observed.MemoryBytes != nil || usage.Observed.StorageBytes != nil || usage.Pending.Servers != nil || usage.Reserved.Servers != nil {
		t.Fatalf("unknown became zero: %+v %v", usage, err)
	}
	r.Resources = capacityResources(ResourceVector{Servers: 1, CPUMillis: math.MaxInt64})
	other := r
	other.ResourceID = "server:other"
	if _, err = summarizeCapacity([]CapacityRecord{r, other}, nil, CapacityPolicy{}, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overflow: %v", err)
	}
}

type capacityRuntimeStore struct {
	Store
	host       Server
	records    []CapacityRecord
	generation int64
}

func (s *capacityRuntimeStore) ListServers(context.Context, string, string) ([]*Server, error) {
	v := s.host
	return []*Server{&v}, nil
}
func (s *capacityRuntimeStore) GetServer(context.Context, string, string, string) (*Server, error) {
	v := s.host
	return &v, nil
}
func (s *capacityRuntimeStore) SaveServer(_ context.Context, v *Server) error {
	s.host = *v
	return nil
}
func (*capacityRuntimeStore) GetCapacityPolicy(context.Context, string, string) (*CapacityPolicy, error) {
	return &CapacityPolicy{}, nil
}
func (*capacityRuntimeStore) SaveCapacityPolicy(context.Context, *CapacityPolicy, int64) error {
	return nil
}
func (*capacityRuntimeStore) GetPool(context.Context, string, string, string) (*Pool, error) {
	return nil, ErrNotFound
}
func (*capacityRuntimeStore) SavePool(context.Context, *Pool, int64) error { return nil }
func (s *capacityRuntimeStore) ListPools(context.Context, string, string) ([]*Pool, error) {
	return []*Pool{{ID: "pool", Members: []PoolMember{{ServerID: s.host.ID, CPUMillis: 1000, MemoryBytes: 1 << 30}}}}, nil
}
func (s *capacityRuntimeStore) ListCapacityInventory(context.Context, string, string) ([]CapacityRecord, error) {
	return s.records, nil
}
func (s *capacityRuntimeStore) BeginCapacityRefresh(context.Context, string, string) (int64, error) {
	s.generation++
	return s.generation, nil
}
func (s *capacityRuntimeStore) SaveCapacityInventory(_ context.Context, _, _ string, generation int64, records []CapacityRecord, _ []CapacityAccountSnapshot) error {
	if generation != s.generation {
		return ErrConflict
	}
	s.records = records
	return nil
}
func (*capacityRuntimeStore) GetCapacityAccount(context.Context, string, string, string) (*CapacityAccountSnapshot, error) {
	return nil, ErrNotFound
}

type capacityRuntime struct {
	health  RuntimeHealth
	failure bool
}

func (r *capacityRuntime) Inspect(context.Context, []byte, map[string]string, bool) (RuntimeHealth, error) {
	if r.failure {
		return RuntimeHealth{}, errors.New("unavailable")
	}
	return r.health, nil
}

type capacityRuntimeCredentials struct{}

func (capacityRuntimeCredentials) Credential(context.Context, string, string) (*connections.Connection, []byte, error) {
	return &connections.Connection{Cloud: "server", Details: map[string]string{"docker_id": "daemon"}}, []byte("fixture"), nil
}
func TestCapacityRuntimeSamplingAndFailure(t *testing.T) {
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	store := &capacityRuntimeStore{host: Server{ID: "host", OrgID: "org", CloudID: "cloud", Provider: "server", AccountID: "daemon", ResourceID: "daemon", RuntimeConnectionID: "runtime", Status: "ready", Health: RuntimeHealth{DockerID: "daemon", CheckedAt: now.Add(-time.Hour)}}}
	runtime := &capacityRuntime{health: RuntimeHealth{DockerID: "daemon", EffectiveCPUMillis: 1000, EffectiveMemoryBytes: 1 << 30, StorageBytes: capacityNumber(900), FreeStorageBytes: capacityNumber(100), CheckedAt: now}}
	service := NewService(Deps{Store: store, Connections: capacityRuntimeCredentials{}, Runtime: runtime})
	service.now = func() time.Time { return now }
	usage, err := service.refreshCapacity(t.Context(), "org", "cloud")
	if err != nil || !usage.Fresh || *usage.Observed.CPUMillis != 1000 || *usage.Allocatable.StorageBytes != 100 {
		t.Fatalf("sample: %+v %v", usage, err)
	}
	if store.host.Health.CheckedAt != now {
		t.Fatal("health check was not persisted")
	}
	runtime.health.StorageBytes = nil
	runtime.health.FreeStorageBytes = nil
	usage, err = service.refreshCapacity(t.Context(), "org", "cloud")
	if err != nil || !usage.Fresh || usage.Observed.StorageBytes != nil || usage.Allocatable.StorageBytes != nil || *usage.Observed.CPUMillis != 1000 {
		t.Fatalf("missing disk hid verified CPU: %+v %v", usage, err)
	}
	runtime.failure = true
	usage, err = service.refreshCapacity(t.Context(), "org", "cloud")
	if err != nil || usage.Fresh || usage.Observed.CPUMillis != nil || usage.Allocatable.MemoryBytes != nil || store.host.Status != "unhealthy" {
		t.Fatalf("failed probe admitted stale health: %+v %v", usage, err)
	}
}
