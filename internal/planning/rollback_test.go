package planning

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// rollbackDeployment is one deployment of an environment as the release
// history sees it.
type rollbackDeployment struct {
	env string
	ReleaseRecord
}

// rollbackHistory is every deployment of a project, of any status.
type rollbackHistory struct{ deployments []rollbackDeployment }

func (h *rollbackHistory) Releases(_ context.Context, _, environmentID string, before time.Time, limit int) ([]ReleaseRecord, error) {
	var out []ReleaseRecord
	for _, d := range h.deployments {
		if d.env == environmentID && d.Status == "succeeded" && (before.IsZero() || d.FinishedAt.Before(before)) {
			out = append(out, d.ReleaseRecord)
		}
	}
	slices.SortFunc(out, func(a, b ReleaseRecord) int { return b.FinishedAt.Compare(a.FinishedAt) })
	return out[:min(limit, len(out))], nil
}

func (h *rollbackHistory) Latest(_ context.Context, _, environmentID string) (*ReleaseRecord, error) {
	var newest *ReleaseRecord
	for i, d := range h.deployments {
		if d.env == environmentID && (newest == nil || d.StartedAt.After(newest.StartedAt)) {
			newest = &h.deployments[i].ReleaseRecord
		}
	}
	if newest == nil {
		return nil, ErrNotFound
	}
	r := *newest
	return &r, nil
}

func (h *rollbackHistory) Deployment(_ context.Context, _, deploymentID string) (*ReleaseRecord, error) {
	for _, d := range h.deployments {
		if d.DeploymentID == deploymentID {
			r := d.ReleaseRecord
			return &r, nil
		}
	}
	return nil, ErrNotFound
}

func (h *rollbackHistory) ReleasedImages(context.Context, string, string, time.Time) (map[string]ReleasedImage, error) {
	return map[string]ReleasedImage{}, nil
}

// rollbackStore keeps plans in memory. A plan's rollback counts once its
// deployment succeeded, as the history says.
type rollbackStore struct {
	Store
	plans   map[string]*Plan
	history *rollbackHistory
}

func (s *rollbackStore) GetPlan(_ context.Context, _, id string) (*Plan, error) {
	p, ok := s.plans[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clonePlan(p), nil
}

func (s *rollbackStore) RolledBackPlans(_ context.Context, _, environmentID string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, p := range s.plans {
		if p.Kind != KindRollback || p.EnvironmentID != environmentID || p.Rollback == nil {
			continue
		}
		for _, d := range s.history.deployments {
			if d.PlanID == p.ID && d.Status == "succeeded" {
				out[p.Rollback.TargetPlanID] = true
			}
		}
	}
	return out, nil
}

// refuse denies one permission and allows the rest.
type refuse struct{ perm security.Permission }

func (r refuse) Require(_ context.Context, _ security.Principal, perm security.Permission, _ string) error {
	if perm == r.perm {
		return fmt.Errorf("%w (%s)", security.ErrForbidden, perm.Name())
	}
	return nil
}

type rollbackWorld struct {
	svc     *Service
	store   *rollbackStore
	history *rollbackHistory
}

// rollbackAt is a time of day on the day of the rollback tests.
func rollbackAt(hour, minute int) time.Time {
	return time.Date(2026, 10, 24, hour, minute, 0, 0, time.UTC)
}

// productionDeployment is a deployment of production that started five minutes
// before it finished, or at finished when it hasn't finished.
func productionDeployment(id, planID, kind, status string, finished time.Time) rollbackDeployment {
	r := ReleaseRecord{DeploymentID: id, PlanID: planID, Kind: kind, Status: status, StartedAt: finished.Add(-5 * time.Minute), FinishedAt: finished}
	if status == "running" || status == "paused" {
		r.StartedAt, r.FinishedAt = finished, time.Time{}
	}
	return rollbackDeployment{env: envProduction, ReleaseRecord: r}
}

// releasePlan is a release of production deploying api and web at the
// given commits.
func releasePlan(id, api, web string) *Plan {
	return &Plan{
		ID: id, OrgID: "org", ProjectID: "proj", EnvironmentID: envProduction, Environment: "production", Kind: KindRelease, Status: StatusApproved,
		Sources: []PlanSource{
			{SourceID: "src_api", Alias: "api", Repository: "acme/api", CommitSHA: api, InScope: true},
			{SourceID: "src_web", Alias: "web", Repository: "acme/web", CommitSHA: web, InScope: true},
		},
	}
}

// rollbackPlan rolls production back from target to restore, deploying
// restore's commits.
func rollbackPlan(id string, target, restore *Plan) *Plan {
	p := releasePlan(id, restore.Sources[0].CommitSHA, restore.Sources[1].CommitSHA)
	p.Kind = KindRollback
	p.Rollback = &Rollback{Reason: "errors", Trigger: TriggerManual, TargetPlanID: target.ID, RestorePlanID: restore.ID}
	return p
}

func newRollbackWorld(authz Authorizer, plans []*Plan, deployments ...rollbackDeployment) *rollbackWorld {
	w := &rollbackWorld{history: &rollbackHistory{deployments: deployments}}
	w.store = &rollbackStore{plans: map[string]*Plan{}, history: w.history}
	for _, p := range plans {
		w.store.plans[p.ID] = p
	}
	w.svc = NewService(Deps{
		Store: w.store, Projects: promoProjects{}, History: w.history,
		Providers: providers.Registry(), Authz: authz, Window: time.Hour,
	})
	return w
}

func (w *rollbackWorld) target(t *testing.T, deploymentID string) (*RollbackTarget, error) {
	t.Helper()
	return w.svc.RollbackTarget(t.Context(), security.Principal{UserID: "usr", OrgID: "org"}, "proj", " production ", deploymentID)
}

func ref(d rollbackDeployment) ReleaseRef {
	return ReleaseRef{DeploymentID: d.DeploymentID, PlanID: d.PlanID, Kind: d.Kind, Status: d.Status, FinishedAt: d.FinishedAt}
}

func TestRollbackTarget_restoresTheReleaseBeforeTheNewest(t *testing.T) {
	d1 := productionDeployment("d1", "plan_1", KindRelease, "succeeded", rollbackAt(10, 0))
	d3 := productionDeployment("d3", "plan_3", KindPromotion, "succeeded", rollbackAt(12, 0))
	w := newRollbackWorld(allowAll{}, []*Plan{releasePlan("plan_1", "aaa1", "www1"), releasePlan("plan_3", "aaa3", "www3")}, d3, d1)
	got, err := w.target(t, "")
	if err != nil {
		t.Fatal(err)
	}
	want := &RollbackTarget{
		Environment: "production", Target: ref(d3), Restore: ref(d1),
		Commits: []RollbackCommit{{Alias: "api", From: "aaa3", To: "aaa1"}, {Alias: "web", From: "www3", To: "www1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("target\n%+v\nwant\n%+v", got, want)
	}
}

func TestRollbackTarget_canTargetAFailedDeployment(t *testing.T) {
	d3 := productionDeployment("d3", "plan_3", KindRelease, "succeeded", rollbackAt(12, 0))
	d4 := productionDeployment("d4", "plan_4", KindRelease, "failed", rollbackAt(13, 0))
	w := newRollbackWorld(allowAll{}, []*Plan{releasePlan("plan_3", "aaa3", "www3"), releasePlan("plan_4", "aaa4", "www3")}, d3, d4)
	got, err := w.target(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != ref(d4) || got.Restore != ref(d3) {
		t.Fatalf("target %+v restore %+v", got.Target, got.Restore)
	}
	if want := []RollbackCommit{{Alias: "api", From: "aaa4", To: "aaa3"}, {Alias: "web", From: "www3", To: "www3"}}; !reflect.DeepEqual(got.Commits, want) {
		t.Fatalf("commits %+v", got.Commits)
	}
}

func TestRollbackTarget_refusesWhileADeploymentRuns(t *testing.T) {
	for _, status := range []string{"running", "paused"} {
		t.Run(status, func(t *testing.T) {
			d3 := productionDeployment("d3", "plan_3", KindRelease, "succeeded", rollbackAt(12, 0))
			d4 := productionDeployment("d4", "plan_4", KindRelease, status, rollbackAt(13, 0))
			w := newRollbackWorld(allowAll{}, []*Plan{releasePlan("plan_3", "aaa3", "www3"), releasePlan("plan_4", "aaa4", "www4")}, d3, d4)
			// Naming an earlier deployment doesn't get round it.
			for _, id := range []string{"", "d4", "d3"} {
				_, err := w.target(t, id)
				if !errors.Is(err, ErrReleaseRunning) || !strings.Contains(err.Error(), "wait for it to finish, or cancel it, before rolling back") {
					t.Fatalf("deployment %q: %v", id, err)
				}
			}
		})
	}
}

func TestRollbackTarget_skipsReleasesThatWereRolledBack(t *testing.T) {
	r1, r2, r3 := releasePlan("plan_1", "aaa1", "www1"), releasePlan("plan_2", "aaa2", "www2"), releasePlan("plan_3", "aaa3", "www3")
	r2r := rollbackPlan("plan_2r", r2, r1)
	// A rollback that failed, or of another plan, undoes nothing it names.
	failed := rollbackPlan("plan_fr", r1, r1)
	d1 := productionDeployment("d1", "plan_1", KindRelease, "succeeded", rollbackAt(10, 0))
	d2 := productionDeployment("d2", "plan_2", KindRelease, "succeeded", rollbackAt(11, 0))
	d2r := productionDeployment("d2r", "plan_2r", KindRollback, "succeeded", rollbackAt(11, 30))
	dfr := productionDeployment("dfr", "plan_fr", KindRollback, "failed", rollbackAt(11, 40))
	d3 := productionDeployment("d3", "plan_3", KindRelease, "succeeded", rollbackAt(12, 0))
	w := newRollbackWorld(allowAll{}, []*Plan{r1, r2, r2r, failed, r3}, d1, d2, d2r, dfr, d3)
	got, err := w.target(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != ref(d3) || got.Restore != ref(d2r) {
		t.Fatalf("target %+v restore %+v, want d3 and d2r", got.Target, got.Restore)
	}
	// d2r restored R1's commits.
	if want := []RollbackCommit{{Alias: "api", From: "aaa3", To: "aaa1"}, {Alias: "web", From: "www3", To: "www1"}}; !reflect.DeepEqual(got.Commits, want) {
		t.Fatalf("commits %+v", got.Commits)
	}
}

// A rollback is never rolled back: that would only bring the bad release
// back. Moving on is a deploy or a promotion, whether the rollback
// succeeded or not.
func TestRollbackTarget_refusesToRollBackARollback(t *testing.T) {
	r1, r2 := releasePlan("plan_1", "aaa1", "www1"), releasePlan("plan_2", "aaa2", "www2")
	r2r := rollbackPlan("plan_2r", r2, r1)
	d1 := productionDeployment("d1", "plan_1", KindRelease, "succeeded", rollbackAt(10, 0))
	d2 := productionDeployment("d2", "plan_2", KindRelease, "succeeded", rollbackAt(11, 0))
	for _, status := range []string{"succeeded", "failed"} {
		d2r := productionDeployment("d2r", "plan_2r", KindRollback, status, rollbackAt(11, 30))
		w := newRollbackWorld(allowAll{}, []*Plan{r1, r2, r2r}, d1, d2, d2r)
		for _, id := range []string{"", "d2r"} {
			_, err := w.target(t, id)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "the current release is a rollback; deploy or promote to move forward") {
				t.Fatalf("%s rollback, deployment %q: %v", status, id, err)
			}
		}
	}
}

func TestRollbackTarget_explicitDeployment(t *testing.T) {
	d1 := productionDeployment("d1", "plan_1", KindRelease, "succeeded", rollbackAt(10, 0))
	d3 := productionDeployment("d3", "plan_3", KindRelease, "succeeded", rollbackAt(12, 0))
	staging := rollbackDeployment{env: envStaging, ReleaseRecord: ReleaseRecord{DeploymentID: "d_s", PlanID: "plan_s", Kind: KindRelease, Status: "succeeded",
		StartedAt: rollbackAt(12, 30), FinishedAt: rollbackAt(12, 35)}}
	plans := []*Plan{releasePlan("plan_1", "aaa1", "www1"), releasePlan("plan_3", "aaa3", "www3")}

	w := newRollbackWorld(allowAll{}, plans, d1)
	if _, err := w.target(t, "d1"); !errors.Is(err, ErrNoPreviousRelease) {
		t.Fatalf("d1 with nothing before it: %v", err)
	}
	if _, err := newRollbackWorld(allowAll{}, plans).target(t, ""); !errors.Is(err, ErrNoPreviousRelease) {
		t.Fatalf("an environment nothing deployed: %v", err)
	}

	w = newRollbackWorld(allowAll{}, plans, d1, d3, staging)
	got, err := w.target(t, " d3 ")
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != ref(d3) || got.Restore != ref(d1) {
		t.Fatalf("target %+v restore %+v", got.Target, got.Restore)
	}
	for id, want := range map[string]string{
		"d_s": "roll back the newest release first",
		"d1":  "roll back the newest release first",
	} {
		_, err = w.target(t, id)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Fatalf("deployment %s: %v", id, err)
		}
	}
}

func TestRollbackTarget_needsDeploymentRead(t *testing.T) {
	d1 := productionDeployment("d1", "plan_1", KindRelease, "succeeded", rollbackAt(10, 0))
	d3 := productionDeployment("d3", "plan_3", KindRelease, "succeeded", rollbackAt(12, 0))
	w := newRollbackWorld(refuse{security.DeploymentRead}, []*Plan{releasePlan("plan_1", "aaa1", "www1"), releasePlan("plan_3", "aaa3", "www3")}, d1, d3)
	if _, err := w.target(t, ""); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("without deployment:read: %v", err)
	}
}

func TestRollbackTarget_refusesWhatTheCanaryAlreadyReverted(t *testing.T) {
	for status, want := range map[string]string{
		"rolled_back":    "the canary already returned production to the baseline; there is nothing to roll back",
		"revert_failed":  "the canary's revert failed; fix it from the deployment before rolling back",
		"promote_failed": "the canary's promotion failed and traffic may still be split; fix it from the deployment before rolling back",
	} {
		t.Run(status, func(t *testing.T) {
			d3 := productionDeployment("d3", "plan_3", KindRelease, "succeeded", rollbackAt(12, 0))
			d4 := productionDeployment("d4", "plan_4", KindRelease, "failed", rollbackAt(13, 0))
			d4.CanaryStatus = status
			w := newRollbackWorld(allowAll{}, []*Plan{releasePlan("plan_3", "aaa3", "www3"), releasePlan("plan_4", "aaa4", "www4")}, d3, d4)
			for _, id := range []string{"", "d4"} {
				_, err := w.target(t, id)
				if !errors.Is(err, ErrCanaryReverted) || err.Error() != want {
					t.Fatalf("deployment %q: %v", id, err)
				}
			}
		})
	}

	// A canary that promoted released like any deployment.
	d1 := productionDeployment("d1", "plan_1", KindRelease, "succeeded", rollbackAt(10, 0))
	d3 := productionDeployment("d3", "plan_3", KindRelease, "succeeded", rollbackAt(12, 0))
	d3.CanaryStatus = "promoted"
	w := newRollbackWorld(allowAll{}, []*Plan{releasePlan("plan_1", "aaa1", "www1"), releasePlan("plan_3", "aaa3", "www3")}, d1, d3)
	if got, err := w.target(t, ""); err != nil || got.Target.DeploymentID != "d3" || got.Restore.DeploymentID != "d1" {
		t.Fatalf("after a promoted canary: %+v %v", got, err)
	}
}

// createStore is production's plans in memory, with the principal each
// decision was settled as.
type createStore struct {
	*promoStore
	history *rollbackHistory
	actors  []security.Principal
}

func (s *createStore) RolledBackPlans(ctx context.Context, orgID, environmentID string) (map[string]bool, error) {
	return (&rollbackStore{plans: s.plans, history: s.history}).RolledBackPlans(ctx, orgID, environmentID)
}

func (s *createStore) Decide(ctx context.Context, _, id, status, by, note string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.plans[id]
	if p.Status != StatusReady {
		return false, nil
	}
	p.Status, p.DecidedBy, p.DecisionNote = status, by, note
	actor, _ := security.PrincipalFrom(ctx)
	s.actors = append(s.actors, actor)
	return true, nil
}

// recordingWorkflow records what it's asked and plans nothing itself.
// With apply, a decision settles the plan the way the workflow would.
type recordingWorkflow struct {
	svc       *Service
	apply     bool
	started   []string
	decisions []orchestration.Decision
}

func (w *recordingWorkflow) Start(_ context.Context, orgID, planID string, window time.Duration) error {
	w.started = append(w.started, orgID+"/"+planID+"/"+window.String())
	return nil
}

func (w *recordingWorkflow) Decide(ctx context.Context, planID, _ string, d orchestration.Decision) error {
	w.decisions = append(w.decisions, d)
	if w.apply {
		_, err := w.svc.ApplyDecision(ctx, "org", planID, d)
		return err
	}
	return nil
}

func (*recordingWorkflow) Cancel(context.Context, string, string) error { return nil }

// recordingAuthz records each check, as "user permission resource", and
// denies one permission.
type recordingAuthz struct {
	deny  security.Permission
	asked []string
	as    []security.Principal
}

func (a *recordingAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, resource string) error {
	a.asked = append(a.asked, p.UserID+" "+perm.Name()+" "+resource)
	a.as = append(a.as, p)
	if perm == a.deny {
		return fmt.Errorf("%w (%s)", security.ErrForbidden, perm.Name())
	}
	return nil
}

type createWorld struct {
	svc      *Service
	store    *createStore
	history  *rollbackHistory
	workflow *recordingWorkflow
	audit    *promoAudit
}

var person = security.Principal{UserID: "usr", OrgID: "org"}

// restorePlan is d1's plan: api at aaa1, web held at www1, made from
// manifest revision 5.
func restorePlan() *Plan {
	p := releasePlan("plan_1", "aaa1", "www1")
	p.Sources[0].AnalysisID, p.Sources[1].AnalysisID = "anl_a1", "anl_w1"
	p.Sources[1].InScope = false
	p.ManifestID, p.ManifestRevision = "man_5", 5
	return p
}

// newCreateWorld is production after d1 (plan_1) and d3 (plan_3).
func newCreateWorld(t *testing.T, authz Authorizer, deployments ...rollbackDeployment) *createWorld {
	t.Helper()
	if len(deployments) == 0 {
		deployments = []rollbackDeployment{
			productionDeployment("d1", "plan_1", KindRelease, "succeeded", rollbackAt(10, 0)),
			productionDeployment("d3", "plan_3", KindRelease, "succeeded", rollbackAt(12, 0)),
		}
	}
	target := releasePlan("plan_3", "aaa3", "www3")
	target.ManifestID, target.ManifestRevision = "man_7", 7
	revisions := map[string]*manifests.Revision{}
	for n, id := range map[int]string{5: "man_5", 7: "man_7"} {
		revisions[id] = &manifests.Revision{ID: id, Revision: n, Manifest: parse(t, "version: 1\n"), Digest: "digest-" + id}
	}
	w := &createWorld{history: &rollbackHistory{deployments: deployments}, workflow: &recordingWorkflow{}, audit: &promoAudit{}}
	w.store = &createStore{
		promoStore: &promoStore{plans: map[string]*Plan{"plan_1": restorePlan(), "plan_3": target}},
		history:    w.history,
	}
	w.svc = NewService(Deps{
		Store: w.store, Projects: promoProjects{}, Analyses: &promoAnalyses{}, Manifests: &promoManifests{byID: revisions, latest: "man_7"},
		History: w.history, Providers: providers.Registry(), Authz: authz, Workflow: w.workflow, Audit: w.audit, Window: time.Hour,
	})
	w.svc.sleep = func(context.Context, time.Duration) error { return nil }
	w.workflow.svc = w.svc
	return w
}

// ready plans a stored plan, as the workflow would.
func (w *createWorld) ready(t *testing.T, id string) *Plan {
	t.Helper()
	status, err := w.svc.Plan(t.Context(), "org", id)
	if err != nil || status != StatusReady {
		t.Fatalf("planning %s: %s, %v", id, status, err)
	}
	p, _ := w.store.GetPlan(t.Context(), "org", id)
	return p
}

func lastAudit(t *testing.T, a *promoAudit) projects.AuditEvent {
	t.Helper()
	if len(a.events) == 0 {
		t.Fatal("nothing audited")
	}
	return a.events[len(a.events)-1]
}

func TestCreateRollback_isANormalPlanPinnedToThePreviousRelease(t *testing.T) {
	w := newCreateWorld(t, allowAll{})
	plan, err := w.svc.CreateRollback(t.Context(), person, "proj", RollbackInput{Environment: " production ", Reason: " 5xx after deploy "})
	if err != nil {
		t.Fatal(err)
	}
	want := restorePlan().Sources
	want[1].InScope = true
	if plan.Kind != KindRollback || plan.Status != StatusPlanning || plan.CreatedBy != "usr" || plan.EnvironmentID != envProduction ||
		!reflect.DeepEqual(plan.Sources, want) || plan.ManifestID != "man_5" || plan.ManifestRevision != 5 || plan.From != "" {
		t.Fatalf("plan %+v", plan)
	}
	rb := &Rollback{
		Reason: "5xx after deploy", Trigger: TriggerManual, TargetDeploymentID: "d3", TargetPlanID: "plan_3",
		RestoreDeploymentID: "d1", RestorePlanID: "plan_1", RestoreFinishedAt: rollbackAt(10, 0),
	}
	if !reflect.DeepEqual(plan.Rollback, rb) {
		t.Fatalf("rollback %+v, want %+v", plan.Rollback, rb)
	}
	if stored, _ := w.store.GetPlan(t.Context(), "org", plan.ID); !reflect.DeepEqual(stored.Rollback, rb) || stored.Kind != KindRollback {
		t.Fatalf("stored %+v", stored)
	}
	if !slices.Equal(w.workflow.started, []string{"org/" + plan.ID + "/1h0m0s"}) {
		t.Fatalf("workflow %v", w.workflow.started)
	}
	e := lastAudit(t, w.audit)
	if e.Action != "plan.rollback.create" || e.Principal != person || e.Attributes["plan_id"] != plan.ID || e.Attributes["reason"] != "5xx after deploy" ||
		e.Attributes["trigger"] != TriggerManual || e.Attributes["automatic"] != false ||
		e.Attributes["target_deployment_id"] != "d3" || e.Attributes["restore_deployment_id"] != "d1" {
		t.Fatalf("audit %+v", e)
	}

	// It plans like any plan, and waits for a person.
	if planned := w.ready(t, plan.ID); planned.Rollback == nil || planned.Leftovers == nil || planned.Artifacts == nil {
		t.Fatalf("planned %+v", planned)
	}
}

func TestCreateRollback_needsAReasonAndDeploymentCreate(t *testing.T) {
	for name, reason := range map[string]string{"empty": " ", "too long": strings.Repeat("x", 2001)} {
		w := newCreateWorld(t, allowAll{})
		_, err := w.svc.CreateRollback(t.Context(), person, "proj", RollbackInput{Environment: "production", Reason: reason})
		if !errors.Is(err, ErrInvalid) || len(w.workflow.started) != 0 {
			t.Fatalf("%s reason: %v, started %v", name, err, w.workflow.started)
		}
	}
	w := newCreateWorld(t, refuse{security.DeploymentCreate})
	if _, err := w.svc.CreateRollback(t.Context(), person, "proj", RollbackInput{Environment: "production", Reason: "errors"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("without deployment:create: %v", err)
	}

	// What RollbackTarget refuses, CreateRollback refuses.
	w = newCreateWorld(t, allowAll{},
		productionDeployment("d1", "plan_1", KindRelease, "succeeded", rollbackAt(10, 0)),
		productionDeployment("d3", "plan_3", KindRelease, "running", rollbackAt(12, 0)))
	if _, err := w.svc.CreateRollback(t.Context(), person, "proj", RollbackInput{Environment: "production", Reason: "errors"}); !errors.Is(err, ErrReleaseRunning) {
		t.Fatalf("while d3 runs: %v", err)
	}
	if len(w.workflow.started) != 0 || len(w.store.plans) != 2 {
		t.Fatalf("started %v, plans %d", w.workflow.started, len(w.store.plans))
	}
}

func TestCreateAutomaticRollback_actsAsTheAutomation(t *testing.T) {
	authz := &recordingAuthz{}
	w := newCreateWorld(t, authz)
	in := AutomaticRollbackInput{Trigger: TriggerWatch, Reason: "4.1% of the candidate's requests failed; the ceiling is 1%.", WatchID: "rw_1"}
	plan, err := w.svc.CreateAutomaticRollback(t.Context(), "org", "d3", in)
	if err != nil {
		t.Fatal(err)
	}
	system := security.SystemPrincipal("org")
	if plan.CreatedBy != security.SystemSubject("org") || plan.Kind != KindRollback || plan.Rollback.Trigger != TriggerWatch ||
		plan.Rollback.WatchID != "rw_1" || plan.Rollback.Reason != in.Reason || plan.Rollback.TargetDeploymentID != "d3" || plan.Rollback.RestoreDeploymentID != "d1" {
		t.Fatalf("plan %+v rollback %+v", plan, plan.Rollback)
	}
	if !slices.Equal(authz.asked, []string{system.UserID + " " + security.DeploymentCreate.Name() + " proj"}) || authz.as[0] != system {
		t.Fatalf("checked %v as %+v", authz.asked, authz.as)
	}
	e := lastAudit(t, w.audit)
	if e.Action != "plan.rollback.create" || e.Principal != system || e.Attributes["automatic"] != true || e.Attributes["trigger"] != TriggerWatch {
		t.Fatalf("audit %+v", e)
	}

	for _, trigger := range []string{TriggerManual, "", "cron"} {
		w = newCreateWorld(t, allowAll{})
		if _, err = w.svc.CreateAutomaticRollback(t.Context(), "org", "d3", AutomaticRollbackInput{Trigger: trigger, Reason: "x"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("trigger %q: %v", trigger, err)
		}
	}
	w = newCreateWorld(t, allowAll{})
	if _, err = w.svc.CreateAutomaticRollback(t.Context(), "org", "d3", AutomaticRollbackInput{Trigger: TriggerCanary}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no reason: %v", err)
	}
	if _, err = w.svc.CreateAutomaticRollback(t.Context(), "org", "d1", AutomaticRollbackInput{Trigger: TriggerCanary, Reason: "x"}); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "roll back the newest release first") {
		t.Fatalf("an older deployment: %v", err)
	}
	if _, err = w.svc.CreateAutomaticRollback(t.Context(), "org", "d_gone", AutomaticRollbackInput{Trigger: TriggerCanary, Reason: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown deployment: %v", err)
	}
	w = newCreateWorld(t, refuse{security.DeploymentCreate})
	if _, err = w.svc.CreateAutomaticRollback(t.Context(), "org", "d3", in); !errors.Is(err, security.ErrForbidden) || len(w.workflow.started) != 0 {
		t.Fatalf("automation without deployment:create: %v", err)
	}

	// A canary that already reverted the release, and a rollback, are
	// never rolled back by a watch.
	d3 := productionDeployment("d3", "plan_3", KindRelease, "failed", rollbackAt(12, 0))
	d3.CanaryStatus = "rolled_back"
	w = newCreateWorld(t, allowAll{}, productionDeployment("d1", "plan_1", KindRelease, "succeeded", rollbackAt(10, 0)), d3)
	if _, err = w.svc.CreateAutomaticRollback(t.Context(), "org", "d3", in); !errors.Is(err, ErrCanaryReverted) {
		t.Fatalf("after the canary reverted: %v", err)
	}
	w = newCreateWorld(t, allowAll{},
		productionDeployment("d1", "plan_1", KindRelease, "succeeded", rollbackAt(10, 0)),
		productionDeployment("d3", "plan_3", KindRollback, "succeeded", rollbackAt(12, 0)))
	if _, err = w.svc.CreateAutomaticRollback(t.Context(), "org", "d3", in); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "the current release is a rollback") {
		t.Fatalf("a rollback: %v", err)
	}
}

func TestApproveAutomatic(t *testing.T) {
	ctx := t.Context()
	system := security.SystemPrincipal("org")
	in := AutomaticRollbackInput{Trigger: TriggerWatch, Reason: "4.1% of the candidate's requests failed; the ceiling is 1%.", WatchID: "rw_1"}
	authz := &recordingAuthz{}
	w := newCreateWorld(t, authz)
	w.workflow.apply = true
	created, err := w.svc.CreateAutomaticRollback(ctx, "org", "d3", in)
	if err != nil {
		t.Fatal(err)
	}
	ready := w.ready(t, created.ID)
	authz.asked = nil
	got, err := w.svc.ApproveAutomatic(ctx, "org", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := orchestration.Decision{Actor: &system, Approve: true, Revision: ready.Revision, By: system.UserID,
		Note: "Automatic rollback after a post-deploy watch breach: " + in.Reason}
	if len(w.workflow.decisions) != 1 || !reflect.DeepEqual(w.workflow.decisions[0], want) {
		t.Fatalf("decisions %+v, want %+v", w.workflow.decisions, want)
	}
	if got.Status != StatusApproved || got.DecidedBy != system.UserID {
		t.Fatalf("settled %s by %s", got.Status, got.DecidedBy)
	}
	if len(w.store.actors) != 1 || w.store.actors[0] != system {
		t.Fatalf("settled as %+v", w.store.actors)
	}
	if !slices.Contains(authz.asked, system.UserID+" "+security.DeploymentApprove.Name()+" proj") {
		t.Fatalf("checked %v", authz.asked)
	}
	e := lastAudit(t, w.audit)
	if e.Action != "plan.approve" || e.Principal != system || e.Attributes["automatic"] != true || e.Attributes["trigger"] != TriggerWatch || e.Attributes["plan_id"] != created.ID {
		t.Fatalf("audit %+v", e)
	}

	// A canary's rollback says so.
	w = newCreateWorld(t, allowAll{})
	created, err = w.svc.CreateAutomaticRollback(ctx, "org", "d3", AutomaticRollbackInput{Trigger: TriggerCanary, Reason: "the canary failed"})
	if err != nil {
		t.Fatal(err)
	}
	w.ready(t, created.ID)
	if _, err = w.svc.ApproveAutomatic(ctx, "org", created.ID); err != nil || !strings.HasPrefix(w.workflow.decisions[0].Note, "Automatic rollback after a canary breach: ") {
		t.Fatalf("canary: %v %+v", err, w.workflow.decisions)
	}

	// A person's rollback, a release and a promotion wait for a person.
	w = newCreateWorld(t, allowAll{})
	manual, err := w.svc.CreateRollback(ctx, person, "proj", RollbackInput{Environment: "production", Reason: "errors"})
	if err != nil {
		t.Fatal(err)
	}
	w.ready(t, manual.ID)
	promotion := releasePlan("plan_p", "aaa3", "www3")
	promotion.Kind, promotion.Status = KindPromotion, StatusReady
	release := releasePlan("plan_r", "aaa3", "www3")
	release.Status = StatusReady
	w.store.plans["plan_p"], w.store.plans["plan_r"] = promotion, release
	for _, id := range []string{manual.ID, "plan_p", "plan_r"} {
		if _, err = w.svc.ApproveAutomatic(ctx, "org", id); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if len(w.workflow.decisions) != 0 {
		t.Fatalf("decided %+v", w.workflow.decisions)
	}

	// Without deployment:approve, the automation decides nothing.
	w = newCreateWorld(t, refuse{security.DeploymentApprove})
	created, err = w.svc.CreateAutomaticRollback(ctx, "org", "d3", in)
	if err != nil {
		t.Fatal(err)
	}
	w.ready(t, created.ID)
	if _, err = w.svc.ApproveAutomatic(ctx, "org", created.ID); !errors.Is(err, security.ErrForbidden) || len(w.workflow.decisions) != 0 {
		t.Fatalf("denied: %v, decisions %+v", err, w.workflow.decisions)
	}

	// Still planning: nothing to approve.
	w = newCreateWorld(t, allowAll{})
	created, err = w.svc.CreateAutomaticRollback(ctx, "org", "d3", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.svc.ApproveAutomatic(ctx, "org", created.ID); !errors.Is(err, ErrNotReady) || len(w.workflow.decisions) != 0 {
		t.Fatalf("planning: %v", err)
	}
}

func TestApplyDecision_acceptsTheAutomaticActor(t *testing.T) {
	ctx := t.Context()
	system := security.SystemPrincipal("org")
	w := newCreateWorld(t, allowAll{})
	created, err := w.svc.CreateAutomaticRollback(ctx, "org", "d3", AutomaticRollbackInput{Trigger: TriggerWatch, Reason: "errors"})
	if err != nil {
		t.Fatal(err)
	}
	ready := w.ready(t, created.ID)
	d := orchestration.Decision{Actor: &system, Approve: true, Revision: ready.Revision, By: system.UserID, Note: "automatic"}
	status, err := w.svc.ApplyDecision(ctx, "org", created.ID, d)
	if err != nil || status != StatusApproved {
		t.Fatalf("automatic rollback: %s, %v", status, err)
	}
	if len(w.store.actors) != 1 || w.store.actors[0] != system {
		t.Fatalf("settled as %+v", w.store.actors)
	}

	// The automation decides nothing else, approving or rejecting, with or
	// without an actor.
	manual, err := w.svc.CreateRollback(ctx, person, "proj", RollbackInput{Environment: "production", Reason: "errors"})
	if err != nil {
		t.Fatal(err)
	}
	manualReady := w.ready(t, manual.ID)
	release := releasePlan("plan_r", "aaa3", "www3")
	release.Status, release.Revision = StatusReady, "rev"
	w.store.plans["plan_r"] = release
	for id, revision := range map[string]string{manual.ID: manualReady.Revision, "plan_r": "rev"} {
		for _, dec := range []orchestration.Decision{
			{Actor: &system, Approve: true, Revision: revision, By: system.UserID},
			{Actor: &system, Revision: revision, By: system.UserID},
			{Approve: true, Revision: revision, By: system.UserID},
		} {
			if _, err = w.svc.ApplyDecision(ctx, "org", id, dec); !errors.Is(err, security.ErrForbidden) {
				t.Fatalf("%s %+v: %v", id, dec, err)
			}
		}
	}
	if len(w.store.actors) != 1 {
		t.Fatalf("settled %+v", w.store.actors)
	}
}

// filtered is a rollback's result for actions, with its findings.
func filtered(actions []Action, bindings []*Binding, problems, warnings []Finding) *Result {
	r := &Result{Actions: actions, Problems: problems, Warnings: warnings, Connections: map[string]string{}}
	if r.Problems == nil {
		r.Problems = []Finding{}
	}
	if r.Warnings == nil {
		r.Warnings = []Finding{}
	}
	r.recount()
	r.Executable = len(r.Problems) == 0
	r.Leftovers = rollbackFilter(r, bindings)
	return r
}

func resourceAction(name, kind string, op Op) Action {
	a := Action{ID: actionID(ResourceSubject, name), Op: op, Subject: ResourceSubject, Name: name, Provider: "cloud-sql"}
	if op != Delete {
		a.Resource = &ResourceConfig{Provider: "cloud-sql", Kind: kind}
	}
	return a
}

func actionOps(r *Result) map[string]Op {
	out := map[string]Op{}
	for _, a := range r.Actions {
		out[a.ID] = a.Op
	}
	return out
}

func TestRollback_leavesDataBearingResourcesAndListsThem(t *testing.T) {
	db := resourceAction("db", "postgres", Update)
	db.Resource.Version = "16"
	db.Changes = []Change{{Field: "version", From: "17", To: "16"}}
	cache := resourceAction("cache", "redis", Delete)
	r := filtered([]Action{cache, db}, []*Binding{{Subject: ResourceSubject, Name: "db"}, {Subject: ResourceSubject, Name: "cache"}}, nil, nil)
	want := []Leftover{
		{Subject: ResourceSubject, Name: "cache", Op: Delete, DataBearing: true,
			Reason: "Created by the release you're rolling back. Rollback never removes a data-bearing resource; delete it yourself if you don't need it."},
		{Subject: ResourceSubject, Name: "db", Op: Update, DataBearing: true,
			Reason: "Changed by the release you're rolling back. Rollback never reverts a data-bearing resource."},
	}
	if !reflect.DeepEqual(r.Leftovers, want) {
		t.Fatalf("leftovers %+v", r.Leftovers)
	}
	if got := actionOps(r); got["resource:cache"] != Keep || got["resource:db"] != Keep || r.Summary != (Summary{Keep: 2}) {
		t.Fatalf("ops %v summary %+v", got, r.Summary)
	}
	if action(t, r, "resource:db").Changes != nil {
		t.Fatal("a kept resource lists changes")
	}

	// Through the planner: the bad release added cache, which the release
	// it restores doesn't have.
	restored := strings.Replace(strings.Replace(acme, "    needs: [cache, api]\n", "    needs: [api]\n", 1), "  cache:\n    kind: redis\n    provider: memorystore\n", "", 1)
	r = compute(t, restored, "production", deployed(t, compute(t, acme, "production"))...)
	if action(t, r, "resource:cache").Op != Delete {
		t.Fatalf("cache %s", action(t, r, "resource:cache").Op)
	}
	r.Leftovers = rollbackFilter(r, deployed(t, compute(t, acme, "production")))
	if action(t, r, "resource:cache").Op != Keep || r.Summary.Delete != 0 || len(r.Leftovers) != 1 || r.Leftovers[0].Name != "cache" || !r.Leftovers[0].DataBearing {
		t.Fatalf("cache %s, summary %+v, leftovers %+v", action(t, r, "resource:cache").Op, r.Summary, r.Leftovers)
	}
}

func TestRollback_revertsStatelessServices(t *testing.T) {
	api := serviceAction("api", "cloud-run", Update, ServiceConfig{Port: 8080, Public: true, CommitSHA: "aaa1"})
	api.Changes = []Change{{Field: "commit", From: "aaa3", To: "aaa1"}}
	web := serviceAction("web", "render", Replace, ServiceConfig{Public: true, CommitSHA: "www1"})
	web.Changes = []Change{{Field: "provider", From: "cloud-run", To: "render"}}
	keep := serviceAction("docs", "vercel", Keep, ServiceConfig{})
	r := filtered([]Action{api, web, keep}, []*Binding{{Subject: ServiceSubject, Name: "api"}, {Subject: ServiceSubject, Name: "web"}, {Subject: ServiceSubject, Name: "docs"}}, nil, nil)
	if got := action(t, r, "service:api"); got.Op != Update || got.Service.CommitSHA != "aaa1" || len(got.Changes) != 1 {
		t.Fatalf("api %+v", got)
	}
	if got := action(t, r, "service:web"); got.Op != Replace || got.Provider != "render" {
		t.Fatalf("web %+v", got)
	}
	if len(r.Leftovers) != 0 || r.Summary != (Summary{Update: 1, Replace: 1, Keep: 1}) || !r.Executable {
		t.Fatalf("leftovers %+v summary %+v", r.Leftovers, r.Summary)
	}
}

func TestRollback_neverCreatesOrDeletes(t *testing.T) {
	worker := Action{ID: "service:worker", Op: Delete, Subject: ServiceSubject, Name: "worker", Provider: "cloud-run"}
	cron := serviceAction("cron", "cloud-run", Create, ServiceConfig{Workload: provider.Worker})
	component := resourceAction("auth--web", "", Create)
	component.Resource.Component = &ComponentConfig{Protection: recipes.ProtectionNone}
	r := filtered([]Action{worker, cron, component}, []*Binding{{Subject: ServiceSubject, Name: "worker"}}, nil, nil)
	want := []Leftover{
		{Subject: ServiceSubject, Name: "worker", Op: Delete,
			Reason: "Created by the release you're rolling back. Rollback never removes anything; delete it yourself if you don't need it."},
		{Subject: ServiceSubject, Name: "cron", Op: Create,
			Reason: "Removed by the release you're rolling back. Rollback never recreates anything; deploy it again if you need it."},
		{Subject: ResourceSubject, Name: "auth--web", Op: Create,
			Reason: "Removed by the release you're rolling back. Rollback never recreates anything; deploy it again if you need it."},
	}
	if !reflect.DeepEqual(r.Leftovers, want) {
		t.Fatalf("leftovers %+v", r.Leftovers)
	}
	for _, a := range r.Actions {
		if a.Op == Create || a.Op == Delete {
			t.Fatalf("%s %s", a.Op, a.ID)
		}
	}
}

func TestRollback_leavesDataBearingServicesAlone(t *testing.T) {
	queue := serviceAction("queue", "server", Update, ServiceConfig{Catalog: &recipes.Recipe{}})
	queue.Changes = []Change{{Field: "catalog", From: "a", To: "b"}}
	stateful := serviceAction("auth", "server", Replace, ServiceConfig{Component: &ComponentConfig{Protection: recipes.ProtectionNative}})
	r := filtered([]Action{queue, stateful}, []*Binding{{Subject: ServiceSubject, Name: "queue"}, {Subject: ServiceSubject, Name: "auth"}}, nil, nil)
	want := []Leftover{
		{Subject: ServiceSubject, Name: "queue", Op: Update, DataBearing: true,
			Reason: "Changed by the release you're rolling back. Rollback never reverts a data-bearing service."},
		{Subject: ServiceSubject, Name: "auth", Op: Replace, DataBearing: true,
			Reason: "Replaced by the release you're rolling back. Rollback never moves a data-bearing service back."},
	}
	if !reflect.DeepEqual(r.Leftovers, want) || r.Summary != (Summary{Keep: 2}) {
		t.Fatalf("leftovers %+v summary %+v", r.Leftovers, r.Summary)
	}
}

// A leftover is a plain Keep: nothing of what the action would have done
// reaches the runner, the route cleanup, capacity or backups.
func TestRollback_leftoversArePlainKeeps(t *testing.T) {
	db := resourceAction("db", "postgres", Create)
	db.BackupEveryHours = 24
	db.Compensation = &CloudCompensation{}
	db.Changes = []Change{{Field: "kind"}}
	queue := serviceAction("queue", "server", Update, ServiceConfig{Catalog: &recipes.Recipe{},
		CapacityIntent: &CapacityIntent{}, Artifact: &ReusedArtifact{Revision: "r"}})
	queue.Routes = []cloudinfra.Route{{ID: "route_1"}}
	r := filtered([]Action{db, queue}, nil, nil, nil)
	for _, a := range r.Actions {
		if a.Op != Keep || a.Changes != nil || a.Routes != nil || a.BackupEveryHours != 0 || a.Compensation != nil {
			t.Fatalf("%s: %+v", a.ID, a)
		}
	}
	if cfg := action(t, r, "service:queue").Service; cfg.CapacityIntent != nil || cfg.Artifact != nil {
		t.Fatalf("queue config %+v", cfg)
	}
	if queue.Service.CapacityIntent == nil {
		t.Fatal("the filter changed the computed action's configuration in place")
	}
}

func TestRollback_dropsFindingsAboutLeftovers(t *testing.T) {
	db := resourceAction("db", "postgres", Update)
	api := serviceAction("api", "cloud-run", Update, ServiceConfig{})
	r := filtered([]Action{db, api}, []*Binding{{Subject: ResourceSubject, Name: "db"}, {Subject: ServiceSubject, Name: "api"}},
		[]Finding{
			{Code: "connection-moved", Subject: ResourceSubject, Name: "db"},
			{Code: "route-release", Subject: ServiceSubject, Name: "api"},
		},
		[]Finding{{Code: "data-loss", Subject: ResourceSubject, Name: "db"}, {Code: "drift", Subject: ServiceSubject, Name: "db"}})
	if got := codes(r.Problems); !slices.Equal(got, []string{"route-release:api"}) || r.Executable {
		t.Fatalf("problems %v, executable %v", got, r.Executable)
	}
	// A service called db isn't the resource db.
	if got := codes(r.Warnings); !slices.Equal(got, []string{"drift:db"}) {
		t.Fatalf("warnings %v", got)
	}

	// With the finding about db gone, nothing blocks the plan.
	r = filtered([]Action{db, api}, []*Binding{{Subject: ResourceSubject, Name: "db"}},
		[]Finding{{Code: "connection-moved", Subject: ResourceSubject, Name: "db"}}, nil)
	if len(r.Problems) != 0 || !r.Executable {
		t.Fatalf("problems %v, executable %v", codes(r.Problems), r.Executable)
	}
}

func TestRollback_blocksWhenAServiceNeedsARemovedResource(t *testing.T) {
	api := serviceAction("api", "cloud-run", Update, ServiceConfig{Env: []EnvLink{
		{Key: "QUEUE_URL", From: "queue", Subject: ResourceSubject},
		{Key: "WEB_URL", From: "web", Subject: ServiceSubject},
	}})
	queue := resourceAction("queue", "redis", Create)
	r := filtered([]Action{queue, api}, []*Binding{{Subject: ServiceSubject, Name: "api"}}, nil, nil)
	if len(r.Problems) != 1 || r.Executable {
		t.Fatalf("problems %+v", r.Problems)
	}
	f := r.Problems[0]
	if f.Code != "rollback-needs-resource" || f.Subject != ServiceSubject || f.Name != "api" || f.Path != "services.api" ||
		f.Message != "api needs queue, which the release you're rolling back removed. Rollback never recreates a resource, so it can't restore api; release a version that creates queue instead." {
		t.Fatalf("problem %+v", f)
	}

	// While queue still runs, api is restored.
	r = filtered([]Action{queue, api}, []*Binding{{Subject: ServiceSubject, Name: "api"}, {Subject: ResourceSubject, Name: "queue"}}, nil, nil)
	if len(r.Problems) != 0 || !r.Executable {
		t.Fatalf("problems %+v", r.Problems)
	}
}

// rollbackComputeWorld plans api on Cloud Run in us-central1 through
// conn_g against doc, with api already running from an older commit.
func rollbackComputeWorld(t *testing.T, doc string, history ReleaseHistory, public bool) (*Service, *Plan) {
	t.Helper()
	revision := &manifests.Revision{ID: "man_1", Revision: 1, Manifest: parse(t, doc), Digest: "digest"}
	options := targetOptions{connections: []*connections.Connection{{ID: "conn_g", Cloud: connections.GCP, Status: connections.StatusReady, Details: map[string]string{"project_id": "shop"}}}}
	s := NewService(Deps{
		Store: artifactStore{}, Analyses: targetAnalyses{}, Manifests: artifactManifests{revision: revision},
		Providers: providers.Registry(), Connections: options, History: history,
	})
	plan := &Plan{
		OrgID: "org", ProjectID: "project", EnvironmentID: envProduction, Environment: "production", ManifestID: "man_1",
		Kind: KindRollback, Sources: []PlanSource{{Alias: "app", AnalysisID: "analysis", CommitSHA: artifactCommit, InScope: true}},
		Rollback: &Rollback{Reason: "errors", Trigger: TriggerManual, TargetDeploymentID: "d3", TargetPlanID: "plan_3",
			RestoreDeploymentID: "d1", RestorePlanID: "plan_1", RestoreFinishedAt: rollbackAt(10, 0)},
	}
	first, _, err := s.compute(t.Context(), &Plan{OrgID: "org", ProjectID: "project", EnvironmentID: envProduction, Environment: "production", ManifestID: "man_1", Sources: plan.Sources})
	if err != nil {
		t.Fatal(err)
	}
	api := action(t, first, "service:api")
	cfg := *api.Service
	cfg.CommitSHA, cfg.Public = "ccc1234567890123456789012345678901234567", public
	api.Service = &cfg
	b := bound(t, api)
	b.ConnectionID = "conn_g"
	s.store = artifactStore{catalogStore{bindings: []*Binding{b}}}
	return s, plan
}

const canaryDoc = "version: 1\nservices:\n  api: {source: cmd/api, provider: cloud-run, region: us-central1, public: true, port: 8080}\nenvironments:\n  production:\n    release:\n      strategy: canary\n"

func TestRollback_deploysAllAtOnce(t *testing.T) {
	for _, public := range []bool{true, false} {
		// A promotion in the same environment ramps api, or is refused a
		// canary of an app-wide change; the rollback deploys it at once.
		s, plan := rollbackComputeWorld(t, canaryDoc, nil, public)
		promotion := *plan
		promotion.Kind, promotion.Rollback, promotion.From, promotion.FromEnvironmentID = KindPromotion, nil, "staging", envStaging
		promoted, _, err := s.compute(t.Context(), &promotion)
		if err != nil {
			t.Fatal(err)
		}
		if public && (promoted.Release == nil || !slices.Equal(promoted.Release.Canary, []string{"service:api"})) {
			t.Fatalf("promotion release %+v, problems %v", promoted.Release, codes(promoted.Problems))
		}
		if !public && !slices.Contains(codes(promoted.Problems), codeCanaryAppWide+":api") {
			t.Fatalf("promotion problems %v", codes(promoted.Problems))
		}

		result, _, err := s.compute(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
		if action(t, result, "service:api").Op != Update || result.Release == nil || result.Release.Strategy != "canary" ||
			len(result.Release.Canary) != 0 || result.Release.Thresholds != promoted.Release.Thresholds {
			t.Fatalf("public %v: api %s, release %+v", public, action(t, result, "service:api").Op, result.Release)
		}
		for _, f := range append(slices.Clone(result.Problems), result.Warnings...) {
			if strings.HasPrefix(f.Code, "canary-") {
				t.Fatalf("public %v: %v", public, codes(append(result.Problems, result.Warnings...)))
			}
		}
		if !result.Executable {
			t.Fatalf("public %v: problems %v", public, codes(result.Problems))
		}
	}
}

func TestRollback_reusesTheRestoredImages(t *testing.T) {
	history := &fakeHistory{images: map[string]ReleasedImage{"api": computedAPI(t)}}
	s, plan := rollbackComputeWorld(t, "version: 1\nservices:\n  api: {source: cmd/api, provider: cloud-run, region: us-central1}\n", history, false)
	result, _, err := s.compute(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(history.asked, []string{envProduction}) || !history.upTo[0].Equal(rollbackAt(10, 0)) {
		t.Fatalf("asked %v up to %v", history.asked, history.upTo)
	}
	a := action(t, result, "service:api")
	if a.Op != Update || a.Service.Artifact == nil || a.Service.Artifact.Reference != artifactImage || a.Service.Artifact.FromEnvironment != "production" {
		t.Fatalf("api %s %+v", a.Op, a.Service.Artifact)
	}
	if want := []ArtifactChoice{{Service: "api", Reused: true, Reference: artifactImage, From: "production"}}; !reflect.DeepEqual(result.Artifacts, want) {
		t.Fatalf("artifacts %+v", result.Artifacts)
	}
}

func TestRollback_digitalOceanRollsBackToTheRecordedDeployment(t *testing.T) {
	plan := &Plan{Kind: KindRollback, Environment: "production", EnvironmentID: envProduction}
	api := productionAPI()
	api.Provider, api.Service.Provider = "digitalocean", "digitalocean"
	img := stagingAPI()
	img.Provider, img.Image, img.Revision, img.DeploymentID = "digitalocean", "", "do_dep_7", "d1"

	result := &Result{Actions: []Action{api}, Problems: []Finding{}, Executable: true}
	artifactService().applyArtifacts(plan, result, map[string]ReleasedImage{"api": img}, "production")
	want := &ReusedArtifact{Revision: "do_dep_7", CommitSHA: artifactCommit, FromEnvironment: "production", FromDeploymentID: "d1"}
	if got := result.Actions[0].Service.Artifact; !reflect.DeepEqual(got, want) {
		t.Fatalf("artifact %+v", got)
	}
	if choices := []ArtifactChoice{{Service: "api", Reused: true, Revision: "do_dep_7", From: "production"}}; !reflect.DeepEqual(result.Artifacts, choices) {
		t.Fatalf("artifacts %+v", result.Artifacts)
	}
	if len(result.Problems) != 0 || !result.Executable {
		t.Fatalf("problems %+v", result.Problems)
	}

	// A release from before Loomplane recorded App Platform deployments
	// can't be rolled back to.
	img.Revision = ""
	result = &Result{Actions: []Action{api}, Problems: []Finding{}, Executable: true}
	artifactService().applyArtifacts(plan, result, map[string]ReleasedImage{"api": img}, "production")
	if len(result.Problems) != 1 || result.Executable || result.Actions[0].Service.Artifact != nil {
		t.Fatalf("problems %+v", result.Problems)
	}
	if f := result.Problems[0]; f.Code != "rollback-unavailable" || f.Subject != ServiceSubject || f.Name != "api" ||
		f.Message != "App Platform can only roll back to a deployment Loomplane recorded; this release predates that" {
		t.Fatalf("problem %+v", f)
	}

	// A promotion still builds the branch.
	result = &Result{Actions: []Action{api}}
	artifactService().applyArtifacts(promotionPlan(), result, map[string]ReleasedImage{"api": img}, "staging")
	if result.Artifacts[0].Reused || len(result.Problems) != 0 {
		t.Fatalf("promotion %+v", result)
	}
}

// routeStore records which plan's held handoffs each read allowed.
type routeStore struct {
	catalogStore
	allowed []string
}

func (s *routeStore) ReleaseRoutes(_ context.Context, _, _, _, allowHeldBy string) ([]cloudinfra.Route, error) {
	s.allowed = append(s.allowed, allowHeldBy)
	return nil, nil
}

// A rollback may plan while the release it undoes holds its routes'
// handoffs; any other plan waits for them.
func TestRollback_toleratesTheHandoffsItsTargetHolds(t *testing.T) {
	store := &routeStore{}
	s := NewService(Deps{Store: store})
	rollback := rollbackPlan("plan_r", releasePlan("plan_3", "aaa3", "www3"), releasePlan("plan_1", "aaa1", "www1"))
	for _, p := range []*Plan{rollback, releasePlan("plan_4", "aaa4", "www4")} {
		if _, err := s.releaseRoutes(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(store.allowed, []string{"plan_3", ""}) {
		t.Fatalf("allowed %q", store.allowed)
	}

	target := releasePlan("plan_3", "aaa3", "www3")
	target.Actions = []Action{
		{ID: "service:web", Routes: []cloudinfra.Route{{ID: "route_1"}, {ID: "route_2"}}},
		{ID: "service:api"},
	}
	want := map[string]bool{
		cloudinfra.ReleasePrepareID("plan_3/service:web", "route_1"): true,
		cloudinfra.ReleasePrepareID("plan_3/service:web", "route_2"): true,
	}
	if got := heldBy(target); !reflect.DeepEqual(got, want) {
		t.Fatalf("held by plan_3: %v", got)
	}
}

type policyStore struct {
	Store
	policy *cloudinfra.CapacityPolicy
}

func (s policyStore) GetCapacityPolicy(context.Context, string, string) (*cloudinfra.CapacityPolicy, error) {
	return s.policy, nil
}

// templateActor records who validated a capacity template.
type templateActor struct{ as []security.Principal }

func (v *templateActor) ValidateCapacityTemplate(ctx context.Context, p security.Principal, d cloudinfra.CapacityDemand, t cloudinfra.PoolExpansionPolicy) (*cloudinfra.CapacityTemplatePin, error) {
	v.as = append(v.as, p)
	return futureTemplateValidator{}.ValidateCapacityTemplate(ctx, p, d, t)
}

// A rollback the automation created checks capacity templates and backup
// schedules as the automation, never as a person named like it.
func TestRollback_automationPlansAsItself(t *testing.T) {
	p, a, target, req, policy := intentFixture()
	p.CreatedBy = security.SystemSubject("org")
	templates := &templateActor{}
	s := NewService(Deps{Store: policyStore{policy: policy}, CapacityTemplates: templates})
	if _, err := s.futureCapacity(t.Context(), p, a, target, req, "app", cloudinfra.ErrConflict); err != nil {
		t.Fatal(err)
	}
	if len(templates.as) != 1 || templates.as[0] != security.SystemPrincipal("org") {
		t.Fatalf("validated as %+v", templates.as)
	}

	auth := &backupGrant{}
	s = NewService(Deps{Authz: auth})
	s.checkBackupPreview(t.Context(), p, &Result{Executable: true, Actions: []Action{{BackupEveryHours: 24}}})
	if auth.actor != security.SystemPrincipal("org") {
		t.Fatalf("backup preview checked as %+v", auth.actor)
	}
}
