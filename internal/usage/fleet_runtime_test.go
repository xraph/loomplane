package usage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/planning"
	domain "github.com/xraph/loomplane/pkg/federation"
	"github.com/xraph/loomplane/pkg/fleet"
)

type runtimeNamespaces struct {
	target cloudinfra.FleetObservationTarget
	opens  int
	begin  func(context.Context) (context.Context, func(), error)
	err    error
}

func (n *runtimeNamespaces) BeginObservation(ctx context.Context) (context.Context, func(), error) {
	if n.begin != nil {
		return n.begin(ctx)
	}
	return ctx, func() {}, n.err
}

func (n *runtimeNamespaces) Target(_ context.Context, org, fleet, instance string) (cloudinfra.FleetObservationTarget, error) {
	if org != n.target.OrgID || fleet != n.target.FleetID || instance != n.target.InstanceID {
		return cloudinfra.FleetObservationTarget{}, fleets.ErrNotFound
	}
	return n.target, n.err
}
func (n *runtimeNamespaces) Revalidate(_ context.Context, t cloudinfra.FleetObservationTarget) error {
	if t != n.target {
		return fleets.ErrConflict
	}
	return n.err
}
func (n *runtimeNamespaces) Open(_ context.Context, t cloudinfra.FleetObservationTarget) (*kube.Client, error) {
	n.opens++
	if t != n.target {
		return nil, fleets.ErrConflict
	}
	return &kube.Client{}, n.err
}

type runtimeBindings struct{ items []*planning.Binding }

func (b *runtimeBindings) ListBindings(context.Context, string, string) ([]*planning.Binding, error) {
	return b.items, nil
}

type runtimeInstances struct{ item instance.Instance }

func (i *runtimeInstances) Get(_ context.Context, key id.ID) (*instance.Instance, error) {
	if key != i.item.ID {
		return nil, fleets.ErrNotFound
	}
	c := i.item
	return &c, nil
}

type runtimeControllers struct{ item kube.ControllerReceipt }

func (c *runtimeControllers) GetController(_ context.Context, org, conn, inst, ref string) (*kube.ControllerReceipt, error) {
	r := c.item
	if r.OrgID != org || r.ConnectionID != conn || r.InstanceID != inst || r.RuntimeRef != ref {
		return nil, kube.ErrControllerMissing
	}
	return &r, nil
}
func runtimeSourceFixture(t *testing.T) (*fleetRuntimeSource, *runtimeNamespaces, *runtimeBindings, *runtimeInstances, fleets.MetricScope, fleet.MetricWindow) {
	t.Helper()
	now := time.Now().UTC()
	iid := id.New(id.PrefixInstance)
	target := cloudinfra.FleetObservationTarget{OrgID: "org", ProjectID: "project", FleetID: "fleet", InstanceID: "fleet-instance", EnvironmentID: "environment", Generation: 1, OwnershipEpoch: 1, ConnectionID: "connection", Namespace: "apps", NamespaceUID: "namespace-uid", ClusterCA: strings.Repeat("b", 64), ServiceAccount: "runtime", ServiceAccountUID: "sa-uid"}
	ns := &runtimeNamespaces{target: target}
	bindings := &runtimeBindings{items: []*planning.Binding{{ID: "binding", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", Provider: "kubernetes", ConnectionID: "connection", Subject: planning.ServiceSubject, Name: "api", ExternalID: iid.String(), PlanID: "plan"}}}
	r := kube.ControllerReceipt{Scope: domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "environment"}, ConnectionID: "connection", InstanceID: iid.String(), PlanID: "plan", RuntimeRef: "runtime-ref", ServiceID: "api", Container: "api", Image: "registry.example/api@sha256:" + strings.Repeat("a", 64), Namespace: "apps", NamespaceUID: "namespace-uid", ClusterCA: target.ClusterCA, ServiceAccount: "default", DeploymentUID: "deployment-uid", DeploymentGeneration: 1, RuntimeRevision: "revision"}
	labels := map[string]string{owned.LabelOrg: r.OrgID, owned.LabelProject: r.ProjectID, owned.LabelEnvironment: r.EnvironmentID, owned.LabelConnection: r.ConnectionID, owned.LabelService: r.ServiceID, owned.LabelPlan: r.PlanID, owned.LabelRuntime: r.RuntimeRef}
	instances := &runtimeInstances{item: instance.Instance{TenantID: "org", ProviderName: "kubernetes.connection", State: cpprovider.StateRunning, Kind: cpprovider.KindDeployment, Labels: labels, Services: []cpprovider.ServiceSpec{{Name: r.Container, Image: r.Image}}}}
	instances.item.ID = iid
	source := NewFleetRuntimeMetricSource(nil, ns, bindings, instances, &runtimeControllers{item: r}).(*fleetRuntimeSource)
	source.now = func() time.Time { return now }
	source.read = func(_ context.Context, _ *kube.Client, got kube.FleetMetricTarget, _ time.Time) (kube.FleetControllerMetrics, error) {
		if got.Controller != r || got.ServiceAccountUID != target.ServiceAccountUID {
			t.Error("wrong read pins")
		}
		cpu, request, memory := 125.0, 500.0, 64.0
		replicas := int64(1)
		return kube.FleetControllerMetrics{Component: "api", MetricsAt: now.Add(-time.Second), ReplicasAt: now, CPUUsageMillicores: &cpu, CPURequestMillicores: &request, MemoryBytes: &memory, DesiredReplicas: &replicas, CurrentReplicas: &replicas, ReadyReplicas: &replicas, IdentityBasis: "pod_name_window"}, nil
	}
	return source, ns, bindings, instances, fleets.MetricScope{OrgID: "org", FleetID: "fleet", InstanceID: "fleet-instance", EnvironmentID: "environment"}, fleet.MetricWindow{Start: now.Add(-time.Hour), End: now}
}
func TestFleetRuntimeSourceReadsPersistedController(t *testing.T) {
	s, ns, _, _, scope, w := runtimeSourceFixture(t)
	batch, err := s.ReadMetrics(t.Context(), scope, w)
	if err != nil {
		t.Fatal(err)
	}
	if ns.opens != 1 || batch.Expected != 1 || len(batch.Samples) != 1 || batch.Samples[0].CPUUsageMillicores == nil || !batch.Samples[0].RuntimeLive || batch.Samples[0].RuntimeIdentity != "pod_name_window" {
		t.Fatalf("batch: %+v opens=%d", batch, ns.opens)
	}
}
func TestFleetRuntimeSourceRejectsScopeBeforeCredentials(t *testing.T) {
	for _, which := range []string{"environment", "project", "connection", "plan", "controller"} {
		t.Run(which, func(t *testing.T) {
			s, ns, b, i, scope, w := runtimeSourceFixture(t)
			switch which {
			case "environment":
				scope.EnvironmentID = "other"
			case "project":
				b.items[0].ProjectID = "other"
			case "connection":
				b.items[0].ConnectionID = "other"
			case "plan":
				b.items[0].PlanID = "other"
			case "controller":
				i.item.Labels[owned.LabelRuntime] = "other"
			}
			batch, _ := s.ReadMetrics(t.Context(), scope, w)
			if ns.opens != 0 {
				t.Fatal("mismatch reached credentials")
			}
			for _, sample := range batch.Samples {
				if sample.CPUUsageMillicores != nil {
					t.Fatal("mismatch produced metrics")
				}
			}
		})
	}
}
func TestFleetRuntimeSourceRechecksAfterRead(t *testing.T) {
	for _, which := range []string{"generation", "binding", "controller", "authority"} {
		t.Run(which, func(t *testing.T) {
			s, ns, b, i, scope, w := runtimeSourceFixture(t)
			read := s.read
			s.read = func(ctx context.Context, c *kube.Client, target kube.FleetMetricTarget, now time.Time) (kube.FleetControllerMetrics, error) {
				v, err := read(ctx, c, target, now)
				switch which {
				case "generation":
					ns.target.Generation++
				case "binding":
					b.items[0].PlanID = "replacement"
				case "controller":
					i.item.Labels[owned.LabelRuntime] = "replacement"
				case "authority":
					ns.err = fleets.ErrConflict
				}
				return v, err
			}
			batch, err := s.ReadMetrics(t.Context(), scope, w)
			if !errors.Is(err, fleets.ErrConflict) || len(batch.Samples) != 0 {
				t.Fatalf("stale observations returned: %+v %v", batch, err)
			}
		})
	}
}
func TestFleetRuntimeSourceHistoricalDoesNotOpenCurrentRuntime(t *testing.T) {
	s, ns, _, _, scope, w := runtimeSourceFixture(t)
	w.End = w.End.Add(-time.Hour)
	w.Start = w.Start.Add(-time.Hour)
	batch, err := s.ReadMetrics(t.Context(), scope, w)
	if err != nil {
		t.Fatal(err)
	}
	if ns.opens != 0 {
		t.Fatal("historical window opened current runtime")
	}
	for _, sample := range batch.Samples {
		if sample.CPUUsageMillicores != nil {
			t.Fatal("historical window used current gauge")
		}
	}
}

func TestFleetRuntimeSourceRetainsObservationOwnership(t *testing.T) {
	source, n, _, _, scope, window := runtimeSourceFixture(t)
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	owned, cancel := context.WithCancel(t.Context())
	defer cancel()
	n.begin = func(context.Context) (context.Context, func(), error) { return owned, func() { close(finished) }, nil }
	source.read = func(ctx context.Context, _ *kube.Client, _ kube.FleetMetricTarget, _ time.Time) (kube.FleetControllerMetrics, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return kube.FleetControllerMetrics{}, ctx.Err()
	}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = source.ReadMetrics(t.Context(), scope, window) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("source did not begin sample")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("sample ignored observation cancellation")
	}
	select {
	case <-finished:
		t.Fatal("ownership released before remote read drained")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sample did not finish")
	}
	select {
	case <-finished:
	default:
		t.Fatal("sample leaked ownership")
	}
}
