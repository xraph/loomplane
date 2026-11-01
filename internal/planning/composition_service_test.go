package planning

import (
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

func reusableKinds(t *testing.T) *recipes.Registry {
	t.Helper()
	k := recipestest.Graph()
	k.Placement = recipes.PlacementServiceProject
	k.Service = &recipes.ServiceDefinition{Connection: map[string]string{recipes.FieldURL: "url"}, Recovery: "graph", BackupFormat: "graph-rdb-v1"}
	r := recipes.NewRegistry()
	for _, k := range []recipes.Kind{k, graphApp()} {
		if err := r.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestReusableServiceStandaloneBinding(t *testing.T) {
	r := composedPlan(t, reusableKinds(t), "version: 1\nresources:\n  g: {kind: graph, provider: server, target: "+targetA+"}\n")
	if !r.Executable {
		t.Fatalf("blocked: %+v", r.Problems)
	}
	a := findAction(r, "resource:g")
	if len(r.Actions) != 1 || a == nil || a.Resource.Component == nil || a.Resource.Component.Resource != "g" || a.Resource.Composition != nil {
		t.Fatalf("direct service actions: %+v", r.Actions)
	}
	next := composedPlan(t, reusableKinds(t), "version: 1\nresources:\n  g: {kind: graph, provider: server, target: "+targetA+"}\n", boundFrom(t, r)...)
	if !next.Executable || findAction(next, "resource:g").Op != Keep {
		t.Fatalf("replan: %+v", next)
	}
}

func TestReusableServiceProvisionAndReuseOrdering(t *testing.T) {
	r := composedPlan(t, reusableKinds(t), graphOn)
	if !r.Executable {
		t.Fatalf("new blocked: %+v", r.Problems)
	}
	a := findAction(r, "resource:app--falkordb")
	if a == nil || a.Resource.Component == nil || a.Resource.Component.Resource != "app--falkordb" || findAction(r, "resource:app--falkordb--server") != nil {
		t.Fatalf("new direct service: %+v", a)
	}
	doc := "version: 1\nresources:\n  app: {kind: graphapp, provider: server, target: " + targetA + ", uses: {falkordb: {resource: zgraph}}}\n  zgraph: {kind: graph, provider: server, target: " + targetA + "}\n"
	r = composedPlan(t, reusableKinds(t), doc)
	if !r.Executable {
		t.Fatalf("reuse blocked: %+v", r.Problems)
	}
	service, api := findAction(r, "resource:zgraph"), findAction(r, "resource:app--api")
	if findAction(r, "resource:app--falkordb") != nil || service == nil || api == nil || api.Step <= service.Step || !slices.Contains(api.DependsOn, "resource:zgraph") {
		t.Fatalf("reuse actions: %+v", r.Actions)
	}
	bad := composedPlan(t, reusableKinds(t), "version: 1\nresources:\n  app: {kind: graphapp, provider: server, target: "+targetA+", uses: {falkordb: {resource: g}}}\n  g: {kind: graph, provider: server, target: "+targetB+"}\n")
	if bad.Executable || !hasCode(bad.Problems, "catalog-connect-across-targets") {
		t.Fatalf("incompatible target: %+v", bad.Problems)
	}
}
