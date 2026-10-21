//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/ctl"
	"github.com/xraph/loomplane/internal/planning"
	apiclient "github.com/xraph/loomplane/pkg/client"
)

// This acceptance uses the public API and CLI, persisted PostgreSQL state,
// encrypted Vault inputs, Dispatch, and the actual Vercel and Render adapters.
// The provider endpoints are HTTP fixtures, not live provider accounts.
func TestCloudFlowIsolationAdoptionRecoveryAndProviderReplacement(t *testing.T) {
	vc := newCloudAPIVercel(t)
	rd := newFakeRender(t, "rnd_flow_0123456789")
	cfg, r, c, _, project := cloudAPISetup(t)
	r.stop()
	cfg.Clouds.VercelEndpoint, cfg.Clouds.RenderEndpoint = vc.srv.URL, rd.srv.URL
	r = start(t, cfg)
	defer func() { r.stop() }()
	c.base = r.srv.URL
	base := "/api/v1/projects/" + project
	analyze(t, cfg, c, project)
	connect := func(provider, token string) string {
		t.Helper()
		var result struct{ ID, Status string }
		c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": provider, "name": map[string]string{"old-token-0123456789": "Original Vercel", "new-token-0123456789": "Replacement Vercel", "docs-token-0123456789": "Docs Vercel", "rnd_flow_0123456789": "Render"}[token], "credential": token}, &result)
		if result.Status != "ready" {
			t.Fatalf("connection not ready: %+v", result)
		}
		return result.ID
	}
	old := connect("vercel", "old-token-0123456789")
	renderID := connect("render", "rnd_flow_0123456789")
	var cloud cloudprofiles.Profile
	c.must(http.StatusCreated, http.MethodPost, base+"/clouds", cloudprofiles.CreateInput{Name: "Production"}, &cloud)
	targets := base + "/clouds/" + cloud.ID + "/targets"
	createTarget := func(name, conn string) cloudprofiles.Target {
		t.Helper()
		var target cloudprofiles.Target
		c.must(http.StatusCreated, http.MethodPost, targets, cloudprofiles.CreateTargetInput{Name: name, Provider: "vercel", ConnectionID: conn}, &target)
		return target
	}
	frontend := createTarget("frontend", old)
	var envs struct{ Environments []struct{ ID, Name string } }
	c.must(http.StatusOK, http.MethodGet, base+"/environments", nil, &envs)
	env := ""
	for _, e := range envs.Environments {
		if e.Name == "production" {
			env = e.ID
		}
	}
	if env == "" {
		t.Fatal("production missing")
	}
	assignment := base + "/environments/" + env + "/cloud"
	var preview cloudprofiles.Preview
	c.must(http.StatusOK, http.MethodPost, assignment+"/preview", apiclient.EnvironmentCloudInput{CloudID: cloud.ID}, &preview)
	if got := waitCloudAPI(t, c, base, applyCloudAPI(t, c, base, &preview, "assign").ID); got.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("assignment: %+v", got)
	}
	direct := "version: 1\nservices:\n  web:\n    source: node:apps/web\n    provider: vercel\nignore:\n  resources: [postgres, redis]\n"
	deploy := func(manifest string) planBody {
		t.Helper()
		c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": manifest}, nil)
		var p planBody
		c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &p)
		waitPlanned(t, c, &p)
		if !p.Executable {
			t.Fatalf("unexecutable plan: %+v", p)
		}
		if got := approveAndWait(t, c, p); got.Status != "succeeded" {
			t.Fatalf("deployment: %+v", got)
		}
		return p
	}
	bindings := func() map[string]planning.Binding {
		t.Helper()
		var result struct{ Bindings []planning.Binding }
		c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &result)
		out := map[string]planning.Binding{}
		for _, b := range result.Bindings {
			out[b.Name] = b
		}
		return out
	}
	count := func(token string) int {
		vc.accounts[token].mu.Lock()
		defer vc.accounts[token].mu.Unlock()
		return len(vc.accounts[token].deployed)
	}
	deploy(direct)
	before := bindings()
	if len(before) != 1 || before["web"].ConnectionID != old {
		t.Fatalf("initial bindings: %+v", before)
	}
	managed := strings.Replace(direct, "provider: vercel", "cloud_target: frontend", 1)
	adopted := deploy(managed)
	for _, action := range adopted.Actions {
		if action.Op != string(planning.Keep) {
			t.Fatalf("adoption mutated provider: %+v", action)
		}
	}
	afterAdoption := bindings()
	for name, b := range afterAdoption {
		var config planning.ServiceConfig
		if err := json.Unmarshal(b.Config, &config); err != nil {
			t.Fatal(err)
		}
		if config.CloudPlacement == nil || config.CloudPlacement.CloudID != cloud.ID || b.ExternalID != before[name].ExternalID || b.Revision != before[name].Revision {
			t.Fatalf("adoption lost placement: %+v", b)
		}
	}
	if count("old-token-0123456789") != 1 {
		t.Fatal("adoption called provider")
	}
	docs := connect("vercel", "docs-token-0123456789")
	docsTarget := createTarget("docs", docs)
	managed = strings.Replace(managed, "ignore:", "  docs:\n    source: node:apps/web\n    cloud_target: docs\nignore:", 1)
	deploy(managed)
	afterAdoption = bindings()
	if len(afterAdoption) != 2 || afterAdoption["docs"].ConnectionID != docs || count("old-token-0123456789") != 1 || count("docs-token-0123456789") != 1 {
		t.Fatalf("two-target deployment: %+v", afterAdoption)
	}
	var assignedBefore apiclient.EnvironmentCloud
	c.must(http.StatusOK, http.MethodGet, assignment, nil, &assignedBefore)
	replacement := connect("vercel", "new-token-0123456789")
	var assignedAfter apiclient.EnvironmentCloud
	c.must(http.StatusOK, http.MethodGet, assignment, nil, &assignedAfter)
	var targetAfter cloudprofiles.Target
	c.must(http.StatusOK, http.MethodGet, targets+"/"+frontend.ID, nil, &targetAfter)
	if !reflect.DeepEqual(assignedBefore, assignedAfter) || !reflect.DeepEqual(afterAdoption, bindings()) || targetAfter.ConnectionID != old || targetAfter.Revision != 1 || count("new-token-0123456789") != 0 {
		t.Fatal("connecting an account changed deployment or assignment")
	}
	// Keep a newer manifest and variable pending. Migration must replay applied inputs.
	c.must(http.StatusCreated, http.MethodPost, base+"/variables", map[string]any{"key": "UNAPPROVED_VALUE", "value": "draft", "environment": "production"}, nil)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": strings.Replace(managed, "cloud_target: frontend", "cloud_target: frontend\n    health_path: /unapproved", 1)}, nil)
	switchTo := func(provider, conn string, revision int64, key string) *cloudprofiles.Sync {
		t.Helper()
		c.must(http.StatusOK, http.MethodPost, base+"/cloud-changes/preview", cloudprofiles.ChangeInput{CloudID: cloud.ID, TargetID: frontend.ID, Revision: revision, Provider: provider, ConnectionID: conn}, &preview)
		if len(preview.Blockers) != 0 || len(preview.Environments) != 1 || preview.Environments[0].Summary.Replace != 1 {
			t.Fatalf("switch preview: %+v", preview)
		}
		return applyCloudAPI(t, c, base, &preview, key)
	}
	cleanupRelease := filepath.Join(t.TempDir(), "allow-cloud-cleanup")
	vc.mu.Lock()
	vc.deleteFailures = -1
	vc.deleteRelease = cleanupRelease
	vc.mu.Unlock()
	switched := switchTo("vercel", replacement, 1, "account-switch")
	got := waitCloudAPI(t, c, base, switched.ID)
	if got.Status == cloudprofiles.SyncRouting {
		c.must(http.StatusAccepted, http.MethodPost, base+"/cloud-syncs/"+got.ID+"/routing", apiclient.CompleteCloudRoutingInput{EnvironmentID: env, Evidence: "Fixture route verified"}, nil)
		got = waitCloudAPI(t, c, base, switched.ID)
	}
	if got.Status != cloudprofiles.SyncCleanup || got.Error == "" {
		t.Fatalf("cleanup failure not persisted: %+v", got)
	}
	serving := bindings()
	if !reflect.DeepEqual(serving["docs"], afterAdoption["docs"]) || serving["web"].ConnectionID != replacement || serving["web"].CommitSHA != afterAdoption["web"].CommitSHA {
		t.Fatalf("switch changed unrelated target or source: %+v", serving)
	}
	var applied planning.ServiceConfig
	if err := json.Unmarshal(serving["web"].Config, &applied); err != nil {
		t.Fatal(err)
	}
	var originalApplied planning.ServiceConfig
	if err := json.Unmarshal(afterAdoption["web"].Config, &originalApplied); err != nil {
		t.Fatal(err)
	}
	if applied.AppliedInputs == nil || originalApplied.AppliedInputs == nil || applied.AppliedInputs.ArtifactKey == "" || applied.AppliedInputs.ArtifactKey != originalApplied.AppliedInputs.ArtifactKey || !reflect.DeepEqual(applied.AppliedInputs.Source, originalApplied.AppliedInputs.Source) {
		t.Fatal("migration changed the applied source or artifact")
	}
	if applied.HealthPath != "" {
		t.Fatalf("draft manifest applied during switch: %+v", applied)
	}
	vc.accounts["new-token-0123456789"].mu.Lock()
	draft := vc.accounts["new-token-0123456789"].env["UNAPPROVED_VALUE"]
	vc.accounts["new-token-0123456789"].mu.Unlock()
	if draft != nil {
		t.Fatal("draft variable applied during switch")
	}
	c.must(http.StatusConflict, http.MethodDelete, "/api/v1/connections/"+old, nil, nil)
	c.must(http.StatusConflict, http.MethodDelete, targets+"/"+frontend.ID, nil, nil)
	c.must(http.StatusConflict, http.MethodDelete, base+"/clouds/"+cloud.ID, nil, nil)
	// Retry through loomctl, against the persisted failed sync.
	var cliKey keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "flow-cli", "access": "write"}, &cliKey)
	var output bytes.Buffer
	command := ctl.New(ctl.Options{Out: &output, ConfigPath: filepath.Join(t.TempDir(), "loomctl.json"), Getenv: func(key string) string {
		if key == ctl.EnvURL {
			return c.base
		}
		if key == ctl.EnvKey {
			return cliKey.Secret
		}
		return ""
	}})
	c.must(http.StatusOK, http.MethodGet, base+"/cloud-syncs/"+switched.ID, nil, &got)
	if got.Status != cloudprofiles.SyncCleanup || got.Error == "" {
		t.Fatalf("cleanup failure escaped before CLI retry: %+v", got)
	}
	attemptBeforeCLI := got.Attempt
	if err := command.Run([]string{"cloud-syncs", "retry", project, switched.ID, "--wait", "--timeout", "1m", "--json"}); err == nil {
		t.Fatal("CLI retry ignored the held cleanup failure")
	}
	var failedRetry cloudprofiles.Sync
	if err := json.Unmarshal(output.Bytes(), &failedRetry); err != nil {
		t.Fatal(err)
	}
	if failedRetry.ID != switched.ID || failedRetry.Status != cloudprofiles.SyncCleanup || failedRetry.Error == "" || failedRetry.Attempt <= attemptBeforeCLI {
		t.Fatalf("CLI did not execute failed retry: %+v", failedRetry)
	}
	t.Logf("CLI retry persisted cleanup failure at attempt %d", failedRetry.Attempt)
	output.Reset()
	// Ordinary runs release only after an actual failed CLI retry. Held runs
	// leave release and the successful retry to the browser, then follow it.
	recoveryCommand := "retry"
	if os.Getenv("LOOMPLANE_CLOUD_FLOW_HOLD") != "" {
		holdCloudFlow(t, c.base, project, cloud.ID, frontend.ID, env, switched.ID, cleanupRelease)
		recoveryCommand = "wait"
	} else if err := os.WriteFile(cleanupRelease, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := command.Run([]string{"cloud-syncs", recoveryCommand, project, switched.ID, "--wait", "--timeout", "1m", "--json"}); err != nil {
		t.Fatal(err)
	}
	var successfulRetry cloudprofiles.Sync
	if err := json.Unmarshal(output.Bytes(), &successfulRetry); err != nil {
		t.Fatal(err)
	}
	if successfulRetry.ID != switched.ID || successfulRetry.Status != cloudprofiles.SyncSucceeded || successfulRetry.Attempt <= failedRetry.Attempt {
		t.Fatalf("cleanup recovery did not finish: %+v", successfulRetry)
	}
	t.Logf("Cleanup recovery completed at attempt %d through CLI %s", successfulRetry.Attempt, recoveryCommand)
	vc.mu.Lock()
	removed := append([]string{}, vc.removed...)
	vc.mu.Unlock()
	if len(removed) != 1 || removed[0] != "old-token-0123456789" || count("new-token-0123456789") != 1 || count("docs-token-0123456789") != 1 {
		t.Fatalf("retry redeployed or cleaned wrong account: %v", removed)
	}
	var unchanged cloudprofiles.Target
	c.must(http.StatusOK, http.MethodGet, targets+"/"+docsTarget.ID, nil, &unchanged)
	if !reflect.DeepEqual(unchanged, docsTarget) || !reflect.DeepEqual(serving["docs"], bindings()["docs"]) {
		t.Fatal("docs target changed during frontend migration")
	}
	// Compatible Next.js workload moves from Vercel serverless to Render Node.
	replaced := switchTo("render", renderID, 2, "provider-switch")
	got = waitCloudAPI(t, c, base, replaced.ID)
	if got.Status == cloudprofiles.SyncRouting {
		c.must(http.StatusAccepted, http.MethodPost, base+"/cloud-syncs/"+got.ID+"/routing", apiclient.CompleteCloudRoutingInput{EnvironmentID: env, Evidence: "Render fixture route verified"}, nil)
		got = waitCloudAPI(t, c, base, replaced.ID)
	}
	if got.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("provider replacement: %+v", got)
	}
	renderBinding := bindings()["web"]
	if renderBinding.Provider != "render" || renderBinding.ConnectionID != renderID || renderBinding.CommitSHA != serving["web"].CommitSHA || !reflect.DeepEqual(bindings()["docs"], serving["docs"]) {
		t.Fatalf("provider replacement lost inputs/isolation: %+v", renderBinding)
	}
	rd.mu.Lock()
	commits := append([]string{}, rd.deploys[renderBinding.ExternalID]...)
	rd.mu.Unlock()
	if len(commits) != 1 || commits[0] != renderBinding.CommitSHA {
		t.Fatalf("Render did not deploy applied commit: %v", commits)
	}
	// A terminal provider build failure retains the serving Render binding.
	vc.mu.Lock()
	vc.deployFailure = true
	vc.mu.Unlock()
	failed := switchTo("vercel", replacement, 3, "failed-return")
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(50 * time.Millisecond) {
		c.must(http.StatusOK, http.MethodGet, base+"/cloud-syncs/"+failed.ID, nil, &got)
		if got.Status == cloudprofiles.SyncFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("build failure not recorded: %+v", got)
		}
	}
	if got.Error == "" || !reflect.DeepEqual(renderBinding, bindings()["web"]) {
		t.Fatalf("failed build replaced serving binding: %+v", got)
	}
	c.must(http.StatusOK, http.MethodPost, base+"/cloud-syncs/"+failed.ID+"/revert/preview", nil, &preview)
	if preview.Reverts != failed.ID || len(preview.Blockers) != 0 {
		t.Fatalf("revert preview: %+v", preview)
	}
	var compensation cloudprofiles.Sync
	c.must(http.StatusAccepted, http.MethodPost, base+"/cloud-syncs/revert/apply", apiclient.ApplyCloudInput{PreviewID: preview.ID, Revision: preview.Revision, IdempotencyKey: "revert-failed-return"}, &compensation)
	if got = waitCloudAPI(t, c, base, compensation.ID); got.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("compensation: %+v", got)
	}
	c.must(http.StatusOK, http.MethodGet, base+"/cloud-syncs/"+failed.ID, nil, &got)
	c.must(http.StatusOK, http.MethodGet, targets+"/"+frontend.ID, nil, &targetAfter)
	if got.Status != cloudprofiles.SyncReverted || targetAfter.Provider != "render" || targetAfter.ConnectionID != renderID || bindings()["web"].ExternalID != renderBinding.ExternalID || !reflect.DeepEqual(bindings()["docs"], serving["docs"]) {
		t.Fatalf("revert did not restore placement: %+v %+v", got, targetAfter)
	}
	// Even a read-only recent preview keeps its referenced Cloud protected.
	var reviewed cloudprofiles.Profile
	c.must(http.StatusCreated, http.MethodPost, base+"/clouds", cloudprofiles.CreateInput{Name: "Recently reviewed"}, &reviewed)
	c.must(http.StatusOK, http.MethodPost, assignment+"/preview", apiclient.EnvironmentCloudInput{CloudID: reviewed.ID, Revision: 1}, &preview)
	c.must(http.StatusConflict, http.MethodDelete, base+"/clouds/"+reviewed.ID, nil, nil)
	// A fresh unreferenced Cloud and target can be removed through public APIs.
	var disposable cloudprofiles.Profile
	c.must(http.StatusCreated, http.MethodPost, base+"/clouds", cloudprofiles.CreateInput{Name: "Disposable"}, &disposable)
	var unused cloudprofiles.Target
	disposableTargets := base + "/clouds/" + disposable.ID + "/targets"
	c.must(http.StatusCreated, http.MethodPost, disposableTargets, cloudprofiles.CreateTargetInput{Name: "unused", Provider: "vercel", ConnectionID: old}, &unused)
	c.must(http.StatusNoContent, http.MethodDelete, disposableTargets+"/"+unused.ID, nil, nil)
	c.must(http.StatusNotFound, http.MethodGet, disposableTargets+"/"+unused.ID, nil, nil)
	c.must(http.StatusNoContent, http.MethodDelete, base+"/clouds/"+disposable.ID, nil, nil)
	c.must(http.StatusNotFound, http.MethodGet, base+"/clouds/"+disposable.ID, nil, nil)
}

func holdCloudFlow(t *testing.T, origin, project, cloud, target, env, syncID, cleanupRelease string) {
	t.Helper()
	duration, err := time.ParseDuration(os.Getenv("LOOMPLANE_CLOUD_FLOW_HOLD"))
	if err != nil || duration <= 0 || duration > 2*time.Hour {
		t.Fatal("LOOMPLANE_CLOUD_FLOW_HOLD must be between zero and two hours")
	}
	stop := filepath.Join(t.TempDir(), "stop-cloud-flow")
	details := map[string]string{"allow_cleanup_file": cleanupRelease, "api_url": origin, "project_id": project, "cloud_id": cloud, "target_id": target, "environment_id": env, "sync_id": syncID, "stop_file": stop, "email": "clouds@example.test", "password": "Correct-horse-battery-9"}
	data, err := json.MarshalIndent(details, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	location := filepath.Join("..", "..", ".superpowers", "sdd", "2026-10-18-cloud-targets", "task-6-fixture.json")
	if err = os.WriteFile(location, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("Cleanup-pending browser fixture %s, metadata %s", origin, location)
	for deadline := time.Now().Add(duration); time.Now().Before(deadline); time.Sleep(time.Second) {
		if _, err = os.Stat(stop); err == nil {
			return
		}
		select {
		case <-t.Context().Done():
			return
		default:
		}
	}
}
