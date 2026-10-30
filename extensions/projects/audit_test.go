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

func TestAuditAttributesAgentCalls(t *testing.T) {
	mem := memory.New()
	log, err := chronicle.New(chronicle.WithStore(store.NewAdapter(mem)))
	if err != nil {
		t.Fatal(err)
	}
	recorder := NewAuditor(log, "app", nil)
	ada := security.Principal{UserID: "usr_ada", OrgID: "o"}
	ctx := security.WithAgentCall(t.Context(), security.AgentCall{TaskID: "atask_1", RunID: "arun_1", Tool: "approve_plan", CallID: "call_1"})
	// An event can't forge the agent's attribution through its attributes.
	recorder.Record(ctx, projects.AuditEvent{Action: "plan.approve", ProjectID: "project", Principal: security.AgentPrincipal("o", &ada), Attributes: map[string]any{"run_id": "forged", "plan_id": "plan_1"}})
	// The agent alone, outside a tool call.
	recorder.Record(t.Context(), projects.AuditEvent{Action: "plan.create", ProjectID: "project", Principal: security.AgentPrincipal("o", nil)})
	// A call that names only its run records only the run.
	runOnly := security.WithAgentCall(t.Context(), security.AgentCall{RunID: "arun_2"})
	recorder.Record(runOnly, projects.AuditEvent{Action: "plan.reject", ProjectID: "project", Principal: security.AgentPrincipal("o", nil)})
	result, err := mem.Query(t.Context(), &audit.Query{AppID: "app"})
	if err != nil || len(result.Events) != 3 {
		t.Fatalf("audit query: %+v %v", result, err)
	}
	byAction := map[string]*audit.Event{}
	for _, e := range result.Events {
		byAction[e.Action] = e
	}
	call := byAction["plan.approve"]
	if call == nil || call.UserID != "agent:o" {
		t.Fatalf("agent call event %+v", call)
	}
	want := map[string]any{"actor": "agent", "actor_name": security.AgentActorName, "on_behalf_of": "usr_ada", "run_id": "arun_1", "task_id": "atask_1", "tool": "approve_plan", "plan_id": "plan_1"}
	for k, v := range want {
		if call.Metadata[k] != v {
			t.Errorf("metadata %s = %v, want %v (all %v)", k, call.Metadata[k], v, call.Metadata)
		}
	}
	if security.AgentActorName != "Loomplane agent" {
		t.Fatalf("AgentActorName = %q", security.AgentActorName)
	}
	alone := byAction["plan.create"]
	if alone == nil || alone.UserID != "agent:o" || alone.Metadata["actor"] != "agent" || alone.Metadata["actor_name"] != security.AgentActorName {
		t.Fatalf("agent event %+v", alone)
	}
	for _, k := range []string{"on_behalf_of", "run_id", "task_id", "tool"} {
		if _, ok := alone.Metadata[k]; ok {
			t.Errorf("the agent alone has %s: %v", k, alone.Metadata)
		}
	}
	partial := byAction["plan.reject"]
	if partial == nil || partial.Metadata["run_id"] != "arun_2" {
		t.Fatalf("run-only event %+v", partial)
	}
	for _, k := range []string{"task_id", "tool"} {
		if _, ok := partial.Metadata[k]; ok {
			t.Errorf("a run-only call recorded a blank %s: %v", k, partial.Metadata)
		}
	}
}

func TestAuditIgnoresAgentCallsForPeople(t *testing.T) {
	mem := memory.New()
	log, err := chronicle.New(chronicle.WithStore(store.NewAdapter(mem)))
	if err != nil {
		t.Fatal(err)
	}
	recorder := NewAuditor(log, "app", nil)
	ctx := security.WithAgentCall(t.Context(), security.AgentCall{TaskID: "atask_1", RunID: "arun_1", Tool: "approve_plan"})
	recorder.Record(ctx, projects.AuditEvent{Action: "plan.approve", ProjectID: "project", Principal: security.Principal{OrgID: "o", UserID: "usr_1"}})
	result, err := mem.Query(t.Context(), &audit.Query{AppID: "app"})
	if err != nil || len(result.Events) != 1 {
		t.Fatalf("audit query: %+v %v", result, err)
	}
	for _, k := range []string{"actor", "on_behalf_of", "run_id", "task_id", "tool"} {
		if _, ok := result.Events[0].Metadata[k]; ok {
			t.Errorf("a person's event has %s: %v", k, result.Events[0].Metadata)
		}
	}
}
