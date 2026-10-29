//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
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

type budgetClusterCatalog struct {
	provider.ClusterReader
	price float64
}

func (p *budgetClusterCatalog) Options(context.Context, provider.AccountAccess, string) (provider.ClusterOptions, error) {
	return provider.ClusterOptions{CheckedAt: time.Now(), Digest: "current", NodeSizeFacts: []provider.ClusterNodeSize{{ID: "small", HourlyUSD: &p.price}}}, nil
}

type mutableBudgetServer struct {
	*budgetLedgerProvider
	price float64
}

func (p *mutableBudgetServer) ServerOptions(ctx context.Context, c provider.InfrastructureCredential) (provider.ServerOptions, error) {
	options, err := p.budgetLedgerProvider.ServerOptions(ctx, c)
	for i := range options.Sizes {
		options.Sizes[i].HourlyUSD = p.price
	}
	return options, err
}

func TestClusterBudgetStaleBaselineRecovery(t *testing.T) {
	for _, source := range []string{"server", "pending", "envelope"} {
		t.Run(source, func(t *testing.T) {
			f := ledgerFixture(t)
			adapters := cloudinfra.NewAdapters()
			serverCatalog := &mutableBudgetServer{budgetLedgerProvider: &budgetLedgerProvider{&ledgerProvider{f.provider}}, price: 0.000029}
			if err := adapters.RegisterServer("digitalocean", serverCatalog); err != nil {
				t.Fatal(err)
			}
			catalog := &budgetClusterCatalog{price: 0.000029}
			if err := adapters.RegisterCluster("digitalocean", catalog, provider.ClusterDiscovery); err != nil {
				t.Fatal(err)
			}
			f.svc.Adapters = adapters
			p, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
			if err != nil {
				t.Fatal(err)
			}
			p.Revision++
			p.Limits.CPUMillis = 20_000
			if err = f.store.SaveCapacityPolicy(t.Context(), p, 1); err != nil {
				t.Fatal(err)
			}
			request := ledgerPreview(t, f)
			request.Status = "preview"
			request.Input.AcknowledgeUnknownCosts = true
			old := time.Now().Add(-time.Hour)
			price := int64(7)
			identity := "digitalocean/account/old-options/small"
			money := cloudinfra.MoneyEstimate{HourlyMicroUSD: 7, HorizonSeconds: 3600, HorizonMicroUSD: 7, Known: true, CheckedAt: old, PriceDigest: "old-price", Components: []cloudinfra.MoneyComponent{{Name: "nodes:workers", Source: "provider_options", Identity: identity, Quantity: 1, HourlyMicroUSD: &price}}}
			switch source {
			case "server":
				if err = f.store.SaveServer(t.Context(), &cloudinfra.Server{ID: "old-server", OrgID: "org", CloudID: "cloud", Ownership: "managed", Provider: "digitalocean", AccountID: "account", ConnectionID: "infra-account", ResourceID: "old-vm", Status: "ready", Revision: 1, Size: "small", PricedSize: "small", HourlyPriceKnown: true, HourlyUSD: 0.000007, PriceCheckedAt: old}); err != nil {
					t.Fatal(err)
				}
			case "pending", "envelope":
				o := clusterLedgerPreview(t, f, "old-cluster")
				o.Input.PolicyRevision = 2
				o.Input.Spec.Region = "region"
				o.MoneyEstimate = &money
				raw, e := json.Marshal(o)
				if e != nil {
					t.Fatal(e)
				}
				if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET body=$1 WHERE id=$2`, raw, o.ID); err != nil {
					t.Fatal(err)
				}
				if source == "pending" {
					if err = f.store.AcceptClusterOperation(t.Context(), o, 2); err != nil {
						t.Fatal(err)
					}
				} else {
					envelope := cloudinfra.ClusterCapacityEnvelope{ID: "old-envelope", OrgID: "org", CloudID: "cloud", OperationID: o.ID, Provider: "digitalocean", AccountID: "account", Revision: 1, State: "active", Maximum: cloudinfra.ResourceVector{Servers: 1}, Money: money}
					envelope.Money.Components[0].Name = "nodes"
					raw, e = json.Marshal(envelope)
					if e != nil {
						t.Fatal(e)
					}
					if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_cluster_capacity_envelopes(org_id,cloud_id,id,provider,account_id,revision,body) VALUES('org','cloud','old-envelope','digitalocean','account',1,$1)`, raw); err != nil {
						t.Fatal(err)
					}
				}
			}
			p.Revision++
			p.Budget = &cloudinfra.CapacityBudget{MaxHourlyMicroUSD: 1000, MaxHorizonMicroUSD: 1000, HorizonSeconds: 3600}
			if err = f.store.SaveCapacityPolicy(t.Context(), p, 2); err != nil {
				t.Fatal(err)
			}
			request.ID += "-stale"
			if err = f.store.CreatePreview(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if request.BudgetReview.Estimate.CheckedAt.After(old.Add(time.Second)) || request.BudgetReview.Estimate.Known {
				t.Fatal("stale baseline relabeled fresh", request.BudgetReview)
			}
			request.Status = "pending"
			if err = f.store.Accept(t.Context(), request); !errors.Is(err, cloudinfra.ErrConflict) {
				t.Fatal("stale known price authorized", err)
			}
			fresh, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "create", Name: "fresh-review", ConnectionID: "infra-account", CloudRevision: 1, Region: "region", Size: "small", Image: "ubuntu", AcknowledgeUnknownCosts: true})
			if err != nil {
				t.Fatal("reachable baseline refresh failed", err)
			}
			if fresh.BudgetReview.Estimate.HourlyMicroUSD != 58 || time.Since(fresh.BudgetReview.Estimate.CheckedAt) > time.Minute {
				t.Fatal("current tariff not projected", fresh.BudgetReview)
			}
			if fresh.BudgetReview.Digest == request.BudgetReview.Digest {
				t.Fatal("changed tariff retained old review")
			}
			catalog.price = 0.000031
			serverCatalog.price = 0.000031
			if _, err = f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "create", Name: "changed-tariff", ConnectionID: "infra-account", CloudRevision: 1, Region: "region", Size: "small", Image: "ubuntu", AcknowledgeUnknownCosts: true}); err != nil {
				t.Fatal(err)
			}
			fresh.Status = "pending"
			var admission *cloudinfra.CapacityAdmissionError
			if err = f.store.Accept(t.Context(), fresh); !errors.Is(err, cloudinfra.ErrConflict) || errors.As(err, &admission) {
				t.Fatal("changed baseline tariff did not require new semantic review", err)
			}
			if source != "server" {
				persisted, e := f.store.GetClusterOperation(t.Context(), "org", "cloud", "old-cluster")
				if e != nil || persisted.MoneyEstimate.HourlyMicroUSD != 7 || persisted.MoneyEstimate.PriceDigest != "old-price" {
					t.Fatal("refresh rewrote approval", persisted, e)
				}
			}
		})
	}
}

func TestClusterBudgetProjectedCategoriesAndPhysicalOverlap(t *testing.T) {
	f := ledgerFixture(t)
	request := ledgerPreview(t, f)
	request.Status = "preview"
	request.Input.AcknowledgeUnknownCosts = true
	now := time.Now()
	price := int64(29)
	request.MoneyEstimate = &cloudinfra.MoneyEstimate{CheckedAt: now, Components: []cloudinfra.MoneyComponent{
		{Name: "nodes:private-node", Source: "provider_options", Identity: "private-node", Quantity: 1, HourlyMicroUSD: &price},
		{Name: "network_egress:private-network", Source: "provider_options", Identity: "private-network", Quantity: 1},
		{Name: "persistent_storage:private-volume", Source: "provider_options", Identity: "private-volume", Quantity: 1},
		{Name: "control_plane:private-cluster", Source: "provider_options", Identity: "private-cluster", Quantity: 1},
	}}
	if err := f.store.SaveServer(t.Context(), &cloudinfra.Server{ID: "attributed", OrgID: "org", CloudID: "cloud", Ownership: "managed", Provider: "digitalocean", AccountID: "account", ConnectionID: "infra-account", ResourceID: "shared-vm", Status: "ready", Revision: 1, Size: "small", PricedSize: "small", HourlyPriceKnown: true, HourlyUSD: 0.000029, PriceCheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	envelope := cloudinfra.ClusterCapacityEnvelope{ID: "overlap", OrgID: "org", CloudID: "cloud", Provider: "digitalocean", AccountID: "account", ResourceID: "cluster", Ref: provider.ClusterRef{ID: "physical"}, Revision: 1, State: "active", CheckedAt: now, NodeIDs: []string{"shared-vm"}, Maximum: cloudinfra.ResourceVector{Servers: 3}, Consumed: cloudinfra.ResourceVector{Servers: 1}, Money: cloudinfra.MoneyEstimate{CheckedAt: now, Components: []cloudinfra.MoneyComponent{{Name: "nodes", Source: "provider_options", Identity: "size", Quantity: 3, HourlyMicroUSD: &price}}}}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_cluster_capacity_envelopes(org_id,cloud_id,id,provider,account_id,revision,body) VALUES('org','cloud','overlap','digitalocean','account',1,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	p, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	p.Revision++
	p.Budget = &cloudinfra.CapacityBudget{MaxHourlyMicroUSD: 1000, MaxHorizonMicroUSD: 1000, HorizonSeconds: 3600}
	if err = f.store.SaveCapacityPolicy(t.Context(), p, 1); err != nil {
		t.Fatal(err)
	}
	request.ID += "-overlap"
	if err = f.store.CreatePreview(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if request.BudgetReview.Estimate.HourlyMicroUSD != 116 {
		t.Fatal("physical VM charged twice or envelope remainder lost", request.BudgetReview)
	}
	categories := map[string]bool{}
	for _, c := range request.BudgetReview.Estimate.Components {
		categories[c.Category] = true
	}
	for _, kind := range []string{"nodes", "network_egress", "persistent_storage", "control_plane"} {
		if !categories[kind] {
			t.Fatal("cost category hidden", kind)
		}
	}
	raw, err = json.Marshal(request.BudgetReview)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "shared-vm") {
		t.Fatal("private source identity leaked", string(raw))
	}
}

func TestClusterBudgetRetainedPriceProvenance(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "expired"}[expired], func(t *testing.T) {
			f := ledgerFixture(t)
			adapters := cloudinfra.NewAdapters()
			if err := adapters.RegisterServer("digitalocean", &budgetLedgerProvider{&ledgerProvider{f.provider}}); err != nil {
				t.Fatal(err)
			}
			f.svc.Adapters = adapters
			now := time.Now()
			checked := now
			if expired {
				checked = now.Add(-time.Hour)
			}
			price := int64(7)
			r := &cloudinfra.ManagedResource{ID: "retired", OrgID: "org", CloudID: "cloud", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: "physical", Incarnation: "birth", Ownership: "managed", Status: "deleted", Revision: 1, DeletedAt: &now, RetainedPriceCheckedAt: checked, RetainedResources: []provider.AssociatedResource{{ID: "private-retained", Kind: "volume", EstimatedHourlyMicroUSD: &price}}}
			if _, err := f.store.ClaimManagedResource(t.Context(), r, 0); err != nil {
				t.Fatal(err)
			}
			p, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
			if err != nil {
				t.Fatal(err)
			}
			p.Revision++
			p.Budget = &cloudinfra.CapacityBudget{MaxHourlyMicroUSD: 1000, MaxHorizonMicroUSD: 1000, HorizonSeconds: 3600}
			if err = f.store.SaveCapacityPolicy(t.Context(), p, 1); err != nil {
				t.Fatal(err)
			}
			current, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "create", Name: "retained-review", ConnectionID: "infra-account", CloudRevision: 1, Region: "region", Size: "small", Image: "ubuntu", AcknowledgeUnknownCosts: true})
			if err != nil {
				t.Fatal(err)
			}
			want := int64(36)
			if expired {
				want = 29
			}
			if current.BudgetReview.Estimate.HourlyMicroUSD != want {
				t.Fatal("retained charge ignored its source timestamp", current.BudgetReview)
			}
			found := false
			for _, c := range current.BudgetReview.Estimate.Components {
				if c.Source == "retained" {
					found = c.Category == "persistent_storage" && (c.HourlyMicroUSD == nil) == expired
				}
			}
			if !found {
				t.Fatal("retained cost category or freshness missing")
			}
			current.Status = "pending"
			if err = f.store.Accept(t.Context(), current); err != nil {
				t.Fatal("explicit retained cost review has no recovery", err)
			}
		})
	}
}
