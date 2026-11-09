package deployments

import (
	"context"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type bucketAccessResolver struct {
	record   *providerresources.ProviderResource
	endpoint *catalogconnect.Endpoint
	raw      string
}

func (b bucketAccessResolver) Verified(context.Context, security.Principal, string) (*providerresources.ProviderResource, error) {
	return b.record, nil
}
func (b bucketAccessResolver) Resolve(context.Context, security.Principal, string, int64) (*providerresources.ProviderResource, *catalogconnect.Endpoint, string, error) {
	return b.record, b.endpoint, b.raw, nil
}

func TestExistingBucketAttachStoresSDKOutputsAndDetachOnlyRemovesLocalAccess(t *testing.T) {
	const id = "providerresource_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	account := connections.AccountIdentity(connections.AWS, "123456789012", map[string]string{"region": "account-inventory"})
	pin := &planning.ExistingResourceConfig{ResourceID: id, EndpointID: "endpoint", Revision: 2, EndpointRevision: 1, AccountIdentity: account, ExternalID: "arn:aws:s3:::assets-bucket"}
	cfg := &planning.ResourceConfig{Provider: "s3", Kind: "object-storage", ExistingResource: pin}
	action := planning.Action{ID: "resource:assets", Name: "assets", Subject: planning.ResourceSubject, Provider: "s3", ConnectionID: "conn", Op: planning.Create, Engine: provider.ResourceEngine, Resource: cfg}
	plan := &planning.Plan{ID: "plan", OrgID: "org", ProjectID: "project", EnvironmentID: "env", CreatedBy: "admin", Actions: []planning.Action{action}}
	plans := &fakePlans{plan: plan, bindings: map[string]*planning.Binding{}}
	secrets := snapshotSecrets{}
	access := catalogconnect.BucketAccess{Provider: "aws", Bucket: "assets-bucket", Region: "us-east-1", ExpectedOwner: "123456789012", AccessKeyID: "AKIAWORKLOADKEY123456", SecretAccessKey: "EXPLICIT-WORKLOAD-SECRET"}
	raw, err := catalogconnect.EncodeBucketAccess(access)
	if err != nil {
		t.Fatal(err)
	}
	record := &providerresources.ProviderResource{ID: id, OrgID: "org", Kind: "object-storage", Provider: "aws", Name: "assets-bucket", Region: "us-east-1", Scope: "account/123456789012/regions/us-east-1/managed-resources", AccountIdentity: account, ExternalID: pin.ExternalID, ConnectionID: "conn", EndpointID: "endpoint", EndpointRevision: 1, Revision: 2}
	resolver := bucketAccessResolver{record: record, endpoint: &catalogconnect.Endpoint{ID: "endpoint", Revision: 1, Engine: catalogconnect.EngineS3}, raw: raw}
	runner := NewRunner(RunnerDeps{Plans: plans, Resources: secrets, Variables: fakeVariables{}, Authz: existingAuth{}, Providers: providers.Registry(), Executors: map[string]Executor{"s3": forbiddenNativeResource{t}}, ExistingResources: resolver})
	deployment := &Deployment{ID: "deployment", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"}
	if err = runner.run(t.Context(), deployment, plan, action, &Step{}, nil, func([]byte) error { return nil }, func(string, string) {}, nil); err != nil {
		t.Fatal(err)
	}
	if string(secrets[ResourceURLKey("org", "env", "assets")]) != "s3://assets-bucket?region=us-east-1" {
		t.Fatal("bucket URL contains the raw access contract instead of workload address")
	}
	for key, want := range mustBucketOutputs(t, raw) {
		field := "output:" + key
		if key == "STORAGE_URL" {
			field = ResourceURL
		}
		if string(secrets[ResourceKey("org", "env", "assets", field)]) != want {
			t.Fatalf("missing output %s", key)
		}
	}
	bound := plans.bindings[action.ID]
	if bound == nil || !planning.ExistingResourceBinding(bound) {
		t.Fatal("non-owning attachment missing")
	}
	// Every generated field is captured once for retries and later rollbacks.
	service := planning.Action{ID: "service:api", Name: "api", Service: &planning.ServiceConfig{Service: "svc"}}
	for _, key := range catalogconnect.BucketEnvironment(catalogconnect.EngineS3) {
		field := "output:" + key
		if key == "STORAGE_URL" {
			field = ""
		}
		service.Service.Env = append(service.Service.Env, planning.EnvLink{Key: key, From: "assets", Subject: planning.ResourceSubject, Field: field})
	}
	resources := map[string]*planning.Binding{"assets": bound}
	if err = runner.captureAppliedVariables(t.Context(), deployment, &service, nil, resources); err != nil {
		t.Fatal(err)
	}
	snapshot := service.Service.AppliedInputs.Variables
	secrets[ResourceKey("org", "env", "assets", "output:AWS_SECRET_ACCESS_KEY")] = []byte("replacement-credential")
	env, masked, err := runner.env(t.Context(), deployment, service, nil, resources)
	if err != nil || env["AWS_SECRET_ACCESS_KEY"] != access.SecretAccessKey || !masked["AWS_SECRET_ACCESS_KEY"] {
		t.Fatalf("retry/rollback snapshot lost approved credential: %v", err)
	}
	if strings.Contains(env["STORAGE_URL"], access.SecretAccessKey) {
		t.Fatal("secret embedded in bucket URL")
	}
	// Saved static-site plans are refused before resource credentials are rewritten.
	static := service
	static.Service = &planning.ServiceConfig{Workload: provider.StaticSite, Execution: provider.Static, Env: service.Service.Env}
	plan.Actions = append(plan.Actions, static)
	action.Op = planning.Keep
	err = runner.run(t.Context(), deployment, plan, action, &Step{}, nil, func([]byte) error { return nil }, func(string, string) {}, nil)
	if err == nil || !strings.Contains(err.Error(), "static") || string(secrets[ResourceKey("org", "env", "assets", "output:AWS_SECRET_ACCESS_KEY")]) != "replacement-credential" {
		t.Fatalf("saved static plan reached secret delivery: %v", err)
	}
	err = runner.verifyExistingServiceAccess(t.Context(), &Job{Deployment: deployment, Plan: plan, Action: static}, resources)
	if err == nil || !strings.Contains(err.Error(), "static") {
		t.Fatalf("kept static consumer bypassed runtime guard: %v", err)
	}
	delete(secrets, snapshot)
	runner.executors = nil
	runner.existingResources = nil
	action.Op = planning.Delete
	if err = runner.run(t.Context(), deployment, plan, action, &Step{}, nil, func([]byte) error { return nil }, func(string, string) {}, nil); err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 0 || plans.bindings[action.ID] != nil {
		t.Fatalf("local credentials survived detach: %d", len(secrets))
	}
}
func mustBucketOutputs(t *testing.T, raw string) map[string]string {
	t.Helper()
	out, err := catalogconnect.BucketOutputs(raw)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestExternalBucketReapDeletesOnlyLocalSDKOutputs(t *testing.T) {
	secrets := snapshotSecrets{}
	for _, field := range append([]string{ResourceURL}, bucketCredentialFields()...) {
		secrets[ResourceKey("org", "env", "assets", field)] = []byte("workload-value")
	}
	var warnings []string
	reaper := &BindingReaper{Secrets: secrets, Warn: func(message string, _ error) { warnings = append(warnings, message) }, Executors: map[string]Executor{"s3": forbiddenNativeResource{t}}}
	reaper.finish(t.Context(), reapedEnvironment{org: "org", id: "env", name: "production", bindings: []*planning.Binding{{Name: "assets", Subject: planning.ResourceSubject, Provider: "s3", Config: []byte(`{"existing_resource":{"resource_id":"bucket"}}`)}}})
	if len(secrets) != 0 || len(warnings) != 0 {
		t.Fatalf("external bucket left credentials or a misleading cloud-deletion instruction: %d %v", len(secrets), warnings)
	}
}
