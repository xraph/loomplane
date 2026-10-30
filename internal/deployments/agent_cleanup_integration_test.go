//go:build integration

package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	cpauth "github.com/xraph/ctrlplane/auth"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// ctrlRemover is an executor whose unfinished SQL instance is removed
// through Ctrl Plane as the job's principal.
type ctrlRemover struct {
	provider cpauth.Provider
	removed  []string
}

func (*ctrlRemover) Apply(context.Context, *Job) (*Outcome, error) {
	return nil, errors.New("cleanup never applies")
}

func (c *ctrlRemover) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	if _, err := cpauth.RequireAuthorized(infrastructure.WithPrincipal(ctx, principal(job)), c.provider, "instances.delete", b.ExternalID, ""); err != nil {
		return err
	}
	c.removed = append(c.removed, b.ExternalID)
	return nil
}

func (*ctrlRemover) Observe(context.Context, *connections.Connection, []byte, *planning.Binding) (*Observation, error) {
	return &Observation{}, nil
}

func (*ctrlRemover) PendingCloudResource(context.Context, *Job) (string, error) {
	return "sql_pending", nil
}

type allowApprover struct{}

func (allowApprover) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

// A person cleans up the SQL instance a failed deployment the agent
// approved left behind. The removal runs as the agent, the deployment's
// approver, and Ctrl Plane lets it.
func TestAgentApprovedFailedCleanupWritesInfrastructure(t *testing.T) {
	pg := reapDatabase(t)
	ctx := t.Context()
	org := "org_1"
	for _, q := range []string{
		`INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('agent-manifest','org_1','proj_1',1,'version: 1','digest','usr_1',now())`,
		`INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at) VALUES('agent-plan','org_1','proj_1','env_1','production','approved','agent-manifest',1,'usr_1',now())`,
	} {
		if _, err := pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	plan := &planning.Plan{ID: "agent-plan", OrgID: org, ProjectID: "proj_1", EnvironmentID: "env_1", Environment: "production",
		Status: planning.StatusApproved, Executable: true, Catalog: true, DecidedBy: security.AgentSubject(org),
		Actions: []planning.Action{{ID: "resource:db", Op: planning.Create, Subject: planning.ResourceSubject, Name: "db", Provider: "cloud-sql", ConnectionID: "conn_g", Step: 1,
			Resource: &planning.ResourceConfig{}}}}
	provider := infrastructure.NewAuthProvider()
	provider.Bind(func(context.Context, string) (string, string, error) { return "", "", errors.New("no sessions here") }, agentAuthorizer(t, org))
	exec := &ctrlRemover{provider: provider}
	store := NewPgStore(pg)
	r := NewRunner(RunnerDeps{
		Store: store, Plans: &fakePlans{plan: plan, bindings: map[string]*planning.Binding{}}, Credentials: fakeCredentials{},
		Authz: allowApprover{}, Providers: available(t), Executors: map[string]Executor{"cloud-sql": exec},
	})
	finished := time.Now()
	if _, err := store.CreateDeployment(ctx, &Deployment{ID: "agent-deployment", OrgID: org, ProjectID: "proj_1", EnvironmentID: "env_1", Environment: "production",
		PlanID: plan.ID, Status: StatusCancelled, StartedBy: plan.DecidedBy, StartedAt: finished, FinishedAt: &finished,
		Steps: []Step{{ActionID: "resource:db", Name: "db", Subject: string(planning.ResourceSubject), Op: string(planning.Create), Provider: "cloud-sql", Order: 1, Status: StepFailed, Checkpoint: []byte(`{"op":"create"}`)}}}); err != nil {
		t.Fatal(err)
	}
	cleanups := NewFailedCleanups(r, store, nil)
	_, _, _, revision, err := cleanups.candidate(ctx, org, "agent-deployment", true)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := json.Marshal(security.Principal{UserID: "usr_ada", OrgID: org})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.Exec(ctx, `INSERT INTO loomplane_failed_cleanups(org_id,deployment_id,revision,actor,checkpoint) VALUES($1,'agent-deployment',$2,$3,$4)`, org, revision, actor, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err = cleanups.RunFailedCleanup(ctx, org, "agent-deployment", 1, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	c, err := cleanups.load(ctx, org, "agent-deployment")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "succeeded" || len(exec.removed) != 1 || exec.removed[0] != "sql_pending" {
		t.Fatalf("cleanup %s (%s), removed %v", c.Status, c.Error, exec.removed)
	}
}
