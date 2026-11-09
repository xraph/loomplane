package identity

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xraph/authsome/bridge"
	"github.com/xraph/authsome/bridge/chronicleadapter"
	"github.com/xraph/authsome/rbac"
	"github.com/xraph/chronicle"
	"github.com/xraph/chronicle/audit"
	"github.com/xraph/chronicle/store"
	"github.com/xraph/chronicle/store/memory"
	"github.com/xraph/forge"
	gulog "github.com/xraph/go-utils/log"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/platformadmin"
)

func TestCategorized_recordsAuthsomeEvents(t *testing.T) {
	tests := []struct {
		name     string
		event    bridge.AuditEvent
		severity string
		want     string
	}{
		// What authsome sends for a sign-in: no category.
		{"uncategorized", bridge.AuditEvent{Action: "session.create", Resource: "session"}, bridge.SeverityInfo, authAuditCategory},
		{"uncategorized warning", bridge.AuditEvent{Action: "auth.signin.failed", Resource: "session"}, bridge.SeverityWarning, authAuditCategory},
		{"uncategorized critical", bridge.AuditEvent{Action: "user.ban", Resource: "user"}, bridge.SeverityCritical, authAuditCategory},
		{"own category", bridge.AuditEvent{Action: "rbac.role.assign", Resource: "role", Category: "rbac"}, bridge.SeverityInfo, "rbac"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mem := memory.New()
			c, err := chronicle.New(chronicle.WithStore(store.NewAdapter(mem)))
			if err != nil {
				t.Fatal(err)
			}
			event := tt.event
			event.Tenant = "aapp_test"
			event.Severity = tt.severity
			if err = chronicleadapter.New(categorized{Emitter: c, category: authAuditCategory}).Record(ctx, &event); err != nil {
				t.Fatalf("record: %v", err)
			}
			res, err := mem.Query(ctx, &audit.Query{AppID: "aapp_test"})
			if err != nil || len(res.Events) != 1 {
				t.Fatalf("query: %v, %d events", err, len(res.Events))
			}
			if got := res.Events[0].Category; got != tt.want {
				t.Fatalf("category %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCategorized_recordFillsAnEmptyCategory(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	c, err := chronicle.New(chronicle.WithStore(store.NewAdapter(mem)))
	if err != nil {
		t.Fatal(err)
	}
	e := categorized{Emitter: c, category: authAuditCategory}
	if err = e.Record(ctx, &audit.Event{Action: "session.create", Resource: "session", AppID: "aapp_test"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	res, err := mem.Query(ctx, &audit.Query{AppID: "aapp_test"})
	if err != nil || len(res.Events) != 1 || res.Events[0].Category != authAuditCategory {
		t.Fatalf("query: %v, %+v", err, res)
	}
}

// streamOf is the tenant of the one event recorded with action.
func streamOf(t *testing.T, mem *memory.Store, action string) string {
	t.Helper()
	res, err := mem.Query(context.Background(), &audit.Query{AppID: "aapp_test", Actions: []string{action}})
	if err != nil || len(res.Events) != 1 {
		t.Fatalf("query %s: %v, %d events", action, err, len(res.Events))
	}
	return res.Events[0].TenantID
}

func TestStaffStream_routesStaffEventsToThePlatformTenant(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	c, err := chronicle.New(chronicle.WithStore(store.NewAdapter(mem)))
	if err != nil {
		t.Fatal(err)
	}
	staff := map[string]bool{"ausr_staff": true}
	bridgeTo := staffStream{
		Chronicle: chronicleadapter.New(categorized{Emitter: c, category: authAuditCategory}),
		isStaff: func(_ context.Context, userID string) (bool, error) {
			if userID == "ausr_broken" {
				return false, errors.New("warden is down")
			}
			return staff[userID], nil
		},
	}
	for _, ev := range []bridge.AuditEvent{
		{Action: "staff.signin", Resource: "session", ActorID: "ausr_staff"},
		{Action: "customer.signin", Resource: "session", ActorID: "ausr_customer"},
		{Action: "anonymous.signin.failed", Resource: "session"},
		{Action: "staff.org.invite", Resource: "invitation", ActorID: "ausr_staff", OrgID: "aorg_customer"},
		{Action: "unknown.signin", Resource: "session", ActorID: "ausr_broken"},
	} {
		ev.Tenant = "aapp_test"
		ev.Severity = bridge.SeverityInfo
		if err = bridgeTo.Record(ctx, &ev); err != nil {
			t.Fatalf("record %s: %v", ev.Action, err)
		}
	}
	for action, want := range map[string]string{
		"staff.signin":            platformadmin.AuditTenant,
		"customer.signin":         "",
		"anonymous.signin.failed": "",
		"staff.org.invite":        "aorg_customer",
		"unknown.signin":          "",
	} {
		if got := streamOf(t, mem, action); got != want {
			t.Errorf("%s recorded under tenant %q, want %q", action, got, want)
		}
	}
	// The platform stream is its own Chronicle stream, holding the staff
	// event alone.
	info, err := mem.GetStreamByScope(ctx, "aapp_test", platformadmin.AuditTenant)
	if err != nil || info.HeadSeq != 1 {
		t.Fatalf("platform stream: %+v, %v; want one event", info, err)
	}
}

// fakeClock is a clock a test moves by hand.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }
func newFakeClock() *fakeClock           { return &fakeClock{t: time.Unix(1_800_000_000, 0)} }
func testLogger() *gulog.TestLogger      { return gulog.NewTestLogger().(*gulog.TestLogger) }
func staffReads(reads map[string]int) func(context.Context, string) (bool, error) {
	return func(_ context.Context, userID string) (bool, error) {
		reads[userID]++
		if userID == "ausr_broken" {
			return false, errors.New("warden is down")
		}
		return userID == "ausr_staff", nil
	}
}

// Every authsome event with an actor and no organization asks whether the
// actor is staff. Within the TTL a user's answer comes from the cache, so
// Warden is read once per user, and again once the answer has expired.
func TestStaffRoles_cachesEachUsersAnswerForTheTTL(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	reads := map[string]int{}
	s := newStaffRoles(staffReads(reads), forge.NewNoopLogger(), nil)
	s.now = clock.now

	for range 5 {
		if staff, err := s.isStaff(ctx, "ausr_staff"); err != nil || !staff {
			t.Fatalf("staff = %v, %v", staff, err)
		}
		if staff, err := s.isStaff(ctx, "ausr_customer"); err != nil || staff {
			t.Fatalf("customer = %v, %v", staff, err)
		}
		clock.add(staffRolesTTL / 10)
	}
	if reads["ausr_staff"] != 1 || reads["ausr_customer"] != 1 {
		t.Fatalf("Warden read %v within the TTL, want once per user", reads)
	}
	clock.add(staffRolesTTL)
	if staff, err := s.isStaff(ctx, "ausr_staff"); err != nil || !staff {
		t.Fatalf("staff after expiry = %v, %v", staff, err)
	}
	if reads["ausr_staff"] != 2 {
		t.Fatalf("Warden read %d times after the TTL, want a second read", reads["ausr_staff"])
	}
}

// The cache holds at most staffRolesMax users.
func TestStaffRoles_staysBounded(t *testing.T) {
	ctx := context.Background()
	s := newStaffRoles(staffReads(map[string]int{}), forge.NewNoopLogger(), nil)
	for i := range staffRolesMax * 2 {
		if _, err := s.isStaff(ctx, fmt.Sprintf("ausr_%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(s.entries); n > staffRolesMax {
		t.Fatalf("cache holds %d users, want at most %d", n, staffRolesMax)
	}
}

// A failed read isn't cached, so the next event asks again. Each failure
// is counted, and a warning goes out at most once a minute saying how many
// events went to the app level since the last one.
func TestStaffRoles_countsAndWarnsOnFailures(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	reads := map[string]int{}
	log := testLogger()
	s := newStaffRoles(staffReads(reads), log, nil)
	s.now = clock.now

	for range 3 {
		if staff, err := s.isStaff(ctx, "ausr_broken"); err == nil || staff {
			t.Fatalf("a failed read = %v, %v; want an error", staff, err)
		}
		clock.add(time.Second)
	}
	if reads["ausr_broken"] != 3 {
		t.Fatalf("Warden read %d times, want every failure retried", reads["ausr_broken"])
	}
	if got := s.failures.Load(); got != 3 {
		t.Fatalf("counted %d failures, want 3", got)
	}
	if n := log.CountLogs("WARN"); n != 1 {
		t.Fatalf("%d warnings within a minute, want one", n)
	}
	clock.add(time.Minute)
	if _, err := s.isStaff(ctx, "ausr_broken"); err == nil {
		t.Fatal("want an error")
	}
	warns := log.GetLogsByLevel("WARN")
	if len(warns) != 2 {
		t.Fatalf("%d warnings after a minute, want a second", len(warns))
	}
	if n, _ := warns[1].Field("events"); fmt.Sprint(n) != "3" {
		t.Fatalf("second warning counts %v events, want the 3 since the first", n)
	}
}

// brokenPlatformRoles is a Warden that can't be read.
type brokenPlatformRoles struct{ rbac.Store }

func (brokenPlatformRoles) ListUserRolesForApp(context.Context, string, string) ([]*rbac.Role, error) {
	return nil, errors.New("warden is down")
}

// authsome's events go through the bridge the engine is built with, and
// its staff check reads the actor's platform roles the way IsPlatformAdmin
// does. A platform admin's event with no organization lands in the
// platform stream of Loomplane's app; a customer's stays at the app level.
func TestAuthsomeChronicle_routesByTheActorsPlatformRole(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	trail, err := chronicle.New(chronicle.WithStore(store.NewAdapter(mem)))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Identity.AppID = "aapp_test"
	e := New(cfg)
	e.SetLogger(forge.NewNoopLogger())
	e.audit = trail
	e.platformRoles = fakePlatformRoles{held: map[string][]*rbac.Role{
		"ausr_admin":    {{AppID: cfg.Identity.AppID, Slug: rbac.PlatformAdminSlug}},
		"ausr_customer": {{AppID: "aapp_other", Slug: rbac.PlatformOwnerSlug}},
	}}
	for _, actor := range []string{"ausr_admin", "ausr_customer"} {
		if admin, _ := e.IsPlatformAdmin(ctx, actor); admin != (actor == "ausr_admin") {
			t.Fatalf("IsPlatformAdmin(%s) = %v", actor, admin)
		}
	}

	bridgeTo := e.authsomeChronicle()
	for _, ev := range []bridge.AuditEvent{
		{Action: "admin.signin", Resource: "session", ActorID: "ausr_admin"},
		{Action: "customer.signin", Resource: "session", ActorID: "ausr_customer"},
	} {
		ev.Tenant, ev.Severity = cfg.Identity.AppID, bridge.SeverityInfo
		if err = bridgeTo.Record(ctx, &ev); err != nil {
			t.Fatalf("record %s: %v", ev.Action, err)
		}
	}
	if got := streamOf(t, mem, "admin.signin"); got != platformadmin.AuditTenant {
		t.Errorf("the admin's sign-in is in tenant %q, want %q", got, platformadmin.AuditTenant)
	}
	if got := streamOf(t, mem, "customer.signin"); got != "" {
		t.Errorf("the customer's sign-in is in tenant %q, want the app level", got)
	}
	if _, err = mem.GetStreamByScope(ctx, cfg.Identity.AppID, platformadmin.AuditTenant); err != nil {
		t.Fatalf("no platform stream in %s: %v", cfg.Identity.AppID, err)
	}

	// With Warden down, the admin's event goes to the app level.
	e.platformRoles = brokenPlatformRoles{}
	ev := bridge.AuditEvent{Action: "admin.signin.unread", Resource: "session", ActorID: "ausr_other_admin", Tenant: cfg.Identity.AppID, Severity: bridge.SeverityInfo}
	if err = e.authsomeChronicle().Record(ctx, &ev); err != nil {
		t.Fatal(err)
	}
	if got := streamOf(t, mem, "admin.signin.unread"); got != "" {
		t.Errorf("an event whose actor's roles couldn't be read is in tenant %q, want the app level", got)
	}
}
