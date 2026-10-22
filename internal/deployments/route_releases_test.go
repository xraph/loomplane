package deployments

import (
	"strings"
	"testing"

	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/planning"
)

func TestRoutedCandidateIdentitySurvivesMutableRuntimeReceipt(t *testing.T) {
	job := &Job{Deployment: &Deployment{ID: "deployment", OrgID: "org", ProjectID: "project", Environment: "production"}, Action: planning.Action{ID: "service:api", Name: "api", Routes: []cloudinfra.Route{{ID: "route"}}}}
	instances := &fakeInstances{byID: map[id.ID]*instance.Instance{}}
	ctrl := CtrlPlane{Instances: instances}
	candidate, name, err := currentInstance(t.Context(), ctrl, job, "server.connection")
	if err != nil || candidate != nil || name == "" {
		t.Fatal(candidate, name, err)
	}
	existing := ctrlInstance(strings.ToLower(name), "server.connection")
	existing.TenantID = "org"
	existing.Labels = map[string]string{labelReleaseAttempt: "deployment/service:api", "loomplane.revision": "controller-release-after-retry"}
	instances.byID[existing.ID] = existing
	candidate, again, err := currentInstance(t.Context(), ctrl, job, "server.connection")
	if err != nil || candidate.ID != existing.ID || again != name {
		t.Fatal("candidate was not adopted", candidate, err)
	}
	existing.Labels[labelReleaseAttempt] = "other/service:api"
	if _, _, err = currentInstance(t.Context(), ctrl, job, "server.connection"); err == nil {
		t.Fatal("foreign attempt adopted")
	}
	existing.Labels[labelReleaseAttempt] = "deployment/service:api"
	existing.TenantID = "foreign"
	if _, _, err = currentInstance(t.Context(), ctrl, job, "server.connection"); err == nil {
		t.Fatal("foreign organization adopted")
	}
}

func TestRoutedFinalizationRequiresEveryReviewedAction(t *testing.T) {
	plan := &planning.Plan{Actions: []planning.Action{{ID: "routed", Op: planning.Replace}, {ID: "dependent", Op: planning.Update}, {ID: "unchanged", Op: planning.Keep}}}
	d := &Deployment{Steps: []Step{{ActionID: "routed", Status: StepSucceeded}, {ActionID: "dependent", Status: StepFailed}}}
	if err := reviewedActionsComplete(d, plan); err == nil {
		t.Fatal("failed dependent accepted")
	}
	d.Steps[1].Status = StepSucceeded
	if err := reviewedActionsComplete(d, plan); err != nil {
		t.Fatal(err)
	}
	plan.Actions = append(plan.Actions, planning.Action{ID: "missing", Op: planning.Create})
	if err := reviewedActionsComplete(d, plan); err == nil {
		t.Fatal("missing action accepted")
	}
}
