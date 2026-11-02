package deployments

import (
	"errors"
	"slices"
	"testing"

	"github.com/xraph/loomplane/pkg/manifest"
)

func TestLocationPlacementFailedReplacementRetainsServingSource(t *testing.T) {
	log := &moveLog{}
	oldExec := &placed{provider: "cloud-run", log: log}
	next := &placed{provider: "vercel", log: log, applyErr: errors.New("replacement unavailable")}
	plan := apiMoves()
	plan.Actions[0].Service.Placement = &manifest.PlacementConstraints{Runtime: "managed"}
	r, _, plans, _ := moveRunner(t, plan, onCloudRun(), map[string]Executor{"cloud-run": oldExec, "vercel": next})
	if _, err := runAll(t, r, plan); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(log.list(), []string{"apply vercel"}) || len(next.applies) != 1 || next.applies[0].Action.Service.Placement == nil {
		t.Fatalf("replacement lost constraints or removed source: %v", log.list())
	}
	if b := plans.bindings["service:api"]; b.Provider != "cloud-run" || b.ExternalID != "ext-old" {
		t.Fatalf("serving source replaced after failure: %+v", b)
	}
}
