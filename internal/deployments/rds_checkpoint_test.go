package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
)

func TestRDSCheckpointPinsItsOriginalScopeAndIncarnation(t *testing.T) {
	job := managedDatabaseJob(t, "rds")
	job.Action.ID = "resource:database"
	job.Deployment = &Deployment{ID: "deployment"}
	var saved []byte
	job.Checkpoint = func(raw []byte) error { saved = append([]byte(nil), raw...); return nil }
	state, err := readRDSState(job, job.Action.Resource, "database", "us-east-1", "", "apply")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.createTrack().Before(); err != nil {
		t.Fatal(err)
	}
	if err := state.createTrack().Captured("db-original"); err != nil {
		t.Fatal(err)
	}
	original := saved
	for _, field := range []string{"unchanged", "account", "connection", "deployment", "action", "region", "instance", "resource", "intent", "resource_id", "version", "stage", "unknown"} {
		t.Run(field, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal(original, &body); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "unchanged":
			case "intent", "resource_id":
				body[field] = "other"
			case "version":
				body["rds_checkpoint"] = 99
			case "stage":
				body[field] = "removed"
			case "unknown":
				body[field] = true
			default:
				body["scope"].(map[string]any)[field] = "another"
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			job.Resume = raw
			restored, err := readRDSState(job, job.Action.Resource, "database", "us-east-1", "db-original", "apply")
			if field == "unchanged" {
				if err != nil || restored.ResourceID != "db-original" {
					t.Fatalf("valid checkpoint lost: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("changed checkpoint was accepted")
			}
		})
	}
}

func TestRDSMissingBindingStillRequiresItsOriginalCredentials(t *testing.T) {
	for _, drift := range []string{"", planning.DriftMissing} {
		t.Run(drift, func(t *testing.T) {
			job := managedDatabaseJob(t, "rds")
			job.Action.ID = "resource:database"
			job.Deployment = &Deployment{ID: "deployment"}
			job.Binding.ExternalID, job.Binding.PhysicalID, job.Binding.Drift = "database", "db-original", drift
			job.Checkpoint = func([]byte) error { return nil }
			job.Recall = func(context.Context, string) ([]byte, error) { return nil, ErrNoCredential }
			job.Keep = func(context.Context, string, []byte) error { return errors.New("unexpected replacement credentials") }
			_, err := (&RDS{}).Apply(t.Context(), job)
			if err == nil || !strings.Contains(err.Error(), "original RDS database has no kept password") {
				t.Fatalf("known source allowed replacement credentials: %v", err)
			}
		})
	}
}
