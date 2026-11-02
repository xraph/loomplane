package deployments

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/gcp"
)

func TestCloudSQLFailedCreateCheckpointIsCleanupOnly(t *testing.T) {
	for _, scenario := range []string{"partial", "absent", "stage", "uncertain", "operation", "intent", "physical", "time", "legacy"} {
		t.Run(scenario, func(t *testing.T) {
			job := managedDatabaseJob(t, "cloud-sql")
			job.Binding = nil
			job.Action.ID = "resource:database"
			job.Deployment = &Deployment{ID: "deployment"}
			job.Checkpoint = func([]byte) error { return nil }
			state, err := readCloudSQLState(job, job.Action.Resource, "project", "us-east-1", "instance", "apply")
			if err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
			source := &gcp.SQLInstance{Name: "instance", Project: "project", Region: "us-east-1", ConnectionName: "project:us-east-1:instance", CreateTime: at.Add(2 * time.Second).Format(time.RFC3339Nano)}
			pin, err := source.Incarnation("project", "us-east-1", "instance")
			if err != nil {
				t.Fatal(err)
			}
			state.ResourceID = pin
			state.FailedCreate = &gcp.SQLFailedCreateProof{SQLCreateProof: gcp.SQLCreateProof{SQLCreateRequest: gcp.SQLCreateRequest{Instance: "instance", Region: "us-east-1", PhysicalID: pin, Intent: state.Intent, DatabaseVersion: "POSTGRES_17", RequestedAt: at, Caller: "creator@example.com"}, Project: "project", Operation: "create", CreatedAt: at.Add(2 * time.Second), InsertedAt: at.Add(time.Second), StartedAt: at.Add(2 * time.Second), EndedAt: at.Add(3 * time.Second)}}
			switch scenario {
			case "absent":
				state.ResourceID = ""
				state.FailedCreate.PhysicalID = ""
				state.FailedCreate.CreatedAt = time.Time{}
				state.FailedCreate.Absent = true
			case "stage":
				state.Stage = "database"
			case "uncertain":
				state.Uncertain = true
			case "operation":
				state.Operation = "create"
			case "intent":
				state.FailedCreate.Intent = "foreign"
			case "physical":
				state.ResourceID = "replacement"
			case "time":
				state.FailedCreate.EndedAt = at
			case "legacy":
				state.Version = 2
			}
			raw, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			job.Resume = raw
			restored, err := readCloudSQLState(job, job.Action.Resource, "project", "us-east-1", "instance", "apply")
			if scenario == "partial" || scenario == "absent" {
				if err != nil || restored.FailedCreate == nil {
					t.Fatal(restored, err)
				}
			} else if err == nil {
				t.Fatal("changed cleanup proof accepted")
			}
		})
	}
}
