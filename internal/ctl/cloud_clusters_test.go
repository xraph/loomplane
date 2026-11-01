package ctl

import (
	"net/http"
	"strings"
	"testing"
)

func TestClusterCLIWaitTimeout(t *testing.T) {
	app, out := infraTestApp(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/clouds/cloud/cluster-operations/operation" {
			t.Errorf("unexpected route %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"operation","status":"waiting_provider","revision":4}`))
	})
	err := app.Run([]string{"cloud-clusters", "operation", "cloud", "operation", "--wait", "--timeout", "20ms"})
	if err == nil || !strings.Contains(out.String(), "waiting_provider") || !strings.Contains(out.String(), "operation") {
		t.Fatalf("lost checkpoint: %v %s", err, out)
	}
}

func TestClusterCLICapacityAndCleanupRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		method, path string
	}{{[]string{"cloud-clusters", "capacity", "cloud", "cluster"}, "GET", "/api/v1/clouds/cloud/clusters/cluster/capacity"}, {[]string{"cloud-clusters", "refresh-capacity", "cloud", "cluster"}, "POST", "/api/v1/clouds/cloud/clusters/cluster/capacity/refresh"}, {[]string{"cleanup", "cancel", "dep", "--yes", "--expected-review-id", "review"}, "POST", "/api/v1/deployments/dep/cleanup/cancel"}} {
		app, out := infraTestApp(t, "", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != tc.method || r.URL.Path != tc.path {
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"status":"cancelled","resource_id":"cluster"}`))
		})
		if err := app.Run(tc.args); err != nil || out.Len() == 0 {
			t.Fatalf("%v: %v %s", tc.args, err, out)
		}
	}
}
