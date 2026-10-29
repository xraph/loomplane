//go:build integration

package integration

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/provider"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
)

func TestClusterBudgetReviewSurvivesRestart(t *testing.T) {
	db := managedClusterDB(t)
	store, _ := managedClusterStore(t, db)
	now := time.Now().UTC()
	op := &cloudinfra.ClusterOperation{ID: "money-review", OrgID: "org", CloudID: "cloud", Kind: "create", SchemaVersion: 1, Status: "preview", Revision: 1, Digest: "review", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute), Input: cloudinfra.ClusterInput{PriceDigest: "price"}, MoneyEstimate: &cloudinfra.MoneyEstimate{HourlyMicroUSD: 29, HorizonSeconds: 3600, HorizonMicroUSD: 29, Known: false, MissingComponents: []string{"storage"}, PriceDigest: "price", CheckedAt: now}}
	if checkErr := store.CreateClusterPreview(t.Context(), op); checkErr != nil {
		t.Fatal(checkErr)
	}
	connection := pgdriver.New()
	if checkErr := connection.Open(t.Context(), db.Config().ConnString()); checkErr != nil {
		t.Fatal(checkErr)
	}
	t.Cleanup(func() { _ = connection.Close() })
	restarted := cloudinfra.NewPgStore(connection)
	saved, err := restarted.GetClusterOperation(t.Context(), "org", "cloud", op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.MoneyEstimate == nil || saved.MoneyEstimate.HourlyMicroUSD != 29 || saved.MoneyEstimate.Known || len(saved.MoneyEstimate.MissingComponents) != 1 {
		t.Fatalf("lost estimate: %+v", saved.MoneyEstimate)
	}
	saved.Revision++
	saved.MoneyEstimate.HourlyMicroUSD = 0
	if checkErr := restarted.SaveClusterCheckpoint(t.Context(), saved, 1); !errors.Is(checkErr, cloudinfra.ErrConflict) {
		t.Fatalf("checkpoint rewrote reviewed price: %v", checkErr)
	}
}

type budgetLedgerProvider struct{ *ledgerProvider }

func (p *budgetLedgerProvider) ServerOptions(ctx context.Context, c provider.InfrastructureCredential) (provider.ServerOptions, error) {
	options, err := p.ledgerProvider.ServerOptions(ctx, c)
	for i := range options.Sizes {
		options.Sizes[i].HourlyUSD = 0.000029
		options.Sizes[i].HourlyPriceKnown = true
	}
	return options, err
}
func TestClusterBudgetSharedBaseline(t *testing.T) {
	f := ledgerFixture(t)
	adapters := cloudinfra.NewAdapters()
	if checkErr := adapters.RegisterServer("digitalocean", &budgetLedgerProvider{&ledgerProvider{f.provider}}); checkErr != nil {
		t.Fatal(checkErr)
	}
	f.svc.Adapters = adapters
	p, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	p.Revision++
	p.Limits.CPUMillis = 20_000
	p.Budget = &cloudinfra.CapacityBudget{MaxHourlyMicroUSD: 40, MaxHorizonMicroUSD: 40, HorizonSeconds: 3600}
	if err = f.store.SaveCapacityPolicy(t.Context(), p, 1); err != nil {
		t.Fatal(err)
	}
	preview := func(ack bool) *cloudinfra.Operation {
		o, e := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "create", Name: "budget", ConnectionID: "infra-account", CloudRevision: 1, Region: "region", Size: "small", Image: "ubuntu", AcknowledgeUnknownCosts: ack})
		if e != nil {
			t.Fatal(e)
		}
		o.Status = "pending"
		return o
	}
	unknown := preview(false)
	if unknown.BudgetReview == nil || unknown.BudgetReview.Estimate.Known || unknown.BudgetReview.Estimate.HourlyMicroUSD != 29 {
		t.Fatalf("partial estimate: %+v", unknown.BudgetReview)
	}
	if err = f.store.Accept(t.Context(), unknown); err == nil {
		t.Fatal("automation accepted unknown account baseline")
	}
	first, oldReview := preview(true), preview(true)
	if err = f.store.Accept(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Accept(t.Context(), oldReview); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("changed baseline reused manual acknowledgement: %v", err)
	}
	overBudget := preview(true)
	if overBudget.BudgetReview.Estimate.HourlyMicroUSD != 58 {
		t.Fatal("pending charge missing", overBudget.BudgetReview)
	}
	if err = f.store.Accept(t.Context(), overBudget); err == nil {
		t.Fatal("known subtotal exceeded budget")
	}
}

func TestClusterBudgetStandingMaximumRetry(t *testing.T) {
	f := ledgerFixture(t)
	o := clusterLedgerPreview(t, f, "seed")
	p, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	p.Revision++
	p.Limits.CPUMillis = 6000
	p.Budget = &cloudinfra.CapacityBudget{MaxHourlyMicroUSD: 100, MaxHorizonMicroUSD: 100, HorizonSeconds: 3600}
	if err = f.store.SaveCapacityPolicy(t.Context(), p, 1); err != nil {
		t.Fatal(err)
	}
	o.ID = "standing-budget"
	o.Digest = "standing-budget"
	o.Input.PolicyRevision = 2
	unit := o.CapacityTarget
	maximum := cloudinfra.ResourceVector{Servers: 3, CPUMillis: 6000, MemoryBytes: 6 << 30, StorageBytes: 60_000_000_000}
	price := int64(29)
	o.MoneyEstimate = &cloudinfra.MoneyEstimate{HourlyMicroUSD: 29, HorizonSeconds: 3600, HorizonMicroUSD: 29, Known: true, PriceDigest: "request-price", CheckedAt: time.Now(), Components: []cloudinfra.MoneyComponent{{Name: "nodes:workers", Source: "provider_options", Identity: "size", Quantity: 1, HourlyMicroUSD: &price}}}
	money := *o.MoneyEstimate
	money.HourlyMicroUSD = 87
	money.HorizonMicroUSD = 87
	money.PriceDigest = "maximum-price"
	money.Components = []cloudinfra.MoneyComponent{{Name: "nodes", Source: "provider_options", Identity: "size", Quantity: 3, HourlyMicroUSD: &price}}
	o.Input.EnvelopeReview = &cloudinfra.ClusterEnvelopeReview{Pools: []cloudinfra.ClusterPoolEnvelopeReview{{Key: "workers", RequestDigest: "pool-request", Maximum: maximum, Initial: unit, Unit: unit, Money: money}}}
	if err = f.store.CreateClusterPreview(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	if o.Input.BudgetReview == nil || o.Input.BudgetReview.Estimate.HourlyMicroUSD != 87 {
		t.Fatal("standing maximum missing from reviewed budget", o.Input.BudgetReview)
	}
	if err = f.store.AcceptClusterOperation(t.Context(), o, 2); err != nil {
		t.Fatal(err)
	}
	leased, err := f.store.LeaseClusterOperation(t.Context(), "org", o.ID, "budget-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.AttemptClusterCreate(t.Context(), leased); err != nil {
		t.Fatal("own reservation changed scoped review before effect", err)
	}
}

func TestClusterBudgetOverflowAdmission(t *testing.T) {
	f := ledgerFixture(t)
	adapters := cloudinfra.NewAdapters()
	if checkErr := adapters.RegisterServer("digitalocean", &budgetLedgerProvider{&ledgerProvider{f.provider}}); checkErr != nil {
		t.Fatal(checkErr)
	}
	f.svc.Adapters = adapters
	p, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	p.Revision++
	p.Budget = &cloudinfra.CapacityBudget{MaxHourlyMicroUSD: math.MaxInt64, MaxHorizonMicroUSD: math.MaxInt64, HorizonSeconds: math.MaxInt64}
	if err = f.store.SaveCapacityPolicy(t.Context(), p, 1); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "create", Name: "overflow", ConnectionID: "infra-account", CloudRevision: 1, Region: "region", Size: "small", Image: "ubuntu", AcknowledgeUnknownCosts: true})
	if !errors.Is(err, cloudinfra.ErrInvalid) {
		t.Fatal("overflowing persisted horizon admitted", err)
	}
}

func TestClusterBudgetCloudAttribution(t *testing.T) {
	f := ledgerFixture(t)
	if checkErr := cloudprofiles.NewPgStore(ledgerDB(t, f)).CreateProfile(t.Context(), &cloudprofiles.Profile{ID: "peer", OrgID: "org", ProjectID: "proj", Name: "Peer", Revision: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}); checkErr != nil {
		t.Fatal(checkErr)
	}
	if checkErr := f.store.SaveServer(t.Context(), &cloudinfra.Server{ID: "peer-cost", Ownership: "managed", OrgID: "org", CloudID: "peer", Provider: "digitalocean", AccountID: "account", ConnectionID: "infra-account", ResourceID: "peer-vm", Status: "ready", Revision: 1, Size: "large", PricedSize: "large", HourlyUSD: 100, HourlyPriceKnown: true}); checkErr != nil {
		t.Fatal(checkErr)
	}
	adapters := cloudinfra.NewAdapters()
	if checkErr := adapters.RegisterServer("digitalocean", &budgetLedgerProvider{&ledgerProvider{f.provider}}); checkErr != nil {
		t.Fatal(checkErr)
	}
	f.svc.Adapters = adapters
	p, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	p.Revision++
	p.Budget = &cloudinfra.CapacityBudget{MaxHourlyMicroUSD: 40, MaxHorizonMicroUSD: 40, HorizonSeconds: 3600}
	if err = f.store.SaveCapacityPolicy(t.Context(), p, 1); err != nil {
		t.Fatal(err)
	}
	o, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "create", Name: "local", ConnectionID: "infra-account", CloudRevision: 1, Region: "region", Size: "small", Image: "ubuntu", AcknowledgeUnknownCosts: true})
	if err != nil {
		t.Fatal(err)
	}
	if o.BudgetReview == nil || o.BudgetReview.Estimate.HourlyMicroUSD != 29 {
		t.Fatal("peer Cloud charge entered local subtotal", o.BudgetReview)
	}
}
