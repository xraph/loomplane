package fleet

import "time"

// MetricSnapshot is one observation captured within an hour, not an hour average.
type MetricSnapshot struct {
	InstanceID     string        `json:"instance_id"`
	Hour           time.Time     `json:"hour"`
	CapturedAt     time.Time     `json:"captured_at"`
	Generation     int64         `json:"generation"`
	OwnershipEpoch int64         `json:"ownership_epoch"`
	Semantics      string        `json:"semantics"`
	Summary        MetricSummary `json:"summary"`
}
type MetricHistoryPage struct {
	Items      []MetricSnapshot `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
}
