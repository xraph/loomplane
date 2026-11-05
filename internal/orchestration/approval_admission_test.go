package orchestration

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"

	"github.com/xraph/loomplane/internal/security"
)

func TestApprovalAdmissionLegacyDecisionEncoding(t *testing.T) {
	dec := Decision{Approve: true, Revision: "revision", By: "user"}
	raw, err := json.Marshal(dec)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"approve":true,"revision":"revision","by":"user"}` {
		t.Fatalf("legacy wire changed: %s", raw)
	}
	var reopened Decision
	if err = json.Unmarshal(raw, &reopened); err != nil || !reflect.DeepEqual(dec, reopened) {
		t.Fatalf("legacy read: %+v %v", reopened, err)
	}
	d := NewDeployments()
	for _, bad := range []Decision{{RequestID: "different"}, {GroupAdmission: "opaque"}} {
		if err := d.Decide(t.Context(), "plan", "request", bad); err == nil || !strings.Contains(err.Error(), "request") {
			t.Fatal("request mismatch reached durable engine")
		}
	}
}

type admissionPlanner struct {
	capacityPlanner
	decisions []Decision
}

func (p *admissionPlanner) ApplyDecision(_ context.Context, _, _ string, d Decision) (string, error) {
	p.decisions = append(p.decisions, d)
	return "approved", nil
}

func TestApprovalAdmissionSignalAllBuilds(t *testing.T) {
	for _, build := range []string{DeploymentBuild, CapacityBuild, CanaryBuild, ManagedWorkloadBuild, LegacyCleanupBuild, ManagedCleanupBuild} {
		t.Run(build, func(t *testing.T) {
			r := newRoutedWorkflows(t)
			r.d.BindRunStore(&memRuns{})
			planner := &admissionPlanner{}
			r.d.Bind(planner, newCanaryProbe(t), r.d.engineOf)
			var worker *drt.Worker
			switch build {
			case DeploymentBuild:
				var err error
				worker, err = drt.NewWorker(r.store, r.d.Options("admission-v2", 1))
				if err != nil {
					t.Fatal(err)
				}
			case CapacityBuild:
				worker = r.w3
			case CanaryBuild:
				worker = r.w4
			case ManagedWorkloadBuild:
				worker = r.w5
			case LegacyCleanupBuild:
				worker = r.w6
			case ManagedCleanupBuild:
				worker = r.w7
			}
			request := planRequest("admission-plan", build)
			if err := r.d.startRun(t.Context(), DeploymentRun{OrgID: "org", PlanID: "admission-plan", Request: request}); err != nil {
				t.Fatal(err)
			}
			pumpCanary(t, worker, nil)
			dec := Decision{Approve: true, Revision: "r1", By: "user", Actor: &security.Principal{UserID: "user", OrgID: "org"}, RequestID: "server-generated-request", GroupAdmission: "v1.opaque.payload.mac", Note: "retained\x00note"}
			if err := r.d.Decide(t.Context(), "admission-plan", dec.RequestID, dec); err != nil {
				t.Fatal(err)
			}
			// A fresh worker must replay the waiting workflow and retain exact bytes.
			var replayOptions drt.Options
			switch build {
			case DeploymentBuild:
				replayOptions = r.d.Options("admission-replay", 1)
			case CapacityBuild:
				replayOptions = r.d.CapacityOptions("admission-replay", 1)
			case CanaryBuild:
				replayOptions = r.d.CanaryOptions("admission-replay", 1)
			case ManagedWorkloadBuild:
				replayOptions = r.d.ManagedWorkloadOptions("admission-replay", 1)
			case LegacyCleanupBuild:
				replayOptions = r.d.LegacyCleanupOptions("admission-replay", 1)
			case ManagedCleanupBuild:
				replayOptions = r.d.ManagedCleanupOptions("admission-replay", 1)
			}
			replay, err := drt.NewWorker(r.store, replayOptions)
			if err != nil {
				t.Fatal(err)
			}
			pumpCanary(t, replay, nil)
			if len(planner.decisions) != 1 || !reflect.DeepEqual(planner.decisions[0], dec) {
				t.Fatalf("decision delivery: %#v", planner.decisions)
			}
			// Replaying completed work cannot replace or repeat the accepted activity.
			pumpCanary(t, worker, nil)
			if len(planner.decisions) != 1 {
				t.Fatal("decision activity repeated")
			}
			history, err := r.store.ReadHistory(t.Context(), request.Key, 0, 1000)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range history {
				if event.Type == durable.EventSignalReceived && strings.Contains(string(event.Payload), "server-generated-request") {
					found = true
				}
			}
			if !found {
				t.Fatal("exact request identity missing from history")
			}
		})
	}
}
