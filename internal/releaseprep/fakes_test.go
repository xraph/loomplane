package releaseprep

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

// Commits of the fixture: production runs api@shaA, staging runs api@shaB.
var (
	shaA = strings.Repeat("a", 40)
	shaB = strings.Repeat("b", 40)
	shaC = strings.Repeat("c", 40)
	shaW = strings.Repeat("e", 40)
)

var collectedAt = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

// world is a project whose staging release is ready to promote: every
// source answers, and every gate would pass.
type world struct {
	plan       *planning.Plan
	staging    *projects.Environment
	production *projects.Environment
	project    *projects.Project

	plans    *fakePlans
	watches  *fakeWatches
	canaries *fakeCanaries
	projects *fakeProjects
	budgets  *fakeBudgets
	authz    *fakeAuthz
	reports  *fakeReports
	diffs    *fakeDiffs
}

func greenWorld() *world {
	staging := &projects.Environment{ID: "env_stg", OrgID: "org_a", ProjectID: "proj_a", Name: "staging", Kind: projects.KindStaging}
	production := &projects.Environment{ID: "env_prd", OrgID: "org_a", ProjectID: "proj_a", Name: "production", Kind: projects.KindProduction}
	plan := &planning.Plan{
		ID: "plan_a", OrgID: "org_a", ProjectID: "proj_a", EnvironmentID: production.ID, Environment: production.Name,
		Kind: planning.KindPromotion, From: staging.Name, FromEnvironmentID: staging.ID,
		Status: planning.StatusReady, Revision: "rev_1", CreatedBy: "agent:org_a",
		Sources: []planning.PlanSource{
			{SourceID: "src_api", Alias: "api", Repository: "acme/api", CommitSHA: shaB, InScope: true},
			{SourceID: "src_web", Alias: "web", Repository: "acme/web", CommitSHA: shaW, InScope: false},
		},
		Actions: []planning.Action{
			{ID: "service:api", Op: planning.Update, Subject: planning.ServiceSubject, Name: "api", Provider: "cloud-run", Service: &planning.ServiceConfig{}},
		},
		Problems:  []planning.Finding{},
		Warnings:  []planning.Finding{},
		Artifacts: []planning.ArtifactChoice{{Service: "api", Reused: true, Reference: "acme/api@sha256:1", From: "staging"}},
		Release:   &planning.Release{Strategy: "canary", Canary: []string{"service:api"}},
	}
	finished := collectedAt.Add(-24 * time.Hour)
	w := &world{
		plan: plan, staging: staging, production: production,
		project: &projects.Project{ID: "proj_a", OrgID: "org_a", Name: "Acme", AutonomousPromotion: true},
		plans: &fakePlans{
			deployed: map[string][]*planning.DeployedRepository{
				"production": {
					{SourceID: "src_api", Alias: "api", Repository: "acme/api", CommitSHA: shaA},
					{SourceID: "src_web", Alias: "web", Repository: "acme/web", CommitSHA: shaW},
				},
			},
			pinsMatch: true,
		},
		watches: &fakeWatches{
			soak: &releasewatch.Soak{
				Environment: "staging", Running: true, Since: collectedAt.Add(-2 * time.Hour), Duration: 2 * time.Hour, Healthy: true,
				Reasons: []string{}, Services: []releasewatch.SoakService{{Name: "api", Provider: "cloud-run", Verdict: releasehealth.Pass}},
			},
			evidence: map[string]*releasewatch.ReleaseEvidence{
				"staging":    {Environment: "staging", DeploymentID: "dep_s1", Watched: true, Status: "passed", Verdict: "pass", Settled: true},
				"production": {Environment: "production", DeploymentID: "dep_p1", Watched: true, Status: "passed", Verdict: "pass", Settled: true},
			},
			coverage: &releasewatch.Coverage{
				Environment: "production", Duration: 15 * time.Minute, Canary: []string{"api"},
				Services: []releasewatch.ServiceCoverage{{Name: "worker", Provider: "cloud-run", Watchable: true}},
			},
		},
		canaries: &fakeCanaries{runs: []deployments.CanaryRun{{
			DeploymentID: "dep_p1", PlanID: "plan_0", Environment: "production", Status: "succeeded", FinishedAt: &finished,
			Canary: &deployments.Canary{Status: deployments.CanaryPromoted, Services: []deployments.CanaryService{{ActionID: "service:api", Name: "api", Provider: "cloud-run"}}},
		}}},
		projects: &fakeProjects{},
		budgets:  &fakeBudgets{state: agentsettings.BudgetState{LimitUSD: "25.00", SpentUSD: "5.00"}},
		authz:    &fakeAuthz{},
		reports:  &fakeReports{},
		diffs: &fakeDiffs{answer: &repositories.Comparison{
			Status: "ahead", AheadBy: 1, TotalCommits: 1,
			Commits: []repositories.ComparedCommit{{SHA: shaB, Message: "Add users", Author: "Ada", Date: collectedAt}},
			Files:   []repositories.ChangedFile{{Filename: "api/users.go", Status: "added", Additions: 10}},
		}},
	}
	w.projects.w = w
	return w
}

func (w *world) sources() Sources {
	return Sources{
		Plans: w.plans, Watches: w.watches, Canaries: w.canaries, Projects: w.projects,
		Budgets: w.budgets, Authz: w.authz, Reports: w.reports, Diffs: w.diffs,
	}
}

func (w *world) collector() *Collector {
	return &Collector{Sources: w.sources(), Now: func() time.Time { return collectedAt }}
}

// agent is the agent acting for usr_dev, who asked for the release.
func agent() security.Principal {
	return security.AgentPrincipal("org_a", &security.Principal{OrgID: "org_a", UserID: "usr_dev"})
}

func (w *world) collect(diffs []SourceDiff) *ReadinessEvidence {
	return w.collector().Collect(context.Background(), CollectInput{
		Principal: agent(), Plan: w.plan, From: w.staging, To: w.production, Diffs: diffs,
	})
}

type fakePlans struct {
	mu          sync.Mutex
	deployed    map[string][]*planning.DeployedRepository
	deployedErr error
	pinsMatch   bool
	pinDiffs    []string
	pinsErr     error
	pinCalls    int
}

func (f *fakePlans) Create(context.Context, security.Principal, string, planning.CreatePlanInput) (*planning.Plan, error) {
	return nil, errors.New("not in this test")
}

func (f *fakePlans) Get(context.Context, security.Principal, string) (*planning.Plan, error) {
	return nil, errors.New("not in this test")
}

func (f *fakePlans) List(context.Context, security.Principal, string, string, page.Cursor, int) ([]*planning.Plan, page.Cursor, error) {
	return nil, page.Cursor{}, errors.New("not in this test")
}

func (f *fakePlans) Deployed(_ context.Context, _ security.Principal, _, environment string) ([]*planning.DeployedRepository, error) {
	if f.deployedErr != nil {
		return nil, f.deployedErr
	}
	return f.deployed[environment], nil
}

func (f *fakePlans) PinsMatchSource(context.Context, string, *planning.Plan) (bool, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pinCalls++
	if f.pinsErr != nil {
		return false, nil, f.pinsErr
	}
	return f.pinsMatch, f.pinDiffs, nil
}

type fakeWatches struct {
	mu          sync.Mutex
	soak        *releasewatch.Soak
	soakErr     error
	soakCalls   int
	evidence    map[string]*releasewatch.ReleaseEvidence
	evidenceErr map[string]error
	coverage    *releasewatch.Coverage
	coverageErr error
	covCalls    int
}

func (f *fakeWatches) SoakForPlan(context.Context, security.Principal, string) (*releasewatch.Soak, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.soakCalls++
	if f.soakErr != nil {
		return nil, f.soakErr
	}
	return f.soak, nil
}

func (f *fakeWatches) Armable(context.Context, security.Principal, string, string) (*releasewatch.Coverage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.covCalls++
	if f.coverageErr != nil {
		return nil, f.coverageErr
	}
	return f.coverage, nil
}

func (f *fakeWatches) Evidence(_ context.Context, _ security.Principal, _, environment string) (*releasewatch.ReleaseEvidence, error) {
	if err := f.evidenceErr[environment]; err != nil {
		return nil, err
	}
	return f.evidence[environment], nil
}

type fakeCanaries struct {
	runs []deployments.CanaryRun
	err  error
	env  string
	n    int
}

func (f *fakeCanaries) RecentCanaries(_ context.Context, _ security.Principal, _, environmentID string, limit int) ([]deployments.CanaryRun, error) {
	f.env, f.n = environmentID, limit
	if f.err != nil {
		return nil, f.err
	}
	return f.runs, nil
}

type fakeProjects struct {
	w       *world
	err     error
	envsErr error
}

func (f *fakeProjects) Get(context.Context, security.Principal, string) (*projects.Project, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.w.project, nil
}

func (f *fakeProjects) Environments(context.Context, security.Principal, string) ([]*projects.Environment, error) {
	if f.envsErr != nil {
		return nil, f.envsErr
	}
	return []*projects.Environment{f.w.staging, f.w.production}, nil
}

type fakeBudgets struct {
	state agentsettings.BudgetState
	err   error
}

func (f *fakeBudgets) Budget(context.Context, string) (agentsettings.BudgetState, error) {
	return f.state, f.err
}

func (f *fakeBudgets) Enabled(context.Context, string) (bool, error) { return true, nil }

// fakeAuthz holds deployment:approve for the users in approvers, and
// records every check.
type fakeAuthz struct {
	mu     sync.Mutex
	deny   map[string]bool // user IDs refused
	err    error
	checks []authzCheck
}

type authzCheck struct {
	p        security.Principal
	perm     security.Permission
	resource string
}

func (f *fakeAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, resourceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks = append(f.checks, authzCheck{p, perm, resourceID})
	if f.err != nil {
		return f.err
	}
	if f.deny[p.UserID] {
		return security.ErrForbidden
	}
	return nil
}

type fakeReports struct {
	open  []OpenReport
	err   error
	calls [][3]string
}

func (f *fakeReports) Open(_ context.Context, orgID, projectID, environmentID string) ([]OpenReport, error) {
	f.calls = append(f.calls, [3]string{orgID, projectID, environmentID})
	if f.err != nil {
		return nil, f.err
	}
	return f.open, nil
}

type diffCall struct{ org, repository, base, head string }

type fakeDiffs struct {
	mu     sync.Mutex
	answer *repositories.Comparison
	err    error
	calls  []diffCall
}

func (f *fakeDiffs) Compare(_ context.Context, orgID, repository, base, head string) (*repositories.Comparison, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, diffCall{orgID, repository, base, head})
	if f.err != nil {
		return nil, f.err
	}
	return f.answer, nil
}

// fake3c is 3c's report service: it records what it was asked.
type fake3c struct {
	reports []*agentreports.Report
	err     error
	calls   [][3]string
}

func (f *fake3c) Open(_ context.Context, orgID, projectID, environmentID string) ([]*agentreports.Report, error) {
	f.calls = append(f.calls, [3]string{orgID, projectID, environmentID})
	return f.reports, f.err
}
