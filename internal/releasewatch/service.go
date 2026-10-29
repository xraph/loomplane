package releasewatch

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
)

// Rollbacks starts and approves the rollback a breach calls for, as
// Loomplane automation. *planning.Service is the real one.
type Rollbacks interface {
	CreateAutomaticRollback(ctx context.Context, orgID, deploymentID string, in planning.AutomaticRollbackInput) (*planning.Plan, error)
	ApproveAutomatic(ctx context.Context, orgID, planID string) (*planning.Plan, error)
}

// Plans reads plans and an environment's bindings. planning.PgStore is
// the real one.
type Plans interface {
	GetPlan(ctx context.Context, orgID, id string) (*planning.Plan, error)
	ListBindings(ctx context.Context, orgID, environmentID string) ([]*planning.Binding, error)
}

// Deployments finds the deployment of a rollback plan. deployments.PgStore
// is the real one.
type Deployments interface {
	ForPlan(ctx context.Context, orgID, planID string) (*deployments.Deployment, error)
}

// Routes lists a project's domain routes. cloudinfra.PgStore is the real
// one.
type Routes interface {
	Routes(ctx context.Context, orgID, projectID string) ([]*cloudinfra.Route, error)
}

// Deps are what the Service needs.
type Deps struct {
	Store       Store
	Rollbacks   Rollbacks
	Plans       Plans
	Deployments Deployments
	History     planning.ReleaseHistory
	// Health is *deployments.HealthReader in production, which canaries
	// read too.
	Health releasehealth.Reader
	// Watchable says whether a provider's services can be watched; nil
	// is Watchable.
	Watchable func(provider string) (bool, string)
	Manifests planning.Manifests
	Projects  planning.Projects
	// Routes may be nil: then Armable counts no service as routed.
	Routes Routes
	Authz  planning.Authorizer
	// Audit may be nil.
	Audit projects.Auditor
	Now   func() time.Time
}

// Service arms, judges and reads post-deploy watches.
type Service struct {
	store       Store
	rollbacks   Rollbacks
	plans       Plans
	deployments Deployments
	history     planning.ReleaseHistory
	health      releasehealth.Reader
	watchable   func(string) (bool, string)
	manifests   planning.Manifests
	projects    planning.Projects
	routes      Routes
	authz       planning.Authorizer
	audit       projects.Auditor
	now         func() time.Time
}

var _ deployments.WatchHook = (*Service)(nil)

// NewService returns a Service over d.
func NewService(d Deps) *Service {
	s := &Service{
		store: d.Store, rollbacks: d.Rollbacks, plans: d.Plans, deployments: d.Deployments, history: d.History,
		health: d.Health, watchable: d.Watchable, manifests: d.Manifests, projects: d.Projects, routes: d.Routes,
		authz: d.Authz, audit: d.Audit, now: d.Now,
	}
	if s.watchable == nil {
		s.watchable = Watchable
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// probeTimeout bounds the health reads Arm makes, all together: it runs
// inside the finish activity, which has a minute in all.
const probeTimeout = 15 * time.Second

// Prepare decides whether a deployment of plan gets a watch. A plan made
// without a release block never does, and Prepare reads nothing to say
// so. Nor do a rollback, a Cloud sync, a catalog plan, a deployment that
// failed or one whose canary didn't promote. Otherwise it does when the
// environment's release block watches (in the manifest the plan was made
// from, so what was reviewed) and the release changed a service its
// canary didn't ramp.
func (s *Service) Prepare(ctx context.Context, d *deployments.Deployment, plan *planning.Plan) (deployments.WatchDecision, error) {
	// Planning pins the environment's release block on the plan.
	if plan == nil || plan.Release == nil || !watchedRelease(d, plan) {
		return deployments.WatchDecision{}, nil
	}
	settings, err := s.settings(ctx, plan)
	if err != nil || settings.Duration <= 0 || len(changed(plan)) == 0 {
		return deployments.WatchDecision{}, err
	}
	return deployments.WatchDecision{Armed: true, Duration: settings.Duration}, nil
}

// watchedRelease reports whether d, a deployment of plan, can be watched
// at all.
func watchedRelease(d *deployments.Deployment, plan *planning.Plan) bool {
	switch {
	case plan == nil || d == nil:
		return false
	case plan.Kind == planning.KindRollback, plan.CloudSyncID != "", plan.Catalog:
		return false
	case d.Status == deployments.StatusFailed:
		return false
	}
	return d.Canary == nil || d.Canary.Status == deployments.CanaryPromoted
}

// changed are the service actions of plan that deploy something its
// canary didn't ramp.
func changed(plan *planning.Plan) []planning.Action {
	out := []planning.Action{}
	for _, a := range plan.Actions {
		if a.Subject != planning.ServiceSubject || (a.Op != planning.Create && a.Op != planning.Update && a.Op != planning.Replace) {
			continue
		}
		if !plan.Release.Ramps(a.ID) {
			out = append(out, a)
		}
	}
	return out
}

// settings reads the watch from the manifest revision plan was made from.
func (s *Service) settings(ctx context.Context, plan *planning.Plan) (Settings, error) {
	if plan.ManifestID == "" {
		return Settings{}, nil
	}
	rev, err := s.manifests.Get(ctx, plan.OrgID, plan.ManifestID)
	if err != nil {
		return Settings{}, fmt.Errorf("read the manifest the plan was made from: %w", err)
	}
	settings, _ := Config(rev.Manifest, plan.Environment)
	return settings, nil
}

// Arm records the watch Prepare decided on, for d as stored once it
// succeeded: the watch starts when it finished. It's idempotent, so a
// retried finish never arms twice. A service whose cloud reports no
// request and error counts, or whose health can't be read now, is left
// unwatched with the reason, and leaves the release unverified; when no
// service can be watched the watch settles inconclusive at once. A health
// read that fails here never fails the finish: the tick judges it.
func (s *Service) Arm(ctx context.Context, d *deployments.Deployment, decision deployments.WatchDecision) error {
	if !decision.Armed || decision.Duration <= 0 || d.Status != deployments.StatusSucceeded || d.FinishedAt == nil {
		return nil
	}
	if _, err := s.store.ForDeployment(ctx, d.OrgID, d.ID); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	plan, err := s.plans.GetPlan(ctx, d.OrgID, d.PlanID)
	if err != nil {
		return err
	}
	bindings, err := s.plans.ListBindings(ctx, d.OrgID, d.EnvironmentID)
	if err != nil {
		return err
	}
	id, err := typeid.Generate("watch")
	if err != nil {
		return fmt.Errorf("generate watch id: %w", err)
	}
	now := s.now().UTC()
	starts := d.FinishedAt.UTC()
	w := &Watch{
		ID: id.String(), OrgID: d.OrgID, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, Environment: d.Environment,
		DeploymentID: d.ID, PlanID: d.PlanID, Status: StatusWatching, Thresholds: plan.Thresholds(),
		Services: []WatchedService{}, Unwatched: []UnwatchedService{}, Held: []HeldRoute{},
		StartsAt: starts, EndsAt: starts.Add(decision.Duration), CreatedAt: now, UpdatedAt: now,
	}
	w.BoundAt = w.EndsAt.Add(grace)
	probe, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	for _, a := range changed(plan) {
		reason := s.unwatchable(probe, d, a, binding(bindings, a.ID))
		if reason != "" {
			w.Unwatched = append(w.Unwatched, UnwatchedService{Name: a.Name, Provider: a.Provider, Reason: reason})
			continue
		}
		b := binding(bindings, a.ID)
		w.Services = append(w.Services, WatchedService{Name: a.Name, Provider: a.Provider, BindingID: b.ID, Op: string(a.Op), Revision: b.Revision})
	}
	w.Unverified = len(w.Unwatched) > 0
	if len(w.Services) == 0 {
		parts := make([]string, 0, len(w.Unwatched))
		for _, u := range w.Unwatched {
			parts = append(parts, u.Name+": "+u.Reason)
		}
		w.Status, w.Verdict, w.SettledAt = StatusInconclusive, string(releasehealth.Inconclusive), &now
		w.Reason = "Loomplane can't read health for any changed service: " + strings.Join(parts, "; ")
	}
	inserted, err := s.store.Insert(ctx, w)
	if err != nil || !inserted {
		return err
	}
	if w.SettledAt != nil {
		s.auditSettle(ctx, w)
	}
	return nil
}

// unwatchable says why action a of d's plan can't be watched, or "" when
// it can. A health read that fails, or runs out of probe's time, leaves it
// watched.
func (s *Service) unwatchable(probe context.Context, d *deployments.Deployment, a planning.Action, b *planning.Binding) string {
	if ok, reason := s.watchable(a.Provider); !ok {
		return reason
	}
	if b == nil {
		return "not watched: Loomplane has no record of where " + a.Name + " runs."
	}
	starts := d.FinishedAt.UTC()
	h, err := s.health.Health(probe, d.OrgID, b.ID, "", releasehealth.Window{Start: starts.Add(-5 * time.Minute), End: starts})
	if err != nil || h.Available {
		return ""
	}
	return "not watched: " + cmp.Or(h.Reason, "its cloud reports no health for it")
}

// binding is the binding of action actionID, or nil.
func binding(bindings []*planning.Binding, actionID string) *planning.Binding {
	for _, b := range bindings {
		if string(b.Subject)+":"+b.Name == actionID {
			return b
		}
	}
	return nil
}

// ForDeployment returns a deployment's watch, or ErrNotFound. It needs
// deployment:read on the watch's project.
func (s *Service) ForDeployment(ctx context.Context, p security.Principal, deploymentID string) (*Watch, error) {
	w, err := s.store.ForDeployment(ctx, p.OrgID, deploymentID)
	if err != nil {
		return nil, err
	}
	if authErr := s.authz.Require(ctx, p, security.DeploymentRead, w.ProjectID); authErr != nil {
		return nil, authErr
	}
	return w, nil
}

// Latest returns the environment's newest watch, or ErrNotFound. It needs
// deployment:read on the project.
func (s *Service) Latest(ctx context.Context, p security.Principal, projectID, environment string) (*Watch, error) {
	env, err := s.environment(ctx, p, projectID, environment)
	if err != nil {
		return nil, err
	}
	return s.store.Latest(ctx, p.OrgID, env.ID)
}

// environment checks the principal may read the project's deployments and
// returns its environment called name.
func (s *Service) environment(ctx context.Context, p security.Principal, projectID, name string) (*projects.Environment, error) {
	if err := s.authz.Require(ctx, p, security.DeploymentRead, projectID); err != nil {
		return nil, err
	}
	envs, err := s.projects.ListEnvironments(ctx, p.OrgID, projectID)
	if err != nil {
		return nil, err
	}
	for _, e := range envs {
		if e.Name == name {
			return e, nil
		}
	}
	return nil, fmt.Errorf("%w: this project has no environment called %q", projects.ErrEnvironmentNotFound, name)
}

// Armable says what a release in the environment would be watched for
// now, from its latest manifest and the services it runs: which ones a
// canary would ramp (by the plan's static rules; a connection's canary
// permissions and app-wide changes aren't checked) and whether each other
// one would be watched. It needs deployment:read on the project.
func (s *Service) Armable(ctx context.Context, p security.Principal, projectID, environment string) (*Coverage, error) {
	env, err := s.environment(ctx, p, projectID, environment)
	if err != nil {
		return nil, err
	}
	var m *manifest.Manifest
	rev, err := s.manifests.Latest(ctx, p.OrgID, projectID)
	switch {
	case err == nil:
		m = rev.Manifest
	case !errors.Is(err, manifests.ErrNotFound):
		return nil, err
	}
	settings, _ := Config(m, env.Name)
	bindings, err := s.plans.ListBindings(ctx, p.OrgID, env.ID)
	if err != nil {
		return nil, err
	}
	routed, err := s.routed(ctx, p.OrgID, projectID, env.ID)
	if err != nil {
		return nil, err
	}
	out := &Coverage{Environment: env.Name, Duration: settings.Duration, Canary: []string{}, Services: []ServiceCoverage{}, Unverified: settings.Duration <= 0}
	slices.SortFunc(bindings, func(a, b *planning.Binding) int { return strings.Compare(a.Name, b.Name) })
	for _, b := range bindings {
		if b.Subject != planning.ServiceSubject {
			continue
		}
		if settings.Strategy == manifest.StrategyCanary {
			var cfg planning.ServiceConfig
			if json.Unmarshal(b.Config, &cfg) == nil && planning.WouldRamp(b.Provider, &cfg, planning.Update, routed[b.Name]) {
				out.Canary = append(out.Canary, b.Name)
				continue
			}
		}
		ok, reason := s.watchable(b.Provider)
		out.Services = append(out.Services, ServiceCoverage{Name: b.Name, Provider: b.Provider, Watchable: ok, Reason: reason})
		out.Unverified = out.Unverified || !ok
	}
	return out, nil
}

// routed returns the environment's live routes, by service.
func (s *Service) routed(ctx context.Context, orgID, projectID, environmentID string) (map[string][]cloudinfra.Route, error) {
	out := map[string][]cloudinfra.Route{}
	if s.routes == nil {
		return out, nil
	}
	routes, err := s.routes.Routes(ctx, orgID, projectID)
	if err != nil {
		return nil, err
	}
	for _, r := range routes {
		if r.Input.EnvironmentID == environmentID && r.Status != "removed" {
			out[r.Input.Service] = append(out[r.Input.Service], *r)
		}
	}
	return out, nil
}

// Evidence is what the watch says about the environment's current
// release: its newest succeeded deployment. Unverified means missing
// evidence: a watch still running or inconclusive, a service it couldn't
// watch, or no watch where one could have run. It needs deployment:read
// on the project.
func (s *Service) Evidence(ctx context.Context, p security.Principal, projectID, environment string) (*ReleaseEvidence, error) {
	env, err := s.environment(ctx, p, projectID, environment)
	if err != nil {
		return nil, err
	}
	releases, err := s.history.Releases(ctx, p.OrgID, env.ID, time.Time{}, 1)
	if err != nil {
		return nil, err
	}
	out := &ReleaseEvidence{Environment: env.Name}
	if len(releases) == 0 {
		out.Unverified, out.Reason = true, env.Name+" has no release yet"
		return out, nil
	}
	rec := releases[0]
	out.DeploymentID = rec.DeploymentID
	w, err := s.store.ForDeployment(ctx, p.OrgID, rec.DeploymentID)
	switch {
	case err == nil:
		out.Watched, out.WatchID, out.Status, out.Verdict = true, w.ID, w.Status, w.Verdict
		out.Settled, out.Unverified, out.Reason = !w.Open(), w.Unverified || w.Open(), w.Reason
		if w.Open() {
			out.Reason = "the watch hasn't finished"
		}
		return out, nil
	case !errors.Is(err, ErrNotFound):
		return nil, err
	}
	out.Unverified, out.Reason, err = s.unwatchedRelease(ctx, p.OrgID, env.Name, rec)
	return out, err
}

// unwatchedRelease says why a release has no watch, and whether that
// leaves it unverified.
func (s *Service) unwatchedRelease(ctx context.Context, orgID, environment string, rec planning.ReleaseRecord) (unverified bool, reason string, err error) {
	if rec.Kind == planning.KindRollback {
		return true, "rollbacks aren't watched", nil
	}
	plan, err := s.plans.GetPlan(ctx, orgID, rec.PlanID)
	if err != nil {
		return false, "", err
	}
	settings, err := s.settings(ctx, plan)
	if err != nil {
		return false, "", err
	}
	switch {
	case settings.Duration <= 0:
		return true, environment + " arms no post-deploy watch", nil
	case len(changed(plan)) > 0:
		return true, "no watch was armed for this release", nil
	case plan.Release != nil && len(plan.Release.Canary) > 0:
		return false, "every changed service was guarded by its canary", nil
	}
	return false, "the release changed no service", nil
}

// auditSettle records that w settled.
func (s *Service) auditSettle(ctx context.Context, w *Watch) {
	if s.audit == nil {
		return
	}
	s.audit.Record(ctx, projects.AuditEvent{
		Action: "release_watch.settle", Severity: "info", ProjectID: w.ProjectID, Principal: security.SystemPrincipal(w.OrgID),
		Attributes: map[string]any{
			"watch_id": w.ID, "deployment_id": w.DeploymentID, "environment": w.Environment,
			"status": w.Status, "verdict": w.Verdict, "reason": w.Reason, "unverified": w.Unverified,
		},
	})
}
