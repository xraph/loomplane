package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/deploy"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

type ownedInstances struct {
	instance.Service
	value   *instance.Instance
	updates int
}

func (s *ownedInstances) Get(context.Context, id.ID) (*instance.Instance, error) {
	if s.value == nil {
		return nil, ctrlplane.ErrNotFound
	}
	snapshot := *s.value
	snapshot.Labels = maps.Clone(s.value.Labels)
	return &snapshot, nil
}
func (s *ownedInstances) GetBySlug(ctx context.Context, _ string) (*instance.Instance, error) {
	return s.Get(ctx, id.Nil)
}
func (s *ownedInstances) Update(_ context.Context, _ id.ID, r instance.UpdateRequest) (*instance.Instance, error) {
	s.updates++
	s.value.Services = r.Services
	s.value.Labels = maps.Clone(r.Labels)
	return s.value, nil
}
func (s *ownedInstances) Delete(context.Context, id.ID) error { s.value = nil; return nil }

type ownedDeploys struct {
	deploy.Service
	fail  bool
	after func()
}

func (s *ownedDeploys) Deploy(context.Context, deploy.DeployRequest) (*deploy.Deployment, error) {
	if s.fail {
		if s.after != nil {
			s.after()
		}
		return nil, errors.New("rollout failed")
	}
	return &deploy.Deployment{}, nil
}

type ownedFixtureBackend struct {
	owned.Backend
	revision string
}

func (b *ownedFixtureBackend) Apply(_ context.Context, _ id.ID, spec cpprovider.ServiceSpec, _ map[string]string) (*cpprovider.ProvisionResult, error) {
	b.revision = spec.Image
	return &cpprovider.ProvisionResult{}, nil
}
func (b *ownedFixtureBackend) Status(context.Context, id.ID, map[string]string) (*cpprovider.InstanceStatus, error) {
	return &cpprovider.InstanceStatus{Ready: true, Metadata: map[string]string{"exists": "true", "revision": b.revision}}, nil
}

func TestOwnedTarget_failedReplacementRestoresPersistedSpecAndSecretReference(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	oldLabels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelRuntime: "org_fixture/env_fixture/api/runtime_old"}
	prior := cpprovider.ServiceSpec{Name: mainService, Image: "old-image", Env: map[string]string{"MODE": "old"}}
	instances := &ownedInstances{value: &instance.Instance{Entity: ctrlplane.Entity{ID: iid}, Name: "api", TenantID: "org_fixture", ProviderName: "server.conn_fixture", Services: []cpprovider.ServiceSpec{prior}, Labels: maps.Clone(oldLabels)}}
	secrets := &memSecrets{m: map[string][]byte{oldLabels[owned.LabelRuntime]: []byte(`{"TOKEN":"old-secret"}`)}}
	backend := &ownedFixtureBackend{revision: "old-image"}
	e := &OwnedTarget{Cloud: connections.Server, Ctrl: CtrlPlane{Providers: cpprovider.NewRegistry(), Instances: instances, Deploys: &ownedDeploys{fail: true, after: func() { backend.revision = "new-image" }}}, Secrets: secrets, Backend: func(string, string) owned.Backend { return backend }}
	job := &Job{Deployment: &Deployment{ID: "dep_fixture", OrgID: "org_fixture", ProjectID: "proj_fixture", EnvironmentID: "env_fixture"}, Plan: &planning.Plan{ID: "plan_fixture"}, Connection: &connections.Connection{ID: "conn_fixture"}, Action: planning.Action{ID: "service:api", Name: "api", ConnectionID: "conn_fixture", Service: &planning.ServiceConfig{Target: "conn_fixture", BuildTarget: &planning.BuildTarget{Image: "docker.io/team/api@sha256:" + strings.Repeat("a", 64)}}}, Env: map[string]string{"TOKEN": "new-secret", "MODE": "new"}, Secrets: map[string]bool{"TOKEN": true}, Progress: func(string, string) {}}
	job.Binding = &planning.Binding{ExternalID: iid.String()}
	var checkpoint []byte
	job.Checkpoint = func(raw []byte) error { checkpoint = append([]byte(nil), raw...); return nil }
	if _, err := e.Apply(t.Context(), job); err == nil {
		t.Fatal("failed replacement accepted")
	}
	if backend.revision != "old-image" {
		t.Fatal("old workload was not restored after provider mutation")
	}
	if instances.value.Services[0].Image != "old-image" || instances.value.Labels[owned.LabelRuntime] != oldLabels[owned.LabelRuntime] {
		t.Fatalf("old spec not restored: %+v", instances.value)
	}
	if strings.Contains(string(checkpoint), "new-secret") || strings.Contains(string(checkpoint), "old-secret") {
		t.Fatal("secret entered durable rollout checkpoint")
	}
	var state ownedState
	if err := json.Unmarshal(checkpoint, &state); err != nil {
		t.Fatal(err)
	}
	if state.Image.Reference == "" || state.PriorInstance != iid.String() || state.PriorLabels[owned.LabelRuntime] != oldLabels[owned.LabelRuntime] {
		t.Fatalf("recovery checkpoint missing: %+v", state)
	}
	job.Resume = checkpoint
	if _, err := e.Apply(t.Context(), job); err == nil {
		t.Fatal("retry failure accepted")
	}
	if string(secrets.m[oldLabels[owned.LabelRuntime]]) != `{"TOKEN":"old-secret"}` {
		t.Fatal("old runtime credential overwritten")
	}
}

func TestOwnedTarget_checksRecordedPlacementAndAllowsCredentialRotation(t *testing.T) {
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelConnection: "conn_fixture", owned.LabelSettings: `{"host":"host.example.com","docker_id":"daemon"}`}
	c := &connections.Connection{ID: "conn_fixture", OrgID: "org_fixture", Cloud: connections.Server, Status: connections.StatusReady, Details: map[string]string{"host": "host.example.com", "docker_id": "daemon"}}
	raw := []byte("rotated-key")
	e := &OwnedTarget{Cloud: connections.Server, Credentials: buildCredentialFunc(func(context.Context, string, string) (*connections.Connection, []byte, error) { return c, raw, nil })}
	if _, got, err := e.connection(t.Context(), c.OrgID, c.ID, labels); err != nil || string(got) != "rotated-key" {
		t.Fatalf("rotation refused: %s %v", got, err)
	}
	c.Details["docker_id"] = "other-daemon"
	if _, _, err := e.connection(t.Context(), c.OrgID, c.ID, labels); err == nil {
		t.Fatal("changed destination accepted")
	}
	if _, _, err := e.connection(t.Context(), "org_other", c.ID, labels); err == nil {
		t.Fatal("cross-organization placement accepted")
	}
}

func TestOwnedTarget_observesPinnedRevisionAndDeletesOnlyBindingTarget(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	reference := "registry.example.com/team/api@sha256:" + strings.Repeat("b", 64)
	backend := &ownedFixtureBackend{revision: reference}
	instances := &ownedInstances{value: &instance.Instance{Entity: ctrlplane.Entity{ID: iid}, TenantID: "org_fixture", ProviderName: "server.conn_old", Labels: map[string]string{owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelRuntime: "org_fixture/env_fixture/api/runtime_owned"}}}
	secrets := &memSecrets{m: map[string][]byte{"org_fixture/env_fixture/api/runtime_owned": []byte("private")}}
	e := &OwnedTarget{Cloud: connections.Server, Ctrl: CtrlPlane{Providers: cpprovider.NewRegistry(), Instances: instances}, Secrets: secrets, Backend: func(string, string) owned.Backend { return backend }}
	b := &planning.Binding{OrgID: "org_fixture", ProjectID: "proj_fixture", EnvironmentID: "env_fixture", ConnectionID: "conn_old", Name: "api", ExternalID: iid.String(), Config: json.RawMessage(`{"target":"conn_old"}`)}
	observation, err := e.Observe(t.Context(), &connections.Connection{ID: "conn_old", OrgID: b.OrgID, Cloud: connections.Server}, nil, b)
	if err != nil || !observation.Exists || observation.Revision != reference {
		t.Fatalf("observe: %+v %v", observation, err)
	}
	job := &Job{Deployment: &Deployment{OrgID: b.OrgID}, Checkpoint: func([]byte) error { return nil }}
	foreign := *b
	foreign.ConnectionID = "conn_new"
	if err = e.Remove(t.Context(), job, &foreign); err == nil || instances.value == nil {
		t.Fatal("deleted through a different connection")
	}
	if err = e.Remove(t.Context(), job, b); err != nil || instances.value != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, found := secrets.m["org_fixture/env_fixture/api/runtime_owned"]; found {
		t.Fatal("runtime credential survived cleanup")
	}
}

func TestOwnedTarget_publicHealthRequiresSuccessfulDeclaredPath(t *testing.T) {
	paths := []string{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer api.Close()
	runner := &Runner{http: api.Client(), now: time.Now, verifyWait: -time.Second}
	if err := runner.verifyService(t.Context(), api.URL, planning.Action{Provider: "server", Service: &planning.ServiceConfig{HealthPath: "/_/health"}}); err == nil {
		t.Fatal("unauthorized health accepted")
	}
	if len(paths) != 1 || paths[0] != "/_/health" {
		t.Fatalf("wrong health path: %v", paths)
	}
	if err := runner.verifyService(t.Context(), api.URL, planning.Action{Provider: "vercel", Service: &planning.ServiceConfig{}}); err != nil {
		t.Fatal("managed protected deployment verification changed")
	}
}

func TestOwnedTarget_forgeWorkerUsesProcessReadiness(t *testing.T) {
	job := &Job{Deployment: &Deployment{OrgID: "org_fixture", ProjectID: "proj_fixture", EnvironmentID: "env_fixture"}, Connection: &connections.Connection{ID: "conn_fixture"}, Action: planning.Action{Name: "worker", Service: &planning.ServiceConfig{Workload: "worker", Framework: "forge", HealthPath: "/_/health"}}}
	spec, labels := ownedSpec(job, "image", ownedState{})
	if len(spec.Ports) != 0 || labels[owned.LabelHealth] != "" {
		t.Fatalf("worker HTTP readiness: %+v %+v", spec, labels)
	}
}

type cleanupSecrets struct {
	*memSecrets
	fail bool
}

func (s *cleanupSecrets) Delete(ctx context.Context, key string) error {
	if s.fail {
		s.fail = false
		return errors.New("vault temporarily unavailable")
	}
	return s.memSecrets.Delete(ctx, key)
}
func TestOwnedTarget_retriesVaultCleanupAfterInstanceDisappears(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	ref := "org_fixture/env_fixture/api/runtime_owned"
	instances := &ownedInstances{value: &instance.Instance{Entity: ctrlplane.Entity{ID: iid}, TenantID: "org_fixture", ProviderName: "server.conn_old", Labels: map[string]string{owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelRuntime: ref}}}
	secrets := &cleanupSecrets{memSecrets: &memSecrets{m: map[string][]byte{ref: []byte("private-value")}}, fail: true}
	e := &OwnedTarget{Cloud: connections.Server, Ctrl: CtrlPlane{Providers: cpprovider.NewRegistry(), Instances: instances}, Secrets: secrets, Backend: func(string, string) owned.Backend { return &ownedFixtureBackend{} }}
	b := &planning.Binding{OrgID: "org_fixture", ProjectID: "proj_fixture", EnvironmentID: "env_fixture", ConnectionID: "conn_old", Name: "api", ExternalID: iid.String()}
	job := &Job{Deployment: &Deployment{OrgID: b.OrgID}, Checkpoint: func([]byte) error { return nil }}
	if err := e.Remove(t.Context(), job, b); err == nil || instances.value != nil {
		t.Fatalf("expected post-instance vault failure: %v", err)
	}
	if !strings.Contains(string(job.Resume), ref) || strings.Contains(string(job.Resume), "private-value") {
		t.Fatal("durable cleanup reference missing or contains value")
	}
	if err := e.Remove(t.Context(), job, b); err != nil {
		t.Fatal(err)
	}
	if _, exists := secrets.m[ref]; exists {
		t.Fatal("vault cleanup was not retried")
	}
}

func TestOwnedTarget_refusesCrossTenantRuntimeCleanupBeforeMutation(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	foreign := "org_other/env_fixture/api/runtime_owned"
	instances := &ownedInstances{value: &instance.Instance{Entity: ctrlplane.Entity{ID: iid}, TenantID: "org_fixture", ProviderName: "server.conn_old", Labels: map[string]string{owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelRuntime: foreign}}}
	secrets := &memSecrets{m: map[string][]byte{foreign: []byte("other-tenant-secret")}}
	e := &OwnedTarget{Cloud: connections.Server, Ctrl: CtrlPlane{Providers: cpprovider.NewRegistry(), Instances: instances}, Secrets: secrets, Backend: func(string, string) owned.Backend { return &ownedFixtureBackend{} }}
	b := &planning.Binding{OrgID: "org_fixture", ProjectID: "proj_fixture", EnvironmentID: "env_fixture", Name: "api", ConnectionID: "conn_old", ExternalID: iid.String()}
	job := &Job{Deployment: &Deployment{OrgID: b.OrgID}, Checkpoint: func([]byte) error { return nil }}
	if err := e.Remove(t.Context(), job, b); err == nil || instances.value == nil {
		t.Fatalf("foreign ref mutated instance: %v", err)
	}
	if _, exists := secrets.m[foreign]; !exists {
		t.Fatal("other tenant's secret was deleted")
	}
	instances.value = nil
	resume, _ := json.Marshal(ownedState{Removals: map[string]string{b.ExternalID: foreign}})
	job.Resume = resume
	if err := e.Remove(t.Context(), job, b); err == nil {
		t.Fatal("foreign cleanup checkpoint accepted")
	}
}

func TestOwnedTarget_publicWithoutDeclaredHealthStillVerifies(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer api.Close()
	r := &Runner{http: api.Client(), now: time.Now, verifyWait: -time.Second}
	if err := r.verifyService(t.Context(), api.URL, planning.Action{Provider: "server", Service: &planning.ServiceConfig{Public: true}}); err == nil {
		t.Fatal("unhealthy public endpoint accepted")
	}
}

func TestOwnedTarget_resumesCommittedWorkloadWithoutRedeploy(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	image := "registry.example.com/team/api@sha256:" + strings.Repeat("a", 64)
	ref := "org_fixture/env_fixture/api/runtime_new"
	instances := &ownedInstances{value: &instance.Instance{Entity: ctrlplane.Entity{ID: iid}, TenantID: "org_fixture", ProviderName: "server.conn_fixture", Services: []cpprovider.ServiceSpec{{Name: mainService, Image: image}}, Labels: map[string]string{owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelRuntime: ref}}}
	backend := &ownedFixtureBackend{revision: image}
	e := &OwnedTarget{Cloud: connections.Server, Ctrl: CtrlPlane{Providers: cpprovider.NewRegistry(), Instances: instances, Deploys: &ownedDeploys{fail: true, after: func() { t.Error("committed rollout was redeployed") }}}, Secrets: &memSecrets{m: map[string][]byte{ref: []byte(`{"TOKEN":"current"}`)}}, Backend: func(string, string) owned.Backend { return backend }}
	state := ownedState{Image: ImageArtifact{Reference: image}, RuntimeRef: ref, Committed: true, PriorInstance: iid.String(), PriorLabels: map[string]string{owned.LabelRuntime: "org_fixture/env_fixture/api/runtime_old"}}
	resume, _ := json.Marshal(state)
	job := &Job{Deployment: &Deployment{OrgID: "org_fixture", ProjectID: "proj_fixture", EnvironmentID: "env_fixture"}, Action: planning.Action{Name: "api", ConnectionID: "conn_fixture", Service: &planning.ServiceConfig{Target: "conn_fixture"}}, Connection: &connections.Connection{ID: "conn_fixture"}, Binding: &planning.Binding{ExternalID: iid.String()}, Resume: resume}
	outcome, err := e.Apply(t.Context(), job)
	if err != nil || outcome.Revision != image || instances.updates != 0 {
		t.Fatalf("resume: %+v %v", outcome, err)
	}
}
