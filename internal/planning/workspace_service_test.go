package planning

import (
	"context"
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

const workspaceService = "endpoint_01jd8x5k2m3n4p5q6r7s8t9v0w"

// workspaceInput attaches a registered Postgres to a service on a server.
func workspaceInput(t *testing.T) Input {
	t.Helper()
	in := Input{
		ProjectID: "project", Environment: "production", Graph: infrastructureGraph(), Providers: existingInput(t).Providers,
		Connections: infrastructureConnections(t),
		Manifest: parse(t, "version: 1\nresources:\n  orders: {kind: postgres, existing_resource: "+workspaceService+"}\n"+
			"services:\n  api: {source: cmd/api, provider: server, target: "+targetA+", endpoint: {host_port: 8080}, needs: [orders]}\n"),
		ExistingResources: map[string]ExistingResourceCheck{workspaceService: {Endpoint: &catalogconnect.Endpoint{
			ID: workspaceService, Name: "orders-db", Engine: catalogconnect.EnginePostgres, Host: "db.example", Port: 5432,
			TLS: catalogconnect.TLSRequire, Revision: 2, Origin: catalogconnect.OriginRegistered,
		}}},
		ExistingResourceChecks: map[string]EndpointCheck{"api:orders": {ID: workspaceService, Found: true, Report: &catalogconnect.Report{Version: "16.4"}}},
	}
	return in
}

func TestAWorkspaceServiceIsAttachedAndLinked(t *testing.T) {
	in := workspaceInput(t)
	got := Compute(in)
	if !got.Executable {
		t.Fatalf("problems: %+v", got.Problems)
	}
	var resource, api *Action
	for i := range got.Actions {
		switch got.Actions[i].Name {
		case "orders":
			resource = &got.Actions[i]
		case "api":
			api = &got.Actions[i]
		}
	}
	if resource == nil || api == nil {
		t.Fatalf("actions: %+v", got.Actions)
	}
	pin := resource.Resource.ExistingResource
	if resource.Provider != WorkspaceProvider || resource.ConnectionID != "" || pin == nil || pin.EndpointID != workspaceService ||
		pin.EndpointRevision != 2 || pin.ResourceID != "" || resource.Resource.Managed != nil || resource.Resource.Catalog != nil {
		t.Fatalf("attachment: %+v %+v", resource, pin)
	}
	if !slices.ContainsFunc(api.Service.Env, func(l EnvLink) bool {
		return l.Key == "DATABASE_URL" && l.From == "orders" && l.Subject == ResourceSubject
	}) || !slices.Contains(api.DependsOn, resource.ID) {
		t.Fatalf("api isn't linked: %+v %v", api.Service.Env, api.DependsOn)
	}
	if !slices.Contains(codes(got.Warnings), "catalog-endpoint-unmanaged:orders") {
		t.Fatalf("warnings: %v", codes(got.Warnings))
	}

	// Planned again unchanged, the attachment is kept; with its
	// credentials replaced, it's updated to the new revision.
	in.Bindings = boundFrom(t, got)
	next := Compute(in)
	for _, a := range next.Actions {
		if a.Name == "orders" && a.Op != Keep {
			t.Fatalf("unchanged attachment: %s %+v", a.Op, a.Changes)
		}
	}
	in.ExistingResources[workspaceService].Endpoint.Revision = 3
	rotated := Compute(in)
	for _, a := range rotated.Actions {
		if a.Name == "orders" && (a.Op != Update || a.Resource.ExistingResource.EndpointRevision != 3) {
			t.Fatalf("rotated attachment: %s %+v", a.Op, a.Resource.ExistingResource)
		}
	}
}

func TestAWorkspaceServiceIsRefusedWhenItCantBeUsed(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*Input)
		code   string
	}{
		"not shared": {func(in *Input) {
			c := in.ExistingResources[workspaceService]
			c.Unshared = true
			in.ExistingResources[workspaceService] = c
		}, "workspace-service-unshared:orders"},
		"another engine": {func(in *Input) {
			in.ExistingResources[workspaceService].Endpoint.Engine = catalogconnect.EngineRedis
		}, "workspace-service-kind:orders"},
		"gone": {func(in *Input) {
			in.ExistingResources[workspaceService] = ExistingResourceCheck{Error: "This workspace service isn't in the workspace any more."}
		}, "workspace-service-unavailable:orders"},
		"a provider's endpoint": {func(in *Input) {
			in.ExistingResources[workspaceService].Endpoint.Origin = catalogconnect.OriginProvider
		}, "workspace-service-unavailable:orders"},
		"unreachable from the service's target": {func(in *Input) {
			in.ExistingResourceChecks["api:orders"] = EndpointCheck{ID: workspaceService, Found: true, Report: &catalogconnect.Report{Problems: []string{"db.example:5432 isn't reachable from the target"}}}
		}, "existing-resource-network:api"},
	} {
		in := workspaceInput(t)
		tc.change(&in)
		got := Compute(in)
		if got.Executable || !slices.Contains(codes(got.Problems), tc.code) {
			t.Errorf("%s: problems %v", name, codes(got.Problems))
		}
	}
}

func TestAWorkspaceServiceWithoutTLSIsWarnedOf(t *testing.T) {
	in := workspaceInput(t)
	in.ExistingResources[workspaceService].Endpoint.TLS = catalogconnect.TLSDisable
	got := Compute(in)
	if !got.Executable || !slices.Contains(codes(got.Warnings), "workspace-service-plaintext:orders") {
		t.Fatalf("problems %v, warnings %v", codes(got.Problems), codes(got.Warnings))
	}
}

func TestKindsAWorkspaceServiceStandsInFor(t *testing.T) {
	for kind, engine := range map[string]string{"postgres": "postgres", "mariadb": "mysql", "valkey": "redis", "mongodb": "mongodb", "object-storage": ""} {
		if got := catalogconnect.EngineForKind(kind); got != engine {
			t.Errorf("%s: %q", kind, got)
		}
	}
	if !catalogconnect.IsEndpointID(workspaceService) || catalogconnect.IsEndpointID(syncedResource) {
		t.Fatal("endpoint IDs")
	}
}

// workspaceEndpoints knows one registered service and refuses to check it,
// so a test sees whether anything tried.
type workspaceEndpoints struct {
	endpoint *catalogconnect.Endpoint
	checked  int
}

func (w *workspaceEndpoints) Endpoint(_ context.Context, orgID, id string) (*catalogconnect.Endpoint, error) {
	if w.endpoint == nil || orgID != "org" || id != w.endpoint.ID {
		return nil, catalogconnect.ErrNotFound
	}
	e := *w.endpoint
	return &e, nil
}

func (w *workspaceEndpoints) Check(context.Context, string, string, recipes.Requirement, catalogconnect.DialFunc) (*catalogconnect.Report, error) {
	w.checked++
	return &catalogconnect.Report{Version: "16.4"}, nil
}

// deployers may deploy the project named "project" and nothing else.
type deployers struct{}

func (deployers) Require(_ context.Context, _ security.Principal, perm security.Permission, resource string) error {
	if perm == security.DeploymentCreate && resource == "project" {
		return nil
	}
	return security.ErrForbidden
}

func TestCheckWorkspaceServiceReadsSharingWithoutSecretRights(t *testing.T) {
	registered := &catalogconnect.Endpoint{ID: workspaceService, Name: "orders-db", Engine: "postgres", Revision: 2, Origin: catalogconnect.OriginRegistered}
	endpoints := &workspaceEndpoints{endpoint: registered}
	s := &Service{endpoints: endpoints, authz: deployers{}}
	plan := &Plan{OrgID: "org", ProjectID: "project"}
	p := security.Principal{OrgID: "org", UserID: "developer"}

	// Someone who may only deploy the project can use a shared service.
	if got := s.checkWorkspaceService(t.Context(), plan, p, workspaceService); got.Error != "" || got.Unshared || got.Endpoint == nil || !got.workspaceService() {
		t.Fatalf("shared: %+v", got)
	}
	registered.Restricted, registered.Projects = true, []string{"another"}
	if got := s.checkWorkspaceService(t.Context(), plan, p, workspaceService); !got.Unshared {
		t.Fatalf("restricted to another project: %+v", got)
	}
	registered.Projects = []string{"project"}
	if got := s.checkWorkspaceService(t.Context(), plan, p, workspaceService); got.Unshared {
		t.Fatalf("restricted to this one: %+v", got)
	}
	registered.Origin = catalogconnect.OriginProvider
	if got := s.checkWorkspaceService(t.Context(), plan, p, workspaceService); got.Error == "" {
		t.Fatal("a provider's endpoint was taken as a workspace service")
	}
	if got := s.checkWorkspaceService(t.Context(), plan, p, "endpoint_00000000000000000000000000"); got.Error == "" {
		t.Fatal("a missing service was found")
	}
	if got := s.checkWorkspaceService(t.Context(), &Plan{OrgID: "org", ProjectID: "elsewhere"}, p, workspaceService); got.Error == "" {
		t.Fatal("a project the person can't deploy was planned")
	}
	if endpoints.checked != 0 {
		t.Fatal("reading a service dialed it")
	}
}

// Attached in production alone, a workspace service isn't there in
// staging: the service that needs it deploys without it, with a warning.
func TestAWorkspaceServiceInOneEnvironmentIsLeftOutOfTheOthers(t *testing.T) {
	in := workspaceInput(t)
	in.Manifest = parse(t, "version: 1\nresources:\n  orders: {kind: postgres, existing_resource: "+workspaceService+", environments: {production: {enabled: true}}}\n"+
		"services:\n  api: {source: cmd/api, provider: server, target: "+targetA+", endpoint: {host_port: 8080}, needs: [orders]}\n")
	in.Environment = "staging"
	got := Compute(in)
	if !got.Executable || !slices.Contains(codes(got.Warnings), "need-off:api") {
		t.Fatalf("problems %v, warnings %v", codes(got.Problems), codes(got.Warnings))
	}
	for _, a := range got.Actions {
		if a.Name == "orders" {
			t.Fatalf("attached in staging: %+v", a)
		}
		if a.Service != nil && slices.ContainsFunc(a.Service.Env, func(l EnvLink) bool { return l.From == "orders" }) {
			t.Fatalf("api got it in staging: %+v", a.Service.Env)
		}
	}
	in.Environment = "production"
	if got := Compute(in); !got.Executable || !slices.ContainsFunc(got.Actions, func(a Action) bool { return a.Name == "orders" }) {
		t.Fatalf("production: %v", codes(got.Problems))
	}
}
