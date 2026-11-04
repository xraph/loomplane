//go:build cloud

package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// billing holds Loomplane Cloud's billing tables. It runs after Loomplane's
// own group and ledger's, and only a -tags cloud build has it.
var billing = migrate.NewGroup("loomplane_billing", migrate.DependsOn(GroupName, "ledger"))

// billingGroup copies the group's migrations, as Group does.
func billingGroup() *migrate.Group {
	g := migrate.NewGroup("loomplane_billing", migrate.DependsOn(GroupName, "ledger"))
	for _, m := range billing.Migrations() {
		copied := *m
		g.MustRegister(&copied)
	}
	return g
}

// Every table billing keeps, made at once so later work adds nothing to the
// schema.
//
// loomplane_billing_accounts is one row per workspace: its Stripe customer,
// whether a card is saved, its two ledger subscriptions (cloud and the AI
// add-on), when the add-on ends, and its spending limit. limit_epoch counts
// the times the workspace was limited, so each restored notice has a key of
// its own.
//
// loomplane_billing_rates is the price of one native unit of each feature in
// micro-USD. Every rate starts at 0, so nothing is charged until one is set.
//
// loomplane_billing_usage is each usage event, priced when it is recorded and
// keeping that price. push_id names the push that sent it to ledger.
//
// loomplane_billing_pushes is what each push sent to ledger, in cents, per
// workspace, feature and month. sent_at is empty until ledger accepted it.
//
// loomplane_billing_carry keeps what is left under one cent between pushes.
//
// loomplane_billing_ai_claims is each platform-key AI request, priced from
// nexus's own cost record once that record is found.
//
// loomplane_billing_invoices, loomplane_billing_credits and
// loomplane_billing_refunds record the invoices sent, the credits granted
// and the refunds made. A refund's sent_at is set once Stripe made it; one
// without is sent again, under its own key, when it is asked for again.
// Unpaid invoices whose payment failed are indexed by workspace, for the
// gate's past-due check. voided_at marks an invoice voided in ledger,
// which is never sent and never made again.
//
// loomplane_billing_closes is one row per month whose close finished, so a
// close that stopped part way is finished by the next hourly run.
func init() {
	billing.Add(
		func(ctx context.Context, db *migrate.DB) error {
			for _, stmt := range []string{
				`CREATE TABLE IF NOT EXISTS loomplane_billing_accounts (
 org_id text PRIMARY KEY,
 stripe_customer_id text NOT NULL DEFAULT '',
 card_ready boolean NOT NULL DEFAULT false,
 cloud_subscription_id text NOT NULL DEFAULT '',
 ai_subscription_id text NOT NULL DEFAULT '',
 ai_ends_at timestamptz NULL,
 spending_limit_cents bigint NULL CHECK (spending_limit_cents IS NULL OR spending_limit_cents >= 0),
 limited_period date NULL,
 limit_epoch bigint NOT NULL DEFAULT 0,
 updated_at timestamptz NOT NULL DEFAULT now())`,
				`CREATE TABLE IF NOT EXISTS loomplane_billing_rates (
 feature text PRIMARY KEY,
 micro_usd_per_unit bigint NOT NULL CHECK (micro_usd_per_unit >= 0),
 updated_at timestamptz NOT NULL DEFAULT now(),
 updated_by text NOT NULL DEFAULT '')`,
				`INSERT INTO loomplane_billing_rates(feature, micro_usd_per_unit) VALUES
 ('build_minutes',0),('compute_vcpu_hours',0),('compute_gb_ram_hours',0),
 ('storage_gb_months',0),('egress_gb',0),('ai_usd',0)
 ON CONFLICT (feature) DO NOTHING`,
				`CREATE TABLE IF NOT EXISTS loomplane_billing_usage (
 id text PRIMARY KEY,
 org_id text NOT NULL,
 feature text NOT NULL,
 quantity double precision NOT NULL,
 rate_micro_usd bigint NOT NULL,
 micro_usd bigint NOT NULL,
 occurred_at timestamptz NOT NULL,
 resource_ref text NOT NULL,
 push_id text NULL,
 recorded_at timestamptz NOT NULL DEFAULT now())`,
				`CREATE INDEX IF NOT EXISTS loomplane_billing_usage_unpushed ON loomplane_billing_usage (org_id, feature) WHERE push_id IS NULL`,
				`CREATE INDEX IF NOT EXISTS loomplane_billing_usage_period ON loomplane_billing_usage (org_id, occurred_at)`,
				`CREATE TABLE IF NOT EXISTS loomplane_billing_pushes (
 id text PRIMARY KEY,
 org_id text NOT NULL,
 feature text NOT NULL,
 period date NOT NULL,
 cents bigint NOT NULL CHECK (cents >= 0),
 at timestamptz NOT NULL,
 sent_at timestamptz NULL,
 created_at timestamptz NOT NULL DEFAULT now())`,
				`CREATE INDEX IF NOT EXISTS loomplane_billing_pushes_unsent ON loomplane_billing_pushes (created_at) WHERE sent_at IS NULL`,
				`CREATE TABLE IF NOT EXISTS loomplane_billing_carry (
 org_id text NOT NULL, feature text NOT NULL, period date NOT NULL,
 micro_usd_remainder bigint NOT NULL CHECK (micro_usd_remainder >= 0 AND micro_usd_remainder < 10000),
 PRIMARY KEY (org_id, feature, period))`,
				`CREATE TABLE IF NOT EXISTS loomplane_billing_ai_claims (
 request_id text PRIMARY KEY,
 org_id text NOT NULL,
 nexus_tenant text NOT NULL,
 claimed_at timestamptz NOT NULL,
 usage_id text NULL,
 settled_at timestamptz NULL)`,
				`CREATE INDEX IF NOT EXISTS loomplane_billing_ai_claims_open ON loomplane_billing_ai_claims (nexus_tenant, claimed_at) WHERE settled_at IS NULL`,
				`CREATE TABLE IF NOT EXISTS loomplane_billing_invoices (
 invoice_id text PRIMARY KEY,
 org_id text NOT NULL,
 subscription_id text NOT NULL,
 plan text NOT NULL,
 period_start timestamptz NOT NULL,
 finalized_at timestamptz NULL,
 provider_id text NOT NULL DEFAULT '',
 payment_failed_at timestamptz NULL,
 paid_at timestamptz NULL,
 voided_at timestamptz NULL,
 UNIQUE (subscription_id, period_start))`,
				// The billing gate asks on every deploy and build whether a workspace
				// has an unpaid invoice whose payment failed; this answers it.
				`CREATE INDEX IF NOT EXISTS loomplane_billing_invoices_unpaid_failed ON loomplane_billing_invoices (org_id)
 WHERE paid_at IS NULL AND payment_failed_at IS NOT NULL`,
				`CREATE TABLE IF NOT EXISTS loomplane_billing_credits (
 id text PRIMARY KEY,
 org_id text NOT NULL,
 subscription_id text NOT NULL,
 coupon_id text NOT NULL,
 cents bigint NOT NULL CHECK (cents > 0),
 note text NOT NULL DEFAULT '',
 granted_by text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 invoice_id text NULL,
 deleted_at timestamptz NULL)`,
				`CREATE TABLE IF NOT EXISTS loomplane_billing_refunds (
 refund_key text PRIMARY KEY CHECK (length(refund_key) BETWEEN 1 AND 200),
 invoice_id text NOT NULL,
 org_id text NOT NULL,
 cents bigint NULL,
 full_refund boolean NOT NULL,
 refunded_by text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 sent_at timestamptz NULL)`,
				`CREATE TABLE IF NOT EXISTS loomplane_billing_closes (
 period date PRIMARY KEY,
 closed_at timestamptz NOT NULL)`,
			} {
				if _, err := db.Exec(ctx, stmt); err != nil {
					return err
				}
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			for _, table := range []string{
				"loomplane_billing_closes",
				"loomplane_billing_refunds",
				"loomplane_billing_credits",
				"loomplane_billing_invoices",
				"loomplane_billing_ai_claims",
				"loomplane_billing_carry",
				"loomplane_billing_pushes",
				"loomplane_billing_usage",
				"loomplane_billing_rates",
				"loomplane_billing_accounts",
			} {
				if err := db.NewDropTable(table).IfExists().Exec(ctx); err != nil {
					return err
				}
			}
			return nil
		},
	)
}
