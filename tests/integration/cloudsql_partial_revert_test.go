//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/deployments"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/config"

	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
	apiclient "github.com/xraph/loomplane/pkg/client"
)

// Cloud switches review serving services only. A failed catalog resource must
// not become implicitly authorized cleanup through an unrelated Cloud revert.
// This uses the installed API, authorization, Vault, Dispatch and PostgreSQL;
// Google is an HTTP fixture, and no successful database login is claimed.
func TestInstalledCloudRevertDoesNotAdoptPartialSQLDeployment(t *testing.T) {
	google := newFakeGoogle(t)
	route := manageddb.CloudSQLSourceEndpoint{ConsumerProject: "acme-prod", Network: "projects/acme-prod/global/networks/apps", ForwardingRule: "projects/acme-prod/regions/us-central1/forwardingRules/database", ProvisionSubnetwork: "projects/acme-prod/regions/us-central1/subnetworks/psc"}
	network := newSourceNetworkFixture(t, route, "", "provision-lost-rule")
	var mu sync.Mutex
	cleanupStarted := false
	holdRecovery := true
	sqlDeleted := false
	var instance string
	var settings map[string]any
	writes := map[string]int{}
	google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// Keep the original deployment failed until an explicit recovery is accepted.
		if (holdRecovery || cleanupStarted) && network.lost && strings.HasSuffix(r.URL.Path, "/operations") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if network.serve(w, r) {
			return
		}
		if r.Method == http.MethodGet {
			if strings.Contains(r.URL.Path, "/operations/") {
				_ = json.NewEncoder(w).Encode(map[string]any{"kind": "sql#operation", "name": "remove", "status": "DONE", "targetProject": "acme-prod", "targetId": instance, "operationType": "DELETE"})
				return
			}
			if instance == "" || sqlDeleted {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "sql#instance", "name": instance, "project": "acme-prod", "region": "us-central1", "createTime": "2026-10-21T00:00:00Z", "state": "RUNNABLE", "databaseVersion": "POSTGRES_17", "connectionName": "acme-prod:us-central1:" + instance, "settings": settings, "pscServiceAttachmentLink": "projects/sql-tenant/regions/us-central1/serviceAttachments/source", "dnsName": instance + ".sql.goog."})
			return
		}
		writes[r.Method+" "+r.URL.Path]++
		if r.Method == http.MethodDelete {
			if r.URL.Query().Get("enableFinalBackup") != "true" || r.URL.Query().Get("finalBackupTtlDays") != "30" {
				t.Error("missing final backup policy")
			}
			sqlDeleted = true
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "sql#operation", "name": "remove", "status": "DONE", "targetProject": "acme-prod", "targetId": instance, "operationType": "DELETE"})
			return
		}
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/instances") {
			t.Error("unexpected SQL mutation", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body struct {
			Name     string         `json:"name"`
			Settings map[string]any `json:"settings"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		instance, settings = body.Name, body.Settings
		settings["settingsVersion"] = "1"
		network.instance = instance
		_ = json.NewEncoder(w).Encode(map[string]any{"kind": "sql#operation", "name": "create", "status": "DONE", "targetProject": "acme-prod", "targetId": instance, "operationType": "CREATE"})
	})
	cfg, app, c, org, project := cloudAPISetup(t, func(cfg *config.Config) {
		cfg.Clouds.GoogleEndpoint = google.srv.URL
		cfg.Orchestration.Concurrency = 1
		cfg.Orchestration.PollInterval = 250 * time.Millisecond
	})
	defer func() { app.stop() }()
	c.base = app.srv.URL
	base := "/api/v1/projects/" + project
	durable, err := pgxpool.New(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer durable.Close()
	var sourcePlan string
	var deploymentID string
	network.checkpoint = func() []byte {
		var raw []byte
		if cleanupStarted {
			if readErr := durable.QueryRow(context.Background(), "SELECT checkpoint FROM loomplane_failed_cleanups WHERE deployment_id=$1", deploymentID).Scan(&raw); readErr != nil {
				t.Error(readErr)
				return nil
			}
			var envelope struct {
				Apply  []byte `json:"apply"`
				Remove []byte `json:"remove"`
			}
			if decodeErr := json.Unmarshal(raw, &envelope); decodeErr != nil {
				t.Error(decodeErr)
				return nil
			}
			if len(envelope.Remove) > 0 {
				return envelope.Remove
			}
			return envelope.Apply
		}
		if readErr := durable.QueryRow(context.Background(), "SELECT steps FROM loomplane_deployments WHERE plan_id=$1", sourcePlan).Scan(&raw); readErr != nil {
			t.Error(readErr)
			return nil
		}
		var steps []struct {
			ActionID             string `json:"action_id"`
			Checkpoint           []byte `json:"checkpoint"`
			CloudCleanupComplete bool   `json:"cloud_cleanup_complete"`
		}
		if decodeErr := json.Unmarshal(raw, &steps); decodeErr != nil {
			t.Error(decodeErr)
			return nil
		}
		for _, step := range steps {
			if step.ActionID == "resource:database" {
				var envelope struct {
					Apply []byte `json:"apply"`
				}
				if json.Unmarshal(step.Checkpoint, &envelope) == nil && len(envelope.Apply) > 0 {
					return envelope.Apply
				}
				return step.Checkpoint
			}
		}
		t.Error("source checkpoint missing")
		return nil
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]string{"cloud": "gcp", "name": "Partial SQL account", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, nil)
	manifest := fmt.Sprintf("version: 1\nresources:\n  database:\n    kind: postgres\n    version: '17'\n    provider: cloud-sql\n    region: us-central1\n    cloud_sql_psc:\n      consumer_project: %s\n      network: %s\n      forwarding_rule: %s\n      provision_subnetwork: %s\n", route.ConsumerProject, route.Network, route.ForwardingRule, route.ProvisionSubnetwork)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": manifest}, nil)
	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]any{"environment": "production", "catalog": true}, &plan)
	waitPlanned(t, c, &plan)
	if !plan.Executable || len(plan.Actions) != 1 || plan.Actions[0].Provider != "cloud-sql" {
		t.Fatalf("SQL plan: %+v", plan)
	}
	mu.Lock()
	sourcePlan = plan.ID
	mu.Unlock()
	c.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
	var failed deploymentBody
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if c.do(http.MethodGet, "/api/v1/plans/"+plan.ID+"/deployment", nil, &failed) == http.StatusNotFound {
			failed.Status = ""
		}
		if failed.Status == "failed" || failed.Status == "succeeded" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	deploymentID = failed.ID
	mu.Unlock()
	if failed.Status != "failed" {
		t.Fatalf("expected partial deployment: %+v", failed)
	}
	mu.Lock()
	partial := network.lost && network.counts["insert-address"] == 1 && network.counts["insert-rule"] == 1 && network.counts["insert-zone"] == 0 && len(writes) == 1
	mu.Unlock()
	if !partial {
		t.Fatal("did not reach lost forwarding-rule response", failed.Error, writes, network.counts)
	}
	var readKey keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "Partial SQL read only", "access": "read"}, &readKey)
	denied := &client{t: t, base: c.base, key: c.key, token: readKey.Secret}
	var profile cloudprofiles.Profile
	c.must(http.StatusCreated, http.MethodPost, base+"/clouds", cloudprofiles.CreateInput{Name: "Unrelated placement"}, &profile)
	var preview cloudprofiles.Preview
	c.must(http.StatusOK, http.MethodPost, base+"/environments/"+plan.EnvironmentID+"/cloud/preview", apiclient.EnvironmentCloudInput{CloudID: profile.ID}, &preview)
	if len(preview.Blockers) != 0 || len(preview.Environments) != 1 || preview.Environments[0].Summary != (cloudprofiles.PlanSummary{}) {
		t.Fatalf("Cloud assignment imported partial SQL: %+v", preview)
	}
	assignment := applyCloudAPI(t, c, base, &preview, "assign-around-partial")
	if got := waitCloudAPI(t, c, base, assignment.ID); got.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("assignment: %+v", got)
	}
	denied.must(http.StatusForbidden, http.MethodPost, base+"/cloud-syncs/"+assignment.ID+"/revert/preview", nil, nil)
	c.must(http.StatusOK, http.MethodPost, base+"/cloud-syncs/"+assignment.ID+"/revert/preview", nil, &preview)
	if preview.Reverts != assignment.ID || len(preview.Blockers) != 0 || preview.Environments[0].Summary != (cloudprofiles.PlanSummary{}) {
		t.Fatalf("revert imported partial SQL: %+v", preview)
	}
	input := apiclient.ApplyCloudInput{PreviewID: preview.ID, Revision: preview.Revision, IdempotencyKey: "revert-around-partial"}
	denied.must(http.StatusForbidden, http.MethodPost, base+"/cloud-syncs/revert/apply", input, nil)
	var reverted cloudprofiles.Sync
	c.must(http.StatusAccepted, http.MethodPost, base+"/cloud-syncs/revert/apply", input, &reverted)
	if got := waitCloudAPI(t, c, base, reverted.ID); got.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("revert: %+v", got)
	}
	app.stop()
	app = start(t, cfg)
	c.base = app.srv.URL
	var bindings struct{ Bindings []planning.Binding }
	c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &bindings)
	if len(bindings.Bindings) != 0 {
		t.Fatal("partial SQL became serving")
	}
	var raw []byte
	var status string
	if err = durable.QueryRow(t.Context(), "SELECT status,steps FROM loomplane_deployments WHERE id=$1 AND org_id=$2", failed.ID, org).Scan(&status, &raw); err != nil {
		t.Fatal(err)
	}
	var steps []struct {
		ActionID             string `json:"action_id"`
		Checkpoint           []byte `json:"checkpoint"`
		CloudCleanupComplete bool   `json:"cloud_cleanup_complete"`
	}
	if err = json.Unmarshal(raw, &steps); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || len(steps) != 1 || steps[0].CloudCleanupComplete || len(steps[0].Checkpoint) == 0 {
		t.Fatal("unrelated revert claimed SQL cleanup", status, string(raw))
	}
	originalCheckpoint := append([]byte(nil), steps[0].Checkpoint...)
	var review deployments.CleanupReview
	denied.base = c.base
	cleanupURL := "/api/v1/deployments/" + failed.ID + "/cleanup"
	denied.must(http.StatusForbidden, http.MethodPost, cleanupURL+"/preview", nil, nil)
	c.must(http.StatusOK, http.MethodPost, cleanupURL+"/preview", nil, &review)
	if review.Revision == "" {
		t.Fatal("cleanup review lacks revision")
	}
	mu.Lock()
	if len(writes) != 1 || len(network.objects) != 2 || network.counts["delete-rule"] != 0 || network.counts["delete-address"] != 0 {
		t.Error("unrelated revert deleted partial SQL", writes, network.counts)
	}
	holdRecovery = false
	network.scenario = "provision-remove-retry"
	network.lost = false
	cleanupStarted = true
	mu.Unlock()
	denied.must(http.StatusForbidden, http.MethodPost, cleanupURL+"/apply", map[string]string{"revision": review.Revision}, nil)
	c.must(http.StatusConflict, http.MethodPost, cleanupURL+"/apply", map[string]string{"revision": "stale"}, nil)
	var cleanup deployments.FailedCleanup
	c.must(http.StatusAccepted, http.MethodPost, cleanupURL+"/apply", map[string]string{"revision": review.Revision}, &cleanup)
	if cleanup.Attempt != 1 {
		t.Fatal("first cleanup attempt", cleanup)
	}
	for _, query := range []string{
		"UPDATE loomplane_failed_cleanups SET actor='{}' WHERE deployment_id=$1",
		"UPDATE loomplane_deployments SET steps='[]' WHERE id=$1",
		"UPDATE loomplane_deployments SET status='running' WHERE id=$1",
	} {
		if _, guardErr := durable.Exec(t.Context(), query, failed.ID); guardErr == nil {
			t.Fatal("accepted cleanup evidence was mutable", query)
		}
	}

	c.must(http.StatusConflict, http.MethodPost, "/api/v1/deployments/"+failed.ID+"/retry", nil, nil)
	waitCleanup := func(want string) {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			c.must(http.StatusOK, http.MethodGet, cleanupURL, nil, &cleanup)
			if cleanup.Status == want {
				return
			}
			if cleanup.Status != "pending" {
				t.Fatalf("cleanup status %s, want %s: %s", cleanup.Status, want, cleanup.Error)
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("cleanup did not settle", cleanup)
	}
	waitCleanup("failed")
	mu.Lock()
	if !sqlDeleted || network.counts["delete-rule"] != 1 || network.counts["delete-address"] != 0 {
		t.Error("cleanup did not fail after deleting SQL and its rule", cleanup.Error, writes, network.counts)
	}
	mu.Unlock()
	app.stop()
	app = start(t, cfg)
	c.base = app.srv.URL
	denied.base = c.base
	denied.must(http.StatusForbidden, http.MethodPost, cleanupURL+"/retry", nil, nil)
	mu.Lock()
	network.lost = false
	network.scenario = ""
	mu.Unlock()
	c.must(http.StatusAccepted, http.MethodPost, cleanupURL+"/retry", nil, &cleanup)
	if cleanup.Attempt != 2 {
		t.Fatal("cleanup retry", cleanup)
	}
	waitCleanup("succeeded")
	c.must(http.StatusAccepted, http.MethodPost, cleanupURL+"/apply", map[string]string{"revision": review.Revision}, &cleanup)
	if cleanup.Status != "succeeded" || cleanup.Attempt != 2 {
		t.Fatal("completed replay", cleanup)
	}
	c.must(http.StatusConflict, http.MethodPost, "/api/v1/deployments/"+failed.ID+"/retry", nil, nil)
	c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &bindings)
	if len(bindings.Bindings) != 0 {
		t.Fatal("cleanup published a binding")
	}
	if err = durable.QueryRow(t.Context(), "SELECT steps FROM loomplane_deployments WHERE id=$1", failed.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &steps); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(originalCheckpoint, steps[0].Checkpoint) {
		t.Fatal("cleanup overwrote original creation evidence")
	}
	mu.Lock()
	defer mu.Unlock()
	for request, count := range writes {
		if count != 1 {
			t.Errorf("repeated SQL mutation %s: %d", request, count)
		}
	}
	if !sqlDeleted || len(writes) != 2 || len(network.objects) != 0 || network.counts["delete-rule"] != 1 || network.counts["delete-address"] != 1 || network.counts["request-delete-rule"] != 1 || network.counts["request-delete-address"] != 1 || network.counts["insert-zone"] != 0 {
		t.Fatal("incomplete or repeated cleanup", writes, network.counts)
	}
}
