package catalogstate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
)

// facts is one environment's deployment facts, as fakes of Deps.
type facts struct {
	bindings []*planning.Binding
	plans    []*planning.Plan
	last     *deployments.Deployment
	lastErr  error
}

func (f facts) deps() Deps {
	return Deps{
		Bindings: func(context.Context, string, string) ([]*planning.Binding, error) { return f.bindings, nil },
		OpenPlans: func(context.Context, string, string) ([]*planning.Plan, error) {
			return f.plans, nil
		},
		LastRun: func(context.Context, string, string, string) (*deployments.Deployment, error) {
			return f.last, f.lastErr
		},
	}
}

func modelBinding(name, connection string) *planning.Binding {
	return &planning.Binding{Subject: planning.ResourceSubject, Name: name, ConnectionID: connection}
}

// model is the resource whose state the tests read.
const model = "llama"

// openPlan is an open plan whose action on model is op.
func openPlan(id string, op planning.Op) *planning.Plan {
	return &planning.Plan{ID: id, Status: planning.StatusReady, Actions: []planning.Action{
		{ID: "service:api", Op: planning.Keep, Subject: planning.ServiceSubject, Name: "api"},
		{ID: "resource:" + model, Op: op, Subject: planning.ResourceSubject, Name: model, ConnectionID: "conn_gpu"},
	}}
}

// run is deployment dep_1 with the given status and steps.
func run(status string, steps ...deployments.Step) *deployments.Deployment {
	return &deployments.Deployment{ID: "dep_1", Status: status, Steps: steps}
}

// step is model's step in a run.
func step(status, message string) deployments.Step {
	return deployments.Step{ActionID: "resource:" + model, Name: model, Subject: "resource", Status: status, Message: message}
}

func TestStateFromFacts(t *testing.T) {
	long := strings.Repeat("é", 400) // 800 bytes
	tests := []struct {
		name  string
		facts facts
		want  State
	}{
		{"nothing", facts{}, State{Value: Absent}},
		{"no run yet", facts{lastErr: deployments.ErrNotFound}, State{Value: Absent}},
		{"another resource is bound", facts{bindings: []*planning.Binding{modelBinding("other", "conn_cpu")}}, State{Value: Absent}},
		{"a service of the same name is bound", facts{bindings: []*planning.Binding{{Subject: planning.ServiceSubject, Name: model}}}, State{Value: Absent}},
		{"open plan creates it", facts{plans: []*planning.Plan{openPlan("plan_1", planning.Create)}},
			State{Value: Planned, PlanID: "plan_1", ConnectionID: "conn_gpu"}},
		{"open plan deletes it", facts{plans: []*planning.Plan{openPlan("plan_1", planning.Delete)}}, State{Value: Absent}},
		{"a closed plan doesn't count", facts{plans: []*planning.Plan{func() *planning.Plan {
			p := openPlan("plan_1", planning.Create)
			p.Status = planning.StatusSuperseded
			return p
		}()}}, State{Value: Absent}},
		{"running deployment", facts{last: run(deployments.StatusRunning, step(deployments.StepDeploying, "Pulling the image"))},
			State{Value: Deploying, DeploymentID: "dep_1"}},
		{"paused for capacity", facts{last: run(deployments.StatusPaused, step(deployments.StepPending, ""))},
			State{Value: Deploying, DeploymentID: "dep_1"}},
		{"bound and succeeded", facts{
			bindings: []*planning.Binding{modelBinding(model, "conn_gpu")},
			last:     run(deployments.StatusSucceeded, step(deployments.StepSucceeded, "")),
		}, State{Value: Ready, DeploymentID: "dep_1", ConnectionID: "conn_gpu"}},
		{"bound with an update planned", facts{
			bindings: []*planning.Binding{modelBinding(model, "conn_gpu")},
			plans:    []*planning.Plan{openPlan("plan_2", planning.Update)},
			last:     run(deployments.StatusSucceeded, step(deployments.StepSucceeded, "")),
		}, State{Value: Ready, PlanID: "plan_2", DeploymentID: "dep_1", ConnectionID: "conn_gpu"}},
		{"its step is done while the run goes on", facts{
			bindings: []*planning.Binding{modelBinding(model, "conn_gpu")},
			last:     run(deployments.StatusRunning, step(deployments.StepSucceeded, "")),
		}, State{Value: Ready, DeploymentID: "dep_1", ConnectionID: "conn_gpu"}},
		{"its step failed", facts{
			last: run(deployments.StatusFailed, step(deployments.StepFailed, "CUDA error: out of memory")),
		}, State{Value: Failed, DeploymentID: "dep_1", Message: "CUDA error: out of memory"}},
		{"failed after a plan to fix it", facts{
			plans: []*planning.Plan{openPlan("plan_3", planning.Create)},
			last:  run(deployments.StatusFailed, step(deployments.StepFailed, "boom")),
		}, State{Value: Failed, PlanID: "plan_3", DeploymentID: "dep_1", Message: "boom", ConnectionID: "conn_gpu"}},
		{"the run failed before its step", func() facts {
			d := run(deployments.StatusFailed, step(deployments.StepSkipped, ""))
			d.Error = "postgres: connection refused"
			return facts{last: d}
		}(), State{Value: Failed, DeploymentID: "dep_1", Message: "postgres: connection refused"}},
		{"another step failed after its own succeeded", facts{
			bindings: []*planning.Binding{modelBinding(model, "conn_gpu")},
			last:     run(deployments.StatusFailed, step(deployments.StepSucceeded, "")),
		}, State{Value: Ready, DeploymentID: "dep_1", ConnectionID: "conn_gpu"}},
		{"a long failure is cut", facts{
			last: run(deployments.StatusFailed, step(deployments.StepFailed, long)),
		}, State{Value: Failed, DeploymentID: "dep_1", Message: strings.Repeat("é", 256)}},
		{"cancelled and never bound", facts{last: run(deployments.StatusCancelled, step(deployments.StepSkipped, ""))},
			State{Value: Absent, DeploymentID: "dep_1"}},
		{"removed", facts{last: run(deployments.StatusSucceeded, step(deployments.StepSucceeded, ""))},
			State{Value: Absent, DeploymentID: "dep_1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Of(context.Background(), tt.facts.deps(), "org_1", "env_1", model)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("Of:\n got %+v\nwant %+v", got, tt.want)
			}
			if len(got.Message) > MaxMessage || !utf8.ValidString(got.Message) {
				t.Errorf("message is %d bytes, valid UTF-8 %v", len(got.Message), utf8.ValidString(got.Message))
			}
		})
	}
}

func TestStatePassesFactErrorsOn(t *testing.T) {
	boom := errors.New("database is down")
	for name, broken := range map[string]func(*Deps){
		"bindings": func(d *Deps) {
			d.Bindings = func(context.Context, string, string) ([]*planning.Binding, error) { return nil, boom }
		},
		"plans": func(d *Deps) {
			d.OpenPlans = func(context.Context, string, string) ([]*planning.Plan, error) { return nil, boom }
		},
		"last run": func(d *Deps) {
			d.LastRun = func(context.Context, string, string, string) (*deployments.Deployment, error) { return nil, boom }
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := facts{}.deps()
			broken(&d)
			if _, err := Of(context.Background(), d, "org_1", "env_1", "llama"); !errors.Is(err, boom) {
				t.Errorf("Of: err = %v, want %v", err, boom)
			}
		})
	}
}

func TestStateAsksForTheRightEnvironment(t *testing.T) {
	var seen []string
	record := func(parts ...string) { seen = append(seen, strings.Join(parts, "/")) }
	d := Deps{
		Bindings: func(_ context.Context, org, env string) ([]*planning.Binding, error) {
			record("bindings", org, env)
			return nil, nil
		},
		OpenPlans: func(_ context.Context, org, env string) ([]*planning.Plan, error) {
			record("plans", org, env)
			return nil, nil
		},
		LastRun: func(_ context.Context, org, env, resource string) (*deployments.Deployment, error) {
			record("last", org, env, resource)
			return nil, nil
		},
	}
	if _, err := Of(context.Background(), d, "org_1", "env_1", "llama"); err != nil {
		t.Fatal(err)
	}
	want := "last/org_1/env_1/llama bindings/org_1/env_1 plans/org_1/env_1"
	if got := strings.Join(seen, " "); got != want {
		t.Errorf("calls: %s, want %s", got, want)
	}
}
