package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/fleet"
)

func TestFleetMonitoringClient(t *testing.T) {
	window := fleet.MetricWindow{Start: time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC), End: time.Date(2026, 1, 1, 1, 0, 0, 123456789, time.UTC)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Errorf("unexpected request %s", r.Method)
		}
		if !strings.HasPrefix(r.URL.EscapedPath(), "/api/v1/projects/project%2Fid/fleet/instances/instance%2Fid/") {
			t.Errorf("unescaped scope %s", r.URL.EscapedPath())
		}
		w.Header().Set("Content-Type", "application/json")
		var body string
		switch {
		case strings.HasSuffix(r.URL.Path, "/events"):
			if r.URL.Query().Get("after") != "cursor+&" || r.URL.Query().Get("limit") != "17" {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			body = `{"items":[{"id":"event","generation":3,"kind":"lifecycle","lifecycle":{"state":"ready"}}],"next_cursor":"next","replay_truncated":true}`
		case strings.HasSuffix(r.URL.Path, "/metrics"):
			if r.URL.Query().Get("start") != window.Start.Format(time.RFC3339Nano) || r.URL.Query().Get("end") != window.End.Format(time.RFC3339Nano) {
				t.Errorf("window %s", r.URL.RawQuery)
			}
			body = `{"freshness":"unknown","partial":true,"current_replicas":null,"cpu_utilization":null,"cost_status":"unknown"}`
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	c := New(server.URL, "synthetic")
	page, err := c.ListFleetEvents(t.Context(), "project/id", "instance/id", "cursor+&", 17)
	if err != nil || len(page.Items) != 1 || page.Items[0].Generation != 3 || page.NextCursor != "next" || !page.ReplayTruncated {
		t.Fatalf("events=%+v err=%v", page, err)
	}
	summary, err := c.GetFleetMetrics(t.Context(), "project/id", "instance/id", window)
	if err != nil || summary.Freshness != "unknown" || summary.CurrentReplicas != nil || summary.CPUUtilization != nil {
		t.Fatalf("metrics=%+v err=%v", summary, err)
	}
}

func TestFleetMonitoringClientDefaultServerWindow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("start") || r.URL.Query().Has("end") {
			t.Errorf("default must use server clock, got %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"freshness":"unknown"}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	if _, err := New(server.URL, "synthetic").GetFleetMetrics(t.Context(), "project", "instance", fleet.MetricWindow{}); err != nil {
		t.Fatal(err)
	}
}
