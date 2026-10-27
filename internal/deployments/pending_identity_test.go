package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
)

type pendingIdentityExecutor struct {
	recorder
	resolve func(*Job) (*PendingCloudIdentity, error)
	remove  func(*Job, *PendingCloudIdentity) error
}

func (e *pendingIdentityExecutor) ResolvePendingCloudResource(_ context.Context, j *Job) (*PendingCloudIdentity, error) {
	return e.resolve(j)
}
func (e *pendingIdentityExecutor) RemovePendingCloudResource(_ context.Context, j *Job, p *PendingCloudIdentity) error {
	return e.remove(j, p)
}

func TestPartialCleanupPersistsIdentityAndSeparateProgressBeforeRemoval(t *testing.T) {
	for _, op := range []planning.Op{planning.Replace, planning.Create} {
		t.Run(string(op), func(t *testing.T) { testPartialCleanupIdentity(t, op) })
	}
}
func testPartialCleanupIdentity(t *testing.T, op planning.Op) {
	r, plan, plans, _, _ := cloudRunner(t)
	plan.CloudSyncID = "forward"
	r.cloudSyncAuth = pendingCleanupAuth{}
	a := &plan.Actions[0]
	a.Op, a.Subject, a.Service = op, planning.ResourceSubject, nil
	a.Resource = &planning.ResourceConfig{Kind: "postgres", Version: "17"}
	st := r.store.(*memStore)
	st.d["partial"] = &Deployment{ID: "partial", OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, PlanID: plan.ID, Status: StatusFailed, Steps: []Step{{ActionID: a.ID, Status: StepFailed, Checkpoint: []byte(`{"created":true}`)}}}
	calls, removes := 0, 0
	want := &PendingCloudIdentity{ExternalID: "pending", PhysicalID: "incarnation", OwnedResources: map[string]json.RawMessage{"psc": json.RawMessage(`{"partial":true}`)}, Proof: json.RawMessage(`{"created":true}`)}
	r.executors[a.Provider] = &pendingIdentityExecutor{resolve: func(j *Job) (*PendingCloudIdentity, error) {
		calls++
		if string(j.Resume) != `{"created":true}` {
			t.Fatal("lost apply checkpoint")
		}
		if err := j.Checkpoint([]byte(`{"reconciled":true}`)); err != nil {
			return nil, err
		}
		return want, nil
	}, remove: func(j *Job, p *PendingCloudIdentity) error {
		removes++
		saved := readReplaceCheckpoint(st.d["partial"].Steps[0].Checkpoint)
		if saved.PendingIdentity == nil || saved.PendingIdentity.PhysicalID != "incarnation" || string(saved.Apply) != `{"reconciled":true}` || p.PhysicalID != "incarnation" {
			t.Fatal("removal before durable identity", saved)
		}
		if removes == 1 {
			if err := j.Checkpoint([]byte(`{"delete":"started"}`)); err != nil {
				return err
			}
			return errors.New("lost delete response")
		}
		if string(j.Resume) != `{"delete":"started"}` {
			t.Fatal("lost removal checkpoint", string(j.Resume))
		}
		return nil
	}}
	if err := r.CleanupRevertedCloudPlan(t.Context(), plan.OrgID, plan.ID, "compensation", func([]byte) error { return nil }); err == nil {
		t.Fatal("lost delete response ignored")
	}
	if st.d["partial"].Steps[0].CloudCleanupComplete {
		t.Fatal("failed cleanup marked complete")
	}
	if err := r.CleanupRevertedCloudPlan(t.Context(), plan.OrgID, plan.ID, "compensation", func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || removes != 2 || !st.d["partial"].Steps[0].CloudCleanupComplete || len(plans.bindings) != 0 {
		t.Fatal("cleanup replay or partial binding publication", calls, removes)
	}
}

func TestPartialCleanupRefusesUndurableIdentity(t *testing.T) {
	for _, failure := range []string{"heartbeat", "store"} {
		t.Run(failure, func(t *testing.T) {
			r, plan, _, _, _ := cloudRunner(t)
			plan.CloudSyncID = "forward"
			r.cloudSyncAuth = pendingCleanupAuth{}
			a := plan.Actions[0]
			a.Op = planning.Create
			job := &Job{Plan: plan, Action: a, Deployment: &Deployment{ID: "partial", OrgID: plan.OrgID}, cleanupSyncID: "compensation"}
			writes := 0
			r.executors[a.Provider] = &pendingIdentityExecutor{resolve: func(*Job) (*PendingCloudIdentity, error) {
				return &PendingCloudIdentity{ExternalID: "created", PhysicalID: "native", Proof: json.RawMessage(`{"scope":"original"}`)}, nil
			}, remove: func(*Job, *PendingCloudIdentity) error { writes++; return nil }}
			refusal := errors.New("persistence refused")
			if failure == "store" {
				r.store = &pendingRefusingStore{Store: r.store, err: refusal}
			}
			save := r.pendingCheckpoint(t.Context(), job, &Step{ActionID: a.ID}, func([]byte) error {
				if failure == "heartbeat" {
					return refusal
				}
				return nil
			})
			if _, err := r.resolvePendingIdentity(t.Context(), job, &replaceCheckpoint{}, save); !errors.Is(err, refusal) {
				t.Fatal("identity accepted without durable evidence", err)
			}
			if writes != 0 || job.pendingIdentity != nil {
				t.Fatal("removal enabled before persistence")
			}
		})
	}
}

type pendingRefusingStore struct {
	Store
	err error
}

func (s *pendingRefusingStore) UpdateStep(context.Context, string, string, Step) error { return s.err }
