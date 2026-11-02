package deployments

import (
	"context"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

type inventoryDependencyResolver struct {
	calls    int
	scenario string
	record   providerresources.ProviderResource
	endpoint catalogconnect.Endpoint
	dsn      string
	t        *testing.T
}

func (f *inventoryDependencyResolver) VerifyProject(context.Context, security.Principal, string, string, string, int64) (*providerresources.ProviderResource, *catalogconnect.Endpoint, error) {
	return nil, nil, errors.New("use exact scoped access")
}
func (f *inventoryDependencyResolver) ResolveProject(_ context.Context, p security.Principal, project, environment, id string, revision int64) (*providerresources.ProviderResource, *catalogconnect.Endpoint, string, error) {
	f.calls++
	if p.OrgID != "org" || p.UserID != "usr" || project != "proj" || environment != "env" || id != "inventory" || revision != 7 {
		f.t.Errorf("wrong scope: %+v %s %s %s %d", p, project, environment, id, revision)
	}
	if f.scenario == "revoked" {
		return nil, nil, "", security.ErrForbidden
	}
	record, endpoint := f.record, f.endpoint
	switch f.scenario {
	case "replacement":
		record.Incarnation = "replacement"
	case "revision":
		record.Revision++
	case "secret-revision":
		endpoint.Revision++
	case "endpoint":
		record.EndpointID = "other"
	case "record-kind":
		record.Kind = "mysql"
	case "plaintext":
		endpoint.TLS = catalogconnect.TLSDisable
	}
	return &record, &endpoint, f.dsn, nil
}

type inventoryDSNChecker struct {
	checks int
	report *catalogconnect.Report
	t      *testing.T
}

func (f *inventoryDSNChecker) Check(context.Context, string, string, recipes.Requirement, catalogconnect.DialFunc) (*catalogconnect.Report, error) {
	f.t.Error("inventory path re-resolved the endpoint instead of probing approved DSN")
	return nil, errors.New("unexpected endpoint read")
}
func (f *inventoryDSNChecker) CheckDSN(ctx context.Context, dsn string, req recipes.Requirement, dial catalogconnect.DialFunc) (*catalogconnect.Report, error) {
	f.checks++
	if dsn != "postgres://app:SECRET@db.test/main?sslmode=verify-full" || req.Engine != "postgres" || req.MinVersion != "16" || !slices.Equal(req.Extensions, []string{"vector"}) || !slices.Equal(req.Preload, []string{"shared_library"}) || !req.CreateRole || !slices.Equal(req.TLS, []string{catalogconnect.TLSVerifyFull}) || catalogconnect.ConsumerFrom(ctx).Title != "Sample" {
		f.t.Errorf("wrong check: %s %+v consumer=%+v", dsn, req, catalogconnect.ConsumerFrom(ctx))
	}
	if _, err := dial(ctx, "tcp", "db.test:5432"); err != nil {
		return nil, err
	}
	return f.report, nil
}
func inventoryDependencyFixture(t *testing.T) (*componentFixture, *Job, *inventoryDependencyResolver, *inventoryDSNChecker) {
	t.Helper()
	f := newComponentFixture(t, sampleKinds(t, func(kind *recipes.Kind) {
		kind.Dependencies[0].External = &recipes.Requirement{Engine: "postgres", MinVersion: "16", Extensions: []string{"vector"}, Preload: []string{"shared_library"}, CreateRole: true, TLS: []string{catalogconnect.TLSVerifyFull}}
		for i := range kind.Components {
			if kind.Components[i].Name == "api" {
				kind.Components[i].Env = append(kind.Components[i].Env, recipes.EnvVar{Name: "PG_ADMIN_URL", From: &recipes.Ref{Dependency: "postgres", Field: recipes.FieldAdminURL}})
			}
		}
	}))
	pin := &planning.ExistingResourceConfig{ResourceID: "inventory", Revision: 7, EndpointID: "endpoint", EndpointRevision: 3, AccountIdentity: "account", ExternalID: "native", Incarnation: "created"}
	deps := map[string]planning.DependencyChoice{"postgres": {Mode: planning.DependencyExternal, Resource: "db", External: "endpoint", Kind: "postgres", ExistingResource: pin}, "cache": {Mode: planning.DependencyOff}}
	job := f.componentJob(t, "api", deps)
	job.Plan.CreatedBy = "usr"
	resolver := &inventoryDependencyResolver{t: t, record: providerresources.ProviderResource{ID: "inventory", OrgID: "org", AccountIdentity: "account", ExternalID: "native", Incarnation: "created", Revision: 7, EndpointID: "endpoint", EndpointRevision: 3, Kind: "postgres"}, endpoint: catalogconnect.Endpoint{ID: "endpoint", OrgID: "org", Name: "database", Engine: "postgres", Revision: 3, TLS: catalogconnect.TLSVerifyFull}, dsn: "postgres://app:SECRET@db.test/main?sslmode=verify-full"}
	checker := &inventoryDSNChecker{t: t, report: &catalogconnect.Report{Version: "17.2"}}
	f.target.ExistingResources = resolver
	f.target.Checker = checker
	f.target.endpointDial = func(_ context.Context, org, connection string) (catalogconnect.DialFunc, io.Closer, error) {
		if org != "org" || connection != "conn" {
			t.Errorf("wrong target %s %s", org, connection)
		}
		return func(context.Context, string, string) (net.Conn, error) { return nil, nil }, noClose{}, nil
	}
	return f, job, resolver, checker
}
func TestInventoryComponentUsesPinnedScopedDSNAndRecipeRequirement(t *testing.T) {
	f, job, resolver, checker := inventoryDependencyFixture(t)
	out, err := f.target.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	spec, _, env := f.started(t, out)
	if env["DATABASE_URL"] != resolver.dsn || env["PG_ADMIN_URL"] != resolver.dsn || spec.Env["DATABASE_URL"] != "" || spec.Env["PG_ADMIN_URL"] != "" || resolver.calls != 1 || checker.checks != 1 {
		t.Fatalf("wrong secret path: env=%v calls=%d checks=%d", env, resolver.calls, checker.checks)
	}
}
func TestInventoryComponentRejectsRevokedChangedAndIncompatibleBeforeRuntime(t *testing.T) {
	for _, scenario := range []string{"revoked", "replacement", "revision", "secret-revision", "endpoint", "extension", "empty-report", "record-kind", "plaintext"} {
		t.Run(scenario, func(t *testing.T) {
			f, job, resolver, checker := inventoryDependencyFixture(t)
			resolver.scenario = scenario
			if scenario == "extension" {
				checker.report = &catalogconnect.Report{Version: "17.2", Missing: []string{"timescaledb"}}
			}
			if scenario == "empty-report" {
				checker.report = &catalogconnect.Report{}
			}
			_, err := f.target.Apply(t.Context(), job)
			if err == nil || strings.Contains(err.Error(), "SECRET") || f.instances.creates != 0 || f.instances.updates != 0 || f.backend.applied != 0 {
				t.Fatalf("unsafe apply: %v creates=%d applied=%d", err, f.instances.creates, f.backend.applied)
			}
			if scenario != "extension" && scenario != "empty-report" && checker.checks != 0 {
				t.Fatal("probed access after inventory pin changed")
			}
		})
	}
}
