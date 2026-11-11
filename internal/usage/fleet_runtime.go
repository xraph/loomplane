package usage

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"time"

	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/fleet"
)

type FleetRuntimeNamespaces interface {
	BeginObservation(context.Context) (context.Context, func(), error)
	Revalidate(context.Context, cloudinfra.FleetObservationTarget) error
	Target(context.Context, string, string, string) (cloudinfra.FleetObservationTarget, error)
	Open(context.Context, cloudinfra.FleetObservationTarget) (*kube.Client, error)
}
type FleetRuntimeInstances interface {
	Get(context.Context, id.ID) (*instance.Instance, error)
}
type FleetRuntimeControllers interface {
	GetController(context.Context, string, string, string, string) (*kube.ControllerReceipt, error)
}

type fleetRuntimeSource struct {
	traffic     fleets.MetricSource
	namespaces  FleetRuntimeNamespaces
	bindings    FleetMetricBindings
	instances   FleetRuntimeInstances
	controllers FleetRuntimeControllers
	read        func(context.Context, *kube.Client, kube.FleetMetricTarget, time.Time) (kube.FleetControllerMetrics, error)
	now         func() time.Time
}

func NewFleetRuntimeMetricSource(traffic fleets.MetricSource, namespaces FleetRuntimeNamespaces, bindings FleetMetricBindings, instances FleetRuntimeInstances, controllers FleetRuntimeControllers) fleets.MetricSource {
	return &fleetRuntimeSource{traffic: traffic, namespaces: namespaces, bindings: bindings, instances: instances, controllers: controllers, read: kube.ReadFleetControllerMetrics, now: time.Now}
}
func (s *fleetRuntimeSource) ReadMetrics(ctx context.Context, scope fleets.MetricScope, w fleet.MetricWindow) (fleets.MetricBatch, error) {
	empty := fleets.MetricBatch{Partial: true}
	if s == nil || s.namespaces == nil || s.bindings == nil || s.instances == nil || s.controllers == nil || s.read == nil || s.now == nil {
		return empty, fleets.ErrInvalid
	}
	now := s.now().UTC()
	if w.Start.IsZero() || !w.End.After(w.Start) || w.End.After(now) || w.End.Sub(w.Start) > 24*time.Hour || now.Sub(w.End) > 30*24*time.Hour {
		return empty, fleets.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ctx, finish, err := s.namespaces.BeginObservation(ctx)
	if err != nil {
		return empty, err
	}
	defer finish()
	target, err := s.namespaces.Target(ctx, scope.OrgID, scope.FleetID, scope.InstanceID)
	if err != nil {
		return empty, err
	}
	if target.OrgID != scope.OrgID || target.FleetID != scope.FleetID || target.InstanceID != scope.InstanceID || target.EnvironmentID != scope.EnvironmentID {
		return empty, fleets.ErrNotFound
	}
	bindings, err := s.bindings.ListBindings(ctx, scope.OrgID, scope.EnvironmentID)
	if err != nil {
		return empty, err
	}
	if len(bindings) > 1000 {
		return empty, fleets.ErrInvalid
	}
	// Copy the persisted snapshot before any remote read can race a new deployment.
	before, err := runtimeBindingSnapshot(bindings, target)
	if err != nil {
		return empty, err
	}
	copied := make([]*planning.Binding, 0, len(bindings))
	for _, b := range bindings {
		if b.Subject == planning.ServiceSubject {
			c := *b
			copied = append(copied, &c)
		}
	}
	slices.SortFunc(copied, func(a, b *planning.Binding) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	out := fleets.MetricBatch{Expected: len(copied), Partial: true, Samples: make([]fleets.MetricSample, 0, len(copied))}
	traffic := map[string]fleets.MetricSample{}
	if s.traffic != nil {
		batch, readErr := s.traffic.ReadMetrics(ctx, scope, w)
		if readErr == nil {
			for _, sample := range batch.Samples {
				if _, ok := traffic[sample.Component]; ok {
					return empty, fleets.ErrInvalid
				}
				traffic[sample.Component] = sample
			}
		}
	}
	historical := now.Sub(w.End) > 5*time.Minute
	type evidence struct {
		binding  *planning.Binding
		receipt  *kube.ControllerReceipt
		instance *instance.Instance
		index    int
	}
	candidates := []evidence{}
	for _, b := range copied {
		sample := fleets.MetricSample{Component: b.Name, Source: b.Provider, Reason: "source_unavailable"}
		if v, ok := traffic[b.Name]; ok {
			sample = v
			delete(traffic, b.Name)
		}
		out.Samples = append(out.Samples, sample)
		if historical || b.Provider != "kubernetes" || len(candidates) >= 32 {
			continue
		}
		receipt, inst, readErr := s.loadReceipt(ctx, b, target)
		if readErr != nil {
			continue
		}
		candidates = append(candidates, evidence{b, receipt, inst, len(out.Samples) - 1})
	}
	if len(traffic) != 0 {
		return empty, fleets.ErrConflict
	}
	if historical || len(candidates) == 0 {
		return out, nil
	}
	client, err := s.namespaces.Open(ctx, target)
	if err != nil {
		return out, nil
	}
	for _, candidate := range candidates {
		observed, readErr := s.read(ctx, client, kube.FleetMetricTarget{RuntimeLabels: candidate.instance.Labels, Controller: *candidate.receipt, ServiceAccount: target.ServiceAccount, ServiceAccountUID: target.ServiceAccountUID}, s.now().UTC())
		if readErr != nil {
			continue
		}
		if observed.Component != candidate.binding.Name {
			return empty, fleets.ErrConflict
		}
		sample := &out.Samples[candidate.index]
		sample.RuntimeLive = true
		sample.RuntimeObservedAt = observed.MetricsAt
		sample.ReplicasObservedAt = observed.ReplicasAt
		sample.RuntimeIdentity = observed.IdentityBasis
		sample.CPUUsageMillicores = observed.CPUUsageMillicores
		sample.CPURequestMillicores = observed.CPURequestMillicores
		sample.MemoryBytes = observed.MemoryBytes
		sample.DesiredReplicas = observed.DesiredReplicas
		sample.CurrentReplicas = observed.CurrentReplicas
		sample.ReadyReplicas = observed.ReadyReplicas
		if observed.Partial {
			sample.Reason = "source_unavailable"
		} else {
			sample.Reason = ""
		}
	}
	afterBindings, err := s.bindings.ListBindings(ctx, scope.OrgID, scope.EnvironmentID)
	if err != nil {
		return empty, err
	}
	after, err := runtimeBindingSnapshot(afterBindings, target)
	if err != nil || before != after {
		return empty, fleets.ErrConflict
	}
	for _, candidate := range candidates {
		r, i, readErr := s.loadReceipt(ctx, candidate.binding, target)
		if readErr != nil || *r != *candidate.receipt || !runtimeSameInstance(i, candidate.instance) {
			return empty, fleets.ErrConflict
		}
	}
	if err = s.namespaces.Revalidate(ctx, target); err != nil {
		return empty, fleets.ErrConflict
	}
	again, err := s.namespaces.Target(ctx, scope.OrgID, scope.FleetID, scope.InstanceID)
	if err != nil || again != target {
		return empty, fleets.ErrConflict
	}
	if checkErr := ctx.Err(); checkErr != nil {
		return empty, checkErr
	}
	return out, nil
}

// loadReceipt joins the current persisted binding and Ctrl Plane attempt before
// opening credentials. A receipt from an older plan cannot replace that attempt.
func (s *fleetRuntimeSource) loadReceipt(ctx context.Context, b *planning.Binding, target cloudinfra.FleetObservationTarget) (*kube.ControllerReceipt, *instance.Instance, error) {
	if b == nil || b.OrgID != target.OrgID || b.ProjectID != target.ProjectID || b.EnvironmentID != target.EnvironmentID || b.ConnectionID != target.ConnectionID || b.Provider != "kubernetes" || b.Subject != planning.ServiceSubject || b.Name == "" || b.PlanID == "" || b.Drift != "" {
		return nil, nil, fleets.ErrConflict
	}
	iid, err := id.ParseWithPrefix(b.ExternalID, id.PrefixInstance)
	if err != nil {
		return nil, nil, fleets.ErrConflict
	}
	inst, err := s.instances.Get(ctx, iid)
	if err != nil {
		return nil, nil, err
	}
	if inst == nil || inst.ID != iid || inst.TenantID != b.OrgID || inst.ProviderName != "kubernetes."+b.ConnectionID || inst.State != cpprovider.StateRunning || (inst.Kind != "" && inst.Kind != cpprovider.KindDeployment) {
		return nil, nil, fleets.ErrConflict
	}
	for k, v := range map[string]string{owned.LabelOrg: b.OrgID, owned.LabelProject: b.ProjectID, owned.LabelEnvironment: b.EnvironmentID, owned.LabelConnection: b.ConnectionID, owned.LabelService: b.Name, owned.LabelPlan: b.PlanID} {
		if inst.Labels[k] != v {
			return nil, nil, fleets.ErrConflict
		}
	}
	ref := inst.Labels[owned.LabelRuntime]
	if ref == "" {
		return nil, nil, fleets.ErrConflict
	}
	r, err := s.controllers.GetController(ctx, b.OrgID, b.ConnectionID, b.ExternalID, ref)
	if err != nil {
		return nil, nil, err
	}
	if r == nil || !r.Valid() || r.OrgID != b.OrgID || r.ProjectID != b.ProjectID || r.EnvironmentID != b.EnvironmentID || r.ConnectionID != b.ConnectionID || r.InstanceID != b.ExternalID || r.ServiceID != b.Name || r.PlanID != b.PlanID || r.RuntimeRef != ref || r.Namespace != target.Namespace || r.NamespaceUID != target.NamespaceUID || r.ClusterCA != target.ClusterCA || len(inst.Services) != 1 || inst.Services[0].Name != r.Container || inst.Services[0].Image != r.Image {
		return nil, nil, fleets.ErrConflict
	}
	copyInstance := *inst
	copyInstance.Labels = make(map[string]string, len(inst.Labels))
	for k, v := range inst.Labels {
		copyInstance.Labels[k] = v
	}
	copyInstance.Services = append([]cpprovider.ServiceSpec(nil), inst.Services...)
	return r, &copyInstance, nil
}
func runtimeSameInstance(a, b *instance.Instance) bool {
	return a != nil && b != nil && a.ID == b.ID && a.TenantID == b.TenantID && a.State == b.State && a.Kind == b.Kind && a.ProviderName == b.ProviderName && reflect.DeepEqual(a.Labels, b.Labels) && reflect.DeepEqual(a.Services, b.Services)
}

func runtimeBindingSnapshot(bindings []*planning.Binding, target cloudinfra.FleetObservationTarget) (string, error) {
	if len(bindings) > 1000 {
		return "", fleets.ErrInvalid
	}
	seen := map[string]bool{}
	items := make([]*planning.Binding, 0, len(bindings))
	for _, b := range bindings {
		if b == nil || b.OrgID != target.OrgID || b.ProjectID != target.ProjectID || b.EnvironmentID != target.EnvironmentID {
			return "", fleets.ErrNotFound
		}
		if b.Subject != planning.ServiceSubject {
			continue
		}
		if b.ID == "" || b.Name == "" || seen[b.Name] {
			return "", fleets.ErrInvalid
		}
		seen[b.Name] = true
		items = append(items, b)
	}
	slices.SortFunc(items, func(a, b *planning.Binding) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	raw, err := json.Marshal(items)
	return string(raw), err
}
