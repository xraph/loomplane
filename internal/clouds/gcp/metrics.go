package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
)

// --- Cloud Monitoring ---

// timeSeriesPage is one page of Cloud Monitoring's timeSeries.list.
type timeSeriesPage struct {
	TimeSeries []struct {
		Points []struct {
			Interval struct {
				EndTime time.Time `json:"endTime"`
			} `json:"interval"`
			Value struct {
				// Int64Value is a string in JSON.
				Int64Value  *string  `json:"int64Value"`
				DoubleValue *float64 `json:"doubleValue"`
			} `json:"value"`
		} `json:"points"`
	} `json:"timeSeries"`
	NextPageToken string `json:"nextPageToken"`
}

// monitoringPoint is a point's end and value.
type monitoringPoint struct {
	end time.Time
	v   float64
}

// ServiceMetrics reads a Cloud Run service's requests, 5xx responses and
// p95 latency over w from Cloud Monitoring, across all its revisions.
func (s *Session) ServiceMetrics(ctx context.Context, region, service string, w metrics.Window) (*metrics.Measurement, error) {
	return s.RevisionMetrics(ctx, region, service, "", w)
}

// RevisionMetrics reads one revision's requests, 5xx responses and p95
// latency over w. An empty revision reads the whole service.
func (s *Session) RevisionMetrics(ctx context.Context, region, service, revision string, w metrics.Window) (*metrics.Measurement, error) {
	base := `resource.type="cloud_run_revision" AND resource.labels.service_name=` + strconv.Quote(service) + ` AND resource.labels.location=` + strconv.Quote(region)
	if revision != "" {
		base += ` AND resource.labels.revision_name=` + strconv.Quote(revision)
	}
	const requestCount = ` AND metric.type="run.googleapis.com/request_count"`
	counts, err := s.timeSeries(ctx, base+requestCount, w, w.Step, "REDUCE_SUM")
	if err != nil {
		return nil, monitoringError("read requests", err)
	}
	failures, err := s.timeSeries(ctx, base+requestCount+` AND metric.labels.response_code_class="5xx"`, w, w.Step, "REDUCE_SUM")
	if err != nil {
		return nil, monitoringError("read errors", err)
	}
	// A percentile reducer merges the revisions' distributions; averaging
	// each revision's percentile would not give the service's.
	latencies, err := s.timeSeries(ctx, base+` AND metric.type="run.googleapis.com/request_latencies"`, w, w.End.Sub(w.Start), "REDUCE_PERCENTILE_95")
	if err != nil {
		return nil, monitoringError("read latency", err)
	}
	m := &metrics.Measurement{ErrorsCounted: true}
	for _, p := range counts {
		m.Requests = append(m.Requests, metrics.Point{T: p.end.Add(-w.Step).UTC(), V: p.v})
	}
	for _, p := range failures {
		m.Errors = append(m.Errors, metrics.Point{T: p.end.Add(-w.Step).UTC(), V: p.v})
	}
	byTime := func(a, b metrics.Point) int { return a.T.Compare(b.T) }
	slices.SortFunc(m.Requests, byTime)
	slices.SortFunc(m.Errors, byTime)
	if len(latencies) > 0 {
		m.Latency, m.Statistic = latencies[0].v, metrics.P95
	}
	return m, nil
}

// timeSeries lists one metric's points, aligned to period and reduced
// across series, following every page.
func (s *Session) timeSeries(ctx context.Context, filter string, w metrics.Window, period time.Duration, reducer string) ([]monitoringPoint, error) {
	q := url.Values{
		"filter":                         {filter},
		"interval.startTime":             {w.Start.UTC().Format(time.RFC3339)},
		"interval.endTime":               {w.End.UTC().Format(time.RFC3339)},
		"aggregation.alignmentPeriod":    {strconv.FormatInt(int64(period/time.Second), 10) + "s"},
		"aggregation.perSeriesAligner":   {"ALIGN_DELTA"},
		"aggregation.crossSeriesReducer": {reducer},
	}
	path := "/v3/projects/" + url.PathEscape(s.key.ProjectID) + "/timeSeries?"
	var out []monitoringPoint
	seen := map[string]bool{}
	for {
		var page timeSeriesPage
		if err := s.do(ctx, http.MethodGet, s.c.url("monitoring.googleapis.com", path+q.Encode()), nil, &page); err != nil {
			return nil, err
		}
		for _, series := range page.TimeSeries {
			for _, p := range series.Points {
				v := 0.0
				switch {
				case p.Value.Int64Value != nil:
					n, err := strconv.ParseInt(*p.Value.Int64Value, 10, 64)
					if err != nil {
						return nil, fmt.Errorf("cloud monitoring sent a count of %q", *p.Value.Int64Value)
					}
					v = float64(n)
				case p.Value.DoubleValue != nil:
					v = *p.Value.DoubleValue
				}
				out = append(out, monitoringPoint{end: p.Interval.EndTime, v: v})
			}
		}
		if page.NextPageToken == "" {
			return out, nil
		}
		if seen[page.NextPageToken] {
			return nil, errors.New("cloud monitoring repeated a page token")
		}
		seen[page.NextPageToken] = true
		q.Set("pageToken", page.NextPageToken)
	}
}

// monitoringError explains a 403 and labels anything else.
func monitoringError(what string, err error) error {
	var api *APIError
	if errors.As(err, &api) && api.Status == http.StatusForbidden {
		return metrics.Unavailablef("Give the service account the Monitoring Viewer role so Loomplane can read requests and latency")
	}
	return fmt.Errorf("%s: %w", what, err)
}

// --- Cloud Billing export ---

// BillingExportSetting is the connection setting naming the billing
// export's table. It's optional: only spend is read from it.
const BillingExportSetting = "billing_export"

// billingTableRE is a BigQuery table's full name. Go's regexp caps a
// repeat at 1000, so dataset and table lengths are checked apart.
var billingTableRE = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]\.([A-Za-z0-9_]+)\.([A-Za-z0-9_]+)$`)

// CheckBillingTable checks a billing export table's name. The name goes
// into SQL, since BigQuery can't take a table as a parameter, so this is
// what keeps anything else out.
func CheckBillingTable(table string) error {
	m := billingTableRE.FindStringSubmatch(table)
	if len(m) < 3 || len(m[1]) > 1024 || len(m[2]) > 1024 {
		return fmt.Errorf("the billing export table is project.dataset.table, such as my-project.billing.gcp_billing_export_v1_0A1B2C, not %q", table)
	}
	return nil
}

// spendQuery is what each day of the month cost, credits taken off.
const spendQuery = "SELECT FORMAT_TIMESTAMP('%%Y-%%m-%%d', usage_start_time, 'UTC') AS day, currency, " +
	"SUM(cost) + SUM(IFNULL((SELECT SUM(c.amount) FROM UNNEST(credits) c), 0)) AS total " +
	"FROM `%s` WHERE usage_start_time >= @start AND usage_start_time < @end AND project.id = @project " +
	"GROUP BY day, currency ORDER BY day"

// queryResults is a page of BigQuery's jobs.query or getQueryResults.
type queryResults struct {
	JobComplete  bool `json:"jobComplete"`
	JobReference struct {
		JobID    string `json:"jobId"`
		Location string `json:"location"`
	} `json:"jobReference"`
	Rows []struct {
		F []struct {
			V *string `json:"v"`
		} `json:"f"`
	} `json:"rows"`
	PageToken string `json:"pageToken"`
}

// Spend reads what the session's project has cost this month from a
// Cloud Billing export to BigQuery. table is "project.dataset.table".
func (s *Session) Spend(ctx context.Context, table string, now time.Time) (*metrics.Spend, error) {
	if table == "" {
		return nil, metrics.Unavailablef("Google Cloud reports cost through a billing export to BigQuery. Add the export's table to this connection")
	}
	if err := CheckBillingTable(table); err != nil {
		return nil, err
	}
	start, end := metrics.CalendarMonth(now)
	const timestamp = "2006-01-02 15:04:05-07:00"
	param := func(name, kind, value string) map[string]any {
		return map[string]any{"name": name, "parameterType": map[string]string{"type": kind}, "parameterValue": map[string]string{"value": value}}
	}
	request := map[string]any{
		"query":         fmt.Sprintf(spendQuery, table),
		"useLegacySql":  false,
		"parameterMode": "NAMED",
		"queryParameters": []map[string]any{
			param("start", "TIMESTAMP", start.Format(timestamp)),
			param("end", "TIMESTAMP", end.Format(timestamp)),
			param("project", "STRING", s.key.ProjectID),
		},
		"timeoutMs": 20000,
	}
	project := "/bigquery/v2/projects/" + url.PathEscape(s.key.ProjectID) + "/queries"
	var page queryResults
	if err := s.do(ctx, http.MethodPost, s.c.url("bigquery.googleapis.com", project), request, &page); err != nil {
		return nil, billingError(table, err)
	}
	spend := &metrics.Spend{}
	seen := map[string]bool{}
	for {
		if page.JobComplete {
			if err := addRows(spend, page); err != nil {
				return nil, err
			}
			if page.PageToken == "" {
				return spend, nil
			}
			if seen[page.PageToken] {
				return nil, errors.New("bigquery repeated a page token")
			}
			seen[page.PageToken] = true
		} else if err := s.wait(ctx); err != nil {
			return nil, err
		}
		if page.JobReference.JobID == "" {
			return nil, errors.New("bigquery answered without a job to wait on")
		}
		q := url.Values{"location": {page.JobReference.Location}, "timeoutMs": {"20000"}}
		if page.JobComplete {
			q.Set("pageToken", page.PageToken)
		}
		job := page.JobReference
		page = queryResults{}
		if err := s.do(ctx, http.MethodGet, s.c.url("bigquery.googleapis.com", project+"/"+url.PathEscape(job.JobID)+"?"+q.Encode()), nil, &page); err != nil {
			return nil, billingError(table, err)
		}
		if page.JobReference.JobID == "" {
			page.JobReference = job
		}
	}
}

// addRows adds a page of (day, currency, total) rows to spend.
func addRows(spend *metrics.Spend, page queryResults) error {
	for _, row := range page.Rows {
		if len(row.F) != 3 || row.F[0].V == nil || row.F[1].V == nil {
			return errors.New("bigquery sent a billing row without a day or currency")
		}
		day, err := time.ParseInLocation("2006-01-02", *row.F[0].V, time.UTC)
		if err != nil {
			return fmt.Errorf("bigquery sent a billing day of %q", *row.F[0].V)
		}
		total := 0.0
		if row.F[2].V != nil {
			if total, err = strconv.ParseFloat(*row.F[2].V, 64); err != nil {
				return fmt.Errorf("bigquery sent a cost of %q", *row.F[2].V)
			}
		}
		currency := *row.F[1].V
		if spend.Currency != "" && spend.Currency != currency {
			return fmt.Errorf("the billing export has costs in both %s and %s, so Loomplane can't add them up", spend.Currency, currency)
		}
		spend.Currency = currency
		spend.Total += total
		spend.Days = append(spend.Days, metrics.Point{T: day, V: total})
	}
	return nil
}

// billingError explains a 403 or 404 and labels anything else.
func billingError(table string, err error) error {
	var api *APIError
	if errors.As(err, &api) {
		switch api.Status {
		case http.StatusForbidden:
			return metrics.Unavailablef("Give the service account BigQuery Job User on the project and BigQuery Data Viewer on the billing export's dataset")
		case http.StatusNotFound:
			return metrics.Unavailablef("Loomplane can't find the billing export table %s", table)
		}
	}
	return fmt.Errorf("read spend: %w", err)
}
