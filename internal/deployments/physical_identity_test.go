package deployments

import (
	"context"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
)

func TestRunnerBindsObservedPhysicalIdentity(t *testing.T) {
	plan := webAndAPI()
	runner, _, _ := newRunner(t, plan, &recorder{}, fakeVariables{})
	d := &Deployment{OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, PlanID: plan.ID}
	bound, err := runner.bind(t.Context(), d, plan.Actions[0], nil, &Outcome{ExternalID: "database", PhysicalID: "db-original"}, "conn_g", nil)
	if err != nil {
		t.Fatal(err)
	}
	if bound.PhysicalID != "db-original" || bound.ExternalID != "database" {
		t.Fatal("observed incarnation was not retained separately from its name")
	}
}

func TestDifferentPhysicalInstancesDoNotCollapseIntoOneBinding(t *testing.T) {
	runner, _, _ := newRunner(t, webAndAPI(), &recorder{}, fakeVariables{})
	a := &planning.Binding{Provider: "rds", Region: "us-east-1", ConnectionID: "conn_g", ExternalID: "database", PhysicalID: "db-original"}
	b := *a
	b.PhysicalID = "db-recreated"
	same, err := runner.sameBindingResource(t.Context(), "org_1", a, &b)
	if err != nil || same || bindingIdentity(a) == bindingIdentity(&b) {
		t.Fatalf("reused name hid a different database: same=%v err=%v", same, err)
	}
}

func TestRunnerDoesNotDropAKnownPhysicalIdentity(t *testing.T) {
	plan := webAndAPI()
	runner, _, _ := newRunner(t, plan, &recorder{}, fakeVariables{})
	d := &Deployment{OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, PlanID: plan.ID}
	current := &planning.Binding{ID: "binding", Provider: plan.Actions[0].Provider, ConnectionID: "conn_g", ExternalID: "database", PhysicalID: "db-original"}
	if _, err := runner.bind(t.Context(), d, plan.Actions[0], current, &Outcome{ExternalID: "database"}, "conn_g", nil); err == nil {
		t.Fatal("a known physical pin was silently discarded")
	}
}

func TestPhysicalDatabaseIDsRemainScopedToTheirRegion(t *testing.T) {
	runner, _, _ := newRunner(t, webAndAPI(), &recorder{}, fakeVariables{})
	a := &planning.Binding{Provider: "rds", Region: "us-east-1", ConnectionID: "conn_g", ExternalID: "database", PhysicalID: "db-original"}
	b := *a
	b.Region = "us-west-2"
	same, err := runner.sameBindingResource(t.Context(), "org_1", a, &b)
	if err != nil || same {
		t.Fatalf("region-scoped physical IDs were treated as global: same=%v err=%v", same, err)
	}
}

// physicalPlacement reports the provider's immutable identity after applying.
type physicalPlacement struct {
	*placed
	physical string
}

func (p *physicalPlacement) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	out, err := p.placed.Apply(ctx, job)
	if out != nil {
		out.PhysicalID = p.physical
	}
	return out, err
}
func TestRegionMoveRetiresTheOriginalPhysicalDatabase(t *testing.T) {
	for _, destinationID := range []string{"db-new", "db-original"} {
		t.Run(destinationID, func(t *testing.T) {
			plan, old := dbMoves()
			old.ExternalID, old.PhysicalID = "database", "db-original"
			exec := &physicalPlacement{placed: &placed{provider: "cloud-sql", log: &moveLog{}, externalID: "database"}, physical: destinationID}
			runner, store, plans, _ := moveRunner(t, plan, old, map[string]Executor{"cloud-sql": exec})
			id, err := runAll(t, runner, plan)
			if err != nil {
				t.Fatal(err)
			}
			deployed, _ := store.GetDeployment(t.Context(), plan.OrgID, id)
			if deployed.Status != StatusSucceeded {
				t.Fatalf("move failed: %s", deployed.Error)
			}
			if len(exec.removes) != 1 || plans.bindings["resource:db"].PhysicalID != destinationID {
				t.Fatal("the distinct destination was not bound before retiring its source")
			}
		})
	}
}
