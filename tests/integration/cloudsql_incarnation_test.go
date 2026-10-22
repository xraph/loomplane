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
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
)

func TestCloudSQLResumeRejectsReusedInstanceCreationTime(t *testing.T) {
	google := newFakeGoogle(t)
	job := checkpointSQLJob(t, google)
	instance := deployments.SQLInstanceName(job) + "-europe-west1"
	created, replaced := false, false
	writes := 0
	var settings map[string]any
	google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/operations/") {
			fmt.Fprint(w, `{"name":"create","status":"DONE"}`)
			return
		}
		if r.Method == http.MethodGet {
			if !created {
				w.WriteHeader(404)
				return
			}
			date := "2026-10-21T00:00:00Z"
			if replaced {
				date = "2026-10-21T01:00:00Z"
			}
			labels, _ := json.Marshal(settings)
			fmt.Fprintf(w, `{"settings":%s,"name":%q,"project":"acme-prod","region":"europe-west1","connectionName":%q,"databaseVersion":"POSTGRES_17","state":"RUNNABLE","createTime":%q}`, labels, instance, "acme-prod:europe-west1:"+instance, date)
			return
		}
		var body struct {
			Settings map[string]any `json:"settings"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		settings = body.Settings
		writes++
		created = true
		fmt.Fprint(w, `{"name":"create","status":"DONE"}`)
	})
	interrupted := errors.New("worker interrupted before database setup")
	var saved []byte
	job.Checkpoint = func(raw []byte) error {
		saved = append([]byte(nil), raw...)
		var state struct {
			Stage     string `json:"stage"`
			Operation string `json:"operation"`
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			return err
		}
		if state.Stage == "database" && state.Operation == "" {
			return interrupted
		}
		return nil
	}
	executor := &deployments.CloudSQL{Google: gcp.New(google.srv.URL)}
	if _, err := executor.Apply(t.Context(), job); !errors.Is(err, interrupted) {
		t.Fatalf("did not capture the original source: %v", err)
	}
	replaced = true
	job.Resume = saved
	job.Checkpoint = func([]byte) error { return nil }
	if _, err := executor.Apply(t.Context(), job); err == nil || !strings.Contains(err.Error(), "incarnation") {
		t.Fatalf("reused instance was adopted: %v", err)
	}
	if writes != 1 {
		t.Fatalf("replacement received native mutations: %d", writes)
	}
}

func TestCloudSQLIncarnationGuardsBoundActions(t *testing.T) {
	for _, scenario := range []string{"apply-reused", "apply-missing", "apply-lost-password", "remove-reused", "remove-missing", "observe-reused", "legacy-capture"} {
		t.Run(scenario, func(t *testing.T) {
			google := newFakeGoogle(t)
			job := checkpointSQLJob(t, google)
			instance := deployments.SQLInstanceName(job) + "-europe-west1"
			original := &gcp.SQLInstance{Name: instance, Project: "acme-prod", Region: "europe-west1", ConnectionName: "acme-prod:europe-west1:" + instance, CreateTime: "2026-10-21T00:00:00Z"}
			pin, err := original.Incarnation("acme-prod", "europe-west1", instance)
			if err != nil {
				t.Fatal(err)
			}
			binding := &planning.Binding{ExternalID: original.ConnectionName, PhysicalID: pin, Region: "europe-west1"}
			if scenario == "legacy-capture" {
				binding.PhysicalID = ""
			}
			job.Binding = binding
			writes, calls, forgotten := 0, 0, false
			google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodGet {
					writes++
					fmt.Fprint(w, `{"name":"op","status":"DONE"}`)
					return
				}
				if strings.HasSuffix(scenario, "missing") {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				date := original.CreateTime
				if strings.HasSuffix(scenario, "reused") {
					date = "2026-10-21T01:00:00Z"
				}
				fmt.Fprintf(w, `{"name":%q,"project":"acme-prod","region":"europe-west1","connectionName":%q,"createTime":%q,"databaseVersion":"POSTGRES_17","state":"RUNNABLE"}`, instance, original.ConnectionName, date)
			})
			job.Checkpoint = func([]byte) error { return nil }
			job.Forget = func(context.Context) error { forgotten = true; return nil }
			if scenario != "apply-lost-password" {
				if keepErr := job.Keep(t.Context(), "password", []byte("original")); keepErr != nil {
					t.Fatal(keepErr)
				}
			}
			executor := &deployments.CloudSQL{Google: gcp.New(google.srv.URL)}
			switch {
			case strings.HasPrefix(scenario, "remove"):
				err = executor.Remove(t.Context(), job, binding)
			case strings.HasPrefix(scenario, "observe"):
				_, err = executor.Observe(t.Context(), job.Connection, job.Credential, binding)
			default:
				var out *deployments.Outcome
				out, err = executor.Apply(t.Context(), job)
				if scenario == "legacy-capture" {
					if err != nil || out.PhysicalID != pin {
						t.Fatalf("legacy execution did not capture observed creation: %+v %v", out, err)
					}
					return
				}
			}
			if err == nil || writes != 0 || forgotten {
				t.Fatalf("guard failed: writes=%d forgotten=%v err=%v", writes, forgotten, err)
			}
			if scenario == "apply-lost-password" && (calls != 0 || !strings.Contains(err.Error(), "kept password")) {
				t.Fatalf("missing original password reached provider: %d %v", calls, err)
			}
		})
	}
}

func TestCloudSQLCreateIntentProtectsBeforeFirstPin(t *testing.T) {
	google := newFakeGoogle(t)
	job := checkpointSQLJob(t, google)
	instance := deployments.SQLInstanceName(job) + "-europe-west1"
	created := false
	writes := 0
	google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			writes++
			created = true
			fmt.Fprint(w, `{"name":"create","status":"PENDING"}`)
			return
		}
		if strings.Contains(r.URL.Path, "/operations/") {
			fmt.Fprint(w, `{"name":"create","status":"DONE"}`)
			return
		}
		if !created {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// A different instance now occupies the name, without the original intent.
		fmt.Fprintf(w, `{"name":%q,"project":"acme-prod","region":"europe-west1","connectionName":%q,"createTime":"2026-10-21T01:00:00Z","state":"RUNNABLE","databaseVersion":"POSTGRES_17"}`, instance, "acme-prod:europe-west1:"+instance)
	})
	var saved []byte
	interrupted := errors.New("stopped before first provider identity read")
	job.Checkpoint = func(raw []byte) error {
		saved = append([]byte(nil), raw...)
		var state struct {
			Operation string `json:"operation"`
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			return err
		}
		if state.Operation != "" {
			return interrupted
		}
		return nil
	}
	executor := &deployments.CloudSQL{Google: gcp.New(google.srv.URL)}
	if _, err := executor.Apply(t.Context(), job); !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	job.Resume = saved
	job.Checkpoint = func([]byte) error { return nil }
	if _, err := executor.Apply(t.Context(), job); err == nil || !strings.Contains(err.Error(), "create intent") {
		t.Fatalf("foreign create adopted before pin: %v", err)
	}
	if writes != 1 {
		t.Fatalf("foreign create received writes: %d", writes)
	}
}

func TestCloudSQLPollingDetectsReplacementAndRetainsCredentials(t *testing.T) {
	for _, stage := range []string{"instance", "database", "remove", "pin-save-failure"} {
		t.Run(stage, func(t *testing.T) {
			google := newFakeGoogle(t)
			job := checkpointSQLJob(t, google)
			instance := deployments.SQLInstanceName(job) + "-europe-west1"
			created, replaced, forgotten := stage == "remove", false, false
			writes := map[string]int{}
			polls := 0
			var settings map[string]any
			google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "/operations/") {
					polls++
					replaced = true
					fmt.Fprintf(w, `{"name":%q,"status":"DONE"}`, stage)
					return
				}
				if r.Method == http.MethodGet {
					if !created {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					date := "2026-10-21T00:00:00Z"
					if replaced {
						date = "2026-10-21T01:00:00Z"
					}
					labels, _ := json.Marshal(settings)
					fmt.Fprintf(w, `{"settings":%s,"name":%q,"project":"acme-prod","region":"europe-west1","connectionName":%q,"createTime":%q,"state":"RUNNABLE","databaseVersion":"POSTGRES_17"}`, labels, instance, "acme-prod:europe-west1:"+instance, date)
					return
				}
				kind := "instance"
				switch {
				case r.Method == http.MethodDelete:
					kind = "remove"
				case strings.HasSuffix(r.URL.Path, "/databases"):
					kind = "database"
				case strings.HasSuffix(r.URL.Path, "/users"):
					kind = "user"
				default:
					var body struct {
						Settings map[string]any `json:"settings"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					settings = body.Settings
					created = true
				}
				writes[kind]++
				status := "DONE"
				if stage == kind {
					status = "PENDING"
				}
				fmt.Fprintf(w, `{"name":%q,"status":%q}`, kind, status)
			})
			captureFailure := errors.New("pin persistence failed")
			job.Checkpoint = func(raw []byte) error {
				var state struct {
					ResourceID string `json:"resource_id"`
				}
				if err := json.Unmarshal(raw, &state); err != nil {
					return err
				}
				if stage == "pin-save-failure" && state.ResourceID != "" {
					return captureFailure
				}
				return nil
			}
			job.Forget = func(context.Context) error { forgotten = true; return nil }
			executor := &deployments.CloudSQL{Google: gcp.New(google.srv.URL)}
			var err error
			if stage == "remove" {
				err = executor.Remove(t.Context(), job, &planning.Binding{ExternalID: "acme-prod:europe-west1:" + instance, Region: "europe-west1"})
			} else {
				_, err = executor.Apply(t.Context(), job)
			}
			if stage == "pin-save-failure" {
				if !errors.Is(err, captureFailure) || polls != 0 || writes["instance"] != 1 || writes["database"] != 0 {
					t.Fatalf("failed pin capture continued setup: polls=%d writes=%v err=%v", polls, writes, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "incarnation") || forgotten || polls != 1 || writes["user"] != 0 || writes[stage] != 1 {
				t.Fatalf("replacement survived polling: forgotten=%v polls=%d writes=%v err=%v", forgotten, polls, writes, err)
			}
			if stage == "instance" && writes["database"] != 0 {
				t.Fatal("replacement received database creation")
			}
		})
	}
}

func TestCloudSQLCreateWaitsForIdentityBeforePollingAndResumes(t *testing.T) {
	google := newFakeGoogle(t)
	job := checkpointSQLJob(t, google)
	instance := deployments.SQLInstanceName(job) + "-europe-west1"
	created, visible := false, false
	writes, polls := 0, 0
	var settings map[string]any
	var saved []byte
	job.Checkpoint = func(raw []byte) error { saved = append([]byte(nil), raw...); return nil }
	google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/operations/") {
			polls++
			var state struct {
				ResourceID string `json:"resource_id"`
			}
			if err := json.Unmarshal(saved, &state); err != nil {
				t.Error(err)
			}
			if state.ResourceID == "" {
				t.Error("CREATE was polled without durable incarnation")
			}
			fmt.Fprint(w, `{"name":"create","status":"DONE"}`)
			return
		}
		if r.Method == http.MethodGet {
			if !created || !visible {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			labels, _ := json.Marshal(settings)
			fmt.Fprintf(w, `{"settings":%s,"name":%q,"project":"acme-prod","region":"europe-west1","connectionName":%q,"createTime":"2026-10-21T00:00:00Z","state":"RUNNABLE","databaseVersion":"POSTGRES_17"}`, labels, instance, "acme-prod:europe-west1:"+instance)
			return
		}
		writes++
		if !created {
			created = true
			var body struct {
				Settings map[string]any `json:"settings"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			settings = body.Settings
			fmt.Fprint(w, `{"name":"create","status":"PENDING"}`)
			return
		}
		fmt.Fprint(w, `{"name":"setup","status":"DONE"}`)
	})
	executor := &deployments.CloudSQL{Google: gcp.New(google.srv.URL)}
	if _, err := executor.Apply(t.Context(), job); err == nil || !strings.Contains(err.Error(), "incarnation is missing") || polls != 0 || writes != 1 {
		t.Fatalf("invisible CREATE progressed without pin: polls=%d writes=%d err=%v", polls, writes, err)
	}
	job.Resume = append([]byte(nil), saved...)
	if _, err := executor.Apply(t.Context(), job); err == nil || !strings.Contains(err.Error(), "incarnation is missing") || polls != 0 || writes != 1 {
		t.Fatalf("invisible resumed CREATE progressed without pin: polls=%d writes=%d err=%v", polls, writes, err)
	}
	visible = true
	out, err := executor.Apply(t.Context(), job)
	if err != nil || out.PhysicalID == "" || writes != 3 || polls != 1 {
		t.Fatalf("visible original failed to resume: polls=%d writes=%d out=%+v err=%v", polls, writes, out, err)
	}
}
