package deployments

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

func TestVercelUnpublishedProjectCheckpointAndCleanupRetry(t *testing.T) {
	var mu sync.Mutex
	exists, failDelete := false, true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v9/projects/api-production-project":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"missing"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v10/projects":
			exists = true
			_, _ = io.WriteString(w, `{"id":"unready-project","name":"api-production-project"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v13/deployments":
			_, _ = io.WriteString(w, `{"id":"unready-deployment"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v13/deployments/unready-deployment":
			_, _ = io.WriteString(w, `{"id":"unready-deployment","readyState":"ERROR","errorMessage":"build failed"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/v9/projects/unready-project":
			if failDelete {
				failDelete = false
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":{"code":"unavailable","message":"try again"}}`)
				return
			}
			exists = false
			w.WriteHeader(http.StatusNoContent)
		default:
			_, _ = io.WriteString(w, `[]`)
		}
	}))
	defer server.Close()
	archive := buildArchive(t, map[string]string{})
	var checkpoint []byte
	job := &Job{Deployment: &Deployment{OrgID: "org", ProjectID: "project", Environment: "production"}, Plan: &planning.Plan{ID: "forward", CloudSyncID: "sync"}, Action: planning.Action{Name: "api", Service: &planning.ServiceConfig{Source: "."}}, Connection: &connections.Connection{ID: "destination", Details: map[string]string{"team_id": "destination"}}, Credential: []byte("destination-token"), Progress: func(string, string) {}, Source: func(context.Context) (io.ReadCloser, int64, error) {
		return io.NopCloser(bytes.NewReader(archive)), int64(len(archive)), nil
	}, Checkpoint: func(raw []byte) error { checkpoint = append([]byte(nil), raw...); return nil }}
	executor := &Vercel{Client: vercel.New(server.URL)}
	outcome, err := executor.Apply(t.Context(), job)
	if err == nil || outcome != nil {
		t.Fatalf("readiness failure published an outcome: %+v %v", outcome, err)
	}
	var state vercelState
	if err = json.Unmarshal(checkpoint, &state); err != nil || state.ProjectID != "unready-project" || state.DeploymentID != "unready-deployment" {
		t.Fatalf("lost resource checkpoint: %+v %v", state, err)
	}
	job.Resume = checkpoint
	id, err := executor.PendingCloudResource(t.Context(), job)
	if err != nil || id != "unready-project" {
		t.Fatalf("pending identity: %s %v", id, err)
	}
	binding := &planning.Binding{ExternalID: id}
	if err = executor.Remove(t.Context(), job, binding); err == nil {
		t.Fatal("fixture cleanup failure lost")
	}
	mu.Lock()
	retained := exists
	mu.Unlock()
	if !retained {
		t.Fatal("failed cleanup forgot project")
	}
	if err = executor.Remove(t.Context(), job, binding); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	retained = exists
	mu.Unlock()
	if retained {
		t.Fatal("cleanup retry left project")
	}
	job.Resume = []byte(`{"deployment_id":"unknown-project"}`)
	if _, err = executor.PendingCloudResource(t.Context(), job); err == nil {
		t.Fatal("unknown resource identity guessed a cleanup target")
	}
}

type pendingCleanupAuth struct{}

func (pendingCleanupAuth) ValidateCloudSync(context.Context, *planning.Plan) error { return nil }
func (pendingCleanupAuth) ValidateCloudSyncCleanup(context.Context, *planning.Plan, string) error {
	return nil
}

type pendingArchive struct {
	fakeSources
	archive []byte
}

func (s *pendingArchive) Open(context.Context, string) (io.ReadCloser, int64, error) {
	return io.NopCloser(bytes.NewReader(s.archive)), int64(len(s.archive)), nil
}

func TestRevertCleanupProtectsServingAccountAliases(t *testing.T) {
	for _, tc := range []struct {
		name, originalTeam string
		deletes            int
		blocked            bool
	}{
		{"same verified account", "team-a", 0, false},
		{"distinct account colliding project ID", "team-b", 1, false},
		{"unverified original account", "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deletes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case req.Method == http.MethodDelete:
					deletes++
					w.WriteHeader(http.StatusNoContent)
				case req.Method == http.MethodPost && req.URL.Path == "/v13/deployments":
					_, _ = io.WriteString(w, `{"id":"failed-build"}`)
				case req.URL.Path == "/v13/deployments/failed-build":
					_, _ = io.WriteString(w, `{"id":"failed-build","readyState":"ERROR","errorMessage":"build failed"}`)
				default:
					_, _ = io.WriteString(w, `{"id":"serving-project","name":"web"}`)
				}
			}))
			defer server.Close()
			r, plan, plans, _, _ := cloudRunner(t)
			plan.CloudSyncID = "forward-sync"
			r.cloudSyncAuth = pendingCleanupAuth{}
			r.executors["vercel"] = &Vercel{Client: vercel.New(server.URL)}
			r.sources = &pendingArchive{archive: buildArchive(t, map[string]string{})}
			a := &plan.Actions[0]
			a.Op, a.Service.Env = planning.Replace, nil
			original := &connections.Connection{ID: "original", Cloud: "vercel", Status: connections.StatusReady, Details: map[string]string{"team_id": tc.originalTeam}}
			r.credentials.(targetCredentials)["original"] = original
			oldCfg := *a.Service
			oldCfg.CloudPlacement = nil
			raw, _ := json.Marshal(oldCfg)
			plans.bindings[a.ID] = &planning.Binding{ID: "serving", OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, Subject: a.Subject, Name: a.Name, Provider: a.Provider, ConnectionID: "original", ExternalID: "serving-project", Config: raw}
			id, _, err := r.Start(t.Context(), plan.OrgID, plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.FetchSource(t.Context(), plan.OrgID, id); err != nil {
				t.Fatal(err)
			}
			if err = r.Run(t.Context(), plan.OrgID, id, a.ID, nil, func([]byte) error { return nil }); err == nil || !strings.Contains(err.Error(), "build failed") {
				t.Fatalf("forward did not fail in provider build: %v", err)
			}
			err = r.CleanupRevertedCloudPlan(t.Context(), plan.OrgID, plan.ID, "compensation", func([]byte) error { return nil })
			if (err != nil) != tc.blocked {
				t.Fatalf("cleanup error %v, want blocked %v", err, tc.blocked)
			}
			if deletes != tc.deletes {
				t.Fatalf("provider deletes %d, want %d", deletes, tc.deletes)
			}
			if plans.bindings[a.ID].ConnectionID != "original" {
				t.Fatal("cleanup changed serving binding")
			}
		})
	}
}
