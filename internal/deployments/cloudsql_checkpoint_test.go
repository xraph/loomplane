package deployments

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/planning"
)

func TestCloudSQLCheckpointKeepsExactScope(t *testing.T) {
	job := managedDatabaseJob(t, "cloud-sql")
	job.Binding = nil
	job.Action.ID = "resource:database"
	job.Deployment = &Deployment{ID: "deployment"}
	var saved []byte
	job.Checkpoint = func(raw []byte) error { saved = append([]byte(nil), raw...); return nil }
	state, err := readCloudSQLState(job, job.Action.Resource, "project", "us-east-1", "instance", "apply")
	if err != nil {
		t.Fatal(err)
	}
	if err = state.track("original@example.com").Before(); err != nil {
		t.Fatal(err)
	}
	job.Resume = saved
	if pending, readErr := readCloudSQLState(job, job.Action.Resource, "project", "us-east-1", "instance", "apply"); readErr != nil || pending.Operation != "" || !pending.Uncertain || pending.RequestedAt == nil || pending.Caller != "original@example.com" {
		t.Fatalf("lost create request has no durable discovery context: %+v %v", pending, readErr)
	}
	if err = state.track("original@example.com").Started("original"); err != nil {
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
				body["caller"] = ""
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

func TestCloudSQLCheckpointKeepsV2CapturedOperationsAndBlocksUnprovenRequests(t *testing.T) {
	for _, scenario := range []string{"v2-captured", "v2-unknown", "v3-unknown-create", "v3-unknown-database", "v3-unknown-user", "v3-unknown-remove", "missing-caller", "missing-time", "v2-new-context"} {
		t.Run(scenario, func(t *testing.T) {
			job := managedDatabaseJob(t, "cloud-sql")
			job.Binding = nil
			job.Action.ID = "resource:database"
			job.Deployment = &Deployment{ID: "deployment"}
			var saved []byte
			job.Checkpoint = func(raw []byte) error { saved = append([]byte(nil), raw...); return nil }
			mode := "apply"
			if scenario == "v3-unknown-remove" {
				mode = "remove"
			}
			state, err := readCloudSQLState(job, job.Action.Resource, "project", "us-east-1", "instance", mode)
			if err != nil {
				t.Fatal(err)
			}
			state.ResourceID = "original-pin"
			if err = state.track("original@example.com").Before(); err != nil {
				t.Fatal(err)
			}
			if scenario == "v3-unknown-database" {
				state.Stage = "database"
			}
			if scenario == "v3-unknown-user" {
				state.Stage = "user"
			}
			if scenario == "v2-captured" {
				state.Operation = "original"
			}
			if strings.HasPrefix(scenario, "v2-") {
				state.Version = 2
				state.RequestedAt = nil
				state.Caller = ""
			}
			if scenario == "missing-caller" {
				state.Caller = ""
			}
			if scenario == "missing-time" {
				state.RequestedAt = nil
			}
			if scenario == "v2-new-context" {
				now := time.Now().UTC()
				state.RequestedAt = &now
				state.Caller = "original@example.com"
			}
			if err = state.save(); err != nil {
				t.Fatal(err)
			}
			job.Resume = saved
			restored, err := readCloudSQLState(job, job.Action.Resource, "project", "us-east-1", "instance", mode)
			if scenario == "v2-captured" || scenario == "v3-unknown-create" {
				if err != nil || restored.ResourceID != "original-pin" || scenario == "v2-captured" && restored.Operation != "original" {
					t.Fatalf("lost compatible request: %+v %v", restored, err)
				}
			} else if err == nil {
				t.Fatal("unproven request enabled discovery or replay")
			}
		})
	}
}

func TestCloudSQLCheckpointRetainsAndValidatesRecoveredCreateProof(t *testing.T) {
	job := managedDatabaseJob(t, "cloud-sql")
	job.Binding = nil
	job.Action.ID = "resource:database"
	job.Deployment = &Deployment{ID: "deployment"}
	var saved []byte
	job.Checkpoint = func(raw []byte) error { saved = append([]byte(nil), raw...); return nil }
	state, err := readCloudSQLState(job, job.Action.Resource, "project", "us-east-1", "instance", "apply")
	if err != nil {
		t.Fatal(err)
	}
	if err = state.track("original@example.com").Before(); err != nil {
		t.Fatal(err)
	}
	requested := *state.RequestedAt
	created := requested.Add(2 * time.Second)
	source := &gcp.SQLInstance{Name: "instance", Project: "project", Region: "us-east-1", ConnectionName: "project:us-east-1:instance", CreateTime: created.Format(time.RFC3339Nano)}
	pin, err := source.Incarnation("project", "us-east-1", "instance")
	if err != nil {
		t.Fatal(err)
	}
	state.ResourceID, state.Operation = pin, "original"
	state.RecoveredCreate = &gcp.SQLCreateProof{SQLCreateRequest: gcp.SQLCreateRequest{Instance: "instance", Region: "us-east-1", PhysicalID: pin, Intent: state.Intent, DatabaseVersion: "POSTGRES_17", RequestedAt: requested, Caller: "original@example.com"}, Project: "project", Operation: "original", CreatedAt: created, InsertedAt: requested.Add(time.Second), StartedAt: created, EndedAt: created.Add(time.Second)}
	if err = state.save(); err != nil {
		t.Fatal(err)
	}
	job.Resume = saved
	if _, err = readCloudSQLState(job, job.Action.Resource, "project", "us-east-1", "instance", "apply"); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"database", "user", "ready"} {
		if err = state.advance(stage); err != nil {
			t.Fatal(err)
		}
		job.Resume = saved
		restored, err := readCloudSQLState(job, job.Action.Resource, "project", "us-east-1", "instance", "apply")
		if err != nil || restored.RecoveredCreate == nil || restored.RecoveredCreate.Operation != "original" {
			t.Fatalf("stage %s lost original proof: %+v %v", stage, restored, err)
		}
	}
	baseline := append([]byte(nil), saved...)
	for _, field := range []string{"project", "instance", "region", "physical_id", "intent", "database_version", "caller", "operation", "created_at", "inserted_at", "started_at", "ended_at"} {
		t.Run(field, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal(baseline, &body); err != nil {
				t.Fatal(err)
			}
			proof := body["recovered_create"].(map[string]any)
			if strings.HasSuffix(field, "_at") {
				proof[field] = requested.Add(-time.Hour).Format(time.RFC3339Nano)
			} else {
				proof[field] = ""
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			job.Resume = raw
			if _, err := readCloudSQLState(job, job.Action.Resource, "project", "us-east-1", "instance", "apply"); err == nil {
				t.Fatal("changed proof accepted")
			}
		})
	}
}
