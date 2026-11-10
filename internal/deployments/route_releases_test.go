package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
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

func TestRoutedPlacementAcceptsDirectTargets(t *testing.T) {
	plan := &planning.Plan{ID: "plan"}
	direct := func() *planning.CloudPlacement {
		return &planning.CloudPlacement{CloudID: "cloud", TargetID: "target", ServerID: "server_1"}
	}
	pool := func(allocation string) *planning.CloudPlacement {
		return &planning.CloudPlacement{CloudID: "cloud", TargetID: "target", PoolID: "pool", ServerID: "host", Allocation: allocation}
	}
	service := func(op planning.Op, pin *planning.CloudPlacement) planning.Action {
		return planning.Action{ID: "service:api", Subject: planning.ServiceSubject, Name: "api", Op: op, Service: &planning.ServiceConfig{CloudPlacement: pin}}
	}
	component := func(pin *planning.CloudPlacement) planning.Action {
		return planning.Action{ID: "resource:gw--gateway", Subject: planning.ResourceSubject, Name: "gw--gateway", Op: planning.Replace, Resource: &planning.ResourceConfig{CloudPlacement: pin, Component: &planning.ComponentConfig{Resource: "gw"}}}
	}
	bound := func(subject planning.Subject, pin *planning.CloudPlacement) *planning.Binding {
		var cfg any = planning.ServiceConfig{CloudPlacement: pin}
		if subject == planning.ResourceSubject {
			cfg = planning.ResourceConfig{CloudPlacement: pin, Component: &planning.ComponentConfig{Resource: "gw"}}
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return &planning.Binding{Subject: subject, Config: raw}
	}
	noServer := direct()
	noServer.ServerID = ""
	allocated := direct()
	allocated.Allocation = "plan"
	for name, tt := range map[string]struct {
		a      planning.Action
		source *planning.Binding
		ok     bool
	}{
		"a direct service":               {service(planning.Replace, direct()), bound(planning.ServiceSubject, direct()), true},
		"a direct component":             {component(direct()), bound(planning.ResourceSubject, direct()), true},
		"a pool service":                 {service(planning.Replace, pool("plan")), bound(planning.ServiceSubject, pool("older")), true},
		"a pool service unreserved":      {service(planning.Replace, pool("")), bound(planning.ServiceSubject, pool("older")), false},
		"a pool service reusing its key": {service(planning.Replace, pool("plan")), bound(planning.ServiceSubject, pool("plan")), false},
		"from a pool onto a direct":      {service(planning.Replace, direct()), bound(planning.ServiceSubject, pool("older")), false},
		"a direct target with no server": {service(planning.Replace, noServer), bound(planning.ServiceSubject, direct()), false},
		"a direct target with a key":     {service(planning.Replace, allocated), bound(planning.ServiceSubject, direct()), false},
		"an update":                      {service(planning.Update, direct()), bound(planning.ServiceSubject, direct()), false},
		"no placement":                   {service(planning.Replace, nil), bound(planning.ServiceSubject, direct()), false},
		"no source":                      {service(planning.Replace, direct()), nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			err := routedPlacementErr(plan, tt.a, tt.source)
			if tt.ok != (err == nil) {
				t.Fatalf("got %v, want ok %v", err, tt.ok)
			}
			if err != nil && name != "a pool service reusing its key" && !errors.Is(err, errRoutedCapacity) {
				t.Fatalf("message %q", err)
			}
		})
	}
}

type recordedReleaseRoutes struct {
	releaseRoutes []cloudinfra.ReleaseRouteRequest
}

func (*recordedReleaseRoutes) ValidateRuntimeRemoval(context.Context, string, string, string) error {
	return nil
}

func (r *recordedReleaseRoutes) ReconcileReleaseRoutes(_ context.Context, _ security.Principal, in cloudinfra.ReleaseRouteRequest) error {
	r.releaseRoutes = append(r.releaseRoutes, in)
	return nil
}

func TestFinalizeReleaseActionReadsAComponentsCloud(t *testing.T) {
	a := planning.Action{
		ID: "resource:gw--gateway", Subject: planning.ResourceSubject, Name: "gw--gateway", Op: planning.Replace, Provider: "server", ConnectionID: "runtime",
		Resource: &planning.ResourceConfig{Provider: "server", CloudPlacement: &planning.CloudPlacement{CloudID: "cloud", ServerID: "server_1"}, Component: &planning.ComponentConfig{Resource: "gw"}},
		Routes:   []cloudinfra.Route{{ID: "route"}},
	}
	plan := &planning.Plan{ID: "plan", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Actions: []planning.Action{a}}
	d := &Deployment{ID: "deployment", PlanID: plan.ID, OrgID: "org", ProjectID: "proj", EnvironmentID: "env", StartedBy: "usr", Status: StatusRunning, Steps: []Step{{ActionID: a.ID, Status: StepSucceeded}}}
	binding := &planning.Binding{ID: "binding", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ResourceSubject, Name: a.Name, Provider: "server", ConnectionID: "runtime", ExternalID: "candidate", PlanID: plan.ID}
	routes := &recordedReleaseRoutes{}
	runner := NewRunner(RunnerDeps{Store: &memStore{d: map[string]*Deployment{d.ID: d}}, Plans: &fakePlans{plan: plan, bindings: map[string]*planning.Binding{a.ID: binding}}, Routes: routes, Providers: providers.Registry()})
	if err := runner.finalizeReleaseAction(t.Context(), d, plan, a, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(routes.releaseRoutes) != 1 || routes.releaseRoutes[0].CloudID != "cloud" || routes.releaseRoutes[0].InstanceID != "candidate" || routes.releaseRoutes[0].Service != "gw--gateway" {
		t.Fatalf("release routes: %+v", routes.releaseRoutes)
	}
}
