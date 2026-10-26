package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
)

func metricsSession(srv *httptest.Server) *Session {
	return &Session{c: New(srv.URL), key: &Key{ProjectID: "project-test"}, http: srv.Client()}
}

var metricsWindow = metrics.Window{
	Start: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC),
	End:   time.Date(2026, 10, 24, 1, 30, 0, 0, time.UTC),
	Step:  30 * time.Minute,
}

func TestServiceMetricsReadsRequestsAndP95(t *testing.T) {
	var requests, latency []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v3/projects/project-test/timeSeries" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		switch {
		case strings.HasSuffix(q.Get("filter"), `metric.type="run.googleapis.com/request_count"`):
			requests = append(requests, q)
			if q.Get("pageToken") == "" {
				w.Write([]byte(`{"timeSeries":[{"points":[
					{"interval":{"startTime":"2026-10-24T00:30:00Z","endTime":"2026-10-24T02:00:00+01:00"},"value":{"int64Value":"5"}},
					{"interval":{"startTime":"2026-10-24T00:00:00Z","endTime":"2026-10-24T00:30:00Z"},"value":{"int64Value":"3"}}
				]}],"nextPageToken":"next"}`))
				return
			}
			w.Write([]byte(`{"timeSeries":[{"points":[
				{"interval":{"startTime":"2026-10-24T01:00:00Z","endTime":"2026-10-24T01:30:00Z"},"value":{"doubleValue":2}}
			]}]}`))
		case strings.HasSuffix(q.Get("filter"), `metric.type="run.googleapis.com/request_latencies"`):
			latency = append(latency, q)
			w.Write([]byte(`{"timeSeries":[{"points":[{"interval":{"startTime":"2026-10-24T00:00:00Z","endTime":"2026-10-24T01:30:00Z"},"value":{"doubleValue":123.5}}]}]}`))
		default:
			t.Errorf("unexpected filter %q", q.Get("filter"))
		}
	}))
	defer srv.Close()

	m, err := metricsSession(srv).ServiceMetrics(context.Background(), "europe-west1", "api", metricsWindow)
	if err != nil {
		t.Fatal(err)
	}

	base := `resource.type="cloud_run_revision" AND resource.labels.service_name="api" AND resource.labels.location="europe-west1"`
	if len(requests) != 2 || requests[1].Get("pageToken") != "next" {
		t.Fatalf("paging was not followed: %v", requests)
	}
	for _, q := range requests {
		if q.Get("filter") != base+` AND metric.type="run.googleapis.com/request_count"` ||
			q.Get("interval.startTime") != "2026-10-24T00:00:00Z" || q.Get("interval.endTime") != "2026-10-24T01:30:00Z" ||
			q.Get("aggregation.alignmentPeriod") != "1800s" || q.Get("aggregation.perSeriesAligner") != "ALIGN_DELTA" ||
			q.Get("aggregation.crossSeriesReducer") != "REDUCE_SUM" {
			t.Errorf("requests query: %v", q)
		}
	}
	if len(latency) != 1 {
		t.Fatalf("latency calls: %d", len(latency))
	}
	if q := latency[0]; q.Get("filter") != base+` AND metric.type="run.googleapis.com/request_latencies"` ||
		q.Get("aggregation.alignmentPeriod") != "5400s" || q.Get("aggregation.perSeriesAligner") != "ALIGN_DELTA" ||
		q.Get("aggregation.crossSeriesReducer") != "REDUCE_PERCENTILE_95" {
		t.Errorf("latency query: %v", q)
	}

	want := []metrics.Point{
		{T: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC), V: 3},
		{T: time.Date(2026, 10, 24, 0, 30, 0, 0, time.UTC), V: 5},
		{T: time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC), V: 2},
	}
	if len(m.Requests) != len(want) {
		t.Fatalf("requests: %+v", m.Requests)
	}
	for i, p := range m.Requests {
		if !p.T.Equal(want[i].T) || p.T.Location() != time.UTC || p.V != want[i].V {
			t.Errorf("request %d: %+v, want %+v", i, p, want[i])
		}
	}
	if m.Latency != 123.5 || m.Statistic != metrics.P95 {
		t.Errorf("latency: %v %q", m.Latency, m.Statistic)
	}
}

func TestServiceMetricsWithoutLatencyData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	m, err := metricsSession(srv).ServiceMetrics(context.Background(), "europe-west1", "api", metricsWindow)
	if err != nil || len(m.Requests) != 0 || m.Latency != 0 || m.Statistic != "" {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestServiceMetricsForbiddenIsUnavailable(t *testing.T) {
	for _, metric := range []string{"request_count", "request_latencies"} {
		t.Run(metric, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Query().Get("filter"), metric) {
					w.WriteHeader(http.StatusForbidden)
					w.Write([]byte(`{"error":{"message":"Permission monitoring.timeSeries.list denied"}}`))
					return
				}
				w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			_, err := metricsSession(srv).ServiceMetrics(context.Background(), "europe-west1", "api", metricsWindow)
			reason, ok := metrics.IsUnavailable(err)
			if !ok || !strings.Contains(reason, "Monitoring Viewer") {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestServiceMetricsOtherErrorsAreNotUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, err := metricsSession(srv).ServiceMetrics(context.Background(), "europe-west1", "api", metricsWindow)
	if _, ok := metrics.IsUnavailable(err); ok || err == nil || !strings.Contains(err.Error(), "read requests") {
		t.Fatalf("%v", err)
	}
}

func TestCheckBillingTable(t *testing.T) {
	for _, table := range []string{"my-project.billing.gcp_billing_export_v1_0A1B2C", "proj01.d.t"} {
		if err := CheckBillingTable(table); err != nil {
			t.Errorf("%s: %v", table, err)
		}
	}
	for _, table := range []string{"", "a.b", "p.d.t;DROP", "my-project.billing.t;DROP TABLE x", "My-project.billing.t", "my-project.bill-ing.t", "my-project.billing.t.extra", "my-project.billing.`t`", "my-project.billing." + strings.Repeat("t", 1025)} {
		err := CheckBillingTable(table)
		if err == nil || !strings.Contains(err.Error(), "project.dataset.table") {
			t.Errorf("%q: %v", table, err)
		}
	}
}

type spendRequest struct {
	Query           string `json:"query"`
	UseLegacySQL    *bool  `json:"useLegacySql"`
	ParameterMode   string `json:"parameterMode"`
	TimeoutMs       int    `json:"timeoutMs"`
	QueryParameters []struct {
		Name          string `json:"name"`
		ParameterType struct {
			Type string `json:"type"`
		} `json:"parameterType"`
		ParameterValue struct {
			Value string `json:"value"`
		} `json:"parameterValue"`
	} `json:"queryParameters"`
}

func TestSpendPollsAndSumsDays(t *testing.T) {
	const table = "my-project.billing.gcp_billing_export_v1_0A1B2C"
	queries, polls, pages := 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/bigquery/v2/projects/project-test/queries":
			queries++
			var q spendRequest
			if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
				t.Error(err)
			}
			if !strings.Contains(q.Query, "FROM `"+table+"`") || !strings.Contains(q.Query, "usage_start_time >= @start AND usage_start_time < @end AND project.id = @project") ||
				!strings.Contains(q.Query, "UNNEST(credits)") || q.UseLegacySQL == nil || *q.UseLegacySQL || q.ParameterMode != "NAMED" || q.TimeoutMs != 20000 {
				t.Errorf("query: %+v", q)
			}
			params := map[string]string{}
			for _, p := range q.QueryParameters {
				params[p.Name+":"+p.ParameterType.Type] = p.ParameterValue.Value
			}
			if params["start:TIMESTAMP"] != "2026-10-01 00:00:00+00:00" || params["end:TIMESTAMP"] != "2026-11-01 00:00:00+00:00" || params["project:STRING"] != "project-test" {
				t.Errorf("parameters: %v", params)
			}
			w.Write([]byte(`{"jobComplete":false,"jobReference":{"projectId":"project-test","jobId":"job-1","location":"US"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/bigquery/v2/projects/project-test/queries/job-1":
			q := r.URL.Query()
			if q.Get("location") != "US" || q.Get("timeoutMs") != "20000" {
				t.Errorf("poll: %v", q)
			}
			if q.Get("pageToken") == "" {
				polls++
				if polls == 1 {
					w.Write([]byte(`{"jobComplete":false,"jobReference":{"jobId":"job-1","location":"US"}}`))
					return
				}
				w.Write([]byte(`{"jobComplete":true,"jobReference":{"jobId":"job-1","location":"US"},"rows":[
					{"f":[{"v":"2026-10-01"},{"v":"USD"},{"v":"12.5"}]},
					{"f":[{"v":"2026-10-02"},{"v":"USD"},{"v":"3.25"}]}
				],"pageToken":"page-2"}`))
				return
			}
			pages++
			if q.Get("pageToken") != "page-2" {
				t.Errorf("page token: %v", q)
			}
			w.Write([]byte(`{"jobComplete":true,"jobReference":{"jobId":"job-1","location":"US"},"rows":[
				{"f":[{"v":"2026-10-05"},{"v":"USD"},{"v":"-0.75"}]}
			]}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()

	spend, err := metricsSession(srv).Spend(context.Background(), table, time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 || polls != 2 || pages != 1 {
		t.Fatalf("queries=%d polls=%d pages=%d", queries, polls, pages)
	}
	if spend.Currency != "USD" || spend.Total != 15 || len(spend.Days) != 3 {
		t.Fatalf("%+v", spend)
	}
	want := []metrics.Point{
		{T: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), V: 12.5},
		{T: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), V: 3.25},
		{T: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), V: -0.75},
	}
	for i, p := range spend.Days {
		if !p.T.Equal(want[i].T) || p.T.Location() != time.UTC || p.V != want[i].V {
			t.Errorf("day %d: %+v, want %+v", i, p, want[i])
		}
	}
}

func TestSpendWithoutRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"jobComplete":true,"jobReference":{"jobId":"job-1","location":"US"}}`))
	}))
	defer srv.Close()
	spend, err := metricsSession(srv).Spend(context.Background(), "my-project.billing.export", time.Now())
	if err != nil || spend.Currency != "" || spend.Total != 0 || len(spend.Days) != 0 {
		t.Fatalf("%+v %v", spend, err)
	}
}

func TestSpendRefusesMixedCurrencies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"jobComplete":true,"jobReference":{"jobId":"job-1","location":"US"},"rows":[
			{"f":[{"v":"2026-10-01"},{"v":"USD"},{"v":"1"}]},
			{"f":[{"v":"2026-10-01"},{"v":"EUR"},{"v":"2"}]}
		]}`))
	}))
	defer srv.Close()
	_, err := metricsSession(srv).Spend(context.Background(), "my-project.billing.export", time.Now())
	if err == nil || !strings.Contains(err.Error(), "EUR") {
		t.Fatalf("%v", err)
	}
	if _, ok := metrics.IsUnavailable(err); ok {
		t.Fatalf("mixed currencies is not a permission: %v", err)
	}
}

func TestSpendUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name, table string
		status      int
		reason      string
	}{
		{"forbidden", "my-project.billing.export", http.StatusForbidden, "BigQuery Job User"},
		{"missing", "my-project.billing.export", http.StatusNotFound, "can't find the billing export table my-project.billing.export"},
		{"no table", "", 0, "billing export to BigQuery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				w.Write([]byte(`{"error":{"message":"denied"}}`))
			}))
			defer srv.Close()
			_, err := metricsSession(srv).Spend(context.Background(), tc.table, time.Now())
			reason, ok := metrics.IsUnavailable(err)
			if !ok || !strings.Contains(reason, tc.reason) {
				t.Fatalf("%v", err)
			}
			if tc.status == 0 && calls != 0 {
				t.Fatal("asked BigQuery without a table")
			}
		})
	}
}

func TestSpendChecksTableBeforeQuerying(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()
	_, err := metricsSession(srv).Spend(context.Background(), "p.d.t;DROP", time.Now())
	if err == nil || calls != 0 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
