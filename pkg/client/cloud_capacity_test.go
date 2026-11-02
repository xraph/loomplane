package client

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/pkg/provider"
)

// mirrors decodes the server's JSON strictly into the client type and checks
// that encoding it again loses nothing.
func mirrors[T any](t *testing.T, name string, server any) {
	t.Helper()
	raw, err := json.Marshal(server)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&out); err != nil {
		t.Fatalf("%s: %v in %s", name, err, raw)
	}
	again, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err = json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(again, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("%s drifted:\nserver %s\nclient %s", name, raw, again)
	}
}

func TestCapacityClientContract(t *testing.T) {
	n := func(v int64) *int64 { return &v }
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	gpuLimits := &cloudinfra.ResourceVector{Servers: 2, CPUMillis: 16000, MemoryBytes: 64 << 30, StorageBytes: 400_000_000_000, GPUKind: "nvidia-h100-80gb", GPUs: 2}
	template := cloudinfra.PoolExpansionPolicy{BootstrapSize: "gpu-h100x1-80gb", PoolID: "gpu", AcceleratorKind: "nvidia-h100-80gb", ConnectionID: "do", Region: "tor1", Architecture: "amd64", Image: "gpu-h100x1-base", AllowedSizes: []string{"gpu-h100x1-80gb"}, MinServers: 0, MaxServers: 2, Limits: gpuLimits, SystemReserve: cloudinfra.ResourceVector{CPUMillis: 500, MemoryBytes: 512 << 20}}
	expansion := cloudinfra.Expansion{
		Revision: 3, Minimum: true, Template: template,
		Block: &cloudinfra.CapacityBlock{Reason: "expansion_approval", Resource: "gpus:nvidia-h100-80gb", CloudID: "cloud", PoolID: "gpu", TargetID: "target", Limit: 2, Counted: 1, Requested: 1, PolicyRevision: 4, Retryable: true},
		ID:    "exp", OrgID: "org", CloudID: "cloud", PoolID: "gpu", OperationID: "op", Status: "awaiting_approval", IntentHash: "intent", PolicyRevision: 4,
		Resources: cloudinfra.ResourceVector{Servers: 1, CPUMillis: 20000, MemoryBytes: 240 << 30, StorageBytes: 720_000_000_000, GPUKind: "nvidia-h100-80gb", GPUs: 1},
		Provider:  "digitalocean", AccountID: "account", ConnectionID: "do", ConnectionGeneration: 2, Region: "tor1", Size: "gpu-h100x1-80gb", Architecture: "amd64", Image: "gpu-h100x1-base",
		ApprovedBy: "manager", ApprovedAt: &at, CreatedAt: at, UpdatedAt: at,
		EstimatedHourlyUSD: 3.39, HourlyPriceKnown: true,
		Accelerator: &provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: provider.VendorNVIDIA, Count: 1, MemoryGiB: 80},
	}
	mirrors[Expansion](t, "expansion", expansion)
	mirrors[ExpansionList](t, "expansion list", map[string]any{"expansions": []cloudinfra.Expansion{expansion}})

	limit := n(5)
	policy := cloudinfra.CapacityPolicy{OrgID: "org", CloudID: "cloud", Revision: 4, Enabled: true, RequireExpansionApproval: true,
		Limits:         cloudinfra.ResourceVector{Servers: 4, CPUMillis: 32000, MemoryBytes: 128 << 30, StorageBytes: 1_000_000_000_000},
		Pools:          []cloudinfra.PoolExpansionPolicy{template},
		FrontendLimits: []cloudinfra.FrontendLimit{{TargetID: "vercel", Maximum: limit}, {TargetID: "netlify"}},
		GPULimits:      []cloudinfra.GPULimit{{Kind: "nvidia-l4", Maximum: 2}, {Kind: "nvidia-h100-80gb", Maximum: 2}},
	}
	mirrors[CapacityPolicy](t, "policy", policy)
	mirrors[CapacityPolicyInput](t, "policy input", cloudinfra.CapacityPolicyInput{ExpectedRevision: 4, Policy: policy})
	pool := &cloudinfra.Pool{ID: "pool_01", OrgID: "org", CloudID: "cloud", Name: "GPU", Revision: 1, Members: []cloudinfra.PoolMember{}, AcceleratorKind: "nvidia-h100-80gb"}
	mirrors[ScalingPoolInput](t, "scaling pool input", cloudinfra.ScalingPoolInput{Name: "GPU", AcceleratorKind: "nvidia-h100-80gb", ExpectedRevision: 4, Policy: policy, Template: template})
	mirrors[ScalingPool](t, "scaling pool", cloudinfra.ScalingPool{Pool: pool, Policy: &policy})

	known := cloudinfra.CapacityResources{Servers: n(1), CPUMillis: n(2000), MemoryBytes: n(2 << 30), StorageBytes: n(20_000_000_000)}
	usage := cloudinfra.CapacityUsage{
		Accounts:    []cloudinfra.CapacityAccountSnapshot{{Provider: "digitalocean", AccountID: "account", Provisioned: known, Quota: provider.CapacityQuota{Servers: n(25)}, CheckedAt: at}},
		Records:     []cloudinfra.CapacityRecord{{Kind: "server", Provider: "digitalocean", AccountID: "account", ResourceID: "server:1", ServerID: "s", ServerStateRevision: 2, Resources: known, Observation: &cloudinfra.CapacityObservation{CPUMillis: n(2000), FreeStorageBytes: n(10), CheckedAt: at}, CheckedAt: at, GPUKind: "nvidia-l4", GPUs: n(1)}},
		Provisioned: known, Pending: known, Reserved: known, Observed: cloudinfra.CapacityResources{}, SystemReserve: known, Allocatable: known, Headroom: known,
		ObservedStorageBytes: n(5), CheckedAt: at, Fresh: true,
		GPUs:      []cloudinfra.GPUUsage{{Kind: "nvidia-l4", Provisioned: nil, Reserved: 2, Pending: 1, Limit: n(4)}},
		Frontends: []cloudinfra.FrontendUsage{{TargetID: "vercel", Maximum: limit, Counted: 3, Active: 1, Pending: 1, Retained: 1}, {TargetID: "netlify"}},
	}
	mirrors[CapacityUsage](t, "usage", usage)

	// The unread GPU count stays unknown in the client, never zero.
	raw, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	var decoded CapacityUsage
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.GPUs[0].Provisioned != nil || decoded.GPUs[0].Limit == nil || *decoded.GPUs[0].Limit != 4 || decoded.Frontends[1].Maximum != nil {
		t.Fatalf("unknown became a number: %+v", decoded)
	}
}

func TestCapacityClientRequests(t *testing.T) {
	tests := []struct {
		name, method, path, body, response string
		call                               func(*Client) error
	}{
		{"policy", "GET", "/api/v1/clouds/c/capacity-policy", "", `{"cloud_id":"c","policy":{"revision":2,"pools":[],"frontend_limits":[]},"automatic_expansion":false}`, func(c *Client) error {
			v, e := c.CapacityPolicy(t.Context(), "c")
			if e == nil && (v.Policy == nil || v.Policy.Revision != 2 || v.AutomaticExpansion) {
				t.Fatalf("policy %+v", v)
			}
			return e
		}},
		{"save policy", "POST", "/api/v1/clouds/c/capacity-policy", `{"expected_revision":2,"policy":{"enabled":false,"require_expansion_approval":true,"limits":{"servers":1,"cpu_millis":1,"memory_bytes":1,"storage_bytes":1},"pools":[],"frontend_limits":[],"gpu_limits":[{"kind":"nvidia-l4","maximum":2}],"org_id":"","cloud_id":"","revision":0}}`, `{"cloud_id":"c","policy":{"revision":3},"automatic_expansion":true}`, func(c *Client) error {
			v, e := c.SaveCapacityPolicy(t.Context(), "c", CapacityPolicyInput{ExpectedRevision: 2, Policy: CapacityPolicy{RequireExpansionApproval: true, Limits: ResourceVector{Servers: 1, CPUMillis: 1, MemoryBytes: 1, StorageBytes: 1}, Pools: []PoolExpansionPolicy{}, FrontendLimits: []FrontendLimit{}, GPULimits: []GPULimit{{Kind: "nvidia-l4", Maximum: 2}}}})
			if e == nil && (v.Policy == nil || v.Policy.Revision != 3 || !v.AutomaticExpansion) {
				t.Fatalf("saved %+v", v)
			}
			return e
		}},
		{"usage", "GET", "/api/v1/clouds/c/capacity-usage", "", `{"gpus":[{"kind":"nvidia-l4","provisioned":null,"pending":1,"reserved":2,"limit":4}],"frontends":[]}`, func(c *Client) error {
			v, e := c.CapacityUsage(t.Context(), "c")
			if e == nil && (len(v.GPUs) != 1 || v.GPUs[0].Provisioned != nil) {
				t.Fatalf("usage %+v", v)
			}
			return e
		}},
		{"refresh", "POST", "/api/v1/clouds/c/capacity-usage/refresh", "", `{"fresh":true}`, func(c *Client) error {
			v, e := c.RefreshCapacity(t.Context(), "c")
			if e == nil && !v.Fresh {
				t.Fatalf("refresh %+v", v)
			}
			return e
		}},
		{"expansions", "GET", "/api/v1/clouds/c/expansions", "", `{"expansions":[{"id":"exp","estimated_hourly_usd":3.39,"hourly_price_known":true}]}`, func(c *Client) error {
			v, e := c.Expansions(t.Context(), "c")
			if e == nil && (len(v) != 1 || v[0].EstimatedHourlyUSD != 3.39 || !v[0].HourlyPriceKnown) {
				t.Fatalf("expansions %+v", v)
			}
			return e
		}},
		{"scaling pool", "POST", "/api/v1/clouds/c/scaling-pools", `{"name":"L4","accelerator_kind":"nvidia-l4","expected_revision":1,"policy":{"enabled":true,"require_expansion_approval":false,"limits":{"servers":1,"cpu_millis":1,"memory_bytes":1,"storage_bytes":1},"pools":[],"frontend_limits":[],"gpu_limits":[{"kind":"nvidia-l4","maximum":1}],"org_id":"","cloud_id":"","revision":0},"template":{"bootstrap_size":"","pool_id":"","accelerator_kind":"nvidia-l4","connection_id":"do","region":"tor1","architecture":"amd64","image":"gpu-base","allowed_sizes":["gpu-l4"],"min_servers":0,"max_servers":1,"limits":null,"system_reserve":{"servers":0,"cpu_millis":0,"memory_bytes":0,"storage_bytes":0}}}`, `{"pool":{"id":"pool_01","members":[],"accelerator_kind":"nvidia-l4","revision":1},"policy":{"revision":2,"pools":[{"pool_id":"pool_01"}]}}`, func(c *Client) error {
			v, e := c.CreateScalingPool(t.Context(), "c", ScalingPoolInput{Name: "L4", AcceleratorKind: "nvidia-l4", ExpectedRevision: 1,
				Policy:   CapacityPolicy{Enabled: true, Limits: ResourceVector{Servers: 1, CPUMillis: 1, MemoryBytes: 1, StorageBytes: 1}, Pools: []PoolExpansionPolicy{}, FrontendLimits: []FrontendLimit{}, GPULimits: []GPULimit{{Kind: "nvidia-l4", Maximum: 1}}},
				Template: PoolExpansionPolicy{AcceleratorKind: "nvidia-l4", ConnectionID: "do", Region: "tor1", Architecture: "amd64", Image: "gpu-base", AllowedSizes: []string{"gpu-l4"}, MaxServers: 1}})
			if e == nil && (v.Pool == nil || v.Pool.ID != "pool_01" || len(v.Pool.Members) != 0 || v.Policy == nil || v.Policy.Revision != 2 || v.Policy.Pools[0].PoolID != "pool_01") {
				t.Fatalf("scaling pool %+v", v)
			}
			return e
		}},
		{"approve", "POST", "/api/v1/clouds/c/expansions/exp/approve", `{"expected_revision":3}`, `{"id":"exp","revision":4}`, func(c *Client) error {
			v, e := c.ApproveExpansion(t.Context(), "c", "exp", 3)
			if e == nil && v.Revision != 4 {
				t.Fatalf("approved %+v", v)
			}
			return e
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tt.method || r.URL.Path != tt.path || r.Header.Get("Authorization") != "Bearer scoped-key" {
					t.Errorf("request %s %s auth=%s", r.Method, r.URL.Path, r.Header.Get("Authorization"))
				}
				if tt.body != "" {
					var got, want any
					if e := json.NewDecoder(r.Body).Decode(&got); e != nil {
						t.Error(e)
					}
					if e := json.Unmarshal([]byte(tt.body), &want); e != nil {
						t.Fatal(e)
					}
					if !reflect.DeepEqual(got, want) {
						a, _ := json.Marshal(got)
						t.Errorf("body %s want %s", a, tt.body)
					}
				}
				_, _ = w.Write([]byte(tt.response))
			}))
			defer srv.Close()
			if e := tt.call(New(srv.URL, "scoped-key")); e != nil {
				t.Fatal(e)
			}
		})
	}
}
