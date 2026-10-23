package deployments

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
)

type nativeInventoryCredentials struct {
	connection *connections.Connection
	calls      int
	credential []byte
}

func (c *nativeInventoryCredentials) Credential(context.Context, string, string) (*connections.Connection, []byte, error) {
	c.calls++
	return c.connection, c.credential, nil
}

func TestNativeInventoryRejectsChangedAccountBeforeCloudReads(t *testing.T) {
	for _, change := range []string{"account", "region", "connection", "unready", "unpinned", "unreviewed", "foreign"} {
		t.Run(change, func(t *testing.T) {
			job := managedDatabaseJob(t, "rds")
			job.Binding.OrgID, job.Connection.OrgID = "org", "org"
			job.Binding.Subject, job.Binding.Name = "resource", "database"
			job.Binding.ExternalID, job.Binding.PhysicalID = "database", "db-original"
			switch change {
			case "account":
				job.Connection.Account = "other"
			case "region":
				job.Connection.Details["region"] = "us-west-2"
			case "connection":
				job.Connection.ID = "other"
			case "unready":
				job.Connection.Status = connections.StatusFailed
			case "unpinned":
				job.Binding.PhysicalID = ""
			case "unreviewed":
				job.Binding.Config = []byte(`{"kind":"postgres","provider":"rds","version":"17"}`)
			case "foreign":
				job.Connection.OrgID = "other"
			}
			creds := &nativeInventoryCredentials{connection: job.Connection}
			backend := NativeDatabases{Credentials: creds}
			if _, _, err := backend.NativeSnapshots(t.Context(), job.Binding); err == nil {
				t.Fatal("changed source reached the cloud")
			}
			if change == "unpinned" && creds.calls != 0 {
				t.Fatal("unpinned history reached credentials")
			}
		})
	}
}

func TestNativeCloudSQLInventoryRejectsChangedScopeBeforeProviderReads(t *testing.T) {
	for _, change := range []string{"account", "region", "connection", "unready", "unpinned", "unreviewed", "foreign", "credential-project", "external-project", "external-region"} {
		t.Run(change, func(t *testing.T) {
			job := managedDatabaseJob(t, "cloud-sql")
			job.Binding.OrgID, job.Connection.OrgID = "org", "org"
			job.Binding.Subject, job.Binding.Name = "resource", "database"
			job.Binding.ExternalID, job.Binding.PhysicalID = "project:us-east-1:database", "cloud-sql:"+strings.Repeat("a", 64)
			credential := []byte(`{"type":"service_account","project_id":"project","client_email":"test@example.com","private_key":"key"}`)
			switch change {
			case "account":
				job.Connection.Details["project_id"] = "other"
			case "region":
				job.Binding.Region = "us-west-2"
			case "connection":
				job.Connection.ID = "other"
			case "unready":
				job.Connection.Status = connections.StatusFailed
			case "unpinned":
				job.Binding.PhysicalID = ""
			case "unreviewed":
				job.Binding.Config = []byte(`{"kind":"postgres","provider":"cloud-sql","version":"17"}`)
			case "foreign":
				job.Connection.OrgID = "other"
			case "credential-project":
				credential = []byte(`{"type":"service_account","project_id":"other","client_email":"test@example.com","private_key":"key"}`)
			case "external-project":
				job.Binding.ExternalID = "other:us-east-1:database"
			case "external-region":
				job.Binding.ExternalID = "project:us-west-2:database"
			}
			creds := &nativeInventoryCredentials{connection: job.Connection, credential: credential}
			backend := NativeDatabases{Credentials: creds}
			// No Google client is installed, so scope validation must finish before a session.
			if _, _, err := backend.NativeSnapshots(t.Context(), job.Binding); err == nil {
				t.Fatal("changed source reached native backup provider")
			}
			if change == "unpinned" && creds.calls != 0 {
				t.Fatal("unpinned source reached credentials")
			}
		})
	}
}

func TestNativeInventoryRequiresSavedManagedProfileForBothProviders(t *testing.T) {
	for _, provider := range []string{"rds", "cloud-sql"} {
		job := managedDatabaseJob(t, provider)
		job.Binding.OrgID, job.Connection.OrgID = "org", "org"
		job.Binding.Subject = "resource"
		job.Binding.ExternalID, job.Binding.PhysicalID = "database", "pin"
		cfg := *job.Action.Resource
		cfg.Managed = nil
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		job.Binding.Config = raw
		creds := &nativeInventoryCredentials{connection: job.Connection}
		backend := NativeDatabases{Credentials: creds}
		if _, _, err := backend.NativeSnapshots(t.Context(), job.Binding); err == nil || creds.calls != 0 {
			t.Fatalf("unreviewed %s history reached credentials: %d %v", provider, creds.calls, err)
		}
	}
}
