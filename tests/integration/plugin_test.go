//go:build integration

package integration

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/examples/provider-plugin/statichost"
	"github.com/xraph/loomplane/internal/config"
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
	if n := len(clouds.Clouds); n != 5 || clouds.Clouds[4].Cloud != "static-host" || clouds.Clouds[4].Builtin || clouds.Clouds[4].Credential != "Access token" {
		t.Fatalf("clouds %+v", clouds.Clouds)
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
