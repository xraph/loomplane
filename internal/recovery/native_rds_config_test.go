package recovery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
)

func TestNativeRDSCopyConfigRequiresReviewedSameAccountPrivateRoute(t *testing.T) {
	for _, scenario := range []string{"reviewed", "no-route", "other-account", "other-region", "other-engine", "unencrypted", "other-point", "source-reprofiled", "unreviewed-source"} {
		t.Run(scenario, func(t *testing.T) {
			profile, err := manageddb.Postgres("rds", "17")
			if err != nil {
				t.Fatal(err)
			}
			cfg := planning.ResourceConfig{Kind: "postgres", Version: "17", Provider: "rds", Region: "us-east-1", Managed: &planning.ManagedDatabaseConfig{Profile: *profile, ConnectionID: "source-connection", AccountIdentity: "account-pin"}}
			source := planning.Binding{Subject: planning.ResourceSubject, Provider: "rds", PhysicalID: "db-original", ExternalID: "orders", Region: "us-east-1", ConnectionID: "source-connection"}
			point := NativePoint{ProviderID: "arn:aws:rds:us-east-1:123456789012:snapshot:original", Name: "original", Kind: "manual", Engine: "postgres", Version: "17.2", Encrypted: true, KMSKey: "original-key", CreatedAt: time.Now()}
			target := NativeRestoreTarget{Provider: "rds", ConnectionID: "target-connection", AccountIdentity: "account-pin", NativeAccount: "123456789012", Region: "us-east-1", RDSNetwork: manageddb.RDSRecoveryNetwork{VPCID: "vpc-recovery", SubnetGroup: "recovery-private", SecurityGroupID: "sg-copy", VerifierGroupID: "sg-worker"}}
			switch scenario {
			case "no-route":
				target.RDSNetwork = manageddb.RDSRecoveryNetwork{}
			case "other-account":
				target.AccountIdentity = "other-account"
			case "other-region":
				target.Region = "eu-west-1"
			case "other-engine":
				point.Version = "16.8"
			case "unencrypted":
				point.Encrypted = false
			case "other-point":
				point.ProviderID = strings.Replace(point.ProviderID, "123456789012", "210987654321", 1)
			case "source-reprofiled":
				cfg.Managed.Profile.StorageGB = 100
			case "unreviewed-source":
				cfg.Managed = nil
			}
			source.Config, err = json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			reviewed, err := NativeSQLCopyConfig(source, point, target)
			if scenario == "reviewed" {
				if err != nil || reviewed == nil || reviewed.Managed.Profile.ID != profile.ID {
					t.Fatal("reviewed RDS copy rejected", err)
				}
			} else if err == nil || reviewed != nil {
				t.Fatal("unreviewed RDS copy accepted", scenario)
			}
		})
	}
}
