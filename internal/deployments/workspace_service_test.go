package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/provider"
)

const (
	workspaceServiceID  = "endpoint_01jd8x5k2m3n4p5q6r7s8t9v0w"
	workspaceServiceDSN = "postgres://app:private-password@db.example:5432/orders?sslmode=require"
)

// workspaceResolver is the endpoint service as a deploy reads it.
type workspaceResolver struct {
	endpoint *catalogconnect.Endpoint
	err      error
}

func (w workspaceResolver) Resolve(_ context.Context, orgID, id string) (*catalogconnect.Endpoint, string, error) {
	if w.err != nil {
		return nil, "", w.err
	}
	if orgID != "org" || id != workspaceServiceID {
		return nil, "", catalogconnect.ErrNotFound
	}
	e := *w.endpoint
	return &e, workspaceServiceDSN, nil
}

// reachChecks records each check of an endpoint from a target.
type reachChecks struct {
	tls    [][]string
	failed bool
}

func (c *reachChecks) Endpoint(context.Context, string, string) (*catalogconnect.Endpoint, error) {
	return nil, errors.New("unused")
}

func (c *reachChecks) Check(_ context.Context, _, _ string, req recipes.Requirement, _ catalogconnect.DialFunc) (*catalogconnect.Report, error) {
	c.tls = append(c.tls, req.TLS)
	if c.failed {
		return &catalogconnect.Report{Problems: []string{"no answer"}}, nil
	}
	return &catalogconnect.Report{Version: "16.4"}, nil
}

type anyTarget struct{}

func (anyTarget) TargetDial(context.Context, string, string) (catalogconnect.DialFunc, io.Closer, error) {
	return func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unused") }, nil, nil
}

func (anyTarget) TargetCloud(context.Context, string, string) (string, error) { return "server", nil }

// workspaceAttachment is the attachment a plan pinned at revision 2.
func workspaceAttachment() planning.Action {
	pin := &planning.ExistingResourceConfig{EndpointID: workspaceServiceID, EndpointRevision: 2}
	return planning.Action{
		ID: "resource:orders", Name: "orders", Subject: planning.ResourceSubject, Provider: planning.WorkspaceProvider,
		Op: planning.Create, Engine: provider.ResourceEngine,
		Resource: &planning.ResourceConfig{Provider: planning.WorkspaceProvider, Kind: "postgres", ExistingResource: pin},
	}
}

func registeredOrders() *catalogconnect.Endpoint {
	return &catalogconnect.Endpoint{ID: workspaceServiceID, Name: "orders-db", Engine: "postgres", Revision: 2, TLS: catalogconnect.TLSRequire, Origin: catalogconnect.OriginRegistered}
}

func TestAWorkspaceServiceIsConnectedAndDisconnectedWithoutAProvider(t *testing.T) {
	a := workspaceAttachment()
	api := planning.Action{ID: "service:api", Name: "api", Subject: planning.ServiceSubject, Provider: "server", Op: planning.Create,
		Service: &planning.ServiceConfig{Target: "conn_box", Env: []planning.EnvLink{{Key: "DATABASE_URL", From: "orders", Subject: planning.ResourceSubject}}}}
	p := &planning.Plan{ID: "plan", OrgID: "org", ProjectID: "project", EnvironmentID: "env", CreatedBy: "dev", Actions: []planning.Action{a, api}}
	plans := &fakePlans{plan: p, bindings: map[string]*planning.Binding{}}
	secrets := &memSecrets{m: map[string][]byte{}}
	checks := &reachChecks{}
	r := NewRunner(RunnerDeps{
		Plans: plans, Resources: secrets, Authz: existingAuth{}, Providers: providers.Registry(),
		Executors:         map[string]Executor{planning.WorkspaceProvider: forbiddenNativeResource{t}},
		ExistingEndpoints: checks, ExistingDialer: anyTarget{},
		WorkspaceEndpoints: workspaceResolver{endpoint: registeredOrders()},
	})
	d := &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", PlanID: "plan"}
	step := &Step{}
	if err := r.run(t.Context(), d, p, a, step, nil, func([]byte) error { return nil }, func(string, string) {}, nil); err != nil {
		t.Fatal(err)
	}
	bound := plans.bindings["resource:orders"]
	if bound == nil || !planning.ExistingResourceBinding(bound) || bound.ExternalID != workspaceServiceID || bound.ConnectionID != "" || step.Revision != "endpoint:2" {
		t.Fatalf("binding: %+v, revision %q", bound, step.Revision)
	}
	if string(secrets.m[ResourceURLKey("org", "env", "orders")]) != workspaceServiceDSN {
		t.Fatal("the connection string wasn't kept as the environment's copy")
	}
	// The service that gets it was checked from its target, over TLS since
	// the workspace service uses it.
	if len(checks.tls) != 1 || len(checks.tls[0]) != 2 {
		t.Fatalf("consumer checks: %v", checks.tls)
	}

	a.Op = planning.Delete
	r.workspaceEndpoints = workspaceResolver{err: errors.New("the workspace service is gone")}
	if err := r.run(t.Context(), d, p, a, step, nil, func([]byte) error { return nil }, func(string, string) {}, nil); err != nil {
		t.Fatal(err)
	}
	if plans.bindings["resource:orders"] != nil || len(secrets.m) != 0 {
		t.Fatal("disconnecting left the binding or the copy behind")
	}
}

func TestAWorkspaceServiceIsntConnectedWhenItChangedSinceThePlan(t *testing.T) {
	for name, tc := range map[string]struct {
		endpoint func(*catalogconnect.Endpoint)
		err      error
		want     string
	}{
		"credentials replaced": {func(e *catalogconnect.Endpoint) { e.Revision = 3 }, nil, "replaced since this plan"},
		"no longer shared":     {func(e *catalogconnect.Endpoint) { e.Restricted, e.Projects = true, []string{"other"} }, nil, "isn't shared with this project"},
		"removed":              {nil, catalogconnect.ErrNotFound, "was removed"},
		"a provider's":         {func(e *catalogconnect.Endpoint) { e.Origin = catalogconnect.OriginProvider }, nil, "belongs to a provider resource"},
	} {
		endpoint := registeredOrders()
		if tc.endpoint != nil {
			tc.endpoint(endpoint)
		}
		a := workspaceAttachment()
		p := &planning.Plan{OrgID: "org", ProjectID: "project", CreatedBy: "dev", Actions: []planning.Action{a}}
		plans := &fakePlans{bindings: map[string]*planning.Binding{}}
		secrets := &memSecrets{m: map[string][]byte{}}
		r := NewRunner(RunnerDeps{Plans: plans, Resources: secrets, Authz: existingAuth{}, Providers: providers.Registry(),
			WorkspaceEndpoints: workspaceResolver{endpoint: endpoint, err: tc.err}})
		err := r.run(t.Context(), &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, p, a, &Step{}, nil, func([]byte) error { return nil }, func(string, string) {}, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) || len(plans.bindings) != 0 || len(secrets.m) != 0 {
			t.Errorf("%s: %v, bindings %d, secrets %d", name, err, len(plans.bindings), len(secrets.m))
		}
	}
}

func TestAServiceReleaseRechecksTheWorkspaceServiceItUses(t *testing.T) {
	a := workspaceAttachment()
	a.Op = planning.Keep
	raw, err := json.Marshal(a.Resource)
	if err != nil {
		t.Fatal(err)
	}
	binding := &planning.Binding{Name: "orders", Subject: planning.ResourceSubject, Provider: planning.WorkspaceProvider, Config: raw}
	job := func() *Job {
		return &Job{
			Deployment: &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "env"},
			Plan:       &planning.Plan{CreatedBy: "dev", Actions: []planning.Action{a}},
			Action: planning.Action{Name: "api", Provider: "server", Service: &planning.ServiceConfig{Target: "conn_box",
				Env: []planning.EnvLink{{Key: "DATABASE_URL", From: "orders", Subject: planning.ResourceSubject}}}},
		}
	}
	plaintext := registeredOrders()
	plaintext.TLS = catalogconnect.TLSDisable
	checks := &reachChecks{}
	r := NewRunner(RunnerDeps{Authz: existingAuth{}, Providers: providers.Registry(), ExistingEndpoints: checks, ExistingDialer: anyTarget{},
		WorkspaceEndpoints: workspaceResolver{endpoint: plaintext}})
	if err := r.verifyExistingServiceAccess(t.Context(), job(), map[string]*planning.Binding{"orders": binding}); err != nil {
		t.Fatal(err)
	}
	// Without TLS on the service, the check asks for none.
	if len(checks.tls) != 1 || checks.tls[0] != nil {
		t.Fatalf("checks: %v", checks.tls)
	}
	unshared := registeredOrders()
	unshared.Restricted = true
	r.workspaceEndpoints = workspaceResolver{endpoint: unshared}
	if err := r.verifyExistingServiceAccess(t.Context(), job(), map[string]*planning.Binding{"orders": binding}); err == nil || !strings.Contains(err.Error(), "isn't shared") {
		t.Fatalf("an unshared service: %v", err)
	}
	r.workspaceEndpoints = workspaceResolver{endpoint: registeredOrders()}
	checks.failed = true
	if err := r.verifyExistingServiceAccess(t.Context(), job(), map[string]*planning.Binding{"orders": binding}); err == nil {
		t.Fatal("a service that can't reach it was released")
	}
}
