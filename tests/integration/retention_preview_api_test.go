//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/ctl"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recovery"
)

func TestRetentionPreviewAPIIsReadOnlyAndScoped(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	running := start(t, cfg)
	defer running.stop()
	c := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var session struct {
		OrgID string `json:"org_id"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/session", nil, &session)
	var project struct{ ID string }
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Retention preview"}, &project)
	waitReady(t, c, project.ID)
	db, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var environment string
	if err = db.QueryRow(t.Context(), `SELECT id FROM loomplane_environments WHERE org_id=$1 AND project_id=$2 AND name='production'`, session.OrgID, project.ID).Scan(&environment); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(t.Context(), `INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('retention-connection',$1,'server','Retention metadata fixture','fixture','ready','fixture',now(),now())`, session.OrgID); err != nil {
		t.Fatal(err)
	}
	recipe, err := recipes.Lookup("postgres", "17", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	resource, err := json.Marshal(planning.ResourceConfig{Provider: "server", Kind: "postgres", Target: "retention-connection", Catalog: recipe})
	if err != nil {
		t.Fatal(err)
	}
	source, err := json.Marshal(planning.Binding{ID: "binding", OrgID: session.OrgID, ProjectID: project.ID, EnvironmentID: environment, Name: "database", Subject: planning.ResourceSubject, ExternalID: "instance", Provider: "server", ConnectionID: "retention-connection", Config: resource})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(t.Context(), `INSERT INTO loomplane_service_backups(id,org_id,project_id,environment_id,connection_id,name,status,request_id,source,object,token,created_by,created_at,completed_at,restored_at)
 SELECT 'point'||n,$1,$2,$3,'retention-connection','database','succeeded','request'||n,$4::jsonb,
 jsonb_build_object('key',$1||'/'||$2||'/'||$3||'/point'||n||'/attempt.dump','size',3,'sha256',repeat('a',64)),
 'attempt','fixture',now()-n*interval '10 days'-interval '1 minute',now()-n*interval '10 days',CASE WHEN n=3 THEN now() END FROM generate_series(1,3) n`, session.OrgID, project.ID, environment, source); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/service-backups/point1/retention-preview?older_than_days=7&keep_last=1"
	var preview recovery.RetentionPreview
	c.must(http.StatusOK, http.MethodGet, path, nil, &preview)
	if !preview.Complete || preview.EligibleCount != 1 || preview.EligibleBytes != 3 || preview.VerifiedBackupID != "point3" {
		t.Fatalf("read-only retention preview: %+v", preview)
	}
	for _, point := range preview.Points {
		if point.Eligible && point.BackupID != "point2" {
			t.Fatalf("newest or tested point selected: %+v", point)
		}
	}
	var count int
	if err = db.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_service_backups WHERE status='succeeded'`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("preview changed recovery records: %d %v", count, err)
	}
	c.must(http.StatusUnprocessableEntity, http.MethodGet, "/api/v1/service-backups/point1/retention-preview?older_than_days=7&keep_last=0", nil, nil)
	c.must(http.StatusBadRequest, http.MethodGet, "/api/v1/service-backups/point1/retention-preview?older_than_days=nope&keep_last=1", nil, nil)
	var key struct {
		Secret string `json:"secret"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "Retention preview reader", "access": "read"}, &key)
	var output bytes.Buffer
	env := map[string]string{ctl.EnvURL: running.srv.URL, ctl.EnvKey: key.Secret}
	app := ctl.New(ctl.Options{Out: &output, ConfigPath: t.TempDir() + "/loomctl.json", Getenv: func(k string) string { return env[k] }})
	if err = app.Run([]string{"backups", "retention", "point1", "--older-than-days", "7", "--keep-last", "1", "--json"}); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(output.Bytes(), &preview); err != nil || preview.EligibleCount != 1 || preview.VerifiedBackupID != "point3" {
		t.Fatalf("CLI read-only preview: %+v %v", preview, err)
	}
	var other struct{ ID string }
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Other", "slug": "other"}, &other)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": other.ID}, nil)
	c.must(http.StatusNotFound, http.MethodGet, path, nil, nil)
	holdCatalogBrowser(t, running.srv.URL, project.ID, "retention-connection")
}
