//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/xraph/ledger"
	"github.com/xraph/ledger/id"
	"github.com/xraph/ledger/plan"
	"github.com/xraph/ledger/types"

	"github.com/xraph/loomplane/internal/platformcloud"
)

// The two plans every workspace can hold, by slug.
const (
	PlanCloud = "cloud"
	PlanAI    = "ai"
)

// FeatureAIUSD is the AI add-on's metered feature: US dollars of provider
// cost on the server's key.
const FeatureAIUSD = "ai_usd"

// FeatureStorageGBMonths is metered storage. Nothing emits it yet (platform
// Cloud has no persistent disks), so its rate stays at 0 until something
// does.
const FeatureStorageGBMonths = "storage_gb_months"

// BaseFeatures are the cloud plan's metered features, in display order.
// The first four match platformcloud's constants; storage has no producer.
var BaseFeatures = []string{
	platformcloud.FeatureBuildMinutes, platformcloud.FeatureComputeVCPUHours,
	platformcloud.FeatureComputeGBRAMHours, FeatureStorageGBMonths, platformcloud.FeatureEgressGB,
}

// Features is every billed feature, AI last.
var Features = append(slices.Clone(BaseFeatures), FeatureAIUSD)

// featureNames is what a person reads for each feature. Ledger holds every
// feature's usage in cents, not in its native unit.
var featureNames = map[string]string{
	platformcloud.FeatureBuildMinutes:      "Build minutes",
	platformcloud.FeatureComputeVCPUHours:  "Compute, vCPU-hours",
	platformcloud.FeatureComputeGBRAMHours: "Compute, memory GiB-hours",
	FeatureStorageGBMonths:                 "Storage, GB-months",
	platformcloud.FeatureEgressGB:          "Egress, GB",
	FeatureAIUSD:                           "AI on Loomplane's models",
}

// The plans' prices and included credit, in cents.
const (
	cloudBaseCents = 500
	aiBaseCents    = 1000
)

// The credit pools' keys.
const (
	creditUsage = "usage"
	creditAI    = "ai"
)

// catalog is both plans exactly as the Global Constraints have them. A
// plan's usage is recorded in cents, so each feature has one graduated tier
// at one cent a unit with no upper bound, and nothing is included (Limit 0):
// the credit pool is what pays for the first $5 or $10.
func catalog(appID string) []*plan.Plan {
	return []*plan.Plan{
		{
			Name:        "Loomplane Cloud",
			Slug:        PlanCloud,
			Description: "Runs on Loomplane Cloud, with $5.00 of usage included every month.",
			Currency:    currency,
			Status:      plan.StatusActive,
			TrialDays:   0,
			AppID:       appID,
			Features:    meteredFeatures(BaseFeatures),
			Pricing:     &plan.Pricing{BaseAmount: types.USD(cloudBaseCents), BillingPeriod: plan.PeriodMonthly, Tiers: centTiers(BaseFeatures)},
			Credits:     []plan.CreditPool{{Key: creditUsage, Amount: types.USD(cloudBaseCents), FeatureKeys: slices.Clone(BaseFeatures)}},
		},
		{
			Name:        "AI add-on",
			Slug:        PlanAI,
			Description: "AI on Loomplane's models, with $10.00 of model cost included every month.",
			Currency:    currency,
			Status:      plan.StatusActive,
			TrialDays:   0,
			AppID:       appID,
			Features:    meteredFeatures([]string{FeatureAIUSD}),
			Pricing:     &plan.Pricing{BaseAmount: types.USD(aiBaseCents), BillingPeriod: plan.PeriodMonthly, Tiers: centTiers([]string{FeatureAIUSD})},
			Credits:     []plan.CreditPool{{Key: creditAI, Amount: types.USD(aiBaseCents), FeatureKeys: []string{FeatureAIUSD}}},
		},
	}
}

func meteredFeatures(keys []string) []plan.Feature {
	out := make([]plan.Feature, 0, len(keys))
	for _, k := range keys {
		out = append(out, plan.Feature{Key: k, Name: featureNames[k], Type: plan.FeatureMetered, Limit: 0, Period: plan.PeriodMonthly})
	}
	return out
}

func centTiers(keys []string) []plan.PriceTier {
	out := make([]plan.PriceTier, 0, len(keys))
	for _, k := range keys {
		out = append(out, plan.PriceTier{FeatureKey: k, Type: plan.TierGraduated, UpTo: 0, UnitAmount: types.USD(1)})
	}
	return out
}

// SeedCatalog creates or updates both plans to exactly the Global
// Constraints. Running it twice changes nothing.
//
// An existing plan keeps its id, its features' and pricing's ids, its
// payment provider link and its metadata; everything else is put back to
// the catalog. Two replicas seeding at once is safe: the loser of the race
// to create a plan finds the winner's and updates it instead.
func SeedCatalog(ctx context.Context, l *ledger.Ledger, appID string) error {
	if l == nil || appID == "" {
		return errors.New("billing: seeding the catalog needs a ledger and an app id")
	}
	for _, want := range catalog(appID) {
		if err := seedPlan(ctx, l, want); err != nil {
			return fmt.Errorf("billing: seed the %s plan: %w", want.Slug, err)
		}
	}
	return nil
}

func seedPlan(ctx context.Context, l *ledger.Ledger, want *plan.Plan) error {
	have, err := l.GetPlanBySlug(ctx, want.Slug, want.AppID)
	if errors.Is(err, ledger.ErrPlanNotFound) {
		createErr := l.CreatePlan(ctx, want)
		if createErr == nil {
			return nil
		}
		// Another replica may have created it since the read.
		if have, err = l.GetPlanBySlug(ctx, want.Slug, want.AppID); err != nil {
			return errors.Join(createErr, err)
		}
	} else if err != nil {
		return err
	}
	keep(want, have)
	if samePlan(want, have) {
		return nil
	}
	return l.UpdatePlan(ctx, want)
}

// keep copies onto want what the catalog doesn't decide: the ids ledger
// gave the plan, its features and its pricing, their timestamps, the
// payment provider's link and the plan's metadata.
func keep(want, have *plan.Plan) {
	want.ID, want.Entity = have.ID, have.Entity
	want.ProviderID, want.ProviderName, want.Metadata = have.ProviderID, have.ProviderName, have.Metadata
	for i := range want.Features {
		if f := have.FindFeature(want.Features[i].Key); f != nil {
			want.Features[i].ID, want.Features[i].Entity = f.ID, f.Entity
			want.Features[i].CatalogID, want.Features[i].Metadata = f.CatalogID, f.Metadata
		} else {
			want.Features[i].ID = id.NewFeatureID()
		}
	}
	if have.Pricing != nil {
		want.Pricing.ID, want.Pricing.Entity = have.Pricing.ID, have.Pricing.Entity
	} else {
		want.Pricing.ID = id.NewPriceID()
	}
	want.Pricing.PlanID = have.ID
}

// samePlan reports whether have already is want in everything the catalog
// decides.
func samePlan(want, have *plan.Plan) bool {
	return want.Name == have.Name && want.Description == have.Description &&
		want.Currency == have.Currency && want.Status == have.Status && want.TrialDays == have.TrialDays &&
		reflect.DeepEqual(want.Features, have.Features) &&
		reflect.DeepEqual(want.Pricing, have.Pricing) &&
		reflect.DeepEqual(want.Credits, have.Credits)
}
