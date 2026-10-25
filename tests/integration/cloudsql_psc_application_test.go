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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/platform"
	"github.com/xraph/loomplane/internal/recovery"
)

// Google responses are fixtures. Planning, approval, Dispatch, Vault, source SQL
// authentication and restart persistence use the installed platform.
func TestCloudSQLPSCSourceAndApplicationThroughInstalledPlatform(t *testing.T) {
	for _, mode := range []string{"manual", "automatic"} {
		t.Run(mode, func(t *testing.T) { testCloudSQLPSCInstalledWorkflow(t, mode == "automatic") })
	}
}

func testCloudSQLPSCInstalledWorkflow(t *testing.T, automatic bool) {
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
	ownedNetwork := newSourceNetworkFixture(t, manageddb.CloudSQLSourceEndpoint{ConsumerProject: "acme-prod", Network: network, ForwardingRule: endpoint, ProvisionSubnetwork: "projects/acme-prod/regions/us-central1/subnetworks/psc"}, "", "provision-lost-rule")
	var mu sync.Mutex
	var recoverNetwork atomic.Bool
	writes := map[string]int{}
	google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// Keep Dispatch retries at the uncertain operation until the next restart.
		if automatic && ownedNetwork.lost && !recoverNetwork.Load() && (strings.HasSuffix(r.URL.Path, "/operations") || ownedNetwork.scenario == "provision-remove-retry" && r.URL.Path == "/compute/v1/"+endpoint) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if automatic && r.URL.Path != "/compute/v1/"+subnet && ownedNetwork.serve(w, r) {
			return
		}
		switch r.URL.Path {
		case "/compute/v1/" + endpoint:
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "compute#forwardingRule", "id": "456", "name": "database", "selfLink": endpoint, "region": "projects/acme-prod/regions/us-central1", "network": network, "target": attachment, "IPAddress": "10.20.0.8", "pscConnectionStatus": "ACCEPTED"})
			return
		case "/compute/v1/" + subnet:
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "compute#subnetwork", "id": "123", "name": "run", "selfLink": subnet, "region": "projects/acme-prod/regions/us-central1", "network": network, "ipCidrRange": "10.20.0.0/26", "purpose": "PRIVATE", "stackType": "IPV4_ONLY", "state": "READY"})
			return
		}
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/projects/acme-prod/instances/") {
			writes["remove"]++
			if r.URL.Query().Get("enableFinalBackup") != "true" || r.URL.Query().Get("finalBackupTtlDays") != "30" {
				t.Error("missing protected final backup")
			}
		}
		record := httptest.NewRecorder()
		cloud.ServeHTTP(record, r)
		if record.Code < 300 && strings.HasPrefix(r.URL.Path, "/v1/projects/acme-prod/instances") {
			if r.Method == http.MethodDelete {
				instance := strings.TrimPrefix(r.URL.Path, "/v1/projects/acme-prod/instances/")
				_ = json.NewEncoder(w).Encode(map[string]any{"kind": "sql#operation", "name": "remove", "status": "DONE", "targetProject": "acme-prod", "targetId": instance, "operationType": "DELETE"})
				return
			}
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
	durable, err := pgxpool.New(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer durable.Close()
	var activePlan atomic.Value
	ownedNetwork.checkpoint = func() []byte {
		id, _ := activePlan.Load().(string)
		var raw []byte
		if err := durable.QueryRow(t.Context(), "SELECT steps FROM loomplane_deployments WHERE plan_id=$1", id).Scan(&raw); err != nil {
			t.Error("checkpoint unavailable before network mutation", err)
			return nil
		}
		var steps []struct {
			ActionID   string `json:"action_id"`
			Checkpoint []byte `json:"checkpoint"`
		}
		if err := json.Unmarshal(raw, &steps); err != nil {
			t.Error(err)
			return nil
		}
		for _, step := range steps {
			if step.ActionID != "resource:"+resource {
				continue
			}
			raw = step.Checkpoint
			var envelope struct {
				Replace bool   `json:"replace"`
				Apply   []byte `json:"apply"`
				Remove  []byte `json:"remove"`
			}
			if json.Unmarshal(raw, &envelope) == nil && envelope.Replace {
				raw = envelope.Apply
				if len(envelope.Remove) > 0 {
					raw = envelope.Remove
				}
			}
			return raw
		}
		t.Error("source checkpoint missing")
		return nil
	}
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
	if automatic {
		sourceManifest += "      provision_subnetwork: " + ownedNetwork.endpoint.ProvisionSubnetwork + "\n"
	}
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
	activePlan.Store(plan.ID)
	workflowID := plan.ID
	retries := map[string]int{}
	waitDeployment := func(path string) deploymentBody {
		t.Helper()
		var d deploymentBody
		deadline := time.Now().Add(3 * readyTimeout)
		for time.Now().Before(deadline) {
			if c.do(http.MethodGet, path, nil, &d) != http.StatusNotFound && d.Status != "" && d.Status != "running" {
				// The deployment row settles before Dispatch acknowledges its final activity.
				var settled bool
				if err := durable.QueryRow(t.Context(), "SELECT state = 'completed' FROM dispatch_executions WHERE namespace='loomplane' AND workflow_id=$1", workflowID).Scan(&settled); err != nil {
					t.Fatal(err)
				}
				if settled {
					return d
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("deployment did not finish: %+v", d)
		return d
	}
	approve := func() deploymentBody {
		workflowID = plan.ID
		c.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
		return waitDeployment("/api/v1/plans/" + plan.ID + "/deployment")
	}
	restartAndRetry := func(d deploymentBody) deploymentBody {
		app.stop()
		recoverNetwork.Store(true)
		app = startApp()
		c.base, denied.base = app.srv.URL, app.srv.URL
		denied.must(http.StatusForbidden, http.MethodPost, "/api/v1/deployments/"+d.ID+"/retry", nil, nil)
		retries[d.ID]++
		workflowID = fmt.Sprintf("%s.retry.%d", d.ID, retries[d.ID])
		c.must(http.StatusOK, http.MethodPost, "/api/v1/deployments/"+d.ID+"/retry", nil, &d)
		return waitDeployment("/api/v1/deployments/" + d.ID)
	}
	d := approve()
	if automatic {
		if d.Status != "failed" || factoryCalls.Load() != 0 {
			t.Fatal("lost endpoint response did not stop source creation", d.Status, d.Error)
		}
		func() {
			mu.Lock()
			defer mu.Unlock()
			if !ownedNetwork.lost || ownedNetwork.counts["insert-rule"] != 1 {
				t.Fatal("source failed before the intended lost endpoint response")
			}
		}()
		d = restartAndRetry(d)
	}
	if d.Status != "failed" || factoryCalls.Load() == 0 || !strings.Contains(d.Error, "authenticate the source login") {
		t.Fatalf("source did not reach installed SQL verification: status=%s error=%s calls=%d", d.Status, d.Error, factoryCalls.Load())
	}
	cloud.mu.Lock()
	premature := len(cloud.builds) + len(cloud.services) + len(cloud.secrets)
	cloud.mu.Unlock()
	if premature != 0 {
		t.Fatal("unverified source reached application")
	}
	var unpublished struct {
		Bindings []planning.Binding `json:"bindings"`
	}
	c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &unpublished)
	if len(unpublished.Bindings) != 0 {
		t.Fatal("source published a binding before SQL authentication")
	}
	transportReady.Store(true)
	d = restartAndRetry(d)
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
	var sourceOwned map[string]json.RawMessage
	if automatic {
		func() {
			mu.Lock()
			defer mu.Unlock()
			ownedNetwork.assertCreates()
		}()
		var bindings struct {
			Bindings []planning.Binding `json:"bindings"`
		}
		c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &bindings)
		if len(bindings.Bindings) != 1 || len(bindings.Bindings[0].OwnedResources["cloud_sql_psc"]) == 0 {
			t.Fatal("installed source lost ownership")
		}
		sourceOwned = bindings.Bindings[0].OwnedResources
		var kept []byte
		if err := durable.QueryRow(t.Context(), "SELECT owned_resources FROM loomplane_bindings WHERE id=$1", bindings.Bindings[0].ID).Scan(&kept); err != nil {
			t.Fatal(err)
		}
		if !bindingOwnershipJSONEqual(sourceOwned, kept) {
			t.Fatal("binding ownership not durable")
		}
	}
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
	activePlan.Store(plan.ID)
	d = approve()
	if d.Status != "succeeded" {
		t.Fatalf("PSC application failed: %+v", d)
	}
	found := false
	func() {
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
	}()
	if !found {
		t.Fatal("application did not receive source secret")
	}
	if automatic {
		func() {
			mu.Lock()
			defer mu.Unlock()
			ownedNetwork.assertCreates()
			ownedNetwork.scenario = "provision-remove-retry"
			ownedNetwork.lost = false
			recoverNetwork.Store(false)
		}()
		c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": apiOnCloudRun}, nil)
		c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
		waitPlanned(t, c, &plan)
		if !plan.Executable {
			t.Fatalf("removal not executable: %+v", plan)
		}
		activePlan.Store(plan.ID)
		d = approve()
		if d.Status != "failed" {
			t.Fatalf("lost cleanup response did not stop removal: %+v", d)
		}
		func() {
			mu.Lock()
			defer mu.Unlock()
			if writes["remove"] != 1 || !ownedNetwork.lost || ownedNetwork.counts["request-delete-rule"] != 1 {
				t.Fatal("cleanup failed before the intended lost deletion response")
			}
		}()
		var bindings struct {
			Bindings []planning.Binding `json:"bindings"`
		}
		c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &bindings)
		retained := false
		for _, b := range bindings.Bindings {
			if b.Name == resource {
				retained = true
				if string(b.OwnedResources["cloud_sql_psc"]) != string(sourceOwned["cloud_sql_psc"]) {
					t.Fatal("failed cleanup lost ownership")
				}
			}
		}
		if !retained {
			t.Fatal("failed cleanup dropped source binding")
		}
		d = restartAndRetry(d)
		if d.Status != "succeeded" {
			t.Fatalf("owned cleanup did not resume: %+v", d)
		}
		c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &bindings)
		if len(bindings.Bindings) != 1 || bindings.Bindings[0].Name != "api" || bindings.Bindings[0].Subject != planning.ServiceSubject {
			t.Fatal("completed source removal did not preserve only the application")
		}
		mu.Lock()
		defer mu.Unlock()
		ownedNetwork.assertRemoved()
		for _, kind := range []string{"rule", "address"} {
			if ownedNetwork.counts["request-delete-"+kind] != 1 {
				t.Fatal("Compute deletion request replayed", kind, ownedNetwork.counts)
			}
		}
		if writes["remove"] != 1 {
			t.Fatal("SQL removal replayed", writes)
		}
	}
}
