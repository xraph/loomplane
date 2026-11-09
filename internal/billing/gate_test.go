//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/metering"
)

func TestGate_Matrix(t *testing.T) {
	cases := []struct {
		status Status
		ai     bool
		kind   metering.Kind
		owned  bool
		reason string // "" means allowed
	}{
		{StatusPendingPayment, false, metering.KindDeploy, false, "Add a card in Settings > Billing to start running anything on Loomplane Cloud."},
		{StatusPendingPayment, false, metering.KindBuild, true, "Add a card in Settings > Billing to start running anything on Loomplane Cloud."},
		{StatusPendingPayment, true, metering.KindAIPlatform, false, "Add a card in Settings > Billing to start running anything on Loomplane Cloud."},
		{StatusPastDue, false, metering.KindDeploy, false, ""},
		{StatusPastDue, false, metering.KindDeploy, true, "Your last payment failed. Update your card in Settings > Billing."},
		{StatusPastDue, true, metering.KindAIPlatform, false, "Your last payment failed. Update your card in Settings > Billing."},
		{StatusLimited, false, metering.KindBuild, false, ""},
		{StatusLimited, false, metering.KindBuild, true, "This workspace reached its spending limit for the month. Raise it in Settings > Billing."},
		{StatusLimited, true, metering.KindAIPlatform, false, "This workspace reached its spending limit for the month. Raise it in Settings > Billing."},
		{StatusActive, false, metering.KindDeploy, true, ""},
		{StatusActive, false, metering.KindAIPlatform, false, "Turn on the AI add-on in Settings > Billing, or add your own model key."},
		{StatusActive, true, metering.KindAIPlatform, false, ""},
	}
	for _, c := range cases {
		g := &gate{account: func(context.Context, string) (Account, error) {
			return Account{OrgID: "org", Status: c.status, AIEnabled: c.ai}, nil
		}, now: time.Now}
		err := g.CanRun(t.Context(), "org", c.kind, c.owned)
		var b *metering.BlockedError
		switch {
		case c.reason == "" && err != nil:
			t.Errorf("%s %s owned=%v: refused %v", c.status, c.kind, c.owned, err)
		case c.reason != "" && (!errors.As(err, &b) || b.Reason != c.reason):
			t.Errorf("%s %s owned=%v: %v, want %q", c.status, c.kind, c.owned, err, c.reason)
		}
	}
}

// TestGate_EveryStatusAndKind walks every status against every kind of
// work, platform-owned or not and with the add-on on or off, so no cell of
// the matrix goes unchecked.
func TestGate_EveryStatusAndKind(t *testing.T) {
	want := func(s Status, kind metering.Kind, owned, ai bool) string {
		spends := owned || kind == metering.KindAIPlatform
		switch {
		case s == StatusPendingPayment:
			return reasonPending
		case s == StatusPastDue && spends:
			return reasonPastDue
		case s == StatusLimited && spends:
			return reasonLimited
		case s == StatusActive && kind == metering.KindAIPlatform && !ai:
			return reasonNoAddOn
		}
		return ""
	}
	n := 0
	for _, s := range []Status{StatusPendingPayment, StatusPastDue, StatusLimited, StatusActive} {
		for _, kind := range []metering.Kind{metering.KindDeploy, metering.KindBuild, metering.KindAIPlatform} {
			for _, owned := range []bool{false, true} {
				for _, ai := range []bool{false, true} {
					n++
					g := &gate{account: func(context.Context, string) (Account, error) {
						return Account{OrgID: "org", Status: s, AIEnabled: ai}, nil
					}, now: time.Now}
					err := g.CanRun(t.Context(), "org", kind, owned)
					reason := want(s, kind, owned, ai)
					var b *metering.BlockedError
					switch {
					case reason == "" && err != nil:
						t.Errorf("%s %s owned=%v ai=%v: refused %v", s, kind, owned, ai, err)
					case reason != "" && (!errors.As(err, &b) || b.Reason != reason || !errors.Is(err, metering.ErrBlocked)):
						t.Errorf("%s %s owned=%v ai=%v: %v, want %q", s, kind, owned, ai, err, reason)
					}
				}
			}
		}
	}
	if n != 48 {
		t.Fatalf("checked %d cells", n)
	}
}

// The spelled-out cells the Global Constraints name: pending blocks every
// kind; past_due and limited block only platform work and platform AI.
func TestGate_PendingBlocksEvenTheWorkspacesOwnClouds(t *testing.T) {
	g := &gate{account: func(context.Context, string) (Account, error) {
		return Account{OrgID: "org", Status: StatusPendingPayment, AIEnabled: true}, nil
	}, now: time.Now}
	for _, kind := range []metering.Kind{metering.KindDeploy, metering.KindBuild} {
		if err := g.CanRun(t.Context(), "org", kind, false); !errors.Is(err, metering.ErrBlocked) {
			t.Errorf("pending %s on the workspace's own cloud: %v", kind, err)
		}
	}
}

func TestGate_RefusesWhatItCantDecide(t *testing.T) {
	broken := errors.New("db down")
	cases := map[string]struct {
		account func(context.Context, string) (Account, error)
		kind    metering.Kind
	}{
		"account read fails, deploy": {func(context.Context, string) (Account, error) { return Account{}, broken }, metering.KindDeploy},
		"account read fails, ai":     {func(context.Context, string) (Account, error) { return Account{}, broken }, metering.KindAIPlatform},
		"unknown status": {func(context.Context, string) (Account, error) {
			return Account{OrgID: "org", Status: "weird"}, nil
		}, metering.KindDeploy},
		"unknown kind": {func(context.Context, string) (Account, error) {
			return Account{OrgID: "org", Status: StatusActive, AIEnabled: true}, nil
		}, metering.Kind("gpu")},
	}
	for name, c := range cases {
		g := &gate{account: c.account, now: time.Now}
		if err := g.CanRun(t.Context(), "org", c.kind, false); err == nil {
			t.Errorf("%s: allowed", name)
		}
	}
}

// countingAccounts is an account reader that counts its reads and answers
// from a status the test changes.
type countingAccounts struct {
	reads  int
	status Status
	ai     bool
	endsAt *time.Time
	err    error
	during func() // runs inside a read, as a write racing it would
}

func (c *countingAccounts) read(context.Context, string) (Account, error) {
	c.reads++
	a, err := Account{OrgID: "org", Status: c.status, AIEnabled: c.ai, AIEndsAt: c.endsAt}, c.err
	if c.during != nil {
		c.during() // after the read, before the gate keeps it
	}
	if err != nil {
		return Account{}, err
	}
	return a, nil
}

func TestGate_AIAnswersCachedFor15SecondsDeploysNever(t *testing.T) {
	clock := at("2026-11-10T12:00:00Z")
	acc := &countingAccounts{status: StatusActive, ai: true}
	g := &gate{account: acc.read, now: func() time.Time { return clock }}
	ctx := t.Context()
	for range 3 {
		if err := g.CanRun(ctx, "org", metering.KindAIPlatform, false); err != nil {
			t.Fatal(err)
		}
	}
	if acc.reads != 1 {
		t.Fatalf("three AI checks read the account %d times, want 1", acc.reads)
	}
	acc.status = StatusPendingPayment // changed by another replica
	clock = clock.Add(14 * time.Second)
	if err := g.CanRun(ctx, "org", metering.KindAIPlatform, false); err != nil {
		t.Fatalf("within 15s the cached answer stands: %v", err)
	}
	clock = clock.Add(time.Second)
	if err := g.CanRun(ctx, "org", metering.KindAIPlatform, false); !errors.Is(err, metering.ErrBlocked) {
		t.Fatalf("at 15s the account is read again: %v", err)
	}
	before := acc.reads
	for range 3 {
		_ = g.CanRun(ctx, "org", metering.KindDeploy, false)
		_ = g.CanRun(ctx, "org", metering.KindBuild, true)
	}
	if acc.reads-before != 6 {
		t.Fatalf("six deploy and build checks read %d times, want 6", acc.reads-before)
	}
}

func TestGate_ForgetDropsTheCachedAnswer(t *testing.T) {
	acc := &countingAccounts{status: StatusActive, ai: false}
	g := &gate{account: acc.read, now: time.Now}
	ctx := t.Context()
	if err := g.CanRun(ctx, "org", metering.KindAIPlatform, false); !errors.Is(err, metering.ErrBlocked) {
		t.Fatalf("no add-on: %v", err)
	}
	acc.ai = true
	g.forget("org")
	if err := g.CanRun(ctx, "org", metering.KindAIPlatform, false); err != nil {
		t.Fatalf("after forget: %v", err)
	}
}

func TestGate_AReadRacingAWriteIsNotKept(t *testing.T) {
	acc := &countingAccounts{status: StatusActive, ai: false}
	g := &gate{account: acc.read, now: time.Now}
	ctx := t.Context()
	// The add-on turns on while the first check is reading: the stale
	// answer it read is used once and never kept.
	acc.during = func() { acc.during = nil; acc.ai = true; g.forget("org") }
	if err := g.CanRun(ctx, "org", metering.KindAIPlatform, false); !errors.Is(err, metering.ErrBlocked) {
		t.Fatalf("first check: %v", err)
	}
	if err := g.CanRun(ctx, "org", metering.KindAIPlatform, false); err != nil {
		t.Fatalf("the stale read was kept: %v", err)
	}
	if acc.reads != 2 {
		t.Fatalf("reads %d, want 2", acc.reads)
	}
}

func TestGate_AFailedReadIsNotKept(t *testing.T) {
	acc := &countingAccounts{status: StatusActive, ai: true, err: errors.New("db down")}
	g := &gate{account: acc.read, now: time.Now}
	ctx := t.Context()
	if err := g.CanRun(ctx, "org", metering.KindAIPlatform, false); err == nil || errors.Is(err, metering.ErrBlocked) {
		t.Fatalf("a failed read: %v", err)
	}
	acc.err = nil
	if err := g.CanRun(ctx, "org", metering.KindAIPlatform, false); err != nil {
		t.Fatalf("after the database is back: %v", err)
	}
}

func TestGate_CacheEndsWhenTheAddOnDoes(t *testing.T) {
	clock := at("2026-11-30T23:59:55Z")
	ends := at("2026-12-01T00:00:00Z")
	acc := &countingAccounts{status: StatusActive, ai: true, endsAt: &ends}
	g := &gate{account: acc.read, now: func() time.Time { return clock }}
	ctx := t.Context()
	if err := g.CanRun(ctx, "org", metering.KindAIPlatform, false); err != nil {
		t.Fatal(err)
	}
	acc.ai = false // the month rolled and the add-on ended
	clock = ends
	if err := g.CanRun(ctx, "org", metering.KindAIPlatform, false); !errors.Is(err, metering.ErrBlocked) {
		t.Fatalf("the add-on ended at midnight, the cache kept it on: %v", err)
	}
}

func TestGate_CacheKeepsWorkspacesApart(t *testing.T) {
	g := &gate{account: func(_ context.Context, org string) (Account, error) {
		return Account{OrgID: org, Status: StatusActive, AIEnabled: org == "org_paid"}, nil
	}, now: time.Now}
	ctx := t.Context()
	if err := g.CanRun(ctx, "org_paid", metering.KindAIPlatform, false); err != nil {
		t.Fatal(err)
	}
	if err := g.CanRun(ctx, "org_free", metering.KindAIPlatform, false); !errors.Is(err, metering.ErrBlocked) {
		t.Fatalf("one workspace's add-on let another in: %v", err)
	}
}

func TestGate_SweepsExpiredEntries(t *testing.T) {
	clock := at("2026-11-10T12:00:00Z")
	g := &gate{account: func(_ context.Context, org string) (Account, error) {
		return Account{OrgID: org, Status: StatusActive, AIEnabled: true}, nil
	}, now: func() time.Time { return clock }}
	ctx := t.Context()
	for i := range aiCacheSweepAt {
		_ = g.CanRun(ctx, fmt.Sprintf("org_%d", i), metering.KindAIPlatform, false)
	}
	clock = clock.Add(time.Minute)
	_ = g.CanRun(ctx, "org_new", metering.KindAIPlatform, false)
	if len(g.cache) != 1 {
		t.Fatalf("cache holds %d entries after a sweep, want 1", len(g.cache))
	}
}

// Postgres: the gate through the Service.

func TestGate_DeployPassesRightAfterCard(t *testing.T) {
	s, _ := newTestService(t, at("2026-11-20T00:00:00Z"))
	ctx := t.Context()
	g := s.Gate()
	if err := g.CanRun(ctx, "org_1", metering.KindDeploy, false); !errors.Is(err, metering.ErrBlocked) {
		t.Fatalf("before the card: %v", err)
	}
	if err := s.MarkCardReady(ctx, "org_1"); err != nil {
		t.Fatal(err)
	}
	if err := g.CanRun(ctx, "org_1", metering.KindDeploy, false); err != nil {
		t.Fatalf("right after the card: %v", err)
	}
}

// TestGate_WorkWaitingForACardRunsOnceItLands is the Review Focus case: a
// workspace with deploys, builds and AI calls refused while it had no card
// gets every one of them through the moment the card lands, with no wait
// for a cache, while AI on the server's key still waits for the add-on.
func TestGate_WorkWaitingForACardRunsOnceItLands(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-20T00:00:00Z"))
	ctx := t.Context()
	g := s.Gate()
	type work struct {
		kind  metering.Kind
		owned bool
	}
	waiting := []work{
		{metering.KindDeploy, false}, {metering.KindDeploy, true},
		{metering.KindBuild, false}, {metering.KindBuild, true},
	}
	for _, w := range waiting {
		var b *metering.BlockedError
		if err := g.CanRun(ctx, "org_1", w.kind, w.owned); !errors.As(err, &b) || b.Reason != reasonPending {
			t.Fatalf("%s owned=%v before the card: %v", w.kind, w.owned, err)
		}
	}
	// An AI check before the card puts pending in this replica's cache.
	var b *metering.BlockedError
	if err := g.CanRun(ctx, "org_1", metering.KindAIPlatform, false); !errors.As(err, &b) || b.Reason != reasonPending {
		t.Fatalf("ai before the card: %v", err)
	}

	if err := s.MarkCardReady(ctx, "org_1"); err != nil {
		t.Fatal(err)
	}
	for _, w := range waiting {
		if err := g.CanRun(ctx, "org_1", w.kind, w.owned); err != nil {
			t.Fatalf("%s owned=%v right after the card: %v", w.kind, w.owned, err)
		}
	}
	// The cached pending is gone: AI now asks for the add-on, not a card.
	if err := g.CanRun(ctx, "org_1", metering.KindAIPlatform, false); !errors.As(err, &b) || b.Reason != reasonNoAddOn {
		t.Fatalf("ai right after the card: %v", err)
	}
	// With the add-on on (Task 9 writes this row and forgets the cache).
	if _, err := pg.Exec(ctx, `UPDATE loomplane_billing_accounts SET ai_subscription_id='sub_ai' WHERE org_id='org_1'`); err != nil {
		t.Fatal(err)
	}
	s.forget("org_1")
	if err := g.CanRun(ctx, "org_1", metering.KindAIPlatform, false); err != nil {
		t.Fatalf("ai with the add-on: %v", err)
	}
}

func TestGate_ThroughTheServiceRefusesAnEmptyWorkspace(t *testing.T) {
	s, _ := newTestService(t, at("2026-11-20T00:00:00Z"))
	if err := s.Gate().CanRun(t.Context(), "", metering.KindDeploy, false); err == nil {
		t.Fatal("an empty workspace id was allowed")
	}
}

func TestGate_AServiceThatCantWorkRefusesEverything(t *testing.T) {
	s := New(Deps{})
	for _, kind := range []metering.Kind{metering.KindDeploy, metering.KindBuild, metering.KindAIPlatform} {
		if err := s.Gate().CanRun(t.Context(), "org_1", kind, false); err == nil {
			t.Errorf("%s allowed by a service with no database", kind)
		}
	}
}
