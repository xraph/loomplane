package identity

import (
	"context"
	"testing"

	"github.com/xraph/authsome/bridge"
	"github.com/xraph/authsome/bridge/chronicleadapter"
	"github.com/xraph/chronicle"
	"github.com/xraph/chronicle/audit"
	"github.com/xraph/chronicle/store"
	"github.com/xraph/chronicle/store/memory"
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
