package cloudinfra

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestClusterEnvelopeConsumesOnce(t *testing.T) {
	e := ClusterCapacityEnvelope{Maximum: ResourceVector{Servers: 5, CPUMillis: 10}, Consumed: ResourceVector{Servers: 2, CPUMillis: 4}, Pending: ResourceVector{Servers: 2, CPUMillis: 4}}
	if got := envelopeRemainder(e); got.Servers != 1 || got.CPUMillis != 2 {
		t.Fatal(got)
	}
	e.Pending = ResourceVector{}
	if got := envelopeRemainder(e); got.Servers != 3 {
		t.Fatal(got)
	}
}
func TestClusterEnvelopeRetainsAmbiguousCost(t *testing.T) {
	e := ClusterCapacityEnvelope{Maximum: ResourceVector{Servers: 5}, DesiredMaximum: ResourceVector{Servers: 2}, Consumed: ResourceVector{Servers: 2}, State: "decreasing"}
	if envelopeRemainder(e).Servers != 3 {
		t.Fatal("unverified decrease released capacity")
	}
}
func TestClusterEnvelopeAccountMembershipPrivate(t *testing.T) {
	a, err := summarizeAccount(provider.CapacityInventory{Provider: "provider", AccountID: "account", CheckedAt: time.Now(), Servers: []provider.CapacityServer{{ID: "private-physical-id", CPUMillis: number(1), MemoryBytes: number(1), StorageBytes: number(1)}}})
	if err != nil {
		t.Fatal(err)
	}
	if a.membership == nil || len(a.membership.Servers) != 1 {
		t.Fatal("lost private membership")
	}
	raw, err := json.Marshal(CapacityUsage{Accounts: []CapacityAccountSnapshot{a}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-physical-id") {
		t.Fatal("public usage exposed account membership")
	}
	var historic CapacityAccountSnapshot
	if err := json.Unmarshal(raw, &historic); err != nil {
		t.Fatal(err)
	}
	if historic.membership != nil {
		t.Fatal("historical totals fabricated membership")
	}
}
func TestClusterEnvelopeReviewPinsMaximum(t *testing.T) {
	in := ClusterInput{}
	original := clusterDigest(in)
	in.EnvelopeReview = &ClusterEnvelopeReview{Pools: []ClusterPoolEnvelopeReview{{Key: "one", Maximum: ResourceVector{Servers: 4}}}}
	if clusterDigest(in) == original {
		t.Fatal("maximum absent from digest")
	}
}

func TestClusterBudgetAutomaticUnknownWithoutPolicy(t *testing.T) {
	o := &ClusterOperation{Input: ClusterInput{EnvelopeReview: &ClusterEnvelopeReview{Automatic: true}}}
	if checkAutomaticEnvelopePrices(o) == nil {
		t.Fatal("missing budget policy authorized unknown automatic cost")
	}
	o.MoneyEstimate = &MoneyEstimate{Known: true, CheckedAt: time.Now()}
	o.Input.EnvelopeReview.Pools = []ClusterPoolEnvelopeReview{{Money: MoneyEstimate{Known: false, CheckedAt: time.Now()}}}
	if checkAutomaticEnvelopePrices(o) == nil {
		t.Fatal("unknown envelope price authorized automation")
	}
	o.Input.EnvelopeReview.Pools[0].Money.Known = true
	if err := checkAutomaticEnvelopePrices(o); err != nil {
		t.Fatal(err)
	}
}

func TestClusterEnvelopeRetirementNeedsAcceptedReceipt(t *testing.T) {
	now := time.Now()
	ref := provider.ClusterRef{ID: "physical", Incarnation: "birth"}
	o := &ClusterOperation{ID: "delete", Kind: "retire-cluster", Status: "succeeded", Digest: "review", Steps: []ClusterStep{{State: "completed", CompletedAt: &now}}}
	if retirementEnvelopeReleaseConfirmed(o, ref, now) {
		t.Fatal("status-only deletion released standing envelope")
	}
	o.Receipts = []provider.ClusterMutation{{Kind: "retire-cluster", OperationID: o.ID, Ref: ref, RequestDigest: o.Digest}}
	if !retirementEnvelopeReleaseConfirmed(o, ref, now) {
		t.Fatal("accepted retirement evidence rejected")
	}
	if retirementEnvelopeReleaseConfirmed(o, ref, now.Add(-time.Second)) {
		t.Fatal("old account observation released envelope")
	}
	o.Receipts[0].Ref.Incarnation = "replacement"
	if retirementEnvelopeReleaseConfirmed(o, ref, now) {
		t.Fatal("replacement receipt released envelope")
	}
}
