package models

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
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
	// A failed read before the deletion is said, and the cleanup still
	// runs; failing again after it, the rows stay and that's said too.
	warned = nil
	after = (&Reaper{Store: failingList{rows}, Warn: r.Warn}).Reap(ctx, "org", projectID, "")
	if after == nil || len(warned) != 1 {
		t.Fatalf("failed read: cleanup %v, warnings %v", after != nil, warned)
	}
	after(ctx)
	if len(warned) != 2 || !strings.Contains(warned[1], "their rows stay") {
		t.Errorf("failed read after the deletion: %v", warned)
	}
}

// Ruling M27: the cleanup lists the rows again once the deletion has
// happened, so a deploy that wrote its row between the first read and the
// deletion leaves no orphan behind.
func TestReaperTakesARowWrittenDuringTheDeletion(t *testing.T) {
	ctx := context.Background()
	rows := &memDeployments{}
	r := &Reaper{Store: rows}
	after := r.Reap(ctx, "org", projectID, stagingID)
	if after == nil {
		t.Fatal("no cleanup for an environment with no models yet")
	}
	late := &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: stagingID, Resource: "late", EntryID: llama8BID,
		Quantization: "bf16", Engine: "vllm", Phase: PhasePlanned}
	kept := &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: productID, Resource: "kept", EntryID: llama8BID,
		Quantization: "bf16", Engine: "vllm", Phase: PhasePlanned}
	for _, d := range []*Deployment{late, kept} {
		if err := rows.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	after(ctx)
	left, _ := rows.List(ctx, "org")
	if len(left) != 1 || left[0].Resource != "kept" {
		t.Errorf("left %+v", left)
	}
}

// Ruling M27: an orphan row whose environment is gone never holds the
// Hugging Face token, so it can't block the token's delete.
func TestGatedDeploymentsSkipsAGoneEnvironment(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, []gpus.GPUTarget{l40sServer()})
	for _, env := range []string{stagingID, "env_gone"} {
		row := &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: env, Resource: "llama-8b", EntryID: llama8BID,
			Quantization: "bf16", Engine: "vllm", Phase: PhasePlanned}
		if err := d.rows.Create(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	gone := &Deployment{OrgID: "org", ProjectID: "prj_gone", EnvironmentID: "env_x", Resource: "llama-8b", EntryID: llama8BID,
		Quantization: "bf16", Engine: "vllm", Phase: PhaseWaitingForServer}
	if err := d.rows.Create(ctx, gone); err != nil {
		t.Fatal(err)
	}
	got, err := d.svc.GatedDeployments(ctx, "org")
	if err != nil || !slices.Equal(got, []string{"llama-8b in shop/staging"}) {
		t.Errorf("gated deployments %v, %v", got, err)
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

// repinnedLlama puts llama-8b in the manifest, running in staging, with
// the settings of a registry pin older than the one d's registry has.
func repinnedLlama(t *testing.T, d *deploySetup) {
	t.Helper()
	old := deployRegistry(t, llamaRev, "")
	e, _ := old.Entry(llama8BID)
	q, _ := e.Quantization("bf16")
	settings := engines.Settings(engines.Spec{Entry: e, Quantization: q, ServedName: llama8BID, MaxModelLen: 16384, GPUs: 1})
	mf := manifests.NewService(d.revisions, memProjects{}, aiAdmin, nil)
	if _, _, err := mf.Edit(context.Background(), member, projectID, manifests.EditInput{AddResource: &manifests.NewResource{
		Name: "llama-8b", Kind: recipes.ModelVLLMKind, Provider: "server", Target: gpuConn, Version: "0.31", Settings: settings,
		EnabledIn: "staging",
	}}); err != nil {
		t.Fatal(err)
	}
}

// staleLlama puts llama-8b in the manifest with the settings of a
// registry pin older than the one d's registry has, on gpu-box, with the
// environment blocks envs.
func staleLlama(t *testing.T, d *deploySetup, envs map[string]manifest.ResourceSettings) {
	t.Helper()
	old := deployRegistry(t, llamaRev, "")
	e, _ := old.Entry(llama8BID)
	q, _ := e.Quantization("bf16")
	putResource(t, d, "llama-8b", manifest.Resource{
		Kind: recipes.ModelVLLMKind, Provider: "server", Target: gpuConn, Version: "0.31", GPU: &manifest.GPU{Kind: "nvidia-l40s", Count: 1},
		Settings: engines.Settings(engines.Spec{Entry: e, Quantization: q, ServedName: llama8BID, MaxModelLen: 16384, GPUs: 1}), Environments: envs,
	})
}

// revisionIn is the weights revision env's copy of llama-8b runs.
func revisionIn(t *testing.T, d *deploySetup, env string) string {
	t.Helper()
	m := current(t, d)
	return m.Resources["llama-8b"].In(env, m.Defaults).Settings["revision"]
}

// Review Focus 4 and Decision 5: moving staging's copy to the new pin
// takes staging's permission alone and leaves production's copy, its
// plans and its weights alone.
func TestMoveToPinChangesOnlyItsEnvironment(t *testing.T) {
	ctx := context.Background()
	newRev := strings.Repeat("e", 40)
	d := newDeploy(t, deployRegistry(t, newRev, ""), deployer, []gpus.GPUTarget{l40sServer()})
	staleLlama(t, d, map[string]manifest.ResourceSettings{"staging": on(), "production": on()})
	production := &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: productID, Resource: "llama-8b", EntryID: llama8BID,
		Quantization: "bf16", Engine: "vllm", Phase: PhaseActive, PlanID: "plan_production"}
	if err := d.rows.Create(ctx, production); err != nil {
		t.Fatal(err)
	}
	edits := d.recordEdits()

	// ai:edit only, and production needs ai:manage.
	res, err := d.svc.MoveToPin(ctx, member, projectID, stagingID, "llama-8b")
	if err != nil {
		t.Fatal(err)
	}
	if len(edits.edits) != 1 || edits.edits[0].UpdateResource == nil {
		t.Fatalf("edits %+v", edits.edits)
	}
	u := edits.edits[0].UpdateResource
	if u.Resource != "llama-8b" || u.Environment != "staging" || u.Settings["revision"] != newRev || u.Settings["model"] != llama8BID ||
		u.Settings["file"] != "" || u.Target != nil || u.Provider != nil || u.GPU != nil || u.Enabled != nil {
		t.Errorf("update %+v", u)
	}
	if len(d.plans.asked) != 1 || d.plans.asked[0].Environment != "staging" || res.PlanID == "" {
		t.Errorf("plans %+v", d.plans.asked)
	}
	if got := revisionIn(t, d, "staging"); got != newRev {
		t.Errorf("staging runs %s", got)
	}
	if got := revisionIn(t, d, "production"); got != llamaRev {
		t.Errorf("production runs %s", got)
	}
	if got := current(t, d).Resources["llama-8b"].Settings["revision"]; got != llamaRev {
		t.Errorf("the top level says %s", got)
	}
	if after, _ := d.rows.Get(ctx, "org", projectID, productID, "llama-8b"); after.Revision != production.Revision || after.PlanID != "plan_production" {
		t.Errorf("production's row %+v", after)
	}
	if _, err = d.svc.MoveToPin(ctx, member, projectID, productID, "llama-8b"); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("ai:edit in production: %v", err)
	}
}

// Review Focus 4: each environment says whether its own copy has an
// update.
func TestUpdateAvailablePerEnvironment(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, strings.Repeat("e", 40), ""), deployer, []gpus.GPUTarget{l40sServer()}, boxes()...)
	staleLlama(t, d, map[string]manifest.ResourceSettings{"staging": on(), "production": on()})
	if _, err := d.svc.MoveToPin(ctx, member, projectID, stagingID, "llama-8b"); err != nil {
		t.Fatal(err)
	}
	staging, err := d.svc.Status(ctx, member, projectID, stagingID, "llama-8b")
	if err != nil || staging.UpdateAvailable || staging.Pin != nil {
		t.Errorf("staging: %+v, %v", staging, err)
	}
	production, err := d.svc.Status(ctx, member, projectID, productID, "llama-8b")
	if err != nil || !production.UpdateAvailable || production.Pin == nil || !strings.Contains(production.Pin.Message, "llama-8b runs") {
		t.Errorf("production: %+v, %v", production, err)
	}
	if pin, perr := d.svc.CheckPin(ctx, member, projectID, productID, "llama-8b"); perr != nil || !pin.UpdateAvailable {
		t.Errorf("production's pin: %+v, %v", pin, perr)
	}
}

// Decision 6: a pin move rewrites the top level when the environment is
// the last one that reads the pin from there, and its own block
// otherwise.
func TestMoveToPinLastReaderRewritesTheTopLevel(t *testing.T) {
	ctx := context.Background()
	newRev := strings.Repeat("e", 40)
	d := newDeploy(t, deployRegistry(t, newRev, ""), deployer, []gpus.GPUTarget{l40sServer()})
	staleLlama(t, d, map[string]manifest.ResourceSettings{"staging": on(), "production": on()})
	if _, err := d.svc.MoveToPin(ctx, member, projectID, stagingID, "llama-8b"); err != nil {
		t.Fatal(err)
	}
	edits := d.recordEdits()
	d.svc.d.Authz = aiAdmin
	if _, err := d.svc.MoveToPin(ctx, member, projectID, productID, "llama-8b"); err != nil {
		t.Fatal(err)
	}
	if len(edits.edits) != 1 || edits.edits[0].UpdateResource.Environment != "" || edits.edits[0].UpdateResource.Settings["revision"] != newRev {
		t.Fatalf("production's edit %+v", edits.edits[0].UpdateResource)
	}
	if len(d.plans.asked) != 2 || d.plans.asked[1].Environment != "production" {
		t.Errorf("plans %+v", d.plans.asked)
	}
	llama := current(t, d).Resources["llama-8b"]
	if llama.Settings["revision"] != newRev || llama.Environments["staging"].Settings["revision"] != newRev || revisionIn(t, d, "staging") != newRev {
		t.Errorf("after production's move %+v", llama)
	}

	// A model in one environment keeps today's YAML: the top level.
	d = newDeploy(t, deployRegistry(t, newRev, ""), deployer, []gpus.GPUTarget{l40sServer()})
	staleLlama(t, d, map[string]manifest.ResourceSettings{"staging": on()})
	edits = d.recordEdits()
	if _, err := d.svc.MoveToPin(ctx, member, projectID, stagingID, "llama-8b"); err != nil {
		t.Fatal(err)
	}
	if u := edits.edits[0].UpdateResource; u.Environment != "" || current(t, d).Resources["llama-8b"].Environments["staging"].Settings != nil {
		t.Errorf("one environment: %+v", u)
	}

	// Production's own stale override moves in production's block, under
	// staging's current top level.
	d = newDeploy(t, deployRegistry(t, newRev, ""), aiAdmin, []gpus.GPUTarget{l40sServer()})
	production := on()
	production.Settings = map[string]string{"revision": llamaRev}
	staleLlama(t, d, map[string]manifest.ResourceSettings{"staging": on(), "production": production})
	m := *current(t, d)
	top := m.Resources["llama-8b"]
	top.Settings = maps.Clone(top.Settings)
	top.Settings["revision"] = newRev
	putResource(t, d, "llama-8b", top)
	edits = d.recordEdits()
	if _, err := d.svc.MoveToPin(ctx, member, projectID, productID, "llama-8b"); err != nil {
		t.Fatal(err)
	}
	if u := edits.edits[0].UpdateResource; u.Environment != "production" || revisionIn(t, d, "production") != newRev {
		t.Errorf("production's override: %+v", u)
	}
}

func TestSoleReader(t *testing.T) {
	raw := manifest.Resource{Target: "c1", Settings: map[string]string{"model": "m", "revision": "r"}, Environments: map[string]manifest.ResourceSettings{
		"staging": on(), "production": {Enabled: on().Enabled, Target: "c2", Settings: map[string]string{"model": "m2", "revision": "r2"}},
		"dev": {Target: "c3"}, // off: it reads nothing
	}}
	for _, c := range []struct {
		env  string
		keys []string
		want bool
	}{
		{"staging", []string{"target"}, true},                        // production has its own target
		{"staging", []string{"model", "revision", "file"}, true},     // and its own pin; nobody sets file
		{"production", []string{"target"}, false},                    // its block sets it
		{"staging", []string{"max_model_len"}, true},                 // the top level doesn't set it
		{"staging", []string{"gpu", "provider"}, true},               // nor these
		{"production", []string{"model", "revision", "file"}, false}, // its block sets them
	} {
		if got := soleReader(raw, c.env, c.keys...); got != c.want {
			t.Errorf("soleReader(%s, %v) = %v", c.env, c.keys, got)
		}
	}
	raw.Settings["max_model_len"] = "8192"
	if soleReader(raw, "staging", "max_model_len") {
		t.Error("production reads the top-level context")
	}
}

// Ruling M27: a move whose plan fails leaves the row saying what to do,
// as a deploy's does.
func TestMoveToPinSaysWhatToDoWhenPlanningFails(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, strings.Repeat("e", 40), ""), deployer, []gpus.GPUTarget{l40sServer()})
	repinnedLlama(t, d)
	row := &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: stagingID, Resource: "llama-8b", EntryID: llama8BID,
		Quantization: "bf16", Engine: "vllm", Phase: PhaseActive}
	if err := d.rows.Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	d.plans.err = errors.New("the planner is down")
	got, err := d.svc.MoveToPin(ctx, member, projectID, stagingID, "llama-8b")
	var perr *PlanAfterEditError
	if !errors.As(err, &perr) || !perr.Repin {
		t.Fatalf("plan failure: %v", err)
	}
	want := "The manifest has llama-8b on the registry's new pin now. Plan staging from the project to update it."
	if got == nil || got.Deployment == nil || got.Deployment.Message != want {
		t.Errorf("result %+v", got)
	}
	if stored, _ := d.rows.Get(ctx, "org", projectID, stagingID, "llama-8b"); stored.Message != want {
		t.Errorf("stored message %q", stored.Message)
	}
}

// requireCounter is an Authorizer that grants perms and counts what was
// required of it, so a test can tell an answer from an action.
type requireCounter struct {
	perms
	mu       sync.Mutex
	required []security.Permission
}

func (r *requireCounter) Require(ctx context.Context, p security.Principal, perm security.Permission, id string) error {
	r.mu.Lock()
	r.required = append(r.required, perm)
	r.mu.Unlock()
	return r.perms.Require(ctx, p, perm, id)
}

// Ruling M27: Preview only reads. Whether the caller may deploy is asked
// with Allowed, which never repairs a member's roles, not Require.
func TestPreviewAsksWithoutRepairing(t *testing.T) {
	authz := &requireCounter{perms: deployer}
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), authz, []gpus.GPUTarget{l40sServer()})
	d.grantLlama()
	in := DeployInput{EntryID: llama8BID, Target: onGPU("nvidia-l40s")}
	pv, err := d.svc.Preview(context.Background(), member, projectID, stagingID, in)
	if err != nil || !pv.CanDeploy {
		t.Fatalf("staging: can deploy %v, %v", pv != nil && pv.CanDeploy, err)
	}
	if pv, err = d.svc.Preview(context.Background(), member, projectID, productID, in); err != nil || pv.CanDeploy {
		t.Fatalf("production: %v", err)
	}
	if slices.Contains(authz.required, security.AIEdit) || slices.Contains(authz.required, security.AIManage) {
		t.Errorf("Preview required %v", authz.required)
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
