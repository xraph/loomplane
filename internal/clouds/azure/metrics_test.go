package azure

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
)

// monitorSession signs in to a fake that answers sign-in itself and hands
// every other request to h. It returns the fake's address too, which a
// nextLink points at.
func monitorSession(t *testing.T, h http.HandlerFunc) (*Session, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/"+tenant+"/oauth2/v2.0/token" {
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	cred, err := ParseCredential([]byte("right"), settings())
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(srv.URL).Session(t.Context(), cred)
	if err != nil {
		t.Fatal(err)
	}
	return s, srv.URL
}

func forbidden(w http.ResponseWriter) {
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":{"code":"AuthorizationFailed","message":"The client does not have authorization to perform action."}}`))
}

func TestISODuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Minute:                   "PT1M",
		30 * time.Minute:              "PT30M",
		3 * time.Hour:                 "PT3H",
		12 * time.Hour:                "PT12H",
		24 * time.Hour:                "P1D",
		90 * time.Minute:              "PT1H30M",
		25*time.Hour + time.Minute:    "P1DT1H1M",
		48 * time.Hour:                "P2D",
		24*time.Hour + 30*time.Minute: "P1DT30M",
	} {
		if got := isoDuration(d); got != want {
			t.Errorf("isoDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

// Azure Monitor refuses a grain it doesn't list, such as PT3H, so a step
// is read at the largest grain that divides it and added back up.
func TestTimeGrainIsOneAzureMonitorAccepts(t *testing.T) {
	for step, want := range map[time.Duration]string{
		30 * time.Minute: "PT30M",
		3 * time.Hour:    "PT1H",
		12 * time.Hour:   "PT12H",
		24 * time.Hour:   "P1D",
		7 * time.Minute:  "PT1M",
		10 * time.Minute: "PT5M",
	} {
		if got := timeGrain(step); got != want {
			t.Errorf("timeGrain(%v) = %q, want %q", step, got, want)
		}
	}
}

const appID = "/subscriptions/" + sub + "/resourceGroups/loomplane/providers/Microsoft.App/containerApps/api"

func TestAppMetrics(t *testing.T) {
	win := metrics.Day.Window(time.Date(2026, 10, 24, 13, 10, 0, 0, time.UTC))
	s, _ := monitorSession(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != appID+"/providers/Microsoft.Insights/metrics" ||
			q.Get("api-version") != "2023-10-01" || q.Get("timespan") != "2026-10-23T13:30:00Z/2026-10-24T13:30:00Z" {
			t.Errorf("asked %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch q.Get("metricnames") {
		case "Requests":
			if q.Get("aggregation") != "Total" || q.Get("interval") != "PT30M" {
				t.Errorf("requests asked %s", r.URL.RawQuery)
			}
			if q.Get("$filter") != "" {
				_, _ = w.Write([]byte(`{"value":[]}`))
				return
			}
			// Two series, a null, a missing total and an offset time.
			_, _ = w.Write([]byte(`{"value":[{"name":{"value":"Requests"},"timeseries":[
				{"data":[{"timeStamp":"2026-10-24T12:00:00Z","total":12},{"timeStamp":"2026-10-24T12:30:00Z","total":null},{"timeStamp":"2026-10-24T13:00:00Z"}]},
				{"data":[{"timeStamp":"2026-10-24T14:00:00+02:00","total":3},{"timeStamp":"2026-10-24T12:30:00Z","total":5}]}]}]}`))
		case "ResponseTime":
			if q.Get("aggregation") != "Average" || q.Get("interval") != "FULL" {
				t.Errorf("response time asked %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"value":[{"name":{"value":"ResponseTime"},"timeseries":[{"data":[{"timeStamp":"2026-10-23T13:30:00Z","average":84.5}]}]}]}`))
		default:
			t.Errorf("asked for %q", q.Get("metricnames"))
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	m, err := s.AppMetrics(t.Context(), appID, win)
	if err != nil {
		t.Fatal(err)
	}
	want := []metrics.Point{
		{T: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC), V: 15},
		{T: time.Date(2026, 10, 24, 12, 30, 0, 0, time.UTC), V: 5},
	}
	if !reflect.DeepEqual(m.Requests, want) {
		t.Errorf("requests %v, want %v", m.Requests, want)
	}
	if m.Latency != 84.5 || m.Statistic != metrics.Average {
		t.Errorf("latency %v %q, want 84.5 average", m.Latency, m.Statistic)
	}
}

// revisionFake records the $filter each metric was asked with, keyed by
// metric name and, for Requests, whether it was the error read.
func revisionFake(t *testing.T) (*Session, map[string]string) {
	t.Helper()
	filters := map[string]string{}
	s, _ := monitorSession(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		key := q.Get("metricnames")
		if strings.Contains(q.Get("$filter"), "statusCodeCategory") {
			key = "errors"
		}
		filters[key] = q.Get("$filter")
		switch key {
		case "errors":
			_, _ = w.Write([]byte(`{"value":[{"timeseries":[{"data":[{"timeStamp":"2026-10-24T12:00:00Z","total":2}]}]}]}`))
		case "Requests":
			_, _ = w.Write([]byte(`{"value":[{"timeseries":[{"data":[{"timeStamp":"2026-10-24T12:00:00Z","total":40}]}]}]}`))
		default:
			_, _ = w.Write([]byte(`{"value":[{"timeseries":[{"data":[{"timeStamp":"2026-10-24T12:00:00Z","average":10}]}]}]}`))
		}
	})
	return s, filters
}

func TestRevisionMetricsFiltersRequestsAndErrors(t *testing.T) {
	s, filters := revisionFake(t)
	win := metrics.Day.Window(time.Date(2026, 10, 24, 13, 10, 0, 0, time.UTC))
	m, err := s.RevisionMetrics(t.Context(), appID, "api--c1a2b3c4-0", win)
	if err != nil {
		t.Fatal(err)
	}
	rev := "revisionName eq 'api--c1a2b3c4-0'"
	want := map[string]string{
		"Requests":     rev,
		"errors":       rev + " and statusCodeCategory eq '5xx'",
		"ResponseTime": rev,
	}
	if !reflect.DeepEqual(filters, want) {
		t.Errorf("filters %v, want %v", filters, want)
	}
	if !m.ErrorsCounted || len(m.Errors) != 1 || m.Errors[0].V != 2 || len(m.Requests) != 1 || m.Requests[0].V != 40 {
		t.Errorf("%+v", m)
	}
	if m.Latency != 10 || m.Statistic != metrics.Average {
		t.Errorf("latency %v %q", m.Latency, m.Statistic)
	}
}

func TestAppMetricsHasNoFilter(t *testing.T) {
	s, filters := revisionFake(t)
	if _, err := s.AppMetrics(t.Context(), appID, metrics.Day.Window(time.Now())); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Requests": "", "errors": "statusCodeCategory eq '5xx'", "ResponseTime": ""}
	if !reflect.DeepEqual(filters, want) {
		t.Errorf("filters %v, want %v", filters, want)
	}
}

func TestRevisionMetricsUsesOneMinuteGrain(t *testing.T) {
	var got []string
	s, _ := monitorSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("metricnames") == "Requests" {
			got = append(got, r.URL.Query().Get("interval"))
		}
		_, _ = w.Write([]byte(`{"value":[]}`))
	})
	start := time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC)
	win := metrics.Window{Start: start, End: start.Add(10 * time.Minute), Step: time.Minute}
	if _, err := s.RevisionMetrics(t.Context(), appID, "api--c1a2b3c4-0", win); err != nil {
		t.Fatal(err)
	}
	// Requests and errors both read at the one-minute grain.
	if !reflect.DeepEqual(got, []string{"PT1M", "PT1M"}) {
		t.Errorf("intervals %v, want PT1M twice", got)
	}
}

func TestAppMetrics_nothingServed(t *testing.T) {
	s, _ := monitorSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("metricnames") == "Requests" {
			_, _ = w.Write([]byte(`{"value":[{"name":{"value":"Requests"},"timeseries":[]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":[{"name":{"value":"ResponseTime"},"timeseries":[{"data":[{"timeStamp":"2026-10-23T13:30:00Z","average":null}]}]}]}`))
	})
	m, err := s.AppMetrics(t.Context(), appID, metrics.Week.Window(time.Now()))
	if err != nil || len(m.Requests) != 0 || m.Latency != 0 || m.Statistic != "" {
		t.Fatalf("%+v, %v", m, err)
	}
}

func TestAppMetrics_forbiddenIsUnavailable(t *testing.T) {
	s, _ := monitorSession(t, func(w http.ResponseWriter, _ *http.Request) { forbidden(w) })
	_, err := s.AppMetrics(t.Context(), appID, metrics.Day.Window(time.Now()))
	if reason, ok := metrics.IsUnavailable(err); !ok || reason != "Give the application the Monitoring Reader role so Loomplane can read requests and response time" {
		t.Fatalf("%v", err)
	}
}

func TestSpend(t *testing.T) {
	now := time.Date(2026, 10, 24, 15, 30, 0, 0, time.FixedZone("CEST", 2*60*60))
	wantBody := map[string]any{
		"type": "ActualCost", "timeframe": "Custom",
		"timePeriod": map[string]any{"from": "2026-10-01T00:00:00Z", "to": "2026-10-24T13:30:00Z"},
		"dataset": map[string]any{
			"granularity": "Daily",
			"aggregation": map[string]any{"totalCost": map[string]any{"name": "Cost", "function": "Sum"}},
		},
	}
	path := "/subscriptions/" + sub + "/resourceGroups/loomplane/providers/Microsoft.CostManagement/query"
	var base string
	var pages []string
	s, base := monitorSession(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		q := r.URL.Query()
		if r.Method != http.MethodPost || r.URL.Path != path || !slices.Equal(q["api-version"], []string{"2023-11-01"}) || !reflect.DeepEqual(body, wantBody) {
			t.Errorf("asked %s %s with %v", r.Method, r.URL, body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		pages = append(pages, q.Get("$skiptoken"))
		if q.Get("$skiptoken") == "" {
			_, _ = w.Write([]byte(`{"properties":{"nextLink":"` + base + path + `?api-version=2023-11-01&$skiptoken=page2",
				"columns":[{"name":"Cost","type":"Number"},{"name":"UsageDate","type":"Number"},{"name":"Currency","type":"String"}],
				"rows":[[1.5,20261001,"USD"],[2.25,20261002,"USD"]]}}`))
			return
		}
		// The second page lists its columns in another order.
		_, _ = w.Write([]byte(`{"properties":{"nextLink":null,
			"columns":[{"name":"UsageDate","type":"Number"},{"name":"Currency","type":"String"},{"name":"Cost","type":"Number"}],
			"rows":[[20261002,"USD",3],[20261003,"USD",0]]}}`))
	})
	got, err := s.Spend(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	want := &metrics.Spend{Currency: "USD", Total: 6.75, Days: []metrics.Point{
		{T: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), V: 1.5},
		{T: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), V: 5.25},
	}}
	if !reflect.DeepEqual(got, want) || !slices.Equal(pages, []string{"", "page2"}) {
		t.Fatalf("%+v over pages %q, want %+v", got, pages, want)
	}
}

// costAnswer is a fake that answers every cost query with body.
func costAnswer(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }
}

func TestSpend_costColumn(t *testing.T) {
	for name, body := range map[string]string{
		"pre-tax cost": `{"properties":{"columns":[{"name":"UsageDate","type":"Number"},{"name":"PreTaxCost","type":"Number"},{"name":"Currency","type":"String"}],
			"rows":[[20261005,4.5,"EUR"]]}}`,
		"the first other number": `{"properties":{"columns":[{"name":"UsageDate","type":"Number"},{"name":"Currency","type":"String"},{"name":"CostUSD","type":"Number"}],
			"rows":[[20261005,"EUR",4.5]]}}`,
	} {
		s, _ := monitorSession(t, costAnswer(body))
		got, err := s.Spend(t.Context(), time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC))
		want := &metrics.Spend{Currency: "EUR", Total: 4.5, Days: []metrics.Point{{T: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), V: 4.5}}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
}

func TestSpend_nothingYet(t *testing.T) {
	s, _ := monitorSession(t, costAnswer(`{"properties":{"columns":[{"name":"Cost","type":"Number"},{"name":"UsageDate","type":"Number"},{"name":"Currency","type":"String"}],"rows":[]}}`))
	got, err := s.Spend(t.Context(), time.Now())
	if err != nil || got.Total != 0 || got.Days != nil {
		t.Fatalf("%+v, %v", got, err)
	}
}

func TestSpend_mixedCurrenciesIsAnError(t *testing.T) {
	s, _ := monitorSession(t, costAnswer(`{"properties":{"columns":[{"name":"Cost","type":"Number"},{"name":"UsageDate","type":"Number"},{"name":"Currency","type":"String"}],
		"rows":[[1,20261001,"USD"],[2,20261002,"EUR"]]}}`))
	got, err := s.Spend(t.Context(), time.Now())
	if _, unavailable := metrics.IsUnavailable(err); err == nil || unavailable {
		t.Fatalf("%+v, %v", got, err)
	}
}

func TestSpend_foreignNextLinkIsRefused(t *testing.T) {
	s, _ := monitorSession(t, costAnswer(`{"properties":{"nextLink":"https://evil.example/query?$skiptoken=x",
		"columns":[{"name":"Cost","type":"Number"},{"name":"UsageDate","type":"Number"},{"name":"Currency","type":"String"}],"rows":[[1,20261001,"USD"]]}}`))
	if got, err := s.Spend(t.Context(), time.Now()); err == nil {
		t.Fatalf("followed it: %+v", got)
	}
}

func TestSpend_forbiddenIsUnavailable(t *testing.T) {
	s, _ := monitorSession(t, func(w http.ResponseWriter, _ *http.Request) { forbidden(w) })
	_, err := s.Spend(t.Context(), time.Now())
	if reason, ok := metrics.IsUnavailable(err); !ok || reason != "Give the application the Cost Management Reader role on the resource group so Loomplane can read what it costs" {
		t.Fatalf("%v", err)
	}
}
