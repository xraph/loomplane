package releasehealth

import (
	"context"
	"time"
)

// Window is a span of time to read health for.
type Window struct{ Start, End time.Time }

// Health is what a cloud's monitoring reports for a service or revision over a window.
type Health struct {
	Requests  float64 `json:"requests"`
	Errors    float64 `json:"errors"` // 5xx count
	LatencyMS float64 `json:"latency_ms"`
	Statistic string  `json:"statistic,omitempty"` // "p95" or "average"
	Available bool    `json:"available"`
	Reason    string  `json:"reason,omitempty"` // why not available
}

// ErrorRate is Errors/Requests, or 0 when there were no requests.
func (h Health) ErrorRate() float64 {
	if h.Requests <= 0 {
		return 0
	}
	return h.Errors / h.Requests
}

// Thresholds say how much worse than the baseline a candidate may be. A zero
// threshold disables its check.
type Thresholds struct {
	ErrorRate       float64 `json:"error_rate"`        // fraction: 0.01 is 1%
	ErrorRateDelta  float64 `json:"error_rate_delta"`  // fraction
	P95LatencyRatio float64 `json:"p95_latency_ratio"` // candidate latency over baseline latency
	MinRequests     float64 `json:"min_requests"`      // below it a reading is inconclusive; 0 means 1
}

// DefaultThresholds are used when a plan sets none.
func DefaultThresholds() Thresholds {
	return Thresholds{ErrorRate: 0.01, ErrorRateDelta: 0.005, P95LatencyRatio: 1.3, MinRequests: 200}
}

// Verdict is the outcome of judging one window.
type Verdict string

const (
	Pass         Verdict = "pass"
	Fail         Verdict = "fail"
	Inconclusive Verdict = "inconclusive"
)

// Reader reads health from a cloud's own monitoring. revision "" reads the
// whole service; otherwise it is an opaque per-provider revision: Cloud Run and
// Azure revision names, ECS target group ARNs. Unavailable metrics come back as
// Health{Available: false, Reason}, not as an error.
type Reader interface {
	Health(ctx context.Context, orgID, bindingID, revision string, w Window) (Health, error)
}

const (
	MetricsLag      = 3 * time.Minute // a window is judged no sooner than this after it ends
	MaxInconclusive = 3               // extra holds before an inconclusive step fails safe
)
