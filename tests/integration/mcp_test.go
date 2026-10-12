//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xraph/loomplane/examples/provider-plugin/statichost"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/ctl"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

// agent connects an MCP client to loomctl's MCP server, the way an agent
// would.
func agent(t *testing.T, c *lpclient.Client, opts ctl.MCPOptions) *mcp.ClientSession {
	t.Helper()
	server := ctl.NewMCPServer(c, "test", opts)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// call calls a tool and decodes its structured result into out.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if out != nil && !res.IsError {
		data, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s: %v in %s", name, err, data)
		}
	}
	return res
}

func TestMCP_anAgentPlansAndDeploysThroughLoomctl(t *testing.T) {
	lpclient.Every = 100 * time.Millisecond
	host := &statichost.Host{Dir: t.TempDir(), Token: "host-token-0123456789"}
	plugin := httptest.NewServer(host.Handler("plugin-token-0123456789"))
	defer plugin.Close()
	host.BaseURL = plugin.URL

	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Providers.Plugins = []config.PluginConfig{{URL: plugin.URL, Token: "plugin-token-0123456789"}}
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	analyze(t, cfg, c, proj.ID)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]any{"cloud": "static-host", "name": "Sites", "credential": host.Token}, nil)
	var write, read keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "agent", "access": "write"}, &write)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "reader", "access": "read"}, &read)

	tools := func(cs *mcp.ClientSession) []string {
		res, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(res.Tools))
		for _, tl := range res.Tools {
			names = append(names, tl.Name)
		}
		return names
	}
	// Without --allow-deploy an agent plans, and a person approves.
	cautious := agent(t, lpclient.New(r.srv.URL, write.Secret), ctl.MCPOptions{})
	if names := tools(cautious); slices.Contains(names, "approve_plan") || !slices.Contains(names, "create_plan") {
		t.Fatalf("tools %v", names)
	}
	schema, err := cautious.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: ctl.SchemaURI})
	if err != nil || !strings.Contains(schema.Contents[0].Text, `"gateways"`) {
		t.Fatalf("schema resource: %v", err)
	}

	var projects struct {
		Projects []lpclient.Project `json:"projects"`
	}
	call(t, cautious, "list_projects", nil, &projects)
	if len(projects.Projects) != 1 {
		t.Fatalf("projects %+v", projects)
	}
	// A manifest that doesn't parse comes back as a tool error the agent
	// can read and fix.
	if res := call(t, cautious, "save_manifest", map[string]any{"project": "mono", "yaml": "version: 2\n"}, nil); !res.IsError {
		t.Fatal("an invalid manifest was saved")
	}
	call(t, cautious, "save_manifest", map[string]any{"project": "mono", "yaml": docsOnStaticHost}, nil)
	var plan lpclient.Plan
	call(t, cautious, "create_plan", map[string]any{"project": "mono", "environment": "production"}, &plan)
	if plan.Status != "ready" || !plan.Executable || len(plan.Sources) != 1 || plan.Sources[0].Alias != "mono" || !plan.Sources[0].InScope {
		t.Fatalf("plan %+v", plan)
	}
	// A scope names repositories by alias; one the project doesn't have is
	// an error the agent reads.
	if res := call(t, cautious, "create_plan", map[string]any{"project": "mono", "environment": "production", "scope": []string{"nope"}}, nil); !res.IsError {
		t.Fatal("planned a scope the project doesn't have")
	}

	// A read-only key reads, and can't plan.
	reader := agent(t, lpclient.New(r.srv.URL, read.Secret), ctl.MCPOptions{AllowDeploy: true})
	if res := call(t, reader, "create_plan", map[string]any{"project": "mono", "environment": "production"}, nil); !res.IsError {
		t.Fatal("a read-only key planned")
	}
	if res := call(t, reader, "approve_plan", map[string]any{"plan_id": plan.ID, "revision": plan.Revision}, nil); !res.IsError {
		t.Fatal("a read-only key approved")
	}

	// With --allow-deploy and a write key, it approves the revision it read.
	deployer := agent(t, lpclient.New(r.srv.URL, write.Secret), ctl.MCPOptions{AllowDeploy: true})
	if res := call(t, deployer, "approve_plan", map[string]any{"plan_id": plan.ID}, nil); !res.IsError {
		t.Fatal("approved without naming a revision")
	}
	var approved lpclient.Plan
	call(t, deployer, "approve_plan", map[string]any{"plan_id": plan.ID, "revision": plan.Revision, "note": "agent"}, &approved)
	if approved.Status != "approved" {
		t.Fatalf("approved %+v", approved)
	}
	deadline := time.Now().Add(readyTimeout)
	var d lpclient.Deployment
	for d.Status != "succeeded" {
		if time.Now().After(deadline) {
			t.Fatalf("deployment %+v", d)
		}
		time.Sleep(100 * time.Millisecond)
		call(t, deployer, "deployment_for_plan", map[string]any{"plan_id": plan.ID}, &d)
		if d.Status == "failed" {
			t.Fatalf("deployment failed %+v", d)
		}
	}
	var status struct {
		Environments []struct {
			Environment string             `json:"environment"`
			Bindings    []lpclient.Binding `json:"bindings"`
		} `json:"environments"`
	}
	call(t, reader, "status", map[string]any{"project": "mono"}, &status)
	found := false
	for _, e := range status.Environments {
		for _, b := range e.Bindings {
			found = found || (e.Environment == "production" && b.Name == "docs" && b.Provider == "static-host")
		}
	}
	if !found {
		t.Fatalf("status %+v", status)
	}
}
