package planning

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
)

const (
	envStaging    = "env_staging"
	envProduction = "env_production"
)

// promoStore keeps plans and what each environment runs in memory. Every
// Store method it doesn't define panics through the nil embedded interface.
type promoStore struct {
	Store
	mu       sync.Mutex
	plans    map[string]*Plan
	deployed map[string][]*DeployedSource // by environment ID
	manifest map[string]*DeployedManifest // by environment ID
}

func clonePlan(p *Plan) *Plan {
	c := *p
	c.Sources = slices.Clone(p.Sources)
	c.Warnings = slices.Clone(p.Warnings)
	return &c
}

func (s *promoStore) CreatePlan(_ context.Context, p *Plan) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plans[p.ID] = clonePlan(p)
	return nil, nil
}

func (s *promoStore) GetPlan(_ context.Context, _, id string) (*Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clonePlan(p), nil
}

func (s *promoStore) FinishPlanning(_ context.Context, p *Plan) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plans[p.ID].Status != StatusPlanning {
		return false, nil
	}
	s.plans[p.ID] = clonePlan(p)
	return true, nil
}

func (s *promoStore) FailPlanning(_ context.Context, _, id, reason string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plans[id].Status, s.plans[id].Error = StatusFailed, reason
	return nil
}

func (s *promoStore) DeployedSources(_ context.Context, _, environmentID string) ([]*DeployedSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.deployed[environmentID]), nil
}

func (s *promoStore) DeployedState(_ context.Context, _, environmentID string) (*DeployedManifest, []*DeployedSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manifest[environmentID], slices.Clone(s.deployed[environmentID]), nil
}

func (s *promoStore) ListBindings(context.Context, string, string) ([]*Binding, error) {
	return nil, nil
}

func (s *promoStore) EnvironmentConnections(context.Context, string, string) (map[string]string, error) {
	return nil, nil
}

// promoAnalyses are a project's sources, their analyses newest first, and
// an empty graph for every analysis.
type promoAnalyses struct {
	sources  []*discovery.Source
	analyses map[string][]*discovery.Analysis
}

func (a *promoAnalyses) ListSources(context.Context, string, string, bool) ([]*discovery.Source, error) {
	return a.sources, nil
}

func (a *promoAnalyses) ListSourceAnalyses(_ context.Context, _, sourceID string, _ int) ([]*discovery.Analysis, error) {
	return a.analyses[sourceID], nil
}

func (a *promoAnalyses) Graph(context.Context, string, string) (*graph.Graph, error) {
	return &graph.Graph{}, nil
}

// promoManifests are a project's manifest revisions, and the IDs compute
// asked for.
type promoManifests struct {
	mu     sync.Mutex
	byID   map[string]*manifests.Revision
	latest string
	asked  []string
}

func (m *promoManifests) Latest(context.Context, string, string) (*manifests.Revision, error) {
	return m.byID[m.latest], nil
}

func (m *promoManifests) Get(_ context.Context, _, id string) (*manifests.Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked = append(m.asked, id)
	r, ok := m.byID[id]
	if !ok {
		return nil, manifests.ErrNotFound
	}
	return r, nil
}

type promoProjects struct{}

func (promoProjects) GetProject(_ context.Context, orgID, id string) (*projects.Project, error) {
	return &projects.Project{ID: id, OrgID: orgID}, nil
}

func (promoProjects) ListEnvironments(context.Context, string, string) ([]*projects.Environment, error) {
	return []*projects.Environment{
		{ID: envStaging, Name: "staging", Kind: "staging"},
		{ID: envProduction, Name: "production", Kind: "production"},
	}, nil
}

type allowAll struct{}

func (allowAll) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

// promoWorkflow plans each plan as soon as it starts, so Create returns it
// planned.
type promoWorkflow struct{ svc *Service }

func (w *promoWorkflow) Start(ctx context.Context, orgID, planID string, _ time.Duration) error {
	_, err := w.svc.Plan(ctx, orgID, planID)
	return err
}

func (*promoWorkflow) Decide(context.Context, string, string, orchestration.Decision) error {
	return nil
}

func (*promoWorkflow) Cancel(context.Context, string, string) error { return nil }

type promoAudit struct{ events []projects.AuditEvent }

func (a *promoAudit) Record(_ context.Context, e projects.AuditEvent) { a.events = append(a.events, e) }

type promoWorld struct {
	svc       *Service
	store     *promoStore
	analyses  *promoAnalyses
	manifests *promoManifests
	audit     *promoAudit
}

// promotionWorld is a project with web and api. Staging runs web anl_1@aaa
// and api anl_2@bbb, both from plan_s1 and manifest revision 5; production
// runs web anl_4@eee and api anl_5@ccc. A push since landed web anl_9@fff,
// and the latest manifest is revision 7. Web's root and branch changed
// after plan_s1 deployed it.
func promotionWorld(t *testing.T) *promoWorld {
	t.Helper()
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	revisions := map[string]*manifests.Revision{}
	for n, id := range map[int]string{5: "man_5", 6: "man_6", 7: "man_7"} {
		revisions[id] = &manifests.Revision{ID: id, Revision: n, Manifest: parse(t, "version: 1\n"), Digest: "digest-" + id}
	}
	w := &promoWorld{
		store: &promoStore{
			plans: map[string]*Plan{
				"plan_s1": {
					ID: "plan_s1", OrgID: "org", ProjectID: "proj", EnvironmentID: envStaging, Environment: "staging", Status: StatusApproved,
					Sources: []PlanSource{
						{SourceID: "src_api", Alias: "api", Repository: "acme/api", AnalysisID: "anl_2", CommitSHA: "bbb", InScope: true},
						{SourceID: "src_web", Alias: "web", Repository: "acme/web", Root: "apps/web", Ref: "release", AnalysisID: "anl_1", CommitSHA: "aaa", InScope: true},
					},
				},
			},
			deployed: map[string][]*DeployedSource{
				envStaging: {
					{EnvironmentID: envStaging, SourceID: "src_api", AnalysisID: "anl_2", CommitSHA: "bbb", PlanID: "plan_s1", DeployedAt: at},
					{EnvironmentID: envStaging, SourceID: "src_web", AnalysisID: "anl_1", CommitSHA: "aaa", PlanID: "plan_s1", DeployedAt: at},
				},
				envProduction: {
					{EnvironmentID: envProduction, SourceID: "src_api", AnalysisID: "anl_5", CommitSHA: "ccc", PlanID: "plan_p1", DeployedAt: at},
					{EnvironmentID: envProduction, SourceID: "src_web", AnalysisID: "anl_4", CommitSHA: "eee", PlanID: "plan_p1", DeployedAt: at},
				},
			},
			manifest: map[string]*DeployedManifest{
				envStaging: {EnvironmentID: envStaging, ManifestID: "man_5", ManifestRevision: 5, PlanID: "plan_s1", DeployedAt: at},
			},
		},
		analyses: &promoAnalyses{
			sources: []*discovery.Source{
				{ID: "src_web", Alias: "web", Repository: "acme/web", Root: "apps/web2", Ref: "main"},
				{ID: "src_api", Alias: "api", Repository: "acme/api"},
			},
			analyses: map[string][]*discovery.Analysis{
				"src_web": {
					{ID: "anl_9", SourceID: "src_web", Status: discovery.StatusSucceeded, CommitSHA: "fff"},
					{ID: "anl_1", SourceID: "src_web", Status: discovery.StatusSucceeded, CommitSHA: "aaa"},
				},
				"src_api": {{ID: "anl_2", SourceID: "src_api", Status: discovery.StatusSucceeded, CommitSHA: "bbb"}},
			},
		},
		manifests: &promoManifests{byID: revisions, latest: "man_7"},
		audit:     &promoAudit{},
	}
	workflow := &promoWorkflow{}
	w.svc = NewService(Deps{
		Store: w.store, Projects: promoProjects{}, Analyses: w.analyses, Manifests: w.manifests,
		Providers: providers.Registry(), Authz: allowAll{}, Workflow: workflow, Audit: w.audit, Window: time.Hour,
	})
	workflow.svc = w.svc
	return w
}

func (w *promoWorld) create(t *testing.T, in CreatePlanInput) (*Plan, error) {
	t.Helper()
	return w.svc.Create(t.Context(), security.Principal{UserID: "usr", OrgID: "org"}, "proj", in)
}

func (w *promoWorld) promote(t *testing.T, scope ...string) *Plan {
	t.Helper()
	plan, err := w.create(t, CreatePlanInput{Environment: "production", From: " staging ", Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != StatusReady {
		t.Fatalf("plan %s: %s", plan.Status, plan.Error)
	}
	return plan
}

// pin is what a test checks of each pinned source.
type pin struct {
	Alias, AnalysisID, CommitSHA, FromPlanID string
	InScope                                  bool
}

func pins(ps []PlanSource) []pin {
	out := make([]pin, 0, len(ps))
	for _, s := range ps {
		out = append(out, pin{s.Alias, s.AnalysisID, s.CommitSHA, s.FromPlanID, s.InScope})
	}
	return out
}

func newerManifest(p *Plan) []string {
	var out []string
	for _, f := range p.Warnings {
		if f.Code == "newer-manifest" {
			out = append(out, f.Message)
		}
	}
	return out
}

func TestCreateFrom_pinsWhatTheSourceEnvironmentRunsNotTheNewestAnalysis(t *testing.T) {
	w := promotionWorld(t)
	plan := w.promote(t)
	want := []pin{{"api", "anl_2", "bbb", "plan_s1", true}, {"web", "anl_1", "aaa", "plan_s1", true}}
	if got := pins(plan.Sources); !reflect.DeepEqual(got, want) {
		t.Fatalf("pins %+v, want %+v", got, want)
	}
	if plan.Kind != KindPromotion || plan.From != "staging" || plan.FromEnvironmentID != envStaging {
		t.Fatalf("kind %q from %q (%q)", plan.Kind, plan.From, plan.FromEnvironmentID)
	}
	e := w.audit.events[len(w.audit.events)-1]
	if e.Action != "plan.create" || e.Attributes["kind"] != KindPromotion || e.Attributes["from"] != "staging" {
		t.Fatalf("audit %+v", e)
	}

	// Without From, the same project plans the newest analysis.
	release, err := w.create(t, CreatePlanInput{Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	if release.Kind != KindRelease || release.From != "" || release.Sources[1].CommitSHA != "fff" {
		t.Fatalf("release %q from %q pins %+v", release.Kind, release.From, pins(release.Sources))
	}
	if e = w.audit.events[len(w.audit.events)-1]; e.Attributes["kind"] != KindRelease || e.Attributes["from"] != "" {
		t.Fatalf("audit %+v", e)
	}
}

func TestCreateFrom_usesTheManifestTheSourceEnvironmentDeployedWith(t *testing.T) {
	w := promotionWorld(t)
	plan := w.promote(t)
	if plan.ManifestID != "man_5" || plan.ManifestRevision != 5 {
		t.Fatalf("manifest %s revision %d", plan.ManifestID, plan.ManifestRevision)
	}
	if slices.Contains(w.manifests.asked, "man_7") || !slices.Contains(w.manifests.asked, "man_5") {
		t.Fatalf("compute read manifests %v", w.manifests.asked)
	}
}

func TestCreateFrom_warnsWhenANewerManifestExists(t *testing.T) {
	w := promotionWorld(t)
	want := []string{"This promotion uses manifest revision 5, which staging deployed with. Revision 7 is newer; deploy it to staging first to promote it."}
	plan := w.promote(t)
	if got := newerManifest(plan); !slices.Equal(got, want) {
		t.Fatalf("warnings %q", got)
	}
	// The warning is part of the revision, so saving another manifest
	// revision makes the open promotion stale on approval.
	w.manifests.byID["man_8"] = &manifests.Revision{ID: "man_8", Revision: 8, Manifest: parse(t, "version: 1\n"), Digest: "digest-man_8"}
	w.manifests.latest = "man_8"
	if _, revision, err := w.svc.compute(t.Context(), plan); err != nil || revision == plan.Revision {
		t.Fatalf("after revision 8 was saved: same revision %v, %v", revision == plan.Revision, err)
	}
	w.manifests.latest = "man_7"

	w.store.manifest[envStaging] = &DeployedManifest{EnvironmentID: envStaging, ManifestID: "man_7", ManifestRevision: 7, PlanID: "plan_s1"}
	if got := newerManifest(w.promote(t)); len(got) != 0 {
		t.Fatalf("at the latest revision: %q", got)
	}

	w.store.manifest[envStaging] = &DeployedManifest{EnvironmentID: envStaging, ManifestID: "man_5", ManifestRevision: 5, PlanID: "plan_s1"}
	release, err := w.create(t, CreatePlanInput{Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	if got := newerManifest(release); len(got) != 0 {
		t.Fatalf("a plan without From: %q", got)
	}
}

func TestCreateFrom_fallsBackToTheLatestManifestWhenNoneIsRecorded(t *testing.T) {
	w := promotionWorld(t)
	delete(w.store.manifest, envStaging)
	plan := w.promote(t)
	if plan.ManifestID != "man_7" || plan.ManifestRevision != 7 {
		t.Fatalf("manifest %s revision %d", plan.ManifestID, plan.ManifestRevision)
	}
	want := []string{"staging's deployed manifest revision isn't recorded; this promotion uses the latest, revision 7"}
	if got := newerManifest(plan); !slices.Equal(got, want) {
		t.Fatalf("warnings %q", got)
	}
}

func TestCreateFrom_copiesRootAndRefFromTheSourcePlan(t *testing.T) {
	w := promotionWorld(t)
	plan := w.promote(t)
	web := plan.Sources[slices.IndexFunc(plan.Sources, func(s PlanSource) bool { return s.Alias == "web" })]
	if web.Root != "apps/web" || web.Ref != "release" {
		t.Fatalf("web root %q ref %q", web.Root, web.Ref)
	}
}

func TestCreateFrom_scopeLimitsWhatMoves(t *testing.T) {
	w := promotionWorld(t)
	plan := w.promote(t, "web")
	want := []pin{{"api", "anl_5", "ccc", "", false}, {"web", "anl_1", "aaa", "plan_s1", true}}
	if got := pins(plan.Sources); !reflect.DeepEqual(got, want) {
		t.Fatalf("pins %+v, want %+v", got, want)
	}
}

func TestCreateFrom_holdsSourcesTheSourceEnvironmentDoesntRun(t *testing.T) {
	w := promotionWorld(t)
	w.store.deployed[envStaging] = w.store.deployed[envStaging][1:] // web only
	plan := w.promote(t)
	want := []pin{{"api", "anl_5", "ccc", "", false}, {"web", "anl_1", "aaa", "plan_s1", true}}
	if got := pins(plan.Sources); !reflect.DeepEqual(got, want) {
		t.Fatalf("pins %+v, want %+v", got, want)
	}

	w.store.deployed[envProduction] = w.store.deployed[envProduction][1:] // web only
	_, err := w.create(t, CreatePlanInput{Environment: "production", From: "staging"})
	if !errors.Is(err, ErrNeverDeployed) || !strings.Contains(err.Error(), "api") || !strings.Contains(err.Error(), "production") {
		t.Fatalf("err %v", err)
	}
}

func TestCreateFrom_refusesAScopeNamingWhatTheSourceDoesntRun(t *testing.T) {
	w := promotionWorld(t)
	w.store.deployed[envStaging] = w.store.deployed[envStaging][1:] // web only
	_, err := w.create(t, CreatePlanInput{Environment: "production", From: "staging", Scope: []string{"api"}})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "staging doesn't run api") {
		t.Fatalf("err %v", err)
	}
}

func TestCreateFrom_refusesBadSources(t *testing.T) {
	tests := []struct {
		name  string
		in    CreatePlanInput
		setup func(*promoWorld)
		want  error
	}{
		{name: "itself", in: CreatePlanInput{Environment: "production", From: "production"}, want: ErrInvalid},
		{name: "unknown", in: CreatePlanInput{Environment: "production", From: "qa"}, want: projects.ErrEnvironmentNotFound},
		{name: "catalog", in: CreatePlanInput{Environment: "production", From: "staging", Catalog: true}, want: ErrInvalid},
		{
			name: "runs nothing", in: CreatePlanInput{Environment: "production", From: "staging"},
			setup: func(w *promoWorld) { delete(w.store.deployed, envStaging) }, want: ErrNotRunning,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := promotionWorld(t)
			if tt.setup != nil {
				tt.setup(w)
			}
			if _, err := w.create(t, tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("err %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCreateFrom_revisionCoversTheSourceEnvironment(t *testing.T) {
	w := promotionWorld(t)
	w.store.manifest[envStaging].ManifestID, w.store.manifest[envStaging].ManifestRevision = "man_7", 7
	release := &Plan{
		ID: "plan_r", OrgID: "org", ProjectID: "proj", EnvironmentID: envProduction, Environment: "production",
		Kind: KindRelease, ManifestID: "man_7", ManifestRevision: 7,
		Sources: []PlanSource{{SourceID: "src_web", Alias: "web", Repository: "acme/web", AnalysisID: "anl_1", CommitSHA: "aaa", InScope: true}},
	}
	promotion := clonePlan(release)
	promotion.Kind, promotion.From, promotion.FromEnvironmentID = KindPromotion, "staging", envStaging
	ctx := t.Context()
	releaseResult, releaseRevision, err := w.svc.compute(ctx, release)
	if err != nil {
		t.Fatal(err)
	}
	promotionResult, promotionRevision, err := w.svc.compute(ctx, promotion)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(releaseResult, promotionResult) {
		t.Fatalf("results differ: %+v and %+v", releaseResult, promotionResult)
	}
	if releaseRevision == promotionRevision {
		t.Fatal("a promotion and a release with the same sources and result share a revision")
	}
}

func TestPinsMatchSource(t *testing.T) {
	w := promotionWorld(t)
	ctx := t.Context()
	plan := w.promote(t)
	ok, diffs, err := w.svc.PinsMatchSource(ctx, "org", plan)
	if err != nil || !ok || len(diffs) != 0 {
		t.Fatalf("unchanged: %v %q %v", ok, diffs, err)
	}

	web := w.store.deployed[envStaging][1]
	w.store.deployed[envStaging][1] = &DeployedSource{EnvironmentID: envStaging, SourceID: "src_web", AnalysisID: "anl_8", CommitSHA: "ddd", PlanID: "plan_s2"}
	ok, diffs, err = w.svc.PinsMatchSource(ctx, "org", plan)
	if err != nil || ok || !slices.Equal(diffs, []string{"web: staging runs ddd, the plan pins aaa"}) {
		t.Fatalf("after web moved: %v %q %v", ok, diffs, err)
	}

	w.store.deployed[envStaging][1] = web
	w.store.manifest[envStaging] = &DeployedManifest{EnvironmentID: envStaging, ManifestID: "man_6", ManifestRevision: 6, PlanID: "plan_s2"}
	ok, diffs, err = w.svc.PinsMatchSource(ctx, "org", plan)
	if err != nil || ok || !slices.Equal(diffs, []string{"manifest: staging runs revision 6, the plan pins 5"}) {
		t.Fatalf("after the manifest moved: %v %q %v", ok, diffs, err)
	}

	release := &Plan{OrgID: "org", Kind: KindRelease, Sources: plan.Sources}
	ok, diffs, err = w.svc.PinsMatchSource(ctx, "org", release)
	if err != nil || !ok || diffs != nil {
		t.Fatalf("without From: %v %q %v", ok, diffs, err)
	}
}
