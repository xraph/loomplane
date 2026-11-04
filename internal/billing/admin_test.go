//go:build cloud

package billing

import (
	"testing"

	"github.com/xraph/loomplane/internal/platformcloud"
)

func TestOverview_PlansAndModeNeverTheKey(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	for key, want := range map[string]StripeMode{
		"sk_live_abc": StripeLive, "rk_live_abc": StripeLive, "sk_test_abc": StripeTest, "": StripeTest,
	} {
		s.cfg.StripeSecretKey = key
		o, err := s.Overview(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if o.StripeMode != want {
			t.Errorf("%q: %s, want %s", key, o.StripeMode, want)
		}
		if len(o.Plans) != 2 || o.Plans[0] != (PlanPrice{Slug: PlanCloud, BaseCents: 500, CreditCents: 500}) ||
			o.Plans[1] != (PlanPrice{Slug: PlanAI, BaseCents: 1000, CreditCents: 1000}) {
			t.Fatalf("plans %+v", o.Plans)
		}
	}
}

func TestWorkspaces_StatusAndThisMonth(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	seedWorkspace(t, pg, "org_1", "usr_1", "ada@example.com", "Acme")
	seedWorkspace(t, pg, "org_2", "usr_2", "bob@example.com", "Bolt")
	for _, q := range []string{
		`INSERT INTO authsome_apps(id,name,slug) VALUES('aapp_other','Other','other')`,
		`INSERT INTO authsome_environments(id,app_id,name,slug,type,is_default) VALUES('env_other','aapp_other','Production','production','production',true)`,
		`INSERT INTO authsome_users(id,app_id,env_id,email,first_name,last_name) VALUES('usr_x','aapp_other','env_other','x@example.com','','')`,
		`INSERT INTO authsome_organizations(id,app_id,env_id,name,slug,created_by) VALUES('org_x','aapp_other','env_other','Other','other','usr_x')`,
	} {
		if _, err := pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.MarkCardReady(ctx, "org_1")
	_ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex")
	if err := s.Record(ctx, compute("a", 800, at("2026-10-04T10:00:00Z"))); err != nil { // $8.00 of usage
		t.Fatal(err)
	}
	if _, err := s.SetAI(ctx, "org_1", true); err != nil {
		t.Fatal(err)
	}
	// A workspace deleted since it was billed keeps its row.
	_ = s.MarkCardReady(ctx, "org_gone")

	ws, err := s.Workspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]WorkspaceBilling{}
	for _, w := range ws {
		by[w.OrgID] = w
	}
	if len(ws) != 3 {
		t.Fatalf("workspaces %+v", ws)
	}
	// $5 base + $3 over the credit + $10 add-on.
	if w := by["org_1"]; w != (WorkspaceBilling{OrgID: "org_1", Name: "Acme", Status: StatusActive, PeriodTotalCents: 1800, OverageCents: 300, AIEnabled: true}) {
		t.Errorf("org_1 %+v", w)
	}
	if w := by["org_2"]; w != (WorkspaceBilling{OrgID: "org_2", Name: "Bolt", Status: StatusPendingPayment}) {
		t.Errorf("org_2 %+v", w)
	}
	if w := by["org_gone"]; w.Name != "" || w.Status != StatusActive || w.PeriodTotalCents != 500 {
		t.Errorf("org_gone %+v", w)
	}
	if _, ok := by["org_x"]; ok {
		t.Error("another app's workspace is listed")
	}
	// Each one says what Account says.
	for _, org := range []string{"org_1", "org_2", "org_gone"} {
		a, _ := s.Account(ctx, org)
		if a.Status != by[org].Status || a.AIEnabled != by[org].AIEnabled {
			t.Errorf("%s: Account %s %v, Workspaces %+v", org, a.Status, a.AIEnabled, by[org])
		}
	}
}
