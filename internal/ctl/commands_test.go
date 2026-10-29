package ctl

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

func TestNewRepositories(t *testing.T) {
	got, err := newRepositories([]string{
		"acme/web", " ", "billing=other/web", "https://github.com/acme/api", "mono = git@github.com:acme/mono.git",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []client.NewRepository{
		{Repository: "acme/web"},
		{Alias: "billing", Repository: "other/web"},
		{Repository: "https://github.com/acme/api"},
		{Alias: "mono", Repository: "git@github.com:acme/mono.git"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for _, bad := range []string{"=acme/web", "billing="} {
		if _, err := newRepositories([]string{bad}); err == nil {
			t.Fatalf("%q was taken", bad)
		}
	}
	if got, err := newRepositories(nil); err != nil || got != nil {
		t.Fatalf("no --repo gave %+v, %v", got, err)
	}
}

func TestCreateProject_sendsEachRepo(t *testing.T) {
	var got struct {
		Name         string                 `json:"name"`
		Repositories []client.NewRepository `json:"repositories"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/projects" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"proj_1","name":"Shop","slug":"shop"}`))
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{EnvURL: srv.URL, EnvKey: "lp_test_key"}
	var out bytes.Buffer
	app := New(Options{Out: &out, Getenv: func(k string) string { return env[k] }, ConfigPath: t.TempDir() + "/none.json"})

	if err := app.Run([]string{"projects", "create", "Shop", "--repo", "acme/web", "--repo", "billing=other/web,acme/api"}); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	want := []client.NewRepository{{Repository: "acme/web"}, {Alias: "billing", Repository: "other/web"}, {Repository: "acme/api"}}
	if got.Name != "Shop" || !slices.Equal(got.Repositories, want) {
		t.Fatalf("sent %+v", got)
	}
}

// Forge's help prints the program's name before a command's usage, so a
// usage that starts with it too reads "loomctl loomctl".
func TestUsage_leavesTheProgramNameToHelp(t *testing.T) {
	var walk func(cmds []cli.Command)
	walk = func(cmds []cli.Command) {
		for _, c := range cmds {
			if strings.HasPrefix(c.Usage(), "loomctl ") {
				t.Errorf("%s's usage starts with loomctl: %q", c.Name(), c.Usage())
			}
			walk(c.Subcommands())
		}
	}
	walk(New(Options{}).commands())

	var out bytes.Buffer
	app := New(Options{Out: &out, Getenv: func(string) string { return "" }, ConfigPath: t.TempDir() + "/none.json"})
	// Help may end with an error; the text is what's checked.
	_ = app.Run([]string{"projects", "create", "--help"})
	if !strings.Contains(out.String(), "  loomctl projects create <name>") || strings.Contains(out.String(), "loomctl loomctl") {
		t.Fatalf("help:\n%s", out.String())
	}
}

func canaryApp(t *testing.T, handler http.HandlerFunc) (*App, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	env := map[string]string{EnvURL: srv.URL, EnvKey: "lp_test_key"}
	var out bytes.Buffer
	return New(Options{Out: &out, Getenv: func(k string) string { return env[k] }, ConfigPath: t.TempDir() + "/none.json"}), &out
}

// squeeze turns every run of spaces into one, so a table's column widths
// don't matter to what a test looks for.
func squeeze(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}
	return strings.Join(lines, "\n")
}

const failedVerdict = `{"id":"dep_1/0/service:web/1/0","deployment_id":"dep_1","retry":0,"action_id":"service:web","service":"web","step":1,"attempt":0,"weight":25,"verdict":"fail","reason":"The error rate is 3.2%, over the 1% limit.","baseline":{"requests":900,"errors":1,"latency_ms":100,"statistic":"p95","available":true},"candidate":{"requests":250,"errors":8,"latency_ms":140,"statistic":"p95","available":true}}`

func TestDeploymentPrintsTheCanary(t *testing.T) {
	canary := `{"status":"rolled_back","steps":[5,25,50,100],"hold_seconds":600,"thresholds":{},"step":1,"weight":25,"reason":"The canary failed at 25%.","services":[]}`
	app, out := canaryApp(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/deployments/dep_1":
			_, _ = w.Write([]byte(`{"id":"dep_1","environment":"production","status":"failed","steps":[],"canary":` + canary + `}`))
		case "/api/v1/deployments/dep_1/canary":
			_, _ = w.Write([]byte(`{"canary":` + canary + `,"verdicts":[` + failedVerdict + `]}`))
		default:
			http.NotFound(w, r)
		}
	})
	if err := app.Run([]string{"deployment", "dep_1"}); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	got := squeeze(out.String())
	for _, want := range []string{
		"Canary: rolled back at 25%",
		"STEP WEIGHT SERVICE VERDICT ERRORS LATENCY REASON",
		"2 25% web fail 3.2% 140ms p95 The error rate is 3.2%, over the 1% limit.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(got, "NEEDS YOU") {
		t.Fatalf("a clean rollback asks for nothing:\n%s", out)
	}
}

func TestDeploymentPrintsWhatAFailedRevertNeeds(t *testing.T) {
	for _, status := range []string{"revert_failed", "promote_failed"} {
		canary := `{"status":"` + status + `","steps":[25],"step":0,"weight":25,"reason":"Traffic may still be split: web's candidate couldn't be removed.","services":[]}`
		app, out := canaryApp(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/canary") {
				_, _ = w.Write([]byte(`{"canary":` + canary + `,"verdicts":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"dep_1","environment":"production","status":"failed","steps":[],"canary":` + canary + `}`))
		})
		if err := app.Run([]string{"deployment", "dep_1"}); err != nil {
			t.Fatalf("run: %v\n%s", err, out)
		}
		want := "Canary: " + strings.ReplaceAll(status, "_", " ") + " at 25%\n  NEEDS YOU: Traffic may still be split: web's candidate couldn't be removed."
		if !strings.Contains(out.String(), want) {
			t.Fatalf("%s: missing %q in:\n%s", status, want, out)
		}
	}
}

func TestFollowPrintsEachVerdictOnce(t *testing.T) {
	old := client.Every
	client.Every = time.Millisecond
	t.Cleanup(func() { client.Every = old })
	canary := `{"status":"ramping","steps":[25],"step":0,"weight":25,"services":[]}`
	var polls int
	app, out := canaryApp(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/canary") {
			_, _ = w.Write([]byte(`{"canary":` + canary + `,"verdicts":[` + failedVerdict + `]}`))
			return
		}
		polls++
		status := "running"
		if polls >= 3 {
			status = "succeeded"
		}
		_, _ = w.Write([]byte(`{"id":"dep_1","environment":"production","status":"` + status + `","steps":[],"canary":` + canary + `}`))
	})
	if err := app.Run([]string{"deployment", "dep_1", "--watch"}); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if polls < 3 {
		t.Fatalf("polled %d times", polls)
	}
	if n := strings.Count(out.String(), "verdict    web at 25%"); n != 1 {
		t.Fatalf("the verdict line printed %d times:\n%s", n, out)
	}
}

func TestPlanPrintsTheRelease(t *testing.T) {
	var out bytes.Buffer
	app := New(Options{Out: &out})
	refusal := client.Finding{Code: "canary-unavailable", Message: "ECS can't weight a canary here."}
	for _, tc := range []struct {
		name    string
		release *client.Release
		want    string
		warn    []client.Finding
	}{
		{"canary", &client.Release{Strategy: "canary", Steps: []int{5, 25, 50, 100}, HoldSeconds: 600, Canary: []string{"service:web"}}, "Release: canary at 5%, 25%, 50%, 100%, holding 10m at each step\n", nil},
		{"direct", &client.Release{Strategy: "direct"}, "Release: direct\n", nil},
		{"none", nil, "", nil},
		{"hours", &client.Release{Strategy: "canary", Steps: []int{50, 100}, HoldSeconds: 5400, Canary: []string{"service:web"}}, "holding 1h30m at each step", nil},
		{"refused", &client.Release{Strategy: "canary", Steps: []int{5, 100}, HoldSeconds: 60}, "Release: canary at 5%, 100%, holding 1m at each step (no service in this plan can ramp)\n", []client.Finding{refusal}},
	} {
		out.Reset()
		app.printPlan(&client.Plan{ID: "plan_1", Environment: "production", Status: "ready", Release: tc.release, Warnings: tc.warn})
		got := out.String()
		if tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%s: missing %q in:\n%s", tc.name, tc.want, got)
		}
		if tc.release == nil && strings.Contains(got, "Release:") {
			t.Errorf("%s: a plan without a release prints one:\n%s", tc.name, got)
		}
		for _, f := range tc.warn {
			if !strings.Contains(got, "Warning (canary-unavailable): "+f.Message) {
				t.Errorf("%s: the refusal is hidden:\n%s", tc.name, got)
			}
		}
	}
}

// mcpAgent connects an MCP client to loomctl's MCP server over a fake API.
func mcpAgent(t *testing.T, handler http.HandlerFunc, opts MCPOptions) *mcp.ClientSession {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	st, ct := mcp.NewInMemoryTransports()
	if _, err := NewMCPServer(client.New(srv.URL, "lp_test_key"), "test", opts).Connect(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: %+v", name, res.Content)
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("%s: %v in %s", name, err, data)
	}
}

func TestMCP_createPlanTakesFrom(t *testing.T) {
	api := &releaseAPI{}
	cs := mcpAgent(t, api.handler(t), MCPOptions{})
	var plan client.Plan
	callTool(t, cs, "create_plan", map[string]any{"project": "shop", "environment": "production", "from": "staging"}, &plan)
	if want := map[string]any{"environment": "production", "from": "staging"}; !reflect.DeepEqual(api.bodies["/api/v1/projects/proj_1/plans"], want) {
		t.Fatalf("plans.create body %v", api.bodies)
	}
	if plan.ID != "plan_1" || plan.Kind != "promotion" || plan.From != "staging" {
		t.Fatalf("plan %+v", plan)
	}
}

func TestMCP_rollbackNeedsAllowDeploy(t *testing.T) {
	tool := func(cs *mcp.ClientSession, name string) *mcp.Tool {
		res, err := cs.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tl := range res.Tools {
			if tl.Name == name {
				return tl
			}
		}
		return nil
	}
	if tool(mcpAgent(t, (&releaseAPI{}).handler(t), MCPOptions{}), "create_rollback") != nil {
		t.Fatal("create_rollback is offered without --allow-deploy")
	}

	api := &releaseAPI{}
	cs := mcpAgent(t, api.handler(t), MCPOptions{AllowDeploy: true})
	tl := tool(cs, "create_rollback")
	if tl == nil || tl.Annotations == nil || tl.Annotations.DestructiveHint == nil || !*tl.Annotations.DestructiveHint {
		t.Fatalf("create_rollback %+v", tl)
	}
	var plan client.Plan
	callTool(t, cs, "create_rollback", map[string]any{"project": "shop", "environment": "production", "reason": "5xx", "deployment_id": "dep_3"}, &plan)
	if want := map[string]any{"environment": "production", "reason": "5xx", "deployment_id": "dep_3"}; !reflect.DeepEqual(api.bodies["/api/v1/projects/proj_1/rollbacks"], want) {
		t.Fatalf("rollbacks.create body %v", api.bodies)
	}
	// It waits until the plan is planned, and approves nothing.
	if plan.ID != "plan_r" || plan.Status != "ready" || len(plan.Leftovers) != 1 || api.approved {
		t.Fatalf("plan %+v, approved %v", plan, api.approved)
	}
}

func TestMCP_getReleaseWatch(t *testing.T) {
	api := &releaseAPI{watch: watchJSON("watching", "", "", "", false, `[{"name":"api","provider":"render","verdict":"pass","reason":"","op":"update"}]`)}
	// Read-only: offered without --allow-deploy.
	cs := mcpAgent(t, api.handler(t), MCPOptions{})
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var found *mcp.Tool
	for _, tl := range res.Tools {
		if tl.Name == "get_release_watch" {
			found = tl
		}
	}
	if found == nil || found.Annotations == nil || !found.Annotations.ReadOnlyHint {
		t.Fatalf("get_release_watch %+v", found)
	}
	var w client.ReleaseWatch
	callTool(t, cs, "get_release_watch", map[string]any{"deployment_id": "dep_9"}, &w)
	if w.ID != "rw_1" || w.Status != client.WatchWatching || len(w.Services) != 1 || w.Services[0].Verdict != "pass" {
		t.Fatalf("watch %+v", w)
	}
}
