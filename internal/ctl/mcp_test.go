package ctl

import (
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xraph/loomplane/pkg/agentops"
	"github.com/xraph/loomplane/pkg/client"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/mcp_tools.json from today's server")

// mcpSurface is what an agent sees of the MCP server before it calls a tool.
type mcpSurface struct {
	Instructions string          `json:"instructions"`
	Resources    []*mcp.Resource `json:"resources"`
	Tools        []*mcp.Tool     `json:"tools"`
}

func listSurface(t *testing.T, opts MCPOptions) mcpSurface {
	t.Helper()
	cs := mcpAgent(t, func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }, opts)
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := cs.ListResources(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return mcpSurface{Instructions: cs.InitializeResult().Instructions, Resources: res.Resources, Tools: tools.Tools}
}

// canonical marshals v and reads it back, so map keys sort and a schema's
// field order doesn't matter.
func canonical(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err = json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	out, err := json.MarshalIndent(generic, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

// TestMCPSurfaceIsUnchanged holds loomctl mcp to what it served before its
// tools came from pkg/agentops: the same tools, descriptions, input and
// output schemas, annotations, instructions and resources, with and
// without --allow-deploy. testdata/mcp_tools.json was written from the
// server as it was.
func TestMCPSurfaceIsUnchanged(t *testing.T) {
	got := canonical(t, map[string]mcpSurface{
		"default":      listSurface(t, MCPOptions{}),
		"allow_deploy": listSurface(t, MCPOptions{AllowDeploy: true}),
	})
	path := filepath.Join("testdata", "mcp_tools.json")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("loomctl mcp's surface changed; diff against %s:\n%s", path, got)
	}
}

// offeredDefs is the definitions loomctl mcp offers with opts, by name.
func offeredDefs(opts MCPOptions) map[string]agentops.Definition {
	out := map[string]agentops.Definition{}
	for _, d := range agentops.All() {
		if d.Surfaces&agentops.SurfaceMCP != 0 || (opts.AllowDeploy && d.Surfaces&agentops.SurfaceMCPDeploy != 0) {
			out[d.Name] = d
		}
	}
	return out
}

func TestMCPToolsMatchDefinitions(t *testing.T) {
	for _, opts := range []MCPOptions{{}, {AllowDeploy: true}} {
		defs := offeredDefs(opts)
		tools := listSurface(t, opts).Tools
		names := make([]string, 0, len(tools))
		for _, tl := range tools {
			names = append(names, tl.Name)
		}
		want := make([]string, 0, len(defs))
		for name := range defs {
			want = append(want, name)
		}
		slices.Sort(names)
		slices.Sort(want)
		if !slices.Equal(names, want) {
			t.Fatalf("allow deploy %v: tools %v, want %v", opts.AllowDeploy, names, want)
		}
		for _, tl := range tools {
			d := defs[tl.Name]
			if tl.Description != d.Description {
				t.Errorf("%s: description %q, want %q", tl.Name, tl.Description, d.Description)
			}
			schema, err := d.Schema()
			if err != nil {
				t.Fatal(err)
			}
			if got, want := canonical(t, tl.InputSchema), canonical(t, schema); string(got) != string(want) {
				t.Errorf("%s: input schema\n%s\nwant\n%s", tl.Name, got, want)
			}
			a := tl.Annotations
			if a == nil {
				t.Fatalf("%s: no annotations", tl.Name)
			}
			if a.ReadOnlyHint != d.ReadOnly || a.IdempotentHint != d.ReadOnly {
				t.Errorf("%s: readOnlyHint %v idempotentHint %v, want %v", tl.Name, a.ReadOnlyHint, a.IdempotentHint, d.ReadOnly)
			}
			switch {
			case d.ReadOnly && a.DestructiveHint != nil:
				t.Errorf("%s: read-only with destructiveHint %v", tl.Name, *a.DestructiveHint)
			case !d.ReadOnly && (a.DestructiveHint == nil || *a.DestructiveHint != d.Destructive):
				t.Errorf("%s: destructiveHint %v, want %v", tl.Name, a.DestructiveHint, d.Destructive)
			}
		}
	}
}

func TestMCPServesNoToolWithoutADefinition(t *testing.T) {
	handlers := newMCPHandlers(client.New("http://127.0.0.1:1", "lp_test_key"))
	defs := offeredDefs(MCPOptions{AllowDeploy: true})
	keys := make([]string, 0, len(handlers))
	names := make([]string, 0, len(defs))
	for name, h := range handlers {
		keys = append(keys, name)
		if d, ok := defs[name]; ok && h.input != d.InputType() {
			t.Errorf("%s: handler takes %v, the definition %v", name, h.input, d.InputType())
		}
	}
	for name := range defs {
		names = append(names, name)
	}
	slices.Sort(keys)
	slices.Sort(names)
	if !slices.Equal(keys, names) {
		t.Fatalf("handlers %v, MCP definitions %v", keys, names)
	}

	delete(handlers, "status")
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "status") {
			t.Fatalf("panic %v, want one naming status", r)
		}
	}()
	newMCPServer("test", MCPOptions{}, handlers)
}

func TestMCPRefusesAHandlerOfAnotherInputType(t *testing.T) {
	handlers := newMCPHandlers(client.New("http://127.0.0.1:1", "lp_test_key"))
	handlers["get_plan"] = handlers["get_deployment"]
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "get_plan") {
			t.Fatalf("panic %v, want one naming get_plan", r)
		}
	}()
	newMCPServer("test", MCPOptions{}, handlers)
}

func TestMCPToolsStillCallTheAPI(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	var variable map[string]any
	cs := mcpAgent(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/api/v1/plans/plan_1":
			_, _ = w.Write([]byte(promotionPlan))
		case r.URL.Path == "/api/v1/projects":
			_, _ = w.Write([]byte(`{"projects":[{"id":"proj_1","name":"Shop","slug":"shop"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/projects/proj_1/variables":
			_ = json.NewDecoder(r.Body).Decode(&variable)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"var_1","key":"LOG_LEVEL","value":"debug"}`))
		default:
			http.NotFound(w, r)
		}
	}, MCPOptions{})

	var plan client.Plan
	callTool(t, cs, "get_plan", map[string]any{"plan_id": "plan_1"}, &plan)
	if plan.ID != "plan_1" || plan.Status != "ready" || plan.Revision != "r1" {
		t.Fatalf("plan %+v", plan)
	}
	var v client.Variable
	callTool(t, cs, "set_variable", map[string]any{"project": "shop", "key": "LOG_LEVEL", "value": "debug", "environment": "staging"}, &v)
	if v.ID != "var_1" || v.Key != "LOG_LEVEL" {
		t.Fatalf("variable %+v", v)
	}
	if want := map[string]any{"key": "LOG_LEVEL", "value": "debug", "environment": "staging"}; !reflect.DeepEqual(variable, want) {
		t.Errorf("variable body %v, want %v", variable, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"GET /api/v1/plans/plan_1", "GET /api/v1/projects", "POST /api/v1/projects/proj_1/variables"}; !slices.Equal(calls, want) {
		t.Errorf("calls %v, want %v", calls, want)
	}

	// The schema still refuses what the agent may not send.
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_plan", Arguments: map[string]any{"plan_id": "plan_1", "approve": true}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("get_plan took an unknown argument")
	}
}
