//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
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
		if err = cs.CreateConnection(ctx, &connections.Connection{ID: id, OrgID: "org", Cloud: "vercel", Name: id, Status: connections.StatusReady, Account: id, CreatedAt: now, UpdatedAt: now}); err != nil {
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
}

func (a *syncAccounts) All(ctx context.Context, org string) ([]*connections.Connection, error) {
	return a.store.ListConnections(ctx, org)
}
func (a *syncAccounts) Credential(ctx context.Context, org, id string) (*connections.Connection, []byte, error) {
	if id == a.missing {
		return nil, nil, connections.ErrNotFound
	}
	c, err := a.store.GetConnection(ctx, org, id)
	return c, []byte("token-" + id), err
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
	url         string
	failStage   bool
	failCleanup bool
	calls       map[string]int
	removed     []string
	contents    map[string]string
	resumed     bool
	badSecret   bool
}

func (e *syncExecutor) Apply(ctx context.Context, job *deployments.Job) (*deployments.Outcome, error) {
	key := job.Deployment.EnvironmentID + "/" + job.Action.Name
	e.calls[key]++
	if len(job.Resume) > 0 {
		e.resumed = true
	}
	if err := job.Checkpoint([]byte(`{"operation":"exact-provider-operation"}`)); err != nil {
		return nil, err
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
	if job.Env["SECRET_TOKEN"] != "applied-secret" {
		e.badSecret = true
	}
	return &deployments.Outcome{ExternalID: job.Connection.ID + "-" + key, URL: e.url, Revision: job.Connection.ID + "-revision"}, nil
}
func (e *syncExecutor) Remove(_ context.Context, job *deployments.Job, b *planning.Binding) error {
	if e.failCleanup {
		e.failCleanup = false
		return errors.New("cleanup temporarily unavailable")
	}
	e.removed = append(e.removed, job.Connection.ID+":"+b.ExternalID)
	return nil
}
func (e *syncExecutor) Observe(context.Context, *connections.Connection, []byte, *planning.Binding) (*deployments.Observation, error) {
	return &deployments.Observation{Exists: true}, nil
}

func TestCloudSyncRunnerPartialRecoveryRoutingCleanupAndRevert(t *testing.T) {
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
			secrets[ref] = []byte(`{"values":{"SECRET_TOKEN":"applied-secret"},"secrets":{"SECRET_TOKEN":true}}`)
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	exec := &syncExecutor{url: server.URL, failStage: true, calls: map[string]int{}, contents: map[string]string{}}
	accounts := &syncAccounts{store: connections.NewPgStore(db)}
	auth := &syncAuth{}
	service := planning.NewService(planning.Deps{Store: plans, Projects: projects.NewPgStore(db), CloudProfiles: st, Connections: accounts, Providers: providers.Registry(), Authz: auth})
	newRunner := func() *deployments.Runner {
		return deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(db), Plans: plans, Credentials: accounts, Variables: syncVars{}, Sources: sources, Resources: secrets, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"vercel": exec}, CloudProfiles: st, CloudSyncAuth: service})
	}
	runner := newRunner()
	service.BindCloudSyncRunner(runner, runner)
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
	runner = newRunner()
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
	for _, environment := range []string{"env", "stage"} {
		if _, err = service.CompleteCloudRouting(ctx, actor, "proj", sync.ID, environment, "fixture routing verified"); err != nil {
			t.Fatal(err)
		}
		if err = service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, heartbeat); err != nil {
			t.Fatal(err)
		}
	}
	completed, err := service.CloudSync(ctx, actor, "proj", sync.ID)
	if err != nil || completed.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("completion: %+v %v", completed, err)
	}
	if len(exec.removed) != 4 {
		t.Fatalf("cleanup removed %v", exec.removed)
	}
}
