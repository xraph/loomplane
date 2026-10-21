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
