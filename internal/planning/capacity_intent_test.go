package planning

import (
	"context"
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
	s.clouds = targetOptions{connections: []*connections.Connection{{ID: targetA, Cloud: "server", Status: "ready", Details: map[string]string{"architecture": "amd64", "docker_id": "builder"}}, {ID: registryTarget, Cloud: "registry", Status: "ready"}}}
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
	in := Input{Environment: "production", Graph: infrastructureGraph(), Providers: infrastructureProviders(t), Manifest: parse(t, "version: 1\nservices:\n  web: {source: cmd/api, provider: server, endpoint: {host_port: 8080}, needs: [api], build: {builder: conn_01m4vewksnea2bwdxwdy5aekhn, registry: conn_01m4vem4sde8ab4yfr1abnj3wf}}\n  api: {source: cmd/api, provider: server, public: false, build: {builder: conn_01m4vewksnea2bwdxwdy5aekhn, registry: conn_01m4vem4sde8ab4yfr1abnj3wf}}\n"), Connections: []ConnectionOption{{ID: targetA, Cloud: "server", Ready: true, Details: map[string]string{"architecture": "amd64", "docker_id": "builder"}}, {ID: registryTarget, Cloud: "registry", Ready: true}}, CapacityIntents: map[string]*CapacityIntent{"web": intent, "api": intent}}
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
