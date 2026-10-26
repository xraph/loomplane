package deployments

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/azure/azuretest"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

const azureDeployment = "dep_01jxcanary0000000000000000"

// azureCanaryEnv is an Azure executor against the fake, with an app an
// ordinary deploy made, whose one revision is the baseline, and a job
// that releases a new image and new values beside it.
type azureCanaryEnv struct {
	f        *azuretest.Fake
	exec     *Azure
	job      *Job
	app      string
	appID    string
	baseline string
	oldImage string
	newImage string
	// oldSecret is the app secret the baseline reads TOKEN from.
	oldSecret string
}

func newAzureCanaryEnv(t *testing.T) *azureCanaryEnv {
	t.Helper()
	f := azuretest.New(t)
	exec := &Azure{Client: azure.New(f.URL())}
	job := azureJob(t, apiConfig())
	job.Deployment.ID = azureDeployment
	job.Action.Provider = "azure-container-apps"
	job.Connection.Name = "Prod"
	job.Env = map[string]string{"MODE": "slow", "TOKEN": "old-secret"}
	job.Secrets = map[string]bool{"TOKEN": true}
	out, err := exec.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	e := &azureCanaryEnv{f: f, exec: exec, job: job, app: AzureName(job), appID: out.ExternalID, oldImage: out.Image}
	e.baseline = e.app + "--1"
	e.oldSecret = azureSecretName("TOKEN")
	job.Binding = &planning.Binding{ExternalID: out.ExternalID, Region: "westeurope", Revision: out.Image}
	job.Env = map[string]string{"MODE": "fast", "TOKEN": "new-secret"}
	// The new image is built already, as an earlier attempt checkpointed it.
	e.newImage = strings.TrimSuffix(out.Image, doApproved[:12]) + "new"
	job.Resume = []byte(`{"image":"` + e.newImage + `"}`)
	return e
}

func (e *azureCanaryEnv) start(t *testing.T) CanaryTarget {
	t.Helper()
	target, err := e.exec.ApplyCandidate(t.Context(), e.job)
	if err != nil {
		t.Fatal(err)
	}
	return *target
}

func (e *azureCanaryEnv) split() map[string]int { return e.f.Split(e.app) }

func (e *azureCanaryEnv) weigh(t *testing.T, target CanaryTarget, percent int) {
	t.Helper()
	if err := e.exec.SetWeights(t.Context(), e.job, target, percent); err != nil {
		t.Fatal(err)
	}
}

// revert is what the runner does to revert: no traffic for the
// candidate, then Revert.
func (e *azureCanaryEnv) revert(t *testing.T, target CanaryTarget) error {
	t.Helper()
	if err := e.exec.SetWeights(t.Context(), e.job, target, 0); err != nil {
		return err
	}
	return e.exec.Revert(t.Context(), e.job, target)
}

// calls are the container app calls made since the nth.
func (e *azureCanaryEnv) calls(n int) []string {
	e.f.Lock()
	defer e.f.Unlock()
	return slices.Clone(e.f.Calls[n:])
}

func (e *azureCanaryEnv) callCount() int {
	e.f.Lock()
	defer e.f.Unlock()
	return len(e.f.Calls)
}

// lastPut is the body of the last container app PUT.
func (e *azureCanaryEnv) lastPut() map[string]any {
	e.f.Lock()
	defer e.f.Unlock()
	return e.f.AppPuts[len(e.f.AppPuts)-1]
}

// lastPatches are the container app PATCHes so far.
func (e *azureCanaryEnv) lastPatches() []map[string]any {
	e.f.Lock()
	defer e.f.Unlock()
	return slices.Clone(e.f.AppPatches)
}

// lastPatch is the body of the last container app PATCH.
func (e *azureCanaryEnv) lastPatch() map[string]any {
	e.f.Lock()
	defer e.f.Unlock()
	return e.f.AppPatches[len(e.f.AppPatches)-1]
}

// assertBaselineAlone checks the baseline takes everything, the app's
// template is the baseline's again, so what's observed is what's bound,
// and the candidate is gone or inactive.
func (e *azureCanaryEnv) assertBaselineAlone(t *testing.T, candidate string) {
	t.Helper()
	if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) {
		t.Fatalf("traffic %v", split)
	}
	revs := e.f.Revisions(e.app)
	if !revs[e.baseline].Active {
		t.Fatal("the baseline isn't active")
	}
	if candidate != "" && revs[candidate].Active {
		t.Fatalf("candidate %s is still active", candidate)
	}
	obs, err := e.exec.Observe(t.Context(), e.job.Connection, e.job.Credential, e.job.Binding)
	if err != nil || obs.Revision != e.oldImage {
		t.Fatalf("observed %+v, %v; want %s", obs, err, e.oldImage)
	}
	if env := templateEnv(revs[e.baseline].Template); env["TOKEN"] != "secret:"+e.oldSecret || env["MODE"] != "slow" {
		t.Fatalf("the baseline reads %v", env)
	}
	secrets := e.f.Secrets(e.app)
	if secrets[e.oldSecret] != "old-secret" {
		t.Fatal("the baseline's secret changed")
	}
	// The candidate's own secrets go with it.
	if candidate != "" {
		for name := range secrets {
			if strings.HasSuffix(name, "-"+strings.TrimPrefix(candidate, e.app+"--")) {
				t.Fatalf("the candidate's secret %s is still on the app", name)
			}
		}
	}
}

// templateEnv is a template's first container's variables: a value, or
// "secret:" and the secret it reads.
func templateEnv(tmpl map[string]any) map[string]string {
	out := map[string]string{}
	cs, _ := tmpl["containers"].([]any)
	if len(cs) == 0 {
		return out
	}
	env, _ := cs[0].(map[string]any)["env"].([]any)
	for _, v := range env {
		m := v.(map[string]any)
		name, _ := m["name"].(string)
		if ref, _ := m["secretRef"].(string); ref != "" {
			out[name] = "secret:" + ref
			continue
		}
		out[name], _ = m["value"].(string)
	}
	return out
}

func trafficOf(body map[string]any) []any {
	conf, _ := body["properties"].(map[string]any)["configuration"].(map[string]any)
	ing, _ := conf["ingress"].(map[string]any)
	traffic, _ := ing["traffic"].([]any)
	return traffic
}

var candidateSuffixRE = regexp.MustCompile(`^c[0-9a-f]{8}-0$`)

func TestApplyCandidateRunsBothRevisions(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	target := e.start(t)
	suffix := azureCandidateSuffix(azureDeployment, 0)
	if !candidateSuffixRE.MatchString(suffix) {
		t.Fatalf("suffix %q", suffix)
	}
	if target.Baseline != e.baseline || target.Candidate != e.app+"--"+suffix || target.Image != e.newImage || target.Refs[azureRefApp] != e.appID {
		t.Fatalf("target %+v", target)
	}
	put := e.lastPut()
	props := put["properties"].(map[string]any)
	conf := props["configuration"].(map[string]any)
	traffic := trafficOf(put)
	if conf["activeRevisionsMode"] != "Multiple" || len(traffic) != 1 ||
		traffic[0].(map[string]any)["revisionName"] != e.baseline || traffic[0].(map[string]any)["weight"] != float64(100) {
		t.Fatalf("configuration %v", conf)
	}
	if got := props["template"].(map[string]any)["revisionSuffix"]; got != suffix {
		t.Fatalf("suffix %v", got)
	}
	// Both revisions run; the baseline has all of the traffic.
	if active := e.f.ActiveRevisions(e.app); !slices.Equal(active, []string{e.baseline, target.Candidate}) {
		t.Fatalf("active %v", active)
	}
	if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) {
		t.Fatalf("split %v", split)
	}
	revs := e.f.Revisions(e.app)
	cand := revs[target.Candidate]
	env := templateEnv(cand.Template)
	if !cand.Ready || cand.Image != e.newImage || env["MODE"] != "fast" || env["TOKEN"] != "secret:"+e.oldSecret+"-"+suffix {
		t.Fatalf("candidate %+v reads %v", cand, env)
	}
	secrets := e.f.Secrets(e.app)
	if secrets[e.oldSecret] != "old-secret" || secrets[e.oldSecret+"-"+suffix] != "new-secret" || secrets[azureRegistrySecret] != "registry-pw" {
		t.Fatalf("secrets %v", secrets)
	}
}

func TestPromoteReturnsToSingle(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	target := e.start(t)
	e.weigh(t, target, 50)
	for range 2 {
		out, err := e.exec.Promote(t.Context(), e.job, target)
		if err != nil {
			t.Fatal(err)
		}
		if out.ExternalID != e.appID || out.Image != e.newImage || out.Revision != e.newImage || out.URL != e.f.URL()+"/apps/"+e.app {
			t.Fatalf("outcome %+v", out)
		}
		patch := e.lastPatch()
		conf := patch["properties"].(map[string]any)["configuration"].(map[string]any)
		traffic := trafficOf(patch)
		if conf["activeRevisionsMode"] != "Single" || len(traffic) != 1 ||
			traffic[0].(map[string]any)["latestRevision"] != true || traffic[0].(map[string]any)["weight"] != float64(100) {
			t.Fatalf("patch %v", patch)
		}
	}
	if e.f.Mode(e.app) != "Single" {
		t.Fatalf("mode %s", e.f.Mode(e.app))
	}
	if active := e.f.ActiveRevisions(e.app); !slices.Equal(active, []string{target.Candidate}) {
		t.Fatalf("active %v", active)
	}
	if split := e.split(); !maps.Equal(split, map[string]int{target.Candidate: 100}) {
		t.Fatalf("split %v", split)
	}
	obs, err := e.exec.Observe(t.Context(), e.job.Connection, e.job.Credential, e.job.Binding)
	if err != nil || obs.Revision != e.newImage {
		t.Fatalf("observed %+v, %v", obs, err)
	}
}

func TestRevertPinsTheBaselineAndDeactivatesTheCandidate(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	target := e.start(t)
	e.weigh(t, target, 25)
	if err := e.revert(t, target); err != nil {
		t.Fatal(err)
	}
	if e.f.Mode(e.app) != "Multiple" {
		t.Fatalf("mode %s", e.f.Mode(e.app))
	}
	e.f.Lock()
	deactivated := slices.Contains(e.f.Deactivations, target.Candidate)
	e.f.Unlock()
	if !deactivated {
		t.Fatal("the candidate wasn't deactivated")
	}
	e.assertBaselineAlone(t, target.Candidate)
	// Nothing but the baseline is left running: the revision that put the
	// baseline's template back takes no traffic and is deactivated too.
	if active := e.f.ActiveRevisions(e.app); !slices.Equal(active, []string{e.baseline}) {
		t.Fatalf("active %v", active)
	}
}

func TestApplyCandidateRemovesAnEarlierCandidate(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	first := e.start(t)
	n := e.callCount()
	second := e.start(t)
	if second.Candidate == first.Candidate || second.Baseline != e.baseline {
		t.Fatalf("first %+v, second %+v", first, second)
	}
	calls := e.calls(n)
	deactivate := slices.Index(calls, "POST deactivate "+e.app)
	put := slices.Index(calls, "PUT app "+e.app)
	if deactivate < 0 || put < 0 || deactivate > put {
		t.Fatalf("calls %v", calls)
	}
	if active := e.f.ActiveRevisions(e.app); !slices.Equal(active, []string{e.baseline, second.Candidate}) {
		t.Fatalf("active %v", active)
	}
	if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) {
		t.Fatalf("split %v", split)
	}
}

func TestOrdinaryDeployStaysSingle(t *testing.T) {
	t.Parallel()
	job := azureJob(t, apiConfig())
	pull := azurePull{Server: "r.azurecr.io", Login: &azure.RegistryLogin{Username: "r", Password: "pw"}}
	app := azureApp(job, apiConfig(), "westeurope", "env", pull, "r.azurecr.io/api:1", azure.SingleRevision, azure.Latest())
	conf := app.Properties.Configuration
	if conf.ActiveRevisionsMode != "Single" || !slices.Equal(conf.Ingress.Traffic, azure.Latest()) || app.Properties.Template.RevisionSuffix != "" {
		t.Fatalf("configuration %+v", conf)
	}
	// A worker takes no traffic, so it has none to set.
	worker := &planning.ServiceConfig{Source: "services/api", Image: "services/api/Dockerfile", Workload: provider.Worker, Execution: provider.Container}
	app = azureApp(job, worker, "westeurope", "env", pull, "r.azurecr.io/api:1", azure.SingleRevision, azure.Latest())
	if app.Properties.Configuration.Ingress != nil || app.Properties.Configuration.ActiveRevisionsMode != "Single" {
		t.Fatalf("worker %+v", app.Properties.Configuration)
	}

	// Apply sends the latest revision all of the traffic, explicitly.
	f := azuretest.New(t)
	if _, err := (&Azure{Client: azure.New(f.URL())}).Apply(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	f.Lock()
	put := f.AppPuts[len(f.AppPuts)-1]
	f.Unlock()
	traffic := trafficOf(put)
	if len(traffic) != 1 || traffic[0].(map[string]any)["latestRevision"] != true || traffic[0].(map[string]any)["weight"] != float64(100) {
		t.Fatalf("traffic %v", traffic)
	}
}

// --- Each call is safe to repeat, and cleans up after itself ---

func TestAzureCanaryWeighs(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	target := e.start(t)
	for _, step := range []struct {
		percent int
		want    map[string]int
	}{
		{5, map[string]int{e.baseline: 95, target.Candidate: 5}},
		{50, map[string]int{e.baseline: 50, target.Candidate: 50}},
		{100, map[string]int{target.Candidate: 100}},
		{0, map[string]int{e.baseline: 100}},
	} {
		for range 2 {
			e.weigh(t, target, step.percent)
			if split := e.split(); !maps.Equal(split, step.want) {
				t.Fatalf("at %d%%: %v", step.percent, split)
			}
		}
	}
	if err := e.exec.SetWeights(t.Context(), e.job, target, 101); err == nil {
		t.Fatal("weighed 101%")
	}
	// No PATCH ever carries a secret or a template.
	e.f.Lock()
	defer e.f.Unlock()
	for _, patch := range e.f.AppPatches {
		props := patch["properties"].(map[string]any)
		conf, _ := props["configuration"].(map[string]any)
		if _, has := conf["secrets"]; has {
			t.Fatalf("a PATCH sent secrets: %v", patch)
		}
		if _, has := conf["registries"]; has {
			t.Fatalf("a PATCH sent registries: %v", patch)
		}
	}
}

func TestAzureCanaryGivesNoTrafficToACandidateThatIsntUp(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	target := e.start(t)
	e.weigh(t, target, 0)
	for _, down := range []struct {
		what string
		do   func()
	}{
		{"crashed", func() { e.f.Crash(e.app, target.Candidate) }},
		{"inactive", func() { e.deactivate(t, target.Candidate) }},
	} {
		what := down.what
		down.do()
		patches := len(e.lastPatches())
		err := e.exec.SetWeights(t.Context(), e.job, target, 10)
		if err == nil || !strings.Contains(err.Error(), "candidate revision "+target.Candidate+" can't take traffic") {
			t.Fatalf("%s: err %v", what, err)
		}
		// Loomplane refused it; it didn't leave it to Azure.
		if n := len(e.lastPatches()); n != patches {
			t.Fatalf("%s: %d PATCHes", what, n-patches)
		}
		if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) {
			t.Fatalf("%s: split %v", what, split)
		}
	}
	// At 0 the candidate needn't be up, or there at all.
	e.f.DropRevision(e.app, target.Candidate)
	e.weigh(t, target, 0)
}

func TestAzureCanaryApplyCandidateIsSafeToRepeat(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	first := e.start(t)
	second := e.start(t)
	// The suffix the first took is taken, so the second is numbered.
	if second.Candidate != first.Candidate+"-2" {
		t.Fatalf("first %s, second %s", first.Candidate, second.Candidate)
	}
	revs := e.f.Revisions(e.app)
	if revs[first.Candidate].Active || !revs[second.Candidate].Active {
		t.Fatalf("revisions %v", e.f.ActiveRevisions(e.app))
	}
	if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) {
		t.Fatalf("split %v", split)
	}
	// Each candidate's secrets carry its own suffix, and the baseline's
	// are the only others kept.
	secrets := slices.Sorted(maps.Keys(e.f.Secrets(e.app)))
	secondSuffix := strings.TrimPrefix(second.Candidate, e.app+"--")
	if !slices.Equal(secrets, []string{e.oldSecret, e.oldSecret + "-" + secondSuffix, azureRegistrySecret}) {
		t.Fatalf("secrets %v", secrets)
	}
}

func TestAzureCanaryAFailedStartCleansUp(t *testing.T) {
	t.Parallel()
	for what, fail := range map[string]func(e *azureCanaryEnv){
		"the candidate crashes":           func(e *azureCanaryEnv) { e.f.FailImage = e.newImage },
		"its readiness can't be read":     func(e *azureCanaryEnv) { e.f.FailAfter("GET revision", 2) },
		"the latest can't be deactivated": func(e *azureCanaryEnv) { e.f.FailImage = e.newImage; e.f.RefuseLatestDeactivation = true },
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			e := newAzureCanaryEnv(t)
			fail(e)
			_, err := e.exec.ApplyCandidate(t.Context(), e.job)
			if err == nil || !strings.Contains(err.Error(), "start the candidate") {
				t.Fatalf("err %v", err)
			}
			candidate := e.app + "--" + azureCandidateSuffix(azureDeployment, 0)
			e.assertBaselineAlone(t, candidate)
		})
	}
}

func TestAzureCanaryAFailedCleanupSaysWhatMayBeLeft(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	e.f.FailImage = e.newImage
	// The start pins the baseline with one PATCH and the cleanup with
	// another, which finds the split in place already. Putting the
	// baseline's template back fails.
	e.f.FailAfter("PATCH app", 2)
	_, err := e.exec.ApplyCandidate(t.Context(), e.job)
	candidate := e.app + "--" + azureCandidateSuffix(azureDeployment, 0)
	for _, want := range []string{"start the candidate", "Cleaning up failed too", candidate, e.app, "may be left"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err %v doesn't say %q", err, want)
		}
	}
}

func TestAzureCanaryPromoteResumesAfterAPartialRun(t *testing.T) {
	t.Parallel()
	for _, call := range []string{"GET revision", "GET app", "PATCH app"} {
		t.Run(call, func(t *testing.T) {
			t.Parallel()
			e := newAzureCanaryEnv(t)
			target := e.start(t)
			e.weigh(t, target, 50)
			e.f.FailNext(call)
			if _, err := e.exec.Promote(t.Context(), e.job, target); err == nil {
				t.Fatal("the promote didn't fail")
			}
			if _, err := e.exec.Promote(t.Context(), e.job, target); err != nil {
				t.Fatal(err)
			}
			if split := e.split(); !maps.Equal(split, map[string]int{target.Candidate: 100}) || e.f.Mode(e.app) != "Single" {
				t.Fatalf("split %v, mode %s", split, e.f.Mode(e.app))
			}
		})
	}
}

func TestAzureCanaryPromoteRefusesACandidateThatIsntUp(t *testing.T) {
	t.Parallel()
	for what, down := range map[string]func(t *testing.T, e *azureCanaryEnv, target CanaryTarget){
		"inactive": func(t *testing.T, e *azureCanaryEnv, target CanaryTarget) {
			e.weigh(t, target, 0)
			e.deactivate(t, target.Candidate)
		},
		"gone": func(t *testing.T, e *azureCanaryEnv, target CanaryTarget) {
			e.weigh(t, target, 0)
			e.f.DropRevision(e.app, target.Candidate)
		},
		"crashed": func(t *testing.T, e *azureCanaryEnv, target CanaryTarget) {
			e.weigh(t, target, 0)
			e.f.Crash(e.app, target.Candidate)
		},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			e := newAzureCanaryEnv(t)
			target := e.start(t)
			down(t, e, target)
			for range 2 {
				_, err := e.exec.Promote(t.Context(), e.job, target)
				if err == nil || !strings.Contains(err.Error(), "nothing was promoted") || !strings.Contains(err.Error(), "The baseline has all of the traffic") {
					t.Fatalf("err %v", err)
				}
			}
			if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) || e.f.Mode(e.app) != "Multiple" {
				t.Fatalf("split %v, mode %s", split, e.f.Mode(e.app))
			}
		})
	}
}

// A candidate that's gone only promotes when what serves all of the
// traffic runs the whole release, not just its image.
func TestAzureCanaryPromoteFinishesWhenWhatServesRunsTheRelease(t *testing.T) {
	t.Parallel()
	// A release of what runs, with no secrets to tell them apart.
	e := newAzureCanaryEnvWith(t, map[string]string{"MODE": "slow"}, nil)
	e.job.Resume = []byte(`{"image":"` + e.oldImage + `"}`)
	target := e.start(t)
	e.f.DropRevision(e.app, target.Candidate)
	for range 2 {
		out, err := e.exec.Promote(t.Context(), e.job, target)
		if err != nil {
			t.Fatal(err)
		}
		if out.Image != e.oldImage || out.ExternalID != e.appID {
			t.Fatalf("outcome %+v", out)
		}
	}
	if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) {
		t.Fatalf("split %v", split)
	}
}

// newAzureCanaryEnvWith is newAzureCanaryEnv whose baseline and release
// both run env, with the secret variables secrets names.
func newAzureCanaryEnvWith(t *testing.T, env map[string]string, secrets map[string]bool) *azureCanaryEnv {
	t.Helper()
	f := azuretest.New(t)
	exec := &Azure{Client: azure.New(f.URL())}
	job := azureJob(t, apiConfig())
	job.Deployment.ID = azureDeployment
	job.Action.Provider = "azure-container-apps"
	job.Connection.Name = "Prod"
	job.Env, job.Secrets = maps.Clone(env), maps.Clone(secrets)
	out, err := exec.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	e := &azureCanaryEnv{f: f, exec: exec, job: job, app: AzureName(job), appID: out.ExternalID, oldImage: out.Image}
	e.baseline = e.app + "--1"
	job.Binding = &planning.Binding{ExternalID: out.ExternalID, Region: "westeurope", Revision: out.Image}
	e.newImage = strings.TrimSuffix(out.Image, doApproved[:12]) + "new"
	job.Resume = []byte(`{"image":"` + e.newImage + `"}`)
	return e
}

func TestAzureCanaryPromoteWontFinishOnARevisionThatOnlySharesTheImage(t *testing.T) {
	t.Parallel()
	for what, change := range map[string]func(job *Job){
		"a secret": func(job *Job) {
			job.Env["TOKEN"] = "s3cret"
			job.Secrets = map[string]bool{"TOKEN": true}
		},
		"a variable": func(job *Job) { job.Env["MODE"] = "fast" },
		"the scale":  func(job *Job) { job.Action.Service.Scale.Max = 5 },
		"the size":   func(job *Job) { job.Action.Service.Size.CPU = 2 },
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			e := newAzureCanaryEnvWith(t, map[string]string{"MODE": "slow"}, nil)
			e.job.Resume = []byte(`{"image":"` + e.oldImage + `"}`)
			e.job.Action.Service = apiConfig()
			change(e.job)
			target := e.start(t)
			e.f.DropRevision(e.app, target.Candidate)
			for range 2 {
				_, err := e.exec.Promote(t.Context(), e.job, target)
				if err == nil || !strings.Contains(err.Error(), "nothing was promoted") {
					t.Fatalf("err %v", err)
				}
			}
			if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) {
				t.Fatalf("split %v", split)
			}
		})
	}
}

func TestAzureCanaryPromoteRefusesWhenTheLatestIsntTheCandidate(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	target := e.start(t)
	e.weigh(t, target, 50)
	s, err := azureSession(t.Context(), e.exec.Client, e.job.Connection, e.job.Credential)
	if err != nil {
		t.Fatal(err)
	}
	if rerr := s.RestoreTemplate(t.Context(), e.appID, e.baseline, "other"); rerr != nil {
		t.Fatal(rerr)
	}
	_, err = e.exec.Promote(t.Context(), e.job, target)
	if err == nil || !strings.Contains(err.Error(), "not candidate") || !strings.Contains(err.Error(), "nothing was promoted") {
		t.Fatalf("err %v", err)
	}
	if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) || e.f.Mode(e.app) != "Multiple" {
		t.Fatalf("split %v, mode %s", split, e.f.Mode(e.app))
	}
}

func TestAzureCanaryRevertIsSafeToRepeat(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	target := e.start(t)
	e.weigh(t, target, 100)
	for range 2 {
		if err := e.revert(t, target); err != nil {
			t.Fatal(err)
		}
		e.assertBaselineAlone(t, target.Candidate)
	}
}

func TestAzureCanaryRevertResumesAfterAPartialRun(t *testing.T) {
	t.Parallel()
	for _, fail := range []struct {
		call  string
		after int
	}{
		{"PATCH app", 0}, // the traffic
		{"GET app", 3},   // reading what the template makes, after the traffic's three
		{"PATCH app", 1}, // putting the baseline's template back
		{"POST deactivate", 0},
		{"GET revisions", 0}, // what's still active beside the baseline
	} {
		t.Run(fmt.Sprintf("%s %d", fail.call, fail.after), func(t *testing.T) {
			t.Parallel()
			e := newAzureCanaryEnv(t)
			target := e.start(t)
			e.weigh(t, target, 30)
			e.f.FailAfter(fail.call, fail.after)
			if err := e.exec.Revert(t.Context(), e.job, target); err == nil {
				t.Fatal("the revert didn't fail")
			}
			if err := e.revert(t, target); err != nil {
				t.Fatal(err)
			}
			e.assertBaselineAlone(t, target.Candidate)
		})
	}
}

// Whether Container Apps lets the latest revision be deactivated isn't
// documented. Either way a revert and a start leave only what they mean
// to running.
func TestAzureCanaryCopesWhenTheLatestCantBeDeactivated(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	e.f.RefuseLatestDeactivation = true
	first := e.start(t)
	second := e.start(t)
	revs := e.f.Revisions(e.app)
	if revs[first.Candidate].Active || !revs[second.Candidate].Active {
		t.Fatalf("active %v", e.f.ActiveRevisions(e.app))
	}
	if err := e.revert(t, second); err != nil {
		t.Fatal(err)
	}
	e.assertBaselineAlone(t, second.Candidate)
}

func TestAzureCanaryNeedsThePermissionsToSplitTraffic(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	e.f.Lock()
	e.f.NotActions = append(e.f.NotActions, "Microsoft.App/containerApps/revisions/deactivate/action", "Microsoft.App/containerApps/listSecrets/action")
	e.f.Unlock()
	n := e.callCount()
	_, err := e.exec.ApplyCandidate(t.Context(), e.job)
	for _, want := range []string{"Prod", "Microsoft.App/containerApps/revisions/deactivate/action, Microsoft.App/containerApps/listSecrets/action", "direct or blue_green"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err %v doesn't say %q", err, want)
		}
	}
	if calls := e.calls(n); len(calls) != 0 {
		t.Fatalf("calls %v", calls)
	}
	e.f.Lock()
	e.f.NotActions = e.f.NotActions[:2]
	e.f.Unlock()
	e.start(t)
}

func TestAzureCanaryNeedsSomethingRunning(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	e.job.Binding = nil
	if _, err := e.exec.ApplyCandidate(t.Context(), e.job); !errors.Is(err, ErrNoExecutor) {
		t.Fatalf("err %v", err)
	}
	e.job.Binding = &planning.Binding{ExternalID: e.appID}
	e.job.Action.Service = nil
	if _, err := e.exec.ApplyCandidate(t.Context(), e.job); !errors.Is(err, ErrNoExecutor) {
		t.Fatalf("err %v", err)
	}
}

// An ordinary deploy after a canary sends the latest revision all of the
// traffic and goes back to single revision mode, whatever the canary
// left pinned.
func TestAzureCanaryAnOrdinaryDeployAfterwardTakesEverything(t *testing.T) {
	t.Parallel()
	for what, leave := range map[string]func(t *testing.T, e *azureCanaryEnv){
		"a revert": func(t *testing.T, e *azureCanaryEnv) {
			target := e.start(t)
			e.weigh(t, target, 50)
			if err := e.revert(t, target); err != nil {
				t.Fatal(err)
			}
		},
		"a failed start": func(t *testing.T, e *azureCanaryEnv) {
			e.f.FailImage = e.newImage
			if _, err := e.exec.ApplyCandidate(t.Context(), e.job); err == nil {
				t.Fatal("a crashing candidate started")
			}
			e.f.FailImage = ""
		},
		"a refused promote": func(t *testing.T, e *azureCanaryEnv) {
			target := e.start(t)
			e.weigh(t, target, 0)
			e.f.DropRevision(e.app, target.Candidate)
			if _, err := e.exec.Promote(t.Context(), e.job, target); err == nil {
				t.Fatal("promoted a candidate that's gone")
			}
		},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			e := newAzureCanaryEnv(t)
			leave(t, e)
			if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) || e.f.Mode(e.app) != "Multiple" {
				t.Fatalf("before: split %v, mode %s", split, e.f.Mode(e.app))
			}
			e.job.Resume = nil
			e.job.CommitSHA = doApproved
			out, err := e.exec.Apply(t.Context(), e.job)
			if err != nil {
				t.Fatal(err)
			}
			app := e.f.App(e.app)
			latest, _ := app["properties"].(map[string]any)["latestRevisionName"].(string)
			if split := e.split(); !maps.Equal(split, map[string]int{latest: 100}) || latest == e.baseline || e.f.Mode(e.app) != "Single" {
				t.Fatalf("after: split %v, latest %s, mode %s", split, latest, e.f.Mode(e.app))
			}
			if active := e.f.ActiveRevisions(e.app); !slices.Equal(active, []string{latest}) {
				t.Fatalf("active %v", active)
			}
			_ = out
		})
	}
}

// --- The baseline ---

func TestAzureCanaryUsesTheRecordedBaseline(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	first := e.start(t)
	e.weigh(t, first, 70)
	// The earlier candidate has most of the traffic now, but it isn't the
	// baseline.
	e.job.Deployment.Retries = 1
	e.job.Deployment.Canary = &Canary{Services: []CanaryService{{ActionID: e.job.Action.ID, Baseline: e.baseline}}}
	second := e.start(t)
	if second.Baseline != e.baseline || second.Candidate != e.app+"--"+azureCandidateSuffix(azureDeployment, 1) {
		t.Fatalf("second %+v", second)
	}
	if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) {
		t.Fatalf("split %v", split)
	}
	if e.f.Revisions(e.app)[first.Candidate].Active {
		t.Fatal("the earlier candidate is still active")
	}
}

// A step at 100% leaves the baseline no traffic. The revert fails and the
// deployment is retried: the retry still ramps from the recorded
// baseline, and its revert ends there.
func TestAzureCanaryARetryAfterAFullStepRampsFromTheRecordedBaseline(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	first := e.start(t)
	e.weigh(t, first, 100)
	e.f.FailNext("PATCH app")
	if err := e.revert(t, first); err == nil {
		t.Fatal("the revert didn't fail")
	}
	if split := e.split(); !maps.Equal(split, map[string]int{first.Candidate: 100}) {
		t.Fatalf("before the retry: %v", split)
	}
	e.job.Deployment.Retries = 1
	e.job.Deployment.Canary = &Canary{Services: []CanaryService{{ActionID: e.job.Action.ID, Baseline: first.Baseline}}}
	second := e.start(t)
	if second.Baseline != e.baseline {
		t.Fatalf("the retry ramps from %s, not the recorded baseline", second.Baseline)
	}
	if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) {
		t.Fatalf("after the retry started: %v", split)
	}
	if err := e.revert(t, second); err != nil {
		t.Fatal(err)
	}
	e.assertBaselineAlone(t, second.Candidate)
	if e.f.Revisions(e.app)[first.Candidate].Active {
		t.Fatalf("the failed candidate %s is still active", first.Candidate)
	}
}

func TestAzureCanaryRefusesARecordedBaselineThatsGone(t *testing.T) {
	t.Parallel()
	for what, gone := range map[string]func(e *azureCanaryEnv){
		"gone":     func(e *azureCanaryEnv) { e.f.DropRevision(e.app, e.baseline) },
		"inactive": func(e *azureCanaryEnv) { e.deactivate(t, e.baseline) },
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			e := newAzureCanaryEnv(t)
			first := e.start(t)
			e.weigh(t, first, 100)
			gone(e)
			e.job.Deployment.Retries = 1
			e.job.Deployment.Canary = &Canary{Services: []CanaryService{{ActionID: e.job.Action.ID, Baseline: e.baseline}}}
			n := e.callCount()
			_, err := e.exec.ApplyCandidate(t.Context(), e.job)
			for _, want := range []string{e.baseline, "left as it is"} {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("err %v doesn't say %q", err, want)
				}
			}
			if split := e.split(); !maps.Equal(split, map[string]int{first.Candidate: 100}) {
				t.Fatalf("traffic moved: %v", split)
			}
			for _, c := range e.calls(n) {
				if !strings.HasPrefix(c, "GET ") {
					t.Fatalf("a refused start made %s", c)
				}
			}
		})
	}
}

func (e *azureCanaryEnv) deactivate(t *testing.T, revision string) {
	t.Helper()
	s, err := azureSession(t.Context(), e.exec.Client, e.job.Connection, e.job.Credential)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeactivateRevision(t.Context(), e.appID, revision); err != nil {
		t.Fatal(err)
	}
}

// Another deployment's canary that never reverted left its traffic split,
// or all of it on its candidate. Without a record, a new canary can't tell
// which revision is the baseline, so it refuses.
func TestAzureCanaryRefusesTrafficAnEarlierCanaryLeft(t *testing.T) {
	t.Parallel()
	for what, percent := range map[string]int{"split": 70, "pinned to the candidate": 100} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			e := newAzureCanaryEnv(t)
			first := e.start(t)
			e.weigh(t, first, percent)
			e.job.Deployment.ID = "dep_01jxanother000000000000000"
			n := e.callCount()
			_, err := e.exec.ApplyCandidate(t.Context(), e.job)
			for _, want := range []string{first.Candidate, "Settle that canary's traffic first", "direct"} {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("err %v doesn't say %q", err, want)
				}
			}
			for _, c := range e.calls(n) {
				if !strings.HasPrefix(c, "GET ") {
					t.Fatalf("a refused start made %s", c)
				}
			}
			if !e.f.Revisions(e.app)[e.baseline].Active {
				t.Fatal("the baseline was deactivated")
			}
		})
	}
}

// The split refusal stands on its own: without a candidate in the
// template, a split still can't say which revision is the baseline.
func TestAzureSettledRefusesASplit(t *testing.T) {
	t.Parallel()
	job := &Job{Action: planning.Action{Name: "api"}, Deployment: &Deployment{ID: azureDeployment}}
	app := &azure.App{}
	app.Properties.LatestReadyRevision = "api--3"
	app.Properties.Configuration = azure.Configuration{ActiveRevisionsMode: azure.MultipleRevisions, Ingress: &azure.Ingress{
		Traffic: []azure.TrafficWeight{{RevisionName: "api--1", Weight: 60}, {RevisionName: "api--2", Weight: 40}},
	}}
	err := azureSettled(job, "api", app)
	if err == nil || !strings.Contains(err.Error(), "api--1 60%, api--2 40%") {
		t.Fatalf("err %v", err)
	}
	// One revision alone, or the latest, is settled.
	app.Properties.Configuration.Ingress.Traffic = []azure.TrafficWeight{{RevisionName: "api--1", Weight: 100}}
	if err := azureSettled(job, "api", app); err != nil {
		t.Fatal(err)
	}
	app.Properties.Configuration.Ingress.Traffic = azure.Latest()
	app.Properties.Template.RevisionSuffix = azureCandidateSuffix("dep_other", 0)
	if err := azureSettled(job, "api", app); err != nil {
		t.Fatal(err)
	}
	if got := azureServing(app); got != "api--3" {
		t.Fatalf("serving %q", got)
	}
}

func TestAzureCanaryStartsAfterAnotherDeploymentReverted(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	first := e.start(t)
	e.weigh(t, first, 40)
	if err := e.revert(t, first); err != nil {
		t.Fatal(err)
	}
	e.job.Deployment.ID = "dep_01jxanother000000000000000"
	second := e.start(t)
	if second.Baseline != e.baseline {
		t.Fatalf("second %+v", second)
	}
}

func TestAzureCanaryStartsAgainAfterAFailedStart(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	e.f.FailImage = e.newImage
	if _, err := e.exec.ApplyCandidate(t.Context(), e.job); err == nil {
		t.Fatal("a crashing candidate started")
	}
	e.f.FailImage = ""
	second := e.start(t)
	if second.Baseline != e.baseline {
		t.Fatalf("second %+v", second)
	}
	if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) {
		t.Fatalf("split %v", split)
	}
}

// --- Secrets ---

// Container Apps keeps secrets on the app, not on a revision, and a
// replica the baseline starts mid-canary reads whatever its secret holds
// then. The candidate's values go in secrets of its own, so the
// baseline's never change.
func TestAzureCanarySecretsNeverReachTheBaseline(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	check := func(when string) {
		t.Helper()
		if v := e.f.Secrets(e.app)[e.oldSecret]; v != "old-secret" {
			t.Fatalf("%s: the baseline's secret holds %q", when, v)
		}
		if env := templateEnv(e.f.Revisions(e.app)[e.baseline].Template); env["TOKEN"] != "secret:"+e.oldSecret {
			t.Fatalf("%s: the baseline reads %v", when, env)
		}
	}
	target := e.start(t)
	check("started")
	e.weigh(t, target, 25)
	check("at 25%")
	if err := e.revert(t, target); err != nil {
		t.Fatal(err)
	}
	check("reverted")
	// Moving traffic never touches secrets. The one PATCH that does, once
	// the candidate is deactivated, drops the candidate's own and leaves
	// the baseline's as they were.
	e.f.Lock()
	defer e.f.Unlock()
	for _, patch := range e.f.AppPatches {
		conf, _ := patch["properties"].(map[string]any)["configuration"].(map[string]any)
		secrets, has := conf["secrets"].([]any)
		if !has {
			continue
		}
		if _, moves := conf["ingress"]; moves {
			t.Fatalf("a traffic PATCH sent secrets: %v", patch)
		}
		for _, sec := range secrets {
			m := sec.(map[string]any)
			if m["name"] == e.oldSecret && m["value"] != "old-secret" {
				t.Fatalf("a PATCH changed the baseline's secret: %v", patch)
			}
			if strings.HasSuffix(m["name"].(string), strings.TrimPrefix(target.Candidate, e.app+"--")) {
				t.Fatalf("a PATCH kept the candidate's secret: %v", patch)
			}
		}
	}
}

// --- What Container Apps applies to every revision ---

// Ingress, the registries an app pulls from and its identity apply to
// every revision at once, so a canary that changed them would change the
// baseline too. The start refuses before it writes anything to the app.
func TestAzureCanaryRefusesAnAppWideChange(t *testing.T) {
	t.Parallel()
	for what, c := range map[string]struct {
		change func(e *azureCanaryEnv)
		says   string
	}{
		"port":          {func(e *azureCanaryEnv) { e.job.Action.Service.Port = 9090 }, "target port from 8080 to 9090"},
		"public":        {func(e *azureCanaryEnv) { e.job.Action.Service.Public = false }, "external ingress from true to false"},
		"pull identity": {func(e *azureCanaryEnv) { e.f.Lock(); e.f.NotActions = nil; e.f.Unlock() }, "how it pulls from the container registry"},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			e := newAzureCanaryEnv(t)
			c.change(e)
			n := e.callCount()
			_, err := e.exec.ApplyCandidate(t.Context(), e.job)
			for _, want := range []string{c.says, "app-wide", "the baseline included", "direct or blue_green"} {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("err %v doesn't say %q", err, want)
				}
			}
			for _, call := range e.calls(n) {
				if !strings.HasPrefix(call, "GET ") {
					t.Fatalf("a refused start made %s", call)
				}
			}
			ing := e.f.App(e.app)["properties"].(map[string]any)["configuration"].(map[string]any)["ingress"].(map[string]any)
			if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) || e.f.Mode(e.app) != "Single" ||
				ing["targetPort"] != float64(8080) || ing["external"] != true {
				t.Fatalf("the baseline changed: split %v, mode %s, ingress %v", split, e.f.Mode(e.app), ing)
			}
			if active := e.f.ActiveRevisions(e.app); !slices.Equal(active, []string{e.baseline}) {
				t.Fatalf("active %v", active)
			}
		})
	}
	// A release that keeps the ingress, the registry and the identity
	// canaries.
	e := newAzureCanaryEnv(t)
	e.job.Action.Service.Size.CPU = 2
	e.start(t)
}

// --- Ordinary deploys after a canary ---

// An ordinary deploy after a canary starts its revision beside what
// serves, with the traffic as it was, and only then gives it everything.
// No revision the canary left, inactive or failed, serves at any point,
// and a deploy that fails leaves the traffic where it was.
func TestAzureCanaryAnOrdinaryDeployServesOnlyWhatItShould(t *testing.T) {
	t.Parallel()
	for what, leave := range map[string]func(t *testing.T, e *azureCanaryEnv) CanaryTarget{
		"a clean revert": func(t *testing.T, e *azureCanaryEnv) CanaryTarget {
			target := e.start(t)
			e.weigh(t, target, 50)
			if err := e.revert(t, target); err != nil {
				t.Fatal(err)
			}
			return target
		},
		// The template couldn't go back, so the candidate that failed its
		// canary is still active, and still the app's latest ready revision.
		"a revert that failed": func(t *testing.T, e *azureCanaryEnv) CanaryTarget {
			target := e.start(t)
			e.weigh(t, target, 50)
			e.f.FailAfter("PATCH app", 2)
			if err := e.revert(t, target); err == nil {
				t.Fatal("the revert didn't fail")
			}
			if !e.f.Revisions(e.app)[target.Candidate].Active {
				t.Fatal("the candidate isn't active")
			}
			return target
		},
	} {
		for _, fails := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, the deploy fails %t", what, fails), func(t *testing.T) {
				t.Parallel()
				e := newAzureCanaryEnv(t)
				leave(t, e)
				if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) {
					t.Fatalf("before: %v", split)
				}
				mark := len(e.f.TrafficHistory(e.app))
				e.job.Resume, e.job.Env["MODE"] = nil, "fastest"
				if fails {
					// The ordinary deploy builds the approved commit again.
					e.f.FailImage = e.oldImage
				}
				_, err := e.exec.Apply(t.Context(), e.job)
				if fails != (err != nil) {
					t.Fatalf("deploy: %v", err)
				}
				allowed := map[string]bool{e.baseline: true}
				latest, _ := e.f.App(e.app)["properties"].(map[string]any)["latestRevisionName"].(string)
				if !fails {
					allowed[latest] = true
				}
				for i, split := range e.f.TrafficHistory(e.app)[mark:] {
					if len(split) == 0 {
						t.Fatalf("split %d: nothing serves", i)
					}
					for rev := range split {
						if !allowed[rev] {
							t.Fatalf("split %d gave %s traffic: %v", i, rev, split)
						}
					}
				}
				want, mode := map[string]int{e.baseline: 100}, "Multiple"
				if !fails {
					want, mode = map[string]int{latest: 100}, "Single"
				}
				if split := e.split(); !maps.Equal(split, want) || e.f.Mode(e.app) != mode {
					t.Fatalf("after: split %v, mode %s", split, e.f.Mode(e.app))
				}
			})
		}
	}
}

// After a promote, the revision that serves reads the candidate's own
// secrets. The next deploy keeps them while that revision runs, and drops
// them once it's gone.
func TestAzureCanaryTheFirstDeployAfterAPromoteKeepsWhatServes(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	target := e.start(t)
	e.weigh(t, target, 50)
	if _, err := e.exec.Promote(t.Context(), e.job, target); err != nil {
		t.Fatal(err)
	}
	own := e.oldSecret + "-" + strings.TrimPrefix(target.Candidate, e.app+"--")
	if e.f.Secrets(e.app)[own] != "new-secret" {
		t.Fatalf("secrets %v", e.f.Secrets(e.app))
	}

	// A deploy whose revision won't start leaves the promoted revision
	// serving, still with its secrets.
	e.job.Resume = nil
	e.f.FailImage = e.oldImage
	if _, err := e.exec.Apply(t.Context(), e.job); err == nil {
		t.Fatal("a crashing revision deployed")
	}
	if split := e.split(); !maps.Equal(split, map[string]int{target.Candidate: 100}) || e.f.Secrets(e.app)[own] != "new-secret" {
		t.Fatalf("split %v, secrets %v", e.split(), e.f.Secrets(e.app))
	}

	// One that starts takes over, and the candidate's secrets go once
	// nothing active reads them.
	e.f.FailImage = ""
	e.job.Env["MODE"] = "fastest"
	if _, err := e.exec.Apply(t.Context(), e.job); err != nil {
		t.Fatal(err)
	}
	latest, _ := e.f.App(e.app)["properties"].(map[string]any)["latestRevisionName"].(string)
	if active := e.f.ActiveRevisions(e.app); !slices.Equal(active, []string{latest}) {
		t.Fatalf("active %v", active)
	}
	secrets := e.f.Secrets(e.app)
	if _, has := secrets[own]; has || secrets[e.oldSecret] != "new-secret" || secrets[azureRegistrySecret] != "registry-pw" {
		t.Fatalf("secrets %v", secrets)
	}
	e.f.Lock()
	put := e.f.AppPuts[len(e.f.AppPuts)-1]
	e.f.Unlock()
	kept := false
	for _, sec := range put["properties"].(map[string]any)["configuration"].(map[string]any)["secrets"].([]any) {
		kept = kept || sec.(map[string]any)["name"] == own
	}
	if !kept {
		t.Fatal("the deploy's PUT dropped the secrets the serving revision read")
	}
}

// --- What an ordinary deploy needs ---

// canaryActionsDenied takes the canary-only actions away from the fake's
// principal.
func canaryActionsDenied(f *azuretest.Fake, actions ...string) {
	f.Lock()
	defer f.Unlock()
	f.NotActions = append(f.NotActions, actions...)
}

// notes records the progress a job shows.
func notes(job *Job) *[]string {
	var out []string
	job.Progress = func(_, m string) { out = append(out, m) }
	return &out
}

// An app that never ran a canary deploys as it did before canaries, with
// a principal that has none of the canary actions: one more GET, and
// nothing else new.
func TestAzureADeployThatNeverRanACanaryNeedsNoCanaryAction(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	canaryActionsDenied(e.f, azure.CanaryActions...)
	said := notes(e.job)
	e.job.Resume = nil
	n := e.callCount()
	if _, err := e.exec.Apply(t.Context(), e.job); err != nil {
		t.Fatal(err)
	}
	calls := e.calls(n)
	puts := 0
	for _, c := range calls {
		if c == "PUT app "+e.app {
			puts++
		}
	}
	if calls[0] != "GET app "+e.app || puts != 1 || slices.ContainsFunc(calls[1:], func(c string) bool { return c != "GET app "+e.app && c != "PUT app "+e.app }) {
		t.Fatalf("calls %v", calls)
	}
	for _, m := range *said {
		if strings.Contains(m, "secret") {
			t.Fatalf("a note about secrets: %q", m)
		}
	}

	// A release that drops a secret variable removes its secret, as it
	// always did, and says which.
	e.f.Lock()
	e.f.AllowDroppingInUseSecrets = true
	e.f.Unlock()
	delete(e.job.Env, "TOKEN")
	n = e.callCount()
	if _, err := e.exec.Apply(t.Context(), e.job); err != nil {
		t.Fatal(err)
	}
	if calls := e.calls(n); slices.ContainsFunc(calls, func(c string) bool { return c != "GET app "+e.app && c != "PUT app "+e.app }) {
		t.Fatalf("calls %v", calls)
	}
	if !slices.ContainsFunc(*said, func(m string) bool {
		return strings.Contains(m, e.oldSecret) && strings.Contains(m, "removes those secrets")
	}) {
		t.Fatalf("notes %q", *said)
	}
	if _, has := e.f.Secrets(e.app)[e.oldSecret]; has {
		t.Fatal("the dropped variable's secret is still there")
	}
}

// After a canary, the deploy has to read what the earlier revisions read
// before it replaces the secrets. A principal that may not is refused,
// with the action named, and nothing is written.
func TestAzureADeployOverCanaryLeftoversNeedsToReadThem(t *testing.T) {
	t.Parallel()
	for _, denied := range []string{"Microsoft.App/containerApps/revisions/read", "Microsoft.App/containerApps/listSecrets/action"} {
		t.Run(denied, func(t *testing.T) {
			t.Parallel()
			e := newAzureCanaryEnv(t)
			target := e.start(t)
			e.weigh(t, target, 50)
			if _, err := e.exec.Promote(t.Context(), e.job, target); err != nil {
				t.Fatal(err)
			}
			canaryActionsDenied(e.f, denied)
			e.job.Resume = nil
			n := e.callCount()
			_, err := e.exec.Apply(t.Context(), e.job)
			for _, want := range []string{"earlier canary", denied, "Grant it"} {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("err %v doesn't say %q", err, want)
				}
			}
			if slices.ContainsFunc(e.calls(n), func(c string) bool { return strings.HasPrefix(c, "PUT ") || strings.HasPrefix(c, "PATCH ") }) {
				t.Fatalf("calls %v", e.calls(n))
			}
			if split := e.split(); !maps.Equal(split, map[string]int{target.Candidate: 100}) {
				t.Fatalf("split %v", split)
			}
		})
	}

	// In multiple revisions mode with nothing to keep, the takeover still
	// reads the new revision before it gives it the traffic.
	e := newAzureCanaryEnv(t)
	target := e.start(t)
	e.weigh(t, target, 50)
	if err := e.revert(t, target); err != nil {
		t.Fatal(err)
	}
	canaryActionsDenied(e.f, "Microsoft.App/containerApps/revisions/read")
	e.job.Resume = nil
	_, err := e.exec.Apply(t.Context(), e.job)
	if err == nil || !strings.Contains(err.Error(), "Microsoft.App/containerApps/revisions/read") {
		t.Fatalf("err %v", err)
	}
	if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) {
		t.Fatalf("split %v", split)
	}
}

// A candidate's secrets an earlier deploy couldn't prune go with the next
// one, even when it keeps nothing.
func TestAzureADeployPrunesWhatAnEarlierOneLeft(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	target := e.start(t)
	e.weigh(t, target, 50)
	if _, err := e.exec.Promote(t.Context(), e.job, target); err != nil {
		t.Fatal(err)
	}
	own := e.oldSecret + "-" + strings.TrimPrefix(target.Candidate, e.app+"--")
	e.job.Resume = nil
	// The first deploy lists the secrets to keep them, then its prune
	// can't list them.
	e.f.FailAfter("POST listsecrets", 1)
	if _, err := e.exec.Apply(t.Context(), e.job); err != nil {
		t.Fatal(err)
	}
	if _, has := e.f.Secrets(e.app)[own]; !has {
		t.Fatal("the prune ran")
	}
	if _, err := e.exec.Apply(t.Context(), e.job); err != nil {
		t.Fatal(err)
	}
	if _, has := e.f.Secrets(e.app)[own]; has {
		t.Fatalf("secrets %v", e.f.Secrets(e.app))
	}
}

// The takeover gives the traffic only to a revision that's active and
// ready, and otherwise leaves it as it is.
func TestAzureTakeOverRefusesARevisionThatIsntUp(t *testing.T) {
	t.Parallel()
	e := newAzureCanaryEnv(t)
	target := e.start(t)
	e.weigh(t, target, 50)
	if err := e.revert(t, target); err != nil {
		t.Fatal(err)
	}
	s, err := azureSession(t.Context(), e.exec.Client, e.job.Connection, e.job.Credential)
	if err != nil {
		t.Fatal(err)
	}
	// The revision that put the baseline's template back is the latest,
	// and inactive.
	latest, _ := e.f.App(e.app)["properties"].(map[string]any)["latestRevisionName"].(string)
	for _, rev := range []string{latest, target.Candidate} {
		err := azureTakeOver(t.Context(), s, e.appID, rev)
		if err == nil || !strings.Contains(err.Error(), "left as it is") {
			t.Fatalf("%s: err %v", rev, err)
		}
		if split := e.split(); !maps.Equal(split, map[string]int{e.baseline: 100}) || e.f.Mode(e.app) != "Multiple" {
			t.Fatalf("%s: split %v, mode %s", rev, split, e.f.Mode(e.app))
		}
	}
}
