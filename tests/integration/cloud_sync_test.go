//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func cloudSyncDatabase(t *testing.T) (*cloudprofiles.PgStore, *pgx.Conn, *pgdriver.PgDB) {
	t.Helper()
	ctx := t.Context()
	dsn := freshDatabase(t)
	if err := ops.Migrate(ctx, dsn, []*migrate.Group{migrations.Group()}); err != nil {
		t.Fatal(err)
	}
	db := pgdriver.New()
	if err := db.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	for _, sql := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('proj','org','One','one','ready','usr',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('env','org','proj','production','production',now(),now()),('stage','org','proj','staging','staging',now(),now())`,
	} {
		if _, err = conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	cs := connections.NewPgStore(db)
	for _, id := range []string{"old", "new"} {
		if err = cs.CreateConnection(ctx, &connections.Connection{ID: id, OrgID: "org", Cloud: "vercel", Name: id, Status: connections.StatusReady, Account: id, Details: map[string]string{"team_id": id}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	st := cloudprofiles.NewPgStore(db)
	if err = st.CreateProfile(ctx, &cloudprofiles.Profile{ID: "cloud", OrgID: "org", ProjectID: "proj", Name: "Cloud", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = st.CreateTarget(ctx, &cloudprofiles.Target{ID: "target", OrgID: "org", ProjectID: "proj", CloudID: "cloud", Name: "frontend", DisplayName: "Frontend", Provider: "vercel", ConnectionID: "old", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return st, conn, db
}
func persistAssignmentPreview(t *testing.T, st *cloudprofiles.PgStore, id, cloud string, revision int64) (*cloudprofiles.Preview, *cloudprofiles.Snapshot, *cloudprofiles.Sync) {
	t.Helper()
	now := time.Now().UTC()
	p := &cloudprofiles.Preview{ID: "preview-" + id, OrgID: "org", ProjectID: "proj", Revision: "digest-" + id, Input: cloudprofiles.ChangeInput{EnvironmentID: "env", CloudID: cloud, Revision: revision}, CreatedAt: now}
	raw, err := st.SnapshotBindings(t.Context(), "org", "env")
	if err != nil {
		t.Fatal(err)
	}
	snap := &cloudprofiles.Snapshot{Environments: []cloudprofiles.EnvironmentSnapshot{{EnvironmentID: "env", AssignmentRevision: revision, Bindings: raw, Plan: json.RawMessage(`{}`)}}, Connections: []string{"old", "new"}}
	if err = st.SavePreview(t.Context(), p, snap); err != nil {
		t.Fatal(err)
	}
	sync := &cloudprofiles.Sync{ID: "sync-" + id, OrgID: "org", ProjectID: "proj", CloudID: cloud, PreviewID: p.ID, Status: cloudprofiles.SyncPending, Attempt: 1, CreatedAt: now, UpdatedAt: now}
	return p, snap, sync
}
func TestCloudSyncAtomicAcceptanceRecoveryAndABA(t *testing.T) {
	st, conn, _ := cloudSyncDatabase(t)
	ctx := t.Context()
	p, snap, sync := persistAssignmentPreview(t, st, "assign", "cloud", 0)
	accepted, err := st.Accept(ctx, p, snap, sync, "request")
	if err != nil {
		t.Fatal(err)
	}
	again, err := st.Accept(ctx, p, snap, sync, "request")
	if err != nil || again.ID != accepted.ID {
		t.Fatalf("idempotency: %+v %v", again, err)
	}
	pending, err := st.PendingSyncs(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("commit without enqueue lost durable work: %v %v", pending, err)
	}
	otherP, otherSnap, otherSync := persistAssignmentPreview(t, st, "blocked", "cloud", 1)
	if _, err = st.Accept(ctx, otherP, otherSnap, otherSync, "other"); !errors.Is(err, cloudprofiles.ErrBusy) {
		t.Fatalf("second change passed guard: %v", err)
	}
	accepted.Status = cloudprofiles.SyncFailed
	if err = st.SaveSync(ctx, accepted, snap); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Accept(ctx, otherP, otherSnap, otherSync, "other"); !errors.Is(err, cloudprofiles.ErrBusy) {
		t.Fatalf("failure released guard: %v", err)
	}
	if _, err = conn.Exec(ctx, `DELETE FROM loomplane_cloud_connections WHERE id='new'`); err == nil {
		t.Fatal("active sync credential could be deleted")
	}
	accepted.Status = cloudprofiles.SyncSucceeded
	if err = st.SaveSync(ctx, accepted, snap); err != nil {
		t.Fatal(err)
	}
	unassign, unsnap, unsync := persistAssignmentPreview(t, st, "unassign", "", 1)
	removed, err := st.Accept(ctx, unassign, unsnap, unsync, "remove")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := st.Resolve(ctx, "org", "proj", "env")
	if err != nil || resolved != nil {
		t.Fatalf("unassignment: %+v %v", resolved, err)
	}
	generation, err := st.AssignmentGeneration(ctx, "org", "proj", "env")
	if err != nil || generation != 2 {
		t.Fatalf("lost tombstone: %d %v", generation, err)
	}
	removed.Status = cloudprofiles.SyncSucceeded
	if err = st.SaveSync(ctx, removed, unsnap); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Accept(ctx, otherP, otherSnap, otherSync, "stale"); !errors.Is(err, cloudprofiles.ErrConflict) {
		t.Fatalf("old assignment generation resurrected: %v", err)
	}
	if _, _, err = st.GetPreview(ctx, "other", "proj", p.ID); !errors.Is(err, cloudprofiles.ErrNotFound) {
		t.Fatalf("preview scope leak: %v", err)
	}
}

type syncAccounts struct {
	store   *connections.PgStore
	missing string
	tokens  map[string]string
}

func (a *syncAccounts) All(ctx context.Context, org string) ([]*connections.Connection, error) {
	return a.store.ListConnections(ctx, org)
}
func (a *syncAccounts) Credential(ctx context.Context, org, id string) (*connections.Connection, []byte, error) {
	if id == a.missing {
		return nil, nil, connections.ErrNotFound
	}
	c, err := a.store.GetConnection(ctx, org, id)
	token := "token-" + id
	if a.tokens[id] != "" {
		token = a.tokens[id]
	}
	return c, []byte(token), err
}

type syncAuth struct{ denied bool }

func (a *syncAuth) Require(context.Context, security.Principal, security.Permission, string) error {
	if a.denied {
		return security.ErrForbidden
	}
	return nil
}

type syncSecrets map[string][]byte

func (s syncSecrets) Put(_ context.Context, k string, v []byte) error {
	s[k] = append([]byte(nil), v...)
	return nil
}
func (s syncSecrets) Get(_ context.Context, k string) ([]byte, error) {
	v, ok := s[k]
	if !ok {
		return nil, deployments.ErrNoCredential
	}
	return append([]byte(nil), v...), nil
}
func (s syncSecrets) Delete(_ context.Context, k string) error { delete(s, k); return nil }

type syncSources map[string]string

func (s syncSources) Store(context.Context, string, string, string, string) error {
	return errors.New("migration must never fetch the current repository")
}
func (s syncSources) Open(_ context.Context, key string) (io.ReadCloser, int64, error) {
	v, ok := s[key]
	if !ok {
		return nil, 0, errors.New("missing artifact")
	}
	return io.NopCloser(strings.NewReader(v)), int64(len(v)), nil
}

type syncVars struct{}

func (syncVars) Values(context.Context, string, string, string, string) (map[string]string, map[string]bool, error) {
	return nil, nil, errors.New("migration must not read current variables")
}

type syncExecutor struct {
	url                   string
	failStage             bool
	failCleanup           bool
	calls                 map[string]int
	removed               []string
	contents              map[string]string
	resumed               bool
	badSecret             bool
	afterCheckpoint       func() error
	tokens                map[string]string
	endpoints             map[string]string
	trackPending          bool
	pendingResources      map[string]bool
	pendingCleanupFail    bool
	pendingDeleteThenFail bool
	pendingTail           bool
}

func (e *syncExecutor) Apply(ctx context.Context, job *deployments.Job) (*deployments.Outcome, error) {
	key := job.Deployment.EnvironmentID + "/" + job.Action.Name
	e.calls[key]++
	if len(job.Resume) > 0 {
		e.resumed = true
	}
	checkpoint := []byte(`{"operation":"exact-provider-operation"}`)
	if e.trackPending {
		id := job.Connection.ID + "-" + key
		if e.pendingResources == nil {
			e.pendingResources = map[string]bool{}
		}
		e.pendingResources[id] = true
		checkpoint, _ = json.Marshal(map[string]string{"operation": "exact-provider-operation", "external_id": id})
	}
	if err := job.Checkpoint(checkpoint); err != nil {
		return nil, err
	}
	if e.tokens == nil {
		e.tokens = map[string]string{}
		e.endpoints = map[string]string{}
	}
	e.tokens[key] = string(job.Credential)
	e.endpoints[key] = job.Env["API_URL"]
	if e.afterCheckpoint != nil {
		if err := e.afterCheckpoint(); err != nil {
			return nil, err
		}
	}
	if job.Deployment.EnvironmentID == "stage" && e.failStage {
		e.failStage = false
		return nil, errors.New("fixture interrupted after provider checkpoint")
	}
	source, _, err := job.Source(ctx)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(source)
	_ = source.Close()
	if err != nil {
		return nil, err
	}
	e.contents[key] = string(body)
	if job.Env["SECRET_TOKEN"] != "applied-secret" || job.Env["BASE_VERSION"] != "applied-base" {
		e.badSecret = true
	}
	return &deployments.Outcome{ExternalID: job.Connection.ID + "-" + key, URL: e.url, Revision: job.Connection.ID + "-revision"}, nil
}
func (e *syncExecutor) Remove(_ context.Context, job *deployments.Job, b *planning.Binding) error {
	if e.pendingCleanupFail && b.ExternalID == "new-stage/api" {
		return errors.New("pending destination cleanup unavailable")
	}
	if e.failCleanup {
		e.failCleanup = false
		return errors.New("cleanup temporarily unavailable")
	}
	delete(e.pendingResources, b.ExternalID)
	if b.ExternalID == "new-stage/api" && e.pendingDeleteThenFail {
		e.pendingDeleteThenFail = false
		e.pendingTail = true
		return errors.New("resource deleted but provider tail cleanup failed")
	}
	if b.ExternalID == "new-stage/api" {
		e.pendingTail = false
	}
	e.removed = append(e.removed, job.Connection.ID+":"+b.ExternalID)
	return nil
}
func (e *syncExecutor) PendingCloudResource(_ context.Context, job *deployments.Job) (string, error) {
	var state struct {
		ExternalID string `json:"external_id"`
	}
	if err := json.Unmarshal(job.Resume, &state); err != nil {
		return "", err
	}

	if e.trackPending && state.ExternalID != "" && !e.pendingResources[state.ExternalID] {
		return "", nil
	}
	return state.ExternalID, nil
}

func (e *syncExecutor) Observe(context.Context, *connections.Connection, []byte, *planning.Binding) (*deployments.Observation, error) {
	return &deployments.Observation{Exists: true}, nil
}

type syncFixture struct {
	store     *cloudprofiles.PgStore
	conn      *pgx.Conn
	db        *pgdriver.PgDB
	service   *planning.Service
	executor  *syncExecutor
	accounts  *syncAccounts
	auth      *syncAuth
	newRunner func() *deployments.Runner
	secrets   syncSecrets
}

func newSyncFixture(t *testing.T) *syncFixture {
	st, conn, db := cloudSyncDatabase(t)
	ctx := t.Context()
	for _, sql := range []string{
		`UPDATE loomplane_cloud_connections SET details=jsonb_build_object('team_id',id)`,
		`INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('org','proj','env','cloud',1),('org','proj','stage','cloud',1)`,
		`INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('manifest','org','proj',1,'version: 1','digest','usr',now())`,
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	plans := planning.NewPgStore(db)
	secrets := syncSecrets{}
	sources := syncSources{}
	for _, environment := range []string{"env", "stage"} {
		original := "original-" + environment
		if _, err := conn.Exec(ctx, `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at) VALUES($1,'org','proj',$2,'production','rejected','manifest',1,'usr',now())`, original, environment); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"api", "web"} {
			commit := name + "-applied-commit"
			ref := "org/" + environment + "/applied-inputs/original/" + name
			key := deployments.SourceKey("org", "proj", "app", commit)
			secrets[ref] = []byte(`{"values":{"SECRET_TOKEN":"applied-secret","BASE_VERSION":"applied-base"},"secrets":{"SECRET_TOKEN":true}}`)
			sources[key] = name + "-exact-original-artifact"
			cfg := planning.ServiceConfig{Provider: "vercel", Repo: "app", CommitSHA: commit, Service: name, Public: true, Workload: provider.WebApp, Execution: provider.Serverless, CloudPlacement: &planning.CloudPlacement{CloudID: "cloud", CloudRevision: 1, AssignmentRevision: 1, TargetID: "target", TargetRevision: 1, TargetName: "frontend", Provider: "vercel", ConnectionID: "old", AccountIdentity: connections.AccountIdentity("vercel", "old", map[string]string{"team_id": "old"})}, AppliedInputs: &planning.AppliedInputs{Variables: ref, Source: &planning.PlanSource{Alias: "app", Repository: "owner/app", CommitSHA: commit}, ArtifactKey: key}}
			if name == "web" {
				cfg.Env = []planning.EnvLink{{Key: "API_URL", Subject: planning.ServiceSubject, From: "api"}}
			}
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			if err = plans.PutBinding(ctx, &planning.Binding{ID: "binding-" + environment + "-" + name, OrgID: "org", ProjectID: "proj", EnvironmentID: environment, Subject: planning.ServiceSubject, Name: name, Provider: "vercel", Engine: provider.Loomplane, ExternalID: "old-" + environment + "/" + name, Config: raw, ConnectionID: "old", Revision: "old-revision", CommitSHA: commit, PlanID: original, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Current drafts, repository analysis and variables all differ from the serving release.
	for _, sql := range []string{
		`INSERT INTO loomplane_project_sources(id,org_id,project_id,alias,repository,ref,created_by,created_at,updated_at) VALUES('current-source','org','proj','app','owner/app','main','usr',now(),now())`,
		`INSERT INTO loomplane_analyses(id,org_id,project_id,source_id,alias,status,repository,ref,commit_sha,graph,created_by,created_at) VALUES('newer-analysis','org','proj','current-source','app','succeeded','owner/app','main','unapproved-newer-commit','{}','usr',now())`,
		`INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('unapproved-manifest','org','proj',2,'version: 1\nservices: {}','unapproved','usr',now())`,
		`INSERT INTO loomplane_variables(id,org_id,project_id,environment,service,key,value,created_by,updated_by,created_at,updated_at) VALUES('current-var','org','proj','production','api','BASE_VERSION','new-unapproved-value','usr','usr',now(),now())`,
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(server.Close)
	exec := &syncExecutor{url: server.URL, failStage: true, calls: map[string]int{}, contents: map[string]string{}}
	accounts := &syncAccounts{store: connections.NewPgStore(db)}
	auth := &syncAuth{}
	service := planning.NewService(planning.Deps{Store: plans, Projects: projects.NewPgStore(db), CloudProfiles: st, Connections: accounts, Providers: providers.Registry(), Authz: auth})
	newRunner := func() *deployments.Runner {
		return deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(db), Plans: plans, Credentials: accounts, Variables: syncVars{}, Sources: sources, Resources: secrets, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"vercel": exec}, CloudProfiles: st, CloudSyncAuth: service})
	}
	runner := newRunner()
	service.BindCloudSyncRunner(runner, runner)
	return &syncFixture{store: st, conn: conn, db: db, service: service, executor: exec, accounts: accounts, auth: auth, newRunner: newRunner, secrets: secrets}
}

func TestCloudSyncRunnerPartialRecoveryRoutingCleanupAndRevert(t *testing.T) {
	f := newSyncFixture(t)
	ctx := t.Context()
	service, exec, auth, conn := f.service, f.executor, f.auth, f.conn
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	preview, err := service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Blockers) > 0 {
		t.Fatalf("preview blocked: %v", preview.Blockers)
	}
	sync, err := service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "switch")
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := func([]byte) error { return nil }
	if err = service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, heartbeat); err == nil {
		t.Fatal("fixture failure was lost")
	}
	failed, err := service.CloudSync(ctx, actor, "proj", sync.ID)
	if err != nil || failed.Status != cloudprofiles.SyncFailed || failed.Environments[0].Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("partial state: %+v %v", failed, err)
	}
	if len(exec.removed) != 0 {
		t.Fatal("removed old resources before all consumers completed")
	}
	// Simulate process restart and a revoked actor. A newly authorized retry may recover.
	runner := f.newRunner()
	service.BindCloudSyncRunner(runner, runner)
	if _, err = service.RetryCloudSync(ctx, actor, "proj", sync.ID); err != nil {
		t.Fatal(err)
	}
	auth.denied = true
	before := exec.calls["stage/api"]
	if err = service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, heartbeat); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("revoked actor executed: %v", err)
	}
	if exec.calls["stage/api"] != before {
		t.Fatal("provider called after revocation")
	}
	auth.denied = false
	f.accounts.tokens = map[string]string{"new": "renewed-token-same-account"}
	if _, err = service.RetryCloudSync(ctx, actor, "proj", sync.ID); err != nil {
		t.Fatal(err)
	}
	if err = service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	routing, err := service.CloudSync(ctx, actor, "proj", sync.ID)
	if err != nil || routing.Status != cloudprofiles.SyncRouting {
		t.Fatalf("routing state %+v %v", routing, err)
	}
	if exec.tokens["stage/api"] != "renewed-token-same-account" || exec.endpoints["env/web"] != exec.url {
		t.Fatal("lost renewed token or updated dependency endpoint")
	}
	if !exec.resumed || exec.badSecret || exec.calls["env/api"] != 1 || exec.contents["env/api"] != "api-exact-original-artifact" || exec.contents["env/web"] != "web-exact-original-artifact" {
		t.Fatalf("lost applied source, snapshot or checkpoint: %+v", exec)
	}
	// External edits block compensation. Restoring the recorded value permits review.
	if _, err = conn.Exec(ctx, `UPDATE loomplane_bindings SET revision='external-edit' WHERE id='binding-env-api'`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.PreviewCloudRevert(ctx, actor, "proj", sync.ID); err == nil {
		t.Fatal("revert accepted external edit")
	}
	if _, err = conn.Exec(ctx, `UPDATE loomplane_bindings SET revision='new-revision' WHERE id='binding-env-api'`); err != nil {
		t.Fatal(err)
	}
	revert, err := service.PreviewCloudRevert(ctx, actor, "proj", sync.ID)
	if err != nil || len(revert.Blockers) > 0 {
		t.Fatalf("revert review: %+v %v", revert, err)
	}
	// Complete forward routing, then prove cleanup failure stays separate and retains credentials.
	exec.failCleanup = true
	for _, environment := range []string{"env", "stage"} {
		if _, err = service.CompleteCloudRouting(ctx, actor, "proj", sync.ID, environment, "fixture routing verified"); err != nil {
			t.Fatal(err)
		}
		if err = service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, heartbeat); environment == "stage" {
			if err == nil {
				t.Fatal("cleanup fixture failure was lost")
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	cleanup, err := service.CloudSync(ctx, actor, "proj", sync.ID)
	if err != nil || cleanup.Status != cloudprofiles.SyncCleanup {
		t.Fatalf("cleanup state: %+v %v", cleanup, err)
	}
	if _, err = conn.Exec(ctx, `DELETE FROM loomplane_cloud_connections WHERE id='old'`); err == nil {
		t.Fatal("cleanup credential was not retained")
	}
	f.accounts.missing = "old"
	if err = service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, heartbeat); err == nil {
		t.Fatal("cleanup substituted missing old credentials")
	}
	f.accounts.missing = ""
	if _, err = service.RetryCloudSync(ctx, actor, "proj", sync.ID); err != nil {
		t.Fatal(err)
	}
	if err = service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	completed, err := service.CloudSync(ctx, actor, "proj", sync.ID)
	if err != nil || completed.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("completion: %+v %v", completed, err)
	}
	if len(exec.removed) != 4 {
		t.Fatalf("cleanup removed %v", exec.removed)
	}
}

func TestCloudSyncRevertOwnsRemainingWork(t *testing.T) {
	f := newSyncFixture(t)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	preview, err := f.service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"})
	if err != nil || len(preview.Blockers) > 0 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	forward, err := f.service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "forward")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.conn.Exec(ctx, `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at) VALUES('queued-behind-forward','org','proj','env','production','approved','manifest',1,'usr',now())`); err != nil {
		t.Fatal(err)
	}
	heartbeat := func([]byte) error { return nil }
	if err = f.service.ReconcileCloudSync(ctx, "org", "proj", forward.ID, heartbeat); err == nil {
		t.Fatal("expected partial failure")
	}
	// A newly added service also makes the compensation review stale.
	if _, err = f.conn.Exec(ctx, `INSERT INTO loomplane_bindings(id,org_id,project_id,environment_id,subject,name,provider,region,engine,external_id,config,url,connection_id,revision,commit_sha,plan_id,created_at,updated_at) SELECT 'extra',org_id,project_id,environment_id,subject,'extra',provider,region,engine,'extra',config,url,connection_id,revision,commit_sha,plan_id,created_at,updated_at FROM loomplane_bindings WHERE id='binding-env-api'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.PreviewCloudRevert(ctx, actor, "proj", forward.ID); err == nil {
		t.Fatal("revert adopted an externally added service")
	}
	if _, err = f.conn.Exec(ctx, `DELETE FROM loomplane_bindings WHERE id='extra'`); err != nil {
		t.Fatal(err)
	}
	revert, err := f.service.PreviewCloudRevert(ctx, actor, "proj", forward.ID)
	if err != nil || len(revert.Blockers) > 0 {
		t.Fatalf("revert: %+v %v", revert, err)
	}
	// A delivered cleanup must finish before a competing revert can be accepted.
	if err = f.store.WithSyncLock(ctx, forward.ID, func() error {
		_, e := f.service.ApplyCloudRevert(ctx, actor, "proj", revert.ID, revert.Revision, "compensate")
		if !errors.Is(e, cloudprofiles.ErrBusy) {
			t.Fatalf("revert raced running cleanup: %v", e)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	compensation, err := f.service.ApplyCloudRevert(ctx, actor, "proj", revert.ID, revert.Revision, "compensate")
	if err != nil {
		t.Fatal(err)
	}
	if available, e := f.newRunner().ReleaseAvailable(ctx, "org", "queued-behind-forward"); e != nil || available {
		t.Fatalf("queued release escaped compensation guard: %v %v", available, e)
	}
	// An already delivered original activity must not revive forward migration.
	if err = f.service.ReconcileCloudSync(ctx, "org", "proj", forward.ID, heartbeat); err != nil {
		t.Fatalf("retired delivery: %v", err)
	}
	retired, err := f.service.CloudSync(ctx, actor, "proj", forward.ID)
	if err != nil || retired.Status != cloudprofiles.SyncReverting {
		t.Fatalf("old delivery changed reverting operation: %+v %v", retired, err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "proj", compensation.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	status, err := f.service.CloudSync(ctx, actor, "proj", compensation.ID)
	if err != nil || status.Status != cloudprofiles.SyncRouting {
		t.Fatalf("compensation status: %+v %v", status, err)
	}
	// Staging never left A. Its metadata updates without another provider call.
	if f.executor.calls["stage/api"] != 1 || f.executor.calls["stage/web"] != 0 {
		t.Fatalf("unchanged environment redeployed: %v", f.executor.calls)
	}
	for _, env := range status.Environments {
		if env.ExternalRouting {
			if _, err = f.service.CompleteCloudRouting(ctx, actor, "proj", compensation.ID, env.EnvironmentID, "routing returned to original account"); err != nil {
				t.Fatal(err)
			}
			if err = f.service.ReconcileCloudSync(ctx, "org", "proj", compensation.ID, heartbeat); err != nil {
				t.Fatal(err)
			}
		}
	}
	retired, err = f.service.CloudSync(ctx, actor, "proj", forward.ID)
	if err != nil || retired.Status != cloudprofiles.SyncReverted {
		t.Fatalf("forward status: %+v %v", retired, err)
	}
	if available, e := f.newRunner().ReleaseAvailable(ctx, "org", "queued-behind-forward"); e != nil || !available {
		t.Fatalf("queued release did not resume after revert: %v %v", available, e)
	}
	bindings, err := planning.NewPgStore(f.db).ListBindings(ctx, "org", "env")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bindings {
		if b.ConnectionID != "old" || len(b.Replaced) > 0 || b.CommitSHA != b.Name+"-applied-commit" {
			t.Fatalf("compensation lost original inputs: %+v", b)
		}
	}
}

func TestCloudSyncDatabaseReleaseSerialization(t *testing.T) {
	st, conn, db := cloudSyncDatabase(t)
	ctx := t.Context()
	if _, err := conn.Exec(ctx, `INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('manifest','org','proj',1,'version: 1','digest','usr',now())`); err != nil {
		t.Fatal(err)
	}
	insertPlan := func(id, status string) {
		t.Helper()
		if _, err := conn.Exec(ctx, `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at) VALUES($1,'org','proj','env','production',$2,'manifest',1,'usr',now())`, id, status); err != nil {
			t.Fatal(err)
		}
	}
	insertPlan("approved", "approved")
	p, snap, sync := persistAssignmentPreview(t, st, "serialized", "cloud", 0)
	if _, err := st.Accept(ctx, p, snap, sync, "switch"); !errors.Is(err, cloudprofiles.ErrBusy) {
		t.Fatalf("approved release overtaken: %v", err)
	}
	if _, err := conn.Exec(ctx, `UPDATE loomplane_plans SET status='rejected' WHERE id='approved'`); err != nil {
		t.Fatal(err)
	}
	accepted, err := st.Accept(ctx, p, snap, sync, "switch")
	if err != nil {
		t.Fatal(err)
	}
	insertPlan("later", "approved")
	deps := deployments.NewPgStore(db)
	if available, e := deps.EnvironmentAvailable(ctx, "org", "env", "later"); e != nil || available {
		t.Fatalf("later release availability: %v %v", available, e)
	}
	normal := &deployments.Deployment{ID: "normal", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Environment: "production", PlanID: "later", Status: deployments.StatusRunning, StartedAt: time.Now()}
	if _, err = deps.CreateDeployment(ctx, normal); err == nil {
		t.Fatal("ordinary start bypassed guard")
	}
	// A failed release also cannot reopen while the environment belongs to a sync.
	normal.Status = deployments.StatusFailed
	if _, err = deps.CreateDeployment(ctx, normal); err != nil {
		t.Fatal(err)
	}
	if _, err = deps.Reopen(ctx, "org", "normal"); err == nil {
		t.Fatal("ordinary retry bypassed guard")
	}
	accepted.Status = cloudprofiles.SyncFailed
	if err = st.SaveSync(ctx, accepted, snap); err != nil {
		t.Fatal(err)
	}
	if _, err = deps.Reopen(ctx, "org", "normal"); err == nil {
		t.Fatal("failed sync released retry guard")
	}
	accepted.Status = cloudprofiles.SyncSucceeded
	if err = st.SaveSync(ctx, accepted, snap); err != nil {
		t.Fatal(err)
	}
	if ok, e := deps.Reopen(ctx, "org", "normal"); e != nil || !ok {
		t.Fatalf("release did not resume: %v %v", ok, e)
	}
}

func TestCloudSyncRestartFromProviderCheckpoint(t *testing.T) {
	f := newSyncFixture(t)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	preview, err := f.service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"})
	if err != nil || len(preview.Blockers) > 0 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	sync, err := f.service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "restart")
	if err != nil {
		t.Fatal(err)
	}
	crashed, cancel := context.WithCancel(ctx)
	defer cancel()
	f.executor.afterCheckpoint = func() error { cancel(); return crashed.Err() }
	if err = f.service.ReconcileCloudSync(crashed, "org", "proj", sync.ID, func([]byte) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("crash: %v", err)
	}
	recovered, _, err := f.store.GetSync(ctx, "org", "proj", sync.ID)
	if err != nil || recovered.Status != cloudprofiles.SyncRunning {
		t.Fatalf("interrupted state: %+v %v", recovered, err)
	}
	f.executor.afterCheckpoint = nil
	f.executor.failStage = false
	runner := f.newRunner()
	f.service.BindCloudSyncRunner(runner, runner)
	if err = f.service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !f.executor.resumed || f.executor.calls["env/api"] != 2 || f.executor.calls["env/web"] != 1 {
		t.Fatalf("restart did not resume provider checkpoint: %v", f.executor.calls)
	}
	// Duplicate delivery while waiting for routing must not issue provider calls.
	if err = f.service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if f.executor.calls["env/api"] != 2 {
		t.Fatal("duplicate delivery redeployed")
	}
}

type syncRecoveryWorkflow struct {
	calls       []string
	unavailable bool
}

func (w *syncRecoveryWorkflow) StartCloudSync(_ context.Context, org, project, id string, _ int) error {
	w.calls = append(w.calls, org+"/"+project+"/"+id)
	if w.unavailable {
		return errors.New("queue unavailable")
	}
	return nil
}

func TestCloudSyncEmptyAssignmentAndEnqueueRecovery(t *testing.T) {
	st, conn, db := cloudSyncDatabase(t)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	accounts := &syncAccounts{store: connections.NewPgStore(db)}
	workflow := &syncRecoveryWorkflow{unavailable: true}
	plans := planning.NewPgStore(db)
	service := planning.NewService(planning.Deps{Store: plans, Projects: projects.NewPgStore(db), CloudProfiles: st, Connections: accounts, Providers: providers.Registry(), Authz: &syncAuth{}, CloudSyncWorkflow: workflow})
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(db), Plans: plans, Credentials: accounts, CloudProfiles: st, CloudSyncAuth: service})
	service.BindCloudSyncRunner(runner, runner)
	preview, err := service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{EnvironmentID: "env", CloudID: "cloud", Revision: 0})
	if err != nil || len(preview.Blockers) > 0 {
		t.Fatalf("initial assignment: %+v %v", preview, err)
	}
	sync, err := service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "empty-assign")
	if err != nil {
		t.Fatal(err)
	}
	if len(workflow.calls) != 1 {
		t.Fatal("acceptance did not attempt enqueue")
	}
	workflow.unavailable = false
	if err = service.RecoverCloudSyncs(ctx); err != nil {
		t.Fatal(err)
	}
	if len(workflow.calls) != 2 || workflow.calls[0] != workflow.calls[1] {
		t.Fatalf("lost committed outbox identity: %v", workflow.calls)
	}
	if err = service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	preview, err = service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"})
	if err != nil || len(preview.Blockers) > 0 {
		t.Fatalf("empty switch: %+v %v", preview, err)
	}
	sync, err = service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "empty-target")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_deployments`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("empty target invented deployments: %d %v", count, err)
	}
}

func TestCloudSyncAssignmentMigrationAndMetadataAdoption(t *testing.T) {
	t.Run("assignment", func(t *testing.T) {
		f := newSyncFixture(t)
		ctx := t.Context()
		actor := security.Principal{OrgID: "org", UserID: "usr"}
		now := time.Now().UTC()
		if err := f.store.CreateProfile(ctx, &cloudprofiles.Profile{ID: "cloud-b", OrgID: "org", ProjectID: "proj", Name: "Cloud B", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := f.store.CreateTarget(ctx, &cloudprofiles.Target{ID: "target-b", OrgID: "org", ProjectID: "proj", CloudID: "cloud-b", Name: "frontend", DisplayName: "Frontend", Provider: "vercel", ConnectionID: "new", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		preview, err := f.service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{EnvironmentID: "env", CloudID: "cloud-b", Revision: 1})
		if err != nil || len(preview.Blockers) > 0 {
			t.Fatalf("assignment preview: %+v %v", preview, err)
		}
		if len(preview.Environments) != 1 || preview.Environments[0].Summary.Replace != 2 {
			t.Fatalf("assignment summary: %+v", preview.Environments)
		}
		sync, err := f.service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "assignment")
		if err != nil {
			t.Fatal(err)
		}
		if err = f.service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, func([]byte) error { return nil }); err != nil {
			t.Fatal(err)
		}
		assignment, err := f.store.Resolve(ctx, "org", "proj", "env")
		if err != nil || assignment.CloudID != "cloud-b" || assignment.Revision != 2 {
			t.Fatalf("assignment not persisted: %+v %v", assignment, err)
		}
		if f.executor.calls["stage/api"] != 0 || f.executor.calls["env/api"] != 1 {
			t.Fatalf("assignment crossed environments: %v", f.executor.calls)
		}
		if _, err = f.conn.Exec(ctx, `DELETE FROM loomplane_cloud_assignments WHERE environment_id='stage'`); err != nil {
			t.Fatal(err)
		}
		if _, err = f.conn.Exec(ctx, `UPDATE loomplane_cloud_previews SET created_at=now()-interval '25 hours'`); err != nil {
			t.Fatal(err)
		}
		if err = f.store.DeleteProfile(ctx, "org", "proj", "cloud"); !errors.Is(err, cloudprofiles.ErrInUse) {
			t.Fatalf("original assignment Cloud lost recovery retention: %v", err)
		}
		if err = f.store.DeleteTarget(ctx, "org", "proj", "cloud", "target"); !errors.Is(err, cloudprofiles.ErrInUse) {
			t.Fatalf("original assignment target lost recovery retention: %v", err)
		}
	})
	t.Run("adoption", func(t *testing.T) {
		f := newSyncFixture(t)
		ctx := t.Context()
		actor := security.Principal{OrgID: "org", UserID: "usr"}
		preview, err := f.service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "old"})
		if err != nil || len(preview.Blockers) > 0 {
			t.Fatalf("adoption preview: %+v %v", preview, err)
		}
		for _, env := range preview.Environments {
			if env.Summary.Adopt != 2 || env.Summary.Replace != 0 {
				t.Fatalf("adoption summary: %+v", env.Summary)
			}
		}
		sync, err := f.service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "adoption")
		if err != nil {
			t.Fatal(err)
		}
		if err = f.service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, func([]byte) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if len(f.executor.calls) != 0 || len(f.executor.removed) != 0 {
			t.Fatal("metadata adoption called a provider")
		}
		bindings, err := planning.NewPgStore(f.db).ListBindings(ctx, "org", "env")
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range bindings {
			var cfg planning.ServiceConfig
			if err = json.Unmarshal(b.Config, &cfg); err != nil {
				t.Fatal(err)
			}
			if cfg.CloudPlacement.TargetRevision != 2 || b.PlanID != "original-env" || b.ExternalID != "old-env/"+b.Name {
				t.Fatalf("adoption changed applied resource: %+v", b)
			}
		}
	})
}

func TestCloudSyncRechecksAppliedInputsBeforeAcceptance(t *testing.T) {
	f := newSyncFixture(t)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	preview, err := f.service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"})
	if err != nil || len(preview.Blockers) > 0 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	delete(f.secrets, "org/env/applied-inputs/original/api")
	if _, err = f.service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "lost-inputs"); err == nil {
		t.Fatal("acceptance lost the reviewed variable snapshot")
	}
	target, err := f.store.GetTarget(ctx, "org", "proj", "cloud", "target")
	if err != nil || target.Revision != 1 || target.ConnectionID != "old" {
		t.Fatalf("rejected apply mutated target: %+v %v", target, err)
	}
	pending, err := f.store.PendingSyncs(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("rejected apply created work: %v %v", pending, err)
	}
}

func TestCloudSyncConcurrentDuplicateDelivery(t *testing.T) {
	f := newSyncFixture(t)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	preview, err := f.service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"})
	if err != nil || len(preview.Blockers) > 0 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	sync, err := f.service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "concurrent")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	f.executor.failStage = false
	f.executor.afterCheckpoint = func() error { close(entered); <-release; return nil }
	done := make(chan error, 1)
	go func() {
		done <- f.service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, func([]byte) error { return nil })
	}()
	<-entered
	if err = f.service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, func([]byte) error { return nil }); !errors.Is(err, cloudprofiles.ErrBusy) {
		t.Errorf("concurrent delivery did not respect execution lock: %v", err)
	}
	f.executor.afterCheckpoint = nil
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if f.executor.calls["env/api"] != 1 || f.executor.calls["env/web"] != 1 {
		t.Fatalf("duplicate provider calls: %v", f.executor.calls)
	}
}

func TestCloudSyncApprovalAcceptanceRace(t *testing.T) {
	st, conn, db := cloudSyncDatabase(t)
	ctx := t.Context()
	if _, err := conn.Exec(ctx, `INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('manifest','org','proj',1,'version: 1','digest','usr',now())`); err != nil {
		t.Fatal(err)
	}
	approvalConn, err := pgx.Connect(ctx, conn.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer approvalConn.Close(ctx)
	for i := range 5 {
		id := fmt.Sprintf("race-%d", i)
		if _, err = conn.Exec(ctx, `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at) VALUES($1,'org','proj','env','production','ready','manifest',1,'usr',now())`, id); err != nil {
			t.Fatal(err)
		}
		revision, e := st.AssignmentGeneration(ctx, "org", "proj", "env")
		if e != nil {
			t.Fatal(e)
		}
		p, snap, sync := persistAssignmentPreview(t, st, id, "cloud", revision)
		start, done := make(chan struct{}), make(chan error, 1)
		go func() {
			<-start
			_, approvalErr := approvalConn.Exec(ctx, `UPDATE loomplane_plans SET status='approved' WHERE id=$1`, id)
			done <- approvalErr
		}()
		close(start)
		accepted, e := st.Accept(ctx, p, snap, sync, id)
		if err = <-done; err != nil {
			t.Fatal(err)
		}
		switch {
		case e == nil:
			d := &deployments.Deployment{ID: "dep-" + id, OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Environment: "production", PlanID: id, Status: deployments.StatusRunning, StartedAt: time.Now()}
			if _, err = deployments.NewPgStore(db).CreateDeployment(ctx, d); err == nil {
				t.Fatal("approval overtook accepted sync")
			}
			accepted.Status = cloudprofiles.SyncSucceeded
			if err = st.SaveSync(ctx, accepted, snap); err != nil {
				t.Fatal(err)
			}
		case errors.Is(e, cloudprofiles.ErrBusy):
			generation, generationErr := st.AssignmentGeneration(ctx, "org", "proj", "env")
			if generationErr != nil || generation != revision {
				t.Fatalf("blocked switch mutated desired assignment: %d %v", generation, generationErr)
			}
		default:
			t.Fatalf("race acceptance: %v", e)
		}
		if _, err = conn.Exec(ctx, `UPDATE loomplane_plans SET status='rejected' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
}

type interleavedCompensationStore struct {
	*planning.PgStore
	afterCheck func() error
}

func (s *interleavedCompensationStore) CheckCloudCompensation(ctx context.Context, org, id, env string, before json.RawMessage) error {
	if err := s.PgStore.CheckCloudCompensation(ctx, org, id, env, before); err != nil {
		return err
	}
	if s.afterCheck != nil {
		fn := s.afterCheck
		s.afterCheck = nil
		return fn()
	}
	return nil
}

func TestCloudSyncRevertRejectsOwnershipChangeBeforeSnapshot(t *testing.T) {
	f := newSyncFixture(t)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	preview, err := f.service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"})
	if err != nil {
		t.Fatal(err)
	}
	forward, err := f.service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "interleave-forward")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "proj", forward.ID, func([]byte) error { return nil }); err == nil {
		t.Fatal("expected partial failure")
	}
	store := &interleavedCompensationStore{PgStore: planning.NewPgStore(f.db), afterCheck: func() error {
		_, e := f.conn.Exec(ctx, `UPDATE loomplane_bindings SET revision='outside-edit-between-check-and-snapshot' WHERE id='binding-env-api'`)
		return e
	}}
	service := planning.NewService(planning.Deps{Store: store, Projects: projects.NewPgStore(f.db), CloudProfiles: f.store, Connections: f.accounts, Providers: providers.Registry(), Authz: f.auth})
	runner := f.newRunner()
	service.BindCloudSyncRunner(runner, runner)
	revert, err := service.PreviewCloudRevert(ctx, actor, "proj", forward.ID)
	if err != nil {
		return
	} // A consistent preview is also allowed to reject the interleaving.
	if _, err = service.ApplyCloudRevert(ctx, actor, "proj", revert.ID, revert.Revision, "interleave-revert"); err == nil {
		t.Fatal("external mutation became owned compensation")
	}
	target, err := f.store.GetTarget(ctx, "org", "proj", "cloud", "target")
	if err != nil || target.ConnectionID != "new" || target.Revision != 2 {
		t.Fatalf("rejected compensation changed target: %+v %v", target, err)
	}
}

func TestCloudSyncRevertCleansUnpublishedProviderResources(t *testing.T) {
	f := newSyncFixture(t)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	f.executor.trackPending = true
	preview, err := f.service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"})
	if err != nil {
		t.Fatal(err)
	}
	forward, err := f.service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "unready-forward")
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := func([]byte) error { return nil }
	if err = f.service.ReconcileCloudSync(ctx, "org", "proj", forward.ID, heartbeat); err == nil {
		t.Fatal("expected readiness failure after resource creation")
	}
	if !f.executor.pendingResources["new-stage/api"] {
		t.Fatal("fixture did not create destination before readiness failure")
	}
	bindings, err := planning.NewPgStore(f.db).ListBindings(ctx, "org", "stage")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bindings {
		if b.ConnectionID != "old" {
			t.Fatal("unready destination became serving")
		}
	}
	revert, err := f.service.PreviewCloudRevert(ctx, actor, "proj", forward.ID)
	if err != nil {
		t.Fatal(err)
	}
	compensation, err := f.service.ApplyCloudRevert(ctx, actor, "proj", revert.ID, revert.Revision, "unready-revert")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "proj", compensation.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	f.executor.pendingCleanupFail = true
	status, err := f.service.CloudSync(ctx, actor, "proj", compensation.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range status.Environments {
		if !env.ExternalRouting {
			continue
		}
		if _, err = f.service.CompleteCloudRouting(ctx, actor, "proj", compensation.ID, env.EnvironmentID, "routing restored"); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "proj", compensation.ID, heartbeat); err == nil {
		t.Fatal("unpublished provider resource cleanup failure was ignored")
	}
	status, err = f.service.CloudSync(ctx, actor, "proj", compensation.ID)
	if err != nil || status.Status != cloudprofiles.SyncCleanup {
		t.Fatalf("pending cleanup state: %+v %v", status, err)
	}
	original, _, err := f.store.GetSync(ctx, "org", "proj", forward.ID)
	if err != nil || original.Status != cloudprofiles.SyncReverting {
		t.Fatalf("original retired before pending resource cleanup: %+v %v", original, err)
	}
	var refs int
	if err = f.conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_cloud_sync_connections WHERE sync_id=$1`, forward.ID).Scan(&refs); err != nil || refs == 0 {
		t.Fatalf("original cleanup credentials were released: %d %v", refs, err)
	}
	if !f.executor.pendingResources["new-stage/api"] {
		t.Fatal("failed cleanup forgot the destination")
	}
	for _, env := range original.Environments {
		if env.PlanID == "" {
			continue
		}
		if e := f.newRunner().CleanupRevertedCloudPlan(ctx, "org", env.PlanID, forward.ID, heartbeat); e == nil {
			t.Fatal("original operation granted its own unrelated cleanup authority")
		}
	}
	f.executor.pendingCleanupFail = false
	f.executor.pendingDeleteThenFail = true
	restarted := f.newRunner()
	f.service.BindCloudSyncRunner(restarted, restarted)
	if _, err = f.service.RetryCloudSync(ctx, actor, "proj", compensation.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "proj", compensation.ID, heartbeat); err == nil {
		t.Fatal("provider tail cleanup failure was lost")
	}
	if f.executor.pendingResources["new-stage/api"] || !f.executor.pendingTail {
		t.Fatal("fixture did not delete resource before tail failure")
	}
	restarted = f.newRunner()
	f.service.BindCloudSyncRunner(restarted, restarted)
	if _, err = f.service.RetryCloudSync(ctx, actor, "proj", compensation.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "proj", compensation.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	if f.executor.pendingTail {
		t.Fatal("retry forgot saved resource identity after provider deletion")
	}
	if f.executor.pendingResources["new-stage/api"] {
		t.Fatal("retry left the unready destination running")
	}
	original, _, err = f.store.GetSync(ctx, "org", "proj", forward.ID)
	if err != nil || original.Status != cloudprofiles.SyncReverted {
		t.Fatalf("original did not retire after cleanup: %+v %v", original, err)
	}
	if err = f.conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_cloud_sync_connections WHERE sync_id=$1`, forward.ID).Scan(&refs); err != nil || refs != 0 {
		t.Fatalf("finished cleanup retained credentials: %d %v", refs, err)
	}
}
