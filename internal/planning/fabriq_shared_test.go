package planning

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
)

func sharedFindings(r *Result) []Finding {
	var out []Finding
	for _, f := range r.Problems {
		if f.Code == "catalog-dependency-shared" {
			out = append(out, f)
		}
	}
	return out
}

func TestTwoFabriqsRefuseOneDatabase(t *testing.T) {
	cases := map[string]string{
		"one project resource": "  tsdb: {kind: timescaledb, provider: server, target: " + targetA + "}\n" +
			"  orders: {kind: fabriq, provider: server, target: " + targetA + ", uses: {postgres: {resource: tsdb}}}\n" +
			"  billing: {kind: fabriq-library, provider: server, target: " + targetA + ", uses: {postgres: {resource: tsdb}}}\n",
		"one external endpoint": "  orders: {kind: fabriq, provider: server, target: " + targetA + ", uses: {postgres: {external: " + prodPG + "}}}\n" +
			"  billing: {kind: fabriq, provider: server, target: " + targetA + ", uses: {postgres: {external: " + prodPG + "}}}\n",
	}
	for name, resources := range cases {
		t.Run(name, func(t *testing.T) {
			r := composedPlan(t, recipes.Default(), "version: 1\nresources:\n"+resources)
			shared := sharedFindings(r)
			if len(shared) != 1 || shared[0].Name != "orders" || r.Executable {
				t.Fatalf("shared: %+v (executable %v)", shared, r.Executable)
			}
			want := "orders's Postgres server is already used by Fabriq billing. Each Fabriq needs its own Postgres server: point one of them at another server."
			if shared[0].Message != want || !strings.HasPrefix(shared[0].Path, "resources.orders.uses.postgres.") {
				t.Fatalf("finding: %+v", shared[0])
			}
		})
	}
}

func TestFabriqsWithTheirOwnDatabasesPlan(t *testing.T) {
	doc := "version: 1\nresources:\n" +
		"  orders: {kind: fabriq, provider: server, target: " + targetA + "}\n" +
		"  billing: {kind: fabriq, provider: server, target: " + targetA + ", uses: {postgres: {external: " + prodPG + "}}}\n" +
		"  search: {kind: fabriq-library, provider: server, target: " + targetA + ", uses: {postgres: {external: " + otherPG + "}}}\n" +
		// Off in production, so it uses nothing there.
		"  spare: {kind: fabriq, provider: server, target: " + targetA + ", uses: {postgres: {external: " + otherPG + "}}, environments: {production: {enabled: false}}}\n"
	if shared := sharedFindings(composedPlan(t, recipes.Default(), doc)); len(shared) != 0 {
		t.Fatalf("shared: %+v", shared)
	}
}

// The same manifest planned in a second environment is the commonest way
// two Fabriqs meet on one server, since uses.postgres.external can't vary
// by environment. The finding says where the other one runs.
func TestFabriqRefusesAnEndpointAnotherEnvironmentsFabriqUses(t *testing.T) {
	doc := "version: 1\nresources:\n  orders: {kind: fabriq, provider: server, target: " + targetA + ", uses: {postgres: {external: " + prodPG + "}}}\n"
	remedy := " Each Fabriq needs its own Postgres server: turn Fabriq off in one environment or point it at another server."
	cases := map[string]struct {
		user FabriqUser
		want string
	}{
		"the same manifest in staging":  {FabriqUser{Name: "orders", Environment: "staging"}, "orders's Postgres server is already used by Fabriq orders in staging."},
		"another project":               {FabriqUser{Name: "events", Environment: "production", Other: true, Project: "shop"}, "orders's Postgres server is already used by Fabriq events in project shop (production)."},
		"an environment without a name": {FabriqUser{Name: "events"}, "orders's Postgres server is already used by Fabriq events in another environment."},
		"a project without a name":      {FabriqUser{Name: "events", Environment: "staging", Other: true}, "orders's Postgres server is already used by Fabriq events in another project."},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := Compute(Input{
				Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
				Connections: composedConnections(t), Kinds: recipes.Default(),
				FabriqPostgres: map[string]FabriqUser{prodPG: c.user, otherPG: {Name: "audit", Environment: "staging"}},
			})
			shared := sharedFindings(r)
			if len(shared) != 1 || shared[0].Message != c.want+remedy || r.Executable {
				t.Fatalf("shared: %+v", shared)
			}
		})
	}
}

func TestFabriqPostgresReadsOtherEnvironmentsBindings(t *testing.T) {
	bound := func(project, env, name, kind string, choice DependencyChoice) *OrgResource {
		raw, err := json.Marshal(ResourceConfig{Provider: "server", Kind: kind, Composition: &CompositionConfig{Kind: kind, Dependencies: map[string]DependencyChoice{"postgres": choice}}})
		if err != nil {
			t.Fatal(err)
		}
		return &OrgResource{
			Binding:     &Binding{ID: env + "-" + name, ProjectID: project, EnvironmentID: env, Subject: ResourceSubject, Name: name, Config: raw},
			Environment: env + " name", Project: project + " name",
		}
	}
	external := func(id string) DependencyChoice { return DependencyChoice{Mode: DependencyExternal, External: id} }
	got := FabriqPostgres([]*OrgResource{
		bound("shop", "staging", "events", "fabriq", external(prodPG)),
		// This environment's own bindings are the manifest's business.
		bound("shop", "production", "orders", "fabriq", external(otherPG)),
		bound("shop", "staging", "lib", "fabriq-library", DependencyChoice{Mode: DependencyNew, Resource: "lib--postgres"}),
		bound("shop", "staging", "app", "sample", external(otherPG)),
		bound("blog", "blog-prod", "posts", "fabriq", external(otherPG)),
	}, "shop", "production")
	want := map[string]FabriqUser{
		prodPG: {Name: "events", Environment: "staging name"},
		// Another project's Fabriq is named with its project.
		otherPG: {Name: "posts", Environment: "blog-prod name", Project: "blog name", Other: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}
