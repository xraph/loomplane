package ctl

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCloudCommandsReviewExactRevisionAndKeepRequestIdentity(t *testing.T) {
	var out bytes.Buffer
	applied := 0
	var accepted map[string]any
	var reviewed map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/projects":
			_, _ = w.Write([]byte(`{"projects":[{"id":"p","name":"App","slug":"app"}]}`))
		case "/api/v1/projects/p/cloud-changes/preview":
			if err := json.NewDecoder(r.Body).Decode(&reviewed); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{"id":"preview","revision":"reviewed-digest","input":{"revision":7,"cloud_id":"cloud","target_id":"target"},"environments":[{"environment_id":"production","summary":{"replace":1}}],"blockers":[]}`))
		case "/api/v1/projects/p/cloud-changes/apply":
			if !strings.Contains(out.String(), "reviewed-digest") || !strings.Contains(out.String(), "replace") {
				t.Error("executed before displaying exact review")
			}
			applied++
			if err := json.NewDecoder(r.Body).Decode(&accepted); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"sync","status":"pending"}`))
		case "/api/v1/projects/p/cloud-syncs/sync":
			_, _ = w.Write([]byte(`{"id":"sync","status":"succeeded"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	app := New(Options{Out: &out, ConfigPath: t.TempDir() + "/none", Getenv: func(key string) string {
		if key == EnvURL {
			return srv.URL
		}
		return "lp_test"
	}})
	args := make([]string, 0, 18)
	args = append(args, []string{"cloud-targets", "switch", "app", "cloud", "target", "--provider", "vercel", "--connection", "new", "--revision", "7", "--request-id", "stable", "--json"}...)
	if err := app.Run(args); err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatal("preview executed work")
	}
	out.Reset()
	if err := app.Run(append(args, "--yes", "--wait", "--timeout", "100ms")); err != nil {
		t.Fatal(err)
	}
	if applied != 1 || accepted["preview_id"] != "preview" || accepted["revision"] != "reviewed-digest" || accepted["idempotency_key"] != "stable" {
		t.Fatalf("acceptance %#v", accepted)
	}
	if reviewed["revision"] != float64(7) || reviewed["connection_id"] != "new" || reviewed["cloud_id"] != "cloud" || reviewed["target_id"] != "target" {
		t.Fatalf("review %#v", reviewed)
	}
	if !strings.Contains(out.String(), "succeeded") {
		t.Fatalf("wait result %s", out.String())
	}
}
func TestCloudCommandsBoundWaitsAndSurfaceActionableCheckpoints(t *testing.T) {
	for _, status := range []string{"pending", "failed", "routing_pending", "cleanup_pending", "reverted"} {
		t.Run(status, func(t *testing.T) {
			var out bytes.Buffer
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/projects" {
					_, _ = w.Write([]byte(`{"projects":[{"id":"p","slug":"app"}]}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"id": "sync", "status": status, "error": "operator action required"})
			}))
			defer srv.Close()
			app := New(Options{Out: &out, ConfigPath: t.TempDir() + "/none", Getenv: func(k string) string {
				if k == EnvURL {
					return srv.URL
				}
				return "lp_test"
			}})
			started := time.Now()
			// Leave time for the local HTTP request under concurrent test load.
			// The one-second bound below still detects unbounded polling.
			err := app.Run([]string{"cloud-syncs", "wait", "app", "sync", "--timeout", "250ms", "--json"})
			if status == "reverted" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("checkpoint reported success")
			}
			if time.Since(started) > time.Second {
				t.Fatal("wait exceeded its bound")
			}
			if !strings.Contains(out.String(), status) {
				t.Fatalf("lost persisted record: %s", out.String())
			}
		})
	}
}
func TestCloudCommandsRejectInvalidTimeoutBeforeMutation(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()
	app := New(Options{Out: &bytes.Buffer{}, ConfigPath: t.TempDir() + "/none", Getenv: func(k string) string {
		if k == EnvURL {
			return srv.URL
		}
		return "lp_test"
	}})
	for _, args := range [][]string{{"cloud-syncs", "retry", "app", "sync", "--wait", "--timeout", "0s"}, {"cloud-syncs", "wait", "app", "sync", "--timeout", "-1s"}, {"cloud-targets", "switch", "app", "cloud", "target", "--yes", "--revision", "1", "--wait", "--timeout", "0s"}} {
		if err := app.Run(args); err == nil {
			t.Fatal("accepted invalid timeout")
		}
	}
	if called {
		t.Fatal("contacted API before timeout validation")
	}
}
func TestCloudAssignmentRevertAndRoutingRequestContracts(t *testing.T) {
	var path string
	var body map[string]any
	var out bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/projects" {
			_, _ = w.Write([]byte(`{"projects":[{"id":"p","slug":"app"}]}`))
			return
		}
		path = r.URL.Path
		if r.Body != nil && r.ContentLength != 0 {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		switch {
		case strings.HasSuffix(path, "/preview"):
			_, _ = w.Write([]byte(`{"id":"review","revision":"digest","reverts":"original","blockers":[]}`))
		case strings.HasSuffix(path, "/apply"), strings.HasSuffix(path, "/routing"):
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"compensation","status":"pending"}`))
		default:
			_, _ = w.Write([]byte(`{"assignment":null,"revision":2}`))
		}
	}))
	defer srv.Close()
	app := New(Options{Out: &out, ConfigPath: t.TempDir() + "/none", Getenv: func(k string) string {
		if k == EnvURL {
			return srv.URL
		}
		return "lp_test"
	}})
	if err := app.Run([]string{"clouds", "assign", "app", "env", "--cloud", "cloud", "--revision", "2"}); err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/projects/p/environments/env/cloud/preview" || body["revision"] != float64(2) || body["cloud_id"] != "cloud" {
		t.Fatalf("assignment %s %#v", path, body)
	}
	if err := app.Run([]string{"cloud-syncs", "revert", "app", "original", "--yes", "--request-id", "compensate"}); err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/projects/p/cloud-syncs/revert/apply" || body["idempotency_key"] != "compensate" {
		t.Fatalf("revert %s %#v", path, body)
	}
	if err := app.Run([]string{"cloud-syncs", "routing", "app", "sync", "--environment", "env", "--evidence", "DNS verified"}); err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/projects/p/cloud-syncs/sync/routing" || body["environment_id"] != "env" || body["evidence"] != "DNS verified" {
		t.Fatalf("routing %s %#v", path, body)
	}
}
