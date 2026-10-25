//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/platform"
	"github.com/xraph/loomplane/internal/recovery"
)

// Google responses are fixtures. Planning, approval, Dispatch, Vault, source SQL
// authentication and restart persistence use the installed platform.
func TestCloudSQLPSCSourceAndApplicationThroughInstalledPlatform(t *testing.T) {
	sqlDSN := freshDatabase(t)
	admin, err := pgx.Connect(t.Context(), sqlDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP ROLE IF EXISTS app")
		_ = admin.Close(context.Background())
	})
	pgcfg := admin.Config().Copy()
	pgcfg.Database = "pscit_" + pgcfg.Database[len(pgcfg.Database)-8:]
	if _, createErr := admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{pgcfg.Database}.Sanitize()); createErr != nil {
		t.Fatal(createErr)
	}
	t.Cleanup(func() {
		if _, dropErr := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{pgcfg.Database}.Sanitize()+" WITH (FORCE)"); dropErr != nil {
			t.Error(dropErr)
		}
	})
	resource := strings.ReplaceAll(pgcfg.Database, "_", "-")
	google := newFakeGoogle(t)
	cloud := newFakeCloud(google.srv.URL)
	const network = "projects/acme-prod/global/networks/apps"
	const subnet = "projects/acme-prod/regions/us-central1/subnetworks/run"
	const endpoint = "projects/acme-prod/regions/us-central1/forwardingRules/database"
	const attachment = "projects/sql-tenant/regions/us-central1/serviceAttachments/source"
	var mu sync.Mutex
	writes := map[string]int{}
	google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/compute/v1/" + endpoint:
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "compute#forwardingRule", "id": "456", "name": "database", "selfLink": endpoint, "region": "projects/acme-prod/regions/us-central1", "network": network, "target": attachment, "IPAddress": "10.20.0.8", "pscConnectionStatus": "ACCEPTED"})
			return
		case "/compute/v1/" + subnet:
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "compute#subnetwork", "id": "123", "name": "run", "selfLink": subnet, "region": "projects/acme-prod/regions/us-central1", "network": network, "ipCidrRange": "10.20.0.0/26", "purpose": "PRIVATE", "stackType": "IPV4_ONLY", "state": "READY"})
			return
		}
		record := httptest.NewRecorder()
		cloud.ServeHTTP(record, r)
		if record.Code < 300 && strings.HasPrefix(r.URL.Path, "/v1/projects/acme-prod/instances") {
			if r.Method == http.MethodPost {
				stage := "instance"
				if strings.HasSuffix(r.URL.Path, "/databases") {
					stage = "database"
				}
				if strings.HasSuffix(r.URL.Path, "/users") {
					stage = "user"
					instance := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/projects/acme-prod/instances/"), "/users")
					cloud.mu.Lock()
					password := cloud.passwords[instance]
					cloud.mu.Unlock()
					if _, sqlErr := admin.Exec(t.Context(), "SELECT set_config('loomplane.test_password',$1,false)", password); sqlErr != nil {
						t.Error(sqlErr)
					}
					if _, sqlErr := admin.Exec(t.Context(), `DO $test$ BEGIN CREATE ROLE app LOGIN; EXECUTE format('ALTER ROLE app PASSWORD %L',current_setting('loomplane.test_password')); END $test$`); sqlErr != nil {
						t.Error(sqlErr)
					}
				}
				writes[stage]++
				cloud.mu.Lock()
				var instance string
				for name := range cloud.sql {
					instance = name
				}
				cloud.mu.Unlock()
				operation := map[string]string{"instance": "CREATE", "database": "CREATE_DATABASE", "user": "CREATE_USER"}[stage]
				_ = json.NewEncoder(w).Encode(map[string]any{"kind": "sql#operation", "name": stage, "status": "DONE", "targetProject": "acme-prod", "targetId": instance, "operationType": operation})
				return
			}
			if r.Method == http.MethodGet {
				var instance map[string]any
				if decodeErr := json.Unmarshal(record.Body.Bytes(), &instance); decodeErr != nil {
					t.Error(decodeErr)
				}
				instance["kind"], instance["pscServiceAttachmentLink"] = "sql#instance", attachment
				instance["dnsName"] = instance["name"].(string) + ".sql.goog."
				instance["settings"].(map[string]any)["settingsVersion"] = "1"
				_ = json.NewEncoder(w).Encode(instance)
				return
			}
		}
		w.WriteHeader(record.Code)
		_, _ = w.Write(record.Body.Bytes())
	})
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Orchestration.PollInterval = 50 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.GoogleEndpoint = google.srv.URL
	var transportReady atomic.Bool
	var factoryCalls atomic.Int32
	transports := &deployments.NativeSQLTransports{CloudSQLDNS: sourceFixtureDNS{}, CloudSQL: func(_ context.Context, _ *gcp.Session, region, instance string) (gcp.SQLConnector, error) {
		factoryCalls.Add(1)
		if region != "us-central1" || !strings.HasPrefix(instance, "lp-"+resource+"-") {
			return nil, errors.New("unexpected source transport identity")
		}
		return nativeFixtureConnector{dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			if !transportReady.Load() {
				return nil, errors.New("private fixture transport unavailable")
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(pgcfg.Host, fmt.Sprint(pgcfg.Port)))
		}}, nil
	}}
	startApp := func() *running {
		app, startErr := platform.New(cfg)
		if startErr != nil {
			t.Fatal(startErr)
		}
		if provideErr := forge.ProvideValue(app.Container(), transports); provideErr != nil {
			t.Fatal(provideErr)
		}
		lifetime, cancel := context.WithCancel(context.Background())
		if startErr = app.Start(lifetime); startErr != nil {
			cancel()
			t.Fatal(startErr)
		}
		return &running{t: t, srv: httptest.NewServer(app.Router().Handler()), app: app, cancel: cancel}
	}
	app := startApp()
	defer func() { app.stop() }()
	c := &client{t: t, base: app.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var project struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "PSC application", "repository_url": "https://github.com/acme/mono"}, &project)
	base := "/api/v1/projects/" + project.ID
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]string{"cloud": "gcp", "name": "PSC account", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, nil)
	sourceManifest := fmt.Sprintf(`version: 1
resources:
  %s:
    kind: postgres
    version: '17'
    provider: cloud-sql
    region: us-central1
    cloud_sql_psc:
      consumer_project: acme-prod
      network: %s
      forwarding_rule: %s
`, resource, network, endpoint)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": sourceManifest}, nil)
	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]any{"environment": "production", "catalog": true}, &plan)
	waitPlanned(t, c, &plan)
	if !plan.Executable || plan.Status != "ready" {
		t.Fatalf("source plan was not executable: %+v", plan)
	}
	var readKey keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "PSC read only", "access": "read"}, &readKey)
	denied := &client{t: t, base: c.base, key: c.key, token: readKey.Secret}
	denied.must(http.StatusForbidden, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
	d := approveAndWait(t, c, plan)
	if d.Status != "failed" || factoryCalls.Load() == 0 || !strings.Contains(d.Error, "authenticate the source login") {
		t.Fatalf("source did not reach installed SQL verification: status=%s error=%s calls=%d", d.Status, d.Error, factoryCalls.Load())
	}
	cloud.mu.Lock()
	premature := len(cloud.builds) + len(cloud.services) + len(cloud.secrets)
	cloud.mu.Unlock()
	if premature != 0 {
		t.Fatal("unverified source reached application")
	}
	app.stop()
	transportReady.Store(true)
	app = startApp()
	c.base = app.srv.URL
	c.must(http.StatusOK, http.MethodPost, "/api/v1/deployments/"+d.ID+"/retry", nil, &d)
	deadline := time.Now().Add(readyTimeout)
	for d.Status == "running" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+d.ID, nil, &d)
	}
	if d.Status != "succeeded" {
		t.Fatalf("source retry failed: %+v", d)
	}
	mu.Lock()
	for _, stage := range []string{"instance", "database", "user"} {
		if writes[stage] != 1 {
			t.Errorf("source retry duplicated %s: %d", stage, writes[stage])
		}
	}
	mu.Unlock()
	var sources recovery.NativeSourcesPage
	c.must(http.StatusOK, http.MethodGet, base+"/native-recovery-sources", nil, &sources)
	if len(sources.Sources) != 1 {
		t.Fatal("verified source was not retained for native recovery", len(sources.Sources))
	}
	analyze(t, cfg, c, project.ID)
	applicationManifest := sourceManifest + fmt.Sprintf(`services:
  api:
    source: go:services/api
    provider: cloud-run
    region: us-central1
    needs: [%s]
    cloud_sql_network:
      consumer_project: acme-prod
      network: %s
      subnetwork: %s
ignore:
  resources: [postgres, redis]
`, resource, network, subnet)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": applicationManifest}, nil)
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if !plan.Executable || plan.Status != "ready" {
		t.Fatalf("application plan was not executable: %+v", plan)
	}
	d = approveAndWait(t, c, plan)
	if d.Status != "succeeded" {
		t.Fatalf("PSC application failed: %+v", d)
	}
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	if len(cloud.services) != 1 || len(cloud.builds) != 1 {
		t.Fatal("unexpected application operations", len(cloud.services), len(cloud.builds))
	}
	var template map[string]any
	for _, service := range cloud.services {
		template = service["template"].(map[string]any)
	}
	encoded, err := json.Marshal(template)
	if err != nil || !strings.Contains(string(encoded), "--psc") || !strings.Contains(string(encoded), "/cloudsql-psc") || !strings.Contains(string(encoded), subnet) {
		t.Fatal("application lost reviewed PSC route", err)
	}
	found := false
	for _, versions := range cloud.secrets {
		for _, raw := range versions {
			credential, parseErr := url.Parse(string(raw))
			if parseErr != nil || credential.Scheme != "postgresql" {
				continue
			}
			if credential.Path != "/"+pgcfg.Database || !strings.HasPrefix(credential.Query().Get("host"), "/cloudsql-psc/acme-prod:us-central1:") {
				t.Fatal("application credential lost source or PSC socket")
			}
			password, present := credential.User.Password()
			if !present || password == "" || strings.Contains(string(encoded), password) {
				t.Fatal("application credential missing or exposed in template")
			}
			login := pgcfg.Copy()
			login.User, login.Password = credential.User.Username(), password
			db, loginErr := pgx.ConnectConfig(t.Context(), login)
			if loginErr != nil {
				t.Fatal("application credential cannot authenticate", loginErr)
			}
			_ = db.Close(t.Context())
			found = true
		}
	}
	if !found {
		t.Fatal("application did not receive source secret")
	}
}
