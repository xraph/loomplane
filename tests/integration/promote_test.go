//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/dispatch"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/engine"
	dispatchpg "github.com/xraph/dispatch/store/postgres"
	"github.com/xraph/grove"
	"github.com/xraph/grove/migrate"
	"github.com/xraph/warden"
	wardenpg "github.com/xraph/warden/store/postgres"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/security"
)

// movingGitHub serves examples/polyglot as acme/mono, with main at
// whichever commit the test last pushed. Every commit it was ever at
// serves the same tarball.
type movingGitHub struct {
	mu   sync.Mutex
	head string
	srv  *httptest.Server
}

func newMovingGitHub(t *testing.T, head string) *movingGitHub {
	t.Helper()
	g := &movingGitHub{head: head}
	tgz := exampleTarball(t)
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/mono":
			_, _ = w.Write([]byte(`{"default_branch":"main"}`))
		case r.URL.Path == "/repos/acme/mono/commits/main":
			g.mu.Lock()
			head := g.head
			g.mu.Unlock()
			_, _ = w.Write([]byte(head))
		case strings.HasPrefix(r.URL.Path, "/repos/acme/mono/tarball/"):
			_, _ = w.Write(tgz)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

// push moves main to commit.
func (g *movingGitHub) push(commit string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.head = commit
}

// releasePlan is a plan with what a promotion or a rollback adds.
type releasePlan struct {
	planBody
	Kind             string           `json:"kind"`
	From             string           `json:"from_environment"`
	ManifestRevision int              `json:"manifest_revision"`
	Artifacts        []artifactChoice `json:"artifacts"`
	Leftovers        []struct {
		Subject     string `json:"subject"`
		Name        string `json:"name"`
		Op          string `json:"op"`
		DataBearing bool   `json:"data_bearing"`
		Reason      string `json:"reason"`
	} `json:"leftovers"`
	Rollback *struct {
		Reason              string `json:"reason"`
		Trigger             string `json:"trigger"`
		TargetDeploymentID  string `json:"target_deployment_id"`
		RestoreDeploymentID string `json:"restore_deployment_id"`
	} `json:"rollback"`
}

// artifactChoice says whether a plan deploys an earlier image of a
// service, or builds it again and why.
type artifactChoice struct {
	Service   string `json:"service"`
	Reused    bool   `json:"reused"`
	Reference string `json:"reference"`
	From      string `json:"from"`
	Reason    string `json:"reason"`
}

// warned reports whether the plan carries a warning with code.
func (p *releasePlan) warned(code string) bool {
	return slices.ContainsFunc(p.Warnings, func(w struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}) bool {
		return w.Code == code
	})
}

// pins reports whether every source of the plan is pinned to commit.
func (p *releasePlan) pins(commit string) bool {
	for _, s := range p.Sources {
		if s.CommitSHA != commit {
			return false
		}
	}
	return len(p.Sources) > 0
}

// artifact is the plan's only artifact choice, or the zero value when it
// hasn't exactly one.
func (p *releasePlan) artifact() artifactChoice {
	if len(p.Artifacts) != 1 {
		return artifactChoice{}
	}
	return p.Artifacts[0]
}

// planRelease creates a plan with body and waits for planning to end.
func planRelease(t *testing.T, c *client, path string, body any) releasePlan {
	t.Helper()
	var p releasePlan
	c.must(http.StatusCreated, http.MethodPost, path, body, &p)
	deadline := time.Now().Add(readyTimeout)
	for p.Status == "planning" {
		if time.Now().After(deadline) {
			t.Fatalf("plan %s still planning after %s", p.ID, readyTimeout)
		}
		time.Sleep(100 * time.Millisecond)
		c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+p.ID, nil, &p)
	}
	return p
}

// promoteManifest runs api on Cloud Run, the same in every environment.
const promoteManifest = `version: 1
services:
  api:
    source: go:services/api
    provider: cloud-run
ignore:
  resources: [postgres, redis]
`

// promoteManifestWithFiles is promoteManifest's second revision: api
// gains a bucket.
const promoteManifestWithFiles = `version: 1
services:
  api:
    source: go:services/api
    provider: cloud-run
    needs: [files]
resources:
  files:
    kind: object-storage
    provider: cloud-storage
ignore:
  resources: [postgres, redis]
`

// serviceImage is the image the Cloud Run service a deployment step's URL
// names runs.
func (f *fakeCloud) serviceImage(t *testing.T, url string) string {
	t.Helper()
	name := url[strings.LastIndex(url, "/run/")+len("/run/"):]
	f.mu.Lock()
	defer f.mu.Unlock()
	svc, ok := f.services[name]
	if !ok {
		t.Fatalf("no Cloud Run service %s", name)
	}
	image, _ := svc["template"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"].(string)
	return image
}

func (f *fakeCloud) buildCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.builds)
}

// Staging builds a commit once; production runs that image by digest, and
// rolling production back runs the earlier image again, with no build
// for either. A resource the rolled-back release added stays.
func TestPromote_productionRunsStagingsImageAndRollsBack(t *testing.T) {
	const commitB = "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0"
	const commitC = "c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0"
	google := newFakeGoogle(t)
	cloud := newFakeCloud(google.srv.URL)
	google.deploy = cloud
	github := newMovingGitHub(t, exampleSHA)
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = github.srv.URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.GoogleEndpoint = google.srv.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	c.must(http.StatusCreated, http.MethodPost, base+"/environments", map[string]string{"name": "staging"}, nil)
	analyze(t, cfg, c, proj.ID)
	// Staging and production share one Google Cloud project and region.
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "gcp", "name": "Prod", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, nil)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": promoteManifest}, nil)

	// Staging builds commit A, once.
	stagingA := planRelease(t, c, base+"/plans", map[string]string{"environment": "staging"})
	if stagingA.Status != "ready" || !stagingA.pins(exampleSHA) {
		t.Fatalf("staging plan at A: %+v", stagingA)
	}
	d := approveAndWait(t, c, stagingA.planBody)
	if d.Status != "succeeded" || len(d.Steps) != 1 {
		t.Fatalf("staging deployment at A: %+v", d)
	}
	imageA := d.Steps[0].Image
	if cloud.buildCount() != 1 || !strings.Contains(imageA, "@sha256:") {
		t.Fatalf("staging at A: %d builds, image %s", cloud.buildCount(), imageA)
	}

	// A newer manifest after staging deployed: the promotion keeps the
	// revision staging ran, and says so.
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": promoteManifestWithFiles}, nil)
	promotion := planRelease(t, c, base+"/plans", map[string]string{"environment": "production", "from": "staging"})
	if promotion.Status != "ready" || promotion.Kind != "promotion" || promotion.From != "staging" || promotion.ManifestRevision != 1 ||
		!promotion.warned("newer-manifest") || len(promotion.Actions) != 1 {
		t.Fatalf("promotion: %+v", promotion)
	}
	if !promotion.pins(exampleSHA) {
		t.Fatalf("promotion sources: %+v", promotion.Sources)
	}
	if a := promotion.artifact(); a.Service != "api" || !a.Reused || a.Reference != imageA || a.From != "staging" {
		t.Fatalf("promotion artifacts: %+v", promotion.Artifacts)
	}
	first := approveAndWait(t, c, promotion.planBody)
	if first.Status != "succeeded" || len(first.Steps) != 1 || first.Steps[0].Image != imageA {
		t.Fatalf("production deployment at A: %+v", first)
	}
	if n := cloud.buildCount(); n != 1 {
		t.Fatalf("the promotion built: %d builds", n)
	}
	productionURL := first.Steps[0].URL
	if got := cloud.serviceImage(t, productionURL); got != imageA {
		t.Fatalf("production runs %s, not staging's %s", got, imageA)
	}

	// Commit B reaches staging with manifest revision 2, and its bucket.
	github.push(commitB)
	analyze(t, cfg, c, proj.ID)
	stagingB := planRelease(t, c, base+"/plans", map[string]string{"environment": "staging"})
	if stagingB.Status != "ready" || !stagingB.pins(commitB) || stagingB.ManifestRevision != 2 {
		t.Fatalf("staging plan at B: %+v", stagingB)
	}
	if d = approveAndWait(t, c, stagingB.planBody); d.Status != "succeeded" {
		t.Fatalf("staging deployment at B: %+v", d)
	}
	var imageB string
	for _, s := range d.Steps {
		if s.ActionID == "service:api" {
			imageB = s.Image
		}
	}
	if cloud.buildCount() != 2 || imageB == "" || imageB == imageA {
		t.Fatalf("staging at B: %d builds, image %s", cloud.buildCount(), imageB)
	}

	// Commit C is pushed and its analysis queued, but staging runs B, so
	// that's what the promotion pins.
	github.push(commitC)
	var queued struct {
		Analyses []analysisBody `json:"analyses"`
	}
	c.must(http.StatusCreated, http.MethodPost, base+"/analyses", nil, &queued)
	if len(queued.Analyses) != 1 || queued.Analyses[0].Status == "succeeded" {
		t.Fatalf("analysis of C: %+v", queued)
	}
	promotionB := planRelease(t, c, base+"/plans", map[string]string{"environment": "production", "from": "staging"})
	if promotionB.Status != "ready" || promotionB.Kind != "promotion" || promotionB.ManifestRevision != 2 || promotionB.warned("newer-manifest") {
		t.Fatalf("promotion at B: %+v", promotionB)
	}
	if !promotionB.pins(commitB) {
		t.Fatalf("promotion at B sources: %+v", promotionB.Sources)
	}
	var stillQueued analysisBody
	c.must(http.StatusOK, http.MethodGet, "/api/v1/analyses/"+queued.Analyses[0].ID, nil, &stillQueued)
	if stillQueued.Status == "succeeded" || stillQueued.Status == "failed" {
		t.Fatalf("the analysis of C finished during the test: %+v", stillQueued)
	}
	if a := promotionB.artifact(); a.Reference != imageB || !a.Reused {
		t.Fatalf("promotion at B artifacts: %+v", promotionB.Artifacts)
	}
	second := approveAndWait(t, c, promotionB.planBody)
	if second.Status != "succeeded" {
		t.Fatalf("production deployment at B: %+v", second)
	}
	if n := cloud.buildCount(); n != 2 {
		t.Fatalf("the promotion at B built: %d builds", n)
	}
	if got := cloud.serviceImage(t, productionURL); got != imageB {
		t.Fatalf("production runs %s after the promotion at B, not %s", got, imageB)
	}
	cloud.mu.Lock()
	buckets := len(cloud.buckets)
	cloud.mu.Unlock()
	if buckets != 2 {
		t.Fatalf("%d buckets, want staging's and production's", buckets)
	}

	// Production rolls back to A and manifest revision 1, running A's image
	// again. The bucket B added has data, so it stays, bound.
	rollback := planRelease(t, c, base+"/rollbacks", map[string]string{"environment": "production", "reason": "test"})
	if rollback.Status != "ready" || rollback.Kind != "rollback" || rollback.ManifestRevision != 1 || rollback.Rollback == nil ||
		rollback.Rollback.Reason != "test" || rollback.Rollback.Trigger != "manual" ||
		rollback.Rollback.TargetDeploymentID != second.ID || rollback.Rollback.RestoreDeploymentID != first.ID {
		t.Fatalf("rollback plan: %+v rollback %+v", rollback, rollback.Rollback)
	}
	if !rollback.pins(exampleSHA) {
		t.Fatalf("rollback sources: %+v", rollback.Sources)
	}
	if a := rollback.artifact(); a.Reference != imageA || !a.Reused {
		t.Fatalf("rollback artifacts: %+v", rollback.Artifacts)
	}
	if len(rollback.Leftovers) != 1 || rollback.Leftovers[0].Name != "files" || rollback.Leftovers[0].Subject != "resource" ||
		rollback.Leftovers[0].Op != "delete" || !rollback.Leftovers[0].DataBearing {
		t.Fatalf("rollback leftovers: %+v", rollback.Leftovers)
	}
	undone := approveAndWait(t, c, rollback.planBody)
	if undone.Status != "succeeded" {
		t.Fatalf("rollback deployment: %+v", undone)
	}
	if n := cloud.buildCount(); n != 2 {
		t.Fatalf("the rollback built: %d builds", n)
	}
	if got := cloud.serviceImage(t, productionURL); got != imageA {
		t.Fatalf("production runs %s after the rollback, not A's %s", got, imageA)
	}
	cloud.mu.Lock()
	buckets, deleted := len(cloud.buckets), slices.Clone(cloud.deleted)
	cloud.mu.Unlock()
	if buckets != 2 || len(deleted) != 0 {
		t.Fatalf("after the rollback: %d buckets, deleted %v", buckets, deleted)
	}
	pg, err := pgx.Connect(context.Background(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close(context.Background())
	var commit, planID string
	if err = pg.QueryRow(t.Context(), `SELECT commit_sha, plan_id FROM loomplane_environment_sources WHERE environment_id=$1`, rollback.EnvironmentID).Scan(&commit, &planID); err != nil {
		t.Fatal(err)
	}
	if commit != exampleSHA || planID != rollback.ID {
		t.Fatalf("production's recorded source: %s from %s", commit, planID)
	}
	var list struct {
		Bindings []struct {
			Name   string `json:"name"`
			PlanID string `json:"plan_id"`
		} `json:"bindings"`
	}
	c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &list)
	planOf := map[string]string{}
	for _, b := range list.Bindings {
		planOf[b.Name] = b.PlanID
	}
	if planOf["files"] != promotionB.ID || planOf["api"] != rollback.ID {
		t.Fatalf("production's bindings: %+v", list.Bindings)
	}
}

// buildingCloud is Cloud Run with its builds, as promotions and rollbacks
// see it: a service built from a commit runs an image of its own, by
// digest, and one that reuses an earlier image runs it without a build.
// It ramps canaries as canaryWeighted does.
type buildingCloud struct {
	mu sync.Mutex
	// serving is the revision taking each environment's traffic.
	serving   map[string]string
	revisions int
	builds    []string
	log       []string
}

func (e *buildingCloud) record(format string, args ...any) {
	e.log = append(e.log, fmt.Sprintf(format, args...))
}

// image is what a job runs: the image its plan reuses, or a new build of
// its commit.
func (e *buildingCloud) image(j *deployments.Job) string {
	if a := j.Action.Service.Artifact; a != nil && a.Reference != "" {
		return a.Reference
	}
	e.builds = append(e.builds, j.Action.Service.CommitSHA)
	image := "us-central1-docker.pkg.dev/acme-prod/loomplane/api@" + fakeDigest(fmt.Sprintf("%s#%d", j.Action.Service.CommitSHA, len(e.builds)))
	e.record("build %s %s", j.Action.Service.CommitSHA, image)
	return image
}

func (e *buildingCloud) Apply(_ context.Context, j *deployments.Job) (*deployments.Outcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	image := e.image(j)
	e.revisions++
	e.serving[j.Deployment.Environment] = fmt.Sprintf("api-%05d", e.revisions)
	e.record("apply %s %s %s", j.Deployment.Environment, e.serving[j.Deployment.Environment], image)
	return &deployments.Outcome{ExternalID: "projects/acme-prod/services/api-" + j.Deployment.Environment, Image: image, Revision: image}, nil
}

func (e *buildingCloud) ApplyCandidate(_ context.Context, j *deployments.Job) (*deployments.CanaryTarget, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	image := e.image(j)
	e.revisions++
	t := &deployments.CanaryTarget{Baseline: e.serving[j.Deployment.Environment], Candidate: fmt.Sprintf("api-%05d", e.revisions), Image: image, Refs: map[string]string{"service": "api"}}
	e.record("candidate %s %s %s", j.Deployment.Environment, t.Candidate, t.Image)
	return t, nil
}

func (e *buildingCloud) SetWeights(_ context.Context, _ *deployments.Job, t deployments.CanaryTarget, percent int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.record("weigh %s %d", t.Candidate, percent)
	return nil
}

func (e *buildingCloud) Promote(_ context.Context, j *deployments.Job, t deployments.CanaryTarget) (*deployments.Outcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.serving[j.Deployment.Environment] = t.Candidate
	e.record("promote %s", t.Candidate)
	return &deployments.Outcome{ExternalID: "projects/acme-prod/services/api-" + j.Deployment.Environment, Image: t.Image, Revision: t.Image}, nil
}

func (e *buildingCloud) Revert(_ context.Context, _ *deployments.Job, t deployments.CanaryTarget) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.record("revert %s", t.Candidate)
	return nil
}

func (*buildingCloud) Remove(context.Context, *deployments.Job, *planning.Binding) error { return nil }

func (*buildingCloud) Observe(context.Context, *connections.Connection, []byte, *planning.Binding) (*deployments.Observation, error) {
	return &deployments.Observation{Exists: true}, nil
}

func (e *buildingCloud) built() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.builds)
}

func (e *buildingCloud) entries() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.log)
}

// releaseHealth is the cloud's monitoring: every window serves 1000
// requests with one 5xx, and once failFrom is set, a window that starts
// then or later answers 80 of them with a 5xx.
type releaseHealth struct {
	mu       sync.Mutex
	failFrom time.Time
}

func (h *releaseHealth) Health(_ context.Context, _, _, _ string, w releasehealth.Window) (releasehealth.Health, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	health := releasehealth.Health{Requests: 1000, Errors: 1, LatencyMS: 120, Statistic: "p95", Available: true}
	if !h.failFrom.IsZero() && !w.Start.Before(h.failFrom) {
		health.Errors = 80
	}
	return health, nil
}

func (h *releaseHealth) failSince(at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failFrom = at
}

// releaseFixture is planning with the release history, the deployment
// runner and its durable workflow, and the post-deploy watch, on a fresh
// Postgres with Warden deciding what everyone may do. Cloud Run, its
// builds and its monitoring are faked; canary holds are cut short
// (shortHolds), and the watch reads a clock the test moves.
type releaseFixture struct {
	sql        *pgx.Conn
	plans      *planning.PgStore
	store      *deployments.PgStore
	service    *planning.Service
	ramp       *shortHolds
	cloud      *buildingCloud
	health     *releaseHealth
	warden     *warden.Engine
	roles      *wardenpg.Store
	watches    *releasewatch.Service
	watchStore *releasewatch.PgStore
	actor      security.Principal
	commits    int

	mu   sync.Mutex
	skew time.Duration
}

// newReleaseFixture plans project proj, whose production environment is
// env and staging stage, from canaryRepository and yaml.
func newReleaseFixture(t *testing.T, yaml string) *releaseFixture {
	t.Helper()
	clouds, sql, db := cloudSyncDatabase(t)
	ctx := t.Context()
	now := time.Now().UTC()
	if err := connections.NewPgStore(db).CreateConnection(ctx, &connections.Connection{ID: "gcp", OrgID: "org", Cloud: "gcp", Name: "Prod", Status: connections.StatusReady, Account: "acme-prod", Details: map[string]string{"project_id": "acme-prod"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := sql.Exec(ctx, `INSERT INTO loomplane_project_sources(id,org_id,project_id,alias,repository,ref,created_by,created_at,updated_at) VALUES('canary-source','org','proj','app','acme/app','main','usr',now(),now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := sql.Exec(ctx, `INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('release-manifest','org','proj',1,$1,'release-manifest','usr',now())`, yaml); err != nil {
		t.Fatal(err)
	}
	if err := ops.Migrate(ctx, sql.Config().ConnString(), []*migrate.Group{dispatchpg.Migrations, wardenpg.Migrations}); err != nil {
		t.Fatal(err)
	}
	gdb, err := grove.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	ds := dispatchpg.New(gdb)

	f := &releaseFixture{sql: sql, plans: planning.NewPgStore(db), store: deployments.NewPgStore(db), actor: security.Principal{OrgID: "org", UserID: "usr"}}
	f.roles = wardenpg.New(gdb)
	if f.warden, err = warden.NewEngine(warden.WithStore(f.roles)); err != nil {
		t.Fatal(err)
	}
	authz := security.NewAuthorizer(f.warden, f.roles, nil)
	if err = authz.Assign(ctx, "org", "usr", security.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	f.cloud = &buildingCloud{serving: map[string]string{}}
	f.health = &releaseHealth{}
	accounts := &syncAccounts{store: connections.NewPgStore(db)}
	prj := projects.NewPgStore(db)
	manifestService := manifests.NewService(manifests.NewPgStore(db), nil, nil, nil)
	history := &deployments.History{Store: f.store, Plans: f.plans, Providers: providers.Registry()}
	workflows := orchestration.NewDeployments()
	f.service = planning.NewService(planning.Deps{
		Store: f.plans, Projects: prj, Analyses: discovery.NewPgStore(db), History: history,
		Manifests: manifestService, Providers: providers.Registry(),
		Connections: accounts, Authz: authz, Workflow: workflows, CloudProfiles: clouds, Window: time.Hour,
	})
	f.watchStore = releasewatch.NewPgStore(db)
	f.watches = releasewatch.NewService(releasewatch.Deps{
		Store: f.watchStore, Rollbacks: f.service, Plans: f.plans, Deployments: f.store, History: history,
		Health: f.health, Manifests: manifestService, Projects: prj, Authz: authz, Now: f.now,
	})
	runner := deployments.NewRunner(deployments.RunnerDeps{
		Authz: authz, Store: f.store, Plans: f.plans, CloudProfiles: clouds, Credentials: accounts, Variables: poolVars{}, Sources: canarySources{},
		Providers: providers.Registry(), Executors: map[string]deployments.Executor{"cloud-run": f.cloud}, Health: f.health, Watches: f.watches,
	})
	f.ramp = &shortHolds{Runner: runner}
	dispatcher, err := dispatch.New(dispatch.WithStore(ds))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Build(dispatcher, engine.WithDurableWorkflows(workflows.Options("release-legacy", 1)))
	if err != nil {
		t.Fatal(err)
	}
	workflows.Bind(f.service, f.ramp, func() *engine.Engine { return eng })
	workflows.BindRunStore(orchestration.NewPgRunStore(db))
	options := workflows.CanaryOptions("release", 4)
	options.PollInterval = 10 * time.Millisecond
	worker, err := drt.NewWorker(ds, options)
	if err != nil {
		t.Fatal(err)
	}
	workflows.BindWorker(orchestration.CurrentBuild, worker)
	f.run(t, worker)
	return f
}

// run works the durable tasks in the background, as the app's worker
// does.
func (f *releaseFixture) run(t *testing.T, worker *drt.Worker) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := worker.Run(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("worker: %v", err)
		}
	})
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
}

// now is the watch's clock: the wall clock, moved on by later.
func (f *releaseFixture) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return time.Now().Add(f.skew)
}

// later moves the watch's clock to at, or by a minute when at has
// passed, so every tick claims the watches again.
func (f *releaseFixture) later(at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if skew := time.Until(at); skew > f.skew+time.Minute {
		f.skew = skew
		return
	}
	f.skew += time.Minute
}

// tick runs the release-watch job once.
func (f *releaseFixture) tick(t *testing.T) {
	t.Helper()
	if err := f.watches.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// release analyzes a new commit of the repository, which the next plan of
// an environment moves to, and returns it.
func (f *releaseFixture) release(t *testing.T) string {
	t.Helper()
	f.commits++
	commit := fmt.Sprintf("c%039d", f.commits)
	r, err := discovery.Analyze(t.Context(), canaryRepository)
	if err != nil {
		t.Fatal(err)
	}
	g, err := json.Marshal(graph.Build(r))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_analyses(id,org_id,project_id,source_id,alias,status,repository,ref,commit_sha,graph,created_by,created_at)
		VALUES($1,'org','proj','canary-source','app','succeeded','acme/app','main',$2,$3,'usr',now())`, fmt.Sprintf("analysis-%d", f.commits), commit, g); err != nil {
		t.Fatal(err)
	}
	return commit
}

// plan plans environment, from another environment when from is set, and
// waits for planning to finish.
func (f *releaseFixture) plan(t *testing.T, environment, from string) *planning.Plan {
	t.Helper()
	p, err := f.service.Create(t.Context(), f.actor, "proj", planning.CreatePlanInput{Environment: environment, From: from})
	if err != nil {
		t.Fatal(err)
	}
	return f.waitPlan(t, p.ID, planning.StatusPlanning)
}

// waitPlan waits for a plan to leave status, and returns it.
func (f *releaseFixture) waitPlan(t *testing.T, id, status string) *planning.Plan {
	t.Helper()
	var p *planning.Plan
	eventually(t, "plan "+id+" leaves "+status, func() bool {
		var err error
		if p, err = f.plans.GetPlan(t.Context(), "org", id); err != nil {
			t.Fatal(err)
		}
		return p.Status != status
	})
	return p
}

// approve approves a ready plan as the test's admin and waits for its
// deployment to finish.
func (f *releaseFixture) approve(t *testing.T, p *planning.Plan) *deployments.Deployment {
	t.Helper()
	if _, err := f.service.Approve(t.Context(), f.actor, p.ID, planning.DecideInput{Revision: p.Revision}); err != nil {
		t.Fatal(err)
	}
	return f.deployed(t, p.ID)
}

// deployed waits for a plan's deployment to finish.
func (f *releaseFixture) deployed(t *testing.T, planID string) *deployments.Deployment {
	t.Helper()
	var d *deployments.Deployment
	eventually(t, "the deployment of "+planID, func() bool {
		var err error
		d, err = f.store.ForPlan(t.Context(), "org", planID)
		if errors.Is(err, deployments.ErrNotFound) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		return d.Status != deployments.StatusRunning
	})
	return d
}

// binding is what runs api in an environment.
func (f *releaseFixture) binding(t *testing.T, environmentID string) *planning.Binding {
	t.Helper()
	bs, err := f.plans.ListBindings(t.Context(), "org", environmentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bs {
		if b.Name == "api" {
			return b
		}
	}
	t.Fatalf("api isn't bound in %s", environmentID)
	return nil
}

// stepOf is a deployment's step for api.
func stepOf(t *testing.T, d *deployments.Deployment) deployments.Step {
	t.Helper()
	for _, s := range d.Steps {
		if s.ActionID == "service:api" {
			return s
		}
	}
	t.Fatalf("deployment %s has no step for api: %+v", d.ID, d.Steps)
	return deployments.Step{}
}

// promoteCanaryManifest releases production as a canary, and staging
// directly.
const promoteCanaryManifest = `version: 1
services:
  api:
    source: go:services/api
    provider: cloud-run
    port: 8080
environments:
  production:
    release:
      strategy: canary
      steps: [50, 100]
      hold: 1m
`

// A promotion into a canary environment ramps staging's image as the
// candidate and builds nothing; rolling that environment back deploys at
// once, with no canary, and arms no watch.
func TestPromote_aCanaryRampsStagingsImageAndARollbackDoesnt(t *testing.T) {
	f := newReleaseFixture(t, promoteCanaryManifest)
	ctx := t.Context()

	// Staging builds A, and production, which runs nothing yet, takes A's
	// image at once: a new service has nothing to compare a canary with.
	commitA := f.release(t)
	stagingA := f.plan(t, "staging", "")
	if stagingA.Status != planning.StatusReady || stagingA.Release != nil && len(stagingA.Release.Canary) != 0 {
		t.Fatalf("staging plan at A: %s %+v %+v", stagingA.Status, stagingA.Release, stagingA.Problems)
	}
	if d := f.approve(t, stagingA); d.Status != deployments.StatusSucceeded {
		t.Fatalf("staging at A: %+v", d)
	}
	imageA := f.binding(t, "stage").Revision
	promoteA := f.plan(t, "production", "staging")
	if promoteA.Status != planning.StatusReady || promoteA.Kind != planning.KindPromotion || len(promoteA.Release.Canary) != 0 {
		t.Fatalf("promotion at A: %s %+v %+v", promoteA.Status, promoteA.Release, promoteA.Problems)
	}
	firstProduction := f.approve(t, promoteA)
	if firstProduction.Status != deployments.StatusSucceeded || firstProduction.Canary != nil || stepOf(t, firstProduction).Image != imageA {
		t.Fatalf("production at A: %+v", firstProduction)
	}

	// Staging builds B; production ramps B's image as the candidate.
	commitB := f.release(t)
	if d := f.approve(t, f.plan(t, "staging", "")); d.Status != deployments.StatusSucceeded {
		t.Fatalf("staging at B: %+v", d)
	}
	imageB := f.binding(t, "stage").Revision
	if built := f.cloud.built(); !slices.Equal(built, []string{commitA, commitB}) || imageB == imageA {
		t.Fatalf("builds after staging at B: %v, images %s %s", built, imageA, imageB)
	}
	promoteB := f.plan(t, "production", "staging")
	if promoteB.Status != planning.StatusReady || promoteB.Release == nil || !slices.Equal(promoteB.Release.Canary, []string{"service:api"}) {
		t.Fatalf("promotion at B: %s %+v %+v", promoteB.Status, promoteB.Release, promoteB.Problems)
	}
	if len(promoteB.Artifacts) != 1 || !promoteB.Artifacts[0].Reused || promoteB.Artifacts[0].Reference != imageB {
		t.Fatalf("promotion at B artifacts: %+v", promoteB.Artifacts)
	}
	ramped := f.approve(t, promoteB)
	if ramped.Status != deployments.StatusSucceeded || ramped.Canary == nil || ramped.Canary.Status != deployments.CanaryPromoted ||
		len(ramped.Canary.Services) != 1 || ramped.Canary.Services[0].Image != imageB || ramped.Canary.Services[0].Baseline != "api-00002" {
		t.Fatalf("canary promotion: %+v canary %+v", ramped, ramped.Canary)
	}
	if s := stepOf(t, ramped); s.Image != imageB || s.Revision != imageB {
		t.Fatalf("canary step: %+v", s)
	}
	if built := f.cloud.built(); len(built) != 2 {
		t.Fatalf("the canary built: %v", built)
	}
	if b := f.binding(t, "env"); b.Revision != imageB || b.PlanID != promoteB.ID {
		t.Fatalf("production after the canary: %+v", b)
	}
	if ramps := f.ramp.read(); len(ramps) != 1 {
		t.Fatalf("ramps: %+v", ramps)
	}
	log := f.cloud.entries()
	if got := strings.Join(log[len(log)-4:], "; "); got != "candidate production api-00004 "+imageB+"; weigh api-00004 50; weigh api-00004 100; promote api-00004" {
		t.Fatalf("cloud calls of the canary: %s", got)
	}

	// The rollback goes straight back to A's image, all at once.
	rollback, err := f.service.CreateRollback(ctx, f.actor, "proj", planning.RollbackInput{Environment: "production", Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	rollback = f.waitPlan(t, rollback.ID, planning.StatusPlanning)
	if rollback.Status != planning.StatusReady || rollback.Release == nil || rollback.Release.Strategy != "canary" || len(rollback.Release.Canary) != 0 {
		t.Fatalf("rollback plan: %s %+v %+v %+v", rollback.Status, rollback.Release, rollback.Problems, rollback.Warnings)
	}
	if len(rollback.Artifacts) != 1 || !rollback.Artifacts[0].Reused || rollback.Artifacts[0].Reference != imageA {
		t.Fatalf("rollback artifacts: %+v", rollback.Artifacts)
	}
	undone := f.approve(t, rollback)
	if undone.Status != deployments.StatusSucceeded || undone.Canary != nil || stepOf(t, undone).Image != imageA {
		t.Fatalf("rollback deployment: %+v canary %+v", undone, undone.Canary)
	}
	log = f.cloud.entries()
	if got := log[len(log)-1]; got != "apply production api-00005 "+imageA {
		t.Fatalf("the rollback's cloud call: %s", got)
	}
	if built, ramps := f.cloud.built(), f.ramp.read(); len(built) != 2 || len(ramps) != 1 {
		t.Fatalf("the rollback built %v and ramped %+v", built, ramps)
	}
	if b := f.binding(t, "env"); b.Revision != imageA || b.CommitSHA != commitA {
		t.Fatalf("production after the rollback: %+v", b)
	}
	if _, err = f.watchStore.ForDeployment(ctx, "org", undone.ID); !errors.Is(err, releasewatch.ErrNotFound) {
		t.Fatalf("the rollback armed a watch: %v", err)
	}
}

// watchedManifest releases production directly and watches it for 15
// minutes afterwards.
const watchedManifest = `version: 1
services:
  api:
    source: go:services/api
    provider: cloud-run
    port: 8080
environments:
  production:
    release:
      strategy: direct
      watch: 15m
`

// breachedRelease deploys A then B to production, arms B's watch, makes
// B's health fail and ticks until the watch has planned a rollback. It
// returns B's watch, the rollback plan once it's ready, and A's image.
func breachedRelease(t *testing.T, f *releaseFixture) (*releasewatch.Watch, *planning.Plan, string) {
	t.Helper()
	f.release(t)
	if d := f.approve(t, f.plan(t, "production", "")); d.Status != deployments.StatusSucceeded {
		t.Fatalf("production at A: %+v", d)
	}
	imageA := f.binding(t, "env").Revision
	f.release(t)
	bad := f.approve(t, f.plan(t, "production", ""))
	if bad.Status != deployments.StatusSucceeded {
		t.Fatalf("production at B: %+v", bad)
	}
	// The finish arms the watch once the deployment has settled.
	var w *releasewatch.Watch
	var err error
	eventually(t, "B's watch", func() bool {
		w, err = f.watchStore.ForDeployment(t.Context(), "org", bad.ID)
		if err != nil && !errors.Is(err, releasewatch.ErrNotFound) {
			t.Fatal(err)
		}
		return err == nil
	})
	if w.Status != releasewatch.StatusWatching || len(w.Services) != 1 || w.Services[0].Op != string(planning.Update) {
		t.Fatalf("B's watch: %+v", w)
	}
	// From B's start, api answers 8% of its requests with a 5xx.
	f.health.failSince(bad.StartedAt.Truncate(time.Minute))
	f.later(w.StartsAt.Add(releasehealth.MetricsLag + 3*time.Minute))
	f.tick(t)
	if w, err = f.watchStore.ForDeployment(t.Context(), "org", bad.ID); err != nil || w.Status != releasewatch.StatusRollingBack ||
		w.Verdict != string(releasehealth.Fail) || !strings.HasPrefix(w.Reason, "api: ") || w.RollbackPlanID == "" {
		t.Fatalf("B's watch after the breach: %+v %v", w, err)
	}
	plan := f.waitPlan(t, w.RollbackPlanID, planning.StatusPlanning)
	if plan.Status != planning.StatusReady || plan.Kind != planning.KindRollback || plan.Rollback.Trigger != planning.TriggerWatch ||
		plan.Rollback.WatchID != w.ID || plan.Rollback.TargetDeploymentID != bad.ID || plan.CreatedBy != security.SystemSubject("org") {
		t.Fatalf("the watch's rollback plan: %s %+v %+v %+v", plan.Status, plan.Rollback, plan.Problems, plan)
	}
	return w, plan, imageA
}

// A release whose watch breaches is rolled back end to end: Loomplane
// automation plans the rollback, approves it through Warden, and the
// earlier image runs again without a build.
func TestReleaseWatch_aBreachRollsTheReleaseBack(t *testing.T) {
	f := newReleaseFixture(t, watchedManifest)
	w, plan, imageA := breachedRelease(t, f)

	f.later(time.Time{})
	f.tick(t)
	approved := f.waitPlan(t, plan.ID, planning.StatusReady)
	if approved.Status != planning.StatusApproved || approved.DecidedBy != security.SystemSubject("org") {
		t.Fatalf("the rollback plan after the tick: %s by %q: %q", approved.Status, approved.DecidedBy, approved.DecisionNote)
	}
	d := f.deployed(t, plan.ID)
	if d.Status != deployments.StatusSucceeded || stepOf(t, d).Image != imageA {
		t.Fatalf("the rollback deployment: %+v", d)
	}
	if built := f.cloud.built(); len(built) != 2 {
		t.Fatalf("the rollback built: %v", built)
	}
	if b := f.binding(t, "env"); b.Revision != imageA || b.PlanID != plan.ID {
		t.Fatalf("production after the rollback: %+v", b)
	}
	f.later(time.Time{})
	f.tick(t)
	settled, err := f.watchStore.ForDeployment(t.Context(), "org", w.DeploymentID)
	if err != nil || settled.Status != releasewatch.StatusRolledBack || settled.SettledAt == nil || settled.RollbackApprovedAt == nil {
		t.Fatalf("B's watch after the rollback: %+v %v", settled, err)
	}
	if _, err = f.watchStore.ForDeployment(t.Context(), "org", d.ID); !errors.Is(err, releasewatch.ErrNotFound) {
		t.Fatalf("the rollback armed a watch: %v", err)
	}
}

// With its Warden assignment gone, Loomplane automation still plans the
// rollback it was allowed to, but can't approve it: the plan waits, ready,
// for a person, and nothing deploys until one approves it.
func TestReleaseWatch_automationWithoutItsRoleLeavesTheRollbackReady(t *testing.T) {
	f := newReleaseFixture(t, watchedManifest)
	w, plan, imageA := breachedRelease(t, f)
	running := f.binding(t, "env")

	system := security.SystemSubject("org")
	if err := f.roles.DeleteAssignmentsBySubject(t.Context(), "org", string(warden.SubjectServiceAcct), system); err != nil {
		t.Fatal(err)
	}
	f.warden.InvalidateSubject(t.Context(), "org", warden.SubjectServiceAcct, system)
	f.later(time.Time{})
	f.tick(t)
	blocked, err := f.watchStore.ForDeployment(t.Context(), "org", w.DeploymentID)
	if err != nil || blocked.Status != releasewatch.StatusRollbackBlocked || blocked.RollbackApprovedAt != nil ||
		blocked.Reason != "Loomplane automation can't approve deployments in this organization; approve the rollback plan yourself" {
		t.Fatalf("B's watch once the automation lost its role: %+v %v", blocked, err)
	}
	// Nothing deploys, however long it waits.
	time.Sleep(500 * time.Millisecond)
	if p, perr := f.plans.GetPlan(t.Context(), "org", plan.ID); perr != nil || p.Status != planning.StatusReady || p.DecidedBy != "" {
		t.Fatalf("the rollback plan: %+v %v", p, perr)
	}
	if _, err = f.store.ForPlan(t.Context(), "org", plan.ID); !errors.Is(err, deployments.ErrNotFound) {
		t.Fatalf("the rollback deployed: %v", err)
	}
	if after := f.binding(t, "env"); after.Revision != running.Revision || after.PlanID != running.PlanID {
		t.Fatalf("production changed: %+v", after)
	}
	if _, err = f.service.ApproveAutomatic(t.Context(), "org", plan.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("the automation approved without its role: %v", err)
	}

	// A person approves it.
	if d := f.approve(t, plan); d.Status != deployments.StatusSucceeded || stepOf(t, d).Image != imageA {
		t.Fatalf("the rollback a person approved: %+v", d)
	}
}
