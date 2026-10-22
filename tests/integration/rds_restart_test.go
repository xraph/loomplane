//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	nativeaws "github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

// Controlled AWS HTTP with actual API/Dispatch/PostgreSQL/Vault and restart.
// It does not prove native SQL persistence or a successful snapshot restore.
func TestRDSAPIRetryKeepsItsPhysicalDatabaseAndFinalSnapshot(t *testing.T) {
	for _, scenario := range []string{"captured-create", "lost-create-response", "changed-incarnation", "protected-remove", "lost-delete-response", "foreign-snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Database.URL = freshDatabase(t)
			cfg.Storage.URL = "file://" + t.TempDir()
			cfg.Orchestration.PollInterval = 100 * time.Millisecond
			fake := newFakeAWS(t)
			cloud := installFakeECS(t, fake)
			cfg.Clouds.AWSEndpoint = fake.srv.URL
			var mu sync.Mutex
			blocked, created, removed := true, false, false
			nativeID := ""
			creates, deletes := 0, 0
			originalCreate, originalRead := fake.query["CreateDBInstance"], fake.query["DescribeDBInstances"]
			nativeError := func(code, message string) (int, string) {
				return http.StatusForbidden, `<ErrorResponse><Error><Code>` + code + `</Code><Message>` + message + `</Message></Error></ErrorResponse>`
			}
			fake.query["CreateDBInstance"] = func(form url.Values) (int, string) {
				mu.Lock()
				defer mu.Unlock()
				creates++
				created = true
				nativeID = form.Get("DBInstanceIdentifier")
				code, body := originalCreate(form)
				if scenario == "lost-create-response" {
					return 500, `<ErrorResponse><Error><Code>InternalFailure</Code><Message>creation response lost</Message></Error></ErrorResponse>`
				}
				return code, body
			}
			removing := strings.Contains(scenario, "remove") || strings.Contains(scenario, "delete") || scenario == "foreign-snapshot"
			fake.query["DescribeDBInstances"] = func(form url.Values) (int, string) {
				mu.Lock()
				defer mu.Unlock()
				if created && blocked && !removing {
					return nativeError("AccessDenied", "database polling temporarily denied")
				}
				code, body := originalRead(form)
				if scenario == "changed-incarnation" && !blocked {
					body = strings.ReplaceAll(body, "db-physical-"+nativeID, "db-recreated")
				}
				return code, body
			}
			fake.query["DescribeDBSnapshots"] = func(form url.Values) (int, string) {
				mu.Lock()
				defer mu.Unlock()
				if !removed {
					return 404, `<ErrorResponse><Error><Code>DBSnapshotNotFound</Code></Error></ErrorResponse>`
				}
				if blocked && scenario != "foreign-snapshot" {
					return nativeError("AccessDenied", "snapshot polling temporarily denied")
				}
				physical := "db-physical-" + nativeID
				if scenario == "foreign-snapshot" {
					physical = "db-foreign"
				}
				id := form.Get("DBSnapshotIdentifier")
				return 200, fmt.Sprintf(`<DescribeDBSnapshotsResponse><DescribeDBSnapshotsResult><DBSnapshots><DBSnapshot><DBSnapshotIdentifier>%s</DBSnapshotIdentifier><DBSnapshotArn>arn:aws:rds:us-east-1:%s:snapshot:%s</DBSnapshotArn><DBInstanceIdentifier>%s</DBInstanceIdentifier><DbiResourceId>%s</DbiResourceId><Status>available</Status><SnapshotType>manual</SnapshotType><Encrypted>true</Encrypted><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion></DBSnapshot></DBSnapshots></DescribeDBSnapshotsResult></DescribeDBSnapshotsResponse>`, id, awsAccount, id, nativeID, physical)
			}
			fake.query["DeleteDBInstance"] = func(form url.Values) (int, string) {
				mu.Lock()
				defer mu.Unlock()
				deletes++
				removed = true
				if form.Get("SkipFinalSnapshot") != "false" || form.Get("DeleteAutomatedBackups") != "false" || form.Get("FinalDBSnapshotIdentifier") != nativeaws.FinalDBSnapshotID("db-physical-"+nativeID) {
					t.Error("protected removal flags or snapshot identity changed")
				}
				cloud.mu.Lock()
				delete(cloud.dbs, nativeID)
				cloud.mu.Unlock()
				if scenario == "lost-delete-response" {
					return 500, `<ErrorResponse><Error><Code>InternalFailure</Code><Message>removal response lost</Message></Error></ErrorResponse>`
				}
				return 200, `<DeleteDBInstanceResponse><DeleteDBInstanceResult><DBInstance><DBInstanceIdentifier>` + nativeID + `</DBInstanceIdentifier><DbiResourceId>db-physical-` + nativeID + `</DbiResourceId><DBInstanceStatus>deleting</DBInstanceStatus></DBInstance></DeleteDBInstanceResult></DeleteDBInstanceResponse>`
			}
			running := start(t, cfg)
			defer func() { running.stop() }()
			c := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
			signedIn(t, c)
			var connection connections.Connection
			c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "aws", "name": "RDS", "credential": awsKey(awsKeyID), "settings": map[string]string{"region": "us-east-1"}}, &connection)
			var key keyBody
			c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "RDS restart", "access": "write"}, &key)
			cli := loomctl{t: t, url: running.srv.URL, key: key.Secret, config: filepath.Join(t.TempDir(), "loomctl.json")}
			var creation lpclient.Plan
			if err := json.Unmarshal([]byte(cli.must("services", "create", "RDS restart orders", "--provider", "rds", "--target", connection.ID, "--json")), &creation); err != nil {
				t.Fatal(err)
			}
			var plan planBody
			c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+creation.ID, nil, &plan)
			d := approveAndWait(t, c, plan)
			base := "/api/v1/projects/" + creation.ProjectID
			bindings := func() []planning.Binding {
				var response struct {
					Bindings []planning.Binding `json:"bindings"`
				}
				c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &response)
				return response.Bindings
			}
			if removing {
				if d.Status != "succeeded" {
					t.Fatalf("initial RDS creation failed: %+v", d)
				}
				bound := bindings()
				if len(bound) != 1 || bound[0].PhysicalID != "db-physical-"+nativeID {
					t.Fatal("RDS binding did not persist its immutable physical identity")
				}
				c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": "version: 1\nresources: {}\n"}, nil)
				c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]any{"environment": "production", "catalog": true}, &plan)
				waitPlanned(t, c, &plan)
				if !plan.Executable || len(plan.Actions) != 1 || plan.Actions[0].Op != "delete" {
					t.Fatalf("unreviewed removal: %+v", plan)
				}
				d = approveAndWait(t, c, plan)
			}
			if d.Status != "failed" {
				t.Fatalf("expected controlled provider failure, got %+v", d)
			}
			db, err := pgx.Connect(t.Context(), cfg.Database.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close(t.Context())
			var raw []byte
			if err := db.QueryRow(t.Context(), "SELECT steps FROM loomplane_deployments WHERE id=$1 AND project_id=$2", d.ID, creation.ProjectID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var steps []struct {
				Checkpoint []byte `json:"checkpoint"`
			}
			if err := json.Unmarshal(raw, &steps); err != nil || len(steps) != 1 {
				t.Fatalf("no persisted step: %v", err)
			}
			saved := steps[0].Checkpoint
			if !removing {
				var envelope struct {
					Apply []byte `json:"apply"`
				}
				if err := json.Unmarshal(saved, &envelope); err != nil {
					t.Fatal(err)
				}
				saved = envelope.Apply
			}
			var state struct {
				Version    int    `json:"rds_checkpoint"`
				Stage      string `json:"stage"`
				ResourceID string `json:"resource_id"`
				Attempted  bool   `json:"attempted"`
			}
			if err := json.Unmarshal(saved, &state); err != nil || state.Version != 1 || !state.Attempted {
				t.Fatalf("RDS operation lost its durable intent: %s %v", saved, err)
			}
			if scenario == "lost-create-response" {
				if state.ResourceID != "" {
					t.Fatal("an uncertain response invented physical identity")
				}
			} else if state.ResourceID != "db-physical-"+nativeID {
				t.Fatal("checkpoint has no original physical ID")
			}
			if removing && len(bindings()) != 1 {
				t.Fatal("failed removal forgot its binding before verified recovery")
			}
			running.stop()
			mu.Lock()
			blocked = false
			mu.Unlock()
			running = start(t, cfg)
			c.base = running.srv.URL
			c.must(http.StatusOK, http.MethodPost, "/api/v1/deployments/"+d.ID+"/retry", nil, &d)
			deadline := time.Now().Add(readyTimeout)
			for d.Status == "running" {
				if time.Now().After(deadline) {
					t.Fatalf("RDS retry still running: %+v", d)
				}
				time.Sleep(100 * time.Millisecond)
				c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+d.ID, nil, &d)
			}
			refused := scenario == "changed-incarnation" || scenario == "foreign-snapshot"
			if refused {
				if d.Status != "failed" || !strings.Contains(d.Error, "physical") {
					t.Fatalf("foreign native identity was accepted: %+v", d)
				}
			} else if d.Status != "succeeded" {
				t.Fatalf("RDS operation did not reconcile: %+v", d)
			}
			bound := bindings()
			if removing {
				want := 0
				if refused {
					want = 1
				}
				if len(bound) != want {
					t.Fatalf("removal released wrong binding state: %+v", bound)
				}
			} else if !refused && (len(bound) != 1 || bound[0].PhysicalID != "db-physical-"+nativeID) {
				t.Fatal("successful create has no original physical pin")
			}
			mu.Lock()
			defer mu.Unlock()
			if creates != 1 || (removing && deletes != 1) || (!removing && deletes != 0) {
				t.Fatalf("restart replayed native database mutations: creates=%d deletes=%d", creates, deletes)
			}
		})
	}
}
