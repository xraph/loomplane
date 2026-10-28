package models

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

// AI settings asks before it deletes the token which deployments download
// gated weights with it, a row still waiting for its server among them.
func TestGatedDeploymentsNamesThem(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, []gpus.GPUTarget{l40sServer()}, cpuServer())
	d.grantLlama()
	if _, err := d.svc.Deploy(ctx, member, projectID, productID, DeployInput{EntryID: llama8BID, Target: onGPU("nvidia-l40s")}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.svc.Deploy(ctx, member, projectID, stagingID, DeployInput{EntryID: bgeSmall, Target: TargetChoice{Source: TargetConnection, ConnectionID: cpuConn}}); err != nil {
		t.Fatal(err)
	}
	waiting := &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: stagingID, Resource: "llama-70b", EntryID: llama70BID,
		Quantization: "awq-int4", Engine: "vllm", Phase: PhaseWaitingForServer}
	if err := d.rows.Create(ctx, waiting); err != nil {
		t.Fatal(err)
	}
	got, err := d.svc.GatedDeployments(ctx, "org")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"llama-3-1-8b-instruct in shop/production", "llama-70b in shop/staging"}) {
		t.Errorf("gated deployments %v", got)
	}
	if got, err = d.svc.GatedDeployments(ctx, "org_other"); err != nil || len(got) != 0 {
		t.Errorf("another workspace: %v, %v", got, err)
	}
}

// failingList is a Store whose ListProject fails.
type failingList struct{ *memDeployments }

func (failingList) ListProject(context.Context, string, string) ([]*Deployment, error) {
	return nil, errors.New("the database is down")
}

// Ruling M24: deleting an environment or project deletes its model rows,
// once it's gone, and says first which were waiting for a GPU server that
// may still be on its way.
func TestReaperDeletesADeletedEnvironmentsRows(t *testing.T) {
	ctx := context.Background()
	rows := &memDeployments{}
	add := func(env, resource, phase, operation string) {
		t.Helper()
		d := &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: env, Resource: resource, EntryID: llama8BID,
			Quantization: "bf16", Engine: "vllm", Phase: phase, ServerCloudID: "cloud_1", ServerOperationID: operation, ServerName: "gpu-1"}
		if err := rows.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	add(stagingID, "llama-8b", PhaseWaitingForServer, "op_1")
	add(stagingID, "bge", PhasePlanned, "")
	add(productID, "llama-8b", PhaseActive, "")
	other := &Deployment{OrgID: "org", ProjectID: "prj_other", EnvironmentID: "env_x", Resource: "llama-8b", EntryID: llama8BID,
		Quantization: "bf16", Engine: "vllm", Phase: PhasePlanned}
	if err := rows.Create(ctx, other); err != nil {
		t.Fatal(err)
	}

	var warned []string
	r := &Reaper{Store: rows, Warn: func(msg string, err error) { warned = append(warned, msg+": "+err.Error()) }}
	after := r.Reap(ctx, "org", projectID, stagingID)
	if len(warned) != 1 || !strings.Contains(warned[0], "llama-8b was waiting for a GPU server") || !strings.Contains(warned[0], "gpu-1") ||
		!strings.Contains(warned[0], "op_1") {
		t.Fatalf("warnings %v", warned)
	}
	// Nothing goes before the deletion has happened.
	if len(rows.rows) != 4 {
		t.Fatalf("%d rows before the deletion", len(rows.rows))
	}
	after(ctx)
	left, _ := rows.List(ctx, "org")
	names := make([]string, 0, len(left))
	for _, d := range left {
		names = append(names, d.ProjectID+"/"+d.EnvironmentID+"/"+d.Resource)
	}
	if !slices.Equal(names, []string{"prj_other/env_x/llama-8b", projectID + "/" + productID + "/llama-8b"}) {
		t.Errorf("left %v", names)
	}

	// The whole project.
	r.Reap(ctx, "org", projectID, "")(ctx)
	if left, _ = rows.List(ctx, "org"); len(left) != 1 || left[0].ProjectID != "prj_other" {
		t.Errorf("after the project: %+v", left)
	}
	// Nothing to do, and a failed read, return nothing to run.
	if f := r.Reap(ctx, "org", "prj_empty", ""); f != nil {
		t.Error("a project with no models got a cleanup")
	}
	warned = nil
	if f := (&Reaper{Store: failingList{rows}, Warn: r.Warn}).Reap(ctx, "org", projectID, ""); f != nil || len(warned) != 1 {
		t.Errorf("failed read: cleanup %v, warnings %v", f != nil, warned)
	}
}

// Rulings M21 and M23: after a registry re-pin, a model's settings name
// the old weights. Its status says an update is available, not an error,
// and one click moves it to the new pin and plans the update.
func TestMoveToTheNewPin(t *testing.T) {
	ctx := context.Background()
	oldRev, newRev := llamaRev, strings.Repeat("e", 40)
	d := newDeploy(t, deployRegistry(t, newRev, ""), deployer, []gpus.GPUTarget{l40sServer()})

	// llama-8b was deployed in staging when the registry pinned oldRev.
	old := deployRegistry(t, oldRev, "")
	e, _ := old.Entry(llama8BID)
	q, _ := e.Quantization("bf16")
	settings := engines.Settings(engines.Spec{Entry: e, Quantization: q, ServedName: llama8BID, MaxModelLen: 16384, GPUs: 1})
	mf := manifests.NewService(d.revisions, memProjects{}, aiAdmin, nil)
	if _, _, err := mf.Edit(ctx, member, projectID, manifests.EditInput{AddResource: &manifests.NewResource{
		Name: "llama-8b", Kind: recipes.ModelVLLMKind, Provider: "server", Target: gpuConn, Version: "0.31", Settings: settings,
		EnabledIn: "staging",
	}}); err != nil {
		t.Fatal(err)
	}

	// Planning it fails with the fix; its status calls that an update.
	var repinned *RepinnedError
	if err := d.svc.pinOf("llama-8b", current(t, d).Resources["llama-8b"]); !errors.As(err, &repinned) {
		t.Fatalf("pin check: %v", err)
	}
	st, err := d.svc.CheckPin(ctx, member, projectID, stagingID, "llama-8b")
	if err != nil || !st.UpdateAvailable || st.Revision != newRev || !strings.Contains(st.Message, "Move it to the new pin") {
		t.Fatalf("status %+v, %v", st, err)
	}

	got, err := d.svc.MoveToPin(ctx, member, projectID, stagingID, "llama-8b")
	if err != nil {
		t.Fatal(err)
	}
	res := current(t, d).Resources["llama-8b"]
	if res.Settings["revision"] != newRev || res.Settings["model"] != llama8BID || res.Settings["max_model_len"] != "16384" ||
		res.Settings["served_name"] != llama8BID {
		t.Errorf("settings %v", res.Settings)
	}
	if got.PlanID == "" || len(d.plans.asked) != 1 || d.plans.asked[0].Environment != "staging" {
		t.Errorf("result %+v plans %+v", got, d.plans.asked)
	}
	if len(d.audit.events) != 1 || d.audit.events[0].Action != "ai.model.repin" {
		t.Errorf("audit %+v", d.audit.events)
	}
	if st, err = d.svc.CheckPin(ctx, member, projectID, stagingID, "llama-8b"); err != nil || st.UpdateAvailable {
		t.Errorf("after the move: %+v, %v", st, err)
	}
	if _, err = d.svc.MoveToPin(ctx, member, projectID, stagingID, "llama-8b"); !errors.Is(err, ErrInvalid) {
		t.Errorf("moving again: %v", err)
	}

	// It runs in staging only, and production needs ai:manage anyway.
	if _, err = d.svc.MoveToPin(ctx, member, projectID, productID, "llama-8b"); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("developer in production: %v", err)
	}
	if _, err = d.svc.CheckPin(ctx, member, projectID, productID, "llama-8b"); !errors.Is(err, ErrNotFound) {
		t.Errorf("status in production: %v", err)
	}
	if _, err = d.svc.CheckPin(ctx, member, projectID, stagingID, "api"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a service: %v", err)
	}
}

// A re-pin, even wrapped, is an update; any other error stays an error.
func TestPinStatusOfOtherErrors(t *testing.T) {
	if st, err := pinStatusOf(nil); err != nil || st.UpdateAvailable {
		t.Errorf("nil: %+v, %v", st, err)
	}
	boom := errors.New("boom")
	if _, err := pinStatusOf(boom); !errors.Is(err, boom) {
		t.Errorf("other error: %v", err)
	}
	wrapped := errors.Join(errors.New("plan"), &RepinnedError{Resource: "x", Entry: "e", Quantization: "q", Model: "m", Revision: "r"})
	if st, err := pinStatusOf(wrapped); err != nil || !st.UpdateAvailable || st.Model != "m" {
		t.Errorf("wrapped re-pin: %+v, %v", st, err)
	}
}
