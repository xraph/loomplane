package render

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
)

// refusedForMetrics is what to do when Render refuses the key.
const refusedForMetrics = "Render refused the API key for metrics; replace the key on this connection"

// series is one labelled series in Render's metrics answer.
type series struct {
	Labels []struct {
		Field string `json:"field"`
		Value string `json:"value"`
	} `json:"labels"`
	Values []struct {
		Timestamp time.Time `json:"timestamp"`
		Value     float64   `json:"value"`
	} `json:"values"`
}

// ServiceMetrics reads a service's requests, 5xx counts and p95 latency over w from
// Render's metrics API. id is the service's ID.
func (c *Client) ServiceMetrics(ctx context.Context, key Key, id string, w metrics.Window) (*metrics.Measurement, error) {
	query := func(resolution time.Duration) url.Values {
		return url.Values{
			"resource":          {id},
			"startTime":         {w.Start.UTC().Format(time.RFC3339)},
			"endTime":           {w.End.UTC().Format(time.RFC3339)},
			"resolutionSeconds": {strconv.FormatInt(int64(resolution/time.Second), 10)},
		}
	}
	var requests []series
	if err := c.metric(ctx, key, "/metrics/http-requests", query(w.Step), &requests); err != nil {
		return nil, err
	}
	// The same metric by status code gives the 5xx counts.
	byStatus := query(w.Step)
	byStatus.Set("aggregateBy", "statusCode")
	var statuses []series
	if err := c.metric(ctx, key, "/metrics/http-requests", byStatus, &statuses); err != nil {
		return nil, err
	}
	var failed []series
	for _, s := range statuses {
		for _, l := range s.Labels {
			if l.Field == "statusCode" && strings.HasPrefix(l.Value, "5") {
				failed = append(failed, s)
			}
		}
	}
	// Render answered for status codes, so an empty series means none fell.
	m := &metrics.Measurement{ErrorsCounted: true}
	m.Requests = sumSeries(requests)
	m.Errors = sumSeries(failed)

	// One bucket the width of the window makes Render's p95 the window's.
	q := query(w.End.Sub(w.Start))
	q.Set("quantile", "0.95")
	var latency []series
	if err := c.metric(ctx, key, "/metrics/http-latency", q, &latency); err != nil {
		return nil, err
	}
	for _, s := range latency {
		for _, v := range s.Values {
			// Should Render split the window anyway, the largest p95 is
			// the honest upper bound: percentiles can't be averaged.
			if m.Statistic == "" || v.Value > m.Latency {
				m.Latency, m.Statistic = v.Value, metrics.P95
			}
		}
	}
	return m, nil
}

// sumSeries adds the series up per timestamp, oldest first.
func sumSeries(all []series) []metrics.Point {
	sums := map[time.Time]float64{}
	for _, s := range all {
		for _, v := range s.Values {
			sums[v.Timestamp.UTC()] += v.Value
		}
	}
	out := make([]metrics.Point, 0, len(sums))
	for t, v := range sums {
		out = append(out, metrics.Point{T: t, V: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T.Before(out[j].T) })
	return out
}

// metric reads one of Render's metrics into out.
func (c *Client) metric(ctx context.Context, key Key, path string, q url.Values, out *[]series) error {
	err := c.do(ctx, key, http.MethodGet, path, q, nil, out)
	if errors.Is(err, ErrRejected) {
		return metrics.Unavailablef(refusedForMetrics)
	}
	return err
}
