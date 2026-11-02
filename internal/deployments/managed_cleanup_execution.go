package deployments

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"k8s.io/apimachinery/pkg/util/wait"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
)

type ManagedCleanupExecution struct {
	Routes interface {
		ValidateRuntimeRemoval(context.Context, string, string, string) error
	}
	Store       *planning.ManagedCleanupStore
	Credentials *connections.Service
	Target      *OwnedTarget
}

type retiredRemovalKey struct{}

type retiredRemoval struct {
	store     *planning.ManagedCleanupStore
	grants    []planning.ManagedCleanupGrant
	confirmed atomic.Bool
}

func completedManagedRemoval(org, connection string) func(context.Context, id.ID, map[string]string) (bool, error) {
	return func(ctx context.Context, iid id.ID, labels map[string]string) (bool, error) {
		proof, ok := ctx.Value(retiredRemovalKey{}).(*retiredRemoval)
		if !ok {
			return false, nil
		}
		for _, g := range proof.grants {
			scope := g.Target.Scope
			if scope.Org != org || scope.Connection != connection || scope.Instance != iid.String() || labels[owned.LabelOrg] != scope.Org || labels[owned.LabelProject] != scope.Project || labels[owned.LabelEnvironment] != scope.Environment || labels[owned.LabelService] != scope.Service || labels[owned.LabelPlan] != scope.Plan || labels[owned.LabelRuntime] != scope.RuntimeRef || labels[owned.LabelRevision] != scope.RuntimeRevision {
				continue
			}
			if err := proof.store.CheckRetiredInstance(ctx, org, g.ID, iid.String()); err != nil {
				return true, err
			}
			proof.confirmed.Store(true)
			return true, nil
		}
		return true, kube.ErrManagedAuthority
	}
}

func (e *ManagedCleanupExecution) execute(ctx context.Context, sel planning.ManagedCleanupSelection, check func() error) ([]planning.ManagedCleanupGrant, error) {
	if !orchestration.IsManagedCleanupContext(ctx) {
		return nil, ErrManagedCleanupProtocol
	}
	if e == nil || e.Store == nil || e.Credentials == nil || e.Target == nil || check == nil {
		return nil, planning.ErrManagedCleanupUnavailable
	}
	if err := check(); err != nil {
		return nil, err
	}
	targets, err := e.Store.ReviewedCleanupTargets(ctx, sel)
	if err != nil {
		return nil, err
	}
	for _, t := range targets {
		if t.Kind == planning.ManagedCleanupRetireDependents {
			continue
		}
		if e.Routes == nil {
			return nil, planning.ErrManagedCleanupUnavailable
		}
		if routeErr := e.Routes.ValidateRuntimeRemoval(ctx, t.Scope.Org, t.Scope.Project, t.Scope.Instance); routeErr != nil {
			return nil, routeErr
		}
	}
	err = wait.PollUntilContextCancel(ctx, time.Second, true, func(call context.Context) (bool, error) {
		if checkErr := check(); checkErr != nil {
			return false, checkErr
		}
		if effectsErr := e.Store.ExecuteCleanupEffects(call, sel, e.Credentials); effectsErr != nil {
			if errors.Is(effectsErr, planning.ErrCleanupConverging) {
				return false, nil
			}
			return false, effectsErr
		}
		if recoveryErr := e.Store.RecoverCleanupChildren(call, sel, e.Credentials); recoveryErr != nil {
			if errors.Is(recoveryErr, planning.ErrCleanupConverging) {
				return false, nil
			}
			return false, recoveryErr
		}
		finalizeErr := e.Store.FinalizeManagedCleanup(call, sel, e.Credentials)
		return finalizeErr == nil, finalizeErr
	})
	if err != nil {
		return nil, err
	}
	return e.Store.RetiredCleanupGrants(ctx, sel)
}

func (e *ManagedCleanupExecution) removeMetadata(ctx context.Context, job *Job, grants []planning.ManagedCleanupGrant) error {
	groups := map[string][]planning.ManagedCleanupGrant{}
	for _, g := range grants {
		groups[g.Target.Scope.Instance] = append(groups[g.Target.Scope.Instance], g)
	}
	for _, group := range groups {
		g := group[0]
		scope := g.Target.Scope
		proof := &retiredRemoval{store: e.Store, grants: group}
		if err := e.Store.CheckRetiredInstance(ctx, scope.Org, g.ID, scope.Instance); err != nil {
			return err
		}
		b := &planning.Binding{OrgID: scope.Org, ProjectID: scope.Project, EnvironmentID: scope.Environment, Name: scope.Service, Subject: planning.ServiceSubject, Provider: "kubernetes", ExternalID: scope.Instance, ConnectionID: scope.Connection, PlanID: scope.Plan}
		if err := e.Target.removeInstance(context.WithValue(ctx, retiredRemovalKey{}, proof), job, b); err != nil {
			return err
		}
	}
	return nil
}

func (e *ManagedCleanupExecution) ExecuteManagedFailedCleanup(ctx context.Context, r *planning.ManagedFailedCleanup, heartbeat func([]byte) error) error {
	if r == nil || r.RequiredProtocol != orchestration.ManagedCleanupProtocol || r.Org == "" || r.Deployment == "" || r.ID == "" || r.Attempt < 1 || heartbeat == nil {
		return planning.ErrManagedFailedCleanupConflict
	}
	sel := planning.ManagedCleanupSelection{Org: r.Org, Deployment: r.Deployment, FailedReviewID: r.ID, ExecutionAttempt: r.Attempt}
	grants, err := e.execute(ctx, sel, func() error { return heartbeat(nil) })
	if err != nil {
		return err
	}
	if len(grants) == 0 {
		return nil
	}
	s := grants[0].Target.Scope
	job := &Job{Deployment: &Deployment{OrgID: s.Org, ProjectID: s.Project, EnvironmentID: s.Environment, ID: r.Deployment, StartedBy: r.Actor.UserID}, Checkpoint: heartbeat}
	return e.removeMetadata(ctx, job, grants)
}

func (p *retiredRemoval) verifiedProvider(ctx context.Context, e *OwnedTarget, key string, iid id.ID) error {
	if _, err := e.reviewedManagedBackend(key); err != nil {
		return err
	}
	return p.store.CheckRetiredInstance(ctx, p.grants[0].Target.Scope.Org, p.grants[0].ID, iid.String())
}

func (e *OwnedTarget) reviewedManagedBackend(key string) (*kube.Runtime, error) {
	backend, err := e.registeredBackend(key)
	if err != nil {
		return nil, err
	}
	runtime, ok := backend.(*kube.Runtime)
	if !ok || runtime == nil || runtime.CompleteRetirement == nil {
		return nil, kube.ErrManagedAuthority
	}
	return runtime, nil
}

func (p *retiredRemoval) verifyGone(ctx context.Context, e *OwnedTarget, iid id.ID, prior cpprovider.InstanceState) error {
	if prior != cpprovider.StateDestroyed && !p.confirmed.Load() {
		return kube.ErrManagedAuthority
	}
	_, err := e.Ctrl.Instances.Get(ctx, iid)
	if errors.Is(err, ctrlplane.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("the reviewed instance still exists after removal")
}

func (e *ManagedCleanupExecution) QualifyClusterWorkload(ctx context.Context, p *planning.Plan, a planning.Action) error {
	if e == nil || e.Store == nil || e.Target == nil || e.Target.Ctrl.Instances == nil || e.Target.Ctrl.Providers == nil || e.Target.Ctrl.Deploys == nil || e.Target.Secrets == nil || e.Target.Cloud != connections.Kubernetes || e.Credentials == nil || e.Credentials.ManagedCredentials == nil || e.Target.Credentials != e.Credentials || e.Routes == nil || !orchestration.IsManagedWorkloadContext(ctx) {
		return planning.ErrManagedCleanupUnavailable
	}
	if journal, ok := e.Target.Controllers.(*kube.ControllerStore); !ok || !journal.ManagedWritesEnabled() {
		return planning.ErrManagedCleanupUnavailable
	}
	if len(a.Routes) > 0 || a.Service == nil || a.Service.Catalog != nil || a.Service.Component != nil {
		return planning.ErrManagedCleanupUnavailable
	}
	if p.RequiredRuntimeProtocol == orchestration.ManagedCleanupProtocol && !orchestration.IsManagedCleanupContext(ctx) {
		return ErrManagedCleanupProtocol
	}
	if err := e.Store.CheckWorkloadExecutionRoute(ctx, p, a); err != nil {
		return err
	}
	runtime, err := e.Target.reviewedManagedBackend(e.Target.Register(p.OrgID, a.ConnectionID))
	if err != nil {
		return err
	}
	if runtime.Session == nil || runtime.Values == nil || runtime.RecordController == nil {
		return planning.ErrManagedCleanupUnavailable
	}
	return nil
}
