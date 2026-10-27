//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

// GPU targets subtract what current bindings hold from a server's GPUs:
// a service's gpu and a catalog component's, by connection and kind, and
// nothing of another organization's or of a binding without a GPU.
func TestGPUBindingsSumsCurrentBindings(t *testing.T) {
	f := newPoolFixture(t)
	ctx := t.Context()
	if _, err := f.sql.Exec(ctx, `INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('elsewhere','other','Elsewhere','elsewhere','ready','usr',now(),now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sql.Exec(ctx, `INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('elsewhere-env','other','elsewhere','production','production',now(),now())`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	put := func(id, org, project, env, connection string, subject planning.Subject, cfg any) {
		t.Helper()
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.plans.PutBinding(ctx, &planning.Binding{
			ID: id, OrgID: org, ProjectID: project, EnvironmentID: env, Subject: subject, Name: id,
			Provider: "server", Engine: "container", ConnectionID: connection, Config: raw, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	gpu := func(kind string, count int) *provider.GPUPlacement {
		return &provider.GPUPlacement{GPURequest: provider.GPURequest{Kind: kind, Count: count}}
	}
	put("web", "org", "proj", "env", "runtime-1", planning.ServiceSubject, map[string]any{"gpu": gpu("nvidia-l4", 1)})
	put("api", "org", "second", "second-env", "runtime-1", planning.ServiceSubject, map[string]any{"gpu": gpu("nvidia-l4", 2)})
	put("embed", "org", "proj", "env", "runtime-1", planning.ResourceSubject, map[string]any{"component": map[string]any{"gpu": gpu("nvidia-t4", 1)}})
	put("llm", "org", "proj", "env", "runtime-2", planning.ResourceSubject, map[string]any{"gpu": nil, "component": map[string]any{"gpu": gpu("nvidia-l4", 1)}})
	put("cpu", "org", "proj", "env", "runtime-1", planning.ServiceSubject, map[string]any{"gpu": nil})
	put("plain", "org", "proj", "env", "runtime-2", planning.ServiceSubject, map[string]any{"image": "nginx"})
	put("theirs", "other", "elsewhere", "elsewhere-env", "runtime-1", planning.ServiceSubject, map[string]any{"gpu": gpu("nvidia-l4", 4)})

	got, err := f.plans.GPUBindings(ctx, "org")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]int{"runtime-1": {"nvidia-l4": 3, "nvidia-t4": 1}, "runtime-2": {"nvidia-l4": 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GPUs held = %v, want %v", got, want)
	}
	if err = f.plans.DeleteBinding(ctx, "org", "web"); err != nil {
		t.Fatal(err)
	}
	if got, err = f.plans.GPUBindings(ctx, "org"); err != nil || got["runtime-1"]["nvidia-l4"] != 2 {
		t.Fatalf("after a delete: %v %v", got, err)
	}
}

// A server connection's GPU target is priced from the Cloud server that
// runs it, in whichever Cloud; deleted servers don't count.
func TestRuntimeServersListServersThatRunAConnection(t *testing.T) {
	f := newPoolFixture(t)
	now := time.Now().UTC()
	cs := connections.NewPgStore(f.db)
	if err := cs.CreateConnection(t.Context(), &connections.Connection{ID: "do", OrgID: "org", Cloud: connections.DigitalOcean, Name: "do", Account: "acct", Details: map[string]string{"account_uuid": "acct"}, Status: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"runtime-priced", "runtime-gone"} {
		if err := cs.CreateConnection(t.Context(), &connections.Connection{ID: id, OrgID: "org", Cloud: connections.Server, Name: id, Account: id, Details: map[string]string{"docker_id": id, "architecture": "amd64"}, Status: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []*cloudinfra.Server{
		{ID: "server-priced", OrgID: "org", CloudID: "cloud", ConnectionID: "do", RuntimeConnectionID: "runtime-priced", Ownership: "managed", Provider: "digitalocean", AccountID: "acct", ResourceID: "1", Status: "ready", Revision: 1, CreatedAt: now, UpdatedAt: now, HourlyUSD: 0.76, HourlyPriceKnown: true, PricedSize: "gpu-l4x1", Accelerator: &provider.Accelerator{Kind: "nvidia-l4", Vendor: provider.VendorNVIDIA, Count: 1, MemoryGiB: 24}},
		{ID: "server-gone", OrgID: "org", CloudID: "cloud", ConnectionID: "do", RuntimeConnectionID: "runtime-gone", Ownership: "managed", Provider: "digitalocean", AccountID: "acct", ResourceID: "2", Status: "deleted", Revision: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "server-new", OrgID: "org", CloudID: "cloud", ConnectionID: "do", Ownership: "managed", Provider: "digitalocean", AccountID: "acct", Status: "provisioning", Revision: 1, CreatedAt: now, UpdatedAt: now},
	} {
		if err := f.store.SaveServer(t.Context(), v); err != nil {
			t.Fatal(err)
		}
	}
	servers, err := f.store.RuntimeServers(t.Context(), "org")
	if err != nil {
		t.Fatal(err)
	}
	byConnection := map[string]*cloudinfra.Server{}
	for _, v := range servers {
		byConnection[v.RuntimeConnectionID] = v
	}
	if len(servers) != 3 || byConnection["runtime-1"] == nil || byConnection["runtime-2"] == nil || byConnection["runtime-gone"] != nil {
		t.Fatalf("servers: %v", byConnection)
	}
	if p := byConnection["runtime-priced"]; p == nil || p.HourlyUSD != 0.76 || !p.HourlyPriceKnown || p.Accelerator == nil {
		t.Fatalf("priced server: %+v", p)
	}
	if servers, err = f.store.RuntimeServers(t.Context(), "other"); err != nil || len(servers) != 0 {
		t.Fatalf("another organization: %v %v", servers, err)
	}
}

// The GPU targets API offers the GPU sizes a connected DigitalOcean account
// can create, with their price, reads each account's sizes once per ten
// minutes, and reports an account it can't read without failing the list.
func TestGPUTargets_listCreatableSizesAndUnreachableAccounts(t *testing.T) {
	const good, limited = "dop_v1_0123456789abcdef", "dop_v1_fedcba9876543210"
	var sizeReads atomic.Int32
	do := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token != good && token != limited {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v2/account":
			_, _ = fmt.Fprintf(w, `{"account":{"uuid":"acct-%s","email":"ada@example.com","status":"active"}}`, token[len(token)-4:])
		case token == limited:
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"id":"too_many_requests","message":"API rate limit exceeded."}`))
		case r.URL.Path == "/v2/regions":
			_, _ = w.Write([]byte(`{"regions":[{"slug":"tor1","name":"Toronto 1","available":true},{"slug":"nyc2","name":"New York 2","available":true}],"links":{}}`))
		case r.URL.Path == "/v2/sizes":
			sizeReads.Add(1)
			_, _ = w.Write([]byte(`{"sizes":[
				{"slug":"gpu-h100x1-80gb","memory":245760,"vcpus":20,"disk":720,"available":true,"price_hourly":3.39,"price_monthly":2522.16,"regions":["tor1","nyc2"],"gpu_info":{"count":1,"model":"nvidia_h100","vram":{"amount":80,"unit":"gib"}}},
				{"slug":"s-2vcpu-4gb","memory":4096,"vcpus":2,"disk":80,"available":true,"price_hourly":0.03571,"price_monthly":24,"regions":["tor1","nyc2"]}],"links":{}}`))
		case r.URL.Path == "/v2/images":
			_, _ = w.Write([]byte(`{"images":[
				{"id":1,"slug":"gpu-h100x1-base","name":"AI/ML Ready","distribution":"Ubuntu","public":true,"regions":["tor1"],"min_disk_size":500},
				{"id":2,"slug":"ubuntu-24-04-x64","name":"24.04 (LTS) x64","distribution":"Ubuntu","public":true,"regions":["tor1","nyc2"],"min_disk_size":7}],"links":{}}`))
		default:
			t.Errorf("asked DigitalOcean %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer do.Close()
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Clouds.DigitalOceanEndpoint = do.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}

	c.must(http.StatusUnauthorized, http.MethodGet, "/api/v1/gpu-targets", nil, nil)
	signedIn(t, c)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "digitalocean", "name": "Good", "credential": good}, nil)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "digitalocean", "name": "Limited", "credential": limited}, nil)

	for range 2 {
		var list gpus.GPUTargetList
		c.must(http.StatusOK, http.MethodGet, "/api/v1/gpu-targets?count=1&min_memory_gib=40", nil, &list)
		if len(list.Targets) != 1 {
			t.Fatalf("targets %+v", list.Targets)
		}
		size := list.Targets[0]
		if size.Source != gpus.SourceServerSize || size.SizeID != "gpu-h100x1-80gb" || !size.Fits || size.HourlyUSD != 3.39 || !size.HourlyPriceKnown ||
			size.Accelerator.Kind != "nvidia-h100-80gb" || size.Free != 1 || len(size.Regions) != 1 || size.Regions[0] != "tor1" {
			t.Fatalf("size %+v", size)
		}
		if len(list.Unreachable) != 1 || list.Unreachable[0].Name != "Limited" || list.Unreachable[0].Message == "" {
			t.Fatalf("unreachable %+v", list.Unreachable)
		}
	}
	if n := sizeReads.Load(); n != 1 {
		t.Fatalf("sizes read %d times within ten minutes", n)
	}
	c.must(http.StatusUnprocessableEntity, http.MethodGet, "/api/v1/gpu-targets?count=two", nil, nil)
	c.must(http.StatusUnprocessableEntity, http.MethodGet, "/api/v1/gpu-targets?count=9", nil, nil)
}
