//go:build integration && cloud

package migrations

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/grove/migrate"
)

var billingTables = []string{
	"loomplane_billing_accounts",
	"loomplane_billing_ai_claims",
	"loomplane_billing_carry",
	"loomplane_billing_closes",
	"loomplane_billing_credits",
	"loomplane_billing_invoices",
	"loomplane_billing_pushes",
	"loomplane_billing_rates",
	"loomplane_billing_refunds",
	"loomplane_billing_usage",
}

// billingMigration is the billing group's one migration.
func billingMigration(t *testing.T) *migrate.Migration {
	t.Helper()
	ms := billingGroup().Migrations()
	if len(ms) != 1 || ms[0].Name != "billing" {
		t.Fatalf("billing group holds %d migrations, want the one named billing", len(ms))
	}
	return ms[0]
}

func TestBillingMigrationCreatesTablesAndSeedsZeroRates(t *testing.T) {
	conn, ex := scratch(t)
	ctx := context.Background()
	if _, err := migrate.NewOrchestrator(ex, All()...).Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	const tables = `SELECT table_name::text FROM information_schema.tables
		WHERE table_schema='public' AND table_name LIKE 'loomplane\_billing\_%' ORDER BY table_name`
	if got := strs(t, conn, tables); !slices.Equal(got, billingTables) {
		t.Fatalf("tables %v\nwant %v", got, billingTables)
	}
	const rates = `SELECT feature||'='||micro_usd_per_unit FROM loomplane_billing_rates ORDER BY feature`
	want := []string{
		"ai_usd=0", "build_minutes=0", "compute_gb_ram_hours=0",
		"compute_vcpu_hours=0", "egress_gb=0", "storage_gb_months=0",
	}
	if got := strs(t, conn, rates); !slices.Equal(got, want) {
		t.Fatalf("rates %v, want %v", got, want)
	}

	// The gate's past-due check has a partial index of its own, and its
	// query (internal/billing/accounts.go) uses it.
	const unpaidIndex = `SELECT indexdef FROM pg_indexes WHERE indexname='loomplane_billing_invoices_unpaid_failed'`
	if got := strs(t, conn, unpaidIndex); len(got) != 1 ||
		!strings.Contains(got[0], "(org_id) WHERE ((paid_at IS NULL) AND (payment_failed_at IS NOT NULL))") {
		t.Fatalf("unpaid invoice index %v", got)
	}
	exec(t, conn, `SET enable_seqscan = off`)
	plan := strings.Join(strs(t, conn, `EXPLAIN SELECT EXISTS (SELECT 1 FROM loomplane_billing_invoices i
		WHERE i.org_id = 'org_1' AND i.paid_at IS NULL AND i.payment_failed_at IS NOT NULL
			AND i.payment_failed_at <= now() - interval '72 hours')`), "\n")
	exec(t, conn, `RESET enable_seqscan`)
	if !strings.Contains(plan, "loomplane_billing_invoices_unpaid_failed") {
		t.Fatalf("the past-due check doesn't use the index:\n%s", plan)
	}

	// A second run changes nothing, and keeps a rate that was set.
	exec(t, conn, `UPDATE loomplane_billing_rates SET micro_usd_per_unit=7 WHERE feature='egress_gb'`)
	if err := billingMigration(t).Up(ctx, ex); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if got := strs(t, conn, `SELECT micro_usd_per_unit::text FROM loomplane_billing_rates WHERE feature='egress_gb'`); !slices.Equal(got, []string{"7"}) {
		t.Fatalf("rerun reset the rate: %v", got)
	}

	// Down drops every billing table and nothing else; up brings them back.
	if err := billingMigration(t).Down(ctx, ex); err != nil {
		t.Fatalf("down: %v", err)
	}
	if got := strs(t, conn, tables); len(got) != 0 {
		t.Fatalf("down left %v", got)
	}
	if got := strs(t, conn, unpaidIndex); len(got) != 0 {
		t.Fatalf("down left the unpaid invoice index: %v", got)
	}
	if got := strs(t, conn, `SELECT to_regclass('ledger_plans')::text`); !slices.Equal(got, []string{"ledger_plans"}) {
		t.Fatalf("down touched ledger's tables: %v", got)
	}
	if err := billingMigration(t).Up(ctx, ex); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if got := strs(t, conn, tables); !slices.Equal(got, billingTables) {
		t.Fatalf("up again made %v", got)
	}
	if got := strs(t, conn, unpaidIndex); len(got) != 1 {
		t.Fatalf("up again made no unpaid invoice index: %v", got)
	}
}

func TestBillingMigrationRefusesBadAmounts(t *testing.T) {
	conn, ex := scratch(t)
	ctx := context.Background()
	if _, err := migrate.NewOrchestrator(ex, All()...).Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, stmt := range []string{
		`INSERT INTO loomplane_billing_accounts (org_id, spending_limit_cents) VALUES ('org_neg', -1)`,
		`INSERT INTO loomplane_billing_rates (feature, micro_usd_per_unit) VALUES ('neg', -1)`,
		`INSERT INTO loomplane_billing_pushes (id, org_id, feature, period, cents, at) VALUES ('p', 'o', 'f', '2026-11-01', -1, now())`,
		`INSERT INTO loomplane_billing_carry (org_id, feature, period, micro_usd_remainder) VALUES ('o', 'f', '2026-11-01', 10000)`,
		`INSERT INTO loomplane_billing_carry (org_id, feature, period, micro_usd_remainder) VALUES ('o', 'f', '2026-11-01', -1)`,
		`INSERT INTO loomplane_billing_credits (id, org_id, subscription_id, coupon_id, cents, granted_by) VALUES ('c', 'o', 's', 'k', 0, 'rex')`,
		`INSERT INTO loomplane_billing_refunds (refund_key, invoice_id, org_id, full_refund, refunded_by) VALUES ('', 'i', 'o', true, 'rex')`,
	} {
		_, err := conn.Exec(ctx, stmt)
		requireSQLState(t, err, "23514")
	}
	// A workspace with no limit, and one at zero, are both allowed.
	exec(t, conn,
		`INSERT INTO loomplane_billing_accounts (org_id) VALUES ('org_none')`,
		`INSERT INTO loomplane_billing_accounts (org_id, spending_limit_cents) VALUES ('org_zero', 0)`,
		`INSERT INTO loomplane_billing_invoices (invoice_id, org_id, subscription_id, plan, period_start) VALUES ('inv_1', 'o', 'sub_1', 'cloud', '2026-11-01T00:00:00Z')`,
	)
	// One invoice per subscription and period.
	_, err := conn.Exec(ctx, `INSERT INTO loomplane_billing_invoices (invoice_id, org_id, subscription_id, plan, period_start) VALUES ('inv_2', 'o', 'sub_1', 'cloud', '2026-11-01T00:00:00Z')`)
	requireSQLState(t, err, "23505")
}
