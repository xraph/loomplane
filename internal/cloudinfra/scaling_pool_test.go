package cloudinfra

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// scalingStore records what CreateScalingPool and SavePool were asked to write.
type scalingStore struct {
	capacityRuntimeStore
	pool     *Pool
	policy   *CapacityPolicy
	previous int64
	saves    int
}

func (s *scalingStore) CreateScalingPool(_ context.Context, pool *Pool, policy *CapacityPolicy, previous int64) error {
	s.pool, s.policy, s.previous = pool, policy, previous
	return nil
}
func (s *scalingStore) SavePool(context.Context, *Pool, int64) error {
	s.saves++
	return nil
}
func (*scalingStore) GetCapacityPolicy(context.Context, string, string) (*CapacityPolicy, error) {
	return &CapacityPolicy{Revision: 1, Enabled: true, Limits: ResourceVector{Servers: 1, CPUMillis: 1, MemoryBytes: 1, StorageBytes: 1}}, nil
}

// scalingCatalog sells one L4 size and a GPU-ready image in tor1.
type scalingCatalog struct{ provider.ServerAdapter }

func (scalingCatalog) ServerOptions(context.Context, provider.InfrastructureCredential) (provider.ServerOptions, error) {
	return provider.ServerOptions{Regions: []provider.ServerRegion{{ID: "tor1"}},
		Sizes: []provider.ServerSize{{ID: "gpu-l4", CPUs: 4, MemoryMiB: 16384, DiskGB: 100, Architectures: []string{"amd64"}, Regions: []string{"tor1"},
			Accelerator: &provider.Accelerator{Kind: "nvidia-l4", Vendor: provider.VendorNVIDIA, Count: 1, MemoryGiB: 24}}},
		Images: []provider.ServerImage{{ID: "gpu-base", Architecture: "amd64", Bootstrap: true, Regions: []string{"tor1"}, Accelerators: []string{provider.VendorNVIDIA}}}}, nil
}

func scalingService(t *testing.T, st Store) *Service {
	t.Helper()
	adapters := NewAdapters()
	if err := adapters.RegisterServer(connections.DigitalOcean, scalingCatalog{}); err != nil {
		t.Fatal(err)
	}
	return NewService(Deps{Store: st, Clouds: capacityAPIClouds{}, Authz: cleanupTestAuth{}, Connections: &optionsAccount{generation: 1}, Adapters: adapters})
}

// scalingInput grows an L4 pool from zero to two servers under an enabled policy.
func scalingInput() ScalingPoolInput {
	cloud := ResourceVector{Servers: 4, CPUMillis: 16000, MemoryBytes: 64 << 30, StorageBytes: 400_000_000_000}
	return ScalingPoolInput{Name: " Inference ", AcceleratorKind: "nvidia-l4", ExpectedRevision: 1,
		Policy:   CapacityPolicy{Enabled: true, Limits: cloud, GPULimits: []GPULimit{{Kind: "nvidia-l4", Maximum: 2}}},
		Template: PoolExpansionPolicy{PoolID: "ignored", AcceleratorKind: "nvidia-l4", ConnectionID: "do", Region: "tor1", Architecture: "amd64", Image: "gpu-base", AllowedSizes: []string{"gpu-l4"}, MinServers: 0, MaxServers: 2}}
}

var scalingManager = security.Principal{OrgID: "org", UserID: "manager"}

func TestScalingPoolBuildsPoolAndPolicy(t *testing.T) {
	st := &scalingStore{}
	in := scalingInput()
	got, err := scalingService(t, st).CreateScalingPool(t.Context(), scalingManager, "cloud", in)
	if err != nil {
		t.Fatal(err)
	}
	pool, policy := st.pool, st.policy
	if pool == nil || !strings.HasPrefix(pool.ID, "pool_") || pool.Name != "Inference" || pool.Revision != 1 || pool.Members == nil || len(pool.Members) != 0 || pool.AcceleratorKind != "nvidia-l4" || pool.OrgID != "org" || pool.CloudID != "cloud" {
		t.Fatalf("pool %+v", pool)
	}
	if st.previous != 1 || policy.Revision != 2 || policy.OrgID != "org" || policy.CloudID != "cloud" || len(policy.Pools) != 1 || policy.authority == nil || policy.authority.UserID != "manager" {
		t.Fatalf("policy %+v previous %d", policy, st.previous)
	}
	if template := policy.Pools[0]; template.PoolID != pool.ID || template.BootstrapSize != "gpu-l4" || template.AcceleratorKind != "nvidia-l4" {
		t.Fatalf("template %+v", template)
	}
	if got.Pool != pool || got.Policy.FrontendLimits == nil {
		t.Fatalf("result %+v", got)
	}
	// The caller's policy keeps its own pool list.
	if in.Policy.Pools != nil || in.Template.PoolID != "ignored" {
		t.Fatalf("input changed: %+v", in)
	}
}

func TestScalingPoolRefusals(t *testing.T) {
	tests := []struct {
		name  string
		p     security.Principal
		edit  func(*ScalingPoolInput)
		want  error
		words string
	}{
		{"read-only key", security.Principal{OrgID: "org", UserID: "manager", KeyID: "key", ReadOnly: true}, func(*ScalingPoolInput) {}, security.ErrForbidden, ""},
		{"no name", scalingManager, func(in *ScalingPoolInput) { in.Name = "  " }, ErrInvalid, ""},
		{"policy off", scalingManager, func(in *ScalingPoolInput) { in.Policy.Enabled = false }, ErrInvalid, "A pool with no servers grows only under an enabled capacity policy."},
		{"unknown kind", scalingManager, func(in *ScalingPoolInput) {
			in.AcceleratorKind, in.Template.AcceleratorKind = "nvidia-made-up", "nvidia-made-up"
		}, ErrInvalid, "known NVIDIA kind"},
		{"template for CPU", scalingManager, func(in *ScalingPoolInput) { in.Template.AcceleratorKind = "" }, ErrInvalid, "holds nvidia-l4 servers but its expansion template buys CPU servers"},
		{"no GPU ceiling", scalingManager, func(in *ScalingPoolInput) { in.Policy.GPULimits = nil }, ErrInvalid, "A GPU pool needs a GPU ceiling for nvidia-l4."},
		{"GPU-only pool ceiling", scalingManager, func(in *ScalingPoolInput) {
			in.Template.Limits = &ResourceVector{GPUKind: "nvidia-l4", GPUs: 1}
		}, ErrInvalid, "A pool GPU ceiling needs the pool's server ceiling too."},
		{"pool ceiling without storage", scalingManager, func(in *ScalingPoolInput) {
			in.Template.Limits = &ResourceVector{Servers: 2, CPUMillis: 8000, MemoryBytes: 32 << 30, GPUKind: "nvidia-l4", GPUs: 1}
		}, ErrInvalid, "A pool GPU ceiling needs the pool's storage ceiling too."},
		{"negative revision", scalingManager, func(in *ScalingPoolInput) { in.ExpectedRevision = -1 }, ErrInvalid, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &scalingStore{}
			in := scalingInput()
			tt.edit(&in)
			_, err := scalingService(t, st).CreateScalingPool(t.Context(), tt.p, "cloud", in)
			if !errors.Is(err, tt.want) || !strings.Contains(err.Error(), tt.words) {
				t.Fatalf("got %v, want %v %q", err, tt.want, tt.words)
			}
			if st.pool != nil {
				t.Fatalf("refused create reached the store: %+v", st.pool)
			}
		})
	}
	// A full pool ceiling with a GPU count is fine.
	st := &scalingStore{}
	in := scalingInput()
	in.Template.Limits = &ResourceVector{Servers: 2, CPUMillis: 8000, MemoryBytes: 32 << 30, StorageBytes: 200_000_000_000, GPUKind: "nvidia-l4", GPUs: 1}
	if _, err := scalingService(t, st).CreateScalingPool(t.Context(), scalingManager, "cloud", in); err != nil {
		t.Fatal(err)
	}
	// A store that can't write both at once refuses rather than writing half.
	if _, err := scalingService(t, &capacityRuntimeStore{}).CreateScalingPool(t.Context(), scalingManager, "cloud", scalingInput()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("store without scaling pools: %v", err)
	}
}

// SavePool still refuses a new pool with no servers. Only CreateScalingPool
// can make one, because only it saves the policy that grows the pool.
func TestEmptyPoolStillRejectedWithoutPolicy(t *testing.T) {
	st := &scalingStore{}
	svc := scalingService(t, st)
	if _, err := svc.SavePool(t.Context(), scalingManager, "cloud", "", PoolInput{Name: "Empty", Members: []PoolMember{}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty new pool: %v", err)
	}
	if _, err := svc.SavePool(t.Context(), scalingManager, "cloud", "", PoolInput{Name: "Empty GPU", AcceleratorKind: "nvidia-l4"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty new GPU pool: %v", err)
	}
	if st.saves != 0 {
		t.Fatalf("an empty new pool reached the store %d times", st.saves)
	}
}
