package cloudinfra

import (
	"testing"
	"time"
)

func TestClusterBudgetScopedUnknown(t *testing.T) {
	p := &CapacityPolicy{Revision: 1, Limits: ResourceVector{Servers: 2, CPUMillis: 2, MemoryBytes: 2, StorageBytes: 2}, Budget: &CapacityBudget{MaxHourlyMicroUSD: 100, MaxHorizonMicroUSD: 100, HorizonSeconds: 3600}}
	estimate, err := moneyEstimate([]MoneyComponent{{Name: "inventory", Source: "account_inventory", Identity: "opaque", Quantity: 1}}, 3600, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	review := &BudgetReview{PolicyRevision: 1, Estimate: estimate, Digest: "baseline"}
	if checkBudgetReview(p, review, review, false) == nil {
		t.Fatal("automation accepted unknown baseline")
	}
	if err := checkBudgetReview(p, review, review, true); err != nil {
		t.Fatal(err)
	}
	changed := *review
	changed.Digest = "changed"
	if checkBudgetReview(p, review, &changed, true) == nil {
		t.Fatal("manual acknowledgement authorized unseen baseline")
	}
}
