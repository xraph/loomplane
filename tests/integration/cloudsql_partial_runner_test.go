//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
)

type partialSQLCredentials struct{ job *deployments.Job }

func (c partialSQLCredentials) Credential(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
	if org != c.job.Deployment.OrgID || id != c.job.Connection.ID {
		return nil, nil, errors.New("wrong cleanup account")
	}
	return c.job.Connection, c.job.Credential, nil
}

type partialSQLCleanupAuth struct{ allowed bool }

func (a *partialSQLCleanupAuth) ValidateCloudSync(context.Context, *planning.Plan) error { return nil }
func (a *partialSQLCleanupAuth) ValidateCloudSyncCleanup(_ context.Context, p *planning.Plan, id string) error {
	if !a.allowed || p.CloudSyncID != "forward" || id != "compensation" {
		return errors.New("cleanup denied")
	}
	return nil
}

func TestCloudSQLPartialCleanupRunnerRestartsFromStoredOwnership(t *testing.T) {
	testCloudSQLPartialCleanup(t, "rule-runner")
}

func runPartialSQLCleanup(t *testing.T, job *deployments.Job, exec *deployments.CloudSQL, network *sourceNetworkFixture, apply []byte, deletes *int) {
	t.Helper()
	_, conn, db := cloudSyncDatabase(t)
	ctx := t.Context()
	// Keep the scope used by Apply while using actual deployment/plan stores.
	for _, query := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('project','org','SQL','sql','ready','usr',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('environment','org','project','production','production',now(),now())`,
		`INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('sql-manifest','org','project',1,'version: 1','digest','usr',now())`,
		`INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at) VALUES('sql-plan','org','project','environment','production','approved','sql-manifest',1,'usr',now())`,
	} {
		if _, err := conn.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	action := job.Action
	action.Op = planning.Create
	action.Subject = planning.ResourceSubject
	action.ConnectionID = job.Connection.ID
	result, err := json.Marshal(planning.Result{Connections: map[string]string{}, CloudSyncID: "forward", Actions: []planning.Action{action}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `UPDATE loomplane_plans SET result=$1 WHERE id='sql-plan'`, result); err != nil {
		t.Fatal(err)
	}
	store := deployments.NewPgStore(db)
	dep := *job.Deployment
	dep.PlanID = "sql-plan"
	dep.Status = deployments.StatusFailed
	dep.StartedAt = time.Now()
	dep.StartedBy = "usr"
	dep.Steps = []deployments.Step{{ActionID: action.ID, Status: deployments.StepFailed, Checkpoint: apply}}
	if _, err = store.CreateDeployment(ctx, &dep); err != nil {
		t.Fatal(err)
	}
	read := func() deployments.Step {
		fresh, readErr := deployments.NewPgStore(db).GetDeployment(ctx, dep.OrgID, dep.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return fresh.Steps[0]
	}
	network.checkpoint = func() []byte {
		raw := read().Checkpoint
		var envelope struct {
			Replace       bool `json:"replace"`
			Apply, Remove []byte
		}
		if json.Unmarshal(raw, &envelope) == nil && envelope.Replace {
			raw = envelope.Apply
			if len(envelope.Remove) > 0 {
				raw = envelope.Remove
			}
		}
		return raw
	}
	auth := &partialSQLCleanupAuth{}
	cleanup := func() error {
		// Each invocation creates new stores, runner and provider sessions.
		runner := deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(db), Plans: planning.NewPgStore(db), Credentials: partialSQLCredentials{job}, Executors: map[string]deployments.Executor{"cloud-sql": &deployments.CloudSQL{Google: exec.Google}}, CloudSyncAuth: auth})
		return runner.CleanupRevertedCloudPlan(ctx, dep.OrgID, dep.PlanID, "compensation", func([]byte) error { return nil })
	}
	if err = cleanup(); err == nil || *deletes != 0 {
		t.Fatal("denied cleanup reached provider", err, *deletes)
	}
	auth.allowed = true
	network.scenario = "provision-remove-retry"
	network.lost = false
	if err = cleanup(); err == nil || *deletes != 1 {
		t.Fatal("lost deletion did not stop installed cleanup", err, *deletes)
	}
	step := read()
	if step.CloudCleanupComplete {
		t.Fatal("incomplete cleanup marked done")
	}
	var saved struct {
		Pending       *deployments.PendingCloudIdentity `json:"pending_identity"`
		Apply, Remove []byte
	}
	if err = json.Unmarshal(step.Checkpoint, &saved); err != nil || saved.Pending == nil || saved.Pending.PhysicalID == "" || len(saved.Apply) == 0 || len(saved.Remove) == 0 {
		t.Fatal("missing durable ownership or independent checkpoints", err)
	}
	if err = cleanup(); err != nil {
		t.Fatal("restart after source deletion", err)
	}
	if !read().CloudCleanupComplete || *deletes != 1 || len(network.objects) != 0 {
		t.Fatal("cleanup did not finish from stored ownership", *deletes, network.counts)
	}
	if err = cleanup(); err != nil || *deletes != 1 {
		t.Fatal("completed cleanup replay", err, *deletes)
	}
	bindings, err := planning.NewPgStore(db).ListBindings(ctx, dep.OrgID, dep.EnvironmentID)
	if err != nil || len(bindings) != 0 {
		t.Fatal("partial ownership published as a binding", err, bindings)
	}
}
