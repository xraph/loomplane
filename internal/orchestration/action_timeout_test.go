package orchestration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
)

// timedDeployer is capacityProbe knowing one action waits hours to start.
type timedDeployer struct {
	capacityProbe
	asked string
}

func (p *timedDeployer) ActionTimeouts(_ context.Context, _, deploymentID string) (map[string]time.Duration, error) {
	p.asked = deploymentID
	return map[string]time.Duration{"first": 3*time.Hour + 15*time.Minute, "second": time.Minute}, nil
}

func TestLongStartingActionsGetLongerSteps(t *testing.T) {
	input, err := json.Marshal(DeploymentInput{OrgID: "org", PlanID: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	d := NewDeployments()
	timed := &timedDeployer{capacityProbe: capacityProbe{calls: map[string]int{}}}
	d.deployer = timed
	raw, err := d.deployStart(t.Context(), drt.ActivityInfo{}, input)
	if err != nil {
		t.Fatal(err)
	}
	var started deployStarted
	if err = json.Unmarshal(raw, &started); err != nil {
		t.Fatal(err)
	}
	if timed.asked != "dep" || started.timeout("first") != 3*time.Hour+15*time.Minute {
		t.Fatalf("asked %q, first gets %s", timed.asked, started.timeout("first"))
	}
	// Never less than a step usually gets.
	if started.timeout("second") != actionTimeout || started.timeout("other") != actionTimeout {
		t.Fatalf("second gets %s, other %s", started.timeout("second"), started.timeout("other"))
	}

	// A deployer that knows none, or a run recorded before timeouts, gives
	// every step the usual time.
	d.deployer = &capacityProbe{calls: map[string]int{}}
	if raw, err = d.deployStart(t.Context(), drt.ActivityInfo{}, input); err != nil {
		t.Fatal(err)
	}
	var plain, old deployStarted
	if err = json.Unmarshal(raw, &plain); err != nil || plain.Timeouts != nil || plain.timeout("first") != actionTimeout {
		t.Fatalf("plain: %+v, %v", plain, err)
	}
	if err = json.Unmarshal([]byte(`{"deployment_id":"dep","steps":[["first"]]}`), &old); err != nil || old.timeout("first") != actionTimeout {
		t.Fatalf("old: %+v, %v", old, err)
	}

	// A Cloud sync's deployments get theirs in the prepare activity's output.
	d = NewDeployments()
	timed.asked = ""
	d.deployer, d.cloudSync = timed, syncPreparer{}
	sync, err := json.Marshal(cloudSyncInput{OrgID: "org", ProjectID: "project", SyncID: "sync"})
	if err != nil {
		t.Fatal(err)
	}
	info := drt.ActivityInfo{Key: durable.Key{WorkflowID: "sync.attempt.1"}}
	if raw, err = d.syncPrepare(t.Context(), info, sync); err != nil {
		t.Fatal(err)
	}
	var prepared CloudSyncPreparation
	if err = json.Unmarshal(raw, &prepared); err != nil || len(prepared.Deployments) != 1 {
		t.Fatalf("prepared %+v, %v", prepared, err)
	}
	moved := prepared.Deployments[0]
	steps := deployStarted{DeploymentID: moved.DeploymentID, Steps: moved.Steps, Timeouts: moved.Timeouts}
	if timed.asked != "sync-dep" || steps.timeout("first") != 3*time.Hour+15*time.Minute || steps.timeout("second") != actionTimeout {
		t.Fatalf("asked %q, sync steps %+v", timed.asked, moved)
	}
	d.deployer = nil
	if _, err = d.syncPrepare(t.Context(), info, sync); err == nil {
		t.Fatal("a sync prepared its deployments without a deployer to time them")
	}
}

// syncPreparer prepares one Cloud sync deployment.
type syncPreparer struct{}

func (syncPreparer) ReconcileCloudSync(context.Context, string, string, string, func([]byte) error) error {
	return nil
}
func (syncPreparer) RecoverCloudSyncs(context.Context) error { return nil }
func (syncPreparer) CloudSyncNeedsCapacity(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (syncPreparer) PrepareCapacityCloudSync(context.Context, string, string, string) (CloudSyncPreparation, error) {
	return CloudSyncPreparation{Deployments: []CloudSyncDeployment{{PlanID: "plan", DeploymentID: "sync-dep", Steps: [][]string{{"first", "second"}}}}}, nil
}
func (syncPreparer) FinishCapacityCloudSync(context.Context, string, string, string, func([]byte) error) error {
	return nil
}

func TestRetryNeedsTheDeployerForItsTimeouts(t *testing.T) {
	d := NewDeployments()
	if err := d.Retry(t.Context(), "org", "dep", "retry", [][]string{{"first"}}); err == nil || err.Error() != "the deployment workflow has no deployer yet" {
		t.Fatalf("retry without a deployer: %v", err)
	}
	timed := &timedDeployer{capacityProbe: capacityProbe{calls: map[string]int{}}}
	d.deployer = timed
	// There's no engine to start it on, but the timeouts were asked for first.
	if err := d.Retry(t.Context(), "org", "dep", "retry", [][]string{{"first"}}); err == nil || timed.asked != "dep" {
		t.Fatalf("retry: asked %q, %v", timed.asked, err)
	}
}
