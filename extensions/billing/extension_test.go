//go:build cloud

package billing

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/dispatch/engine"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	_ "github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"
	"github.com/xraph/ledger"
	"github.com/xraph/ledger/provider/stripe"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/billing"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/instance"
	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/platformcloud"
)

const testAppID = "aapp_test"

// scratchDB is a database of its own with every migration applied, the
// cloud groups included, dropped when the test ends.
func scratchDB(t *testing.T) (*grove.DB, *pgdriver.PgDB) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the billing tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := pgx.Identifier{"billingext_" + time.Now().Format("150405.000000000")}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name[1:len(name)-1]
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	exec, err := migrate.NewExecutorFor(pg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = migrate.NewOrchestrator(exec, migrations.All()...).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := grove.Open(pg)
	if err != nil {
		t.Fatal(err)
	}
	return db, pg
}

// testContainer is a forge container holding the values identity and
// planning provide (the database, the ledger, its Stripe provider,
// billing's config and platform Cloud's service), over scratchDB.
func testContainer(t *testing.T) forge.Container {
	t.Helper()
	db, pg := scratchDB(t)
	cfg := billing.Config{StripeSecretKey: "sk_test_x", StripeWebhookSecret: "whsec_x", PublicURL: "https://cloud.example"}
	l, p, err := billing.NewLedger(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := forge.NewContainer()
	if err = errors.Join(
		forge.ProvideValue(c, db),
		forge.ProvideValue(c, l),
		forge.ProvideValue(c, p),
		forge.ProvideValue(c, cfg),
		forge.ProvideValue(c, platformcloud.NewService(platformcloud.NewPgStore(pg), instance.New(pg), nil)),
	); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRegisterProvidesTheGateAndMeter(t *testing.T) {
	c := testContainer(t) // a forge container holding the values identity and planning provide, over scratchDB
	e := New(config.Config{}, func() *engine.Engine { return nil })
	if err := e.provide(c); err != nil { // the part of Register that builds and provides
		t.Fatal(err)
	}
	if !forge.HasType[metering.Gate](c) || !forge.HasType[metering.Meter](c) {
		t.Fatal("billing didn't provide its gate and meter")
	}
	g, m, err := metering.Resolve(c)
	if err != nil || g == metering.AllowAll || m == metering.Discard {
		t.Fatalf("Resolve fell back: %v %v %v", g, m, err)
	}
}

// The gate billing provides is the one that refuses a workspace with no
// card, and lets it run once the card is saved.
func TestRegisterProvidesTheGateThatNeedsACard(t *testing.T) {
	c := testContainer(t)
	eng, _ := testEngine(t)
	e := New(config.Config{Identity: config.IdentityConfig{AppID: testAppID}}, func() *engine.Engine { return eng })
	if err := e.register(c); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	g, _, err := metering.Resolve(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = g.CanRun(ctx, "org_1", metering.KindDeploy, false); !errors.Is(err, metering.ErrBlocked) {
		t.Fatalf("no card: %v", err)
	}
	if err = e.svc.MarkCardReady(ctx, "org_1"); err != nil {
		t.Fatal(err)
	}
	if err = g.CanRun(ctx, "org_1", metering.KindDeploy, false); err != nil {
		t.Fatalf("with a card: %v", err)
	}
}

func TestStartSeedsTheCatalog(t *testing.T) {
	c := testContainer(t)
	eng, _ := testEngine(t)
	e := New(config.Config{Identity: config.IdentityConfig{AppID: testAppID}}, func() *engine.Engine { return eng })
	if err := e.register(c); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	l, _ := forge.InjectType[*ledger.Ledger](c)
	for _, slug := range []string{billing.PlanCloud, billing.PlanAI} {
		if _, err := l.GetPlanBySlug(ctx, slug, testAppID); err != nil {
			t.Fatalf("plan %s: %v", slug, err)
		}
	}
}

func TestStartFailsWhenTheCatalogCantBeSeeded(t *testing.T) {
	c := testContainer(t)
	eng, _ := testEngine(t)
	e := New(config.Config{}, func() *engine.Engine { return eng }) // no app id
	if err := e.register(c); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(t.Context()); err == nil {
		t.Fatal("started with no catalog")
	}
}

func TestProvideNamesWhatIsMissing(t *testing.T) {
	full := testContainer(t)
	db, _ := forge.InjectType[*grove.DB](full)
	l, _ := forge.InjectType[*ledger.Ledger](full)
	p, _ := forge.InjectType[*stripe.Provider](full)
	cfg, _ := forge.InjectType[billing.Config](full)
	pc, _ := forge.InjectType[*platformcloud.Service](full)
	values := []struct {
		missing string
		provide func(forge.Container) error
	}{
		{"grove", func(c forge.Container) error { return forge.ProvideValue(c, db) }},
		{"ledger", func(c forge.Container) error { return forge.ProvideValue(c, l) }},
		{"Stripe", func(c forge.Container) error { return forge.ProvideValue(c, p) }},
		{"config", func(c forge.Container) error { return forge.ProvideValue(c, cfg) }},
		{"platform Cloud", func(c forge.Container) error { return forge.ProvideValue(c, pc) }},
	}
	for skip := range values {
		c := forge.NewContainer()
		for i, v := range values {
			if i != skip {
				if err := v.provide(c); err != nil {
					t.Fatal(err)
				}
			}
		}
		err := New(config.Config{}, func() *engine.Engine { return nil }).provide(c)
		if err == nil || !strings.Contains(err.Error(), values[skip].missing) {
			t.Errorf("without %s: %v", values[skip].missing, err)
		}
		if forge.HasType[metering.Gate](c) {
			t.Errorf("without %s the gate was provided", values[skip].missing)
		}
	}
}
