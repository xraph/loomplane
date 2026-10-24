package deployments

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
)

func TestNativePITRCopyScopePinsExactTime(t *testing.T) {
	job := managedDatabaseJob(t, "rds")
	raw, err := json.Marshal(job.Action.Resource)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	at := now.Add(-24 * time.Hour)
	request := recovery.NativeRestore{ID: "copy", OrgID: "org", ProjectID: "project", EnvironmentID: "env", SourceID: "source", PointID: "point", ExpiresAt: now.Add(time.Hour)}
	request.Source = planning.Binding{OrgID: "org", ProjectID: "project", EnvironmentID: "env", Subject: planning.ResourceSubject, Name: "database", Provider: "rds", ExternalID: "database", PhysicalID: "db-source", ConnectionID: job.Action.Resource.Managed.ConnectionID, Region: job.Action.Resource.Region, Config: raw}
	request.Target = recovery.NativeRestoreTarget{Provider: "rds", ConnectionID: "target", NativeAccount: "123456789012", AccountIdentity: job.Action.Resource.Managed.AccountIdentity, Region: request.Source.Region, RDSNetwork: manageddb.RDSRecoveryNetwork{VPCID: "vpc-recovery", SubnetGroup: "private", SecurityGroupID: "sg-copy", VerifierGroupID: "sg-worker"}}
	request.Point = recovery.NativePoint{ID: "point", SourceID: "source", ProviderID: "arn:aws:rds:" + request.Source.Region + ":123456789012:db:database", Name: "database", Kind: "pitr", Engine: "postgres", Version: "17.2", CreatedAt: now.Add(-30 * 24 * time.Hour), Encrypted: true, KMSKey: "key", PITR: &recovery.NativePITRBounds{Protocol: 1, Earliest: at.Add(-time.Hour), Latest: at.Add(time.Hour)}, RestoreTime: &at}
	first, _, err := nativeSQLCopyScope(request)
	if err != nil {
		t.Fatal(err)
	}
	other := at.Add(time.Second)
	request.Point.RestoreTime = &other
	second, _, err := nativeSQLCopyScope(request)
	if err != nil || first == second {
		t.Fatal("native copy scope did not pin exact PITR time", err)
	}
}
