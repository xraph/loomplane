//go:build integration

// Package integration boots the whole Loomplane Forge app against a real
// Postgres and drives it over HTTP the way the dashboard does.
//
// It needs a Postgres it may create databases on:
//
//	docker compose -f deployments/docker-compose/docker-compose.yml up -d
//	LOOMPLANE_TEST_DATABASE_URL=postgres://loomplane:loomplane@localhost:55532/loomplane?sslmode=disable \
//	  go test -tags=integration ./tests/integration/
//
// Each run creates a database of its own and drops it afterwards.
package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/platform"
	"github.com/xraph/loomplane/internal/projects"
)

const (
	startTimeout = time.Minute
	readyTimeout = 30 * time.Second
)

// freshDatabase creates an empty database next to the one in
// LOOMPLANE_TEST_DATABASE_URL and returns its DSN.
func freshDatabase(t *testing.T) string {
	t.Helper()
	admin := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL to run the integration tests")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect to %s: %v", admin, err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })

	suffix := make([]byte, 6)
	if _, err = rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "loomplane_it_" + hex.EncodeToString(suffix)
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		if _, derr := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); derr != nil {
			t.Logf("drop database %s: %v", name, derr)
		}
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// running is one started Loomplane app and an HTTP server in front of it.
type running struct {
	t      *testing.T
	srv    *httptest.Server
	app    interface{ Stop(context.Context) error }
	cancel context.CancelFunc
}

func start(t *testing.T, cfg config.Config, configure ...func(*httptest.Server)) *running {
	t.Helper()
	app, err := platform.New(cfg)
	if err != nil {
		t.Fatalf("build app: %v", err)
	}
	// Started the way forge.App.Run starts it, on a context that outlives
	// Start: Dispatch's durable worker runs on the context Start gets, so a
	// timeout here would stop it the moment Start returned.
	lifetime, cancel := context.WithCancel(context.Background())
	if err := app.Start(lifetime); err != nil {
		cancel()
		t.Fatalf("start app: %v", err)
	}
	srv := httptest.NewUnstartedServer(app.Router().Handler())
	for _, option := range configure {
		option(srv)
	}
	srv.Start()
	return &running{t: t, srv: srv, app: app, cancel: cancel}
}

// stop shuts the app down the way a signal would, and fails the test if
// shutdown fails.
func (r *running) stop() {
	r.t.Helper()
	if r.cancel != nil {
		defer r.cancel()
	}
	r.srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	if err := r.app.Stop(ctx); err != nil {
		r.t.Fatalf("stop app: %v", err)
	}
}

type client struct {
	t     *testing.T
	ctx   context.Context
	base  string
	token string
	key   string
}

func (c *client) context() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return c.t.Context()
}

func (c *client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(c.context(), method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Publishable-Key", c.key)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	if out != nil && res.StatusCode < 300 && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	if res.StatusCode >= 500 {
		c.t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, raw)
	}
	return res.StatusCode
}

// raw returns a GET's body as text, whatever its status.
func (c *client) raw(path string) string {
	c.t.Helper()
	req, err := http.NewRequestWithContext(c.context(), http.MethodGet, c.base+path, http.NoBody)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("X-Publishable-Key", c.key)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return string(data)
}

func (c *client) must(want int, method, path string, body, out any) {
	c.t.Helper()
	if got := c.do(method, path, body, out); got != want {
		c.t.Fatalf("%s %s: status %d, want %d", method, path, got, want)
	}
}

func TestPlatform_projectLifecycleSurvivesRestart(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Dashboard.Dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.Dashboard.Dir, "index.html"), []byte("<div id=root>loomplane</div>"), 0o600); err != nil {
		t.Fatal(err)
	}

	// First run: migrate a fresh database, sign up, create an organization
	// and a project, and let Dispatch initialize it.
	r := start(t, cfg)
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}

	c.must(http.StatusUnauthorized, http.MethodGet, "/api/v1/projects", nil, nil)

	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{
		"email": "ada@example.com", "password": "Correct-horse-battery-9", "first_name": "Ada",
	}, &auth)
	if auth.SessionToken == "" {
		t.Fatal("sign-up returned no session token")
	}
	c.token = auth.SessionToken

	// No organization yet, so nothing org-scoped is allowed.
	c.must(http.StatusForbidden, http.MethodGet, "/api/v1/session", nil, nil)

	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Analytical Engines", "slug": "analytical-engines"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)

	var sess struct {
		OrgID       string   `json:"org_id"`
		Permissions []string `json:"permissions"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/session", nil, &sess)
	if sess.OrgID != org.ID || !contains(sess.Permissions, "organization:administer") {
		t.Fatalf("the organization's creator isn't its owner: %+v", sess)
	}

	var proj projects.Project
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Difference Engine"}, &proj)
	if proj.Status != projects.StatusInitializing || proj.Slug != "difference-engine" || proj.Icon == "" || proj.Color == "" {
		t.Fatalf("created %+v", proj)
	}
	c.must(http.StatusConflict, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Difference Engine"}, nil)

	waitReady(t, c, proj.ID)

	// The style a caller picks is written back, and an unknown one refused.
	c.must(http.StatusOK, http.MethodPatch, "/api/v1/projects/"+proj.ID, map[string]string{"icon": "rocket", "color": "teal"}, &proj)
	if proj.Icon != "rocket" || proj.Color != "teal" {
		t.Fatalf("restyled %+v", proj)
	}
	c.must(http.StatusBadRequest, http.MethodPatch, "/api/v1/projects/"+proj.ID, map[string]string{"icon": "unicorn"}, nil)

	var envs struct {
		Environments []projects.Environment `json:"environments"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+proj.ID+"/environments", nil, &envs)
	if len(envs.Environments) != 2 || envs.Environments[0].Name != "production" {
		t.Fatalf("environments %+v", envs.Environments)
	}

	var infra struct {
		Engine string `json:"engine"`
		Store  string `json:"store"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/infrastructure", nil, &infra)
	if infra.Engine != "ctrlplane" || infra.Store != "postgres" {
		t.Fatalf("infrastructure %+v", infra)
	}

	// The same process serves the dashboard, client routes included.
	for _, path := range []string{"/", "/projects/" + proj.ID} {
		body := get(t, r.srv.URL+path)
		if !strings.Contains(body, "loomplane") {
			t.Fatalf("GET %s served %q, not the dashboard", path, body)
		}
	}
	r.stop()

	// Second run on the same database: migrations have nothing to do, and
	// the session, organization and project are all still there.
	r = start(t, cfg)
	defer r.stop()
	c.base = r.srv.URL

	var list struct {
		Projects []projects.Project `json:"projects"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects", nil, &list)
	if len(list.Projects) != 1 || list.Projects[0].ID != proj.ID || list.Projects[0].Status != projects.StatusReady ||
		list.Projects[0].Icon != "rocket" || list.Projects[0].Color != "teal" {
		t.Fatalf("after restart: %+v", list.Projects)
	}

	c.must(http.StatusNoContent, http.MethodDelete, "/api/v1/projects/"+proj.ID, nil, nil)
	c.must(http.StatusNotFound, http.MethodGet, "/api/v1/projects/"+proj.ID, nil, nil)
}

func TestPlatform_membersOnlySeeTheirOrganization(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	r := start(t, cfg)
	defer r.stop()

	signUpWithOrg := func(email, orgSlug string) *client {
		c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
		var auth struct {
			SessionToken string `json:"session_token"`
		}
		c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": email, "password": "Correct-horse-battery-9"}, &auth)
		c.token = auth.SessionToken
		var org struct {
			ID string `json:"id"`
		}
		c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": orgSlug, "slug": orgSlug}, &org)
		c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
		return c
	}
	ada := signUpWithOrg("ada@example.com", "engines")
	grace := signUpWithOrg("grace@example.com", "compilers")

	var proj projects.Project
	ada.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Engine"}, &proj)

	grace.must(http.StatusNotFound, http.MethodGet, "/api/v1/projects/"+proj.ID, nil, nil)
	grace.must(http.StatusNotFound, http.MethodDelete, "/api/v1/projects/"+proj.ID, nil, nil)
	var list struct {
		Projects []projects.Project `json:"projects"`
	}
	grace.must(http.StatusOK, http.MethodGet, "/api/v1/projects", nil, &list)
	if len(list.Projects) != 0 {
		t.Fatalf("another organization's projects are visible: %+v", list.Projects)
	}
}

func waitReady(t *testing.T, c *client, id string) {
	t.Helper()
	deadline := time.Now().Add(readyTimeout)
	for {
		var p projects.Project
		c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+id, nil, &p)
		if p.Status == projects.StatusReady {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("project %s still %q after %s; is Dispatch working the %q queue?", id, p.Status, readyTimeout, "loomplane")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func get(t *testing.T, u string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", u, res.StatusCode)
	}
	return string(b)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestPlatform_invitationMakesADeveloper(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	r := start(t, cfg)
	defer r.stop()

	signUp := func(email string) *client {
		c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
		var auth struct {
			SessionToken string `json:"session_token"`
		}
		c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": email, "password": "Correct-horse-battery-9", "first_name": strings.Split(email, "@")[0]}, &auth)
		c.token = auth.SessionToken
		return c
	}

	ada := signUp("ada@example.com")
	var org struct {
		ID string `json:"id"`
	}
	ada.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Engines", "slug": "engines"}, &org)
	ada.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)

	ada.must(http.StatusBadRequest, http.MethodPost, "/api/v1/invitations", map[string]string{"email": "not an email"}, nil)
	ada.must(http.StatusBadRequest, http.MethodPost, "/api/v1/invitations", map[string]string{"email": "grace@example.com", "role": "superuser"}, nil)

	var created struct {
		Invitation struct {
			ID    string `json:"id"`
			Email string `json:"email"`
			Role  string `json:"role"`
		} `json:"invitation"`
		AcceptPath string `json:"accept_path"`
	}
	ada.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations", map[string]string{"email": "grace@example.com", "role": "developer"}, &created)
	if created.Invitation.Email != "grace@example.com" || created.Invitation.Role != "developer" || !strings.HasPrefix(created.AcceptPath, "/invitations/accept?token=") {
		t.Fatalf("created %+v", created)
	}
	u, err := url.Parse(created.AcceptPath)
	if err != nil {
		t.Fatal(err)
	}
	token := u.Query().Get("token")

	var pending struct {
		Invitations []struct {
			Email string `json:"email"`
		} `json:"invitations"`
	}
	ada.must(http.StatusOK, http.MethodGet, "/api/v1/invitations", nil, &pending)
	if len(pending.Invitations) != 1 || pending.Invitations[0].Email != "grace@example.com" {
		t.Fatalf("pending %+v", pending.Invitations)
	}

	// Grace signs up with the invited email and accepts through authsome.
	grace := signUp("grace@example.com")
	var member struct {
		OrgID string `json:"org_id"`
	}
	if code := grace.do(http.MethodPost, "/authsome/v1/orgs/invitations/accept", map[string]string{"token": token}, &member); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("accept: status %d", code)
	}
	grace.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)

	var sess struct {
		Permissions []string `json:"permissions"`
	}
	grace.must(http.StatusOK, http.MethodGet, "/api/v1/session", nil, &sess)
	if !contains(sess.Permissions, "project:create") || contains(sess.Permissions, "project:delete") || contains(sess.Permissions, "member:manage") {
		t.Fatalf("an invited developer has %v", sess.Permissions)
	}
	grace.must(http.StatusForbidden, http.MethodPost, "/api/v1/invitations", map[string]string{"email": "alan@example.com"}, nil)

	var members struct {
		Members []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
			Name  string `json:"name"`
			Role  string `json:"role"`
		} `json:"members"`
	}
	grace.must(http.StatusOK, http.MethodGet, "/api/v1/members", nil, &members)
	if len(members.Members) != 2 || members.Members[0].Email != "ada@example.com" || members.Members[0].Role != "owner" ||
		members.Members[1].Email != "grace@example.com" || members.Members[1].Role != "developer" || members.Members[1].Name != "grace" {
		t.Fatalf("members %+v", members.Members)
	}

	// Accepted invitations leave the pending list.
	ada.must(http.StatusOK, http.MethodGet, "/api/v1/invitations", nil, &pending)
	if len(pending.Invitations) != 0 {
		t.Fatalf("still pending: %+v", pending.Invitations)
	}

	// Removed, Grace keeps her session, which still names the organization.
	// The session read has to say she's out, not hand back an empty workspace.
	if code := ada.do(http.MethodDelete, "/authsome/v1/orgs/"+org.ID+"/members/"+members.Members[1].ID, nil, nil); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("remove: status %d", code)
	}
	grace.must(http.StatusForbidden, http.MethodGet, "/api/v1/session", nil, nil)
	grace.must(http.StatusForbidden, http.MethodGet, "/api/v1/members", nil, nil)
	grace.must(http.StatusForbidden, http.MethodGet, "/api/v1/projects", nil, nil)
}
