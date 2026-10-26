//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSharedCloudLiveLifecycleStages(t *testing.T) {
	for _, stage := range []string{"plan_create", "plan_wait", "plan_approve", "deployment_wait", "binding_read", "route_preview", "route_apply", "route_wait", "public_https", "migration_preview", "migration_apply", "migration_wait", "future_release"} {
		if !liveStageAllowed(stage) {
			t.Errorf("missing lifecycle stage %s", stage)
		}
	}
}

func TestSharedCloudLiveLifecycleSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Error("missing isolated authentication")
		}
		_, _ = w.Write([]byte(`{"status":"secret-status","error":"secret-provider-error","message":"https://secret-host","executable":false,"problems":[{"message":"secret-config"}],"steps":[{"status":"failed","message":"secret-log"},{"status":"secret-step"}],"bindings":[{"config":"secret-value"}]}`))
	}))
	defer server.Close()
	directory := t.TempDir()
	c := &client{base: server.URL, token: "secret-token"}
	if err := liveCaptureLifecycle(filepath.Join(directory, "state.json"), &liveProgress{Stage: "deployment_wait"}, c, []liveLifecycleScope{{Project: "proj_01h455vb4pex5vsknk084sn02q", Environment: "env_01h455vb4pex5vsknk084sn02q", Plan: "plan_01h455vb4pex5vsknk084sn02q", Route: "routeop_01h455vb4pex5vsknk084sn02q", Sync: "sync_01h455vb4pex5vsknk084sn02q"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "run-lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"kind": "plan"`, `"kind": "deployment"`, `"kind": "bindings"`, `"kind": "route"`, `"kind": "sync"`, `"unknown"`, `"problems_present"`, `"failed"`} {
		if !strings.Contains(string(raw), expected) {
			t.Errorf("missing %s", expected)
		}
	}
	if strings.Contains(string(raw), "secret-") {
		t.Fatal("raw evidence leaked")
	}
}

func TestSharedCloudLiveLifecycleReadFailureBound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	result := liveReadLifecycle(ctx, &client{base: server.URL}, "plan", "/plan")
	if result.Code != "read_unavailable" || time.Since(start) > time.Second {
		t.Fatalf("read did not respect deadline: %+v", result)
	}
}

func TestSharedCloudLiveLifecycleBeforeCleanup(t *testing.T) {
	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), executable, "-test.run=^TestSharedCloudLiveLifecycleFailureChild$")
	command.Env = append(os.Environ(), "LOOMPLANE_LIFECYCLE_TEST="+directory)
	if command.Run() == nil {
		t.Fatal("failure fixture passed")
	}
	raw, err := os.ReadFile(filepath.Join(directory, "run-lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"stage": "deployment_wait"`, `"code": "deployment_failed"`, `"http_status": 409`, `"status": "failed"`} {
		if !strings.Contains(string(raw), expected) {
			t.Errorf("missing %s in %s", expected, raw)
		}
	}
	if _, err = os.Stat(filepath.Join(directory, "database-dropped")); err != nil {
		t.Fatal("cleanup did not run")
	}
}

func TestSharedCloudLiveLifecycleFailureChild(t *testing.T) {
	directory := os.Getenv("LOOMPLANE_LIFECYCLE_TEST")
	if directory == "" {
		t.Skip("subprocess fixture only")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"failed","error":"secret-provider-error"}`))
	}))
	defer func() { server.Close(); _ = os.WriteFile(filepath.Join(directory, "database-dropped"), nil, 0o600) }()
	progress := newLiveProgress(t, directory)
	progress.step(t, "deployment_wait")
	progress.HTTPStatus = http.StatusConflict
	c := &client{base: server.URL, observeStatus: func(int) { t.Error("diagnostic altered original HTTP status") }}
	scopes, capture := liveLifecycleDiagnostics(t, progress, c, []string{"proj_01h455vb4pex5vsknk084sn02q"})
	scopes[0].Plan = "plan_01h455vb4pex5vsknk084sn02q"
	scopes[0].Environment = "env_01h455vb4pex5vsknk084sn02q"
	defer capture()
	progress.failure(t, "deployment_failed")
	t.Fatal("original deployment failure")
}

func TestSharedCloudLiveLifecycleReadRedaction(t *testing.T) {
	for _, body := range []string{`{"status":"secret-status"}`, `not json`, strings.Repeat("x", 256*1024+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		result := liveReadLifecycle(t.Context(), &client{base: server.URL}, "plan", "/plan")
		server.Close()
		if body[0] == '{' {
			if result.Status != "unknown" {
				t.Fatal("unknown status retained")
			}
		} else if result.Code != "read_unavailable" {
			t.Fatal("unbounded or invalid response accepted")
		}
	}
	if liveDiagnosticID("https://secret-host") != "unknown" || liveDiagnosticID("key_01h455vb4pex5vsknk084sn02q") != "unknown" || liveFailureAllowed("secret-error") {
		t.Fatal("unsafe diagnostic value accepted")
	}
}

func TestSharedCloudLiveLifecycleReadHTTPFailures(t *testing.T) {
	for _, test := range []struct {
		status int
		code   string
	}{{403, "read_denied"}, {404, "not_found"}, {503, "http_error"}, {302, "http_error"}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "https://secret-host")
			w.WriteHeader(test.status)
			_, _ = w.Write([]byte("secret-api-error"))
		}))
		result := liveReadLifecycle(t.Context(), &client{base: server.URL}, "deployment", "/deployment")
		server.Close()
		if result.Code != test.code || result.HTTPStatus != test.status {
			t.Fatalf("wrong HTTP evidence: %+v", result)
		}
	}
	if livePlanProblemCode("capacity-architecture") != "capacity-architecture" || livePlanProblemCode("secret-provider-code") != "unknown" {
		t.Fatal("unsafe planning code")
	}
}
