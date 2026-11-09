package deployments

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

func TestJobPrincipalForAutomationApprovedPlans(t *testing.T) {
	auto := &Job{Deployment: &Deployment{OrgID: "o", StartedBy: "loomplane:o"}}
	if got := principal(auto); got != security.SystemPrincipal("o") {
		t.Fatalf("automation-approved deployment acts as %+v", got)
	}
	person := &Job{Deployment: &Deployment{OrgID: "o", StartedBy: "usr_1"}}
	if got, want := principal(person), (security.Principal{OrgID: "o", UserID: "usr_1"}); got != want {
		t.Fatalf("person-approved deployment acts as %+v, want %+v", got, want)
	}
	// A record naming another organization's automation is a person here,
	// with no roles: it never gains the automation's.
	if got := principal(&Job{Deployment: &Deployment{OrgID: "o", StartedBy: "loomplane:other"}}); got.IsServiceAccount() {
		t.Fatalf("another org's automation acts as %+v", got)
	}
}

// A plan the automation approved checks and installs a default backup
// schedule as the automation, never as a person named like it.
func TestBackupPoliciesActAsTheAutomation(t *testing.T) {
	r, _, plans, _, installer, _ := backupRunner(t)
	system := security.SystemPrincipal(plans.plan.OrgID)
	plans.plan.DecidedBy = system.UserID
	asked := &principalAuthz{}
	r.authz = asked
	if err := r.checkBackupPolicies(t.Context(), plans.plan, plans.plan.Actions); err != nil {
		t.Fatal(err)
	}
	plans.bindings["resource:data--postgres"] = &planning.Binding{Name: "data--postgres"}
	d := &Deployment{OrgID: plans.plan.OrgID, ProjectID: "proj_1", Environment: "production", StartedBy: system.UserID}
	if err := r.installBackupPolicy(t.Context(), d, plans.plan.Actions[0]); err != nil {
		t.Fatal(err)
	}
	if len(asked.as) != 1 || asked.as[0] != system || installer.principal != system {
		t.Fatalf("checked as %+v, installed as %+v", asked.as, installer.principal)
	}
}

type principalAuthz struct{ as []security.Principal }

func (a *principalAuthz) Require(_ context.Context, p security.Principal, _ security.Permission, _ string) error {
	a.as = append(a.as, p)
	return nil
}

// busyStore is an environment a Cloud sync or another deployment holds.
type busyStore struct{ *memStore }

func (busyStore) EnvironmentAvailable(context.Context, string, string, string) (bool, error) {
	return false, nil
}

// A rollback the automation approved while a Cloud sync runs waits like
// any plan: the runner refuses to start it with the same error.
func TestRollbackWaitsForABusyEnvironment(t *testing.T) {
	plan := webAndAPI()
	plan.Kind, plan.DecidedBy = planning.KindRollback, security.SystemSubject(plan.OrgID)
	plan.Rollback = &planning.Rollback{Reason: "errors", Trigger: planning.TriggerWatch, TargetPlanID: "plan_0"}
	r, store, _ := newRunner(t, plan, &recorder{}, nil)
	r.store = busyStore{store}
	id, _, err := r.Start(t.Context(), plan.OrgID, plan.ID)
	if !errors.Is(err, cloudprofiles.ErrBusy) || id != "" || len(store.d) != 0 {
		t.Fatalf("start: %q, %v", id, err)
	}
}
