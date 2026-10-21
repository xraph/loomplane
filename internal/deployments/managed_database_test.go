package deployments

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
)

func managedDatabaseJob(t *testing.T, target string) *Job {
	t.Helper()
	cloud := "aws"
	if target == "cloud-sql" {
		cloud = "gcp"
	}
	conn := &connections.Connection{ID: "account", Cloud: cloud, Status: connections.StatusReady, Account: "123456789012", Details: map[string]string{"region": "us-east-1", "project_id": "project"}}
	p, err := manageddb.Postgres(target, "17")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &planning.ResourceConfig{Provider: target, Kind: "postgres", Version: "17", Region: "us-east-1", Managed: &planning.ManagedDatabaseConfig{Profile: *p, ConnectionID: conn.ID, AccountIdentity: connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details)}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &Job{Connection: conn, Action: planning.Action{Provider: target, Region: cfg.Region, Resource: cfg}, Binding: &planning.Binding{Provider: target, Region: cfg.Region, ConnectionID: conn.ID, Config: raw}}
}

func TestManagedDatabaseReviewRejectsChangedRequestsBeforeCloudCalls(t *testing.T) {
	for _, target := range []string{"rds", "cloud-sql"} {
		for _, change := range []string{"profile", "account", "connection", "unready", "region", "cloud", "version", "drop-profile", "binding-account", "replacement"} {
			t.Run(target+"/"+change, func(t *testing.T) {
				job := managedDatabaseJob(t, target)
				switch change {
				case "profile":
					job.Action.Resource.Managed.Profile.StorageGB++
				case "account":
					job.Connection.Account = "other"
					job.Connection.Details["project_id"] = "other"
				case "connection":
					job.Connection.ID = "other"
				case "unready":
					job.Connection.Status = connections.StatusFailed
				case "region":
					job.Action.Resource.Region = "us-west-2"
				case "cloud":
					job.Connection.Cloud = "other"
				case "version":
					job.Action.Resource.Version = "16"
				case "drop-profile":
					job.Action.Resource.Managed = nil
				case "binding-account":
					job.Binding.ConnectionID = "other"
				case "replacement":
					job.Replaces, job.Binding = job.Binding, nil
				}
				// Nil native clients and invalid credentials prove validation happens
				// before sessions, network writes or credential publication.
				var executor Executor = &RDS{}
				if target == "cloud-sql" {
					executor = &CloudSQL{}
				}
				if _, err := executor.Apply(t.Context(), job); err == nil || !strings.Contains(err.Error(), "managed database review") {
					t.Fatalf("changed request reached the provider: %v", err)
				}
			})
		}
	}
}

func TestManagedDatabaseBindingReviewProtectsObserveAndRemove(t *testing.T) {
	for _, target := range []string{"rds", "cloud-sql"} {
		for _, change := range []string{"account", "profile", "region", "connection"} {
			t.Run(target+"/"+change, func(t *testing.T) {
				job := managedDatabaseJob(t, target)
				switch change {
				case "account":
					job.Connection.Account = "other"
					job.Connection.Details["project_id"] = "other"
				case "profile":
					job.Action.Resource.Managed.Profile.BackupRetentionValue = 0
					job.Binding.Config, _ = json.Marshal(job.Action.Resource)
				case "region":
					job.Binding.Region = "us-west-2"
				case "connection":
					job.Binding.ConnectionID = "other"
				}
				var executor Executor = &RDS{}
				if target == "cloud-sql" {
					executor = &CloudSQL{}
				}
				if _, err := executor.Observe(t.Context(), job.Connection, nil, job.Binding); err == nil || !strings.Contains(err.Error(), "managed database review") {
					t.Fatalf("changed observation reached the provider: %v", err)
				}
				if err := executor.Remove(t.Context(), job, job.Binding); err == nil || !strings.Contains(err.Error(), "managed database review") {
					t.Fatalf("changed removal reached the provider: %v", err)
				}
			})
		}
	}
}
