package planning

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

func intentFixture() (*Plan, *cloudprofiles.Assignment, cloudprofiles.Target, cloudinfra.PlacementRequest, *cloudinfra.CapacityPolicy) {
	p := &Plan{ID: "plan", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Sources: []PlanSource{{Alias: "app", CommitSHA: "commit"}}}
	a := &cloudprofiles.Assignment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", CloudID: "cloud", Revision: 2, CloudRevision: 3, GrantRevision: 4}
	target := cloudprofiles.Target{ID: "target", OrgID: "org", CloudID: "cloud", PoolID: "pool", Revision: 5, Provider: "server"}
	req := cloudinfra.PlacementRequest{PlanID: p.ID, OrgID: p.OrgID, ProjectID: p.ProjectID, EnvironmentID: p.EnvironmentID, CloudID: a.CloudID, TargetID: target.ID, PoolID: target.PoolID, Service: "web", CPUMillis: 1000, MemoryBytes: 512 << 20, Replicas: 1}
	policy := &cloudinfra.CapacityPolicy{Enabled: true, Revision: 1, Pools: []cloudinfra.PoolExpansionPolicy{{PoolID: "pool", ConnectionID: "provider", Region: "region", Architecture: "amd64", Image: "image", AllowedSizes: []string{"size"}, MaxServers: 3}}}
	return p, a, target, req, policy
}
func TestCapacityIntentApproval(t *testing.T) {
	p, a, target, req, policy := intentFixture()
	got, err := newCapacityIntent(p, a, target, req, policy, "app")
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitSHA != "commit" || got.Demand.Resources.CPUMillis != 1000 || got.TemplateHash == "" || got.Demand.IntentHash == "" || got.AssignmentRevision != 2 {
		t.Fatalf("unbounded intent: %+v", got)
	}
	policy.Enabled = false
	if _, err = newCapacityIntent(p, a, target, req, policy, "app"); err == nil {
		t.Fatal("disabled pool accepted future intent")
	}
}
func TestCapacityIntentLocalData(t *testing.T) {
	p, a, target, req, policy := intentFixture()
	req.LocalData = true
	if _, err := newCapacityIntent(p, a, target, req, policy, "app"); err == nil {
		t.Fatal("local data moved")
	}
	req.LocalData = false
	req.StickyServerID = "old"
	if _, err := newCapacityIntent(p, a, target, req, policy, "app"); err == nil {
		t.Fatal("existing host moved without review")
	}
}
func TestCapacityIntentTemplateCeiling(t *testing.T) {
	_, _, _, _, policy := intentFixture()
	before, _ := capacityTemplateHash(policy.Pools[0])
	policy.Pools[0].MaxServers++
	after, _ := capacityTemplateHash(policy.Pools[0])
	if before != after {
		t.Fatal("ceiling changed workload template")
	}
	policy.Pools[0].Image = "other"
	after, _ = capacityTemplateHash(policy.Pools[0])
	if before == after {
		t.Fatal("template change lost")
	}
}

type futurePoolStore struct {
	catalogStore
	policy *cloudinfra.CapacityPolicy
}

func (f futurePoolStore) PreviewPlacement(context.Context, cloudinfra.PlacementRequest) (*cloudinfra.Placement, error) {
	return nil, cloudinfra.ErrConflict
}
func (f futurePoolStore) GetCapacityPolicy(context.Context, string, string) (*cloudinfra.CapacityPolicy, error) {
	return f.policy, nil
}

func TestCapacityIntentEmptyPoolPlan(t *testing.T) {
	s, p, r := targetPlanningService(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: shared, endpoint: {host_port: 8080}, build: {builder: conn_01m4vewksnea2bwdxwdy5aekhn, registry: conn_01m4vem4sde8ab4yfr1abnj3wf}}\n")
	_, a, target, _, policy := intentFixture()
	target.Name = "shared"
	a.Targets = []cloudprofiles.Target{target}
	r.assignment = a
	p.ID = "plan"
	s.capacityTemplates = futureTemplateValidator{}
	s.store = futurePoolStore{policy: policy}
	s.clouds = targetOptions{connections: []*connections.Connection{{ID: targetA, Cloud: "server", Status: "ready", Details: map[string]string{"build_workers": "dedicated", "architecture": "amd64", "docker_id": "builder"}}, {ID: registryTarget, Cloud: "registry", Status: "ready"}}}
	result, _, err := s.compute(t.Context(), p)
	if err != nil || !result.Executable {
		t.Fatalf("future pool blocked: %+v %v", result, err)
	}
	got := result.Actions[0]
	if got.Service.CapacityIntent == nil || got.ConnectionID != "" || got.Service.Target != "" || got.Service.CloudPlacement != nil {
		t.Fatalf("fake host: %+v", got)
	}
	if got.Service.CapacityIntent.CommitSHA != got.Service.CommitSHA {
		t.Fatal("commit differs")
	}
	policy.Enabled = false
	result, _, err = s.compute(t.Context(), p)
	if err != nil || result.Executable {
		t.Fatalf("disabled pool changed legacy behavior: %+v %v", result, err)
	}
}
func TestCapacityIntentPrivateDependency(t *testing.T) {
	p, a, target, req, policy := intentFixture()
	intent, err := newCapacityIntent(p, a, target, req, policy, "app")
	if err != nil {
		t.Fatal(err)
	}
	in := Input{Environment: "production", Graph: infrastructureGraph(), Providers: infrastructureProviders(t), Manifest: parse(t, "version: 1\nservices:\n  web: {source: cmd/api, provider: server, endpoint: {host_port: 8080}, needs: [api], build: {builder: conn_01m4vewksnea2bwdxwdy5aekhn, registry: conn_01m4vem4sde8ab4yfr1abnj3wf}}\n  api: {source: cmd/api, provider: server, public: false, build: {builder: conn_01m4vewksnea2bwdxwdy5aekhn, registry: conn_01m4vem4sde8ab4yfr1abnj3wf}}\n"), Connections: []ConnectionOption{{ID: targetA, Cloud: "server", Ready: true, Details: map[string]string{"build_workers": "dedicated", "architecture": "amd64", "docker_id": "builder"}}, {ID: registryTarget, Cloud: "registry", Ready: true}}, CapacityIntents: map[string]*CapacityIntent{"web": intent, "api": intent}}
	result := Compute(in)
	if result.Executable {
		t.Fatal("future hosts implied private connectivity")
	}
	found := false
	for _, problem := range result.Problems {
		if strings.Contains(problem.Code, "target") || strings.Contains(problem.Code, "private") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing private network finding: %+v", result.Problems)
	}
}

type futureTemplateValidator struct{}

func (futureTemplateValidator) ValidateCapacityTemplate(context.Context, security.Principal, cloudinfra.CapacityDemand, cloudinfra.PoolExpansionPolicy) (*cloudinfra.CapacityTemplatePin, error) {
	return &cloudinfra.CapacityTemplatePin{ConnectionGeneration: 1, AccountID: "provider"}, nil
}

// storedCPUIntent is an approved intent body as written before capacity had a
// GPU dimension. Loading re-hashes it, so new fields must not change its digest.
const storedCPUIntent = `{"demand":{"org_id":"org","cloud_id":"cloud","pool_id":"pool","target_id":"target","project_id":"project","environment_id":"env","plan_id":"plan","service":"web","architecture":"amd64","sticky_server_id":"","resources":{"servers":0,"cpu_millis":1000,"memory_bytes":536870912,"storage_bytes":0},"local_data":false,"policy_revision":1,"intent_hash":"0d0956b300639d27557fa45861fa51c4b811b62fad91b325c1590a42c2d8bc11"},"provider_pin":{"connection_generation":1,"account_id":"account"},"template":{"bootstrap_size":"","pool_id":"pool","connection_id":"provider","region":"region","architecture":"amd64","image":"image","allowed_sizes":["size"],"min_servers":0,"max_servers":0,"limits":null,"system_reserve":{"servers":0,"cpu_millis":250,"memory_bytes":268435456,"storage_bytes":0}},"template_hash":"055c8bb206dec8b49e1c053a12faddd5118161c0b4220eaf839bd2b09d68c87d","commit_sha":"commit","assignment_revision":2,"cloud_revision":3,"target_revision":5,"grant_revision":4,"target_name":""}`

func TestStoredCPUIntentDigestUnchanged(t *testing.T) {
	var intent CapacityIntent
	if err := json.Unmarshal([]byte(storedCPUIntent), &intent); err != nil {
		t.Fatal(err)
	}
	saved := intent.Demand.IntentHash
	intent.Demand.IntentHash = ""
	if hash, err := Digest(intent); err != nil || hash != saved {
		t.Fatalf("stored intent digest moved: %s, want %s (%v)", hash, saved, err)
	}
	if hash, err := capacityTemplateHash(intent.Template); err != nil || hash != intent.TemplateHash {
		t.Fatalf("stored template hash moved: %s, want %s (%v)", hash, intent.TemplateHash, err)
	}
	intent.Demand.IntentHash = saved
	raw, err := json.Marshal(intent)
	if err != nil || string(raw) != storedCPUIntent {
		t.Fatalf("stored intent re-encodes differently:\n%s\n%s", raw, storedCPUIntent)
	}

	// A fresh intent for the same CPU workload still hashes the same way.
	p, a, target, req, policy := intentFixture()
	policy.Pools[0].SystemReserve = cloudinfra.ResourceVector{CPUMillis: 250, MemoryBytes: 256 << 20}
	fresh, err := newCapacityIntent(p, a, target, req, policy, "app")
	if err != nil {
		t.Fatal(err)
	}
	fresh.ProviderPin = cloudinfra.CapacityTemplatePin{AccountID: "account", ConnectionGeneration: 1}
	fresh.Demand.IntentHash = ""
	if hash, hashErr := Digest(fresh); hashErr != nil || hash != saved {
		t.Fatalf("fresh CPU intent digest moved: %s, want %s (%v)", hash, saved, hashErr)
	}
}

func TestCapacityIntentCarriesGPUs(t *testing.T) {
	p, a, target, req, policy := intentFixture()
	req.GPUKind, req.GPUs = "nvidia-l4", 1
	policy.Pools[0].AcceleratorKind = "nvidia-l4"
	got, err := newCapacityIntent(p, a, target, req, policy, "app")
	if err != nil {
		t.Fatal(err)
	}
	if r := got.Demand.Resources; r.GPUKind != "nvidia-l4" || r.GPUs != 1 || r.CPUMillis != 1000 {
		t.Fatalf("GPU demand: %+v", r)
	}

	policy.Pools[0].AcceleratorKind = "nvidia-h100-80gb"
	_, err = newCapacityIntent(p, a, target, req, policy, "app")
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "this pool's servers have nvidia-h100-80gb GPUs; the service asks for nvidia-l4") {
		t.Fatalf("GPU kind mismatch: %v", err)
	}
}

// A GPU pool grows only for GPU work, and a CPU pool never for GPU work.
func TestCapacityIntentKeepsGPUAndCPUPoolsApart(t *testing.T) {
	p, a, target, req, policy := intentFixture()
	policy.Pools[0].AcceleratorKind = "nvidia-l4"
	if _, err := newCapacityIntent(p, a, target, req, policy, "app"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "GPU pool") {
		t.Fatalf("CPU work in a GPU pool: %v", err)
	}
	policy.Pools[0].AcceleratorKind = ""
	req.GPUKind, req.GPUs = "nvidia-l4", 1
	if _, err := newCapacityIntent(p, a, target, req, policy, "app"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "CPU pool") {
		t.Fatalf("GPU work in a CPU pool: %v", err)
	}
}

// Admission takes only a candidate with the GPUs the approved demand asks for.
func TestCapacityAdmissionComparesGPUs(t *testing.T) {
	p, a, target, req, policy := intentFixture()
	req.GPUKind, req.GPUs = "nvidia-l4", 1
	policy.Pools[0].AcceleratorKind = "nvidia-l4"
	intent, err := newCapacityIntent(p, a, target, req, policy, "app")
	if err != nil {
		t.Fatal(err)
	}
	action := Action{Name: "web", Service: &ServiceConfig{CapacityIntent: intent}}
	matching := cloudinfra.Placement{PoolID: "pool", ServerID: "host", ConnectionID: "runtime", CPUMillis: 1000, MemoryBytes: 512 << 20, GPUKind: "nvidia-l4", GPUs: 1}
	for name, candidate := range map[string]cloudinfra.Placement{
		"no GPUs":     {PoolID: "pool", ServerID: "host", ConnectionID: "runtime", CPUMillis: 1000, MemoryBytes: 512 << 20},
		"other kind":  {PoolID: "pool", ServerID: "host", ConnectionID: "runtime", CPUMillis: 1000, MemoryBytes: 512 << 20, GPUKind: "nvidia-h100-80gb", GPUs: 1},
		"other count": {PoolID: "pool", ServerID: "host", ConnectionID: "runtime", CPUMillis: 1000, MemoryBytes: 512 << 20, GPUKind: "nvidia-l4", GPUs: 2},
	} {
		// The store is never reached: a mismatch is refused before any query.
		var admission *cloudinfra.CapacityAdmissionError
		if err = (&PgStore{}).admitCapacityPlacement(t.Context(), p, action, candidate); !errors.As(err, &admission) || admission.Block.Reason != "review_required" {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if !capacityCandidateFits(intent.Demand, matching) {
		t.Fatal("matching GPU candidate refused")
	}
}
