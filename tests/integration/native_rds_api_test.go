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
	cleanupDenied := false
	mutations := map[string]int{}
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
	fake.query["DescribeDBSnapshots"] = func(form url.Values) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if form.Get("DbiResourceId") != "" && form.Get("DbiResourceId") != "db-source" {
			t.Error("snapshot read lost source pin")
		}
		return 200, fmt.Sprintf(`<DescribeDBSnapshotsResponse><DescribeDBSnapshotsResult><DBSnapshots><DBSnapshot><DBSnapshotIdentifier>original</DBSnapshotIdentifier><DBSnapshotArn>arn:aws:rds:us-east-1:%s:snapshot:original</DBSnapshotArn><DBInstanceIdentifier>%s</DBInstanceIdentifier><DbiResourceId>db-source</DbiResourceId><SnapshotType>manual</SnapshotType><SnapshotCreateTime>2026-10-22T00:00:00Z</SnapshotCreateTime><Status>available</Status><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><Encrypted>true</Encrypted><KmsKeyId>%s</KmsKeyId><AllocatedStorage>20</AllocatedStorage><MasterUsername>app</MasterUsername></DBSnapshot></DBSnapshots></DescribeDBSnapshotsResult></DescribeDBSnapshotsResponse>`, awsAccount, sourceName, kms)
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
	if len(inventory.Points) != 1 {
		t.Fatal("native RDS point missing")
	}
	var cloud cloudprofiles.Profile
	cloudBase := "/api/v1/projects/" + plan.ProjectID + "/clouds"
	c.must(201, http.MethodPost, cloudBase, map[string]string{"name": "Recovery"}, &cloud)
	var target cloudprofiles.Target
	c.must(201, http.MethodPost, cloudBase+"/"+cloud.ID+"/targets", cloudprofiles.CreateTargetInput{Name: "recovery", Runtime: "managed", Provider: "rds", ConnectionID: account.ID, Region: "us-east-1"}, &target)
	reviewFile := filepath.Join(t.TempDir(), "review.json")
	lc.must("backups", "native-copies", "review", source.ID, inventory.Points[0].ID, "--cloud", cloud.ID, "--target", target.ID, "--request-id", "rds-http-request", "--out", reviewFile)
	var copyRecord recovery.NativeRestore
	if err = json.Unmarshal([]byte(lc.must("backups", "native-copies", "create", "--review-file", reviewFile, "--acknowledge-charges", "--json")), &copyRecord); err != nil {
		t.Fatal(err)
	}
	wait := func(want string) {
		t.Helper()
		until := time.Now().Add(90 * time.Second)
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
	wait("succeeded")
	if copyRecord.VerifiedAt == nil || copyRecord.PhysicalID != "db-copy" {
		t.Fatal("Dispatch returned no SQL proof")
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
	var contract json.RawMessage
	c.must(200, http.MethodGet, "/openapi.json", nil, &contract)
	if path := os.Getenv("LOOMPLANE_TEST_OPENAPI_OUTPUT"); path != "" {
		if err = os.WriteFile(path, contract, 0600); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, kind := range []string{"restore", "password", "delete"} {
		if mutations[kind] != 1 {
			t.Fatal("native operation replayed", kind, mutations[kind])
		}
	}
}
