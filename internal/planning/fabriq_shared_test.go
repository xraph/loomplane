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
			want := "orders's Postgres is already used by Fabriq billing. Each Fabriq needs its own database."
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

func TestFabriqRefusesAnEndpointAnotherEnvironmentsFabriqUses(t *testing.T) {
	doc := "version: 1\nresources:\n  orders: {kind: fabriq, provider: server, target: " + targetA + ", uses: {postgres: {external: " + prodPG + "}}}\n"
	r := Compute(Input{
		Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
		Connections: composedConnections(t), Kinds: recipes.Default(),
		FabriqPostgres: map[string]string{prodPG: "events", otherPG: "audit"},
	})
	shared := sharedFindings(r)
	if len(shared) != 1 || shared[0].Message != "orders's Postgres is already used by Fabriq events. Each Fabriq needs its own database." {
		t.Fatalf("shared: %+v", shared)
	}
}

func TestFabriqPostgresReadsOtherEnvironmentsBindings(t *testing.T) {
	bound := func(env, name, kind string, choice DependencyChoice) *Binding {
		raw, err := json.Marshal(ResourceConfig{Provider: "server", Kind: kind, Composition: &CompositionConfig{Kind: kind, Dependencies: map[string]DependencyChoice{"postgres": choice}}})
		if err != nil {
			t.Fatal(err)
		}
		return &Binding{ID: env + "-" + name, EnvironmentID: env, Subject: ResourceSubject, Name: name, Config: raw}
	}
	external := DependencyChoice{Mode: DependencyExternal, External: prodPG}
	got := FabriqPostgres([]*Binding{
		bound("staging", "events", "fabriq", external),
		// This environment's own bindings are the manifest's business.
		bound("production", "orders", "fabriq", DependencyChoice{Mode: DependencyExternal, External: otherPG}),
		bound("staging", "lib", "fabriq-library", DependencyChoice{Mode: DependencyNew, Resource: "lib--postgres"}),
		bound("staging", "app", "sample", DependencyChoice{Mode: DependencyExternal, External: otherPG}),
	}, "production")
	if want := map[string]string{prodPG: "events"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}
