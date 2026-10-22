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
// commit, and run as container apps in the target's region. The API is
// private, so it takes internal ingress and the gateway in the same
// environment reaches it at the address its binding recorded. The API
// needs Postgres, which azure-database makes as a flexible server, and
// gets its address as an app secret.
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

	// A Cloud whose targets are the subscription, in West Europe: one for
	// apps and one for databases.
	var cloud cloudprofiles.Profile
	c.must(http.StatusCreated, http.MethodPost, base+"/clouds", cloudprofiles.CreateInput{Name: "Production"}, &cloud)
	var target, data cloudprofiles.Target
	c.must(http.StatusCreated, http.MethodPost, base+"/clouds/"+cloud.ID+"/targets",
		cloudprofiles.CreateTargetInput{Name: "backend", Provider: "azure-container-apps", ConnectionID: conn.ID, Region: "westeurope"}, &target)
	c.must(http.StatusCreated, http.MethodPost, base+"/clouds/"+cloud.ID+"/targets",
		cloudprofiles.CreateTargetInput{Name: "data", Provider: "azure-database", ConnectionID: conn.ID, Region: "westeurope"}, &data)
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

	// A manifest places a resource by its provider, not a Cloud target, so
	// db names the data target's provider and region.
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": "version: 1\nservices:\n  api:\n    source: go:services/api\n    public: false\n    needs: [db]\n    cloud_target: backend\n  gateway:\n    source: apps/gateway\n    needs: [api]\n    cloud_target: backend\nresources:\n  db:\n    kind: postgres\n    version: \"16\"\n    provider: " + data.Provider + "\n    region: " + data.Region + "\nignore:\n  resources: [redis, kafka]\n"}, nil)
	c.must(http.StatusCreated, http.MethodPost, base+"/variables", map[string]any{"key": "API_TOKEN", "value": "kept-in-azure", "secret": true, "environment": "production"}, nil)
	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	providers := map[string]int{}
	for _, a := range plan.Actions {
		providers[a.Provider]++
	}
	if !plan.Executable || len(plan.Actions) != 3 || providers["azure-container-apps"] != 2 || providers["azure-database"] != 1 {
		t.Fatalf("plan %+v", plan)
	}
	if d := approveAndWait(t, c, plan); d.Status != "succeeded" {
		t.Fatalf("deployment %+v", d)
	}
	var bound struct{ Bindings []planning.Binding }
	c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &bound)
	if len(bound.Bindings) != 3 {
		t.Fatalf("bindings %+v", bound.Bindings)
	}
	var b, gw, db planning.Binding
	for _, got := range bound.Bindings {
		switch got.Name {
		case "api":
			b = got
		case "gateway":
			gw = got
		case "db":
			db = got
		}
	}
	if db.Provider != "azure-database" || db.Region != "westeurope" || !strings.Contains(db.ExternalID, "/providers/Microsoft.DBforPostgreSQL/flexibleServers/db-production-") {
		t.Fatalf("db binding %+v", db)
	}
	server := db.ExternalID[strings.LastIndex(db.ExternalID, "/")+1:]
	registry := azure.RegistryName(azuretest.Subscription, "loomplane")
	if b.ConnectionID != conn.ID || b.Region != "westeurope" || !strings.Contains(b.ExternalID, "/providers/Microsoft.App/containerApps/api-production-") ||
		!strings.HasPrefix(b.Revision, registry+".azurecr.io/api-") || !strings.HasPrefix(b.URL, az.URL()+"/apps/api-production-") {
		t.Fatalf("binding %+v", b)
	}

	if !az.Registered("Microsoft.App") || !az.Registered("Microsoft.ContainerRegistry") || !az.Registered("Microsoft.DBforPostgreSQL") {
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
	if api["location"] != "westeurope" || conf["ingress"].(map[string]any)["external"] != false {
		t.Fatalf("app %v", api)
	}
	// The secret variable and the database's address are app secrets,
	// not plain values.
	container := props["template"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	refs := map[string]string{}
	for _, e := range container["env"].([]any) {
		v := e.(map[string]any)
		if v["name"] == "API_TOKEN" || v["name"] == "DATABASE_URL" {
			if v["value"] != nil || v["secretRef"] == nil {
				t.Fatalf("%s %v", v["name"], v)
			}
			refs[v["name"].(string)] = v["secretRef"].(string)
		}
	}
	if len(refs) != 2 {
		t.Fatalf("env %v", container["env"])
	}
	// The address Loomplane kept, and gave the API, is the server's, over
	// TLS.
	var dbURL string
	for _, sec := range conf["secrets"].([]any) {
		if v := sec.(map[string]any); v["name"] == refs["DATABASE_URL"] {
			dbURL, _ = v["value"].(string)
		}
	}
	if !strings.HasPrefix(dbURL, "postgresql://loomplane:") || !strings.HasSuffix(dbURL, "@"+server+".postgres.database.azure.com:5432/postgres?sslmode=require") {
		t.Fatalf("DATABASE_URL %q", dbURL)
	}
	if len(az.ServerPuts) != 1 || az.FirewallRules[server+"/"+azure.AllowAzureServices] == nil {
		t.Fatalf("servers %d, firewall rules %v", len(az.ServerPuts), az.FirewallRules)
	}
	// The gateway reaches the private API at the address its binding
	// recorded, which is the API's internal ingress address.
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
