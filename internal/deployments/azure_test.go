package deployments

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

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

func TestAzure_refusesWhatItCantBuild(t *testing.T) {
	exec := &Azure{Client: azure.New("http://127.0.0.1:1")}
	static := &planning.ServiceConfig{Source: "web", Workload: provider.StaticSite, Execution: provider.Static}
	if _, err := exec.Apply(t.Context(), azureJob(t, static)); err == nil {
		t.Fatal("ran a static site")
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
