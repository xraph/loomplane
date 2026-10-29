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

	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
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

	// Rolling back the rollback itself skips d2, which it undid.
	w = newRollbackWorld(allowAll{}, []*Plan{r1, r2, r2r}, d1, d2, d2r)
	if got, err = w.target(t, ""); err != nil {
		t.Fatal(err)
	}
	if got.Target != ref(d2r) || got.Restore != ref(d1) {
		t.Fatalf("target %+v restore %+v, want d2r and d1", got.Target, got.Restore)
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
