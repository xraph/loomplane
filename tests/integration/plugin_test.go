//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/examples/provider-plugin/statichost"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/plugins"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/pkg/provider/sdk"
)

const docsOnStaticHost = `version: 1
services:
  docs:
    source: apps/docs
    kind: static-site
    provider: static-host
ignore:
  resources: [postgres, redis]
`

func TestPlugin_aProviderPluginDeploysLikeABuiltInTarget(t *testing.T) {
	t.Run("progress", func(t *testing.T) { testPluginLifecycle(t, false) })
	t.Run("operation-output", func(t *testing.T) { testPluginLifecycle(t, true) })
}

type loggingStaticHost struct{ *statichost.Host }

func (loggingStaticHost) OperationLogs() bool { return true }
func (h loggingStaticHost) Apply(ctx context.Context, job *sdk.Job) (*sdk.Outcome, error) {
	job.Log("stdout", "Fixture build marker "+h.Token)
	job.Log("stdout", "Fixture build marker "+h.Token)
	return h.Host.Apply(ctx, job)
}
func testPluginLifecycle(t *testing.T, logs bool) {
	host := &statichost.Host{Dir: t.TempDir(), Token: "host-token-0123456789"}
	handler := host.Handler("plugin-token-0123456789")
	if logs {
		mux := http.NewServeMux()
		mux.Handle("/v1/", sdk.Handler(loggingStaticHost{host}, "plugin-token-0123456789"))
		mux.Handle("/", handler)
		handler = mux
	}
	plugin := httptest.NewServer(handler)
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
	base := "/api/v1/projects/" + proj.ID
	analyze(t, cfg, c, proj.ID)

	// The plugin's cloud can be connected, with the form it describes, and
	// it decides whether a credential works.
	var clouds struct {
		Clouds []struct {
			Cloud      string `json:"cloud"`
			Title      string `json:"title"`
			Builtin    bool   `json:"builtin"`
			Credential string `json:"credential"`
		} `json:"clouds"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/connection-clouds", nil, &clouds)
	found := false
	for _, cloud := range clouds.Clouds {
		if cloud.Cloud != "static-host" {
			continue
		}
		if cloud.Builtin || cloud.Credential != "Access token" {
			t.Fatalf("plugin form: %+v", cloud)
		}
		found = true
	}
	if !found {
		t.Fatalf("plugin missing from clouds: %+v", clouds.Clouds)
	}
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections",
		map[string]any{"cloud": "static-host", "name": "Sites", "credential": "wrong"}, nil)
	var conn struct {
		Status  string `json:"status"`
		Account string `json:"account"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]any{"cloud": "static-host", "name": "Sites", "credential": host.Token}, &conn)
	if conn.Status != "ready" || conn.Account != plugin.URL {
		t.Fatalf("connection %+v", conn)
	}

	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": docsOnStaticHost}, nil)
	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if plan.Status != "ready" || !plan.Executable || len(plan.Actions) != 1 || plan.Actions[0].Provider != "static-host" {
		t.Fatalf("plan %+v", plan)
	}
	d := approveAndWait(t, c, plan)
	if d.Status != "succeeded" {
		t.Fatalf("deployment %+v", d)
	}
	if logs {
		var retained realtime.LogPage
		c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+d.ID+"/logs", nil, &retained)
		if len(retained.Logs) != 2 || retained.Logs[0].ProviderID == retained.Logs[1].ProviderID {
			t.Fatalf("plugin output identities: %+v", retained)
		}
		for _, line := range retained.Logs {
			if strings.Contains(line.Message, host.Token) || !strings.Contains(line.Message, "Fixture build marker [redacted]") {
				t.Fatalf("unsafe provider output: %+v", line)
			}
		}
		first := retained.Logs[0]
		page, readErr := plugins.New(plugin.URL, "plugin-token-0123456789").OperationLogs(t.Context(), first.OperationID, "", 100)
		if readErr != nil || len(page.Lines) != 2 {
			t.Fatalf("plugin replay: %+v %v", page, readErr)
		}
		store, injectErr := forge.InjectType[*realtime.PgStore](r.app.(forge.App).Container())
		if injectErr != nil {
			t.Fatal(injectErr)
		}
		for _, line := range page.Lines {
			_, appendErr := store.AppendLog(t.Context(), realtime.LogInput{OrgID: first.OrgID, ProjectID: first.ProjectID, DeploymentID: first.DeploymentID, ActionID: first.ActionID, OperationID: first.OperationID, Attempt: first.Attempt, ProviderID: line.ID, Stream: line.Stream, Source: "provider", Level: "info", Message: line.Message, Time: line.Time}, realtime.NewRedactor([]string{host.Token}))
			if appendErr != nil {
				t.Fatal(appendErr)
			}
		}
		var replay realtime.LogPage
		c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+d.ID+"/logs", nil, &replay)
		if len(replay.Logs) != 2 {
			t.Fatalf("provider replay duplicated retained output: %+v", replay)
		}
		checkpoint, checkpointErr := store.Checkpoint(t.Context(), realtime.LogInput{OrgID: first.OrgID, DeploymentID: first.DeploymentID, ActionID: first.ActionID, OperationID: first.OperationID, Attempt: first.Attempt})
		if checkpointErr != nil || checkpoint.State != "complete" {
			t.Fatalf("collection: %+v %v", checkpoint, checkpointErr)
		}
	}
	site := d.Steps[0].URL
	if !strings.HasPrefix(site, plugin.URL+"/sites/"+proj.ID+"/production/docs/") {
		t.Fatalf("site at %q", site)
	}
	if page := get(t, site); !strings.Contains(page, `<div id="root">`) {
		t.Fatalf("the site serves %q", page)
	}

	// Nothing changed, so the next plan keeps it.
	var again planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &again)
	waitPlanned(t, c, &again)
	if len(again.Actions) != 1 || again.Actions[0].Op != "keep" {
		t.Fatalf("second plan %+v", again)
	}

	// Dropping it from the manifest removes it through the plugin.
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": "version: 1\nignore:\n  resources: [postgres, redis]\n"}, nil)
	var removal planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &removal)
	waitPlanned(t, c, &removal)
	if len(removal.Actions) != 1 || removal.Actions[0].Op != "delete" {
		t.Fatalf("removal plan %+v", removal)
	}
	if d := approveAndWait(t, c, removal); d.Status != "succeeded" {
		t.Fatalf("removal %+v", d)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, site, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("the removed site answers %d", res.StatusCode)
	}

	// Loomplane's own metrics count what happened, by outcome and provider.
	metrics := get(t, r.srv.URL+"/_/metrics")
	for _, want := range []string{
		`forge_loomplane_connection_checks_total{cloud="static-host",result="rejected"} 1`,
		`forge_loomplane_connection_checks_total{cloud="static-host",result="ready"} 1`,
		`forge_loomplane_plans_total{status="ready"} 3`,
		`forge_loomplane_plan_decisions_total{decision="approved"} 2`,
		`forge_loomplane_deployments_total{status="succeeded"} 2`,
		`forge_loomplane_deployment_steps_total{op="create",provider="static-host",status="succeeded"} 1`,
		`forge_loomplane_deployment_steps_total{op="delete",provider="static-host",status="succeeded"} 1`,
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
	if t.Failed() {
		for _, l := range strings.Split(metrics, "\n") {
			if strings.HasPrefix(l, "forge_loomplane_") && !strings.Contains(l, "_bucket") {
				t.Log(l)
			}
		}
	}
}
