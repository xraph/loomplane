package cloudinfra

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestClusterScalingModeTransition(t *testing.T) {
	observation := provider.ClusterObservation{State: "running", Pools: []provider.NodePoolSpec{{ID: "pool", Count: 2, Autoscale: true, Min: 1, Max: 3}}}
	policy := &ClusterScalingPolicy{PoolID: "pool", Mode: "manual"}
	if scalingConverged(policy, observation) {
		t.Fatal("manual ownership before native disable")
	}
	observation.Pools[0].Autoscale = false
	if !scalingConverged(policy, observation) {
		t.Fatal("disabled controller did not hand off")
	}
	policy.Mode, policy.MinNodes, policy.MaxNodes = "native", 1, 3
	if scalingConverged(policy, observation) {
		t.Fatal("native ownership before native enable")
	}
	observation.Pools[0].Autoscale = true
	if !scalingConverged(policy, observation) {
		t.Fatal("converged native bounds rejected")
	}
	observation.Pools[0].Max = 4
	if scalingConverged(policy, observation) {
		t.Fatal("unreviewed maximum accepted")
	}
}

func TestClusterScalingExternalBreach(t *testing.T) {
	policy := ClusterScalingPolicy{PoolID: "pool", Mode: "native", MinNodes: 0, MaxNodes: 3, CooldownSeconds: 600, ContinuityPoolID: "system", ContinuityFloor: 1}
	observation := provider.ClusterObservation{State: "running", Pools: []provider.NodePoolSpec{{ID: "pool", Autoscale: true, Min: 0, Max: 3, Count: 2}, {ID: "system", Count: 1}}}
	if reason := scalingBreach(policy, observation); reason != "" {
		t.Fatal(reason)
	}
	observation.Pools[1].Autoscale = true
	if reason := scalingBreach(policy, observation); reason != "continuity_floor_lost" {
		t.Fatal(reason)
	}
	observation.Pools[1].Autoscale = false
	observation.Pools[0].Count = 4
	if reason := scalingBreach(policy, observation); reason != "native_bounds_changed" {
		t.Fatal(reason)
	}
}

func TestClusterScalingDefaultOff(t *testing.T) {
	s, _, _, _, principal, _ := clusterFixture(t)
	if _, err := s.PreviewClusterScaling(t.Context(), principal, "cloud", "cluster", ClusterScalingPolicy{}); !errors.Is(err, security.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.CurrentClusterScalingPolicy(t.Context(), "org", "cloud", "cluster", "pool"); !errors.Is(err, security.ErrForbidden) {
		t.Fatal(err)
	}
}

type scalingProvider struct {
	*maintenanceProvider
	converge  bool
	wrongPool bool
}

func (p *scalingProvider) UpdateNodePoolScaling(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef, pool string, bounds provider.NodePoolScaling) (provider.ClusterMutation, error) {
	if err := a.BeforeEffect(ctx); err != nil {
		return provider.ClusterMutation{}, err
	}
	p.effects++
	if p.converge {
		p.observation.Pools[0].Autoscale = bounds.Native
		p.observation.Pools[0].Min = bounds.Min
		p.observation.Pools[0].Max = bounds.Max
	}
	if p.wrongPool {
		pool = "wrong-pool"
	}
	return provider.ClusterMutation{Kind: "node-pool-scaling", Ref: ref, PoolID: pool}, nil
}

func TestClusterScalingServiceDisableWaits(t *testing.T) {
	s, store, base, _, principal, in := maintenanceFixture(t)
	adapter := &scalingProvider{maintenanceProvider: base}
	adapter.observation.Pools = []provider.NodePoolSpec{{ID: "pool", Count: 2, Autoscale: true, Min: 1, Max: 3}}
	s.ClusterCapacity.Enabled = true
	s.Adapters = NewAdapters()
	if err := s.Adapters.RegisterCluster("digitalocean", adapter, provider.NodePoolAutoscaling, provider.ClusterMutationObservation); err != nil {
		t.Fatal(err)
	}
	store.resource.ScalingPolicies = map[string]ClusterScalingPolicy{"pool": {ID: "old", Revision: 1, Mode: "native", PoolID: "pool"}}
	in.Maintenance = &ClusterMaintenanceInput{PoolID: "pool", ObservedRevision: 1}
	preview, err := s.PreviewClusterScaling(t.Context(), principal, "cloud", "cluster", ClusterScalingPolicy{PoolID: "pool", Mode: "manual", ExpectedRevision: 1, ReviewInput: &in})
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.ApplyCluster(t.Context(), principal, "cloud", preview.ID, ApplyInput{Review: preview.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileClusterOperation(t.Context(), principal.OrgID, op.ID, nil); err != nil {
		t.Fatal(err)
	}
	if store.op.Status == "succeeded" || store.resource.ScalingPolicies["pool"].Revoked || adapter.effects != 1 {
		t.Fatal("premature native ownership release")
	}
	adapter.observation.Pools[0].Autoscale = false
	store.restart()
	if err := s.ReconcileClusterOperation(t.Context(), principal.OrgID, op.ID, nil); err != nil {
		t.Fatal(err)
	}
	saved := store.resource.ScalingPolicies["pool"]
	if store.op.Status != "succeeded" || !saved.Revoked || saved.Revision != 2 || saved.OperationID != op.ID || adapter.effects != 1 {
		t.Fatalf("disable did not settle once: %+v", saved)
	}
}

func TestClusterScalingEnvelopeRemainderMoney(t *testing.T) {
	price := int64(10)
	estimate, err := moneyEstimate([]MoneyComponent{{Name: "nodes:workers", Source: "provider_options", Identity: "node-price", Quantity: 3, HourlyMicroUSD: &price}, {Name: "persistent_storage", Source: "provider_options", Identity: "storage", Quantity: 1}}, 3600, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := clusterPoolEnvelopeMoney(&estimate, "workers")
	if err != nil {
		t.Fatal(err)
	}
	if !nodes.Known || nodes.HourlyMicroUSD != 30 || len(nodes.Components) != 1 || nodes.Components[0].Name != "nodes" || estimate.Known || len(estimate.Components) != 2 {
		t.Fatal("node normalization changed required non-node review", nodes, estimate)
	}
	remaining, err := envelopeRemainderMoney(ClusterCapacityEnvelope{Money: nodes}, 2)
	if err != nil || remaining.HourlyMicroUSD != 20 || remaining.Components[0].Quantity != 2 {
		t.Fatal("consumed node charged in remainder", remaining, err)
	}
	reduced := nodes
	reduced.Components = append([]MoneyComponent(nil), nodes.Components...)
	reduced.Components[0].Quantity = 1
	envelope := ClusterCapacityEnvelope{Money: nodes, PriorMoney: &nodes, Maximum: ResourceVector{Servers: 3}, DesiredMaximum: ResourceVector{Servers: 1}, Consumed: ResourceVector{Servers: 1}, State: "decreasing", Review: ClusterPoolEnvelopeReview{Money: reduced}}
	held, err := envelopeRemainderMoney(envelope, envelope.Maximum.Servers-envelope.Consumed.Servers)
	if err != nil || held.HourlyMicroUSD != 20 || envelope.PriorMoney.HourlyMicroUSD != 30 {
		t.Fatal("unconverged decrease released prior money", held, err)
	}
}

func TestClusterScalingServiceRejectsWrongPoolReceipt(t *testing.T) {
	s, store, base, _, principal, in := maintenanceFixture(t)
	adapter := &scalingProvider{maintenanceProvider: base, converge: true, wrongPool: true}
	adapter.observation.Pools = []provider.NodePoolSpec{{ID: "pool", Count: 2, Autoscale: true, Min: 1, Max: 3}}
	s.ClusterCapacity.Enabled = true
	s.Adapters = NewAdapters()
	if err := s.Adapters.RegisterCluster("digitalocean", adapter, provider.NodePoolAutoscaling, provider.ClusterMutationObservation); err != nil {
		t.Fatal(err)
	}
	store.resource.ScalingPolicies = map[string]ClusterScalingPolicy{"pool": {ID: "old", Revision: 1, Mode: "native", PoolID: "pool"}}
	in.Maintenance = &ClusterMaintenanceInput{PoolID: "pool", ObservedRevision: 1}
	preview, err := s.PreviewClusterScaling(t.Context(), principal, "cloud", "cluster", ClusterScalingPolicy{PoolID: "pool", Mode: "manual", ExpectedRevision: 1, ReviewInput: &in})
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.ApplyCluster(t.Context(), principal, "cloud", preview.ID, ApplyInput{Review: preview.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileClusterOperation(t.Context(), principal.OrgID, op.ID, nil); err != nil {
		t.Fatal(err)
	}
	if store.op.Status != "needs_attention" || store.resource.ScalingPolicies["pool"].Revoked {
		t.Fatalf("wrong pool receipt released ownership: %s", store.op.Status)
	}
}
