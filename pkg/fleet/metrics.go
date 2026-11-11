package fleet

import "time"

// MetricWindow bounds one observation query to at most 24 hours.
type MetricWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// LatencyHistogram uses disjoint buckets. The final count is above the last bound.
type LatencyHistogram struct {
	UpperBoundsMillis []float64 `json:"upper_bounds_millis"`
	Counts            []uint64  `json:"counts"`
}

// MetricCoverage counts expected components separately from available observations.
type MetricCoverage struct {
	Requests int `json:"requests"`
	Expected int `json:"expected"`
	Observed int `json:"observed"`
	Fresh    int `json:"fresh"`
	CPU      int `json:"cpu"`
	Memory   int `json:"memory"`
	Latency  int `json:"latency"`
	Replicas int `json:"replicas"`
}

// MetricSummary retains unknown values as null. CPUUtilization is a ratio, not a percentage.
type MetricSummary struct {
	Requests                *float64                  `json:"requests"`
	Sources                 []MetricSourceObservation `json:"sources"`
	CostStatus              string                    `json:"cost_status"`
	EstimatedHourlyMicroUSD *int64                    `json:"estimated_hourly_micro_usd"`
	ObservedCostMicroUSD    *int64                    `json:"observed_cost_micro_usd"`
	Window                  MetricWindow              `json:"window"`
	AsOf                    time.Time                 `json:"as_of"`
	OldestObservation       *time.Time                `json:"oldest_observation"`
	Freshness               string                    `json:"freshness"`
	Partial                 bool                      `json:"partial"`
	Coverage                MetricCoverage            `json:"coverage"`
	CPUUsageMillicores      *float64                  `json:"cpu_usage_millicores"`
	CPURequestMillicores    *float64                  `json:"cpu_request_millicores"`
	CPUUtilization          *float64                  `json:"cpu_utilization"`
	MemoryBytes             *float64                  `json:"memory_bytes"`
	DesiredReplicas         *int64                    `json:"desired_replicas"`
	CurrentReplicas         *int64                    `json:"current_replicas"`
	ReadyReplicas           *int64                    `json:"ready_replicas"`
	LatencyHistogram        *LatencyHistogram         `json:"latency_histogram"`
	// LatencyP95Millis is the upper bound of the histogram bucket containing p95.
	LatencyP95Millis *float64 `json:"latency_p95_millis"`
	Reason           string   `json:"reason,omitempty"`
}

// MetricSourceObservation retains each component's provider and observation time.
type MetricSourceObservation struct {
	RuntimeObservedAt  *time.Time `json:"runtime_observed_at"`
	ReplicasObservedAt *time.Time `json:"replicas_observed_at"`
	RuntimeIdentity    string     `json:"runtime_identity,omitempty"`
	RuntimeLive        bool       `json:"runtime_live"`
	Component          string     `json:"component"`
	Source             string     `json:"source"`
	ObservedAt         *time.Time `json:"observed_at"`
	Reason             string     `json:"reason,omitempty"`
}
