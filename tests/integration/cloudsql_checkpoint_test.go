//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
)

func checkpointSQLJob(t *testing.T, google *fakeGoogle) *deployments.Job {
	t.Helper()
	kept := map[string][]byte{}
	return &deployments.Job{
		Connection: &connections.Connection{ID: "selected", Cloud: "gcp", Account: "acme-prod", Status: "ready", Details: map[string]string{"project_id": "acme-prod"}},
		Credential: []byte(serviceAccountKey(t, google.srv.URL+"/token")),
		Deployment: &deployments.Deployment{ID: "deployment", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", Environment: "production"},
		Action:     planning.Action{ID: "resource:database", Name: "database", Provider: "cloud-sql", Region: "europe-west1", Resource: &planning.ResourceConfig{Provider: "cloud-sql", Kind: "postgres", Version: "17", Region: "europe-west1"}},
		Progress:   func(string, string) {},
		Recall: func(_ context.Context, field string) ([]byte, error) {
			v, ok := kept[field]
			if !ok {
				return nil, deployments.ErrNoCredential
			}
			return v, nil
		},
		Keep: func(_ context.Context, field string, v []byte) error {
			kept[field] = append([]byte(nil), v...)
			return nil
		},
	}
}

func TestCloudSQLExecutorResumesEverySavedMutation(t *testing.T) {
	for _, stop := range []string{"instance", "database", "user", "user-lost-password", "remove"} {
		t.Run(stop, func(t *testing.T) {
			google := newFakeGoogle(t)
			job := checkpointSQLJob(t, google)
			instance := deployments.SQLInstanceName(job) + "-europe-west1"
			created := stop == "remove"
			writes := map[string]int{}
			passwords := []string{}
			google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "/operations/") {
					op := strings.TrimPrefix(r.URL.Path, "/v1/projects/acme-prod/operations/")
					fmt.Fprintf(w, `{"name":%q,"status":"DONE","targetId":%q,"targetProject":"acme-prod"}`, op, instance)
					return
				}
				if r.Method == http.MethodGet {
					if !created {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					fmt.Fprintf(w, `{"name":%q,"connectionName":%q,"state":"RUNNABLE","databaseVersion":"POSTGRES_17"}`, instance, "acme-prod:europe-west1:"+instance)
					return
				}
				stage := "instance"
				switch {
				case r.Method == http.MethodDelete:
					stage = "remove"
				case strings.HasSuffix(r.URL.Path, "/databases"):
					stage = "database"
				case strings.HasSuffix(r.URL.Path, "/users"):
					stage = "user"
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					passwords = append(passwords, body["password"])
				}
				writes[stage]++
				if stage == "instance" {
					created = true
				}
				if stage == "remove" {
					if r.URL.Query().Get("enableFinalBackup") != "true" || r.URL.Query().Get("finalBackupTtlDays") != "30" {
						t.Error("retry weakened final backup")
					}
					created = false
				}
				fmt.Fprintf(w, `{"name":%q,"status":"PENDING","targetId":%q,"targetProject":"acme-prod"}`, stage, instance)
			})
			var saved []byte
			interrupted := errors.New("worker interrupted after persisted operation")
			pause := true
			job.Checkpoint = func(raw []byte) error {
				saved = append([]byte(nil), raw...)
				var state struct {
					Stage     string `json:"stage"`
					Operation string `json:"operation"`
				}
				if err := json.Unmarshal(raw, &state); err != nil {
					return err
				}
				if pause && state.Stage == strings.TrimSuffix(stop, "-lost-password") && state.Operation != "" {
					pause = false
					return interrupted
				}
				return nil
			}
			forgotten := 0
			job.Forget = func(context.Context) error { forgotten++; return nil }
			executor := &deployments.CloudSQL{Google: gcp.New(google.srv.URL)}
			binding := &planning.Binding{ExternalID: "acme-prod:europe-west1:" + instance, Region: "europe-west1"}
			run := func() error {
				if stop == "remove" {
					return executor.Remove(t.Context(), job, binding)
				}
				_, err := executor.Apply(t.Context(), job)
				return err
			}
			if err := run(); !errors.Is(err, interrupted) {
				t.Fatalf("did not stop at captured %s operation: %v", stop, err)
			}
			if forgotten != 0 {
				t.Fatal("forgot credentials before operation completion")
			}
			job.Resume = saved
			if stop == "user-lost-password" {
				job.Recall = func(context.Context, string) ([]byte, error) { return nil, deployments.ErrNoCredential }
				job.Keep = func(context.Context, string, []byte) error {
					t.Error("a resumed user operation generated or published a replacement password")
					return errors.New("unexpected credential write")
				}
				if err := run(); err == nil || !strings.Contains(err.Error(), "kept password") {
					t.Fatalf("lost credential accepted by a saved user operation: %v", err)
				}
				if writes["user"] != 1 {
					t.Fatal("lost credential replayed user mutation")
				}
				return
			}
			if err := run(); err != nil {
				t.Fatal(err)
			}
			stages := []string{"instance", "database", "user"}
			if stop == "remove" {
				stages = []string{"remove"}
			}
			for _, stage := range stages {
				if writes[stage] != 1 {
					t.Fatalf("%s mutation repeated: %v", stage, writes)
				}
			}
			if stop == "remove" && forgotten != 1 {
				t.Fatal("completed removal retained credentials")
			}
			if len(passwords) > 0 && (passwords[0] == "" || strings.Contains(string(saved), passwords[0])) {
				t.Fatal("password absent or leaked into checkpoint")
			}
		})
	}
}

func TestCloudSQLExecutorRejectsUnscopedOrChangedCheckpoint(t *testing.T) {
	google := newFakeGoogle(t)
	calls := 0
	google.deploy = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) })
	executor := &deployments.CloudSQL{Google: gcp.New(google.srv.URL)}
	for _, raw := range []string{`{`, `{"cloud_sql_checkpoint":1,"stage":"instance"}`, `{"cloud_sql_checkpoint":1,"stage":"ready"}`, `{"operation":"legacy"}`, `{"cloud_sql_checkpoint":99}`, `{"cloud_sql_checkpoint":1,"scope":{"project":"another"},"stage":"instance","operation":"foreign"}`} {
		job := checkpointSQLJob(t, google)
		job.Resume = []byte(raw)
		job.Checkpoint = func([]byte) error { return nil }
		if _, err := executor.Apply(t.Context(), job); err == nil || !strings.Contains(err.Error(), "checkpoint") {
			t.Errorf("invalid checkpoint reached native client: %v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid saved scope made %d provider calls", calls)
	}
}
