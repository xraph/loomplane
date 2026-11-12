package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/xraph/loomplane/pkg/fleet"
)

// ListFleetEvents reads the journal after an opaque instance-bound cursor.
func (c *Client) ListFleetEvents(ctx context.Context, projectID, instanceID, after string, limit int) (*fleet.EventPage, error) {
	q := url.Values{}
	if after != "" {
		q.Set("after", after)
	}
	if limit != 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out fleet.EventPage
	err := c.do(ctx, http.MethodGet, fleetPath(projectID)+"/instances/"+url.PathEscape(instanceID)+"/events?"+q.Encode(), nil, &out)
	return &out, err
}

// GetFleetMetrics preserves unavailable signals as nil. A zero window uses the last 15 server minutes.
func (c *Client) GetFleetMetrics(ctx context.Context, projectID, instanceID string, window fleet.MetricWindow) (*fleet.MetricSummary, error) {
	q := url.Values{}
	if !window.Start.IsZero() || !window.End.IsZero() {
		q.Set("start", window.Start.Format(time.RFC3339Nano))
		q.Set("end", window.End.Format(time.RFC3339Nano))
	}
	var out fleet.MetricSummary
	err := c.do(ctx, http.MethodGet, fleetPath(projectID)+"/instances/"+url.PathEscape(instanceID)+"/metrics?"+q.Encode(), nil, &out)
	return &out, err
}
