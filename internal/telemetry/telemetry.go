// Package telemetry is Loomplane's own metrics, beside Forge's and
// Dispatch's on /_/metrics: what deployments, plans, analyses, drift
// checks, API keys and cloud connections do, labelled by outcome and
// provider so a dashboard can say which cloud is failing.
//
// A nil *Recorder records nothing, so code that takes one never checks.
package telemetry

import (
	"time"

	"github.com/xraph/go-utils/metrics"
)

// Recorder records Loomplane's metrics.
type Recorder struct {
	m metrics.Metrics
}

// New returns a Recorder over Forge's metrics. m may be nil.
func New(m metrics.Metrics) *Recorder {
	if m == nil {
		return nil
	}
	return &Recorder{m: m}
}

// Seconds-scale buckets: a step that builds takes minutes.
var durations = metrics.WithBuckets(0.5, 1, 5, 15, 30, 60, 120, 300, 600, 1200, 3600)

func (r *Recorder) count(name string, labels map[string]string) {
	if r == nil {
		return
	}
	r.m.Counter(name, metrics.WithLabels(labels)).Inc()
}

func (r *Recorder) observe(name string, labels map[string]string, d time.Duration) {
	if r == nil {
		return
	}
	r.m.Histogram(name, metrics.WithLabels(labels), durations).Observe(d.Seconds())
}

// DeploymentFinished records a deployment that finished, succeeded or
// failed, and how long it took.
func (r *Recorder) DeploymentFinished(status string, took time.Duration) {
	r.count("loomplane_deployments_total", map[string]string{"status": status})
	r.observe("loomplane_deployment_duration_seconds", map[string]string{"status": status}, took)
}

// StepFinished records one action of a deployment.
func (r *Recorder) StepFinished(provider, op, status string, took time.Duration) {
	labels := map[string]string{"provider": provider, "op": op, "status": status}
	r.count("loomplane_deployment_steps_total", labels)
	r.observe("loomplane_deployment_step_duration_seconds", map[string]string{"provider": provider, "status": status}, took)
}

// Planned records a plan worked out: ready, blocked or failed.
func (r *Recorder) Planned(status string) {
	r.count("loomplane_plans_total", map[string]string{"status": status})
}

// Decided records a decision on a plan: approved, rejected, stale or
// expired.
func (r *Recorder) Decided(decision string) {
	r.count("loomplane_plan_decisions_total", map[string]string{"decision": decision})
}

// Drift records a binding found different from what's live: missing or
// changed.
func (r *Recorder) Drift(provider, state string) {
	r.count("loomplane_drift_findings_total", map[string]string{"provider": provider, "state": state})
}

// AnalysisFinished records a repository analysis.
func (r *Recorder) AnalysisFinished(status string, took time.Duration) {
	r.count("loomplane_analyses_total", map[string]string{"status": status})
	r.observe("loomplane_analysis_duration_seconds", map[string]string{"status": status}, took)
}

// KeyAuthenticated records a request with an API key: accepted or
// rejected. A run of rejections is someone guessing, or a revoked key
// still in a pipeline.
func (r *Recorder) KeyAuthenticated(accepted bool) {
	result := "accepted"
	if !accepted {
		result = "rejected"
	}
	r.count("loomplane_api_key_authentications_total", map[string]string{"result": result})
}

// ConnectionChecked records a cloud credential check: ready, incomplete,
// rejected or unavailable.
func (r *Recorder) ConnectionChecked(cloud, result string) {
	r.count("loomplane_connection_checks_total", map[string]string{"cloud": cloud, "result": result})
}
