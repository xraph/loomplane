package agentops

import (
	"testing"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// PlanRisk is the line between what the agent may promote through a gate
// and what always needs a person: in production, a delete, or a replace of
// something that holds data.
func TestPlanRisk(t *testing.T) {
	statelessService := planning.Action{ID: "service:api", Subject: planning.ServiceSubject, Name: "api", Service: &planning.ServiceConfig{}}
	database := planning.Action{ID: "resource:db", Subject: planning.ResourceSubject, Name: "db", Resource: &planning.ResourceConfig{Kind: "postgres"}}
	bucket := planning.Action{ID: "resource:files", Subject: planning.ResourceSubject, Name: "files", Resource: &planning.ResourceConfig{Kind: "bucket"}}
	with := func(a planning.Action, op planning.Op) planning.Action {
		a.Op = op
		return a
	}
	cases := []struct {
		name string
		kind string
		plan planning.Plan
		want defs.Risk
	}{
		{
			name: "staging plan with a delete", kind: projects.KindStaging,
			plan: planning.Plan{Actions: []planning.Action{with(statelessService, planning.Delete), with(database, planning.Delete)}},
			want: defs.RiskDeployNonProduction,
		},
		{
			name: "production update of services", kind: projects.KindProduction,
			plan: planning.Plan{Actions: []planning.Action{with(statelessService, planning.Update)}},
			want: defs.RiskPromoteProduction,
		},
		{
			name: "production replace of a stateless service (routed blue/green)", kind: projects.KindProduction,
			plan: planning.Plan{Actions: []planning.Action{with(statelessService, planning.Replace)}},
			want: defs.RiskPromoteProduction,
		},
		{
			name: "production replace of a database", kind: projects.KindProduction,
			plan: planning.Plan{Actions: []planning.Action{with(statelessService, planning.Update), with(database, planning.Replace)}},
			want: defs.RiskDestroyProduction,
		},
		{
			name: "production delete of a stateless service", kind: projects.KindProduction,
			plan: planning.Plan{Actions: []planning.Action{with(statelessService, planning.Delete)}},
			want: defs.RiskDestroyProduction,
		},
		{
			name: "production create of a bucket", kind: projects.KindProduction,
			plan: planning.Plan{Actions: []planning.Action{with(bucket, planning.Create)}},
			want: defs.RiskPromoteProduction,
		},
		{
			name: "production rollback plan (updates only)", kind: projects.KindProduction,
			plan: planning.Plan{Kind: planning.KindRollback, Actions: []planning.Action{with(statelessService, planning.Update), with(database, planning.Update)}},
			want: defs.RiskPromoteProduction,
		},
		{
			name: "preview plan replacing a database", kind: projects.KindPreview,
			plan: planning.Plan{Actions: []planning.Action{with(database, planning.Replace)}},
			want: defs.RiskDeployNonProduction,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlanRisk(&tc.plan, tc.kind); got != tc.want {
				t.Fatalf("PlanRisk = %q, want %q", got, tc.want)
			}
		})
	}
}
