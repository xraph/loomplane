//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

func TestNativeRDSCopyBackendVerifiesReaderAndCleansExactCopy(t *testing.T) {
	for _, scenario := range []string{"service-observe-missing", "service-observe-error", "service-pin-checkpoint", "service-pin-committed", "service-admin-checkpoint", "service-verify-checkpoint", "service-ready-checkpoint", "service-proof-expiry", "cleanup-busy", "service-lost-restore", "service-lost-admin", "cleanup-denied", "admitted", "complete", "captured-restart", "lost-restore-response", "lost-admin-response", "proof-save-failure", "network-before-admin", "reader-privilege-drift"} {
		t.Run(scenario, func(t *testing.T) { runNativeRDSCopyBackend(t, scenario) })
	}
}

func runNativeRDSCopyBackend(t *testing.T, scenario string) {
	admittedFlow := scenario == "admitted" || strings.HasPrefix(scenario, "service-")
	f := nativePointHistoryFixture(t)
	sql := nativeSQLFixtureFor(t)
	if _, setupErr := sql.admin.Exec(t.Context(), `CREATE ROLE app LOGIN SUPERUSER PASSWORD 'unexposed-source-password'`); setupErr != nil {
		t.Fatal(setupErr)
	}
	profile, err := manageddb.Postgres("rds", "17")
	if err != nil {
		t.Fatal(err)
	}
	network := manageddb.RDSRecoveryNetwork{VPCID: "vpc-recovery", SubnetGroup: "recovery-private", SecurityGroupID: "sg-copy", VerifierGroupID: "sg-worker"}
	details := network.Settings()
	details["region"] = "us-east-1"
	account := &connections.Connection{ID: "rds-account", OrgID: "org", Cloud: connections.AWS, Account: "123456789012", Details: details, Status: connections.StatusReady, Name: "RDS test", CreatedBy: "operator", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = connections.NewPgStore(f.db).CreateConnection(t.Context(), account); err != nil {
		t.Fatal(err)
	}
	key := []byte(`{"access_key_id":"AKIATESTTESTTESTTEST","secret_access_key":"test-test-test-test-secret"}`)
	identity := connections.AccountIdentity(connections.AWS, account.Account, details)
	cfg := planning.ResourceConfig{Provider: "rds", Kind: "postgres", Version: "17", Region: "us-east-1", Managed: &planning.ManagedDatabaseConfig{Profile: *profile, ConnectionID: account.ID, AccountIdentity: identity}}
	binding := f.source.Binding
	binding.ID = "rds-native-copy-source"
	binding.Provider, binding.ConnectionID, binding.ExternalID, binding.PhysicalID, binding.Region, binding.Name = "rds", account.ID, "orders", "db-source", "us-east-1", sql.config.Database
	binding.Config, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = planning.NewPgStore(f.db).PutBinding(t.Context(), &binding); err != nil {
		t.Fatal(err)
	}
	sources, err := f.store.ListNativeSources(t.Context(), "org", "project", recovery.NativePageInput{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, source := range sources.Sources {
		if source.PhysicalID == binding.PhysicalID {
			f.source = source
			found = true
		}
	}
	if !found {
		t.Fatal("RDS retained source missing")
	}
	point := recovery.NativePoint{ProviderID: "arn:aws:rds:us-east-1:123456789012:snapshot:original", Name: "original", Kind: "manual", Status: "available", Engine: "postgres", Version: "17.2", Encrypted: true, KMSKey: "arn:aws:kms:us-east-1:123456789012:key/original", CreatedAt: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)}
	if err = f.refresh(t.Context(), []recovery.NativePoint{point}); err != nil {
		t.Fatal(err)
	}
	points, err := f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil || len(points.Points) != 1 {
		t.Fatal("RDS native point missing", err)
	}
	request := &recovery.NativeRestore{ID: "rds-copy", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", SourceID: f.source.ID, PointID: points.Points[0].ID, Point: points.Points[0], RequestID: "rds-request", CreatedBy: "operator", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), Target: recovery.NativeRestoreTarget{Provider: "rds", ConnectionID: account.ID, NativeAccount: account.Account, AccountIdentity: identity, Region: "us-east-1", RDSNetwork: network}}
	var claimed *recovery.NativeRestore
	if !admittedFlow {
		if _, err = f.store.CreateNativeRestore(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		claimed, err = f.store.ClaimNativeRestore(t.Context(), "org", request.ID, "rds-worker")
		if err != nil {
			t.Fatal(err)
		}
	}

	vault := nativeCredentialVault(t, f)
	var mu sync.Mutex
	exists := false
	unsafeNetwork := false
	injected := false
	cleanupDenied := false
	adminReads := 0
	observation := ""
	var name, intent, adminPassword string
	writes := map[string]int{}
	t.Cleanup(func() {
		if len(intent) >= 32 {
			if _, dropErr := sql.admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgx.Identifier{"lp_recovery_" + intent[:32]}.Sanitize()); dropErr != nil {
				t.Error(dropErr)
			}
		}
		if _, dropErr := sql.admin.Exec(context.Background(), "DROP ROLE app"); dropErr != nil {
			t.Error(dropErr)
		}
	})

	instance := func() string {
		value := nativeRDSInstanceXML(name, "db-copy", intent, point.KMSKey)
		if scenario == "service-lost-admin" && writes["ModifyDBInstance"] > 0 && adminReads < 3 {
			value = strings.Replace(value, "</DBInstance>", "<PendingModifiedValues><MasterUserPassword>pending</MasterUserPassword></PendingModifiedValues></DBInstance>", 1)
		}
		if unsafeNetwork {
			value = strings.Replace(value, "<PubliclyAccessible>false</PubliclyAccessible>", "<PubliclyAccessible>true</PubliclyAccessible>", 1)
		}
		return value
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if parseErr := r.ParseForm(); parseErr != nil {
			t.Error(parseErr)
		}
		w.Header().Set("Content-Type", "text/xml")
		action := r.Form.Get("Action")
		switch action {
		case "DescribeDBSnapshots":
			fmt.Fprintf(w, `<DescribeDBSnapshotsResponse><DescribeDBSnapshotsResult><DBSnapshots><DBSnapshot><DBSnapshotIdentifier>original</DBSnapshotIdentifier><DBSnapshotArn>%s</DBSnapshotArn><DBInstanceIdentifier>orders</DBInstanceIdentifier><DbiResourceId>db-source</DbiResourceId><SnapshotType>manual</SnapshotType><SnapshotCreateTime>%s</SnapshotCreateTime><Status>available</Status><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><Encrypted>true</Encrypted><KmsKeyId>%s</KmsKeyId><AllocatedStorage>20</AllocatedStorage><MasterUsername>app</MasterUsername></DBSnapshot></DBSnapshots></DescribeDBSnapshotsResult></DescribeDBSnapshotsResponse>`, point.ProviderID, point.CreatedAt.Format(time.RFC3339Nano), point.KMSKey)
		case "DescribeDBInstances":
			if scenario == "service-lost-admin" && writes["ModifyDBInstance"] > 0 {
				adminReads++
			}
			if exists && observation != "" {
				if observation == "missing" {
					w.WriteHeader(404)
					fmt.Fprint(w, `<ErrorResponse><Error><Code>DBInstanceNotFound</Code></Error></ErrorResponse>`)
				} else {
					w.WriteHeader(503)
					fmt.Fprint(w, `<ErrorResponse><Error><Code>ServiceUnavailable</Code></Error></ErrorResponse>`)
				}
				return
			}
			if !exists {
				w.WriteHeader(404)
				fmt.Fprint(w, `<ErrorResponse><Error><Code>DBInstanceNotFound</Code></Error></ErrorResponse>`)
				return
			}
			if r.Form.Get("DBInstanceIdentifier") != name {
				t.Error("copy inspection touched source")
			}
			fmt.Fprint(w, `<DescribeDBInstancesResponse><DescribeDBInstancesResult><DBInstances>`+instance()+`</DBInstances></DescribeDBInstancesResult></DescribeDBInstancesResponse>`)
		case "DescribeDBSubnetGroups":
			fmt.Fprint(w, `<DescribeDBSubnetGroupsResponse><DescribeDBSubnetGroupsResult><DBSubnetGroups><DBSubnetGroup><DBSubnetGroupName>recovery-private</DBSubnetGroupName><VpcId>vpc-recovery</VpcId><SubnetGroupStatus>Complete</SubnetGroupStatus></DBSubnetGroup></DBSubnetGroups></DescribeDBSubnetGroupsResult></DescribeDBSubnetGroupsResponse>`)
		case "DescribeSecurityGroups":
			fmt.Fprint(w, `<DescribeSecurityGroupsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><securityGroupInfo><item><ownerId>123456789012</ownerId><groupId>sg-copy</groupId><vpcId>vpc-recovery</vpcId><ipPermissions><item><ipProtocol>tcp</ipProtocol><fromPort>5432</fromPort><toPort>5432</toPort><groups><item><userId>123456789012</userId><groupId>sg-worker</groupId></item></groups></item></ipPermissions></item><item><ownerId>123456789012</ownerId><groupId>sg-worker</groupId><vpcId>vpc-recovery</vpcId></item></securityGroupInfo></DescribeSecurityGroupsResponse>`)
		case "RestoreDBInstanceFromDBSnapshot":
			writes[action]++
			name = r.Form.Get("DBInstanceIdentifier")
			intent = r.Form.Get("Tags.Tag.1.Value")
			exists = true
			if scenario == "lost-restore-response" || scenario == "service-lost-restore" || strings.HasPrefix(scenario, "service-observe-") {
				w.WriteHeader(500)
				fmt.Fprint(w, `<ErrorResponse><Error><Code>InternalFailure</Code></Error></ErrorResponse>`)
				return
			}
			fmt.Fprint(w, `<RestoreDBInstanceFromDBSnapshotResponse><RestoreDBInstanceFromDBSnapshotResult>`+instance()+`</RestoreDBInstanceFromDBSnapshotResult></RestoreDBInstanceFromDBSnapshotResponse>`)
		case "ModifyDBInstance":
			writes[action]++
			if r.Form.Get("DBInstanceIdentifier") != name {
				t.Error("administrator update touched source")
			}
			adminPassword = r.Form.Get("MasterUserPassword")
			if _, sqlErr := sql.admin.Exec(t.Context(), "ALTER ROLE "+pgx.Identifier{"app"}.Sanitize()+" PASSWORD '"+adminPassword+"'"); sqlErr != nil {
				t.Error(sqlErr)
			}
			if scenario == "lost-admin-response" || scenario == "service-lost-admin" {
				w.WriteHeader(500)
				fmt.Fprint(w, `<ErrorResponse><Error><Code>InternalFailure</Code></Error></ErrorResponse>`)
				return
			}
			fmt.Fprint(w, `<ModifyDBInstanceResponse><ModifyDBInstanceResult>`+instance()+`</ModifyDBInstanceResult></ModifyDBInstanceResponse>`)
		case "DeleteDBInstance":
			if (scenario == "cleanup-denied" || scenario == "cleanup-busy") && !cleanupDenied {
				cleanupDenied = true
				w.WriteHeader(403)
				code := "AccessDenied"
				if scenario == "cleanup-busy" {
					code = "InvalidDBInstanceState"
				}
				fmt.Fprintf(w, `<ErrorResponse><Error><Code>%s</Code></Error></ErrorResponse>`, code)
				return
			}
			writes[action]++
			if r.Form.Get("DBInstanceIdentifier") != name || r.Form.Get("SkipFinalSnapshot") != "true" || r.Form.Get("DeleteAutomatedBackups") != "true" {
				t.Error("wrong cleanup")
			}
			fmt.Fprint(w, `<DeleteDBInstanceResponse><DeleteDBInstanceResult>`+instance()+`</DeleteDBInstanceResult></DeleteDBInstanceResponse>`)
			exists = false
		default:
			t.Error("unexpected RDS call", action)
			w.WriteHeader(400)
		}
	}))
	defer api.Close()
	backend := &deployments.NativeDatabases{AWS: aws.New(api.URL), Credentials: nativeCopyAccounts{connections: map[string]*connections.Connection{account.ID: account}, keys: map[string][]byte{account.ID: key}}, Secrets: vault, RDSConnector: func(_ context.Context, _ *aws.Session, instance, host string, port int32) (aws.DBConnector, error) {
		mu.Lock()
		defer mu.Unlock()
		if instance != name || host != name+".abcdef.us-east-1.rds.amazonaws.com" || port != 5432 {
			t.Error("wrong SQL destination")
		}
		return nativeFixtureConnector{dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", sql.config.Host, sql.config.Port))
		}}, nil
	}}
	principal := security.Principal{OrgID: "org", UserID: "operator"}
	var service *recovery.NativeService
	if admittedFlow {
		now := time.Now().UTC()
		clouds := cloudprofiles.NewPgStore(f.db)
		if err = clouds.CreateProfile(t.Context(), &cloudprofiles.Profile{ID: "rds-cloud", OrgID: "org", ProjectID: "project", Name: "RDS recovery", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err = clouds.CreateTarget(t.Context(), &cloudprofiles.Target{ID: "rds-target", CloudID: "rds-cloud", OrgID: "org", ProjectID: "project", Name: "recovery", DisplayName: "RDS recovery", Provider: "rds", Runtime: "managed", ConnectionID: account.ID, Region: "us-east-1", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if _, err = clouds.SetProjectGrant(t.Context(), "org", "rds-cloud", "project", []string{"rds-target"}, false); err != nil {
			t.Fatal(err)
		}
		service = &recovery.NativeService{Store: f.store, Copies: f.store, Cleanups: f.store, Admission: f.store, Projects: projects.NewPgStore(f.db), Authz: nativeAccessAllow{}, ReviewKey: bytes.Repeat([]byte{37}, 32), Backend: backend, CopyBackend: backend, CleanupBackend: backend, AccessBackend: backend, EnqueueCopy: func(context.Context, string, string) error { return nil }, EnqueueCleanup: func(context.Context, string, string) error { return nil }}
		if strings.HasSuffix(scenario, "-checkpoint") || scenario == "service-pin-committed" {
			service.Copies = &nativeRDSCheckpointFailure{PgStore: f.store, scenario: scenario}
		}
		if scenario == "service-proof-expiry" {
			service.Copies = &nativeRDSCheckpointExpiry{PgStore: f.store}
		}
		review, reviewErr := service.ReviewCopy(t.Context(), principal, request.SourceID, request.PointID, recovery.NativeCopyReviewInput{RequestID: "rds-admitted-request", CloudID: "rds-cloud", TargetID: "rds-target", TTLHours: 1})
		if reviewErr != nil || review == nil || review.Capacity.ComputeClass != "db.t4g.micro" {
			t.Fatal("RDS has no signed private copy admission", reviewErr)
		}
		admitted, createErr := service.CreateCopy(t.Context(), principal, recovery.NativeCopyCreateInput{RequestID: review.RequestID, ReviewToken: review.ReviewToken, AcknowledgeCharges: true})
		if createErr != nil {
			t.Fatal(createErr)
		}

		if strings.HasPrefix(scenario, "service-") {
			if interrupted := service.RunCopy(t.Context(), "org", admitted.ID); interrupted == nil {
				t.Fatal("uncertain native request did not interrupt worker")
			}
			pending, pendingErr := f.store.GetNativeRestore(t.Context(), "org", admitted.ID)
			if pendingErr != nil {
				t.Fatal(pendingErr)
			}
			if pending.Status != "queued" {
				t.Fatal("uncertain native request cannot resume through public admission", pending.Status, pendingErr)
			}
			var originalCheckpoint struct {
				CredentialVersion int `json:"credential_version"`
			}
			if decodeErr := json.Unmarshal(pending.Checkpoint, &originalCheckpoint); decodeErr != nil || originalCheckpoint.CredentialVersion <= 0 {
				t.Fatal("pending copy lost immutable credential version", decodeErr)
			}
			if strings.HasPrefix(scenario, "service-observe-") {
				mu.Lock()
				observation = strings.TrimPrefix(scenario, "service-observe-")
				mu.Unlock()
				if observeErr := service.RunCopy(t.Context(), "org", admitted.ID); observeErr == nil {
					t.Fatal("unresolved native observation returned success")
				}
				pending, pendingErr = f.store.GetNativeRestore(t.Context(), "org", admitted.ID)
				if pendingErr != nil || pending.Status != "queued" || pending.VerifiedAt != nil {
					t.Fatal("unresolved original restore lost reconciliation", pending.Status, pendingErr)
				}
				mu.Lock()
				observation = ""
				mu.Unlock()
			}
			pendingWork, scanErr := f.store.PendingNativeRestores(t.Context(), recovery.NativeCopyPage{})
			if scanErr != nil || len(pendingWork) != 1 || pendingWork[0].ID != admitted.ID || pendingWork[0].VerifiedAt != nil {
				t.Fatal("pending copy disappeared from reconciliation scan", scanErr)
			}
			t.Cleanup(func() {
				final, finalErr := f.store.GetNativeRestore(context.Background(), "org", admitted.ID)
				var kept struct {
					CredentialVersion int `json:"credential_version"`
				}
				if finalErr != nil || json.Unmarshal(final.Checkpoint, &kept) != nil || kept.CredentialVersion != originalCheckpoint.CredentialVersion || !final.ExpiresAt.Equal(admitted.ExpiresAt) {
					t.Error("resumed request changed credentials or expiry", finalErr)
				}
			})
			replay, replayErr := service.CreateCopy(t.Context(), principal, recovery.NativeCopyCreateInput{RequestID: review.RequestID, ReviewToken: review.ReviewToken, AcknowledgeCharges: true})
			if replayErr != nil || replay.ID != admitted.ID || !replay.ExpiresAt.Equal(admitted.ExpiresAt) {
				t.Fatal("native reconciliation changed request or expiry", replayErr)
			}
		}
		if runErr := service.RunCopy(t.Context(), "org", admitted.ID); runErr != nil {
			t.Fatal(runErr)
		}
		claimed, err = f.store.GetNativeRestore(t.Context(), "org", admitted.ID)
		if err != nil {
			t.Fatal(err)
		}
	}

	control := recovery.NativeCopyControl{NativeWorkerControl: recovery.NativeWorkerControl{Check: func(ctx context.Context) error { return f.store.CheckNativeLease(ctx, claimed, "running") }, Save: func(ctx context.Context, raw json.RawMessage) error {
		var metadata struct {
			Stage     string
			Uncertain bool
		}
		if decodeErr := json.Unmarshal(raw, &metadata); decodeErr != nil {
			return decodeErr
		}
		if !injected && scenario == "proof-save-failure" && metadata.Stage == "ready" {
			injected = true
			return recovery.ErrConflict
		}
		if saveErr := f.store.SaveNativeCheckpoint(ctx, claimed, raw); saveErr != nil {
			return saveErr
		}
		if metadata.Stage == "admin" && !metadata.Uncertain {
			if scenario == "network-before-admin" {
				mu.Lock()
				unsafeNetwork = true
				mu.Unlock()
			}
			if !injected && scenario == "captured-restart" {
				injected = true
				return recovery.ErrConflict
			}
		}
		return nil
	}}, Selected: func(ctx context.Context) (*recovery.NativePoint, error) {
		return backend.CheckNativeSnapshot(ctx, &claimed.Source, claimed.Point)
	}}
	var result recovery.NativeRestoreResult
	if admittedFlow {
		result = recovery.NativeRestoreResult{ExternalID: claimed.ExternalID, PhysicalID: claimed.PhysicalID, SQLVerified: claimed.VerifiedAt != nil}
	} else {
		result, err = backend.RestoreNative(t.Context(), principal, *claimed, control)
	}
	if scenario == "captured-restart" || scenario == "lost-restore-response" || scenario == "lost-admin-response" || scenario == "proof-save-failure" {
		if err == nil {
			t.Fatal("injected interruption did not stop backend")
		}
		claimed, err = f.store.GetNativeRestore(t.Context(), "org", claimed.ID)
		if err != nil {
			t.Fatal(err)
		}
		result, err = backend.RestoreNative(t.Context(), principal, *claimed, control)
	}
	if scenario == "network-before-admin" {
		if err == nil || result.SQLVerified {
			t.Fatal("unsafe RDS network reached verification")
		}
		mu.Lock()
		if writes["ModifyDBInstance"] != 0 {
			t.Error("unsafe copy received credentials")
		}
		mu.Unlock()
	} else if err != nil || !result.SQLVerified || result.PhysicalID != "db-copy" {
		t.Fatal("RDS native copy has no SQL-verified backend", result, err)
	}
	if !admittedFlow {
		if err = f.store.FinishNativeRestore(t.Context(), claimed, result, time.Second, err); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := f.store.GetNativeRestore(t.Context(), "org", claimed.ID)
	if err != nil {
		t.Fatal(err)
	}

	if adminPassword != "" && strings.Contains(string(stored.Checkpoint), adminPassword) || strings.Contains(string(stored.Checkpoint), sql.spec.ReaderPassword) {
		t.Fatal("checkpoint leaked copy password")
	}
	if scenario != "network-before-admin" {
		if scenario == "reader-privilege-drift" {
			if _, grantErr := sql.admin.Exec(t.Context(), `GRANT UPDATE ON public.recovered_rows TO PUBLIC`); grantErr != nil {
				t.Fatal(grantErr)
			}
		}
		var credentials *recovery.NativeCopyCredentials
		var accessErr error
		if admittedFlow {
			credentials, accessErr = service.CopyCredentials(t.Context(), principal, stored.ID)
		} else {
			credentials, accessErr = backend.NativeCredentials(t.Context(), principal, *stored, func(ctx context.Context) error { return ctx.Err() })
		}

		if scenario == "reader-privilege-drift" {
			if accessErr == nil || credentials != nil {
				t.Fatal("RDS privilege drift disclosed reader")
			}
		} else if accessErr != nil || credentials == nil || credentials.Provider != "rds" || credentials.Password == adminPassword {
			t.Fatal("verified RDS reader unavailable", accessErr)
		}
	}

	if admittedFlow {
		if _, err = service.RequestCopyCleanup(t.Context(), principal, claimed.ID); err != nil {
			t.Fatal(err)
		}
		if err = service.RunCleanup(t.Context(), "org", claimed.ID); err != nil {
			t.Fatal(err)
		}
		removed, readErr := f.store.GetNativeRestore(t.Context(), "org", claimed.ID)
		if readErr != nil || removed.Status != "deleted" {
			t.Fatal("RDS service cleanup did not finish", readErr)
		}
	} else {
		if _, err = f.store.RequestNativeCleanup(t.Context(), "org", claimed.ID, "operator", func(context.Context) error { return nil }); err != nil {
			t.Fatal(err)
		}
		cleanup, err := f.store.ClaimNativeCleanup(t.Context(), "org", claimed.ID, "rds-cleanup")
		if err != nil {
			t.Fatal(err)
		}
		removed, err := backend.DeleteNative(t.Context(), principal, *cleanup, recovery.NativeWorkerControl{Check: func(ctx context.Context) error { return f.store.CheckNativeLease(ctx, cleanup, "deleting") }, Save: func(ctx context.Context, raw json.RawMessage) error {
			return f.store.SaveNativeCheckpoint(ctx, cleanup, raw)
		}})
		if scenario == "cleanup-denied" || scenario == "cleanup-busy" {
			if err == nil || removed.ConfirmedAbsent {
				t.Fatal("denied cleanup was confirmed absent")
			}
			retry, readErr := f.store.GetNativeRestore(t.Context(), "org", claimed.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			var checkpoint struct {
				DeleteUncertain bool `json:"delete_uncertain"`
			}
			if decodeErr := json.Unmarshal(retry.Checkpoint, &checkpoint); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if checkpoint.DeleteUncertain {
				t.Fatal("a definitive AWS denial left cleanup unable to retry its original copy")
			}
			removed, err = backend.DeleteNative(t.Context(), principal, *retry, recovery.NativeWorkerControl{Check: func(ctx context.Context) error { return f.store.CheckNativeLease(ctx, cleanup, "deleting") }, Save: func(ctx context.Context, raw json.RawMessage) error {
				return f.store.SaveNativeCheckpoint(ctx, cleanup, raw)
			}})
		}

		if err != nil || !removed.ConfirmedAbsent {
			t.Fatal("RDS cleanup not confirmed", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, action := range []string{"RestoreDBInstanceFromDBSnapshot", "ModifyDBInstance", "DeleteDBInstance"} {
		want := 1
		if scenario == "network-before-admin" && action == "ModifyDBInstance" {
			want = 0
		}
		if writes[action] != want {
			t.Fatal("native mutation replayed or missing", action, writes[action])
		}
	}
}

type nativeRDSCheckpointExpiry struct {
	*recovery.PgStore
	expired bool
}

func (s *nativeRDSCheckpointExpiry) SaveNativeCheckpoint(ctx context.Context, r *recovery.NativeRestore, raw json.RawMessage) error {
	var checkpoint struct{ Stage string }
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		return err
	}
	if checkpoint.Stage == "ready" && !s.expired {
		s.expired = true
		return context.DeadlineExceeded
	}
	return s.PgStore.SaveNativeCheckpoint(ctx, r, raw)
}

// Fail one durable transition either before persistence or after a lost commit acknowledgment.
type nativeRDSCheckpointFailure struct {
	*recovery.PgStore
	scenario string
	failed   bool
}

func (s *nativeRDSCheckpointFailure) SaveNativeCheckpoint(ctx context.Context, r *recovery.NativeRestore, raw json.RawMessage) error {
	var checkpoint struct {
		Stage      string
		PhysicalID string `json:"physical_id"`
		Uncertain  bool
	}
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		return err
	}
	pin := checkpoint.Stage == "restore" && checkpoint.PhysicalID != ""
	matches := (strings.HasPrefix(s.scenario, "service-pin-") && pin) ||
		(s.scenario == "service-admin-checkpoint" && checkpoint.Stage == "admin" && !checkpoint.Uncertain) ||
		(s.scenario == "service-verify-checkpoint" && checkpoint.Stage == "verify") ||
		(s.scenario == "service-ready-checkpoint" && checkpoint.Stage == "ready")
	if !s.failed && matches {
		s.failed = true
		if s.scenario == "service-pin-committed" {
			if err := s.PgStore.SaveNativeCheckpoint(ctx, r, raw); err != nil {
				return err
			}
		}
		return fmt.Errorf("injected transient checkpoint failure")
	}
	return s.PgStore.SaveNativeCheckpoint(ctx, r, raw)
}
