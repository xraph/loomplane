package deployments

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/registry"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/planning"
)

// platformRegistry is the platform registry: a token service that records
// the scope of every token it mints.
type platformRegistry struct {
	mu     sync.Mutex
	url    string
	scopes []string
	// anonymous lets every probe through without a challenge.
	anonymous bool
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
		r.mu.Lock()
		anonymous := r.anonymous
		r.mu.Unlock()
		if anonymous {
			w.WriteHeader(http.StatusNotFound)
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
	return &registry.Config{Endpoint: r.url, Repository: "loomplane/ws-" + org, Username: "platform", Password: "platform-registry-secret", ScopedOnly: true}
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

// routeOf is the host's route for hostname.
func (h *platformHost) routeOf(t *testing.T, hostname string) (fc.RouteRecord, bool) {
	t.Helper()
	r, err := h.host.Store.Route(t.Context(), hostname)
	if errors.Is(err, fc.ErrMissing) {
		return r, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return r, true
}

// A public service is reached through its generated hostname on the host's
// ingress, never through a port the VM publishes, and a service that goes
// private stops answering on the name.
func TestPlatformServicePublishedOnItsGeneratedHostname(t *testing.T) {
	f := newPlatformFixture(t)
	f.platform.apex = "apps.test"
	job := f.job(t)
	job.Action.Service.Public, job.Action.Service.Port = true, 8080
	if _, err := f.target.Apply(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	host := f.hosts["host_a"]
	const name = "web-abcdefghijkl.apps.test"
	r, ok := host.routeOf(t, name)
	want := fc.Scope{Org: "org_a", Connection: fc.PlatformConnection, Project: "prj_1", Environment: "env_1", Resource: f.iid.String()}
	if !ok || r.Scope != want || r.Route.Port != 8080 || r.Route.Remove {
		t.Fatalf("route %+v (found %v)", r, ok)
	}
	if records := host.records(t); len(records) != 1 || records[0].Current == nil || records[0].Current.Spec.PublishedPort != 0 || records[0].Current.Spec.Port != 8080 {
		t.Fatalf("host keeps %+v", records)
	}
	if len(f.platform.published) == 0 || f.platform.published[len(f.platform.published)-1] != "host_a" {
		t.Fatalf("published %v", f.platform.published)
	}

	job = f.job(t)
	job.Action.Service.Public, job.Action.Service.Port = false, 8080
	if _, err := f.target.Apply(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if r, ok = host.routeOf(t, name); !ok || !r.Route.Remove {
		t.Fatalf("a private service still has %+v", r)
	}
	if f.platform.hostnames["web"] != "" {
		t.Fatal("a private service kept its hostname")
	}
}

// A platform registry that lets anything through without minting a scoped
// token is refused before a build VM is started.
func TestPlatformBuildRefusesUnscopedRegistry(t *testing.T) {
	f := newPlatformFixture(t)
	job := f.sourceJob(t)
	f.platform.registry.anonymous = true
	if _, err := f.target.Apply(t.Context(), job); err == nil {
		t.Fatal("built against a registry that mints no scoped token")
	}
	if host := f.hosts["host_a"]; host.called("/v1/apply") || host.called("/v1/source") {
		t.Fatalf("the host was sent work: %v", host.paths)
	}
}

func TestPlatformRepositoryReadsOnlyCanonicalReferences(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("c", 64)
	for image, want := range map[string]string{
		"reg.test/loomplane/ws-org_a/loomplane/web-1:abc" + digest: "loomplane/ws-org_a/loomplane/web-1",
		"reg.test/loomplane/ws-org_a/loomplane/web" + digest:       "loomplane/ws-org_a/loomplane/web",
		"reg.test/loomplane/ws-org_a/../ws-org_b/web" + digest:     "",
		"reg.test/loomplane/ws-org_a//web" + digest:                "",
		"reg.test/loomplane/ws-org_a/Web" + digest:                 "",
		"reg.test/loomplane/ws-org_ab/web" + digest:                "",
		"reg.test/loomplane/ws-org_a/web:latest":                   "",
		"other.test/loomplane/ws-org_a/web" + digest:               "",
	} {
		got, ok := platformRepository(image, "reg.test", "loomplane/ws-org_a")
		if got != want || ok != (want != "") {
			t.Errorf("%s: %q %v", image, got, ok)
		}
	}
}
