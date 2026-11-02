package deployments

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
)

type selectedNativeBackend interface {
	CheckNativeSnapshot(context.Context, *planning.Binding, recovery.NativePoint) (*recovery.NativePoint, error)
}

func TestSelectedNativeSnapshotKeepsManagedAccountReview(t *testing.T) {
	for _, provider := range []string{"rds", "cloud-sql"} {
		for _, change := range []string{"account", "region", "connection", "unpinned", "unreviewed", "foreign"} {
			t.Run(provider+"/"+change, func(t *testing.T) {
				job := managedDatabaseJob(t, provider)
				job.Binding.OrgID, job.Connection.OrgID = "org", "org"
				job.Binding.Subject, job.Binding.ExternalID, job.Binding.PhysicalID = "resource", "database", "db-original"
				switch change {
				case "account":
					job.Connection.Account = "other"
					if provider == "cloud-sql" {
						job.Connection.Details["project_id"] = "other"
					}
				case "region":
					job.Binding.Region = "us-west-2"
				case "connection":
					job.Connection.ID = "other"
				case "unpinned":
					job.Binding.PhysicalID = ""
				case "unreviewed":
					job.Binding.Config = []byte(`{"kind":"postgres","provider":"rds","version":"17"}`)
				case "foreign":
					job.Connection.OrgID = "foreign"
				}
				credentials := &nativeInventoryCredentials{connection: job.Connection}
				backend := &NativeDatabases{Credentials: credentials}
				reader, ok := any(backend).(selectedNativeBackend)
				if !ok {
					t.Fatal("selected native recovery adapter is missing")
				}
				selected := recovery.NativePoint{Name: "daily", ProviderID: "native-reference", Engine: "postgres", Version: "17", Kind: "manual", Encrypted: true, KMSKey: "original-key", CreatedAt: time.Date(2026, 10, 21, 2, 0, 0, 1, time.UTC)}
				_, err := reader.CheckNativeSnapshot(t.Context(), job.Binding, selected)
				if err == nil {
					t.Fatal("changed source reached native recovery")
				}
				message := err.Error()
				reviewError := strings.Contains(message, "managed database review") || strings.Contains(message, "native source account changed") || strings.Contains(message, "pinned") || strings.Contains(message, "saved managed account review")
				if !reviewError {
					t.Fatal("changed source was not rejected by account/source review", err)
				}
				if (change == "unpinned" || change == "unreviewed") && credentials.calls != 0 {
					t.Fatal("unreviewed point reached credentials")
				}
			})
		}
	}
}
