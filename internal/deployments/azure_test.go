package deployments

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/azure/azuretest"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// monoTarball is GitHub's tarball of acme/mono: one top-level directory.
func monoTarball(t *testing.T) []byte {
	t.Helper()
	var tgz bytes.Buffer
	zw := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(zw)
	_ = tw.WriteHeader(&tar.Header{Name: "acme-mono-0123456/", Typeflag: tar.TypeDir, Mode: 0o755})
	for _, f := range [][2]string{{"acme-mono-0123456/services/api/Dockerfile", "FROM scratch"}, {"acme-mono-0123456/services/api/main.go", "package main"}} {
		name, body := f[0], f[1]
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = zw.Close()
	return tgz.Bytes()
}

func azureJob(t *testing.T, cfg *planning.ServiceConfig) *Job {
	t.Helper()
	tgz := monoTarball(t)
	return &Job{
		Deployment: &Deployment{OrgID: "org", ProjectID: "proj_0123456789", Environment: "production",
			Sources: []SourceArtifact{{Alias: "mono", Repository: "acme/mono", CommitSHA: doApproved}}},
		Plan:       &planning.Plan{ID: "plan_1"},
		Action:     planning.Action{ID: "service:api", Name: "api", Service: cfg, Region: "westeurope"},
		Connection: &connections.Connection{ID: "conn_az", Cloud: connections.Azure, Details: azuretest.Settings()},
		Credential: []byte(azuretest.Secret + "\n"), CommitSHA: doApproved, SourceID: doApproved,
		Env:     map[string]string{"DATABASE_URL": "postgres://secret", "LOG_LEVEL": "info"},
		Secrets: map[string]bool{"DATABASE_URL": true},
		Source: func(context.Context) (io.ReadCloser, int64, error) {
			return io.NopCloser(bytes.NewReader(tgz)), int64(len(tgz)), nil
		},
		Checkpoint: func([]byte) error { return nil }, Progress: func(string, string) {},
	}
}

func TestAzure_buildsTheApprovedSourceAndRunsItOnContainerApps(t *testing.T) {
	f := azuretest.New(t)
	exec := &Azure{Client: azure.New(f.URL())}
	job := azureJob(t, apiConfig())
	var saved azureState
	job.Checkpoint = func(data []byte) error { return json.Unmarshal(data, &saved) }
	out, err := exec.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	registry := azure.RegistryName(azuretest.Subscription, "loomplane")
	image := registry + ".azurecr.io/api-23456789:" + doApproved[:12]
	name := AzureName(job)
	if name != "api-production-23456789" || out.Image != image || out.Revision != image ||
		!strings.HasSuffix(out.ExternalID, "/resourceGroups/loomplane/providers/Microsoft.App/containerApps/"+name) ||
		out.URL != f.URL()+"/apps/"+name || saved.AppID != out.ExternalID || saved.RunID == "" {
		t.Fatalf("outcome %+v, checkpoint %+v", out, saved)
	}

	f.Lock()
	defer f.Unlock()
	// The subscription had neither resource provider; both were registered.
	if len(f.Uploads) != 1 || len(f.Tasks) != 1 {
		t.Fatalf("uploads %d, tasks %v", len(f.Uploads), f.Tasks)
	}
	for _, data := range f.Uploads {
		if !bytes.Equal(data, monoTarball(t)) {
			t.Fatal("the upload isn't the approved source")
		}
	}
	// The Dockerfile is built from its own directory in the tarball.
	if want := "-f acme-mono-0123456/services/api/Dockerfile acme-mono-0123456/services/api\n"; !strings.Contains(f.Tasks[0], want) ||
		!strings.Contains(f.Tasks[0], "{{.Run.Registry}}/api-23456789:"+doApproved[:12]) {
		t.Fatalf("task:\n%s", f.Tasks[0])
	}
	app := f.Apps[name]
	props := app["properties"].(map[string]any)
	conf := props["configuration"].(map[string]any)
	ingress := conf["ingress"].(map[string]any)
	if app["location"] != "westeurope" || !strings.HasSuffix(props["managedEnvironmentId"].(string), "/managedEnvironments/loomplane-westeurope") ||
		ingress["external"] != true || ingress["targetPort"] != float64(8080) || conf["activeRevisionsMode"] != "Single" {
		t.Fatalf("app %v", app)
	}
	reg := conf["registries"].([]any)[0].(map[string]any)
	if reg["server"] != registry+".azurecr.io" || reg["passwordSecretRef"] != azureRegistrySecret {
		t.Fatalf("registry %v", reg)
	}
	tmpl := props["template"].(map[string]any)
	c := tmpl["containers"].([]any)[0].(map[string]any)
	res := c["resources"].(map[string]any)
	scale := tmpl["scale"].(map[string]any)
	if c["image"] != image || res["cpu"] != float64(1) || res["memory"] != "2Gi" || scale["minReplicas"] != float64(2) || scale["maxReplicas"] != float64(2) {
		t.Fatalf("container %v, scale %v", c, scale)
	}
	// The secret variable is an app secret the container refers to; the
	// other is a plain value.
	env := map[string]map[string]any{}
	for _, e := range c["env"].([]any) {
		env[e.(map[string]any)["name"].(string)] = e.(map[string]any)
	}
	ref, _ := env["DATABASE_URL"]["secretRef"].(string)
	if env["DATABASE_URL"]["value"] != nil || ref == "" || env["LOG_LEVEL"]["value"] != "info" {
		t.Fatalf("env %v", env)
	}
	secrets := map[string]any{}
	for _, s := range conf["secrets"].([]any) {
		secrets[s.(map[string]any)["name"].(string)] = s.(map[string]any)["value"]
	}
	if secrets[ref] != "postgres://secret" || secrets[azureRegistrySecret] != "registry-pw" {
		t.Fatalf("secrets %v", secrets)
	}
}

func TestAzure_observesAndRemovesWhatItRan(t *testing.T) {
	f := azuretest.New(t)
	exec := &Azure{Client: azure.New(f.URL())}
	job := azureJob(t, apiConfig())
	out, err := exec.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	b := &planning.Binding{ExternalID: out.ExternalID, Region: "westeurope"}
	obs, err := exec.Observe(t.Context(), job.Connection, job.Credential, b)
	if err != nil || !obs.Exists || obs.Revision != out.Image {
		t.Fatalf("observe %+v, %v", obs, err)
	}
	if where := exec.Locate(b); where != "the container app api-production-23456789 in westeurope" {
		t.Fatalf("locate %q", where)
	}
	// A deploy to what's bound keeps its app and builds again.
	job.Binding = b
	if again, err := exec.Apply(t.Context(), job); err != nil || again.ExternalID != out.ExternalID {
		t.Fatalf("redeploy %+v, %v", again, err)
	}
	if err := exec.Remove(t.Context(), job, b); err != nil {
		t.Fatal(err)
	}
	if obs, err := exec.Observe(t.Context(), job.Connection, job.Credential, b); err != nil || obs.Exists {
		t.Fatalf("observe a removed app %+v, %v", obs, err)
	}
	// Removing it again, or an app in another subscription, deletes nothing.
	if err := exec.Remove(t.Context(), job, b); err != nil {
		t.Fatalf("remove twice: %v", err)
	}
	other := &planning.Binding{ExternalID: strings.Replace(out.ExternalID, azuretest.Subscription, "44444444-4444-4444-4444-444444444444", 1)}
	if err := exec.Remove(t.Context(), job, other); err == nil {
		t.Fatal("removed an app in another subscription")
	}
	f.Lock()
	defer f.Unlock()
	if len(f.Deleted) != 1 || len(f.Tasks) != 2 {
		t.Fatalf("deleted %v, tasks %d", f.Deleted, len(f.Tasks))
	}
}

func TestAzure_aFailedBuildDeploysNothing(t *testing.T) {
	f := azuretest.New(t)
	f.FailBuild = true
	_, err := (&Azure{Client: azure.New(f.URL())}).Apply(t.Context(), azureJob(t, apiConfig()))
	if err == nil || !strings.Contains(err.Error(), "non-zero code") || !strings.Contains(err.Error(), "Runs in the container registry") {
		t.Fatalf("err %v", err)
	}
	f.Lock()
	defer f.Unlock()
	if len(f.Apps) != 0 {
		t.Fatalf("apps %v", f.Apps)
	}
}

func TestAzure_buildsWithoutADockerfileUsingBuildpacks(t *testing.T) {
	f := azuretest.New(t)
	exec := &Azure{Client: azure.New(f.URL())}
	cfg := apiConfig()
	cfg.Image = ""
	cfg.Ecosystem = "go"
	cfg.Source = "services/api"
	job := azureJob(t, cfg)
	out, err := exec.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	image := azure.RegistryName(azuretest.Subscription, "loomplane") + ".azurecr.io/api-23456789:" + doApproved[:12]
	if out.Image != image {
		t.Fatalf("image %q, want %q", out.Image, image)
	}
	f.Lock()
	defer f.Unlock()
	if len(f.Tasks) != 1 {
		t.Fatalf("tasks %v", f.Tasks)
	}
	for _, want := range []string{
		"cmd: gcr.io/k8s-skaffold/pack build {{.Run.Registry}}/api-23456789:",
		"--builder gcr.io/buildpacks/builder:latest",
		"--path acme-mono-0123456 ",
		"--env GOOGLE_BUILDABLE",
		`env: ["GOOGLE_BUILDABLE=./services/api"]`,
		"entryPoint: pack",
	} {
		if !strings.Contains(f.Tasks[0], want) {
			t.Fatalf("task lacks %q:\n%s", want, f.Tasks[0])
		}
	}
	if strings.Contains(f.Tasks[0], "build: -t") {
		t.Fatalf("a docker build step:\n%s", f.Tasks[0])
	}
}

func TestAzure_refusesAnActionWithoutAService(t *testing.T) {
	exec := &Azure{Client: azure.New("http://127.0.0.1:1")}
	job := azureJob(t, nil)
	if _, err := exec.Apply(t.Context(), job); err == nil {
		t.Fatal("ran an action with no service")
	}
}

func azureIngress(t *testing.T, f *azuretest.Fake, name string) (map[string]any, map[string]any) {
	t.Helper()
	f.Lock()
	defer f.Unlock()
	props := f.Apps[name]["properties"].(map[string]any)
	conf := props["configuration"].(map[string]any)
	ingress, _ := conf["ingress"].(map[string]any)
	return conf, ingress
}

func TestAzure_workersHaveNoIngressAndNeverScaleToZero(t *testing.T) {
	for _, tt := range []struct {
		name     string
		scale    *manifest.Scale
		min, max any
	}{
		{"scaled to zero", &manifest.Scale{Min: 0, Max: 3}, float64(1), float64(3)},
		{"no scale", nil, float64(1), nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := azuretest.New(t)
			cfg := &planning.ServiceConfig{Source: "services/api", Image: "services/api/Dockerfile", Port: 9090,
				Workload: provider.Worker, Execution: provider.Container, Scale: tt.scale}
			job := azureJob(t, cfg)
			out, err := (&Azure{Client: azure.New(f.URL())}).Apply(t.Context(), job)
			if err != nil {
				t.Fatal(err)
			}
			if out.URL != "" {
				t.Fatalf("a worker has the address %q", out.URL)
			}
			conf, ingress := azureIngress(t, f, AzureName(job))
			if _, has := conf["ingress"]; has || ingress != nil {
				t.Fatalf("a worker has ingress: %v", conf["ingress"])
			}
			f.Lock()
			defer f.Unlock()
			scale := f.Apps[AzureName(job)]["properties"].(map[string]any)["template"].(map[string]any)["scale"].(map[string]any)
			if scale["minReplicas"] != tt.min || scale["maxReplicas"] != tt.max {
				t.Fatalf("scale %v", scale)
			}
		})
	}
}

func TestAzure_aStaticSiteIsAPublicContainerApp(t *testing.T) {
	f := azuretest.New(t)
	cfg := &planning.ServiceConfig{Source: "services/api", Image: "services/api/Dockerfile",
		Workload: provider.StaticSite, Execution: provider.Container}
	job := azureJob(t, cfg)
	out, err := (&Azure{Client: azure.New(f.URL())}).Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	if out.URL == "" {
		t.Fatal("a static site has no address")
	}
	_, ingress := azureIngress(t, f, AzureName(job))
	if ingress["external"] != true || ingress["targetPort"] != float64(80) {
		t.Fatalf("ingress %v", ingress)
	}
}

func TestAzureResources(t *testing.T) {
	for _, tt := range []struct {
		size        *manifest.Size
		cpu         float64
		memory      string
		description string
	}{
		{nil, 0.5, "1Gi", "the default"},
		{&manifest.Size{CPU: 0.25, Memory: 256}, 0.25, "0.5Gi", "the smallest"},
		{&manifest.Size{CPU: 1, Memory: 4096}, 2, "4Gi", "memory decides"},
		{&manifest.Size{CPU: 1.1, Memory: 512}, 1.25, "2.5Gi", "a quarter CPU at a time"},
		{&manifest.Size{CPU: 16, Memory: 65536}, 4, "8Gi", "the largest"},
	} {
		got := azureResources(tt.size)
		if got.CPU != tt.cpu || got.Memory != tt.memory {
			t.Errorf("%s: %+v", tt.description, got)
		}
	}
}

func TestAzureSecretName(t *testing.T) {
	a, b := azureSecretName("API_KEY"), azureSecretName("API-KEY")
	if a == b || !strings.HasPrefix(a, "env-api-key-") || strings.ContainsAny(a, "_ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		t.Fatalf("%q %q", a, b)
	}
}

// shortPullWaits makes a test wait out a role's propagation in
// milliseconds.
func shortPullWaits(t *testing.T, wait time.Duration) {
	t.Helper()
	oldWait, oldRetry := azurePullWait, azurePullRetry
	azurePullWait, azurePullRetry = wait, 10*time.Millisecond
	t.Cleanup(func() { azurePullWait, azurePullRetry = oldWait, oldRetry })
}

// azurePullConf is the registry entry, secrets and identity of the app
// Loomplane PUT last.
func azurePullConf(t *testing.T, f *azuretest.Fake, name string) (reg map[string]any, secrets []string, identity map[string]any) {
	t.Helper()
	f.Lock()
	defer f.Unlock()
	app := f.Apps[name]
	conf := app["properties"].(map[string]any)["configuration"].(map[string]any)
	reg = conf["registries"].([]any)[0].(map[string]any)
	for _, s := range conf["secrets"].([]any) {
		secrets = append(secrets, s.(map[string]any)["name"].(string))
	}
	identity, _ = app["identity"].(map[string]any)
	return reg, secrets, identity
}

func TestAzure_pullsWithTheIdentityWhenItMay(t *testing.T) {
	f := azuretest.New(t)
	f.NotActions = nil
	job := azureJob(t, apiConfig())
	if _, err := (&Azure{Client: azure.New(f.URL())}).Apply(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	id := "/subscriptions/" + azuretest.Subscription + "/resourceGroups/loomplane/providers/Microsoft.ManagedIdentity/userAssignedIdentities/" + azure.PullIdentity
	reg, secrets, identity := azurePullConf(t, f, AzureName(job))
	if reg["identity"] != id || reg["server"] != azure.RegistryName(azuretest.Subscription, "loomplane")+".azurecr.io" {
		t.Fatalf("registry %v", reg)
	}
	if _, has := reg["passwordSecretRef"]; has {
		t.Fatalf("the registry has a password: %v", reg)
	}
	if _, has := reg["username"]; has {
		t.Fatalf("the registry has a user: %v", reg)
	}
	if slices.Contains(secrets, azureRegistrySecret) || len(secrets) != 1 {
		t.Fatalf("secrets %v", secrets)
	}
	users, _ := identity["userAssignedIdentities"].(map[string]any)
	if _, has := users[id]; identity["type"] != "UserAssigned" || !has || len(users) != 1 {
		t.Fatalf("identity %v", identity)
	}
	f.Lock()
	defer f.Unlock()
	if f.IdentityPuts != 1 || len(f.RoleAssignments) != 1 || f.RoleAssignments[0].PrincipalID != azuretest.PullPrincipal {
		t.Fatalf("identity puts %d, role assignments %+v", f.IdentityPuts, f.RoleAssignments)
	}
}

func TestAzure_grantsThePullBeforeTheBuild(t *testing.T) {
	f := azuretest.New(t)
	f.NotActions = nil
	f.FailBuild = true
	if _, err := (&Azure{Client: azure.New(f.URL())}).Apply(t.Context(), azureJob(t, apiConfig())); err == nil {
		t.Fatal("a failed build deployed")
	}
	f.Lock()
	defer f.Unlock()
	// The role propagates while the build runs.
	if f.IdentityPuts != 1 || len(f.RoleAssignments) != 1 || len(f.Tasks) != 1 {
		t.Fatalf("identity puts %d, role assignments %+v, tasks %d", f.IdentityPuts, f.RoleAssignments, len(f.Tasks))
	}
}

func TestAzure_fallsBackToTheAdminUser(t *testing.T) {
	f := azuretest.New(t)
	job := azureJob(t, apiConfig())
	if _, err := (&Azure{Client: azure.New(f.URL())}).Apply(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	reg, secrets, identity := azurePullConf(t, f, AzureName(job))
	if reg["passwordSecretRef"] != azureRegistrySecret || reg["username"] == "" || reg["username"] == nil {
		t.Fatalf("registry %v", reg)
	}
	if _, has := reg["identity"]; has || identity != nil {
		t.Fatalf("registry %v, identity %v", reg, identity)
	}
	if !slices.Contains(secrets, azureRegistrySecret) {
		t.Fatalf("secrets %v", secrets)
	}
	f.Lock()
	defer f.Unlock()
	if f.IdentityPuts != 0 || len(f.RoleAssignments) != 0 {
		t.Fatalf("identity puts %d, role assignments %+v", f.IdentityPuts, f.RoleAssignments)
	}
}

func TestAzure_waitsForTheRoleToPropagate(t *testing.T) {
	shortPullWaits(t, time.Minute)
	f := azuretest.New(t)
	f.NotActions = nil
	f.PullDeniedFor = 2
	exec := &Azure{Client: azure.New(f.URL())}
	job := azureJob(t, apiConfig())
	out, err := exec.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	// The third revision is the one that pulled, and it's ready.
	s, err := azureSession(t.Context(), exec.Client, job.Connection, job.Credential)
	if err != nil {
		t.Fatal(err)
	}
	live, err := s.GetApp(t.Context(), out.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	third := AzureName(job) + "--3"
	if p := live.Properties; p.LatestRevisionName != third || p.LatestReadyRevision != third {
		t.Fatalf("latest %q, ready %q", p.LatestRevisionName, p.LatestReadyRevision)
	}
}

func TestAzure_givesUpWhenThePullNeverWorks(t *testing.T) {
	shortPullWaits(t, 150*time.Millisecond)
	f := azuretest.New(t)
	f.NotActions = nil
	f.PullDeniedFor = 1000
	_, err := (&Azure{Client: azure.New(f.URL())}).Apply(t.Context(), azureJob(t, apiConfig()))
	if err == nil || !strings.Contains(err.Error(), "identity") || !strings.Contains(err.Error(), "retry the deployment") {
		t.Fatalf("err %v", err)
	}
}

func TestAzure_waitsForEntraToKnowTheIdentity(t *testing.T) {
	shortPullWaits(t, time.Minute)
	f := azuretest.New(t)
	f.NotActions = nil
	f.PrincipalMissingFor = 2
	if _, err := (&Azure{Client: azure.New(f.URL())}).Apply(t.Context(), azureJob(t, apiConfig())); err != nil {
		t.Fatal(err)
	}
	f.Lock()
	defer f.Unlock()
	if len(f.RoleAssignments) != 1 {
		t.Fatalf("role assignments %+v", f.RoleAssignments)
	}
}

func TestAzure_givesUpWhenEntraNeverKnowsTheIdentity(t *testing.T) {
	shortPullWaits(t, 50*time.Millisecond)
	f := azuretest.New(t)
	f.NotActions = nil
	f.PrincipalMissingFor = 1000
	_, err := (&Azure{Client: azure.New(f.URL())}).Apply(t.Context(), azureJob(t, apiConfig()))
	var ae *azure.APIError
	if !errors.As(err, &ae) || ae.Code != "PrincipalNotFound" || !strings.Contains(err.Error(), "retry the deployment") {
		t.Fatalf("err %v", err)
	}
	f.Lock()
	defer f.Unlock()
	if len(f.Tasks) != 0 || len(f.Apps) != 0 {
		t.Fatalf("built %d, apps %v", len(f.Tasks), f.Apps)
	}
}
