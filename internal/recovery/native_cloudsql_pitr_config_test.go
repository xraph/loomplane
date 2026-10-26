package recovery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
)

func cloudSQLPITRReviewFixture(t *testing.T) (planning.Binding, NativePoint, NativeRestoreTarget) {
	t.Helper()
	profile, err := manageddb.CloudSQLPSCPostgres("17")
	if err != nil {
		t.Fatal(err)
	}
	cfg := planning.ResourceConfig{Provider: "cloud-sql", Kind: "postgres", Version: "17", Region: "us-central1", Managed: &planning.ManagedDatabaseConfig{Profile: *profile, ConnectionID: "source-account", AccountIdentity: "gcp:source-project", CloudSQLPSC: manageddb.CloudSQLSourceEndpoint{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", ForwardingRule: "projects/app-project/regions/us-central1/forwardingRules/database"}}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	source := planning.Binding{Subject: planning.ResourceSubject, Provider: "cloud-sql", Name: "database", ConnectionID: "source-account", ExternalID: "source-project:us-central1:source-database", PhysicalID: "cloud-sql:" + strings.Repeat("a", 64), Region: "us-central1", Config: raw}
	now := time.Now().UTC()
	at := now.Add(-time.Hour)
	point := NativePoint{ProviderID: source.PhysicalID, Name: "source-database", Kind: "pitr", Engine: "postgres", Version: "17", Status: "available", CreatedAt: now.Add(-24 * time.Hour), Encrypted: true, PITR: &NativePITRBounds{Protocol: 1, Earliest: now.Add(-2 * time.Hour), Latest: now.Add(-time.Minute), SettingsDigest: strings.Repeat("b", 64)}, RestoreTime: &at}
	target := NativeRestoreTarget{Provider: "cloud-sql", ConnectionID: "copy-account", NativeProject: "source-project", AccountIdentity: cfg.Managed.AccountIdentity, Region: source.Region, CloudSQLNetwork: manageddb.CloudSQLRecoveryNetwork{ConsumerProject: "worker-project", Network: "projects/worker-project/global/networks/recovery"}}
	return source, point, target
}

func TestCloudSQLPITRReviewRequiresIsolatedExactSelection(t *testing.T) {
	for _, scenario := range []string{"accepted", "cross-project", "cross-region", "missing-network", "production-network", "wrong-account", "wrong-source", "wrong-instance", "missing-time", "changed-time", "missing-digest", "public-profile", "missing-source-endpoint", "missing-created", "expired-point", "snapshot-with-pitr"} {
		t.Run(scenario, func(t *testing.T) {
			source, point, target := cloudSQLPITRReviewFixture(t)
			switch scenario {
			case "cross-project":
				target.NativeProject = "another-project"
			case "cross-region":
				target.Region = "us-east1"
			case "missing-network":
				target.CloudSQLNetwork = manageddb.CloudSQLRecoveryNetwork{}
			case "production-network":
				target.CloudSQLNetwork = manageddb.CloudSQLRecoveryNetwork{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps"}
			case "wrong-account":
				target.AccountIdentity = "gcp:other-project"
			case "wrong-source":
				point.ProviderID = "cloud-sql:" + strings.Repeat("c", 64)
			case "wrong-instance":
				point.Name = "other"
			case "missing-time":
				point.RestoreTime = nil
			case "changed-time":
				at := point.PITR.Latest
				point.RestoreTime = &at
			case "missing-digest":
				point.PITR.SettingsDigest = ""
			case "missing-created":
				point.CreatedAt = time.Time{}
			case "expired-point":
				point.Status = "unavailable"
			case "snapshot-with-pitr":
				point.Kind = "ON_DEMAND"
				point.ProviderID = "projects/source-project/backups/backup"
				point.Name = "backup"
				target.NativeProject = "other-project"
			case "public-profile", "missing-source-endpoint":
				var cfg planning.ResourceConfig
				if err := json.Unmarshal(source.Config, &cfg); err != nil {
					t.Fatal(err)
				}
				if scenario == "public-profile" {
					profile, err := manageddb.Postgres("cloud-sql", "17")
					if err != nil {
						t.Fatal(err)
					}
					cfg.Managed.Profile = *profile
				} else {
					cfg.Managed.CloudSQLPSC = manageddb.CloudSQLSourceEndpoint{}
				}
				source.Config, _ = json.Marshal(cfg)
			}
			cfg, err := NativeSQLCopyConfig(source, point, target)
			if scenario == "accepted" {
				if err != nil || cfg.Managed.Profile.Access != "cloud-sql-psc-connector" {
					t.Fatal("reviewed PSC point refused", err)
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe PITR copy accepted", scenario)
			}
		})
	}
}
