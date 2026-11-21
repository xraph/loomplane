package ctl

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

// servicesFake answers services add's lookups and records each manifest
// edit's body. It answers a plan for staging or production as ready.
func servicesFake(t *testing.T, manifestYAML, connections string, edits *[]map[string]any, plans *[]string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if projectLookups(w, r, manifestYAML, connections) {
			return
		}
		switch r.URL.Path {
		case "/api/v1/projects/shop/manifest/suggestions":
			_, _ = w.Write([]byte(`{"analyzed":true,"services":[{"id":"svc:go:cmd/worker","kind":"worker","ecosystem":"go","path":"cmd/worker","service":{"name":"worker","source":"go:cmd/worker","provider":"ecs-fargate","needs":["db"]}}]}`))
		case "/api/v1/projects/shop/manifest/edits":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			*edits = append(*edits, body)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "rev_2", "revision": 2, "yaml": "version: 1\nservices:\n  api: {source: cmd/api}\n"})
		case "/api/v1/projects/shop/plans":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			*plans = append(*plans, body["environment"].(string))
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"plan_1","status":"planning"}`))
		case "/api/v1/plans/plan_1":
			_, _ = w.Write([]byte(`{"id":"plan_1","status":"ready","executable":true,"environment":"staging"}`))
		default:
			http.NotFound(w, r)
		}
	}
}

// A catalog kind needs no name or target when the manifest has room for
// its own name and one ready connection runs it.
func TestServicesAddDefaultsNameAndTarget(t *testing.T) {
	var edits []map[string]any
	var plans []string
	app, _ := recipesCLI(t, recipestest.Registry(t), servicesFake(t, "version: 1\nresources:\n  sample: {kind: redis}\n", "", &edits, &plans))
	if err := app.Run([]string{"services", "add", "Shop", "--kind", "sample", "--json"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"add_resource": map[string]any{
		"name": "sample-2", "kind": "sample", "provider": "server", "target": "conn_1", "version": "1",
	}}
	if len(edits) != 1 || !reflect.DeepEqual(edits[0], want) {
		t.Fatalf("edits %+v", edits)
	}
	if !reflect.DeepEqual(plans, []string{"production"}) {
		t.Fatalf("plans %v", plans)
	}
}

func TestServicesAddAsksWhichTargetWhenThereAreSeveral(t *testing.T) {
	var edits []map[string]any
	var plans []string
	conns := `[{"id":"conn_1","cloud":"server","name":"a","status":"ready"},{"id":"conn_2","cloud":"kubernetes","name":"b","status":"ready","details":{"namespace":"apps"}}]`
	app, _ := recipesCLI(t, recipestest.Registry(t), servicesFake(t, "", conns, &edits, &plans))
	err := app.Run([]string{"services", "add", "shop", "--kind", "sample"})
	if err == nil || !strings.Contains(err.Error(), "--target") || !strings.Contains(err.Error(), "conn_2") || len(edits) != 0 {
		t.Fatalf("err %v, edits %v", err, edits)
	}
	// Naming the provider narrows it to one.
	if err := app.Run([]string{"services", "add", "shop", "--kind", "sample", "--provider", "kubernetes", "--json"}); err != nil {
		t.Fatal(err)
	}
	if got := edits[0]["add_resource"].(map[string]any); got["target"] != "conn_2" || got["provider"] != "kubernetes" {
		t.Fatalf("add_resource %+v", got)
	}
}

// A service from your code takes its name from its directory, and --only
// and --needed-by scope and wire it in the same edit.
func TestServicesAddFromSource(t *testing.T) {
	var edits []map[string]any
	var plans []string
	app, out := recipesCLI(t, recipestest.Registry(t), servicesFake(t, "version: 1\nservices:\n  api: {source: cmd/api}\n", "", &edits, &plans))
	args := []string{"services", "add", "shop", "--source", "go:services/billing", "--kind", "worker", "--needs", "api", "--needed-by", "api", "--env", "staging", "--only"}
	if err := app.Run(args); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"add_service": map[string]any{
		"name": "billing", "source": "go:services/billing", "kind": "worker",
		"needs": []any{"api"}, "needed_by": []any{"api"}, "enabled_in": "staging", "only": true,
	}}
	if len(edits) != 1 || !reflect.DeepEqual(edits[0], want) {
		t.Fatalf("edits %+v", edits)
	}
	if !reflect.DeepEqual(plans, []string{"staging"}) || !strings.Contains(out.String(), "Added billing to Shop.") {
		t.Fatalf("plans %v, output %s", plans, out)
	}
}

func TestServicesAddSuggested(t *testing.T) {
	var edits []map[string]any
	var plans []string
	app, _ := recipesCLI(t, recipestest.Registry(t), servicesFake(t, "", "", &edits, &plans))
	if err := app.Run([]string{"services", "add", "shop", "--suggested", "worker", "--provider", "cloud-run", "--no-plan"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"add_service": map[string]any{
		"name": "worker", "source": "go:cmd/worker", "provider": "cloud-run", "needs": []any{"db"},
	}}
	if len(edits) != 1 || !reflect.DeepEqual(edits[0], want) || len(plans) != 0 {
		t.Fatalf("edits %+v, plans %v", edits, plans)
	}
	if err := app.Run([]string{"services", "add", "shop", "--suggested", "nope"}); err == nil || !strings.Contains(err.Error(), "Suggested: worker") {
		t.Fatalf("an unknown suggestion: %v", err)
	}
}

func TestServicesAddDryRunPrintsTheYAML(t *testing.T) {
	var edits []map[string]any
	var plans []string
	app, out := recipesCLI(t, recipestest.Registry(t), servicesFake(t, "", "", &edits, &plans))
	if err := app.Run([]string{"services", "add", "shop", "--source", "apps/web/Dockerfile", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 || edits[0]["dry_run"] != true || edits[0]["add_service"].(map[string]any)["name"] != "web" || len(plans) != 0 {
		t.Fatalf("edits %+v, plans %v", edits, plans)
	}
	if !strings.Contains(out.String(), "Nothing was saved.") || !strings.Contains(out.String(), "api: {source: cmd/api}") {
		t.Fatalf("output %s", out)
	}
}

func TestServicesSuggestListsWhatToAdd(t *testing.T) {
	var edits []map[string]any
	var plans []string
	app, out := recipesCLI(t, recipestest.Registry(t), servicesFake(t, "", "", &edits, &plans))
	if err := app.Run([]string{"services", "suggest", "shop"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"worker", "go:cmd/worker", "ecs-fargate", "db", "loomctl services add shop --suggested worker"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestServiceNames(t *testing.T) {
	for source, want := range map[string]string{
		"go:services/api":          "api",
		"apps/web/":                "web",
		"apps/web/Dockerfile":      "web",
		"Dockerfile":               "app",
		"shop/Procfile/worker":     "worker",
		"python:services/ML_Model": "ml-model",
		"cmd/2fa":                  "service-2fa",
	} {
		if got := slug(lastPart(source)); got != want {
			t.Errorf("%s: %q, want %q", source, got, want)
		}
	}
	taken := map[string]bool{"api": true, "api-2": true}
	if got := freeName("api", taken); got != "api-3" {
		t.Errorf("freeName = %q", got)
	}
}

// A Cloud target replaces the provider the suggestion came with.
func TestServicesAddOnACloudTarget(t *testing.T) {
	var edits []map[string]any
	var plans []string
	app, _ := recipesCLI(t, recipestest.Registry(t), servicesFake(t, "", "", &edits, &plans))
	if err := app.Run([]string{"services", "add", "shop", "--suggested", "worker", "--cloud", "GPU Lab", "--cloud-target", "a100", "--no-plan"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"add_service": map[string]any{
		"name": "worker", "source": "go:cmd/worker", "cloud": "GPU Lab", "cloud_target": "a100", "needs": []any{"db"},
	}}
	if len(edits) != 1 || !reflect.DeepEqual(edits[0], want) {
		t.Fatalf("edits %+v", edits)
	}
	for _, args := range [][]string{
		{"services", "add", "shop", "--suggested", "worker", "--cloud-target", "a100", "--provider", "cloud-run"},
		{"services", "add", "shop", "--suggested", "worker", "--cloud", "GPU Lab"},
	} {
		if err := app.Run(args); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
}
