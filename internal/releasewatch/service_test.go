package releasewatch

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/security"
)

var ada = security.Principal{OrgID: "org", UserID: "usr_ada"}

func TestArm_watchesChangedServicesTheCanaryDoesntGuard(t *testing.T) {
	r := newRig(t, "      strategy: canary\n      thresholds:\n        error_rate: 2%\n")
	r.binding("api", "render")
	r.binding("web", "cloud-run")
	r.binding("worker", "render")
	plan := release(service("api", "render", planning.Update), service("web", "cloud-run", planning.Update), service("worker", "render", planning.Keep))
	thresholds := releasehealth.Thresholds{ErrorRate: 0.02, ErrorRateDelta: 0.005, P95LatencyRatio: 1.3, MinRequests: 200}
	plan.Release = &planning.Release{Strategy: "canary", Canary: []string{"service:web"}, Thresholds: thresholds}
	d := succeeded(plan)
	d.Canary = &deployments.Canary{Status: deployments.CanaryPromoted}
	r.arm(t, plan, d)

	w := r.store.only(t)
	want := []WatchedService{{Name: "api", Provider: "render", BindingID: "bind_api", Op: "update", Revision: "rev_api"}}
	if !reflect.DeepEqual(w.Services, want) {
		t.Fatalf("services %+v, want %+v", w.Services, want)
	}
	if len(w.Unwatched) != 0 || w.Unverified {
		t.Fatalf("unwatched %+v, unverified %v", w.Unwatched, w.Unverified)
	}
	if !w.StartsAt.Equal(finished) || !w.EndsAt.Equal(finished.Add(15*time.Minute)) || !w.BoundAt.Equal(finished.Add(30*time.Minute)) {
		t.Fatalf("starts %s ends %s bound %s", w.StartsAt, w.EndsAt, w.BoundAt)
	}
	if w.Status != StatusWatching || w.Thresholds != plan.Thresholds() || w.Thresholds != thresholds {
		t.Fatalf("status %s thresholds %+v", w.Status, w.Thresholds)
	}
	if w.OrgID != "org" || w.ProjectID != "proj" || w.EnvironmentID != "env_p" || w.Environment != "production" ||
		w.DeploymentID != "dep_1" || w.PlanID != "plan_1" || w.SettledAt != nil || w.Held == nil || len(w.Held) != 0 {
		t.Fatalf("watch %+v", w)
	}
}

func TestArm_watchesEveryChangedServiceWithoutACanary(t *testing.T) {
	r := newRig(t, "      strategy: blue_green\n")
	r.binding("api", "render")
	r.binding("web", "cloud-run")
	plan := release(service("api", "render", planning.Update), service("web", "cloud-run", planning.Create))
	plan.Release = &planning.Release{Strategy: "blue_green", Thresholds: releasehealth.DefaultThresholds()}
	r.arm(t, plan, succeeded(plan))
	w := r.store.only(t)
	if len(w.Services) != 2 || w.Services[0].Name != "api" || w.Services[1].Name != "web" || w.Services[1].Op != "create" {
		t.Fatalf("services %+v", w.Services)
	}
}

func TestArm_isIdempotentAndSkipsRollbacksAndCanaryFailures(t *testing.T) {
	r := newRig(t, "      strategy: direct\n      watch: 20m\n")
	r.binding("api", "render")
	plan := release(service("api", "render", planning.Update))
	d := succeeded(plan)
	r.arm(t, plan, d)
	decision, err := r.svc.Prepare(t.Context(), d, plan)
	if err != nil || !decision.Armed || decision.Duration != 20*time.Minute {
		t.Fatalf("prepare %+v %v", decision, err)
	}
	if err = r.svc.Arm(t.Context(), d, decision); err != nil {
		t.Fatal(err)
	}
	if n := r.store.count(); n != 1 {
		t.Fatalf("arming twice left %d watches", n)
	}
	if w := r.store.only(t); !w.EndsAt.Equal(finished.Add(20 * time.Minute)) {
		t.Fatalf("ends %s", w.EndsAt)
	}

	never := func(name string, plan *planning.Plan, d *deployments.Deployment) {
		t.Helper()
		other := newRig(t, "      strategy: canary\n")
		other.binding("api", "render")
		other.binding("web", "cloud-run")
		other.arm(t, plan, d)
		if n := other.store.count(); n != 0 {
			t.Errorf("%s armed %d watches", name, n)
		}
	}
	rollback := release(service("api", "render", planning.Update))
	rollback.Kind, rollback.Rollback = planning.KindRollback, &planning.Rollback{Trigger: planning.TriggerWatch}
	never("a rollback", rollback, succeeded(rollback))
	for _, status := range []string{deployments.CanaryRolledBack, deployments.CanaryRevertFailed, deployments.CanaryPromoteFailed} {
		ramped := release(service("web", "cloud-run", planning.Update), service("api", "render", planning.Update))
		ramped.Release = &planning.Release{Strategy: "canary", Canary: []string{"service:web"}}
		d := succeeded(ramped)
		d.Canary = &deployments.Canary{Status: status}
		never("a canary that ended "+status, ramped, d)
	}
	keeps := release(service("api", "render", planning.Keep), service("web", "cloud-run", planning.Update))
	keeps.Release = &planning.Release{Strategy: "canary", Canary: []string{"service:web"}}
	never("a release whose only change ramped", keeps, succeeded(keeps))
	sync := release(service("api", "render", planning.Update))
	sync.CloudSyncID = "sync_1"
	never("a Cloud sync", sync, succeeded(sync))
	none := newRig(t, "")
	none.binding("api", "render")
	none.arm(t, release(service("api", "render", planning.Update)), succeeded(release()))
	if n := none.store.count(); n != 0 {
		t.Errorf("an environment without a release block armed %d watches", n)
	}
	failed := succeeded(plan)
	failed.Status = deployments.StatusFailed
	if decision, err = r.svc.Prepare(t.Context(), failed, plan); err != nil || decision.Armed {
		t.Errorf("a failed deployment prepared %+v %v", decision, err)
	}
}

func TestArm_servicesWithoutMetricsAreUnwatched(t *testing.T) {
	r := newRig(t, "      strategy: blue_green\n")
	r.binding("api", "vercel")
	r.binding("web", "cloud-run")
	r.reader.answer = func(binding string, _ releasehealth.Window) (releasehealth.Health, error) {
		if binding == "bind_web" {
			return releasehealth.Health{Reason: "The connection that deployed web is gone."}, nil
		}
		return healthy(), nil
	}
	plan := release(service("api", "vercel", planning.Update), service("web", "cloud-run", planning.Update))
	r.arm(t, plan, succeeded(plan))
	w := r.store.only(t)
	_, vercel := Watchable("vercel")
	want := []UnwatchedService{
		{Name: "api", Provider: "vercel", Reason: vercel},
		{Name: "web", Provider: "cloud-run", Reason: "not watched: The connection that deployed web is gone."},
	}
	if !reflect.DeepEqual(w.Unwatched, want) {
		t.Fatalf("unwatched %+v, want %+v", w.Unwatched, want)
	}
	if !strings.HasPrefix(vercel, "not watched: ") || !strings.Contains(vercel, "request and error counts") {
		t.Fatalf("vercel reason %q talks about something other than metrics", vercel)
	}
	if w.Status != StatusInconclusive || w.Verdict != string(releasehealth.Inconclusive) || !w.Unverified || w.SettledAt == nil || len(w.Services) != 0 {
		t.Fatalf("watch %+v", w)
	}
	wantReason := "Loomplane can't read health for any changed service: api: " + vercel + "; web: not watched: The connection that deployed web is gone."
	if w.Reason != wantReason {
		t.Fatalf("reason %q, want %q", w.Reason, wantReason)
	}
	if r.audit.find("release_watch.settle") == nil {
		t.Fatalf("audit %v", r.audit.actions())
	}

	// One watchable service is enough to watch; the rest leave it unverified.
	r = newRig(t, "      strategy: blue_green\n")
	r.binding("api", "vercel")
	r.binding("web", "cloud-run")
	plan = release(service("api", "vercel", planning.Update), service("web", "cloud-run", planning.Update))
	r.arm(t, plan, succeeded(plan))
	w = r.store.only(t)
	if w.Status != StatusWatching || !w.Unverified || len(w.Services) != 1 || len(w.Unwatched) != 1 {
		t.Fatalf("watch %+v", w)
	}

	// A reader that fails at arm time never stops the finish; the tick
	// decides.
	r = newRig(t, "      strategy: blue_green\n")
	r.binding("web", "cloud-run")
	r.reader.answer = func(string, releasehealth.Window) (releasehealth.Health, error) {
		return releasehealth.Health{}, errors.New("monitoring timed out")
	}
	plan = release(service("web", "cloud-run", planning.Update))
	r.arm(t, plan, succeeded(plan))
	if w = r.store.only(t); w.Status != StatusWatching || len(w.Services) != 1 || w.Unverified {
		t.Fatalf("watch %+v", w)
	}
}

func TestArmable(t *testing.T) {
	r := newRig(t, "      strategy: canary\n")
	r.binding("web", "cloud-run")
	r.binding("api", "render")
	r.binding("site", "vercel")
	web := r.plans.bindings["env_p"][0]
	web.Config, _ = json.Marshal(planning.ServiceConfig{Provider: "cloud-run", Port: 8080, Public: true})
	got, err := r.svc.Armable(t.Context(), ada, "proj", "production")
	if err != nil {
		t.Fatal(err)
	}
	_, vercel := Watchable("vercel")
	want := &Coverage{
		Environment: "production", Duration: 15 * time.Minute, Canary: []string{"web"},
		Services: []ServiceCoverage{
			{Name: "api", Provider: "render", Watchable: true},
			{Name: "site", Provider: "vercel", Watchable: false, Reason: vercel},
		},
		Unverified: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("coverage %+v, want %+v", got, want)
	}
	if len(r.authz.checks) != 1 || r.authz.checks[0] != security.DeploymentRead {
		t.Fatalf("checked %v", r.authz.checks)
	}

	// A routed service switches over by DNS, so the watch has it.
	r.svc.routes = fakeRoutes{{ID: "route_1", Input: cloudinfra.RouteInput{EnvironmentID: "env_p", Service: "web"}}}
	if got, err = r.svc.Armable(t.Context(), ada, "proj", "production"); err != nil || len(got.Canary) != 0 || len(got.Services) != 3 {
		t.Fatalf("routed coverage %+v %v", got, err)
	}
	r.svc.routes = nil

	r.plans.bindings["env_p"] = r.plans.bindings["env_p"][:2]
	if got, err = r.svc.Armable(t.Context(), ada, "proj", "production"); err != nil || got.Unverified {
		t.Fatalf("all watchable %+v %v", got, err)
	}

	bare := newRig(t, "")
	bare.binding("api", "render")
	if got, err = bare.svc.Armable(t.Context(), ada, "proj", "production"); err != nil || got.Duration != 0 || !got.Unverified {
		t.Fatalf("no release block %+v %v", got, err)
	}

	r.authz.deny = true
	if _, err = r.svc.Armable(t.Context(), ada, "proj", "production"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("denied %v", err)
	}
	r.authz.deny = false
	if _, err = r.svc.Armable(t.Context(), ada, "proj", "qa"); err == nil {
		t.Fatal("an unknown environment had coverage")
	}
}

func TestEvidence(t *testing.T) {
	r := newRig(t, "      strategy: blue_green\n")
	r.binding("api", "render")
	plan := release(service("api", "render", planning.Update))
	r.arm(t, plan, succeeded(plan))
	got, err := r.svc.Evidence(t.Context(), ada, "proj", "production")
	if err != nil {
		t.Fatal(err)
	}
	w := r.store.only(t)
	want := &ReleaseEvidence{
		Environment: "production", DeploymentID: "dep_1", Watched: true, WatchID: w.ID, Status: StatusWatching,
		Settled: false, Unverified: true, Reason: "the watch hasn't finished",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evidence %+v, want %+v", got, want)
	}

	w.Status, w.Verdict, w.Reason = StatusPassed, "pass", "Every watched service passed."
	if err = r.store.Save(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	got, _ = r.svc.Evidence(t.Context(), ada, "proj", "production")
	if !got.Settled || got.Unverified || got.Status != StatusPassed || got.Verdict != "pass" || got.Reason != "Every watched service passed." {
		t.Fatalf("passed evidence %+v", got)
	}

	// An environment that arms no watch.
	direct := newRig(t, "      strategy: direct\n")
	direct.binding("api", "render")
	plan = release(service("api", "render", planning.Update))
	direct.arm(t, plan, succeeded(plan))
	got, _ = direct.svc.Evidence(t.Context(), ada, "proj", "production")
	if got.Watched || !got.Unverified || got.Reason != "production arms no post-deploy watch" || got.DeploymentID != "dep_1" {
		t.Fatalf("direct evidence %+v", got)
	}

	// Every changed service ramped as a canary.
	canary := newRig(t, "      strategy: canary\n")
	canary.binding("web", "cloud-run")
	plan = release(service("web", "cloud-run", planning.Update))
	plan.Release = &planning.Release{Strategy: "canary", Canary: []string{"service:web"}}
	d := succeeded(plan)
	d.Canary = &deployments.Canary{Status: deployments.CanaryPromoted}
	canary.arm(t, plan, d)
	got, _ = canary.svc.Evidence(t.Context(), ada, "proj", "production")
	if got.Watched || got.Unverified || got.Reason != "every changed service was guarded by its canary" {
		t.Fatalf("canary evidence %+v", got)
	}

	// A rollback isn't watched.
	rb := newRig(t, "      strategy: canary\n")
	rb.binding("api", "render")
	plan = release(service("api", "render", planning.Update))
	plan.Kind, plan.Rollback = planning.KindRollback, &planning.Rollback{Trigger: planning.TriggerManual}
	rb.arm(t, plan, succeeded(plan))
	got, _ = rb.svc.Evidence(t.Context(), ada, "proj", "production")
	if got.Watched || !got.Unverified || got.Reason != "rollbacks aren't watched" {
		t.Fatalf("rollback evidence %+v", got)
	}

	empty := newRig(t, "      strategy: canary\n")
	got, _ = empty.svc.Evidence(t.Context(), ada, "proj", "production")
	if got.Watched || !got.Unverified || got.DeploymentID != "" || got.Reason != "production has no release yet" {
		t.Fatalf("no release evidence %+v", got)
	}

	r.authz.deny = true
	if _, err = r.svc.Evidence(t.Context(), ada, "proj", "production"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("denied %v", err)
	}
	r.authz.mu.Lock()
	checks := r.authz.checks
	r.authz.mu.Unlock()
	for _, c := range checks {
		if c != security.DeploymentRead {
			t.Fatalf("checked %v", c)
		}
	}
}

func TestForDeployment(t *testing.T) {
	r := newRig(t, "      strategy: blue_green\n")
	r.binding("api", "render")
	plan := release(service("api", "render", planning.Update))
	r.arm(t, plan, succeeded(plan))
	w, err := r.svc.ForDeployment(t.Context(), ada, "dep_1")
	if err != nil || w.DeploymentID != "dep_1" {
		t.Fatalf("watch %+v %v", w, err)
	}
	if _, err = r.svc.ForDeployment(t.Context(), ada, "dep_2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing %v", err)
	}
	if _, err = r.svc.ForDeployment(t.Context(), security.Principal{OrgID: "other", UserID: "usr_eve"}, "dep_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another organization's %v", err)
	}
	latest, err := r.svc.Latest(t.Context(), ada, "proj", "production")
	if err != nil || latest.ID != w.ID {
		t.Fatalf("latest %+v %v", latest, err)
	}
	r.authz.deny = true
	if _, err = r.svc.ForDeployment(t.Context(), ada, "dep_1"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("denied %v", err)
	}
}
