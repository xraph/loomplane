//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/deployments"
)

func TestCloudSQLDiscoversLostCreateWithoutReplayingIt(t *testing.T) {
	google := newFakeGoogle(t)
	job := checkpointSQLJob(t, google)
	instance := deployments.SQLInstanceName(job) + "-europe-west1"
	key, err := gcp.ParseKey(job.Credential)
	if err != nil {
		t.Fatal(err)
	}
	var saved []byte
	job.Checkpoint = func(raw []byte) error { saved = append([]byte(nil), raw...); return nil }
	created := false
	writes := map[string]int{}
	var settings map[string]any
	var queued time.Time
	operation := func() map[string]any {
		return map[string]any{"kind": "sql#operation", "name": "original-create", "targetId": instance, "targetProject": "acme-prod", "operationType": "CREATE", "status": "DONE", "user": key.ClientEmail, "insertTime": queued.Format(time.RFC3339Nano), "startTime": queued.Add(time.Millisecond).Format(time.RFC3339Nano), "endTime": queued.Add(3 * time.Millisecond).Format(time.RFC3339Nano)}
	}
	google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/projects/acme-prod/operations" {
			if r.Method != http.MethodGet || r.URL.Query().Get("instance") != instance || r.URL.Query().Get("location") != "europe-west1" {
				t.Error("unscoped operation discovery")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "sql#operationsList", "items": []any{operation()}})
			return
		}
		if r.URL.Path == "/v1/projects/acme-prod/operations/original-create" {
			_ = json.NewEncoder(w).Encode(operation())
			return
		}
		if r.Method == http.MethodGet {
			if !created {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			labels, _ := json.Marshal(settings)
			fmt.Fprintf(w, `{"settings":%s,"name":%q,"project":"acme-prod","region":"europe-west1","connectionName":%q,"createTime":%q,"state":"RUNNABLE","databaseVersion":"POSTGRES_17"}`, labels, instance, "acme-prod:europe-west1:"+instance, queued.Add(2*time.Millisecond).Format(time.RFC3339Nano))
			return
		}
		stage := "instance"
		if strings.HasSuffix(r.URL.Path, "/databases") {
			stage = "database"
		}
		if strings.HasSuffix(r.URL.Path, "/users") {
			stage = "user"
		}
		writes[stage]++
		if stage == "instance" {
			var body struct {
				Settings map[string]any `json:"settings"`
			}
			if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil {
				t.Error(decodeErr)
			}
			settings = body.Settings
			queued = time.Now().UTC()
			created = true
			// The write reached Google, but the response lost its operation identity.
			fmt.Fprint(w, `{"status":"PENDING"}`)
			return
		}
		var checkpoint struct {
			Proof *struct {
				Operation string `json:"operation"`
			} `json:"recovered_create"`
		}
		if decodeErr := json.Unmarshal(saved, &checkpoint); decodeErr != nil || checkpoint.Proof == nil || checkpoint.Proof.Operation != "original-create" {
			t.Errorf("setup preceded durable recovery proof: %s %v", saved, decodeErr)
		}
		fmt.Fprint(w, `{"name":"setup","status":"DONE"}`)
	})
	executor := &deployments.CloudSQL{Google: gcp.New(google.srv.URL)}
	if _, applyErr := executor.Apply(t.Context(), job); applyErr == nil {
		t.Fatal("lost create response did not interrupt original execution")
	}
	job.Resume = append([]byte(nil), saved...)
	out, err := executor.Apply(t.Context(), job)
	if err != nil {
		t.Fatalf("original create could not be discovered: %v", err)
	}
	if out.PhysicalID == "" || writes["instance"] != 1 || writes["database"] != 1 || writes["user"] != 1 {
		t.Fatalf("discovery replayed a mutation or lost identity: out=%+v writes=%v", out, writes)
	}
	var checkpoint struct {
		Stage string `json:"stage"`
		Proof *struct {
			Operation string `json:"operation"`
		} `json:"recovered_create"`
	}
	if err := json.Unmarshal(saved, &checkpoint); err != nil || checkpoint.Stage != "ready" || checkpoint.Proof == nil || checkpoint.Proof.Operation != "original-create" {
		t.Fatalf("ready checkpoint lost recovery evidence: %s %v", saved, err)
	}
}
