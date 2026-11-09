package deployments

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/planning"
)

func externalAzureCanary(t *testing.T) *azureCanaryEnv {
	t.Helper()
	e := newAzureCanaryEnv(t)
	e.job.Action.Service.Env = []planning.EnvLink{{Key: "TOKEN", From: "db", Subject: planning.ResourceSubject}}
	e.job.Plan.Actions = []planning.Action{{Name: "db", Subject: planning.ResourceSubject, Op: planning.Update, Resource: &planning.ResourceConfig{ExistingResource: &planning.ExistingResourceConfig{ResourceID: "external-db"}}}}
	e.job.Binding.Config, _ = json.Marshal(e.job.Action.Service)
	return e
}

func TestAzureExternalCanaryRecoversOldImageWithApprovedCredentials(t *testing.T) {
	e := externalAzureCanary(t)
	target := e.start(t)
	if target.Baseline == e.baseline {
		t.Fatal("the canary still ramps from the baseline's old external credentials")
	}
	recovery := e.f.Revisions(e.app)[target.Baseline]
	env := templateEnv(recovery.Template)
	if recovery.Image != e.oldImage || env["MODE"] != "slow" || !strings.HasPrefix(env["TOKEN"], "secret:") || e.f.Secrets(e.app)[strings.TrimPrefix(env["TOKEN"], "secret:")] != "new-secret" {
		t.Fatalf("recovery image %s, environment %v", recovery.Image, env)
	}
	if old, present := e.f.Secrets(e.app)[e.oldSecret]; present && old != "old-secret" {
		t.Fatal("the old baseline's app secret was overwritten")
	}
	e.weigh(t, target, 60)
	if err := e.revert(t, target); err != nil {
		t.Fatal(err)
	}
	if split := e.split(); !maps.Equal(split, map[string]int{target.Baseline: 100}) {
		t.Fatalf("revert traffic %v", split)
	}
	if e.f.Secrets(e.app)[strings.TrimPrefix(env["TOKEN"], "secret:")] != "new-secret" {
		t.Fatal("revert pruned approved recovery credentials")
	}
	e.job.Deployment.Retries++
	e.job.Deployment.Canary = &Canary{Services: []CanaryService{{ActionID: e.job.Action.ID, Baseline: target.Baseline}}}
	retry := e.start(t)
	if retry.Baseline != target.Baseline {
		t.Fatalf("retry changed recovery baseline from %s to %s", target.Baseline, retry.Baseline)
	}
	if _, err := e.exec.Promote(t.Context(), e.job, retry); err != nil {
		t.Fatal(err)
	}
	if split := e.split(); !maps.Equal(split, map[string]int{retry.Candidate: 100}) {
		t.Fatalf("promotion traffic %v", split)
	}
}

func TestAzureExternalCanaryFailedStartRetainsRecordedSourceForRetry(t *testing.T) {
	e := externalAzureCanary(t)
	e.job.Deployment.Canary = &Canary{Services: []CanaryService{{ActionID: e.job.Action.ID, Baseline: e.baseline}}}
	e.f.FailImage = e.newImage
	if _, err := e.exec.ApplyCandidate(t.Context(), e.job); err == nil {
		t.Fatal("a failed candidate started")
	}
	if !e.f.Revisions(e.app)[e.baseline].Active {
		t.Fatal("failed preparation deactivated the recorded source baseline")
	}
	e.f.FailImage = ""
	e.job.Deployment.Retries++
	retry := e.start(t)
	if retry.Baseline == e.baseline {
		t.Fatal("retry used the historical external credentials")
	}
}

func TestAzureExternalCanaryRetryNeverRoutesHistoricalCredentials(t *testing.T) {
	e := externalAzureCanary(t)
	e.job.Deployment.Canary = &Canary{Services: []CanaryService{{ActionID: e.job.Action.ID, Baseline: e.baseline}}}
	e.f.FailImage = e.newImage
	if _, err := e.exec.ApplyCandidate(t.Context(), e.job); err == nil {
		t.Fatal("a failed candidate started")
	}
	serving := e.split()
	if serving[e.baseline] > 0 || len(serving) != 1 {
		t.Fatalf("failed start serves historical credentials: %v", serving)
	}
	patches := len(e.lastPatches())
	e.f.FailImage = ""
	e.job.Deployment.Retries++
	e.f.FailAfter("POST listsecrets", 1)
	if _, err := e.exec.ApplyCandidate(t.Context(), e.job); err == nil {
		t.Fatal("the injected second preparation failure was ignored")
	}
	if split := e.split(); !maps.Equal(split, serving) {
		t.Fatalf("second preparation failure restored historical access: %v", split)
	}
	for _, patch := range e.lastPatches()[patches:] {
		for _, item := range trafficOf(patch) {
			weight := item.(map[string]any)
			if weight["revisionName"] == e.baseline && weight["weight"].(float64) > 0 {
				t.Fatal("retry temporarily routed historical credentials")
			}
		}
	}
	retry := e.start(t)
	if serving[retry.Baseline] != 100 {
		t.Fatalf("retry failed to reuse approved recovery baseline: %s", retry.Baseline)
	}
}

func TestAzureExternalCanaryRetryRefusesUnsettledOrForeignTraffic(t *testing.T) {
	for _, scenario := range []string{"split", "foreign"} {
		t.Run(scenario, func(t *testing.T) {
			e := externalAzureCanary(t)
			e.job.Deployment.Canary = &Canary{Services: []CanaryService{{ActionID: e.job.Action.ID, Baseline: e.baseline}}}
			e.f.FailImage = e.newImage
			if _, err := e.exec.ApplyCandidate(t.Context(), e.job); err == nil {
				t.Fatal("a failed candidate started")
			}
			recovery := ""
			for name := range e.split() {
				recovery = name
			}
			session, err := azureSession(t.Context(), e.exec.Client, e.job.Connection, e.job.Credential)
			if err != nil {
				t.Fatal(err)
			}
			traffic := []azure.TrafficWeight{{RevisionName: recovery, Weight: 50}, {RevisionName: e.baseline, Weight: 50}}
			if scenario == "foreign" {
				if err := session.RestoreTemplate(t.Context(), e.appID, recovery, "foreign"); err != nil {
					t.Fatal(err)
				}
				traffic = []azure.TrafficWeight{{RevisionName: e.app + "--foreign", Weight: 100}}
			}
			if err := session.SetTraffic(t.Context(), e.appID, azure.MultipleRevisions, traffic); err != nil {
				t.Fatal(err)
			}
			before, patches := e.split(), len(e.lastPatches())
			e.f.FailImage = ""
			e.job.Deployment.Retries++
			if _, err := e.exec.ApplyCandidate(t.Context(), e.job); err == nil {
				t.Fatal("retry replaced traffic without a validated recovery baseline")
			}
			if after := e.split(); !maps.Equal(after, before) || len(e.lastPatches()) != patches {
				t.Fatalf("retry changed unsettled traffic: before %v, after %v", before, after)
			}
		})
	}
}

func TestAzureExternalCanaryFailedRecoveryDoesNotMoveTraffic(t *testing.T) {
	e := externalAzureCanary(t)
	e.f.FailImage = e.oldImage
	if _, err := e.exec.ApplyCandidate(t.Context(), e.job); err == nil {
		t.Fatal("an unhealthy recovery baseline started")
	}
	if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) {
		t.Fatalf("failed recovery moved traffic: %v", split)
	}
	if e.f.Secrets(e.app)[e.oldSecret] != "old-secret" {
		t.Fatal("failed recovery overwrote the serving baseline's credential")
	}
}

func TestAzureExternalCanaryRemovesHistoricalResourceVariables(t *testing.T) {
	e := externalAzureCanary(t)
	e.job.Action.Service.Env = nil
	e.job.Plan.Actions[0].Op = planning.Delete
	delete(e.job.Env, "TOKEN")
	target := e.start(t)
	if target.Baseline == e.baseline {
		t.Fatal("resource removal retained the historical baseline")
	}
	if env := templateEnv(e.f.Revisions(e.app)[target.Baseline].Template); env["TOKEN"] != "" || env["MODE"] != "slow" {
		t.Fatalf("recovery retained a removed resource variable: %v", env)
	}
	if err := e.revert(t, target); err != nil {
		t.Fatal(err)
	}
	app := e.f.App(e.app)
	if env := templateEnv(app["properties"].(map[string]any)["template"].(map[string]any)); env["TOKEN"] != "" {
		t.Fatalf("revert restored a removed resource variable: %v", env)
	}
}

func TestAzureExternalCanaryRemovesLastResourceWithoutAnExternalAction(t *testing.T) {
	e := externalAzureCanary(t)
	var previous planning.ServiceConfig
	if err := json.Unmarshal(e.job.Binding.Config, &previous); err != nil {
		t.Fatal(err)
	}
	previous.TargetAccountIdentity = "azure/subscription"
	e.job.Binding.Config, _ = json.Marshal(previous)
	e.job.Action.Service.Env = nil
	e.job.Action.Service.TargetAccountIdentity = ""
	e.job.Plan.Actions[0].Op = planning.Delete
	e.job.Plan.Actions[0].Resource = nil
	delete(e.job.Env, "TOKEN")
	target := e.start(t)
	if target.Baseline == e.baseline {
		t.Fatal("removing the last attachment kept historical external credentials")
	}
	if env := templateEnv(e.f.Revisions(e.app)[target.Baseline].Template); env["TOKEN"] != "" {
		t.Fatalf("removed credentials remain: %v", env)
	}
}
