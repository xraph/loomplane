//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

// This exercises the persisted API, CLI and real cloud adapters against local
// provider endpoints. It does not qualify cloud storage or a native restore.
func TestManagedCatalogCLIProvisionsStandalonePostgres(t *testing.T) {
	previous := lpclient.Every
	lpclient.Every = 100 * time.Millisecond
	t.Cleanup(func() { lpclient.Every = previous })
	for _, provider := range []string{"rds", "cloud-sql"} {
		t.Run(provider, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Database.URL = freshDatabase(t)
			cfg.Orchestration.PollInterval = 100 * time.Millisecond
			cfg.Storage.URL = "file://" + t.TempDir()
			var credential, cloud, region string
			var settings map[string]string
			var resources func() int
			var checkProvisioned func()
			if provider == "rds" {
				fake := newFakeAWS(t)
				state := installFakeECS(t, fake)
				cfg.Clouds.AWSEndpoint = fake.srv.URL
				cloud, credential, region = "aws", awsKey(awsKeyID), "us-east-1"
				settings = map[string]string{"region": region}
				resources = func() int {
					state.mu.Lock()
					defer state.mu.Unlock()
					return len(state.dbs)
				}
				checkProvisioned = func() {
					state.mu.Lock()
					defer state.mu.Unlock()
					if len(state.dbs) != 1 || len(state.services) != 0 || len(state.builds) != 0 {
						t.Fatal("managed service created extra compute or database resources")
					}
					for _, request := range state.dbs {
						if request.Get("Engine") != "postgres" || request.Get("EngineVersion") != "17" || request.Get("PubliclyAccessible") != "false" || request.Get("StorageEncrypted") != "true" || request.Get("BackupRetentionPeriod") != "7" || request.Get("AllocatedStorage") != "20" || request.Get("StorageType") != "gp3" || request.Get("DBInstanceClass") != "db.t4g.micro" || request.Get("MultiAZ") != "false" {
							t.Fatal("managed PostgreSQL lost its engine, private storage or backup settings")
						}
					}
				}
			} else {
				fake := newFakeGoogle(t)
				state := newFakeCloud(fake.srv.URL)
				fake.deploy = state
				cfg.Clouds.GoogleEndpoint = fake.srv.URL
				cloud, credential, region = "gcp", serviceAccountKey(t, fake.srv.URL+"/token"), "europe-west1"
				resources = func() int {
					state.mu.Lock()
					defer state.mu.Unlock()
					return len(state.sql)
				}
				checkProvisioned = func() {
					state.mu.Lock()
					defer state.mu.Unlock()
					if len(state.sql) != 1 || len(state.services) != 0 || len(state.builds) != 0 {
						t.Fatal("managed service created extra compute or database resources")
					}
					for name, instance := range state.sql {
						if instance["databaseVersion"] != "POSTGRES_17" || len(state.databases[name]) != 1 || state.databases[name][0] != "database" || state.passwords[name] == "" {
							t.Fatal("managed PostgreSQL has no database, selected engine or application credential")
						}
						settings := instance["settings"].(map[string]any)
						backup := settings["backupConfiguration"].(map[string]any)
						retention, ok := backup["backupRetentionSettings"].(map[string]any)
						if !ok || settings["dataDiskSizeGb"] != "10" || settings["dataDiskType"] != "PD_SSD" || settings["availabilityType"] != "ZONAL" || settings["storageAutoResize"] != false || settings["tier"] != "db-custom-1-3840" || backup["pointInTimeRecoveryEnabled"] != true || backup["transactionLogRetentionDays"] != float64(7) || retention["retainedBackups"] != float64(7) || retention["retentionUnit"] != "COUNT" {
							t.Fatal("reviewed storage and recovery requests did not reach Cloud SQL")
						}
					}
				}
			}
			running := start(t, cfg)
			defer func() { running.stop() }()
			c := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
			signedIn(t, c)
			var selected connections.Connection
			for _, name := range []string{"Other account", "Selected account"} {
				c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": cloud, "name": name, "credential": credential, "settings": settings}, &selected)
			}
			var key keyBody
			c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "managed catalog", "access": "write"}, &key)
			lc := loomctl{t: t, url: running.srv.URL, key: key.Secret, config: filepath.Join(t.TempDir(), "loomctl.json")}
			var response lpclient.Plan
			if err := json.Unmarshal([]byte(lc.must("services", "create", "Managed orders", "--provider", provider, "--target", selected.ID, "--region", region, "--json")), &response); err != nil {
				t.Fatal(err)
			}
			var plan planning.Plan
			c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+response.ID, nil, &plan)
			if !plan.Catalog || !plan.Executable || plan.Status != planning.StatusReady || len(plan.Sources) != 0 || len(plan.Actions) != 1 || resources() != 0 {
				t.Fatalf("managed plan is not repository-free or was applied without review: status=%s executable=%v sources=%d actions=%d", plan.Status, plan.Executable, len(plan.Sources), len(plan.Actions))
			}
			action := plan.Actions[0]
			if !response.Catalog || response.Connections[cloud] != selected.ID || plan.Connections[cloud] != selected.ID || action.Provider != provider || action.Region != region || action.Op != planning.Create || action.Resource == nil || action.Resource.Kind != "postgres" || action.Resource.Version != "17" || action.Resource.Target != "" || action.Resource.Catalog != nil {
				t.Fatalf("managed plan lost its selected placement or inherited container assumptions: catalog=%v selected=%s response=%v connections=%v action=%+v resource=%+v", response.Catalog, selected.ID, response.Connections, plan.Connections, action, action.Resource)
			}
			profile, err := manageddb.Postgres(provider, "17")
			if err != nil {
				t.Fatal(err)
			}
			if action.Resource.Managed == nil || action.Resource.Managed.Profile != *profile || action.Resource.Managed.ConnectionID != selected.ID || action.Resource.Managed.AccountIdentity == "" {
				t.Fatal("managed plan has no immutable reviewed profile and account identity")
			}
			var checked connections.Connection
			c.must(http.StatusOK, http.MethodGet, "/api/v1/connections/"+selected.ID, nil, &checked)
			if selected.AccountIdentity == "" || checked.AccountIdentity != selected.AccountIdentity || checked.AccountIdentity != action.Resource.Managed.AccountIdentity {
				t.Fatal("the public connection identity does not match the reviewed managed plan")
			}
			readChoice := managedCatalogConnectionReader(c, plan.ProjectID, cloud)
			if readChoice() != selected.ID {
				t.Fatal("managed plan did not save the selected account")
			}
			lc.must("approve", plan.ID, "--yes")
			checkProvisioned()
			var bindings struct{ Bindings []planning.Binding }
			c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+plan.ProjectID+"/bindings?environment=production", nil, &bindings)
			if len(bindings.Bindings) != 1 || bindings.Bindings[0].Provider != provider || bindings.Bindings[0].ConnectionID != selected.ID || bindings.Bindings[0].ExternalID == "" {
				t.Fatal("managed database has no scoped persisted binding")
			}
			var saved planning.ResourceConfig
			if err := json.Unmarshal(bindings.Bindings[0].Config, &saved); err != nil || !reflect.DeepEqual(saved.Managed, action.Resource.Managed) {
				t.Fatal("deployed binding lost its reviewed request profile")
			}
			if provider == "cloud-sql" && !strings.Contains(bindings.Bindings[0].ExternalID, ":"+region+":") {
				t.Fatal("Cloud SQL's returned physical location does not match the selected region")
			}
			running.stop()
			running = start(t, cfg)
			c.base, lc.url = running.srv.URL, running.srv.URL
			if readChoice() != selected.ID {
				t.Fatal("the selected cloud account did not survive control-plane restart")
			}
			// The container recovery adapter must not claim this managed service.
			c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/projects/"+plan.ProjectID+"/service-backups", map[string]string{"environment": "production", "resource": "database", "request_id": "managed-backup-not-qualified"}, nil)
			var again planBody
			c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+plan.ProjectID+"/plans", map[string]any{"environment": "production", "catalog": true}, &again)
			waitPlanned(t, c, &again)
			if !again.Executable || len(again.Actions) != 1 || again.Actions[0].Op != "keep" || resources() != 1 {
				t.Fatal("managed service reconciliation proposed another database")
			}
			var reviewedAgain planning.Plan
			c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+again.ID, nil, &reviewedAgain)
			if !reflect.DeepEqual(reviewedAgain.Actions[0].Resource.Managed, saved.Managed) {
				t.Fatal("reviewed profile changed after control-plane restart")
			}
			if provider == "rds" {
				var misplaced lpclient.Plan
				if err := json.Unmarshal([]byte(lc.must("services", "create", "Wrong region", "--provider", provider, "--target", selected.ID, "--region", "us-west-2", "--json")), &misplaced); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, problem := range misplaced.Problems {
					found = found || problem.Code == "connection-region"
				}
				if misplaced.Executable || !found || resources() != 1 {
					t.Fatal("an RDS region mismatch was executable or mutated the provider")
				}
			}
			holdCatalogBrowser(t, running.srv.URL, plan.ProjectID, selected.ID)
		})
	}
}

func managedCatalogConnectionReader(c *client, project, cloud string) func() string {
	return func() string {
		var choices struct{ Connections map[string]string }
		c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project+"/environments/production/connections", nil, &choices)
		return choices.Connections[cloud]
	}
}

func TestManagedCatalogAccountReadDoesNotReusePreviousMapping(t *testing.T) {
	for _, missing := range []string{`{"connections":{}}`, `{}`, `{"connections":{"aws":"other"}}`} {
		t.Run(missing, func(t *testing.T) {
			reads := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/projects/project/environments/production/connections" {
					t.Errorf("unscoped placement read: %s %s", r.Method, r.URL)
				}
				reads++
				body := `{"connections":{"gcp":"selected"}}`
				if reads > 1 {
					body = missing
				}
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			read := managedCatalogConnectionReader(&client{t: t, base: srv.URL}, "project", "gcp")
			if read() != "selected" {
				t.Fatal("fixture has no original placement")
			}
			if got := read(); got != "" {
				t.Fatalf("an absent selection reused stale placement %q", got)
			}
		})
	}
}
