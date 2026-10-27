package orchestration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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
}
