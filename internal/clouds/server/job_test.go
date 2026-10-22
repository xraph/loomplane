package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

const jobFixtureName = "lp-job-0123456789ab"

// dockerFrame is one frame of a container's multiplexed log stream.
func dockerFrame(stream byte, text string) []byte {
	header := make([]byte, 8, 8+len(text))
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(text)))
	return append(header, text...)
}

// jobDocker is a Docker daemon that runs one job container: wait answers
// with exitCode, or blocks until the client gives up when hang is set.
type jobDocker struct {
	t        *testing.T
	exitCode int
	hang     bool
	logs     []byte

	mu       sync.Mutex
	create   map[string]any
	requests []string
}

func (d *jobDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.requests = append(d.requests, r.Method+" "+r.URL.RequestURI())
	d.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/containers/"+jobFixtureName+"/json":
		http.NotFound(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/networks/"+NetworkName("org_fixture", "env_fixture"):
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "network_one", "Labels": map[string]string{owned.LabelOrg: "org_fixture", owned.LabelEnvironment: "env_fixture"}})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/images/"):
		_, _ = io.WriteString(w, `{}`)
	case r.Method == http.MethodPost && r.URL.Path == "/containers/create":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			d.t.Error(err)
		}
		d.mu.Lock()
		d.create = body
		d.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"Id":"job_one"}`)
	case r.Method == http.MethodPost && r.URL.Path == "/containers/job_one/start":
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && r.URL.Path == "/containers/job_one/wait":
		if d.hang {
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"StatusCode": d.exitCode})
	case r.Method == http.MethodGet && r.URL.Path == "/containers/job_one/logs":
		_, _ = w.Write(d.logs)
	case r.Method == http.MethodPost && r.URL.Path == "/containers/job_one/kill":
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && r.URL.Path == "/containers/job_one":
		w.WriteHeader(http.StatusNoContent)
	default:
		d.t.Errorf("unexpected job request: %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

func (d *jobDocker) runtime(t *testing.T) *Runtime {
	t.Helper()
	api := httptest.NewServer(d)
	t.Cleanup(api.Close)
	u, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		return &Client{Config: &Config{Details: map[string]string{"docker_id": "daemon_fixture"}}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
			return http.DefaultTransport.RoundTrip(r)
		})}}, nil
	}, Values: func(context.Context, map[string]string) (owned.RuntimeValues, error) {
		return owned.RuntimeValues{Env: map[string]string{"ADMIN_PASSWORD": "s3cret-Generated-value"}}, nil
	}}
}

func (d *jobDocker) called(request string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Contains(d.requests, request)
}

func jobFixture() (cpprovider.ServiceSpec, map[string]string) {
	spec := cpprovider.ServiceSpec{Name: "main", Image: "registry.example/sample-migrate@sha256:" + strings.Repeat("a", 64), Command: []string{"sample", "migrate"}, Env: map[string]string{"MODE": "migrate"}, Resources: cpprovider.ResourceSpec{CPUMillis: 500, MemoryMB: 256}}
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelConnection: "conn_fixture", owned.LabelRuntime: "vault_reference", owned.LabelCatalogComponent: "migrate", owned.LabelRevision: "dep/resource:app--migrate"}
	return spec, labels
}

func TestServerRunJobRemovesContainerAndRedactsOutput(t *testing.T) {
	logs := append(dockerFrame(1, "connecting with s3cret-Generated-value\n"), dockerFrame(2, "migration 0003 failed\n")...)
	docker := &jobDocker{t: t, exitCode: 1, logs: logs}
	spec, labels := jobFixture()
	result, err := docker.runtime(t).RunJob(t.Context(), jobFixtureName, spec, labels, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 1 || result.Output != "connecting with [redacted]\nmigration 0003 failed\n" {
		t.Fatalf("result: %+v", result)
	}
	host, _ := docker.create["HostConfig"].(map[string]any)
	restart, _ := host["RestartPolicy"].(map[string]any)
	if restart["Name"] != "no" {
		t.Fatalf("restart policy: %v", host["RestartPolicy"])
	}
	networking, _ := docker.create["NetworkingConfig"].(map[string]any)
	endpoints, _ := networking["EndpointsConfig"].(map[string]any)
	if _, ok := endpoints[NetworkName("org_fixture", "env_fixture")]; !ok || len(endpoints) != 1 {
		t.Fatalf("network: %v", endpoints)
	}
	encoded, _ := json.Marshal(docker.create["Env"])
	if !strings.Contains(string(encoded), "ADMIN_PASSWORD=s3cret-Generated-value") || !strings.Contains(string(encoded), "MODE=migrate") {
		t.Fatalf("job env: %s", encoded)
	}
	encoded, _ = json.Marshal(docker.create["Labels"])
	if strings.Contains(string(encoded), "s3cret") || !strings.Contains(string(encoded), jobFixtureName) {
		t.Fatalf("job labels: %s", encoded)
	}
	if !docker.called("POST /containers/create?name="+jobFixtureName) || !docker.called("POST /containers/job_one/start") {
		t.Fatal("the job never started")
	}
	if !docker.called("DELETE /containers/job_one?force=1") {
		t.Fatalf("the job container was left behind: %v", docker.requests)
	}
}

func TestServerRunJobTimesOut(t *testing.T) {
	docker := &jobDocker{t: t, hang: true, logs: dockerFrame(1, "connecting with s3cret-Generated-value\napplying 0003\n")}
	spec, labels := jobFixture()
	_, err := docker.runtime(t).RunJob(t.Context(), jobFixtureName, spec, labels, 100*time.Millisecond)
	if err == nil || err.Error() != "migrate did not finish within 100ms: connecting with [redacted]\napplying 0003\n" {
		t.Fatalf("timeout: %v", err)
	}
	// Its output is read before it's killed and removed.
	logs := slices.Index(docker.requests, "GET /containers/job_one/logs?stdout=1&stderr=1&tail=2000")
	kill := slices.Index(docker.requests, "POST /containers/job_one/kill")
	if logs < 0 || kill < logs || !docker.called("DELETE /containers/job_one?force=1") {
		t.Fatalf("a job that ran out of time: %v", docker.requests)
	}
}
