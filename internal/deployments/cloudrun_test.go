package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/azure/azuretest"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

func TestBuildFor(t *testing.T) {
	const img = "us-central1-docker.pkg.dev/p/loomplane/api-abc:sha"
	cases := []struct {
		name string
		cfg  planning.ServiceConfig
		step string
		dir  string
		args []string
	}{
		{"a dockerfile builds from its own directory", planning.ServiceConfig{Image: "services/api/Dockerfile"},
			dockerBuilder, "acme-mono-1/services/api", []string{"build", "-t", img, "-f", "Dockerfile", "."}},
		{"one at the root builds from the root", planning.ServiceConfig{Image: "Dockerfile.prod"},
			dockerBuilder, "acme-mono-1", []string{"build", "-t", img, "-f", "Dockerfile.prod", "."}},
		{"a build context from the root names the Dockerfile from there", planning.ServiceConfig{Image: "services/api/Dockerfile", Context: "."},
			dockerBuilder, "acme-mono-1", []string{"build", "-t", img, "-f", "services/api/Dockerfile", "."}},
		{"go without one uses the buildpack on its main package", planning.ServiceConfig{Ecosystem: "go", Source: "services/api"},
			packBuilder, "", []string{"build", img, "--builder", googleBuilder, "--publish", "--env", "GOOGLE_BUILDABLE=./services/api", "--path", "acme-mono-1"}},
		{"a jvm module builds from the root of its build", planning.ServiceConfig{Ecosystem: "jvm", Source: "services/api"},
			packBuilder, "", []string{"build", img, "--builder", googleBuilder, "--publish", "--env", "GOOGLE_BUILDABLE=services/api", "--path", "acme-mono-1"}},
		{"python without one builds its project", planning.ServiceConfig{Ecosystem: "python", Source: "services/api"},
			packBuilder, "", []string{"build", img, "--builder", googleBuilder, "--publish", "--path", "acme-mono-1/services/api"}},
		{"a python process says how it starts", planning.ServiceConfig{Ecosystem: "python", Source: "services/api", Start: "celery -A shop worker"},
			packBuilder, "", []string{"build", img, "--builder", googleBuilder, "--publish", "--env", "GOOGLE_ENTRYPOINT=celery -A shop worker", "--path", "acme-mono-1/services/api"}},
		{"node without one builds its package", planning.ServiceConfig{Ecosystem: "node", Source: "apps/gateway"},
			packBuilder, "", []string{"build", img, "--builder", googleBuilder, "--publish", "--path", "acme-mono-1/apps/gateway"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := buildFor(&tc.cfg, "acme-mono-1", img)
			if len(b.Steps) != 1 || b.Steps[0].Name != tc.step || b.Steps[0].Dir != tc.dir || !slices.Equal(b.Steps[0].Args, tc.args) {
				t.Fatalf("steps %+v", b.Steps)
			}
			if (tc.step == dockerBuilder) != (len(b.Images) == 1) {
				t.Fatalf("images %v", b.Images)
			}
		})
	}
}

// createdInstances is the Ctrl Plane a Cloud Run step sees with nothing
// deployed yet: it remembers what it was asked to create.
type createdInstances struct {
	instance.Service
	created []instance.CreateRequest
}

func (c *createdInstances) GetBySlug(context.Context, string) (*instance.Instance, error) {
	return nil, ctrlplane.ErrNotFound
}

func (c *createdInstances) Create(_ context.Context, r instance.CreateRequest) (*instance.Instance, error) {
	c.created = append(c.created, r)
	return &instance.Instance{Entity: ctrlplane.Entity{ID: id.New(id.PrefixInstance)}, Name: r.Name}, nil
}

// cloudBuildFake is the Google behind a Cloud Run build. Cloud Build
// reports resultDigest for the image it pushed, when it's set, and
// Artifact Registry's tag points at tagDigest, when that's set.
type cloudBuildFake struct {
	mu                      sync.Mutex
	resultDigest, tagDigest string
	// tagStatus answers a tag read with that status instead, and
	// tagUnversioned with a tag that names no version.
	tagStatus      int
	tagUnversioned bool
	builds         []map[string]any
}

func (f *cloudBuildFake) serve(t *testing.T) *httptest.Server {
	t.Helper()
	const project = "/v1/projects/shop"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := r.URL.Path
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch {
		case p == "/token":
			reply(map[string]any{"access_token": "token", "token_type": "Bearer", "expires_in": 3600})
		case r.Method == http.MethodPost && (p == "/storage/v1/b" || p == "/upload/storage/v1/b/shop_loomplane/o"):
			reply(map[string]any{})
		case r.Method == http.MethodPost && p == project+"/locations/europe-west1/repositories":
			reply(map[string]any{"name": "op"})
		case r.Method == http.MethodGet && p == project+"/locations/europe-west1/repositories/loomplane":
			reply(map[string]any{})
		case r.Method == http.MethodGet && strings.HasPrefix(p, project+"/locations/europe-west1/repositories/loomplane/packages/"):
			switch {
			case f.tagStatus != 0:
				w.WriteHeader(f.tagStatus)
				reply(map[string]any{"error": map[string]any{"message": "Permission 'artifactregistry.tags.get' denied"}})
				return
			case f.tagUnversioned:
				reply(map[string]any{"name": strings.TrimPrefix(p, "/v1/")})
				return
			}
			if f.tagDigest == "" {
				w.WriteHeader(http.StatusNotFound)
				reply(map[string]any{"error": map[string]any{"message": "tag not found"}})
				return
			}
			pkg, _, _ := strings.Cut(strings.TrimPrefix(p, project+"/locations/europe-west1/repositories/loomplane/packages/"), "/tags/")
			reply(map[string]any{"name": strings.TrimPrefix(p, "/v1/"), "version": "projects/shop/locations/europe-west1/repositories/loomplane/packages/" + pkg + "/versions/" + f.tagDigest})
		case r.Method == http.MethodPost && p == project+"/builds":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			f.builds = append(f.builds, b)
			reply(map[string]any{"metadata": map[string]any{"build": map[string]any{"id": "build-1"}}})
		case r.Method == http.MethodGet && p == project+"/builds/build-1":
			images := []any{}
			if f.resultDigest != "" {
				for _, name := range f.builds[0]["images"].([]any) {
					images = append(images, map[string]any{"name": name, "digest": f.resultDigest})
				}
			}
			reply(map[string]any{"id": "build-1", "status": "SUCCESS", "results": map[string]any{"images": images}})
		default:
			t.Errorf("asked %s %s", r.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// cloudRunJob is api, built from its Dockerfile unless cfg says
// otherwise, for production in europe-west1.
func cloudRunJob(t *testing.T, cfg *planning.ServiceConfig, tokenURL string) *Job {
	t.Helper()
	job := azureJob(t, cfg)
	job.Action.Region, job.Action.Provider = "europe-west1", "cloud-run"
	job.Deployment.ID = "dep_1"
	job.Connection = &connections.Connection{ID: "conn_g", Cloud: connections.GCP}
	job.Credential = serviceAccountKey(t, tokenURL)
	job.Env, job.Secrets = nil, nil
	return job
}

func TestCloudRun_deploysTheBuiltImageByDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("c4", 32)
	repo := "europe-west1-docker.pkg.dev/shop/loomplane/api-23456789"
	for _, tc := range []struct {
		name string
		cfg  *planning.ServiceConfig
		fake *cloudBuildFake
	}{
		// Cloud Build pushes a Dockerfile's image and reports its digest.
		{"a dockerfile build", apiConfig(), &cloudBuildFake{resultDigest: digest}},
		// pack pushes the image itself, so Cloud Build reports none and
		// Artifact Registry's tag says what was pushed.
		{"a buildpack build", &planning.ServiceConfig{Ecosystem: "go", Source: "services/api", Port: 8080, Public: true}, &cloudBuildFake{tagDigest: digest}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := tc.fake.serve(t)
			instances := &createdInstances{}
			exec := &CloudRun{Google: gcp.New(srv.URL), Ctrl: CtrlPlane{Providers: cpprovider.NewRegistry(), Instances: instances}}
			out, err := exec.Apply(t.Context(), cloudRunJob(t, tc.cfg, srv.URL+"/token"))
			if err != nil {
				t.Fatal(err)
			}
			if want := repo + "@" + digest; out.Image != want || out.Revision != want {
				t.Fatalf("outcome %+v, want %s", out, want)
			}
			if len(instances.created) != 1 || len(instances.created[0].Services) != 1 || instances.created[0].Services[0].Image != out.Image {
				t.Fatalf("Ctrl Plane was asked for %+v", instances.created)
			}
		})
	}
}

// A build that reports no digest for its image fails its step before
// anything is deployed: a release always records what it ran by digest.
func TestDigestMissingFailsTheStep(t *testing.T) {
	const missing = "the build reported no image digest"
	t.Run("cloud run", func(t *testing.T) {
		srv := (&cloudBuildFake{}).serve(t)
		instances := &createdInstances{}
		exec := &CloudRun{Google: gcp.New(srv.URL), Ctrl: CtrlPlane{Providers: cpprovider.NewRegistry(), Instances: instances}}
		_, err := exec.Apply(t.Context(), cloudRunJob(t, apiConfig(), srv.URL+"/token"))
		if err == nil || !strings.Contains(err.Error(), missing) || len(instances.created) != 0 {
			t.Fatalf("err %v, created %+v", err, instances.created)
		}
	})
	t.Run("ecs", func(t *testing.T) {
		for _, answer := range []string{ecrNotFound, ecrNoImage} {
			if _, err := ecsBuild(t, answer); err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("ECR answering %q: %v", answer, err)
			}
		}
	})
	t.Run("azure", func(t *testing.T) {
		f := azuretest.New(t)
		f.NoDigest = true
		_, err := (&Azure{Client: azure.New(f.URL())}).Apply(t.Context(), azureJob(t, apiConfig()))
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatalf("err %v", err)
		}
		f.Lock()
		defer f.Unlock()
		if len(f.Apps) != 0 {
			t.Fatalf("apps %v", f.Apps)
		}
	})
}

// An account that may not read Artifact Registry's tags deploys a
// buildpack image by tag, as before images were pinned, and the step says
// why. One that reads the tag and finds no version still fails.
func TestCloudRun_deploysByTagWhenItMayNotReadTheDigest(t *testing.T) {
	pack := func() *planning.ServiceConfig {
		return &planning.ServiceConfig{Ecosystem: "go", Source: "services/api", Port: 8080, Public: true}
	}
	srv := (&cloudBuildFake{tagStatus: http.StatusForbidden}).serve(t)
	instances := &createdInstances{}
	exec := &CloudRun{Google: gcp.New(srv.URL), Ctrl: CtrlPlane{Providers: cpprovider.NewRegistry(), Instances: instances}}
	job := cloudRunJob(t, pack(), srv.URL+"/token")
	var warnings []string
	job.warn = func(err error) { warnings = append(warnings, err.Error()) }
	var saved cloudRunState
	job.Checkpoint = func(data []byte) error { return json.Unmarshal(data, &saved) }
	out, err := exec.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	tagged := "europe-west1-docker.pkg.dev/shop/loomplane/api-23456789:" + doApproved[:12]
	if out.Image != tagged || len(instances.created) != 1 || instances.created[0].Services[0].Image != tagged || saved.Unpinned != "artifactregistry.tags.get" {
		t.Fatalf("outcome %+v, checkpoint %+v", out, saved)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "artifactregistry.tags.get") || !strings.Contains(warnings[0], "rolling back api builds it again") {
		t.Fatalf("warnings %q", warnings)
	}

	// A retry after the build still says why.
	warnings = nil
	job.Resume, _ = json.Marshal(saved)
	if _, err := exec.Apply(t.Context(), job); err != nil || len(warnings) != 1 {
		t.Fatalf("retry: %v, warnings %q", err, warnings)
	}

	srv = (&cloudBuildFake{tagUnversioned: true}).serve(t)
	exec = &CloudRun{Google: gcp.New(srv.URL), Ctrl: CtrlPlane{Providers: cpprovider.NewRegistry(), Instances: &createdInstances{}}}
	if _, err := exec.Apply(t.Context(), cloudRunJob(t, pack(), srv.URL+"/token")); err == nil || !strings.Contains(err.Error(), "the build reported no image digest") {
		t.Fatalf("a tag with no version: %v", err)
	}
}

// reusing makes job deploy reference, the image staging ran, instead of
// building: opening its source fails the test. It returns the progress
// messages the step reports.
func reusing(t *testing.T, job *Job, reference string) *[]string {
	t.Helper()
	cfg := *job.Action.Service
	_, digest, _ := strings.Cut(reference, "@")
	cfg.Artifact = &planning.ReusedArtifact{Reference: reference, Digest: digest, CommitSHA: doApproved, FromEnvironment: "staging", FromDeploymentID: "dep_staging"}
	job.Action.Service = &cfg
	job.Source = func(context.Context) (io.ReadCloser, int64, error) {
		t.Error("the step opened its source to build it")
		return nil, 0, errors.New("no source")
	}
	var mu sync.Mutex
	messages := &[]string{}
	job.Progress = func(_, m string) {
		mu.Lock()
		defer mu.Unlock()
		*messages = append(*messages, m)
	}
	return messages
}

func TestCloudRun_deploysAReusedImageWithoutBuilding(t *testing.T) {
	reused := "europe-west1-docker.pkg.dev/shop/loomplane/api-23456789@sha256:" + strings.Repeat("d6", 32)
	fake := &cloudBuildFake{}
	srv := fake.serve(t)
	instances := &createdInstances{}
	exec := &CloudRun{Google: gcp.New(srv.URL), Ctrl: CtrlPlane{Providers: cpprovider.NewRegistry(), Instances: instances}}
	job := cloudRunJob(t, apiConfig(), srv.URL+"/token")
	messages := reusing(t, job, reused)
	out, err := exec.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	if out.Image != reused || out.Revision != reused {
		t.Fatalf("outcome %+v, want %s", out, reused)
	}
	if len(instances.created) != 1 || instances.created[0].Services[0].Image != reused {
		t.Fatalf("Ctrl Plane was asked for %+v", instances.created)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.builds) != 0 {
		t.Fatalf("started builds %v", fake.builds)
	}
	if !slices.Contains(*messages, "Using the image staging ran") {
		t.Fatalf("progress %q", *messages)
	}
}
