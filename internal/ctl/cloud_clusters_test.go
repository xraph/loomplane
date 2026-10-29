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
