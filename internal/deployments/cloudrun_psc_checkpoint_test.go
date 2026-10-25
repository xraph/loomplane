package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/pkg/provider"
)

type applicationRouteObserver struct {
	subnetID   string
	endpointID string
	calls      int
}

func (o *applicationRouteObserver) ReadSQLApplicationNetwork(ctx context.Context, _ string, route provider.CloudSQLApplicationNetwork, check func(context.Context) error) (*gcp.SQLApplicationNetwork, error) {
	o.calls++
	if err := check(ctx); err != nil {
		return nil, err
	}
	return &gcp.SQLApplicationNetwork{Route: route, ID: o.subnetID, IPCIDR: "10.20.0.0/26"}, nil
}
func (o *applicationRouteObserver) ReadSQLPITRSourceEndpoint(ctx context.Context, _ gcp.SQLInstanceSpec, physical string, _ provider.CloudSQLSourceEndpoint, check func(context.Context) error) (*gcp.SQLSourceEndpoint, error) {
	o.calls++
	if err := check(ctx); err != nil {
		return nil, err
	}
	return &gcp.SQLSourceEndpoint{ID: o.endpointID, Address: "10.20.0.8", DNSName: "database.sql.goog", SourcePhysicalID: physical, SettingsDigest: "settings"}, nil
}

func TestCloudRunPSCRetryRequiresOriginalRouteWitness(t *testing.T) {
	for _, scenario := range []string{"stable", "observation-refreshed", "source-replaced", "subnet-replaced", "endpoint-replaced", "review-changed", "missing-witness", "checkpoint-failed", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			job := cloudRunPSCJob(t)
			psc, err := prepareCloudRunPSC(job)
			if err != nil {
				t.Fatal(err)
			}
			observer := &applicationRouteObserver{subnetID: "123", endpointID: "456"}
			var state cloudRunState
			var saved []byte
			save := func() error { var encodeErr error; saved, encodeErr = json.Marshal(state); return encodeErr }
			if checkErr := psc.check(t.Context(), observer, job, &state, save); checkErr != nil {
				t.Fatal(checkErr)
			}
			if len(saved) == 0 {
				t.Fatal("route witness not saved")
			}
			if decodeErr := json.Unmarshal(saved, &state); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			state.Image = "built-image"
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch scenario {
			case "observation-refreshed":
				now := time.Now()
				job.Attach[0].ObservedAt = &now
				job.Attach[0].UpdatedAt = now
				job.Attach[0].DriftMessage = "checked again"
				psc, err = prepareCloudRunPSC(job)
				if err != nil {
					t.Fatal(err)
				}
			case "source-replaced":
				job.Attach[0].PhysicalID = "replacement"
				psc, err = prepareCloudRunPSC(job)
				if err != nil {
					t.Fatal(err)
				}
			case "subnet-replaced":
				observer.subnetID = "789"
			case "endpoint-replaced":
				observer.endpointID = "789"
			case "review-changed":
				job.Plan.Revision = "replacement-review"
			case "missing-witness":
				state.PSC = nil
			case "checkpoint-failed":
				save = func() error { return errors.New("lease revoked") }
			case "canceled":
				cancel()
			}
			err = psc.check(ctx, observer, job, &state, save)
			if (err == nil) != (scenario == "stable" || scenario == "observation-refreshed") {
				t.Fatal("unexpected retry outcome", err)
			}
		})
	}
}

type cloudRunPSCRetryExecutor struct {
	CloudRun
	fixture  *Job
	observer *applicationRouteObserver
	resumed  bool
	result   error
}

func (e *cloudRunPSCRetryExecutor) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	var state cloudRunState
	if len(job.Resume) > 0 {
		if err := json.Unmarshal(job.Resume, &state); err != nil {
			return nil, err
		}
	}
	e.resumed = state.PSC != nil
	prepared, err := prepareCloudRunPSC(e.fixture)
	if err != nil {
		return nil, err
	}
	save := func() error {
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		return job.Checkpoint(raw)
	}
	e.result = prepared.check(ctx, e.observer, e.fixture, &state, save)
	if e.result != nil {
		return nil, e.result
	}
	return nil, errors.New("transient rollout failure")
}

func TestRunnerRetainsCloudRunPSCWitnessWithoutActivityResume(t *testing.T) {
	executor := &cloudRunPSCRetryExecutor{fixture: cloudRunPSCJob(t), observer: &applicationRouteObserver{subnetID: "123", endpointID: "456"}}
	plan := webAndAPI()
	plan.Actions = plan.Actions[:1]
	runner, store, _ := newRunner(t, plan, executor, nil)
	deployment, err := runAll(t, runner, plan)
	if err != nil {
		t.Fatal(err)
	}
	if reopened, err := store.Reopen(t.Context(), plan.OrgID, deployment); err != nil || !reopened {
		t.Fatal("failed deployment not reopened", err)
	}
	executor.observer.subnetID = "replacement"
	_ = runner.Run(t.Context(), plan.OrgID, deployment, "service:api", nil, func([]byte) error { return nil })
	if !executor.resumed || executor.result == nil || !strings.Contains(executor.result.Error(), "network or source changed") {
		t.Fatal("explicit retry forgot its original route", executor.resumed, executor.result)
	}
}
