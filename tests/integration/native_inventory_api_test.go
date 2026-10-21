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

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/recovery"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

func TestNativeRDSInventoryAPIKeepsHistoryAcrossRestartAndRemoval(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	fake := newFakeAWS(t)
	installFakeECS(t, fake)
	cfg.Clouds.AWSEndpoint = fake.srv.URL
	var mu sync.Mutex
	denied, foreign, reused, empty := false, false, false, false
	reads := 0
	fake.query["DescribeDBSnapshots"] = func(form url.Values) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		reads++
		if form.Get("DbiResourceId") == "" || form.Get("DBInstanceIdentifier") != "" {
			t.Error("snapshot request lost its physical pin")
		}
		if denied {
			return 403, `<ErrorResponse><Error><Code>AccessDenied</Code><Message>private provider detail</Message></Error></ErrorResponse>`
		}
		physical := form.Get("DbiResourceId")
		if foreign {
			physical = "db-foreign"
		}
		created := "2026-10-20T00:00:00Z"
		if reused {
			created = "2026-10-21T00:00:00Z"
		}
		body := fmt.Sprintf(`<DBSnapshot><DBSnapshotIdentifier>daily</DBSnapshotIdentifier><DBSnapshotArn>arn:aws:rds:us-east-1:%s:snapshot:daily</DBSnapshotArn><DBInstanceIdentifier>database</DBInstanceIdentifier><DbiResourceId>%s</DbiResourceId><SnapshotType>manual</SnapshotType><SnapshotCreateTime>%s</SnapshotCreateTime><Status>available</Status><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><Encrypted>true</Encrypted></DBSnapshot>`, awsAccount, physical, created)
		if empty {
			body = ""
		}
		return 200, `<DescribeDBSnapshotsResponse><DescribeDBSnapshotsResult><DBSnapshots>` + body + `</DBSnapshots></DescribeDBSnapshotsResult></DescribeDBSnapshotsResponse>`
	}
	running := start(t, cfg)
	defer func() { running.stop() }()
	c := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var connection connections.Connection
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "aws", "name": "Native history", "credential": awsKey(awsKeyID), "settings": map[string]string{"region": "us-east-1"}}, &connection)
	var key keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "Native inventory CLI", "access": "write"}, &key)
	lc := loomctl{t: t, url: running.srv.URL, key: key.Secret, config: filepath.Join(t.TempDir(), "loomctl.json")}
	var plan lpclient.Plan
	if err := json.Unmarshal([]byte(lc.must("services", "create", "Native orders", "--provider", "rds", "--target", connection.ID, "--json")), &plan); err != nil {
		t.Fatal(err)
	}
	lc.must("approve", plan.ID, "--yes")
	var sources struct {
		Sources []recovery.NativeSource `json:"sources"`
	}
	base := "/api/v1/projects/" + plan.ProjectID
	c.must(http.StatusOK, http.MethodGet, base+"/native-recovery-sources", nil, &sources)
	if len(sources.Sources) != 1 || sources.Sources[0].PhysicalID == "" || sources.Sources[0].State != "never" {
		t.Fatalf("no captured source: %+v", sources)
	}
	source := sources.Sources[0]
	path := "/api/v1/native-recovery-sources/" + source.ID
	var inventory recovery.NativeInventory
	c.must(http.StatusOK, http.MethodPost, path+"/refresh", nil, &inventory)
	if !inventory.Source.Complete || inventory.Source.ObservedAt == nil || len(inventory.Points) != 1 || !inventory.Points[0].Present {
		t.Fatalf("snapshot not captured: %+v", inventory)
	}
	original := inventory.Points[0].ID
	// Browser fixture and contracts use this actual API with saved native points.
	if strings.Contains(lc.must("backups", "native-inventory", source.ID, "--json"), original) == false {
		t.Fatal("CLI lost native identity")
	}
	if !strings.Contains(lc.must("backups", "native-sources", plan.ProjectID, "--json"), source.ID) {
		t.Fatal("CLI lost source history")
	}
	if !strings.Contains(lc.must("backups", "native-refresh", source.ID, "--json"), original) {
		t.Fatal("CLI refresh lost stable point")
	}
	// Another organization cannot read or refresh a captured history.
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	other := &client{t: t, base: c.base, key: cfg.Identity.PublishableKey}
	other.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "foreign-native@example.com", "password": "Correct-horse-battery-9"}, &auth)
	other.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	other.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Other native", "slug": "other-native"}, &org)
	other.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	mu.Lock()
	before := reads
	mu.Unlock()
	other.must(http.StatusNotFound, http.MethodGet, path, nil, nil)
	other.must(http.StatusNotFound, http.MethodPost, path+"/refresh", nil, nil)
	mu.Lock()
	if reads != before {
		t.Error("foreign organization reached provider")
	}
	mu.Unlock()
	for _, cause := range []string{"denied", "foreign"} {
		mu.Lock()
		denied, foreign = cause == "denied", cause == "foreign"
		mu.Unlock()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, c.base+path+"/refresh", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Publishable-Key", c.key)
		req.Header.Set("Authorization", "Bearer "+c.token)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("failed provider refresh status: %d", response.StatusCode)
		}
		c.must(http.StatusOK, http.MethodGet, path, nil, &inventory)
		if inventory.Source.State != "failed" || inventory.Source.Complete || len(inventory.Points) != 1 || inventory.Points[0].ID != original || strings.Contains(inventory.Source.Error, "private") {
			t.Fatal("failure erased history or exposed provider detail")
		}
	}
	mu.Lock()
	denied, foreign, reused = false, false, true
	mu.Unlock()
	c.must(http.StatusOK, http.MethodPost, path+"/refresh", nil, &inventory)
	if len(inventory.Points) != 2 || inventory.Points[0].ID == original || !inventory.Points[0].Present || inventory.Points[1].Present {
		t.Fatal("native name reuse collapsed old snapshot")
	}
	// Source deletion cannot erase its independent recovery history.
	db, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, deleteErr := db.Exec(t.Context(), `DELETE FROM loomplane_bindings WHERE org_id=$1 AND project_id=$2`, source.OrgID, plan.ProjectID); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	db.Close(t.Context())
	running.stop()
	running = start(t, cfg)
	c.base, lc.url = running.srv.URL, running.srv.URL
	c.must(http.StatusOK, http.MethodGet, path+"?limit=1", nil, &inventory)
	if len(inventory.Points) != 1 || inventory.NextBefore == "" || inventory.Source.PhysicalID != source.PhysicalID {
		t.Fatal("restart lost physical source or pagination")
	}
	c.must(http.StatusOK, http.MethodGet, path+"?limit=1&before="+inventory.NextBefore, nil, &inventory)
	if len(inventory.Points) != 1 || inventory.Points[0].ID != original {
		t.Fatal("next native point page lost earlier incarnation")
	}
	mu.Lock()
	empty = true
	mu.Unlock()
	c.must(http.StatusOK, http.MethodPost, path+"/refresh", nil, &inventory)
	if len(inventory.Points) != 2 || inventory.Points[0].Present || inventory.Points[1].Present || !inventory.Source.Complete {
		t.Fatal("complete empty inventory erased observations")
	}
	holdCatalogBrowser(t, running.srv.URL, plan.ProjectID, connection.ID)
}
