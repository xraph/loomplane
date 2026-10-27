package azure

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
)

const (
	monitorVersion = "2023-10-01"
	costVersion    = "2023-11-01"
	// maxCostPages stops a run of nextLinks that never ends. A month of
	// daily rows fits on one page.
	maxCostPages = 20
)

// What to grant when Azure answers 403.
const (
	monitorReader = "Give the application the Monitoring Reader role so Loomplane can read requests and response time"
	costReader    = "Give the application the Cost Management Reader role on the resource group so Loomplane can read what it costs"
)

// AppMetrics reads a container app's requests, 5xx responses and average
// response time over w from Azure Monitor, across all its revisions.
// appID is the app's resource ID.
func (s *Session) AppMetrics(ctx context.Context, appID string, w metrics.Window) (*metrics.Measurement, error) {
	return s.RevisionMetrics(ctx, appID, "", w)
}

// RevisionMetrics reads one revision's requests, 5xx responses and average
// response time over w. An empty revision reads the whole app.
func (s *Session) RevisionMetrics(ctx context.Context, appID, revision string, w metrics.Window) (*metrics.Measurement, error) {
	span := w.Start.UTC().Format(time.RFC3339) + "/" + w.End.UTC().Format(time.RFC3339)
	var revFilter string
	if revision != "" {
		revFilter = "revisionName eq '" + strings.ReplaceAll(revision, "'", "''") + "'"
	}
	errFilter := "statusCodeCategory eq '5xx'"
	if revFilter != "" {
		errFilter = revFilter + " and " + errFilter
	}
	query := func(name, aggregation, interval, filter string) url.Values {
		q := url.Values{
			"metricnames": {name}, "aggregation": {aggregation},
			"interval": {interval}, "timespan": {span},
		}
		if filter != "" {
			q.Set("$filter", filter)
		}
		return q
	}
	requests, err := s.monitor(ctx, appID, query("Requests", "Total", timeGrain(w.Step), revFilter))
	if err != nil {
		return nil, err
	}
	failures, err := s.monitor(ctx, appID, query("Requests", "Total", timeGrain(w.Step), errFilter))
	if err != nil {
		return nil, err
	}
	// Azure answered for errors, so an empty series means none fell.
	m := &metrics.Measurement{ErrorsCounted: true}
	m.Requests = totals(requests)
	m.Errors = totals(failures)

	// FULL is one value for the whole span. Container Apps reports no
	// percentile, so this is an average and says so.
	latency, err := s.monitor(ctx, appID, query("ResponseTime", "Average", "FULL", revFilter))
	if err != nil {
		return nil, err
	}
	var sum float64
	var n int
	for _, d := range latency {
		if d.Average != nil {
			sum += *d.Average
			n++
		}
	}
	if n > 0 {
		// Should Azure split the span anyway, the mean of its pieces.
		m.Latency, m.Statistic = sum/float64(n), metrics.Average
	}
	return m, nil
}

// totals adds the series' totals up per timestamp, oldest first.
func totals(points []monitorPoint) []metrics.Point {
	sums := map[time.Time]float64{}
	for _, d := range points {
		if d.Total != nil {
			sums[d.TimeStamp.UTC()] += *d.Total
		}
	}
	out := make([]metrics.Point, 0, len(sums))
	for t, v := range sums {
		out = append(out, metrics.Point{T: t, V: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T.Before(out[j].T) })
	return out
}

// monitorPoint is one value of a metric in Azure Monitor's answer.
type monitorPoint struct {
	TimeStamp time.Time `json:"timeStamp"`
	Total     *float64  `json:"total"`
	Average   *float64  `json:"average"`
}

// monitor reads one metric of a resource, every series of it in one list.
func (s *Session) monitor(ctx context.Context, resource string, q url.Values) ([]monitorPoint, error) {
	var out struct {
		Value []struct {
			Timeseries []struct {
				Data []monitorPoint `json:"data"`
			} `json:"timeseries"`
		} `json:"value"`
	}
	path := resource + "/providers/Microsoft.Insights/metrics?" + q.Encode()
	if _, err := s.do(ctx, http.MethodGet, path, monitorVersion, nil, &out); err != nil {
		if isForbidden(err) {
			return nil, metrics.Unavailablef(monitorReader)
		}
		return nil, err
	}
	var points []monitorPoint
	for _, v := range out.Value {
		for _, ts := range v.Timeseries {
			points = append(points, ts.Data...)
		}
	}
	return points, nil
}

// grains are the time grains Azure Monitor accepts, largest first.
var grains = []time.Duration{24 * time.Hour, 12 * time.Hour, 6 * time.Hour, time.Hour, 30 * time.Minute, 15 * time.Minute, 5 * time.Minute, time.Minute}

// timeGrain is the largest grain Azure Monitor accepts that divides step,
// so the buckets it answers with add up into step's. It refuses others,
// such as PT3H.
func timeGrain(step time.Duration) string {
	for _, g := range grains {
		if step%g == 0 {
			return isoDuration(g)
		}
	}
	return isoDuration(time.Minute)
}

// isoDuration writes a whole number of minutes as an ISO 8601 duration,
// such as PT30M or P1D.
func isoDuration(d time.Duration) string {
	m := int64(d / time.Minute)
	days, m := m/(24*60), m%(24*60)
	hours, m := m/60, m%60
	var b strings.Builder
	b.WriteByte('P')
	if days > 0 {
		fmt.Fprintf(&b, "%dD", days)
	}
	if hours > 0 || m > 0 || days == 0 {
		b.WriteByte('T')
		if hours > 0 {
			fmt.Fprintf(&b, "%dH", hours)
		}
		if m > 0 || hours == 0 {
			fmt.Fprintf(&b, "%dM", m)
		}
	}
	return b.String()
}

// Spend reads what the resource group has cost this month from Cost
// Management.
func (s *Session) Spend(ctx context.Context, now time.Time) (*metrics.Spend, error) {
	start, _ := metrics.CalendarMonth(now)
	body := map[string]any{
		"type": "ActualCost", "timeframe": "Custom",
		"timePeriod": map[string]any{"from": start.Format(time.RFC3339), "to": now.UTC().Format(time.RFC3339)},
		"dataset": map[string]any{
			"granularity": "Daily",
			"aggregation": map[string]any{"totalCost": map[string]any{"name": "Cost", "function": "Sum"}},
		},
	}
	sp := &metrics.Spend{}
	days := map[time.Time]float64{}
	path, version := s.group()+"/providers/Microsoft.CostManagement/query", costVersion
	for page := 0; path != ""; page++ {
		if page == maxCostPages {
			return nil, fmt.Errorf("azure cost management kept paging after %d pages", maxCostPages)
		}
		var out struct {
			Properties struct {
				NextLink string `json:"nextLink"`
				Columns  []struct {
					Name string `json:"name"`
					Type string `json:"type"`
				} `json:"columns"`
				Rows [][]any `json:"rows"`
			} `json:"properties"`
		}
		if _, err := s.do(ctx, http.MethodPost, path, version, body, &out); err != nil {
			if isForbidden(err) {
				return nil, metrics.Unavailablef(costReader)
			}
			return nil, err
		}
		names := make([]string, len(out.Properties.Columns))
		types := make([]string, len(out.Properties.Columns))
		for i, c := range out.Properties.Columns {
			names[i], types[i] = c.Name, c.Type
		}
		cost, date, currency := costColumns(names, types)
		if len(out.Properties.Rows) > 0 && (cost < 0 || date < 0) {
			return nil, fmt.Errorf("azure cost management answered without a cost or a date column: %v", names)
		}
		for _, row := range out.Properties.Rows {
			if cost >= len(row) || date >= len(row) || currency >= len(row) {
				return nil, errors.New("azure cost management answered a row shorter than its columns")
			}
			v, ok := row[cost].(float64)
			if !ok {
				return nil, fmt.Errorf("azure cost management answered a cost of %v", row[cost])
			}
			day, err := usageDate(row[date])
			if err != nil {
				return nil, err
			}
			if currency >= 0 {
				var c string
				if s, ok := row[currency].(string); ok {
					c = s
				}
				switch {
				case sp.Currency == "":
					sp.Currency = c
				case c != "" && c != sp.Currency:
					return nil, fmt.Errorf("azure cost management answered in both %s and %s", sp.Currency, c)
				}
			}
			sp.Total += v
			days[day] += v
		}
		path, version = out.Properties.NextLink, ""
		// nextLink is absolute and the session's token goes with it, so
		// it may only lead back to Resource Manager.
		if path != "" && !strings.HasPrefix(path, s.c.management+"/") {
			return nil, fmt.Errorf("azure cost management pointed the next page outside Resource Manager, at %s", path)
		}
	}
	for t, v := range days {
		if v != 0 {
			sp.Days = append(sp.Days, metrics.Point{T: t, V: v})
		}
	}
	sort.Slice(sp.Days, func(i, j int) bool { return sp.Days[i].T.Before(sp.Days[j].T) })
	return sp, nil
}

// costColumns finds the cost, date and currency columns by name, -1 for
// one that isn't there. The cost is Cost, else PreTaxCost, else the first
// other number.
func costColumns(names, types []string) (cost, date, currency int) {
	date, currency = -1, -1
	for i, n := range names {
		switch n {
		case "UsageDate":
			date = i
		case "Currency":
			currency = i
		}
	}
	for _, want := range []string{"Cost", "PreTaxCost"} {
		if i := slices.Index(names, want); i >= 0 {
			return i, date, currency
		}
	}
	for i, t := range types {
		if strings.EqualFold(t, "Number") && i != date {
			return i, date, currency
		}
	}
	return -1, date, currency
}

// usageDate reads a UsageDate, a number such as 20261024, at UTC midnight.
func usageDate(v any) (time.Time, error) {
	var s string
	switch d := v.(type) {
	case float64:
		s = strconv.FormatFloat(d, 'f', 0, 64)
	case string:
		s = d
	}
	t, err := time.Parse("20060102", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("azure cost management answered a usage date of %v", v)
	}
	return t, nil
}
