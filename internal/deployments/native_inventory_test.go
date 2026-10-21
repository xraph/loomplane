package deployments

import (
	"context"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
)

type nativeInventoryCredentials struct {
	connection *connections.Connection
	calls      int
}

func (c *nativeInventoryCredentials) Credential(context.Context, string, string) (*connections.Connection, []byte, error) {
	c.calls++
	return c.connection, nil, nil
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
