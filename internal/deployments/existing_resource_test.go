package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type forbiddenNativeResource struct{ t *testing.T }

func (f forbiddenNativeResource) Apply(context.Context, *Job) (*Outcome, error) {
	f.t.Fatal("external attachment called native provisioning")
	return nil, nil
}
func (f forbiddenNativeResource) Remove(context.Context, *Job, *planning.Binding) error {
	f.t.Fatal("external attachment called native cloud deletion")
	return nil
}
func (f forbiddenNativeResource) Observe(context.Context, *connections.Connection, []byte, *planning.Binding) (*Observation, error) {
	f.t.Fatal("external attachment called native lifecycle observation")
	return nil, nil
}

type existingAccess struct {
	planning.ExistingResourceResolver
	record   *providerresources.ProviderResource
	endpoint *catalogconnect.Endpoint
	err      error
}

func (f existingAccess) Resolve(context.Context, security.Principal, string, int64) (*providerresources.ProviderResource, *catalogconnect.Endpoint, string, error) {
	return f.record, f.endpoint, "postgres://app:private-password@db.example:25060/app?sslmode=verify-full", f.err
}

type existingAuth struct{}

func (existingAuth) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

func TestExistingResourceApplyAndDetachNeverUseNativeLifecycle(t *testing.T) {
	const id = "providerresource_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pin := &planning.ExistingResourceConfig{ResourceID: id, EndpointID: "endpoint-1", Revision: 4, EndpointRevision: 1, AccountIdentity: "account", ExternalID: "native-db", Incarnation: "created-at-1"}
	cfg := &planning.ResourceConfig{Provider: "digitalocean", Kind: "postgres", Region: "nyc1", ExistingResource: pin}
	a := planning.Action{ID: "resource:db", Name: "db", Subject: planning.ResourceSubject, Provider: "digitalocean", Region: "nyc1", ConnectionID: "conn-1", Op: planning.Create, Engine: provider.ResourceEngine, Resource: cfg}
	p := &planning.Plan{ID: "plan", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production", CreatedBy: "admin", DecidedBy: "admin", Actions: []planning.Action{a}}
	plans := &fakePlans{plan: p, bindings: map[string]*planning.Binding{}}
	secrets := &memSecrets{m: map[string][]byte{}}
	record := &providerresources.ProviderResource{ID: id, OrgID: "org", Provider: "digitalocean", Kind: "postgres", AccountIdentity: "account", ConnectionID: "conn-1", ExternalID: "native-db", Incarnation: "created-at-1", Revision: 4, EndpointID: "endpoint-1", EndpointRevision: 1}
	r := NewRunner(RunnerDeps{Plans: plans, Resources: secrets, Authz: existingAuth{}, Providers: providers.Registry(), Executors: map[string]Executor{"digitalocean": forbiddenNativeResource{t}}, ExistingResources: existingAccess{record: record, endpoint: &catalogconnect.Endpoint{ID: "endpoint-1", Revision: 1, Engine: "postgres"}}})
	d := &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production", PlanID: "plan"}
	step := &Step{}
	if err := r.run(t.Context(), d, p, a, step, nil, func([]byte) error { return nil }, func(string, string) {}, nil); err != nil {
		t.Fatal(err)
	}
	bound := plans.bindings["resource:db"]
	if bound == nil || bound.ExternalID != "native-db" || bound.PhysicalID != "created-at-1" || !planning.ExistingResourceBinding(bound) {
		t.Fatalf("non-owning binding not persisted: %+v", bound)
	}
	if string(secrets.m[ResourceURLKey("org", "env", "db")]) != "postgres://app:private-password@db.example:25060/app?sslmode=verify-full" {
		t.Fatal("application access was not stored in the resource vault")
	}
	r.existingResources = existingAccess{err: errors.New("provider account disconnected")}
	r.executors = nil
	a.Op = planning.Delete
	if err := r.run(t.Context(), d, p, a, step, nil, func([]byte) error { return nil }, func(string, string) {}, nil); err != nil {
		t.Fatal(err)
	}
	if plans.bindings["resource:db"] != nil || len(secrets.m) != 0 {
		t.Fatal("local attachment and credential survived detach")
	}
}

func TestExistingResourceRevisionChangeDoesNotSaveAccess(t *testing.T) {
	pin := &planning.ExistingResourceConfig{ResourceID: "inventory", EndpointID: "endpoint", Revision: 4, EndpointRevision: 1, AccountIdentity: "account", ExternalID: "db", Incarnation: "old"}
	a := planning.Action{ID: "resource:db", Name: "db", Subject: planning.ResourceSubject, Provider: "digitalocean", ConnectionID: "conn", Op: planning.Create, Resource: &planning.ResourceConfig{Kind: "postgres", ExistingResource: pin}}
	p := &planning.Plan{OrgID: "org", ProjectID: "project", CreatedBy: "admin", Actions: []planning.Action{a}}
	plans := &fakePlans{bindings: map[string]*planning.Binding{}}
	secrets := &memSecrets{m: map[string][]byte{}}
	r := NewRunner(RunnerDeps{Plans: plans, Resources: secrets, Authz: existingAuth{}, Providers: providers.Registry(), Executors: map[string]Executor{"digitalocean": forbiddenNativeResource{t}}, ExistingResources: existingAccess{record: &providerresources.ProviderResource{ID: "inventory", Revision: 5}, endpoint: &catalogconnect.Endpoint{ID: "endpoint", Revision: 1}}})
	err := r.run(t.Context(), &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, p, a, &Step{}, nil, func([]byte) error { return nil }, func(string, string) {}, nil)
	if err == nil || len(plans.bindings) != 0 || len(secrets.m) != 0 {
		t.Fatalf("changed identity granted access: %v", err)
	}
}

func TestExistingResourceCannotAcquirePendingNativeCleanup(t *testing.T) {
	r := &Runner{executors: map[string]Executor{"cloud-sql": &CloudSQL{}}}
	a := planning.Action{Provider: "cloud-sql", Op: planning.Create, Resource: &planning.ResourceConfig{ExistingResource: &planning.ExistingResourceConfig{ResourceID: "inventory"}}}
	if r.canResolvePendingAction(a) {
		t.Fatal("external attachment acquired a native failed-create cleanup owner")
	}
}

type revokedExistingProject struct{ existingAccess }

func (revokedExistingProject) VerifyProject(context.Context, security.Principal, string, string, string, int64) (*providerresources.ProviderResource, *catalogconnect.Endpoint, error) {
	return nil, nil, security.ErrForbidden
}
func (revokedExistingProject) ResolveProject(context.Context, security.Principal, string, string, string, int64) (*providerresources.ProviderResource, *catalogconnect.Endpoint, string, error) {
	return nil, nil, "", security.ErrForbidden
}

func TestExistingResourceKeptBindingRechecksProjectGrant(t *testing.T) {
	cfg := &planning.ResourceConfig{ExistingResource: &planning.ExistingResourceConfig{ResourceID: "inventory", EndpointID: "endpoint", Revision: 1, EndpointRevision: 1}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunner(RunnerDeps{ExistingResources: revokedExistingProject{}})
	job := &Job{Deployment: &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, Plan: &planning.Plan{CreatedBy: "developer"}, Action: planning.Action{Name: "api", Service: &planning.ServiceConfig{Env: []planning.EnvLink{{From: "db", Subject: planning.ResourceSubject}}}}}
	if err := r.verifyExistingServiceAccess(t.Context(), job, map[string]*planning.Binding{"db": {Subject: planning.ResourceSubject, Config: raw}}); err == nil {
		t.Fatal("a kept resource bypassed the current project grant")
	}
}
