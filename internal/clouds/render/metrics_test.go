package render

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
)

// metricsFake answers Render's two metrics with requests and latency,
// after checking what each was asked.
func metricsFake(t *testing.T, check func(path string, q map[string][]string), requests, latency string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer rnd_good" {
			t.Errorf("asked %s with %q", r.Method, r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if check != nil {
			check(r.URL.Path, r.URL.Query())
		}
		switch r.URL.Path {
		case "/metrics/http-requests":
			_, _ = w.Write([]byte(requests))
		case "/metrics/http-latency":
			_, _ = w.Write([]byte(latency))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

var key = Key{Value: "rnd_good", OwnerID: "tea-1"}

func TestServiceMetrics(t *testing.T) {
	win := metrics.Day.Window(time.Date(2026, 10, 24, 13, 10, 0, 0, time.UTC))
	asked := map[string]bool{}
	check := func(path string, q map[string][]string) {
		asked[path] = true
		want := map[string][]string{
			"resource":  {"srv-1"},
			"startTime": {"2026-10-23T13:30:00Z"},
			"endTime":   {"2026-10-24T13:30:00Z"},
		}
		switch path {
		case "/metrics/http-requests":
			want["resolutionSeconds"] = []string{"1800"}
			if _, ok := q["aggregateBy"]; ok {
				want["aggregateBy"] = []string{"statusCode"}
			}
		case "/metrics/http-latency":
			want["resolutionSeconds"] = []string{"86400"}
			want["quantile"] = []string{"0.95"}
		}
		if !reflect.DeepEqual(q, want) {
			t.Errorf("%s asked %v, want %v", path, q, want)
		}
	}
	// Two series, by status code, sharing a timestamp.
	requests := `[
		{"labels":[{"field":"statusCode","value":"200"}],"values":[{"timestamp":"2026-10-24T12:00:00Z","value":12},{"timestamp":"2026-10-24T12:30:00Z","value":4}]},
		{"labels":[{"field":"statusCode","value":"500"}],"values":[{"timestamp":"2026-10-24T14:00:00+02:00","value":3}]}]`
	latency := `[{"labels":[{"field":"quantile","value":"0.95"}],"values":[{"timestamp":"2026-10-23T13:30:00Z","value":212.5}]}]`
	m, err := metricsFake(t, check, requests, latency).ServiceMetrics(t.Context(), key, "srv-1", win)
	if err != nil {
		t.Fatal(err)
	}
	want := []metrics.Point{
		{T: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC), V: 15},
		{T: time.Date(2026, 10, 24, 12, 30, 0, 0, time.UTC), V: 4},
	}
	if !reflect.DeepEqual(m.Requests, want) {
		t.Errorf("requests %v, want %v", m.Requests, want)
	}
	if m.Latency != 212.5 || m.Statistic != metrics.P95 {
		t.Errorf("latency %v %q, want 212.5 p95", m.Latency, m.Statistic)
	}
	if !asked["/metrics/http-requests"] || !asked["/metrics/http-latency"] {
		t.Errorf("asked %v", asked)
	}
}

func TestServiceMetricsCountsFiveHundreds(t *testing.T) {
	var aggregated int
	check := func(path string, q map[string][]string) {
		if path == "/metrics/http-requests" && reflect.DeepEqual(q["aggregateBy"], []string{"statusCode"}) {
			aggregated++
		}
	}
	requests := `[
		{"labels":[{"field":"statusCode","value":"200"}],"values":[{"timestamp":"2026-10-24T12:00:00Z","value":30}]},
		{"labels":[{"field":"statusCode","value":"404"}],"values":[{"timestamp":"2026-10-24T12:00:00Z","value":7}]},
		{"labels":[{"field":"statusCode","value":"500"}],"values":[{"timestamp":"2026-10-24T12:00:00Z","value":2},{"timestamp":"2026-10-24T12:30:00Z","value":1}]},
		{"labels":[{"field":"statusCode","value":"503"}],"values":[{"timestamp":"2026-10-24T12:00:00Z","value":4}]}]`
	m, err := metricsFake(t, check, requests, `[]`).ServiceMetrics(t.Context(), key, "srv-1", metrics.Day.Window(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	want := []metrics.Point{
		{T: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC), V: 6},
		{T: time.Date(2026, 10, 24, 12, 30, 0, 0, time.UTC), V: 1},
	}
	if aggregated != 1 || !m.ErrorsCounted || !reflect.DeepEqual(m.Errors, want) {
		t.Errorf("aggregated %d, counted %v, errors %v, want %v", aggregated, m.ErrorsCounted, m.Errors, want)
	}
}

func TestServiceMetrics_refusedStatusReadKeepsRequestsAndLatency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/metrics/http-requests" && r.URL.Query().Get("aggregateBy") != "":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"bad aggregateBy"}`))
		case r.URL.Path == "/metrics/http-requests":
			_, _ = w.Write([]byte(`[{"labels":[],"values":[{"timestamp":"2026-10-24T12:00:00Z","value":9}]}]`))
		default:
			_, _ = w.Write([]byte(`[{"labels":[],"values":[{"timestamp":"2026-10-23T13:30:00Z","value":150}]}]`))
		}
	}))
	defer srv.Close()
	m, err := New(srv.URL).ServiceMetrics(t.Context(), key, "srv-1", metrics.Day.Window(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if m.ErrorsCounted || len(m.Errors) != 0 || len(m.Requests) != 1 || m.Requests[0].V != 9 || m.Latency != 150 || m.Statistic != metrics.P95 {
		t.Fatalf("%+v", m)
	}
}

func TestServiceMetrics_severalLatencyBucketsTakeTheLargest(t *testing.T) {
	latency := `[{"labels":[],"values":[{"timestamp":"2026-10-23T13:30:00Z","value":180},{"timestamp":"2026-10-24T01:30:00Z","value":340},{"timestamp":"2026-10-24T07:30:00Z","value":95}]}]`
	m, err := metricsFake(t, nil, `[]`, latency).ServiceMetrics(t.Context(), key, "srv-1", metrics.Day.Window(time.Now()))
	if err != nil || m.Latency != 340 || m.Statistic != metrics.P95 {
		t.Fatalf("%+v, %v", m, err)
	}
}

func TestServiceMetrics_nothingServed(t *testing.T) {
	m, err := metricsFake(t, nil, `[]`, `[{"labels":[],"values":[]}]`).ServiceMetrics(t.Context(), key, "srv-1", metrics.Week.Window(time.Now()))
	if err != nil || len(m.Requests) != 0 || m.Latency != 0 || m.Statistic != "" {
		t.Fatalf("%+v, %v", m, err)
	}
}

func TestServiceMetrics_refusedKeyIsUnavailable(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
		}))
		_, err := New(srv.URL).ServiceMetrics(t.Context(), key, "srv-1", metrics.Day.Window(time.Now()))
		srv.Close()
		if reason, ok := metrics.IsUnavailable(err); !ok || reason != "Render refused the API key for metrics; replace the key on this connection" {
			t.Errorf("%d: %v", status, err)
		}
	}
}
