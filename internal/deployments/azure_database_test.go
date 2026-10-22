package deployments

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/azure/azuretest"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// azureDatabaseJob is a job for a database on azure-database, whose kept
// credentials live in kept.
func azureDatabaseJob(kind, version string, kept map[string]string) *Job {
	return &Job{
		Deployment: &Deployment{OrgID: "org", ProjectID: "proj_0123456789", Environment: "production"},
		Plan:       &planning.Plan{ID: "plan_1"},
		Action: planning.Action{ID: "resource:db", Name: "db", Provider: "azure-database", Region: "westeurope",
			Resource: &planning.ResourceConfig{Provider: "azure-database", Kind: kind, Version: version, Region: "westeurope"}},
		Connection: &connections.Connection{ID: "conn_az", Cloud: connections.Azure, Details: azuretest.Settings()},
		Credential: []byte(azuretest.Secret),
		Checkpoint: func([]byte) error { return nil }, Progress: func(string, string) {},
		Keep: func(_ context.Context, field string, v []byte) error { kept[field] = string(v); return nil },
		Recall: func(_ context.Context, field string) ([]byte, error) {
			v, ok := kept[field]
			if !ok {
				return nil, ErrNoCredential
			}
			return []byte(v), nil
		},
		Forget: func(context.Context) error { clear(kept); return nil },
	}
}

func TestAzureDatabase_postgres(t *testing.T) {
	f := azuretest.New(t)
	exec := &AzureDatabase{Client: azure.New(f.URL())}
	kept := map[string]string{}
	job := azureDatabaseJob("postgres", "16", kept)
	var saved map[string]string
	job.Checkpoint = func(data []byte) error { return json.Unmarshal(data, &saved) }
	out, err := exec.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	name := AzureDatabaseName(job, azuretest.Subscription)
	if !strings.HasSuffix(out.ExternalID, "/resourceGroups/loomplane/providers/Microsoft.DBforPostgreSQL/flexibleServers/"+name) ||
		out.Revision != "postgres" || saved["server_id"] != out.ExternalID {
		t.Fatalf("outcome %+v, checkpoint %v", out, saved)
	}
	addr := kept[ResourceURL]
	if !strings.HasPrefix(addr, "postgresql://loomplane:") || !strings.HasSuffix(addr, ".postgres.database.azure.com:5432/postgres?sslmode=require") ||
		!strings.Contains(addr, ":"+kept[passwordField]+"@"+name+".") {
		t.Fatalf("address %q", addr)
	}
	if !f.Registered("Microsoft.DBforPostgreSQL") {
		t.Fatal("the postgres namespace wasn't registered")
	}

	f.Lock()
	defer f.Unlock()
	if len(f.ServerPuts) != 1 {
		t.Fatalf("puts %v", f.ServerPuts)
	}
	props := f.ServerPuts[0]["properties"].(map[string]any)
	sku := f.ServerPuts[0]["sku"].(map[string]any)
	if props["version"] != "16" || sku["name"] != "Standard_B1ms" || f.ServerPuts[0]["location"] != "westeurope" ||
		props["administratorLoginPassword"] != kept[passwordField] {
		t.Fatalf("server %v", f.ServerPuts[0])
	}
	if f.FirewallRules[name+"/AllowAllAzureServicesAndResourcesWithinAzureIps"] == nil {
		t.Fatalf("firewall rules %v", f.FirewallRules)
	}
	// The services executor's resources aren't touched.
	if len(f.Apps) != 0 {
		t.Fatalf("apps %v", f.Apps)
	}
	if id, perr := exec.PendingCloudResource(t.Context(), &Job{Resume: []byte(`{"server_id":"` + out.ExternalID + `"}`)}); perr != nil || id != out.ExternalID {
		t.Fatalf("pending %q, %v", id, perr)
	}
}

func TestAzureDatabase_mysql(t *testing.T) {
	f := azuretest.New(t)
	exec := &AzureDatabase{Client: azure.New(f.URL())}
	kept := map[string]string{}
	out, err := exec.Apply(t.Context(), azureDatabaseJob("mysql", "8.0.21", kept))
	if err != nil {
		t.Fatal(err)
	}
	addr := kept[ResourceURL]
	if !strings.Contains(out.ExternalID, "/providers/Microsoft.DBforMySQL/flexibleServers/") || out.Revision != "mysql" ||
		!strings.HasPrefix(addr, "mysql://loomplane:") || !strings.HasSuffix(addr, ".mysql.database.azure.com:3306/mysql?tls=true") {
		t.Fatalf("outcome %+v, address %q", out, addr)
	}
	f.Lock()
	defer f.Unlock()
	if props := f.ServerPuts[0]["properties"].(map[string]any); props["version"] != "8.0.21" {
		t.Fatalf("server %v", f.ServerPuts[0])
	}
}

func TestAzureDatabase_recallsThePassword(t *testing.T) {
	f := azuretest.New(t)
	exec := &AzureDatabase{Client: azure.New(f.URL())}
	kept := map[string]string{}
	job := azureDatabaseJob("postgres", "16", kept)
	out, err := exec.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	first, addr := kept[passwordField], kept[ResourceURL]
	if len(first) != 32 {
		t.Fatalf("password %q", first)
	}
	job.Binding = &planning.Binding{Provider: "azure-database", ExternalID: out.ExternalID, Region: "westeurope"}
	again, err := exec.Apply(t.Context(), job)
	if err != nil || again.ExternalID != out.ExternalID {
		t.Fatalf("%+v, %v", again, err)
	}
	if kept[passwordField] != first || kept[ResourceURL] != addr {
		t.Fatalf("kept %v, first %q %q", kept, first, addr)
	}
	f.Lock()
	defer f.Unlock()
	if len(f.ServerPuts) != 2 {
		t.Fatalf("puts %d", len(f.ServerPuts))
	}
	for _, put := range f.ServerPuts {
		if put["properties"].(map[string]any)["administratorLoginPassword"] != first {
			t.Fatalf("a PUT set another password: %v", put)
		}
	}
}

func TestAzureDatabase_removesWithItsData(t *testing.T) {
	f := azuretest.New(t)
	exec := &AzureDatabase{Client: azure.New(f.URL())}
	kept := map[string]string{}
	job := azureDatabaseJob("postgres", "16", kept)
	out, err := exec.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	b := &planning.Binding{Provider: "azure-database", ExternalID: out.ExternalID, Region: "westeurope"}
	obs, err := exec.Observe(t.Context(), job.Connection, job.Credential, b)
	if err != nil || !obs.Exists || obs.Revision != "postgres" {
		t.Fatalf("observe %+v, %v", obs, err)
	}
	forgot := false
	job.Forget = func(context.Context) error { forgot = true; return nil }
	if rerr := exec.Remove(t.Context(), job, b); rerr != nil || !forgot {
		t.Fatalf("remove %v, forgot %v", rerr, forgot)
	}
	f.Lock()
	deleted := slices.Clone(f.Deleted)
	f.Unlock()
	if !slices.Contains(deleted, AzureDatabaseName(job, azuretest.Subscription)) {
		t.Fatalf("deleted %v", deleted)
	}
	obs, err = exec.Observe(t.Context(), job.Connection, job.Credential, b)
	if err != nil || obs.Exists {
		t.Fatalf("observe after remove %+v, %v", obs, err)
	}
}

func TestAzureDatabaseName(t *testing.T) {
	valid := regexp.MustCompile(`^[a-z][a-z0-9-]{1,61}[a-z0-9]$`)
	short := azureDatabaseJob("postgres", "16", nil)
	long := azureDatabaseJob("postgres", "16", nil)
	long.Action.Name = "a-really-long-database-name-that-goes-on-and-on-and-on-and-on"
	long.Deployment.Environment = "preview-feature-branch-with-a-long-name"
	odd := azureDatabaseJob("postgres", "16", nil)
	odd.Action.Name = "9_Orders.DB"
	for _, job := range []*Job{short, long, odd} {
		name := AzureDatabaseName(job, azuretest.Subscription)
		if !valid.MatchString(name) {
			t.Errorf("%q isn't a valid flexible server name", name)
		}
	}
	if AzureDatabaseName(short, azuretest.Subscription) == AzureDatabaseName(short, "44444444-4444-4444-4444-444444444444") {
		t.Fatal("two subscriptions share a server name")
	}
	if a, b := AzureDatabaseName(short, azuretest.Subscription), AzureDatabaseName(short, strings.ToUpper(azuretest.Subscription)); a != b {
		t.Fatalf("the subscription's case changed the name: %q %q", a, b)
	}
}

func TestAzurePassword(t *testing.T) {
	allowed := regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)
	for range 200 {
		p, err := azurePassword()
		if err != nil {
			t.Fatal(err)
		}
		if !allowed.MatchString(p) || !strings.ContainsAny(p, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") ||
			!strings.ContainsAny(p, "abcdefghijklmnopqrstuvwxyz") || !strings.ContainsAny(p, "0123456789") {
			t.Fatalf("password %q", p)
		}
	}
}
