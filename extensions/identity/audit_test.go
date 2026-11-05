package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/authsome/bridge"
	"github.com/xraph/authsome/bridge/chronicleadapter"
	"github.com/xraph/chronicle"
	"github.com/xraph/chronicle/audit"
	"github.com/xraph/chronicle/store"
	"github.com/xraph/chronicle/store/memory"

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
