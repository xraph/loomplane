//go:build integration

package integration

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/azure/azuretest"
	"github.com/xraph/loomplane/internal/planning"
	apiclient "github.com/xraph/loomplane/pkg/client"
)

// TestDeploy_anAPIOnAzureContainerAppsThroughACloudTarget connects an
// Azure subscription, puts it in a Cloud as a target, and deploys the
// API's Dockerfile through it: built with ACR Tasks from the approved
// commit, and run as a public container app in the target's region.
func TestDeploy_anAPIOnAzureContainerAppsThroughACloudTarget(t *testing.T) {
	az := azuretest.New(t)
	cfg, r, c, _, project := cloudAPISetup(t)
	r.stop()
	cfg.Clouds.AzureEndpoint = az.URL()
	r = start(t, cfg)
	defer func() { r.stop() }()
	c.base = r.srv.URL
	base := "/api/v1/projects/" + project
	analyze(t, cfg, c, project)

	// Azure is a cloud you can connect, with the principal's IDs as
	// settings, and a target you can deploy to.
	var forms struct {
		Clouds []struct {
			Cloud    string `json:"cloud"`
			Settings []struct {
				Key      string `json:"key"`
				Required bool   `json:"required"`
			} `json:"settings"`
		} `json:"clouds"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/connection-clouds", nil, &forms)
	var required []string
	for _, f := range forms.Clouds {
		if f.Cloud == "azure" {
			for _, s := range f.Settings {
				if s.Required {
					required = append(required, s.Key)
				}
			}
		}
	}
	if !slices.Equal(required, []string{"tenant_id", "client_id", "subscription_id"}) {
		t.Fatalf("azure's form asks for %v", required)
	}
	var catalog struct {
		Providers []struct {
			Name      string   `json:"name"`
			Cloud     string   `json:"cloud"`
			Regions   []string `json:"regions"`
			Available bool     `json:"available"`
		} `json:"providers"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/providers", nil, &catalog)
	found := false
	for _, p := range catalog.Providers {
		if p.Name == "azure-container-apps" {
			found = p.Cloud == "azure" && p.Available && slices.Contains(p.Regions, "westeurope")
		}
	}
	if !found {
		t.Fatalf("providers %+v", catalog.Providers)
	}

	// A wrong secret is refused; a reader is incomplete, with what it
	// lacks; a contributor is ready.
	connect := func(secret string) (int, map[string]any) {
		var out map[string]any
		status := c.do(http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "azure", "name": "Azure " + secret[:4], "credential": secret, "settings": azuretest.Settings()}, &out)
		return status, out
	}
	if status, _ := connect("wrong-secret"); status != http.StatusUnprocessableEntity {
		t.Fatalf("a wrong secret: %d", status)
	}
	az.Lock()
	az.Actions, az.NotActions = []string{"*/read"}, nil
	az.Unlock()
	var conn struct {
		ID      string            `json:"id"`
		Status  string            `json:"status"`
		Account string            `json:"account"`
		Missing []string          `json:"missing"`
		Details map[string]string `json:"details"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "azure", "name": "Azure", "credential": azuretest.Secret, "settings": azuretest.Settings()}, &conn)
	if conn.Status != "incomplete" || !slices.Contains(conn.Missing, "Microsoft.App/containerApps/write") || slices.Contains(conn.Missing, "Microsoft.App/containerApps/read") {
		t.Fatalf("reader %+v", conn)
	}
	az.Lock()
	az.Actions, az.NotActions = []string{"*"}, []string{"Microsoft.Authorization/*/Write"}
	az.Unlock()
	c.must(http.StatusOK, http.MethodPost, "/api/v1/connections/"+conn.ID+"/check", nil, &conn)
	if conn.Status != "ready" || conn.Account != "Acme production" || len(conn.Missing) != 0 || conn.Details["subscription_id"] != azuretest.Subscription || conn.Details["resource_group"] != "loomplane" {
		t.Fatalf("contributor %+v", conn)
	}

	// A Cloud whose one target is the subscription, in West Europe.
	var cloud cloudprofiles.Profile
	c.must(http.StatusCreated, http.MethodPost, base+"/clouds", cloudprofiles.CreateInput{Name: "Production"}, &cloud)
	var target cloudprofiles.Target
	c.must(http.StatusCreated, http.MethodPost, base+"/clouds/"+cloud.ID+"/targets",
		cloudprofiles.CreateTargetInput{Name: "backend", Provider: "azure-container-apps", ConnectionID: conn.ID, Region: "westeurope"}, &target)
	var envs struct{ Environments []struct{ ID, Name string } }
	c.must(http.StatusOK, http.MethodGet, base+"/environments", nil, &envs)
	env := ""
	for _, e := range envs.Environments {
		if e.Name == "production" {
			env = e.ID
		}
	}
	var preview cloudprofiles.Preview
	c.must(http.StatusOK, http.MethodPost, base+"/environments/"+env+"/cloud/preview", apiclient.EnvironmentCloudInput{CloudID: cloud.ID}, &preview)
	if got := waitCloudAPI(t, c, base, applyCloudAPI(t, c, base, &preview, "assign-azure").ID); got.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("assignment %+v", got)
	}

	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": "version: 1\nservices:\n  api:\n    source: go:services/api\n    cloud_target: backend\n  gateway:\n    source: apps/gateway\n    needs: [api]\n    cloud_target: backend\nignore:\n  resources: [postgres, redis, kafka]\n"}, nil)
	c.must(http.StatusCreated, http.MethodPost, base+"/variables", map[string]any{"key": "API_TOKEN", "value": "kept-in-azure", "secret": true, "environment": "production"}, nil)
	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if !plan.Executable || len(plan.Actions) != 2 || plan.Actions[0].Provider != "azure-container-apps" || plan.Actions[1].Provider != "azure-container-apps" {
		t.Fatalf("plan %+v", plan)
	}
	if d := approveAndWait(t, c, plan); d.Status != "succeeded" {
		t.Fatalf("deployment %+v", d)
	}
	var bound struct{ Bindings []planning.Binding }
	c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &bound)
	if len(bound.Bindings) != 2 {
		t.Fatalf("bindings %+v", bound.Bindings)
	}
	var b, gw planning.Binding
	for _, got := range bound.Bindings {
		switch got.Name {
		case "api":
			b = got
		case "gateway":
			gw = got
		}
	}
	registry := azure.RegistryName(azuretest.Subscription, "loomplane")
	if b.ConnectionID != conn.ID || b.Region != "westeurope" || !strings.Contains(b.ExternalID, "/providers/Microsoft.App/containerApps/api-production-") ||
		!strings.HasPrefix(b.Revision, registry+".azurecr.io/api-") || !strings.HasPrefix(b.URL, az.URL()+"/apps/api-production-") {
		t.Fatalf("binding %+v", b)
	}

	if !az.Registered("Microsoft.App") || !az.Registered("Microsoft.ContainerRegistry") {
		t.Fatal("the resource providers weren't registered")
	}
	az.Lock()
	defer az.Unlock()
	// The API builds from its Dockerfile and the gateway, which has none,
	// with buildpacks.
	if len(az.Uploads) != 2 || len(az.Tasks) != 2 {
		t.Fatalf("uploads %d, tasks %v", len(az.Uploads), az.Tasks)
	}
	var dockerTasks, packTasks int
	for _, task := range az.Tasks {
		switch {
		case strings.Contains(task, "/services/api/Dockerfile"):
			dockerTasks++
		case strings.Contains(task, "cmd: gcr.io/k8s-skaffold/pack"):
			packTasks++
		}
	}
	if dockerTasks != 1 || packTasks != 1 {
		t.Fatalf("tasks %v", az.Tasks)
	}
	apps := map[string]map[string]any{}
	for name, a := range az.Apps {
		apps[strings.SplitN(name, "-production-", 2)[0]] = a
	}
	if len(apps) != 2 {
		t.Fatalf("apps %v", az.Apps)
	}
	api, gateway := apps["api"], apps["gateway"]
	props := api["properties"].(map[string]any)
	conf := props["configuration"].(map[string]any)
	if api["location"] != "westeurope" || conf["ingress"].(map[string]any)["external"] != true {
		t.Fatalf("app %v", api)
	}
	// The secret variable is an app secret, not a plain value.
	container := props["template"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	for _, e := range container["env"].([]any) {
		if v := e.(map[string]any); v["name"] == "API_TOKEN" && (v["value"] != nil || v["secretRef"] == nil) {
			t.Fatalf("API_TOKEN %v", v)
		}
	}
	// The gateway reaches the API at the address its binding recorded.
	var apiURL any
	gwContainer := gateway["properties"].(map[string]any)["template"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	for _, e := range gwContainer["env"].([]any) {
		if v := e.(map[string]any); v["name"] == "API_URL" {
			apiURL = v["value"]
		}
	}
	if apiURL != b.URL || gw.ConnectionID != conn.ID || !strings.Contains(gw.ExternalID, "/containerApps/gateway-production-") {
		t.Fatalf("gateway binding %+v, API_URL %v, want %s", gw, apiURL, b.URL)
	}
}
