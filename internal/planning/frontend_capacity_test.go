package planning

import (
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/pkg/provider"
)

// A frontend demand names the plan that counted it, so it differs on every
// plan. It's governance metadata, not configuration, and mustn't turn an
// unchanged frontend into an update.
func TestChangesIgnoreFrontendDemand(t *testing.T) {
	deployed := ServiceConfig{Provider: "vercel", Workload: provider.WebApp, Service: "web",
		FrontendDemand: &cloudinfra.FrontendDemand{OrgID: "org", PlanID: "plan_one", Service: "web", Provider: "vercel", ConnectionID: "conn"}}
	raw, err := json.Marshal(deployed)
	if err != nil {
		t.Fatal(err)
	}
	next := deployed
	next.FrontendDemand = nil
	if got := changes(raw, next); len(got) != 0 {
		t.Fatalf("changes = %+v, want none", got)
	}
	next.FrontendDemand = &cloudinfra.FrontendDemand{OrgID: "org", PlanID: "plan_two", Service: "web", Provider: "vercel", ConnectionID: "conn"}
	if got := changes(raw, next); len(got) != 0 {
		t.Fatalf("changes = %+v, want none", got)
	}
}
