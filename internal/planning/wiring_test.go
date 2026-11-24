package planning

import (
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

// wiredEnv is the variables api gets from orders, by name and part.
func wiredEnv(t *testing.T, got *Result) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, a := range got.Actions {
		if a.Name != "api" || a.Service == nil {
			continue
		}
		for _, l := range a.Service.Env {
			if l.From == "orders" {
				out[l.Key] = l.Part
			}
		}
	}
	return out
}

func wiredInput(t *testing.T, wiring string) Input {
	t.Helper()
	in := workspaceInput(t)
	in.Manifest = parse(t, "version: 1\nresources:\n  orders: {kind: postgres, existing_resource: "+workspaceService+"}\n"+
		"services:\n  api:\n    source: cmd/api\n    provider: server\n    target: "+targetA+"\n    endpoint: {host_port: 8080}\n    needs: [orders]\n"+wiring)
	return in
}

func TestWiringRenamesTheAddressOrSplitsIt(t *testing.T) {
	got := Compute(wiredInput(t, "    wiring:\n      orders:\n        env: {DATABASE_DSN: url}\n"))
	if !got.Executable {
		t.Fatalf("problems: %v", codes(got.Problems))
	}
	// The kind's DATABASE_URL gives way to the wired name.
	if env := wiredEnv(t, got); len(env) != 1 || env["DATABASE_DSN"] != "" {
		t.Fatalf("renamed: %v", env)
	}

	got = Compute(wiredInput(t, "    wiring:\n      orders:\n        env: {PGHOST: host, PGPORT: port, PGUSER: user, PGPASSWORD: password, PGDATABASE: database}\n"))
	if !got.Executable {
		t.Fatalf("problems: %v", codes(got.Problems))
	}
	want := map[string]string{"PGHOST": "host", "PGPORT": "port", "PGUSER": "user", "PGPASSWORD": "password", "PGDATABASE": "database"}
	if env := wiredEnv(t, got); len(env) != len(want) {
		t.Fatalf("split: %v", env)
	} else {
		for k, v := range want {
			if env[k] != v {
				t.Fatalf("split: %v", env)
			}
		}
	}
}

func TestWiringRefusesAVariableTwoNeedsWouldSet(t *testing.T) {
	in := wiredInput(t, "    wiring:\n      orders:\n        env: {REDIS_URL: url}\n")
	// A second need whose own variable is REDIS_URL.
	in.Manifest = parse(t, "version: 1\nresources:\n  orders: {kind: postgres, existing_resource: "+workspaceService+"}\n  cache: {kind: redis, provider: server, target: "+targetA+"}\n"+
		"services:\n  api:\n    source: cmd/api\n    provider: server\n    target: "+targetA+"\n    endpoint: {host_port: 8080}\n    needs: [orders, cache]\n    wiring:\n      orders:\n        env: {REDIS_URL: url}\n")
	got := Compute(in)
	if got.Executable || !slices.Contains(codes(got.Problems), "wiring-conflict:api") {
		t.Fatalf("problems: %v", codes(got.Problems))
	}
}

func TestWiringLeavesAServiceItIsntOn(t *testing.T) {
	in := wiredInput(t, "    wiring:\n      orders:\n        env: {PGHOST: host}\n")
	in.Manifest = parse(t, "version: 1\nresources:\n  orders: {kind: postgres, existing_resource: "+workspaceService+", environments: {production: {enabled: true}}}\n"+
		"services:\n  api:\n    source: cmd/api\n    provider: server\n    target: "+targetA+"\n    endpoint: {host_port: 8080}\n    needs: [orders]\n    wiring:\n      orders:\n        env: {PGHOST: host}\n")
	in.Environment = "staging"
	got := Compute(in)
	if !got.Executable || len(wiredEnv(t, got)) != 0 {
		t.Fatalf("problems %v, env %v", codes(got.Problems), wiredEnv(t, got))
	}
}

func TestAMappingCanNameAPartOfAnAddress(t *testing.T) {
	in := workspaceInput(t)
	in.ResourceMappings = map[string]map[string]string{"": {"PGHOST": workspaceService + ".host"}}
	got := Compute(in)
	if !got.Executable {
		t.Fatalf("problems: %v", codes(got.Problems))
	}
	env := wiredEnv(t, got)
	if env["DATABASE_URL"] != "" || env["PGHOST"] != "host" {
		t.Fatalf("env: %v", env)
	}
}

// A catalog kind gives its own variables, one per output, so wiring it
// would drop the others: the plan says so instead.
func TestWiringRefusesACatalogKindsOwnVariables(t *testing.T) {
	got := composedPlan(t, recipestest.Registry(t), sampleApp+"services:\n  web:\n    source: cmd/api\n    provider: server\n    target: "+targetA+"\n    public: false\n    needs: [app]\n    wiring:\n      app:\n        env: {GATE: url}\n")
	if !slices.Contains(codes(got.Problems), "wiring-unsupported:web") {
		t.Fatalf("problems: %v", codes(got.Problems))
	}
}

// A database Loomplane runs is wired the same way as one it attaches.
func TestWiringAppliesToADatabaseLoomplaneRuns(t *testing.T) {
	got := composedPlan(t, recipestest.Registry(t), "version: 1\nresources:\n  orders: {kind: postgres, provider: server, target: "+targetA+"}\n"+
		"services:\n  api:\n    source: cmd/api\n    provider: server\n    target: "+targetA+"\n    public: false\n    needs: [orders]\n    wiring:\n      orders:\n        env: {PGHOST: host, DB: url}\n")
	if !got.Executable {
		t.Fatalf("problems: %v", codes(got.Problems))
	}
	if env := wiredEnv(t, got); len(env) != 2 || env["PGHOST"] != "host" || env["DB"] != "" {
		t.Fatalf("env: %v", env)
	}
}
