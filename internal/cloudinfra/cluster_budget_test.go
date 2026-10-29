package cloudinfra

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestClusterBudgetUnknownPrice(t *testing.T) {
	in := ClusterInput{Spec: provider.ClusterSpec{NodePools: []provider.NodePoolSpec{{Name: "workers", Size: "missing", Count: 2}}}}
	_, missing, err := clusterCosts(in, provider.ClusterOptions{}, math.MaxInt64)
	if err != nil || !slices.Contains(missing, "nodes:workers") {
		t.Fatalf("missing size price was silently omitted: %v, %v", missing, err)
	}
}

func TestClusterBudgetOverflow(t *testing.T) {
	price := 1.0
	in := ClusterInput{Spec: provider.ClusterSpec{NodePools: []provider.NodePoolSpec{{Name: "workers", Size: "large", Count: math.MaxInt}}}}
	options := provider.ClusterOptions{NodeSizeFacts: []provider.ClusterNodeSize{{ID: "large", HourlyUSD: &price}}}
	if _, _, err := clusterCosts(in, options, math.MaxInt64); err == nil {
		t.Fatal("overflowing node cost accepted")
	}
	if _, err := moneyEstimate([]MoneyComponent{{Name: "node", Source: "provider", Identity: "node", Quantity: 1, HourlyMicroUSD: number(math.MaxInt64)}}, DefaultBudgetHorizonSeconds, time.Now()); err == nil {
		t.Fatal("overflowing horizon accepted")
	}
}

func TestClusterBudgetDecimalUnits(t *testing.T) {
	value, err := hourlyMicroUSD(0.000029)
	if err != nil || value != 29 {
		t.Fatalf("decimal conversion: %d %v", value, err)
	}
	estimate, err := moneyEstimate([]MoneyComponent{{Name: "node", Source: "provider", Identity: "node", Quantity: 3, HourlyMicroUSD: number(value)}}, 3600, time.Now())
	if err != nil || !estimate.Known || estimate.HourlyMicroUSD != 87 || estimate.HorizonMicroUSD != 87 {
		t.Fatalf("exact units: %+v %v", estimate, err)
	}
}

func TestClusterBudgetReviewPinsProviderFacts(t *testing.T) {
	s, _, adapter, _, principal, in := clusterFixture(t)
	o, err := s.PreviewCluster(t.Context(), principal, "cloud", in)
	if err != nil {
		t.Fatal(err)
	}
	if o.MoneyEstimate == nil || o.MoneyEstimate.Known || o.MoneyEstimate.PriceDigest == "" || o.Input.PriceDigest != o.MoneyEstimate.PriceDigest || o.MoneyEstimate.HorizonSeconds != DefaultBudgetHorizonSeconds {
		t.Fatalf("missing authoritative review estimate: %+v", o.MoneyEstimate)
	}
	if adapter.creates != 0 {
		t.Fatal("preview made provider effect")
	}
	in.PriceDigest = "caller"
	if _, err := s.PreviewCluster(t.Context(), principal, "cloud", in); err == nil {
		t.Fatal("caller chose price authority")
	}
}

func TestClusterBudgetCategoryAndSemanticTime(t *testing.T) {
	now := time.Now().UTC()
	for _, category := range []string{"network_egress", "persistent_storage", "control_plane", "nodes"} {
		if got := costCategory("", category+":private-resource"); got != category {
			t.Fatal(got)
		}
	}
	if got := costCategory("private-provider-name", "secret"); got != "other" {
		t.Fatal(got)
	}
	a, err := moneyEstimate([]MoneyComponent{{Name: "opaque", Category: "nodes", Source: "pending", Identity: "opaque", Quantity: 1, PriceCheckedAt: &now}}, 3600, now)
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Second)
	c := a.Components[0]
	c.PriceCheckedAt = &later
	b, err := moneyEstimate([]MoneyComponent{c}, 3600, later)
	if err != nil || a.PriceDigest != b.PriceDigest {
		t.Fatal("timestamp changed semantic review", err)
	}
	c.Category = "persistent_storage"
	b, err = moneyEstimate([]MoneyComponent{c}, 3600, later)
	if err != nil || a.PriceDigest == b.PriceDigest {
		t.Fatal("category missing from semantic review", err)
	}
}

func TestClusterBudgetPhysicalNodeOverlap(t *testing.T) {
	now := time.Now()
	e := ClusterCapacityEnvelope{Provider: "p", AccountID: "a", ResourceID: "cluster", Ref: provider.ClusterRef{ID: "physical"}, CheckedAt: now, Consumed: ResourceVector{Servers: 2}, Maximum: ResourceVector{Servers: 4}, NodeIDs: []string{"vm1", "vm2"}}
	servers := map[string]bool{budgetIdentity("p", "a", "vm1"): true, budgetIdentity("p", "other", "vm2"): true}
	count, ok := distinctEnvelopeNodes(e, servers, now)
	if !ok || count != 1 || envelopeRemainder(e).Servers != 2 {
		t.Fatal(count, ok, envelopeRemainder(e))
	}
	e.NodeIDs = []string{"vm1", "vm1"}
	if count, ok = distinctEnvelopeNodes(e, servers, now); ok || count != 2 {
		t.Fatal("ambiguous membership dropped costs", count, ok)
	}
}
