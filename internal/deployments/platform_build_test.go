package deployments

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/registry"
	"github.com/xraph/loomplane/internal/planning"
)

// platformRegistry is the platform registry: a token service that records
// the scope of every token it mints.
type platformRegistry struct {
	mu     sync.Mutex
	url    string
	scopes []string
}

func newPlatformRegistry(t *testing.T) *platformRegistry {
	t.Helper()
	r := &platformRegistry{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/token" {
			r.mu.Lock()
			r.scopes = append(r.scopes, req.URL.Query().Get("scope"))
			r.mu.Unlock()
			_, _ = io.WriteString(w, `{"token":"platform-scoped-token"}`)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+r.url+`/token",service="platform"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	r.url = server.URL
	return r
}

// config is the platform registry as platformcloud hands it to a build or
// deploy of org: the workspace's own repository under the platform's.
func (r *platformRegistry) config(org string) *registry.Config {
	return &registry.Config{Endpoint: r.url, Repository: "loomplane/ws-" + org, Username: "platform", Password: "platform-registry-secret"}
}

func (r *platformRegistry) minted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.scopes...)
}

// sourceTarball is an approved source archive with a Dockerfile.
func sourceTarball(t *testing.T) []byte {
	t.Helper()
	var tgz bytes.Buffer
	zw := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(zw)
	for _, f := range [][2]string{{"team-web-0123456/Dockerfile", "FROM scratch"}, {"team-web-0123456/main.go", "package main"}} {
		if err := tw.WriteHeader(&tar.Header{Name: f[0], Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(f[1]))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return tgz.Bytes()
}

// sourceJob deploys web, built from source on Loomplane Cloud.
func (f *platformFixture) sourceJob(t *testing.T) *Job {
	t.Helper()
	f.platform.registry = newPlatformRegistry(t)
	job := f.job(t)
	cfg := job.Action.Service
	cfg.Image, cfg.Context = "Dockerfile", "."
	cfg.BuildTarget = &planning.BuildTarget{BuilderCloud: "platform", Builder: "platform"}
	source := sourceTarball(t)
	job.SourceID = strings.Repeat("a", 40)
	job.Source = func(context.Context) (io.ReadCloser, int64, error) {
		return io.NopCloser(bytes.NewReader(source)), int64(len(source)), nil
	}
	job.Logs = &buildLogFixture{}
	return job
}

// A host that no longer passes the platform policy is never sent source: the
// build stops before its VM starts, and its placement is given back.
func TestPlatformBuildRefusedWithoutPlatformEvidence(t *testing.T) {
	f := newPlatformFixture(t)
	job := f.sourceJob(t)
	host := f.hosts["host_a"]
	host.driver.smt = true
	_, err := f.target.Apply(t.Context(), job)
	if err == nil {
		t.Fatal("built on a host with SMT on")
	}
	if host.called("/v1/source") || host.called("/v1/apply") || host.driver.sources != 0 {
		t.Fatalf("the host was sent work: %v", host.paths)
	}
	for _, leak := range []string{host.id, host.server.URL, "127.0.0.1"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("the error names the host: %v", err)
		}
	}
	for _, p := range f.platform.placements {
		if p.Kind == "build" && p.State != "released" {
			t.Fatalf("build placement %+v kept", p)
		}
	}
}

// A build pushes with a token scoped to the workspace's own repository, and
// the deploy pulls the image it built with a pull token scoped the same way.
func TestPlatformBuildPushesToWorkspaceRepository(t *testing.T) {
	f := newPlatformFixture(t)
	job := f.sourceJob(t)
	out, err := f.target.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	registryHost := strings.TrimPrefix(f.platform.registry.url, "http://")
	if !strings.HasPrefix(out.Image, registryHost+"/loomplane/ws-org_a/loomplane/web-") || !strings.HasSuffix(out.Image, "@sha256:"+strings.Repeat("c", 64)) {
		t.Fatalf("image %q", out.Image)
	}
	scopes := f.platform.registry.minted()
	var push, pull int
	for _, scope := range scopes {
		if !strings.HasPrefix(scope, "repository:loomplane/ws-org_a/loomplane/web-") {
			t.Fatalf("a token was minted outside the workspace repository: %q", scope)
		}
		switch {
		case strings.HasSuffix(scope, ":pull,push"):
			push++
		case strings.HasSuffix(scope, ":pull"):
			pull++
		}
	}
	if push != 1 || pull == 0 {
		t.Fatalf("minted %q", scopes)
	}
	d := f.hosts["host_a"].driver
	if d.sources != 1 {
		t.Fatalf("the build VM was sent %d source archives", d.sources)
	}
	last := d.registry[len(d.registry)-1]
	if !strings.Contains(string(last), "platform-scoped-token") || strings.Contains(string(last), "platform-registry-secret") {
		t.Fatalf("the service VM pulls with %q", last)
	}
	p := f.platform.live("org_a", "env_1", "web")
	if p == nil || p.State != "running" {
		t.Fatalf("service placement %+v", p)
	}
}

// The build placement goes back when the build VM exits, whether the build
// worked or not, and the build is metered either way.
func TestPlatformBuildReleasedOnExit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		f := newPlatformFixture(t)
		job := f.sourceJob(t)
		f.hosts["host_a"].driver.failBuild = fail
		_, err := f.target.Apply(t.Context(), job)
		if (err != nil) != fail {
			t.Fatalf("fail=%v: %v", fail, err)
		}
		var builds int
		for _, p := range f.platform.placements {
			if p.Kind != "build" {
				continue
			}
			builds++
			if p.State != "released" || p.InstanceID != "" {
				t.Fatalf("fail=%v: build placement %+v", fail, p)
			}
			if p.VCPUs != 2 || p.MemoryMiB != 2048 || p.DiskMiB != 16384 || p.Resource == "web" {
				t.Fatalf("fail=%v: build reserved %+v", fail, p)
			}
		}
		if builds != 1 || len(f.platform.builds) != 1 {
			t.Fatalf("fail=%v: %d build placements, %d metered", fail, builds, len(f.platform.builds))
		}
		if f.hosts["host_a"].driver.removed == 0 {
			t.Fatalf("fail=%v: the build VM was left on its host", fail)
		}
	}
}
