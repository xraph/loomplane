package deployments

import (
	"encoding/json"
	"strings"
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

func cloudSQLPITRCopyRequest(t *testing.T) recovery.NativeRestore {
	t.Helper()
	job := managedDatabaseJob(t, "cloud-sql")
	cfg := job.Action.Resource
	cfg.Region = "us-central1"
	profile, err := manageddb.CloudSQLPSCPostgres("17")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Managed.Profile = *profile
	cfg.Managed.CloudSQLPSC = manageddb.CloudSQLSourceEndpoint{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", ForwardingRule: "projects/app-project/regions/us-central1/forwardingRules/database"}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	at := now.Add(-time.Hour)
	r := recovery.NativeRestore{ID: "copy", OrgID: "org", ProjectID: "project", EnvironmentID: "env", SourceID: "source", PointID: "point", ExpiresAt: now.Add(time.Hour)}
	r.Source = planning.Binding{OrgID: "org", ProjectID: "project", EnvironmentID: "env", Subject: planning.ResourceSubject, Provider: "cloud-sql", Region: cfg.Region, Name: "database", ExternalID: "project:us-central1:database", PhysicalID: "cloud-sql:" + strings.Repeat("a", 64), ConnectionID: cfg.Managed.ConnectionID, Config: raw}
	r.Target = recovery.NativeRestoreTarget{Provider: "cloud-sql", NativeProject: "project", Region: cfg.Region, ConnectionID: "target", AccountIdentity: cfg.Managed.AccountIdentity, CloudSQLNetwork: manageddb.CloudSQLRecoveryNetwork{ConsumerProject: "worker-project", Network: "projects/worker-project/global/networks/recovery"}}
	r.Point = recovery.NativePoint{ID: "point", SourceID: "source", ProviderID: r.Source.PhysicalID, Name: "database", Kind: "pitr", Status: "available", Engine: "postgres", Version: "17", Encrypted: true, CreatedAt: now.Add(-24 * time.Hour), PITR: &recovery.NativePITRBounds{Protocol: 1, Earliest: now.Add(-2 * time.Hour), Latest: now.Add(-time.Minute), SettingsDigest: strings.Repeat("b", 64)}, RestoreTime: &at}
	return r
}

func TestCloudSQLPITRScopePinsNanosecondsAndCloneWitness(t *testing.T) {
	r := cloudSQLPITRCopyRequest(t)
	s, _, err := readNativeSQLCopy(r)
	if err != nil || s.Version != 2 {
		t.Fatal("PSC restore checkpoint unavailable", err)
	}
	changed := *r.Point.RestoreTime
	changed = changed.Add(time.Nanosecond)
	r.Point.RestoreTime = &changed
	other, _, err := readNativeSQLCopy(r)
	if err != nil || other.Scope == s.Scope {
		t.Fatal("PITR scope lost nanoseconds", err)
	}
	for _, scenario := range []string{"valid", "missing-clone", "legacy-version", "wrong-clone", "missing-pin"} {
		t.Run(scenario, func(t *testing.T) {
			input := cloudSQLPITRCopyRequest(t)
			state, _, readErr := readNativeSQLCopy(input)
			if readErr != nil {
				t.Fatal(readErr)
			}
			state.Stage, state.CredentialVersion, state.PhysicalID, state.CloneOperation = "network", 1, "cloud-sql:"+strings.Repeat("c", 64), "clone-operation"
			switch scenario {
			case "missing-clone":
				state.CloneOperation = ""
			case "legacy-version":
				state.Version = 1
			case "wrong-clone":
				state.Stage = "restore"
				state.Operation = "other-clone"
				state.Uncertain = true
			case "missing-pin":
				state.PhysicalID = ""
			}
			input.Checkpoint, _ = json.Marshal(state)
			_, _, readErr = readNativeSQLCopy(input)
			if (readErr == nil) != (scenario == "valid") {
				t.Fatal("checkpoint witness validation failed", scenario, readErr)
			}
		})
	}
}
