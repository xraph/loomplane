package deployments

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	ctrlplane "github.com/xraph/ctrlplane"
	cpauth "github.com/xraph/ctrlplane/auth"
	"github.com/xraph/warden"
	wardenmemory "github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// agentAuthorizer is Warden's real engine with the organization's agent
// turned on.
func agentAuthorizer(t *testing.T, orgID string) *security.Authorizer {
	t.Helper()
	store := wardenmemory.New()
	eng, err := warden.NewEngine(warden.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	a := security.NewAuthorizer(eng, store, nil)
	if err = a.AssignAgent(t.Context(), orgID); err != nil {
		t.Fatal(err)
	}
	return a
}

// ctrlWriter is an executor that writes through Ctrl Plane as the job's
// principal before it deploys, as the Ctrl Plane executors do.
type ctrlWriter struct {
	*recorder
	provider cpauth.Provider
}

func (w *ctrlWriter) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	if _, err := cpauth.RequireAuthorized(infrastructure.WithPrincipal(ctx, principal(job)), w.provider, "instances.create", "inst_"+job.Action.Name, ""); err != nil {
		return nil, err
	}
	return w.recorder.Apply(ctx, job)
}

// runPlans records whether each plan read happened in a deployment run.
type runPlans struct {
	*fakePlans
	mu   sync.Mutex
	runs []bool
}

func (p *runPlans) GetPlan(ctx context.Context, orgID, id string) (*planning.Plan, error) {
	p.mu.Lock()
	p.runs = append(p.runs, security.InDeploymentRun(ctx))
	p.mu.Unlock()
	return p.fakePlans.GetPlan(ctx, orgID, id)
}

// A deployment the agent approved runs as the agent alone, and its Ctrl
// Plane writes pass only because the runner marks the run.
func TestAgentApprovedDeploymentWritesInfrastructure(t *testing.T) {
	plan := webAndAPI()
	plan.DecidedBy = security.AgentSubject(plan.OrgID)
	provider := infrastructure.NewAuthProvider()
	provider.Bind(func(context.Context, string) (string, string, error) { return "", "", errors.New("no sessions here") }, agentAuthorizer(t, plan.OrgID))
	exec := &ctrlWriter{recorder: &recorder{url: answering(t, http.StatusOK)}, provider: provider}
	r, store, plans := newRunner(t, plan, exec, nil)
	marked := &runPlans{fakePlans: plans}
	r.plans = marked

	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.GetDeployment(t.Context(), plan.OrgID, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusSucceeded || d.StartedBy != security.AgentSubject(plan.OrgID) {
		t.Fatalf("deployment %s by %s: %s", d.Status, d.StartedBy, d.Error)
	}
	if len(exec.jobs) != 2 {
		t.Fatalf("%d jobs deployed, want 2", len(exec.jobs))
	}
	// Every step, Finish's route handoff included, reads its plan inside
	// the run.
	if len(marked.runs) == 0 {
		t.Fatal("no plan reads")
	}
	for i, run := range marked.runs {
		if !run {
			t.Fatalf("plan read %d happened outside the deployment run", i)
		}
	}

	// The same write outside a deployment run is refused.
	ctx := infrastructure.WithPrincipal(t.Context(), security.AgentPrincipal(plan.OrgID, nil))
	if _, err = cpauth.RequireAuthorized(ctx, provider, "instances.create", "inst_api", ""); !errors.Is(err, ctrlplane.ErrForbidden) {
		t.Fatalf("an agent write outside a deployment run: %v, want forbidden", err)
	}
}
