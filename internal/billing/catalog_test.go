//go:build cloud

package billing

import (
	"slices"
	"testing"

	"github.com/xraph/ledger/invoice"
	"github.com/xraph/ledger/plan"
	"github.com/xraph/ledger/types"
)

func TestSeedCatalog_IdempotentAndExact(t *testing.T) {
	db, _ := scratchDB(t)
	l := newLedgerWith(db, nil)
	ctx := t.Context()
	for range 2 {
		if err := SeedCatalog(ctx, l, testAppID); err != nil {
			t.Fatal(err)
		}
	}
	cloud, err := l.GetPlanBySlug(ctx, PlanCloud, testAppID)
	if err != nil {
		t.Fatal(err)
	}
	if cloud.Status != plan.StatusActive || cloud.TrialDays != 0 || cloud.Pricing.BaseAmount != types.USD(500) || cloud.Pricing.BillingPeriod != plan.PeriodMonthly {
		t.Fatalf("cloud plan %+v", cloud)
	}
	if len(cloud.Credits) != 1 || cloud.Credits[0].Key != "usage" || cloud.Credits[0].Amount != types.USD(500) || !slices.Equal(cloud.Credits[0].FeatureKeys, BaseFeatures) {
		t.Fatalf("cloud credit %+v", cloud.Credits)
	}
	for _, k := range BaseFeatures {
		tiers := tiersOf(cloud, k)
		if len(tiers) != 1 || tiers[0].Type != plan.TierGraduated || tiers[0].UnitAmount != types.USD(1) || tiers[0].UpTo != 0 {
			t.Fatalf("%s tiers %+v", k, tiers)
		}
		if vErr := invoice.ValidateTiers(tiers, "usd"); vErr != nil {
			t.Fatalf("%s tiers don't validate: %v", k, vErr)
		}
	}
	ai, err := l.GetPlanBySlug(ctx, PlanAI, testAppID)
	if err != nil || ai.Pricing.BaseAmount != types.USD(1000) || ai.Credits[0].Amount != types.USD(1000) || !slices.Equal(ai.Credits[0].FeatureKeys, []string{FeatureAIUSD}) {
		t.Fatalf("ai plan %+v %v", ai, err)
	}
}

func tiersOf(p *plan.Plan, key string) []plan.PriceTier {
	var out []plan.PriceTier
	for _, t := range p.Pricing.Tiers {
		if t.FeatureKey == key {
			out = append(out, t)
		}
	}
	return out
}

func TestSeedCatalog_FeaturesAreMeteredMonthlyWithNothingIncluded(t *testing.T) {
	db, _ := scratchDB(t)
	l := newLedgerWith(db, nil)
	ctx := t.Context()
	if err := SeedCatalog(ctx, l, testAppID); err != nil {
		t.Fatal(err)
	}
	for slug, keys := range map[string][]string{PlanCloud: BaseFeatures, PlanAI: {FeatureAIUSD}} {
		p, err := l.GetPlanBySlug(ctx, slug, testAppID)
		if err != nil {
			t.Fatal(err)
		}
		if p.Currency != "usd" || len(p.Features) != len(keys) {
			t.Fatalf("%s: currency %q, %d features", slug, p.Currency, len(p.Features))
		}
		for i, k := range keys {
			f := p.Features[i]
			if f.Key != k || f.Type != plan.FeatureMetered || f.Limit != 0 || f.Period != plan.PeriodMonthly || f.SoftLimit || f.Name == "" {
				t.Fatalf("%s feature %d: %+v", slug, i, f)
			}
		}
	}
}

func TestSeedCatalog_SecondRunWritesNothing(t *testing.T) {
	db, _ := scratchDB(t)
	l := newLedgerWith(db, nil)
	ctx := t.Context()
	if err := SeedCatalog(ctx, l, testAppID); err != nil {
		t.Fatal(err)
	}
	before, err := l.GetPlanBySlug(ctx, PlanCloud, testAppID)
	if err != nil {
		t.Fatal(err)
	}
	if err = SeedCatalog(ctx, l, testAppID); err != nil {
		t.Fatal(err)
	}
	after, err := l.GetPlanBySlug(ctx, PlanCloud, testAppID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("the second seed rewrote the plan: %v then %v", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestSeedCatalog_PutsAChangedPlanBack(t *testing.T) {
	db, _ := scratchDB(t)
	l := newLedgerWith(db, nil)
	ctx := t.Context()
	if err := SeedCatalog(ctx, l, testAppID); err != nil {
		t.Fatal(err)
	}
	p, err := l.GetPlanBySlug(ctx, PlanCloud, testAppID)
	if err != nil {
		t.Fatal(err)
	}
	planID, priceID, featureID := p.ID.String(), p.Pricing.ID.String(), p.Features[0].ID.String()
	p.TrialDays = 14
	p.Pricing.BaseAmount = types.USD(900)
	p.Credits[0].Amount = types.USD(50)
	p.Pricing.Tiers[0].UnitAmount = types.USD(3)
	p.ProviderID, p.ProviderName = "prod_1", "stripe"
	if err = l.UpdatePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = SeedCatalog(ctx, l, testAppID); err != nil {
		t.Fatal(err)
	}
	got, err := l.GetPlanBySlug(ctx, PlanCloud, testAppID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrialDays != 0 || got.Pricing.BaseAmount != types.USD(500) || got.Credits[0].Amount != types.USD(500) || got.Pricing.Tiers[0].UnitAmount != types.USD(1) {
		t.Fatalf("the seed left the change: %+v", got)
	}
	if got.ID.String() != planID || got.Pricing.ID.String() != priceID || got.Features[0].ID.String() != featureID {
		t.Fatal("the seed replaced the plan's ids")
	}
	if got.ProviderID != "prod_1" || got.ProviderName != "stripe" {
		t.Fatalf("the seed dropped the provider link: %q %q", got.ProviderID, got.ProviderName)
	}
}

func TestSeedCatalog_ReactivatesAnArchivedPlan(t *testing.T) {
	db, _ := scratchDB(t)
	l := newLedgerWith(db, nil)
	ctx := t.Context()
	if err := SeedCatalog(ctx, l, testAppID); err != nil {
		t.Fatal(err)
	}
	p, err := l.GetPlanBySlug(ctx, PlanAI, testAppID)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.ArchivePlan(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err = SeedCatalog(ctx, l, testAppID); err != nil {
		t.Fatal(err)
	}
	if got, _ := l.GetPlanBySlug(ctx, PlanAI, testAppID); got == nil || got.Status != plan.StatusActive {
		t.Fatalf("ai plan after reseed: %+v", got)
	}
}
