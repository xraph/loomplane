//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"

	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/platform"
	"github.com/xraph/loomplane/internal/recovery"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

func TestNativeRDSRecoveryHTTPCLIAndDispatchSurviveRestart(t *testing.T) {
	runNativeRDSRecoveryHTTP(t, "copy")
}
func TestNativeRDSPITRHTTPCLIAndDispatchRetainsExactTime(t *testing.T) {
	runNativeRDSRecoveryHTTP(t, "pitr")
}
func TestNativeRDSPITRRefusalHTTPCLIAndDispatchAllowsAbsentCleanup(t *testing.T) {
	runNativeRDSRecoveryHTTP(t, "pitr-refusal")
}
func TestNativeRDSRetentionHTTPCLIAndDispatchUsesVerifiedWitness(t *testing.T) {
	runNativeRDSRecoveryHTTP(t, "retention")
}
func TestNativeRDSRetentionPolicyHTTPCLIAndDispatch(t *testing.T) {
	runNativeRDSRecoveryHTTP(t, "policy")
}
func runNativeRDSRecoveryHTTP(t *testing.T, workflow string) {
	nativePITR := strings.HasPrefix(workflow, "pitr")
	nativeRefusal := workflow == "pitr-refusal"
	nativeRetention := workflow == "retention" || workflow == "policy"
	nativePolicy := workflow == "policy"
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	sql := nativeSQLFixtureFor(t)
	if _, err := sql.admin.Exec(t.Context(), `CREATE ROLE app LOGIN SUPERUSER PASSWORD 'private-source-password'`); err != nil {
		t.Fatal(err)
	}
	fake := newFakeAWS(t)
	installFakeECS(t, fake)
	cfg.Clouds.AWSEndpoint = fake.srv.URL
	kms := "arn:aws:kms:us-east-1:" + awsAccount + ":key/original"
	t.Cleanup(func() {
		if _, dropErr := sql.admin.Exec(context.Background(), "DROP ROLE app"); dropErr != nil {
			t.Error(dropErr)
		}
	})
	var mu sync.Mutex
	var sourceName, copyName, intent, adminPassword string
	exists := false
	windowExpired := false
	restoreAt := time.Now().UTC().Truncate(time.Second).Add(-24 * time.Hour)
	sourceCreated := restoreAt.Add(-30 * 24 * time.Hour)
	cleanupDenied := false
	mutations := map[string]int{}
	oldBackupExists, pointDeleteDenied := true, false
	oldPointTime := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Second)
	notFound := func() (int, string) {
		return 404, `<ErrorResponse><Error><Code>DBInstanceNotFound</Code></Error></ErrorResponse>`
	}
	fake.query["CreateDBInstance"] = func(form url.Values) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		sourceName = form.Get("DBInstanceIdentifier")
		return 200, `<CreateDBInstanceResponse><CreateDBInstanceResult>` + nativeRDSInstanceXML(sourceName, "db-source", "", kms) + `</CreateDBInstanceResult></CreateDBInstanceResponse>`
	}
	fake.query["DescribeDBInstances"] = func(form url.Values) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		name, physical := form.Get("DBInstanceIdentifier"), "db-copy"
		if name == sourceName && sourceName != "" {
			physical = "db-source"
		} else if !exists || name != copyName {
			return notFound()
		}
		return 200, `<DescribeDBInstancesResponse><DescribeDBInstancesResult><DBInstances>` + nativeRDSInstanceXML(name, physical, intent, kms) + `</DBInstances></DescribeDBInstancesResult></DescribeDBInstancesResponse>`
	}
	fake.query["DescribeDBInstanceAutomatedBackups"] = func(form url.Values) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if form.Get("DbiResourceId") != "db-source" {
			t.Error("PITR window lost immutable source", form)
		}
		body := ""
		if !windowExpired {
			body = fmt.Sprintf(`<DBInstanceAutomatedBackup><DBInstanceArn>arn:aws:rds:us-east-1:123456789012:db:%s</DBInstanceArn><DBInstanceIdentifier>%s</DBInstanceIdentifier><DbiResourceId>db-source</DbiResourceId><InstanceCreateTime>%s</InstanceCreateTime><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><Encrypted>true</Encrypted><KmsKeyId>%s</KmsKeyId><MasterUsername>app</MasterUsername><AllocatedStorage>20</AllocatedStorage><Status>active</Status><RestoreWindow><EarliestTime>%s</EarliestTime><LatestTime>%s</LatestTime></RestoreWindow></DBInstanceAutomatedBackup>`, sourceName, sourceName, sourceCreated.Format(time.RFC3339Nano), kms, restoreAt.Add(-24*time.Hour).Format(time.RFC3339Nano), restoreAt.Add(time.Hour).Format(time.RFC3339Nano))
		}
		return http.StatusOK, `<DescribeDBInstanceAutomatedBackupsResponse><DescribeDBInstanceAutomatedBackupsResult><DBInstanceAutomatedBackups>` + body + `</DBInstanceAutomatedBackups></DescribeDBInstanceAutomatedBackupsResult></DescribeDBInstanceAutomatedBackupsResponse>`
	}
	fake.query["DescribeDBSnapshots"] = func(form url.Values) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if form.Get("DbiResourceId") != "" && form.Get("DbiResourceId") != "db-source" {
			t.Error("snapshot read lost source pin")
		}
		selected := form.Get("DBSnapshotIdentifier")
		body := ""
		if selected == "" || selected == "original" {
			body += nativeRDSSnapshotXML("original", sourceName, kms, time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC))
		}
		if nativeRetention && oldBackupExists && (selected == "" || selected == "old-retained") {
			body += nativeRDSSnapshotXML("old-retained", sourceName, kms, oldPointTime)
		}
		if body == "" {
			return http.StatusNotFound, `<ErrorResponse><Error><Code>DBSnapshotNotFound</Code></Error></ErrorResponse>`
		}
		return http.StatusOK, `<DescribeDBSnapshotsResponse><DescribeDBSnapshotsResult><DBSnapshots>` + body + `</DBSnapshots></DescribeDBSnapshotsResult></DescribeDBSnapshotsResponse>`
	}
	fake.query["DeleteDBSnapshot"] = func(form url.Values) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if !nativeRetention || form.Get("DBSnapshotIdentifier") != "old-retained" {
			t.Error("retention touched another native recovery point", form)
		}
		db, err := pgx.Connect(t.Context(), cfg.Database.URL)
		if err != nil {
			t.Error(err)
			return http.StatusInternalServerError, ""
		}
		defer db.Close(context.Background())
		var durable bool
		if err = db.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM loomplane_native_retirements WHERE point->>'name'='old-retained' AND status='running' AND attempted)`).Scan(&durable); err != nil || !durable {
			t.Error("native point deletion preceded durable intent", err)
		}
		if !pointDeleteDenied {
			pointDeleteDenied = true
			return http.StatusForbidden, `<ErrorResponse><Error><Code>AccessDenied</Code></Error></ErrorResponse>`
		}
		mutations["point_delete"]++
		oldBackupExists = false
		return http.StatusOK, `<DeleteDBSnapshotResponse><DeleteDBSnapshotResult/></DeleteDBSnapshotResponse>`
	}
	fake.query["DescribeDBSubnetGroups"] = func(url.Values) (int, string) {
		return 200, `<DescribeDBSubnetGroupsResponse><DescribeDBSubnetGroupsResult><DBSubnetGroups><DBSubnetGroup><DBSubnetGroupName>recovery-private</DBSubnetGroupName><VpcId>vpc-recovery</VpcId><SubnetGroupStatus>Complete</SubnetGroupStatus></DBSubnetGroup></DBSubnetGroups></DescribeDBSubnetGroupsResult></DescribeDBSubnetGroupsResponse>`
	}
	fake.query["DescribeSecurityGroups"] = func(url.Values) (int, string) {
		return 200, `<DescribeSecurityGroupsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><securityGroupInfo><item><ownerId>123456789012</ownerId><groupId>sg-copy</groupId><vpcId>vpc-recovery</vpcId><ipPermissions><item><ipProtocol>tcp</ipProtocol><fromPort>5432</fromPort><toPort>5432</toPort><groups><item><userId>123456789012</userId><groupId>sg-worker</groupId></item></groups></item></ipPermissions></item><item><ownerId>123456789012</ownerId><groupId>sg-worker</groupId><vpcId>vpc-recovery</vpcId></item></securityGroupInfo></DescribeSecurityGroupsResponse>`
	}
	fake.query["RestoreDBInstanceFromDBSnapshot"] = func(form url.Values) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		mutations["restore"]++
		copyName, intent = form.Get("DBInstanceIdentifier"), form.Get("Tags.Tag.1.Value")
		exists = true
		return 200, `<RestoreDBInstanceFromDBSnapshotResponse><RestoreDBInstanceFromDBSnapshotResult>` + nativeRDSInstanceXML(copyName, "db-copy", intent, kms) + `</RestoreDBInstanceFromDBSnapshotResult></RestoreDBInstanceFromDBSnapshotResponse>`
	}
	fake.query["RestoreDBInstanceToPointInTime"] = func(form url.Values) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		mutations["restore"]++
		if !nativePITR || form.Get("SourceDbiResourceId") != "db-source" || form.Get("RestoreTime") != restoreAt.Format(time.RFC3339) || form.Get("UseLatestRestorableTime") != "false" || form.Get("SourceDBInstanceIdentifier") != "" || form.Get("PubliclyAccessible") != "false" || form.Get("Iops") != "3000" || form.Get("StorageThroughput") != "125" || form.Get("MaxAllocatedStorage") != "20" {
			t.Error("PITR native request changed reviewed time or isolated capacity", form)
		}
		copyName, intent = form.Get("TargetDBInstanceIdentifier"), form.Get("Tags.Tag.1.Value")
		if nativeRefusal {
			return http.StatusBadRequest, `<ErrorResponse><Error><Code>InstanceQuotaExceeded</Code></Error></ErrorResponse>`
		}
		exists = true
		// A provider may accept the request before its response is lost. The
		// next worker must observe the original intent rather than repost it.
		return http.StatusInternalServerError, `<ErrorResponse><Error><Code>InternalFailure</Code></Error></ErrorResponse>`
	}
	fake.query["ModifyDBInstance"] = func(form url.Values) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		mutations["password"]++
		if form.Get("DBInstanceIdentifier") != copyName {
			t.Error("password update touched source")
		}
		adminPassword = form.Get("MasterUserPassword")
		if _, err := sql.admin.Exec(t.Context(), "ALTER ROLE app PASSWORD '"+adminPassword+"'"); err != nil {
			t.Error(err)
		}
		return 200, `<ModifyDBInstanceResponse><ModifyDBInstanceResult>` + nativeRDSInstanceXML(copyName, "db-copy", intent, kms) + `</ModifyDBInstanceResult></ModifyDBInstanceResponse>`
	}
	fake.query["DeleteDBInstance"] = func(form url.Values) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if !cleanupDenied {
			cleanupDenied = true
			return 403, `<ErrorResponse><Error><Code>AccessDenied</Code></Error></ErrorResponse>`
		}
		mutations["delete"]++
		if form.Get("DBInstanceIdentifier") != copyName || form.Get("SkipFinalSnapshot") != "true" || form.Get("DeleteAutomatedBackups") != "true" {
			t.Error("temporary cleanup touched source retention")
		}
		exists = false
		return 200, `<DeleteDBInstanceResponse><DeleteDBInstanceResult>` + nativeRDSInstanceXML(copyName, "db-copy", intent, kms) + `</DeleteDBInstanceResult></DeleteDBInstanceResponse>`
	}
	transports := &deployments.NativeSQLTransports{RDS: func(_ context.Context, _ *aws.Session, instance, host string, port int32) (aws.DBConnector, error) {
		mu.Lock()
		defer mu.Unlock()
		if instance != copyName || host != copyName+".abcdef.us-east-1.rds.amazonaws.com" || port != 5432 {
			t.Error("incorrect SQL transport destination")
		}
		return nativeFixtureConnector{dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", sql.config.Host, sql.config.Port))
		}}, nil
	}}
	startApp := func() *running {
		app, err := platform.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err = forge.ProvideValue(app.Container(), transports); err != nil {
			t.Fatal(err)
		}
		lifetime, cancel := context.WithCancel(context.Background())
		if err = app.Start(lifetime); err != nil {
			cancel()
			t.Fatal(err)
		}
		srv := httptest.NewServer(app.Router().Handler())
		return &running{t: t, srv: srv, app: app, cancel: cancel}
	}
	running := startApp()
	defer func() { running.stop() }()
	c := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var contract json.RawMessage
	c.must(200, http.MethodGet, "/openapi.json", nil, &contract)
	if path := os.Getenv("LOOMPLANE_TEST_OPENAPI_OUTPUT"); path != "" {
		if writeErr := os.WriteFile(path, contract, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	settings := manageddb.RDSRecoveryNetwork{VPCID: "vpc-recovery", SubnetGroup: "recovery-private", SecurityGroupID: "sg-copy", VerifierGroupID: "sg-worker"}.Settings()
	settings["region"] = "us-east-1"
	var account connections.Connection
	c.must(201, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "aws", "name": "RDS recovery", "credential": awsKey(awsKeyID), "settings": settings}, &account)
	var apiKey keyBody
	c.must(201, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "Recovery CLI", "access": "write"}, &apiKey)
	lc := loomctl{t: t, url: c.base, key: apiKey.Secret, config: filepath.Join(t.TempDir(), "loomctl.json")}
	var plan lpclient.Plan
	if err := json.Unmarshal([]byte(lc.must("services", "create", "Native RDS orders", "--provider", "rds", "--target", account.ID, "--json")), &plan); err != nil {
		t.Fatal(err)
	}
	lc.must("approve", plan.ID, "--yes")
	var sources struct {
		Sources []recovery.NativeSource `json:"sources"`
	}
	c.must(200, http.MethodGet, "/api/v1/projects/"+plan.ProjectID+"/native-recovery-sources", nil, &sources)
	if len(sources.Sources) != 1 {
		t.Fatal("retained RDS source missing")
	}
	source := sources.Sources[0]
	database := strings.ReplaceAll(source.Name, "-", "_")
	if _, err := sql.admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, dropErr := sql.admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{database}.Sanitize()+" WITH (FORCE)"); dropErr != nil {
			t.Error(dropErr)
		}
	})
	dbConfig := sql.config.Copy()
	dbConfig.Database = database
	data, err := pgx.ConnectConfig(t.Context(), dbConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = data.Exec(t.Context(), `CREATE TABLE recovered_rows(id int PRIMARY KEY,value text);INSERT INTO recovered_rows VALUES(7,'retained-after-restore')`); err != nil {
		t.Fatal(err)
	}
	_ = data.Close(t.Context())
	var inventory recovery.NativeInventory
	c.must(200, http.MethodPost, "/api/v1/native-recovery-sources/"+source.ID+"/refresh", nil, &inventory)
	expectedPoints := 1
	if nativeRetention {
		expectedPoints = 2
	}
	if len(inventory.Points) != expectedPoints {
		t.Fatal("native RDS point missing", len(inventory.Points))
	}
	var cloud cloudprofiles.Profile
	cloudBase := "/api/v1/projects/" + plan.ProjectID + "/clouds"
	c.must(201, http.MethodPost, cloudBase, map[string]string{"name": "Recovery"}, &cloud)
	var target cloudprofiles.Target
	c.must(201, http.MethodPost, cloudBase+"/"+cloud.ID+"/targets", cloudprofiles.CreateTargetInput{Name: "recovery", Runtime: "managed", Provider: "rds", ConnectionID: account.ID, Region: "us-east-1"}, &target)
	reviewFile := filepath.Join(t.TempDir(), "review.json")
	pointID := inventory.Points[0].ID
	reviewArgs := []string{"backups", "native-copies", "review", source.ID, pointID, "--cloud", cloud.ID, "--target", target.ID, "--request-id", "rds-http-request", "--out", reviewFile}
	if nativePITR {
		var window recovery.NativePITRWindow
		if decodeErr := json.Unmarshal([]byte(lc.must("backups", "native-pitr", source.ID, "--json")), &window); decodeErr != nil || !window.Available || window.PointID == "" {
			t.Fatal("public PITR window missing", window, decodeErr)
		}
		reviewArgs[4] = window.PointID
		reviewArgs = append(reviewArgs, "--restore-time", restoreAt.Format(time.RFC3339))
		base := "/api/v1/native-recovery-sources/" + source.ID + "/points/" + window.PointID + "/copy-review"
		c.must(http.StatusUnprocessableEntity, http.MethodPost, base, recovery.NativeCopyReviewInput{RequestID: "no-time-request", CloudID: cloud.ID, TargetID: target.ID, TTLHours: 1}, nil)
	}
	lc.must(reviewArgs...)
	var copyRecord recovery.NativeRestore
	if err = json.Unmarshal([]byte(lc.must("backups", "native-copies", "create", "--review-file", reviewFile, "--acknowledge-charges", "--json")), &copyRecord); err != nil {
		t.Fatal(err)
	}
	wait := func(want string) {
		t.Helper()
		until := time.Now().Add(90 * time.Second)
		if nativeRefusal {
			until = time.Now().Add(20 * time.Second)
		}
		for {
			c.must(200, http.MethodGet, "/api/v1/native-recovery-copies/"+copyRecord.ID, nil, &copyRecord)
			if copyRecord.Status == want {
				return
			}
			if copyRecord.Status == "failed" || time.Now().After(until) {
				t.Fatal("copy did not reach expected status", want, copyRecord.Status, copyRecord.Error)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if nativeRefusal {
		wait("failed")
		control, connectErr := pgx.Connect(t.Context(), cfg.Database.URL)
		if connectErr != nil {
			t.Fatal(connectErr)
		}
		defer control.Close(context.Background())
		assertRefused := func() {
			t.Helper()
			var checkpoint struct {
				Uncertain  bool   `json:"uncertain"`
				PhysicalID string `json:"physical_id"`
			}
			var raw []byte
			if queryErr := control.QueryRow(t.Context(), `SELECT checkpoint FROM loomplane_native_restores WHERE id=$1`, copyRecord.ID).Scan(&raw); queryErr != nil || json.Unmarshal(raw, &checkpoint) != nil || checkpoint.Uncertain || checkpoint.PhysicalID != "" {
				t.Fatal("known refusal retained creation uncertainty", string(raw), queryErr)
			}
			var replay recovery.NativeRestore
			if decodeErr := json.Unmarshal([]byte(lc.must("backups", "native-copies", "create", "--review-file", reviewFile, "--acknowledge-charges", "--json")), &replay); decodeErr != nil || replay.ID != copyRecord.ID || replay.Status != "failed" || !replay.ExpiresAt.Equal(copyRecord.ExpiresAt) {
				t.Fatal("refusal replay created new intent or extended expiry", replay, decodeErr)
			}
			lc.must("backups", "native-copies", "cleanup", copyRecord.ID, "--yes", "--wait")
			wait("deleted")
			var kept bool
			key := deployments.ResourceKey(source.OrgID, source.EnvironmentID, "native-copy:"+copyRecord.ID, "credentials")
			if queryErr := control.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM vault_secrets WHERE app_id='loomplane-resource-credentials' AND key=$1)`, key).Scan(&kept); queryErr != nil || kept {
				t.Fatal("absent refused copy retained credentials", queryErr)
			}
		}
		assertRefused()
		firstID := copyRecord.ID
		reviewFile = filepath.Join(t.TempDir(), "new-review.json")
		for index, value := range reviewArgs {
			if value == "--request-id" {
				reviewArgs[index+1] = "rds-new-explicit-request"
			}
			if value == "--out" {
				reviewArgs[index+1] = reviewFile
			}
		}
		lc.must(reviewArgs...)
		if decodeErr := json.Unmarshal([]byte(lc.must("backups", "native-copies", "create", "--review-file", reviewFile, "--acknowledge-charges", "--json")), &copyRecord); decodeErr != nil || copyRecord.ID == firstID {
			t.Fatal("new explicit request did not create separate intent", decodeErr)
		}
		wait("failed")
		assertRefused()
		mu.Lock()
		defer mu.Unlock()
		if mutations["restore"] != 2 || mutations["password"] != 0 || mutations["delete"] != 0 || exists {
			t.Fatal("known refusal reposted or touched an instance", mutations, exists)
		}
		return
	}
	wait("succeeded")
	if copyRecord.VerifiedAt == nil || copyRecord.PhysicalID != "db-copy" {
		t.Fatal("Dispatch returned no SQL proof")
	}
	if nativePITR {
		if copyRecord.RestoreTime == nil || !copyRecord.RestoreTime.Equal(restoreAt) {
			t.Fatal("public PITR copy lost exact time", copyRecord.RestoreTime)
		}
		mu.Lock()
		windowExpired = true
		mu.Unlock()
		var unavailable recovery.NativePITRWindow
		c.must(http.StatusOK, http.MethodGet, "/api/v1/native-recovery-sources/"+source.ID+"/pitr-window", nil, &unavailable)
		if unavailable.Available || unavailable.PointID != "" {
			t.Fatal("expired window reused cached availability", unavailable)
		}
	}
	running.stop()
	running = startApp()
	c.base, lc.url = running.srv.URL, running.srv.URL
	output := filepath.Join(t.TempDir(), "reader.json")
	lc.must("backups", "native-copies", "credentials", copyRecord.ID, "--out", output)
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var credentials recovery.NativeCopyCredentials
	if err = json.Unmarshal(raw, &credentials); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	copyAdmin := adminPassword
	mu.Unlock()
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0600 || credentials.Provider != "rds" || credentials.Password == copyAdmin {
		t.Fatal("CLI did not keep private reader credentials", err)
	}
	readerConfig := dbConfig.Copy()
	readerConfig.User, readerConfig.Password = credentials.Username, credentials.Password
	reader, err := pgx.ConnectConfig(t.Context(), readerConfig)
	if err != nil {
		t.Fatal(err)
	}
	var retained string
	if err = reader.QueryRow(t.Context(), `SELECT value FROM recovered_rows WHERE id=7`).Scan(&retained); err != nil || retained != "retained-after-restore" {
		t.Fatal("reader could not read copied SQL fixture", err)
	}
	_ = reader.Close(t.Context())
	var readKey keyBody
	c.must(201, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "Reader", "access": "read"}, &readKey)
	readOnly := &client{t: t, base: c.base, key: c.key, token: readKey.Secret}
	readOnly.must(403, http.MethodPost, "/api/v1/native-recovery-copies/"+copyRecord.ID+"/credentials", nil, nil)
	if _, err = sql.admin.Exec(t.Context(), "DROP ROLE "+pgx.Identifier{credentials.Username}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	request, requestErr := http.NewRequestWithContext(t.Context(), http.MethodPost, c.base+"/api/v1/native-recovery-copies/"+copyRecord.ID+"/credentials", nil)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	request.Header.Set("X-Publishable-Key", c.key)
	request.Header.Set("Authorization", "Bearer "+c.token)
	denied, deniedErr := http.DefaultClient.Do(request)
	if deniedErr != nil {
		t.Fatal(deniedErr)
	}
	_ = denied.Body.Close()
	if denied.StatusCode != http.StatusServiceUnavailable || denied.Header.Get("Cache-Control") != "no-store, private" {
		t.Fatal("removed reader was disclosed or its denial cached", denied.StatusCode)
	}
	var roleExists bool
	if err = sql.admin.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, credentials.Username).Scan(&roleExists); err != nil || roleExists {
		t.Fatal("credential disclosure recreated reader", err)
	}
	c.must(200, http.MethodDelete, "/api/v1/clouds/"+cloud.ID+"/projects/"+source.ProjectID, nil, nil)
	c.must(202, http.MethodDelete, "/api/v1/native-recovery-copies/"+copyRecord.ID, nil, nil)
	wait("cleanup_failed")
	controlDB, controlErr := pgx.Connect(t.Context(), cfg.Database.URL)
	if controlErr != nil {
		t.Fatal(controlErr)
	}
	defer controlDB.Close(context.Background())
	secretKey := deployments.ResourceKey(source.OrgID, source.EnvironmentID, "native-copy:"+copyRecord.ID, "credentials")
	secretExists := func() bool {
		var kept bool
		if queryErr := controlDB.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM vault_secrets WHERE app_id='loomplane-resource-credentials' AND key=$1)`, secretKey).Scan(&kept); queryErr != nil {
			t.Fatal(queryErr)
		}
		return kept
	}
	if !secretExists() {
		t.Fatal("denied cleanup discarded copy credentials")
	}
	lc.must("backups", "native-copies", "cleanup", copyRecord.ID, "--yes", "--wait")
	wait("deleted")
	if secretExists() {
		t.Fatal("confirmed RDS cleanup retained copy credentials")
	}
	if nativeRetention {
		base := "/api/v1/native-recovery-sources/" + source.ID
		var preview recovery.RetentionPreview
		c.must(http.StatusOK, http.MethodGet, base+"/retention-preview?older_than_days=7&keep_last=1", nil, &preview)
		if !preview.Complete || preview.EligibleCount != 1 || preview.VerifiedBackupID != inventory.Points[0].ID {
			t.Fatal("native retention lost the SQL-verified witness", preview)
		}
		oldPointID := ""
		for _, point := range inventory.Points {
			if point.Name == "old-retained" {
				oldPointID = point.ID
			}
		}
		endpoint := base + "/points/" + oldPointID + "/retirements"
		input := recovery.NativeRetirementInput{RequestID: "rds-native-retire-request", Policy: recovery.RetentionInput{OlderThanDays: 7, KeepLast: 1}}
		c.must(http.StatusUnprocessableEntity, http.MethodPost, endpoint, input, nil)
		input.AcknowledgeDeletion = true
		readOnly.must(http.StatusForbidden, http.MethodPost, endpoint, input, nil)
		var retirement recovery.NativeRetirement
		if nativePolicy {
			policyPath := base + "/retention-policy"
			readOnly.must(http.StatusForbidden, http.MethodPut, policyPath, recovery.RetentionPolicyInput{Policy: input.Policy, Enabled: true}, nil)
			lc.must("backups", "native-retention", "policy-set", source.ID, "--yes", "--revision", "0", "--older-than-days", "7", "--keep-last", "1", "--json")
			var policy recovery.NativeRetentionPolicyState
			until := time.Now().Add(100 * time.Second)
			for {
				c.must(http.StatusOK, http.MethodGet, policyPath, nil, &policy)
				if policy.Policy != nil && policy.Policy.LastRetirementID != "" {
					break
				}
				if time.Now().After(until) {
					t.Fatal("native policy did not reach the real minute reconciliation", policy)
				}
				time.Sleep(100 * time.Millisecond)
			}
			retirement.ID = policy.Policy.LastRetirementID
			lc.must("backups", "native-retention", "policy-pause", source.ID, "--yes", "--json")
			c.must(http.StatusOK, http.MethodGet, policyPath, nil, &policy)
			if policy.Policy == nil || policy.Policy.Enabled || policy.Policy.Revision != 2 {
				t.Fatal("native policy pause did not persist", policy)
			}
			var revisions recovery.RetentionPolicyHistory
			c.must(http.StatusOK, http.MethodGet, policyPath+"/history?limit=100", nil, &revisions)
			if len(revisions.Events) != 2 || revisions.Events[0].Enabled || !revisions.Events[1].Enabled {
				t.Fatal("native policy lost its authorization history", revisions)
			}
		} else {
			c.must(http.StatusAccepted, http.MethodPost, endpoint, input, &retirement)
		}
		waitRetirement := func(want string) {
			t.Helper()
			until := time.Now().Add(90 * time.Second)
			for {
				c.must(http.StatusOK, http.MethodGet, "/api/v1/native-recovery-retirements/"+retirement.ID, nil, &retirement)
				if retirement.Status == want {
					return
				}
				if time.Now().After(until) {
					t.Fatal("native retirement did not finish", want, retirement.Status, retirement.Error)
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
		waitRetirement("failed")
		lc.must("backups", "native-retention", "retry", retirement.ID, "--yes", "--wait")
		waitRetirement("succeeded")
		if !nativePolicy {
			var replay recovery.NativeRetirement
			c.must(http.StatusAccepted, http.MethodPost, endpoint, input, &replay)
			if replay.ID != retirement.ID || replay.WitnessID != inventory.Points[0].ID {
				t.Fatal("native deletion replay changed its witness or operation")
			}
		} else if retirement.WitnessID != inventory.Points[0].ID || retirement.PolicyID == "" || retirement.PolicyRevision != 1 {
			t.Fatal("native policy deletion lost its captured authority", retirement)
		}
		var history recovery.RetirementHistory
		c.must(http.StatusOK, http.MethodGet, "/api/v1/native-recovery-retirements/"+retirement.ID+"/history?limit=100", nil, &history)
		if len(history.Events) < 5 {
			t.Fatal("native deletion lost committed history", history)
		}
		c.must(http.StatusOK, http.MethodPut, "/api/v1/clouds/"+cloud.ID+"/projects/"+source.ProjectID, map[string]any{"target_ids": []string{target.ID}}, nil)
		c.must(http.StatusConflict, http.MethodPost, base+"/points/"+oldPointID+"/copy-review", recovery.NativeCopyReviewInput{RequestID: "retired-copy-request", CloudID: cloud.ID, TargetID: target.ID, TTLHours: 1}, nil)
		mu.Lock()
		if oldBackupExists || mutations["point_delete"] != 1 {
			t.Error("native deletion was not exact", oldBackupExists, mutations["point_delete"])
		}
		mu.Unlock()
	}
	mu.Lock()
	defer mu.Unlock()
	for _, kind := range []string{"restore", "password", "delete"} {
		if mutations[kind] != 1 {
			t.Fatal("native operation replayed", kind, mutations[kind])
		}
	}
}
