package deployments

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
)

func TestCloudSQLCheckpointKeepsExactScope(t *testing.T) {
	job := managedDatabaseJob(t, "cloud-sql")
	job.Action.ID = "resource:database"
	job.Deployment = &Deployment{ID: "deployment"}
	var saved []byte
	job.Checkpoint = func(raw []byte) error { saved = append([]byte(nil), raw...); return nil }
	state, err := readCloudSQLState(job, job.Action.Resource, "project", "us-east-1", "instance", "apply")
	if err != nil {
		t.Fatal(err)
	}
	if err = state.track().Before(); err != nil {
		t.Fatal(err)
	}
	job.Resume = saved
	if _, err = readCloudSQLState(job, job.Action.Resource, "project", "us-east-1", "instance", "apply"); err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatalf("lost response intent can replay: %v", err)
	}
	if err = state.track().Started("original"); err != nil {
		t.Fatal(err)
	}
	original := append([]byte(nil), saved...)
	for _, field := range []string{"unchanged", "mode", "deployment", "action", "connection", "account", "project", "region", "instance", "resource", "stage", "uncertain", "operation", "suffix", "unknown"} {
		t.Run(field, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal(original, &body); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "unchanged":
			case "stage":
				body[field] = "removed"
			case "uncertain":
				body[field] = false
			case "operation":
				body[field] = ""
			case "suffix":
			case "unknown":
				body[field] = true
			default:
				body["scope"].(map[string]any)[field] = "another"
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			if field == "suffix" {
				raw = append(raw, []byte(" {}")...)
			}
			job.Resume = raw
			restored, err := readCloudSQLState(job, job.Action.Resource, "project", "us-east-1", "instance", "apply")
			if field == "unchanged" {
				if err != nil || restored.Operation != "original" {
					t.Fatalf("lost valid operation: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("changed checkpoint scope was accepted")
			}
		})
	}
}

func TestBoundSQLInstanceRequiresOriginalProjectAndRegion(t *testing.T) {
	for _, test := range []struct {
		external, project, region string
		valid                     bool
	}{
		{"project:europe-west1:instance", "project", "europe-west1", true},
		{"domain:project:europe-west1:instance", "domain:project", "europe-west1", true},
		{"project:europe-west1:instance", "project", "", true},
		{"other:europe-west1:instance", "project", "europe-west1", false},
		{"project:us-central1:instance", "project", "europe-west1", false},
		{"project:europe-west1:instance:another", "project", "europe-west1", false},
		{"project:europe-west1:", "project", "europe-west1", false},
	} {
		t.Run(test.external+test.region, func(t *testing.T) {
			instance, region, err := boundSQLInstance(test.external, test.project, test.region)
			if test.valid {
				if err != nil || instance != "instance" || region != "europe-west1" {
					t.Fatalf("valid binding rejected: %s %s %v", instance, region, err)
				}
			} else if err == nil {
				t.Fatal("foreign binding accepted")
			}
		})
	}
}

func TestCloudSQLCheckpointRequiresIncarnationAfterCreateAndBlocksV1(t *testing.T) {
	job := managedDatabaseJob(t, "cloud-sql")
	job.Action.ID = "resource:database"
	job.Deployment = &Deployment{ID: "deployment"}
	var saved []byte
	job.Checkpoint = func(raw []byte) error { saved = append([]byte(nil), raw...); return nil }
	state, err := readCloudSQLState(job, job.Action.Resource, "project", "us-east-1", "instance", "apply")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.save(); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"legacy", "missing-pin", "intent", "binding-pin"} {
		t.Run(scenario, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal(saved, &body); err != nil {
				t.Fatal(err)
			}
			job.Binding = nil
			switch scenario {
			case "legacy":
				body["cloud_sql_checkpoint"] = 1
				delete(body, "intent")
			case "missing-pin":
				body["stage"] = "database"
			case "intent":
				body["intent"] = "foreign"
			case "binding-pin":
				job.Binding = &planning.Binding{PhysicalID: "known"}
				body["resource_id"] = "another"
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			job.Resume = raw
			if _, err := readCloudSQLState(job, job.Action.Resource, "project", "us-east-1", "instance", "apply"); err == nil {
				t.Fatal("unproven checkpoint accepted")
			}
		})
	}
}
