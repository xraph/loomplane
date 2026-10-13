//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/platform"
)

func TestOps_aBackupRestoresIntoAnEmptyDatabase(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	// Attaching the repository checks GitHub can read it.
	cfg.GitHub.APIURL = fakeGitHubRepos(t, map[string][]byte{"acme/mono": exampleTarball(t)}).URL
	cfg.Analysis.WorkDir = t.TempDir()
	r := start(t, cfg)

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Engines", "slug": "engines"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+proj.ID+"/variables",
		map[string]any{"key": "API_TOKEN", "value": "kept-secret", "secret": true}, nil)
	var key keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "ci", "access": "read"}, &key)

	// Back up while it runs: the copy is one snapshot.
	var backup bytes.Buffer
	ctx := context.Background()
	m, err := ops.Backup(ctx, cfg.Database.URL, "test", &backup)
	if err != nil {
		t.Fatal(err)
	}
	r.stop()
	if len(m.Migrations) == 0 || len(m.Tables) < 20 {
		t.Fatalf("manifest %d migrations, %d tables", len(m.Migrations), len(m.Tables))
	}
	if inspected, ierr := ops.Inspect(bytes.NewReader(backup.Bytes())); ierr != nil || inspected.CreatedAt != m.CreatedAt {
		t.Fatalf("inspect %+v, %v", inspected, ierr)
	}

	// An empty database: restore migrates it first.
	restored := cfg
	restored.Database.URL = freshDatabase(t)
	if _, err := ops.Restore(ctx, restored.Database.URL, migrations.All(), bytes.NewReader(backup.Bytes()), ops.RestoreOptions{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	// Now it has data, so a second restore needs --force.
	if _, err := ops.Restore(ctx, restored.Database.URL, migrations.All(), bytes.NewReader(backup.Bytes()), ops.RestoreOptions{}); !errors.Is(err, ops.ErrNotEmpty) {
		t.Fatalf("restoring over data: %v", err)
	}
	if _, err := ops.Restore(ctx, restored.Database.URL, migrations.All(), bytes.NewReader(backup.Bytes()), ops.RestoreOptions{Force: true}); err != nil {
		t.Fatalf("forced restore: %v", err)
	}

	// The restored Loomplane has everything: the account, its project,
	// its variables and its API keys.
	r2 := start(t, restored)
	defer r2.stop()
	again := &client{t: t, base: r2.srv.URL, key: cfg.Identity.PublishableKey}
	var signin struct {
		SessionToken string `json:"session_token"`
	}
	again.must(http.StatusOK, http.MethodPost, "/authsome/v1/signin", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &signin)
	again.token = signin.SessionToken
	again.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var projects struct {
		Projects []struct {
			ID           string   `json:"id"`
			Repositories []string `json:"repositories"`
		} `json:"projects"`
	}
	again.must(http.StatusOK, http.MethodGet, "/api/v1/projects", nil, &projects)
	if len(projects.Projects) != 1 || projects.Projects[0].ID != proj.ID || !slices.Equal(projects.Projects[0].Repositories, []string{"acme/mono"}) {
		t.Fatalf("restored projects %+v", projects)
	}
	var vars struct {
		Variables []struct {
			Key    string `json:"key"`
			Secret bool   `json:"secret"`
		} `json:"variables"`
	}
	again.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+proj.ID+"/variables", nil, &vars)
	if len(vars.Variables) != 1 || !vars.Variables[0].Secret {
		t.Fatalf("restored variables %+v", vars)
	}
	viewer := &client{t: t, base: r2.srv.URL, key: cfg.Identity.PublishableKey, token: key.Secret}
	viewer.must(http.StatusOK, http.MethodGet, "/api/v1/projects", nil, nil)

	// New rows don't collide with restored ones.
	again.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Second"}, nil)
}

func TestOps_anOlderLoomplaneRefusesANewerSchema(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	ctx := context.Background()
	if err := ops.Migrate(ctx, cfg.Database.URL, migrations.All()); err != nil {
		t.Fatal(err)
	}
	st, err := ops.SchemaStatus(ctx, cfg.Database.URL, migrations.All())
	if err != nil || len(st.Pending) != 0 || len(st.Unknown) != 0 || len(st.Applied) == 0 {
		t.Fatalf("status %+v, %v", st, err)
	}
	// A newer Loomplane's migration.
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, xerr := conn.Exec(ctx, `INSERT INTO grove_migrations (version, name, "group") VALUES ('29990101000000', 'from_the_future', 'loomplane')`); xerr != nil {
		t.Fatal(xerr)
	}
	if cerr := ops.CheckCompatible(ctx, cfg.Database.URL, migrations.All()); !errors.Is(cerr, ops.ErrNewerSchema) {
		t.Fatalf("check: %v", cerr)
	}
	// Development only warns, since branches share a local database.
	cfg.Environment = config.EnvProduction
	app, err := platform.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	if err := app.Start(sctx); !errors.Is(err, ops.ErrNewerSchema) {
		_ = app.Stop(ctx)
		t.Fatalf("started against a newer schema: %v", err)
	}
}
