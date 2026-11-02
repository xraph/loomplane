package deployments

import (
	"testing"

	"github.com/xraph/loomplane/internal/planning"
)

func TestCloudCompensationRefusesUnresolvedMixedRuntime(t *testing.T) {
	for _, original := range []string{"server", "render"} {
		source := "server"
		if original == "server" {
			source = "render"
		}
		r := &Runner{plans: &fakePlans{plan: &planning.Plan{ID: "old", Actions: []planning.Action{{ID: "service:web", Provider: original}}}}}
		action := planning.Action{Name: "web", Compensation: &planning.CloudCompensation{PlanID: "old", ActionID: "service:web", ServerAttempt: original == "server", Source: planning.Binding{Provider: source}}}
		if err := r.validateCompensationCapability(t.Context(), &Deployment{OrgID: "org"}, &planning.Plan{ID: "new"}, action); err == nil {
			t.Fatal("unresolved mixed runtime accepted", original)
		}
		action.Compensation.ServerAttempt = !action.Compensation.ServerAttempt
		if err := r.validateCompensationCapability(t.Context(), &Deployment{OrgID: "org"}, &planning.Plan{ID: "new"}, action); err == nil {
			t.Fatal("forged original capability accepted", original)
		}
	}
}
