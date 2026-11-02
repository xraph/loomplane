//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/ctl"
	"github.com/xraph/loomplane/internal/planning"
	apiclient "github.com/xraph/loomplane/pkg/client"
)

// cloudAPIVercel keeps immutable account identities and resources partitioned by
// token. Both accounts use the actual adapter against HTTP provider fixtures.
type cloudAPIVercel struct {
	srv            *httptest.Server
	accounts       map[string]*fakeVercelDeploys
	mu             sync.Mutex
	removed        []string
	deleteFailures int
	deleteRelease  string
	deployFailure  bool
}

func newCloudAPIVercel(t *testing.T) *cloudAPIVercel {
	t.Helper()
	f := &cloudAPIVercel{accounts: map[string]*fakeVercelDeploys{}}
	for _, token := range []string{"old-token-0123456789", "new-token-0123456789", "docs-token-0123456789"} {
		f.accounts[token] = newFakeVercelDeploys(t, token)
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		account := f.accounts[token]
		if account == nil {
			http.Error(w, "invalid account", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/v2/user" {
			identity, label := "user-original", "Original Vercel"
			if strings.HasPrefix(token, "new-") {
				identity, label = "user-replacement", "Replacement Vercel"
			}
			if strings.HasPrefix(token, "docs-") {
				identity, label = "user-docs", "Docs Vercel"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": identity, "username": label}})
			return
		}
		f.mu.Lock()
		failDeploy := f.deployFailure
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v9/projects/") && f.deleteFailures != 0 {
			if f.deleteFailures < 0 && f.deleteRelease != "" {
				if _, err := os.Stat(f.deleteRelease); err == nil {
					f.deleteFailures = 0
				}
			}
			if f.deleteFailures != 0 {
				if f.deleteFailures > 0 {
					f.deleteFailures--
				}
				f.mu.Unlock()
				http.Error(w, "fixture cleanup temporarily unavailable", http.StatusBadRequest)
				return
			}
		}
		f.mu.Unlock()
		if failDeploy && r.Method == http.MethodGet && r.URL.Path == "/v13/deployments/dpl_1" {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "dpl_1", "readyState": "ERROR", "errorMessage": "fixture build failed"})
			return
		}
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v9/projects/") {
			account.mu.Lock()
			for key := range account.projects {
				delete(account.projects, key)
			}
			account.mu.Unlock()
			f.mu.Lock()
			f.removed = append(f.removed, token)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Keep the same account-scoped project ID in each account. Cleanup
		// must use the original connection even when the strings collide.
		account.serve(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}
func cloudAPISetup(t *testing.T, configure ...func(*config.Config)) (config.Config, *running, *client, string, string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 50 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Storage.URL = "file://" + t.TempDir()
	for _, apply := range configure {
		apply(&cfg)
	}
	// The fixture endpoint is set by callers before starting deployment work.
	r := start(t, cfg)
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	firstAccount(t, c, "clouds@example.test", "Correct-horse-battery-9")
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Clouds", "slug": "clouds"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var project struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Cloud app", "repository_url": "https://github.com/acme/mono"}, &project)
	return cfg, r, c, org.ID, project.ID
}
func waitCloudAPI(t *testing.T, c *client, base, id string) *cloudprofiles.Sync {
	t.Helper()
	var result cloudprofiles.Sync
	for deadline := time.Now().Add(70 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		c.must(http.StatusOK, http.MethodGet, base+"/cloud-syncs/"+id, nil, &result)
		switch result.Status {
		case cloudprofiles.SyncCleanup:
			if result.Error != "" {
				return &result
			}
		case cloudprofiles.SyncSucceeded, cloudprofiles.SyncReverted, cloudprofiles.SyncRouting:
			return &result
		case cloudprofiles.SyncFailed:
			t.Fatalf("Cloud operation failed: %+v", result)
		}
		if time.Now().After(deadline) {
			t.Fatalf("Cloud operation did not settle: %+v", result)
		}
	}
}
func applyCloudAPI(t *testing.T, c *client, base string, preview *cloudprofiles.Preview, key string) *cloudprofiles.Sync {
	t.Helper()
	var result cloudprofiles.Sync
	c.must(http.StatusAccepted, http.MethodPost, base+"/cloud-changes/apply", apiclient.ApplyCloudInput{PreviewID: preview.ID, Revision: preview.Revision, IdempotencyKey: key}, &result)
	return &result
}
func TestCloudAPIActualReleaseSwitchAndNextRelease(t *testing.T) {
	provider := newCloudAPIVercel(t)
	cfg, r, c, org, project := cloudAPISetup(t)
	// Restart with the fixture endpoint while preserving the database and session.
	r.stop()
	cfg.Clouds.VercelEndpoint = provider.srv.URL
	r = start(t, cfg)
	defer func() { r.stop() }()
	c.base = r.srv.URL
	base := "/api/v1/projects/" + project
	analyze(t, cfg, c, project)
	var oldAccount, newAccount struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]string{"cloud": "vercel", "name": "Old", "credential": "old-token-0123456789"}, &oldAccount)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]string{"cloud": "vercel", "name": "New", "credential": "new-token-0123456789"}, &newAccount)
	var cloud cloudprofiles.Profile
	c.must(http.StatusCreated, http.MethodPost, base+"/clouds", cloudprofiles.CreateInput{Name: "Production"}, &cloud)
	var target cloudprofiles.Target
	targets := base + "/clouds/" + cloud.ID + "/targets"
	c.must(http.StatusCreated, http.MethodPost, targets, cloudprofiles.CreateTargetInput{Name: "frontend", Provider: "vercel", ConnectionID: oldAccount.ID}, &target)
	var envs struct {
		Environments []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"environments"`
	}
	c.must(http.StatusOK, http.MethodGet, base+"/environments", nil, &envs)
	env := ""
	for _, e := range envs.Environments {
		if e.Name == "production" {
			env = e.ID
		}
	}
	if env == "" {
		t.Fatal("production missing")
	}
	assignment := base + "/environments/" + env + "/cloud"
	var initial apiclient.EnvironmentCloud
	c.must(http.StatusOK, http.MethodGet, assignment, nil, &initial)
	if initial.Assignment != nil || initial.Revision != 0 {
		t.Fatalf("unassigned: %+v", initial)
	}
	var preview cloudprofiles.Preview
	c.must(http.StatusOK, http.MethodPost, assignment+"/preview", apiclient.EnvironmentCloudInput{CloudID: cloud.ID}, &preview)
	first := applyCloudAPI(t, c, base, &preview, "initial-assignment")
	if got := waitCloudAPI(t, c, base, first.ID); got.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("initial: %+v", got)
	}
	manifest := `version: 1
services:
  web:
    source: node:apps/web
    cloud_target: frontend
ignore:
  resources: [postgres, redis]
`
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": manifest}, nil)
	c.must(http.StatusCreated, http.MethodPost, base+"/variables", map[string]any{"key": "PRIVATE_VALUE", "value": "captured-secret", "secret": true, "environment": "production"}, nil)
	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if !plan.Executable {
		t.Fatalf("plan %+v", plan)
	}
	if deployed := approveAndWait(t, c, plan); deployed.Status != "succeeded" {
		t.Fatalf("deployment failed: %+v", deployed)
	}
	input := cloudprofiles.ChangeInput{CloudID: cloud.ID, TargetID: target.ID, Revision: 1, Provider: "vercel", ConnectionID: newAccount.ID}
	c.must(http.StatusOK, http.MethodPost, base+"/cloud-changes/preview", input, &preview)
	if len(preview.Blockers) != 0 || len(preview.Environments) != 1 || preview.Environments[0].Summary.Replace != 1 {
		t.Fatalf("switch review: %+v", preview)
	}
	// A private snapshot must never appear in the preview or read API.
	raw := c.raw(base + "/cloud-changes/" + preview.ID)
	for _, secret := range []string{"captured-secret", "old-token-0123456789", "new-token-0123456789", "\"snapshot\"", "\"actor\"", "variables_ref"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("preview exposed %q", secret)
		}
	}
	// API permissions are checked before accepting any placement or retry work.
	var readKey keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "read", "access": "read"}, &readKey)
	readonly := *c
	readonly.token = readKey.Secret
	refused(t, &readonly, http.MethodPost, base+"/cloud-changes/preview", input)
	refused(t, &readonly, http.MethodPost, base+"/cloud-changes/apply", apiclient.ApplyCloudInput{PreviewID: preview.ID, Revision: preview.Revision, IdempotencyKey: "denied"})
	c.must(http.StatusConflict, http.MethodPost, base+"/cloud-changes/apply", apiclient.ApplyCloudInput{PreviewID: preview.ID, Revision: "stale", IdempotencyKey: "stale"}, nil)
	operation := applyCloudAPI(t, c, base, &preview, "switch-one")
	replay := applyCloudAPI(t, c, base, &preview, "switch-one")
	if operation.ID != replay.ID {
		t.Fatal("lost idempotency identity")
	}
	result := waitCloudAPI(t, c, base, operation.ID)

	for _, denial := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, base + "/clouds", cloudprofiles.CreateInput{Name: "Denied"}},
		{http.MethodPut, base + "/clouds/" + cloud.ID, cloudprofiles.UpdateInput{Name: "Denied", Revision: 1}},
		{http.MethodDelete, base + "/clouds/" + cloud.ID, nil},
		{http.MethodPost, targets, cloudprofiles.CreateTargetInput{Name: "denied", Provider: "vercel", ConnectionID: newAccount.ID}},
		{http.MethodPut, targets + "/" + target.ID, cloudprofiles.UpdateTargetInput{DisplayName: "Denied", Revision: 2}},
		{http.MethodDelete, targets + "/" + target.ID, nil},
		{http.MethodPost, base + "/cloud-syncs/" + operation.ID + "/retry", nil},
		{http.MethodPost, base + "/cloud-syncs/" + operation.ID + "/revert/preview", nil},
		{http.MethodPost, base + "/cloud-syncs/revert/apply", apiclient.ApplyCloudInput{PreviewID: preview.ID, Revision: preview.Revision, IdempotencyKey: "denied-revert"}},
		{http.MethodPost, base + "/cloud-syncs/" + operation.ID + "/routing", apiclient.CompleteCloudRoutingInput{EnvironmentID: env, Evidence: "Denied"}},
	} {
		refused(t, &readonly, denial.method, denial.path, denial.body)
	}
	// Desired account changes at acceptance; persisted bindings describe what serves.
	var current cloudprofiles.Target
	c.must(http.StatusOK, http.MethodGet, targets+"/"+target.ID, nil, &current)
	if current.ConnectionID != newAccount.ID || current.Revision != 2 {
		t.Fatalf("desired: %+v", current)
	}
	var bindings struct {
		Bindings []planning.Binding `json:"bindings"`
	}
	c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &bindings)
	if len(bindings.Bindings) != 1 || bindings.Bindings[0].ConnectionID != newAccount.ID {
		t.Fatalf("applied: %+v", bindings)
	}
	provider.accounts["new-token-0123456789"].mu.Lock()
	captured := provider.accounts["new-token-0123456789"].env["PRIVATE_VALUE"]
	provider.accounts["new-token-0123456789"].mu.Unlock()
	if captured != "captured-secret" {
		t.Fatalf("Vault snapshot not applied: %v", captured)
	}
	if result.Status == cloudprofiles.SyncRouting {
		c.must(http.StatusNotFound, http.MethodPost, base+"/cloud-syncs/"+operation.ID+"/routing", apiclient.CompleteCloudRoutingInput{EnvironmentID: "foreign", Evidence: "verified"}, nil)
		c.must(http.StatusAccepted, http.MethodPost, base+"/cloud-syncs/"+operation.ID+"/routing", apiclient.CompleteCloudRoutingInput{EnvironmentID: env, Evidence: "Fixture production route verified"}, &result)
		result = waitCloudAPI(t, c, base, operation.ID)
	}
	if result.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("migration: %+v", result)
	}
	provider.mu.Lock()
	removed := append([]string{}, provider.removed...)
	provider.mu.Unlock()
	if len(removed) != 1 || removed[0] != "old-token-0123456789" {
		t.Fatalf("wrong cleanup accounts: %v", removed)
	}
	// The next ordinary release resolves the new account, independently of sync.
	c.must(http.StatusCreated, http.MethodPost, base+"/variables", map[string]any{"key": "NEXT_RELEASE", "value": "two", "environment": "production"}, nil)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": strings.Replace(manifest, "cloud_target: frontend", "cloud_target: frontend\n    health_path: /ready", 1)}, nil)
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if deployed := approveAndWait(t, c, plan); deployed.Status != "succeeded" {
		t.Fatalf("deployment failed: %+v", deployed)
	}
	for token, want := range map[string]int{"old-token-0123456789": 1, "new-token-0123456789": 2} {
		account := provider.accounts[token]
		account.mu.Lock()
		got := len(account.deployed)
		account.mu.Unlock()
		if got != want {
			t.Fatalf("%s deployments %d, want %d", token, got, want)
		}
	}
	// Syncs and scoped configuration survive application and worker restart.
	r.stop()
	r = start(t, cfg)
	c.base = r.srv.URL
	var history apiclient.CloudSyncPage
	c.must(http.StatusOK, http.MethodGet, base+"/cloud-syncs?limit=1", nil, &history)
	if len(history.Syncs) != 1 || history.Next == "" {
		t.Fatalf("pagination %+v", history)
	}
	var older apiclient.CloudSyncPage
	c.must(http.StatusOK, http.MethodGet, base+"/cloud-syncs?limit=1&before="+history.Next, nil, &older)
	if len(older.Syncs) != 1 || older.Syncs[0].ID == history.Syncs[0].ID {
		t.Fatalf("older %+v", older)
	}
	c.must(http.StatusOK, http.MethodGet, base+"/cloud-syncs/"+operation.ID, nil, &result)
	if result.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("restart %+v", result)
	}
	// Exercise loomctl against the same persisted API after restart.
	var cliKey keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "cloud-cli", "access": "write"}, &cliKey)
	var cliOutput bytes.Buffer
	command := ctl.New(ctl.Options{Out: &cliOutput, ConfigPath: filepath.Join(t.TempDir(), "loomctl.json"), Getenv: func(key string) string {
		switch key {
		case ctl.EnvURL:
			return r.srv.URL
		case ctl.EnvKey:
			return cliKey.Secret
		}
		return ""
	}})
	for _, args := range [][]string{{"clouds", project, "--json"}, {"cloud-targets", project, cloud.ID, "--json"}, {"cloud-syncs", "wait", project, operation.ID, "--timeout", "1s", "--json"}} {
		if commandErr := command.Run(args); commandErr != nil {
			t.Fatal(commandErr)
		}
	}
	if !strings.Contains(cliOutput.String(), cloud.ID) || !strings.Contains(cliOutput.String(), target.ID) || !strings.Contains(cliOutput.String(), "succeeded") {
		t.Fatalf("CLI lost persisted state: %s", cliOutput.String())
	}
	// Cross-project references never resolve through an organization-wide account.
	var other struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Other", "repository_url": "https://github.com/acme/mono"}, &other)
	otherBase := "/api/v1/projects/" + other.ID
	c.must(http.StatusNotFound, http.MethodGet, otherBase+"/clouds/"+cloud.ID, nil, nil)
	c.must(http.StatusNotFound, http.MethodGet, otherBase+"/cloud-syncs/"+operation.ID, nil, nil)
	c.must(http.StatusNotFound, http.MethodGet, otherBase+"/environments/"+env+"/cloud", nil, nil)
	var foreignCloud cloudprofiles.Profile
	c.must(http.StatusCreated, http.MethodPost, otherBase+"/clouds", cloudprofiles.CreateInput{Name: "Foreign"}, &foreignCloud)
	c.must(http.StatusNotFound, http.MethodPost, assignment+"/preview", apiclient.EnvironmentCloudInput{CloudID: foreignCloud.ID, Revision: 1}, nil)
	c.must(http.StatusBadRequest, http.MethodGet, base+"/cloud-syncs?before=bad-cursor", nil, nil)
	input.Revision = 1
	c.must(http.StatusConflict, http.MethodPost, base+"/cloud-changes/preview", input, nil)
	// Events are persisted in the same transaction and exclude every private input.
	db, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(t.Context())
	var events string
	if err = db.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(data)::text,'') FROM loomplane_events WHERE org_id=$1 AND type='cloud_sync.updated'`, org).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(events, operation.ID) {
		t.Fatal("sync lifecycle event missing")
	}
	for _, secret := range []string{"captured-secret", "variables_ref", "snapshot", "actor", "old-token-0123456789"} {
		if strings.Contains(events, secret) {
			t.Fatalf("private event data %s", secret)
		}
	}

	outsider := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var outsideAuth struct {
		SessionToken string `json:"session_token"`
	}
	outsider.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "outsider@example.test", "password": "Correct-horse-battery-9"}, &outsideAuth)
	outsider.token = outsideAuth.SessionToken
	var outsideOrg struct {
		ID string `json:"id"`
	}
	outsider.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Outside", "slug": "outside"}, &outsideOrg)
	outsider.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": outsideOrg.ID}, nil)
	for _, path := range []string{base + "/clouds", base + "/clouds/" + cloud.ID, targets, targets + "/" + target.ID, assignment, base + "/cloud-syncs", base + "/cloud-syncs/" + operation.ID} {
		refused(t, outsider, http.MethodGet, path, nil)
	}
	refused(t, outsider, http.MethodPost, base+"/cloud-changes/apply", apiclient.ApplyCloudInput{PreviewID: preview.ID, Revision: preview.Revision, IdempotencyKey: "outside"})
	assertCloudAPISchemas(t, c)
	if duration := os.Getenv("LOOMPLANE_CLOUD_FIXTURE_HOLD"); duration != "" {
		hold, parseErr := time.ParseDuration(duration)
		if parseErr != nil || hold <= 0 || hold > 2*time.Hour {
			t.Fatal("LOOMPLANE_CLOUD_FIXTURE_HOLD must be between zero and two hours")
		}
		stopFile := filepath.Join(t.TempDir(), "stop-cloud-fixture")
		details := map[string]string{"stop_file": stopFile, "api_url": r.srv.URL, "email": "clouds@example.test", "password": "Correct-horse-battery-9", "project_id": project, "cloud_id": cloud.ID, "target_id": target.ID, "environment_id": env, "old_connection_id": oldAccount.ID, "new_connection_id": newAccount.ID}
		data, marshalErr := json.MarshalIndent(details, "", "  ")
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		location := filepath.Join("..", "..", ".superpowers", "sdd", "2026-10-18-cloud-targets", "task-4-fixture.json")
		if writeErr := os.WriteFile(location, append(data, '\n'), 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
		t.Logf("Cloud browser fixture: %s; credentials in %s", r.srv.URL, location)
		timer := time.NewTimer(hold)
		defer timer.Stop()
		checkStop := time.NewTicker(time.Second)
		defer checkStop.Stop()
		holding := true
		for holding {
			select {
			case <-timer.C:
				holding = false
			case <-t.Context().Done():
				holding = false
			case <-checkStop.C:
				if _, statErr := os.Stat(stopFile); statErr == nil {
					holding = false
				}
			}
		}
	}
}

func TestCloudAPIStartupAndPeriodicOutboxRecovery(t *testing.T) {
	cfg, r, c, _, project := cloudAPISetup(t)
	defer func() { r.stop() }()
	base := "/api/v1/projects/" + project
	var cloud cloudprofiles.Profile
	c.must(http.StatusCreated, http.MethodPost, base+"/clouds", cloudprofiles.CreateInput{Name: "Recovery"}, &cloud)
	var env struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, base+"/environments", map[string]string{"name": "recovery"}, &env)
	assignment := base + "/environments/" + env.ID + "/cloud"
	db := pgdriver.New()
	if err := db.Open(t.Context(), cfg.Database.URL); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := cloudprofiles.NewPgStore(db)
	acceptWithoutEnqueue := func(id string, preview *cloudprofiles.Preview) *cloudprofiles.Sync {
		t.Helper()
		saved, snapshot, err := store.GetPreview(t.Context(), cloud.OrgID, project, preview.ID)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		sync := &cloudprofiles.Sync{ID: id, OrgID: cloud.OrgID, ProjectID: project, CloudID: preview.Input.CloudID, PreviewID: preview.ID, Status: cloudprofiles.SyncPending, Attempt: 1, CreatedBy: snapshot.Actor.UserID, Actor: snapshot.Actor, Environments: append([]cloudprofiles.EnvironmentChange{}, preview.Environments...), CreatedAt: now, UpdatedAt: now}
		sync, err = store.Accept(t.Context(), saved, snapshot, sync, id)
		if err != nil {
			t.Fatal(err)
		}
		return sync
	}
	var preview cloudprofiles.Preview
	c.must(http.StatusOK, http.MethodPost, assignment+"/preview", apiclient.EnvironmentCloudInput{CloudID: cloud.ID, Revision: 0}, &preview)
	r.stop()
	pending := acceptWithoutEnqueue("startup-outbox", &preview)
	r = start(t, cfg)
	c.base = r.srv.URL
	if got := waitCloudAPI(t, c, base, pending.ID); got.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("startup: %+v", got)
	}
	c.must(http.StatusOK, http.MethodPost, assignment+"/preview", apiclient.EnvironmentCloudInput{CloudID: "", Revision: 1}, &preview)
	pending = acceptWithoutEnqueue("periodic-outbox", &preview)
	if got := waitCloudAPI(t, c, base, pending.ID); got.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("periodic: %+v", got)
	}
	var tombstone apiclient.EnvironmentCloud
	c.must(http.StatusOK, http.MethodGet, assignment, nil, &tombstone)
	if tombstone.Assignment != nil || tombstone.Revision != 2 {
		t.Fatalf("unassignment generation lost: %+v", tombstone)
	}
	c.must(http.StatusConflict, http.MethodPost, assignment+"/preview", apiclient.EnvironmentCloudInput{CloudID: cloud.ID, Revision: 0}, nil)
	c.must(http.StatusConflict, http.MethodPost, base+"/cloud-syncs/"+pending.ID+"/retry", nil, nil)
	c.must(http.StatusOK, http.MethodPost, base+"/cloud-syncs/"+pending.ID+"/revert/preview", nil, &preview)
	if preview.Reverts != pending.ID {
		t.Fatalf("compensation review %+v", preview)
	}
	var compensation cloudprofiles.Sync
	c.must(http.StatusAccepted, http.MethodPost, base+"/cloud-syncs/revert/apply", apiclient.ApplyCloudInput{PreviewID: preview.ID, Revision: preview.Revision, IdempotencyKey: "compensate-unassignment"}, &compensation)
	if got := waitCloudAPI(t, c, base, compensation.ID); got.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("compensation %+v", got)
	}
	var restored apiclient.EnvironmentCloud
	c.must(http.StatusOK, http.MethodGet, assignment, nil, &restored)
	if restored.Assignment == nil || restored.Assignment.CloudID != cloud.ID || restored.Revision != 3 {
		t.Fatalf("restored %+v", restored)
	}
}

func assertCloudAPISchemas(t *testing.T, c *client) {
	t.Helper()
	var document struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	c.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &document)
	found := map[string]bool{}
	for _, path := range document.Paths {
		for _, op := range path {
			found[op.OperationID] = true
		}
	}
	for _, operation := range []string{"clouds.list", "clouds.create", "clouds.get", "clouds.update", "clouds.delete", "cloudTargets.list", "cloudTargets.get", "cloudTargets.create", "cloudTargets.update", "cloudTargets.delete", "cloudChanges.preview", "cloudChanges.getPreview", "cloudChanges.apply", "environmentClouds.get", "environmentClouds.preview", "cloudSyncs.list", "cloudSyncs.get", "cloudSyncs.retry", "cloudSyncs.previewRevert", "cloudSyncs.applyRevert", "cloudSyncs.completeRouting"} {
		if !found[operation] {
			t.Errorf("missing operation %s", operation)
		}
	}
	assignmentSchema := document.Components.Schemas["EnvironmentCloud"]
	if !strings.Contains(string(assignmentSchema.Properties["assignment"]), "nullable") {
		t.Error("assignment schema does not admit explicit null")
	}
	for _, name := range []string{"Sync", "Preview"} {
		schema, ok := document.Components.Schemas[name]
		if !ok {
			t.Errorf("missing schema %s", name)
			continue
		}
		for _, private := range []string{"snapshot", "actor", "version", "connections", "variables"} {
			if _, ok := schema.Properties[private]; ok {
				t.Errorf("private schema field %s.%s", name, private)
			}
		}
	}
}
