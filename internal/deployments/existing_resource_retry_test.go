package deployments

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

type retryResourceResolver struct {
	existingAccess
	checks   int
	raw      string
	resolves int
}

func (r *retryResourceResolver) VerifyProject(context.Context, security.Principal, string, string, string, int64) (*providerresources.ProviderResource, *catalogconnect.Endpoint, error) {
	r.checks++
	return r.record, r.endpoint, nil
}
func (r *retryResourceResolver) ResolveProject(context.Context, security.Principal, string, string, string, int64) (*providerresources.ProviderResource, *catalogconnect.Endpoint, string, error) {
	r.resolves++
	if r.err != nil {
		return nil, nil, "", r.err
	}
	raw := r.raw
	if raw == "" {
		raw = "current-credential"
	}
	return r.record, r.endpoint, raw, nil
}

type retryResourceChecker struct {
	planning.EndpointChecker
	checks int
}

func (r *retryResourceChecker) Check(context.Context, string, string, recipes.Requirement, catalogconnect.DialFunc) (*catalogconnect.Report, error) {
	r.checks++
	return &catalogconnect.Report{Version: "17"}, nil
}

type retryResourceDialer struct{}

func (retryResourceDialer) TargetDial(context.Context, string, string) (catalogconnect.DialFunc, io.Closer, error) {
	return func(context.Context, string, string) (net.Conn, error) { return nil, nil }, nil, nil
}

func TestExistingServiceRetryRequiresItsApprovedResourcePin(t *testing.T) {
	for _, scenario := range []string{"unchanged", "rotated-binding", "missing-binding", "owned-binding", "missing-plan-resource", "kind-changed", "provider-changed", "account-changed"} {
		t.Run(scenario, func(t *testing.T) {
			pin := &planning.ExistingResourceConfig{ResourceID: "inventory", Revision: 1, EndpointID: "endpoint-p1", EndpointRevision: 1, AccountIdentity: "account", ExternalID: "native-db", Incarnation: "physical-1"}
			approved := planning.ResourceConfig{Provider: "digitalocean", Kind: "postgres", AccountIdentity: "account", ExistingResource: pin}
			current := approved
			currentPin := *pin
			current.ExistingResource = &currentPin
			resource := planning.Action{ID: "resource:db", Name: "db", Subject: planning.ResourceSubject, Provider: "digitalocean", ConnectionID: "provider-connection", Resource: &approved}
			plan := &planning.Plan{CreatedBy: "owner", Actions: []planning.Action{resource}}
			service := planning.Action{Name: "api", ConnectionID: "runtime-target", Service: &planning.ServiceConfig{Env: []planning.EnvLink{{Key: "DATABASE_URL", From: "db", Subject: planning.ResourceSubject}}, AppliedInputs: &planning.AppliedInputs{Variables: "snapshot-p1"}}}
			switch scenario {
			case "rotated-binding":
				currentPin.Revision = 2
				currentPin.EndpointID = "endpoint-p2"
				currentPin.EndpointRevision = 2
			case "owned-binding":
				current.ExistingResource = nil
			case "missing-plan-resource":
				plan.Actions = nil
			case "kind-changed":
				current.Kind = "mysql"
			case "provider-changed":
				current.Provider = "azure-postgres"
			case "account-changed":
				current.AccountIdentity = "another-account"
			}
			raw, err := json.Marshal(current)
			if err != nil {
				t.Fatal(err)
			}
			binding := &planning.Binding{Name: "db", Subject: planning.ResourceSubject, Provider: "digitalocean", ConnectionID: "provider-connection", Config: raw}
			bindings := map[string]*planning.Binding{"db": binding}
			if scenario == "missing-binding" {
				delete(bindings, "db")
			}
			resolver := &retryResourceResolver{existingAccess: existingAccess{record: &providerresources.ProviderResource{ID: "inventory", Kind: "postgres", AccountIdentity: "account", ExternalID: "native-db", Incarnation: "physical-1", Revision: currentPin.Revision, EndpointID: currentPin.EndpointID, EndpointRevision: currentPin.EndpointRevision}, endpoint: &catalogconnect.Endpoint{ID: currentPin.EndpointID, Revision: currentPin.EndpointRevision, Engine: "postgres"}}}
			checker := &retryResourceChecker{}
			secrets := snapshotSecrets{"snapshot-p1": []byte(`{"resource_values":{"DATABASE_URL":"credential-p1"}}`)}
			runner := NewRunner(RunnerDeps{Resources: secrets, ExistingResources: resolver, ExistingEndpoints: checker, ExistingDialer: retryResourceDialer{}})
			err = runner.verifyExistingServiceAccess(t.Context(), &Job{Deployment: &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, Plan: plan, Action: service}, bindings)
			if scenario == "unchanged" {
				if err != nil || resolver.checks != 1 || checker.checks != 1 {
					t.Fatalf("unchanged approved access blocked: %v", err)
				}
				return
			}
			if err == nil || resolver.checks != 0 || checker.checks != 0 {
				t.Fatalf("old approved snapshot bypassed %s before grant checks: err=%v grants=%d probes=%d", scenario, err, resolver.checks, checker.checks)
			}
			if string(secrets["snapshot-p1"]) != `{"resource_values":{"DATABASE_URL":"credential-p1"}}` {
				t.Fatal("refusal changed the approved snapshot")
			}
		})
	}
}

func TestExistingAzureCanaryRefusesChangedExternalAccess(t *testing.T) {
	pin := &planning.ExistingResourceConfig{ResourceID: "inventory", Revision: 1, EndpointID: "endpoint", EndpointRevision: 1, AccountIdentity: "account", ExternalID: "native", Incarnation: "physical"}
	cfg := planning.ResourceConfig{Provider: "digitalocean", Kind: "postgres", AccountIdentity: "account", ExistingResource: pin}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []planning.Op{planning.Keep, planning.Create, planning.Update} {
		for _, ramps := range []bool{false, true} {
			t.Run(string(op)+"/"+map[bool]string{false: "direct-worker", true: "ramping-service"}[ramps], func(t *testing.T) {
				resource := planning.Action{ID: "resource:db", Name: "db", Subject: planning.ResourceSubject, Provider: "digitalocean", ConnectionID: "resource-connection", Resource: &cfg, Op: op}
				service := planning.Action{ID: "service:api", Name: "api", Provider: "azure-container-apps", ConnectionID: "runtime", Service: &planning.ServiceConfig{Env: []planning.EnvLink{{Key: "DATABASE_URL", From: "db", Subject: planning.ResourceSubject}}}}
				release := &planning.Release{Strategy: "canary"}
				if ramps {
					release.Canary = []string{service.ID}
				} else {
					service.Service.Workload = "worker"
				}
				job := &Job{Deployment: &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, Plan: &planning.Plan{CreatedBy: "user", Actions: []planning.Action{resource}, Release: release}, Action: service}
				resolver := &retryResourceResolver{existingAccess: existingAccess{record: &providerresources.ProviderResource{ID: "inventory", Kind: "postgres", AccountIdentity: "account", ExternalID: "native", Incarnation: "physical", EndpointID: "endpoint", EndpointRevision: 1, Revision: 1}, endpoint: &catalogconnect.Endpoint{ID: "endpoint", Revision: 1, Engine: "postgres"}}}
				runner := NewRunner(RunnerDeps{ExistingResources: resolver, ExistingEndpoints: &retryResourceChecker{}, ExistingDialer: retryResourceDialer{}})
				err = runner.verifyExistingServiceAccess(t.Context(), job, map[string]*planning.Binding{"db": {Name: "db", Subject: planning.ResourceSubject, Provider: "digitalocean", ConnectionID: "resource-connection", Config: raw}})
				if op == planning.Keep || !ramps {
					if err != nil {
						t.Fatalf("code-only canary/direct worker blocked: %v", err)
					}
				} else if err == nil || resolver.checks != 0 {
					t.Fatalf("changed external credentials entered native canary revert path: %v", err)
				}
			})
		}
	}
}
