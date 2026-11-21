package planning

import (
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/providers"
)

func TestResourceOutputPlan(t *testing.T) {
	in := Input{Environment: "production", Graph: polyglot(t), Manifest: parse(t, acme), Providers: providers.Registry(), Connections: connected}
	in.Bindings = deployed(t, Compute(in))
	in.ResourceMappings = map[string]map[string]string{"": {"APP_DB_URL": "db"}}
	result := Compute(in)
	api := action(t, result, "service:api")
	has := func(a Action) bool {
		return slices.ContainsFunc(a.Service.Env, func(l EnvLink) bool { return l.Key == "APP_DB_URL" && l.From == "db" && l.Subject == ResourceSubject })
	}
	if !result.Executable || api.Op != Update || !has(api) || has(action(t, result, "service:web")) {
		t.Fatalf("wrong scope: %v %+v", result.Problems, api)
	}
	in.Bindings = deployed(t, result)
	if next := action(t, Compute(in), "service:api"); next.Op != Keep {
		t.Fatalf("unstable mapping: %+v", next)
	}
	in.ResourceMappings[""]["APP_DB_URL"] = "db.nonexistent"
	result = Compute(in)
	if result.Executable || !slices.Contains(findingCodes(result.Problems), "resource-variable-output") {
		t.Fatal(result.Problems)
	}
}
