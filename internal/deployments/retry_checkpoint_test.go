package deployments

import (
	"errors"
	"net/http"
	"testing"
)

type retainedPlaced struct{ *placed }

func (*retainedPlaced) RetainCheckpointOnRetry() bool { return true }

func TestRunnerPreservesAnOldProvidersCheckpointOnFirstMove(t *testing.T) {
	log := &moveLog{}
	old := &retainedPlaced{&placed{provider: "cloud-run", log: log, removeErr: []error{errors.New("removal still running")}}}
	next := &placed{provider: "vercel", log: log, url: answering(t, http.StatusOK)}
	plan := apiMoves()
	runner, store, _, _ := moveRunner(t, plan, onCloudRun(), map[string]Executor{"cloud-run": old, "vercel": next})
	id, err := runAll(t, runner, plan)
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.GetDeployment(t.Context(), "org_1", id)
	if err != nil {
		t.Fatal(err)
	}
	step := d.Step("service:api")
	if step == nil || len(step.Checkpoint) == 0 || string(readReplaceCheckpoint(step.Checkpoint).Remove) != "rm-cloud-run" {
		t.Fatal("the first move lost the old provider's retained checkpoint")
	}
	if err = runner.Run(t.Context(), "org_1", id, "service:api", nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(old.removes) != 2 || string(old.removes[1].Resume) != "rm-cloud-run" || len(next.applies) != 1 {
		t.Fatal("retry did not resume only the original cleanup")
	}
}
