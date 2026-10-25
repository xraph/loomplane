//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/platform"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recovery"
)

// The retained source and Google API responses are fixtures. Recovery admission,
// signed CLI review, Dispatch, Vault and PostgreSQL authentication are real.
func runCloudSQLPITRHTTP(t *testing.T, f nativePointFixture, sql nativeSQLFixture, binding planning.Binding, resource planning.ResourceConfig, endpoint string, selected time.Time, transport func(context.Context, *gcp.Session, string, string) (gcp.SQLConnector, error)) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Database.URL = f.sql.Config().ConnString()
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Orchestration.PollInterval = 50 * time.Millisecond
	cfg.Clouds.GoogleEndpoint = endpoint
	startApp := func() *running {
		app, err := platform.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err = forge.ProvideValue(app.Container(), &deployments.NativeSQLTransports{CloudSQL: transport}); err != nil {
			t.Fatal(err)
		}
		lifetime, cancel := context.WithCancel(context.Background())
		if err = app.Start(lifetime); err != nil {
			cancel()
			t.Fatal(err)
		}
		return &running{t: t, srv: httptest.NewServer(app.Router().Handler()), app: app, cancel: cancel}
	}
	app := startApp()
	defer func() { app.stop() }()
	c := &client{t: t, base: app.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var project projects.Project
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "PITR API recovery"}, &project)
	var environments struct {
		Environments []projects.Environment `json:"environments"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project.ID+"/environments", nil, &environments)
	var environment string
	for _, candidate := range environments.Environments {
		if candidate.Name == "production" {
			environment = candidate.ID
		}
	}
	if environment == "" {
		t.Fatal("API did not create the production environment")
	}
	var account connections.Connection
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{
		"cloud": "gcp", "name": "PITR source account", "credential": string(nativeSQLAccountKey(t, "source-project", endpoint+"/token")),
		"settings": map[string]string{"cloud_sql_recovery_network": "projects/worker-project/global/networks/recovery"},
	}, &account)
	resource.Managed.ConnectionID = account.ID
	resource.Managed.AccountIdentity = connections.AccountIdentity(account.Cloud, account.Account, account.Details)
	binding.ID, binding.OrgID, binding.ProjectID, binding.EnvironmentID, binding.ConnectionID = "psc-http-source", project.OrgID, project.ID, environment, account.ID
	var err error
	binding.Config, err = json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	if err = planning.NewPgStore(f.db).PutBinding(t.Context(), &binding); err != nil {
		t.Fatal(err)
	}
	var sources recovery.NativeSourcesPage
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project.ID+"/native-recovery-sources", nil, &sources)
	if len(sources.Sources) != 1 {
		t.Fatal("retained PSC source missing", sources)
	}
	source := sources.Sources[0]
	var key keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "PITR CLI", "access": "write"}, &key)
	lc := loomctl{t: t, url: c.base, key: key.Secret, config: filepath.Join(t.TempDir(), "loomctl.json")}
	var window recovery.NativePITRWindow
	if err = json.Unmarshal([]byte(lc.must("backups", "native-pitr", source.ID, "--json")), &window); err != nil || !window.Available || window.PointID == "" {
		t.Fatal("CLI did not discover a PSC PITR window", window, err)
	}
	var cloud cloudprofiles.Profile
	base := "/api/v1/projects/" + project.ID + "/clouds"
	c.must(http.StatusCreated, http.MethodPost, base, map[string]string{"name": "Recovery"}, &cloud)
	var target cloudprofiles.Target
	c.must(http.StatusCreated, http.MethodPost, base+"/"+cloud.ID+"/targets", cloudprofiles.CreateTargetInput{Name: "recovery", Runtime: "managed", Provider: "cloud-sql", ConnectionID: account.ID, Region: "us-central1"}, &target)
	reviewFile := filepath.Join(t.TempDir(), "review.json")
	lc.must("backups", "native-copies", "review", source.ID, window.PointID, "--cloud", cloud.ID, "--target", target.ID, "--request-id", "pitr-http-request", "--restore-time", selected.Format(time.RFC3339Nano), "--out", reviewFile)
	var review recovery.NativeCopyReview
	raw, err := os.ReadFile(reviewFile)
	if err != nil || json.Unmarshal(raw, &review) != nil || review.RestoreTime == nil || !review.RestoreTime.Equal(selected) || review.Target.CloudSQLNetwork.Network != "projects/worker-project/global/networks/recovery" {
		t.Fatal("signed review lost exact time or recovery network", err)
	}
	var copyRecord recovery.NativeRestore
	if err = json.Unmarshal([]byte(lc.must("backups", "native-copies", "create", "--review-file", reviewFile, "--acknowledge-charges", "--json")), &copyRecord); err != nil {
		t.Fatal(err)
	}
	copyPath := "/api/v1/native-recovery-copies/" + copyRecord.ID
	wait := func(status string) {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for {
			c.must(http.StatusOK, http.MethodGet, copyPath, nil, &copyRecord)
			if copyRecord.Status == status {
				return
			}
			if copyRecord.Status == "failed" || copyRecord.Status == "cleanup_failed" || time.Now().After(deadline) {
				t.Fatal("PITR worker did not reach", status, copyRecord.Status, copyRecord.Error)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	wait("succeeded")
	if copyRecord.VerifiedAt == nil || copyRecord.RestoreTime == nil || !copyRecord.RestoreTime.Equal(selected) {
		t.Fatal("verified copy lost its PITR time")
	}
	app.stop()
	app = startApp()
	c.base, lc.url = app.srv.URL, app.srv.URL
	credentialFile := filepath.Join(t.TempDir(), "credentials.json")
	output := lc.must("backups", "native-copies", "credentials", copyRecord.ID, "--out", credentialFile)
	raw, err = os.ReadFile(credentialFile)
	if err != nil {
		t.Fatal(err)
	}
	var credentials recovery.NativeCopyCredentials
	if err = json.Unmarshal(raw, &credentials); err != nil || !credentials.PrivateServiceConnect || credentials.Database != sql.config.Database || credentials.Password == "" || strings.Contains(output, credentials.Password) {
		t.Fatal("CLI credential file lost its PSC contract or leaked its password", err)
	}
	t.Cleanup(func() {
		for _, role := range []string{credentials.Username, "postgres"} {
			if _, dropErr := sql.admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize()); dropErr != nil {
				t.Error(dropErr)
			}
		}
	})
	info, err := os.Stat(credentialFile)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("CLI did not protect its credential file", err)
	}
	readerConfig := sql.config.Copy()
	readerConfig.User, readerConfig.Password = credentials.Username, credentials.Password
	reader, err := pgx.ConnectConfig(t.Context(), readerConfig)
	if err != nil {
		t.Fatal(err)
	}
	var value string
	err = reader.QueryRow(t.Context(), `SELECT value FROM public.recovered_rows WHERE id=7`).Scan(&value)
	_ = reader.Close(t.Context())
	if err != nil || value != "retained-after-restore" {
		t.Fatal("restarted API credentials cannot read the verified copy", err)
	}
	var replay recovery.NativeRestore
	if err = json.Unmarshal([]byte(lc.must("backups", "native-copies", "create", "--review-file", reviewFile, "--acknowledge-charges", "--json")), &replay); err != nil || replay.ID != copyRecord.ID || !replay.ExpiresAt.Equal(copyRecord.ExpiresAt) {
		t.Fatal("signed request replay changed copy identity or expiry", err)
	}
	var readKey keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "PITR reader", "access": "read"}, &readKey)
	denied := &client{t: t, base: c.base, key: c.key, token: readKey.Secret}
	denied.must(http.StatusForbidden, http.MethodPost, copyPath+"/credentials", nil, nil)
	denied.must(http.StatusForbidden, http.MethodDelete, copyPath, nil, nil)
	lc.must("backups", "native-copies", "cleanup", copyRecord.ID, "--yes", "--wait")
	wait("deleted")
	c.must(http.StatusConflict, http.MethodPost, copyPath+"/credentials", nil, nil)
	secretKey := deployments.ResourceKey(project.OrgID, environment, "native-copy:"+copyRecord.ID, "credentials")
	var kept bool
	if err = f.sql.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM vault_secrets WHERE app_id='loomplane-resource-credentials' AND key=$1)`, secretKey).Scan(&kept); err != nil || kept {
		t.Fatal("confirmed cleanup retained copy secrets", err)
	}
	for _, jobName := range []string{"loomplane.service.native.restore", "loomplane.service.native.cleanup"} {
		var count int
		if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM dispatch_jobs WHERE name=$1 AND convert_from(payload,'UTF8')::jsonb->>'id'=$2 AND max_retries=0`, jobName, copyRecord.ID).Scan(&count); err != nil || count < 1 {
			t.Fatal("workflow bypassed Dispatch", jobName, count, err)
		}
	}
}
