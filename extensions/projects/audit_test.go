package projects

import (
	"testing"

	"github.com/xraph/chronicle"
	"github.com/xraph/chronicle/audit"
	"github.com/xraph/chronicle/store"
	"github.com/xraph/chronicle/store/memory"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

func TestProjectAuditPreservesExplicitFailureOutcome(t *testing.T) {
	for _, outcome := range []string{"", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			mem := memory.New()
			log, err := chronicle.New(chronicle.WithStore(store.NewAdapter(mem)))
			if err != nil {
				t.Fatal(err)
			}
			recorder := NewAuditor(log, "app", nil)
			recorder.Record(t.Context(), projects.AuditEvent{Action: "service.backup.retirement.finish", ProjectID: "project", Severity: "warning", Outcome: outcome, Principal: security.Principal{OrgID: "org", UserID: "owner"}})
			result, err := mem.Query(t.Context(), &audit.Query{AppID: "app"})
			if err != nil || len(result.Events) != 1 {
				t.Fatalf("audit query: %+v %v", result, err)
			}
			expected := outcome
			if expected == "" {
				expected = "success"
			}
			if result.Events[0].Outcome != expected {
				t.Fatalf("outcome=%q expected=%q", result.Events[0].Outcome, expected)
			}
		})
	}
}

func TestAuditNamesTheAutomation(t *testing.T) {
	mem := memory.New()
	log, err := chronicle.New(chronicle.WithStore(store.NewAdapter(mem)))
	if err != nil {
		t.Fatal(err)
	}
	recorder := NewAuditor(log, "app", nil)
	recorder.Record(t.Context(), projects.AuditEvent{Action: "plan.approve", ProjectID: "project", Principal: security.SystemPrincipal("o"), Attributes: map[string]any{"trigger": "watch"}})
	recorder.Record(t.Context(), projects.AuditEvent{Action: "plan.approve", ProjectID: "project", Principal: security.Principal{OrgID: "o", UserID: "usr_1"}, Attributes: map[string]any{"trigger": "watch"}})
	result, err := mem.Query(t.Context(), &audit.Query{AppID: "app"})
	if err != nil || len(result.Events) != 2 {
		t.Fatalf("audit query: %+v %v", result, err)
	}
	byUser := map[string]*audit.Event{}
	for _, e := range result.Events {
		byUser[e.UserID] = e
	}
	auto := byUser["loomplane:o"]
	if auto == nil {
		t.Fatalf("no event by the automation in %+v", result.Events)
	}
	if auto.Metadata["actor"] != "system" || auto.Metadata["actor_name"] != "Loomplane automation" || auto.Metadata["trigger"] != "watch" {
		t.Fatalf("automation metadata %v", auto.Metadata)
	}
	person := byUser["usr_1"]
	if person == nil {
		t.Fatal("no event by the person")
	}
	if _, ok := person.Metadata["actor"]; ok {
		t.Fatalf("person metadata %v", person.Metadata)
	}
	if _, ok := person.Metadata["actor_name"]; ok {
		t.Fatalf("person metadata %v", person.Metadata)
	}
}
