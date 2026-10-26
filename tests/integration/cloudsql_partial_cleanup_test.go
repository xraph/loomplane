//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
)

func TestCloudSQLPartialCleanupReconcilesBeforeDeletingSource(t *testing.T) {
	for _, stop := range []string{"sql-create", "sql-user", "none", "address", "rule", "zone", "records", "rule-replaced-source", "rule-changed-proof", "rule-lease-refused", "rule-sql-checkpoint-lost"} {
		t.Run(stop, func(t *testing.T) { testCloudSQLPartialCleanup(t, stop) })
	}
}
func testCloudSQLPartialCleanup(t *testing.T, stop string) {
	scenario := stop
	if strings.HasPrefix(stop, "rule-") {
		stop = "rule"
	}
	google := newFakeGoogle(t)
	job := checkpointSQLJob(t, google)
	instance := deployments.SQLInstanceName(job) + "-europe-west1"
	profile, err := manageddb.CloudSQLPSCPostgres("17")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := manageddb.CloudSQLSourceEndpoint{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", ForwardingRule: "projects/app-project/regions/europe-west1/forwardingRules/database", ProvisionSubnetwork: "projects/app-project/regions/europe-west1/subnetworks/psc"}
	job.Action.Resource.Managed = &planning.ManagedDatabaseConfig{Profile: *profile, ConnectionID: job.Connection.ID, AccountIdentity: connections.AccountIdentity(job.Connection.Cloud, job.Connection.Account, job.Connection.Details), CloudSQLPSC: endpoint}
	network := newSourceNetworkFixture(t, endpoint, instance, "provision-lost-rule")
	if stop != "rule" {
		network.scenario = "normal"
	}
	created, removing, interrupted := false, false, false
	createTime := "2026-10-21T00:00:00Z"
	rejectRemoved := scenario == "rule-sql-checkpoint-lost"
	deletes, forgets, sqlWrites := 0, 0, 0
	var settings map[string]any
	var saved []byte
	job.Checkpoint = func(raw []byte) error {
		if removing && scenario == "rule-lease-refused" {
			return errors.New("lease refused")
		}
		var stage struct {
			Stage string `json:"stage"`
		}
		if decodeErr := json.Unmarshal(raw, &stage); decodeErr != nil {
			return decodeErr
		}
		if !removing && (stop == "sql-create" && stage.Stage == "database" || stop == "sql-user" && stage.Stage == "ready") {
			interrupted = true
			return errors.New("lost SQL advance checkpoint")
		}
		if removing && rejectRemoved && stage.Stage == "removed" {
			rejectRemoved = false
			return errors.New("lost SQL completion checkpoint")
		}
		var state struct {
			PSC struct {
				Provision gcp.SQLPSCProvisionState `json:"provision"`
			} `json:"psc"`
		}
		if decodeErr := json.Unmarshal(raw, &state); decodeErr != nil {
			return decodeErr
		}
		p := state.PSC.Provision
		if !removing && (stop == "none" && p.Address.Attempted || stop == "address" && p.ForwardingRule.Attempted || stop == "zone" && p.DNS.RecordRequest != "") {
			interrupted = true
			return errors.New("checkpoint unavailable")
		}
		saved = append([]byte(nil), raw...)
		return nil
	}
	network.checkpoint = func() []byte { return saved }
	job.Forget = func(context.Context) error { forgets++; return nil }
	google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if network.serve(w, r) {
			return
		}
		if r.Method == http.MethodGet {
			if strings.Contains(r.URL.Path, "/operations/") {
				op := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				kind := map[string]string{"create": "CREATE", "user": "UPDATE_USER", "remove": "DELETE"}[op]
				json.NewEncoder(w).Encode(map[string]any{"kind": "sql#operation", "name": op, "status": "DONE", "targetProject": "acme-prod", "targetId": instance, "operationType": kind})
				return
			}
			if !created {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"kind": "sql#instance", "name": instance, "project": "acme-prod", "region": "europe-west1", "createTime": createTime, "state": "RUNNABLE", "databaseVersion": "POSTGRES_17", "connectionName": "acme-prod:europe-west1:" + instance, "settings": settings, "pscServiceAttachmentLink": "projects/sql-tenant/regions/europe-west1/serviceAttachments/source", "dnsName": instance + ".sql.goog."})
			return
		}
		if r.Method != http.MethodDelete {
			sqlWrites++
		}
		op, kind := "create", "CREATE"
		switch {
		case r.Method == http.MethodDelete:
			if r.URL.Query().Get("enableFinalBackup") != "true" {
				t.Error("missing final backup")
			}
			created = false
			deletes++
			op, kind = "remove", "DELETE"
		case strings.HasSuffix(r.URL.Path, "/databases"):
			op, kind = "database", "CREATE_DATABASE"
		case strings.HasSuffix(r.URL.Path, "/users"):
			op, kind = "user", "CREATE_USER"
			if stop == "sql-user" {
				if r.Method == http.MethodPost {
					w.WriteHeader(http.StatusConflict)
					return
				}
				kind = "UPDATE_USER"
			}
		default:
			var body map[string]any
			if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil {
				t.Error(decodeErr)
			}
			settings = body["settings"].(map[string]any)
			settings["settingsVersion"] = "1"
			created = true
		}
		json.NewEncoder(w).Encode(map[string]any{"kind": "sql#operation", "name": op, "status": "DONE", "targetProject": "acme-prod", "targetId": instance, "operationType": kind})
	})
	exec := &deployments.CloudSQL{Google: gcp.New(google.srv.URL).WithDNSResolver(sourceFixtureDNS{}), PSCConnector: func(context.Context, *gcp.Session, string, string) (gcp.SQLConnector, error) {
		interrupted = true
		return nil, errors.New("transport unavailable")
	}}
	out, applyErr := exec.Apply(t.Context(), job)
	if applyErr == nil || out != nil {
		t.Fatal("partial apply unexpectedly succeeded", out, applyErr)
	}
	if !interrupted && !network.lost {
		t.Fatal("did not reach interruption", applyErr)
	}
	writesBeforeCleanup := sqlWrites
	removing = true
	job.Resume = saved
	if scenario == "rule-runner" {
		runPartialSQLCleanup(t, job, exec, network, saved, &deletes)
		return
	}
	if scenario == "rule-replaced-source" {
		createTime = "2026-10-22T00:00:00Z"
	}
	pending, err := exec.ResolvePendingCloudResource(t.Context(), job)
	if scenario == "rule-replaced-source" || scenario == "rule-lease-refused" {
		if err == nil || deletes != 0 || forgets != 0 {
			t.Fatal("unsafe pending recovery", err, deletes, forgets)
		}
		return
	}
	if err != nil {
		t.Fatal("resolve", err)
	}
	if pending.PhysicalID == "" || pending.ExternalID != "acme-prod:europe-west1:"+instance {
		t.Fatal("missing pending identity", pending)
	}
	// Partial ownership must still be rejected by the ordinary binding path.
	raw, _ := json.Marshal(job.Action.Resource)
	bound := &planning.Binding{Provider: "cloud-sql", ConnectionID: job.Connection.ID, Region: job.Action.Region, ExternalID: pending.ExternalID, PhysicalID: pending.PhysicalID, OwnedResources: pending.OwnedResources, Config: raw}
	job.Resume = nil
	if stop != "records" && stop != "sql-user" {
		if err = exec.Remove(t.Context(), job, bound); err == nil || deletes != 0 {
			t.Fatal("partial ownership entered ordinary removal", err, deletes)
		}
	}
	if scenario == "rule-changed-proof" {
		pending.PhysicalID = "replacement"
		if err = exec.RemovePendingCloudResource(t.Context(), job, pending); err == nil || deletes != 0 || forgets != 0 {
			t.Fatal("changed pending proof accepted", err, deletes, forgets)
		}
		return
	}
	// Lose a network deletion response after the source has already gone.
	if scenario == "rule" {
		network.scenario = "provision-remove-retry"
		network.lost = false
	}
	err = exec.RemovePendingCloudResource(t.Context(), job, pending)
	if scenario == "rule" || scenario == "rule-sql-checkpoint-lost" {
		if err == nil || deletes != 1 || forgets != 0 {
			t.Fatal("lost deletion did not retain cleanup", err, deletes, forgets)
		}
		job.Resume = saved
		exec = &deployments.CloudSQL{Google: gcp.New(google.srv.URL).WithDNSResolver(sourceFixtureDNS{})}
		err = exec.RemovePendingCloudResource(t.Context(), job, pending)
	}
	if err != nil || deletes != 1 || forgets != 1 || len(network.objects) != 0 || network.zone != nil {
		t.Fatal("partial cleanup", err, deletes, forgets, network.counts)
	}
	if sqlWrites != writesBeforeCleanup {
		t.Fatal("cleanup reissued SQL provisioning", sqlWrites, writesBeforeCleanup)
	}
	want := map[string]int{"address": 0, "rule": 0, "zone": 0, "records": 0}
	if stop != "none" && stop != "sql-create" {
		want["address"] = 1
	}
	if stop == "rule" || stop == "zone" || stop == "records" || stop == "sql-user" {
		want["rule"] = 1
	}
	if stop == "zone" || stop == "records" || stop == "sql-user" {
		want["zone"] = 1
	}
	if stop == "records" || stop == "sql-user" {
		want["records"] = 1
	}
	for kind, count := range want {
		if network.counts["insert-"+kind] != count || network.counts["delete-"+kind] != count {
			t.Fatal("cleanup created absent stages or leaked resources", kind, network.counts)
		}
	}
}
