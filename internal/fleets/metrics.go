package fleets

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

// MetricSample contains observations for one component in the requested window.
type MetricSample struct {
	RuntimeLive                                           bool
	RuntimeObservedAt, ReplicasObservedAt                 time.Time
	RuntimeIdentity                                       string
	Source, Reason                                        string
	Requests                                              *float64
	Component                                             string
	ObservedAt                                            time.Time
	CPUUsageMillicores, CPURequestMillicores, MemoryBytes *float64
	DesiredReplicas, CurrentReplicas, ReadyReplicas       *int64
	LatencyHistogram                                      *fleet.LatencyHistogram
	// A provider percentile cannot be aggregated across components.
	LatencyP95Millis *float64
}

// MetricBatch reports the expected component count even when sources fail.
type MetricBatch struct {
	Expected int
	Samples  []MetricSample
	Partial  bool
}

// AggregateMetrics combines fresh observations without treating gaps as zero.
func AggregateMetrics(w fleet.MetricWindow, now time.Time, b MetricBatch) (*fleet.MetricSummary, error) {
	if err := validateMetricWindow(w, now); err != nil {
		return nil, err
	}
	if b.Expected < 0 || b.Expected > 1000 || len(b.Samples) > b.Expected {
		return nil, ErrInvalid
	}
	out := &fleet.MetricSummary{Window: w, AsOf: now.UTC(), Freshness: "unknown", CostStatus: "unknown", Sources: []fleet.MetricSourceObservation{}, Partial: b.Partial, Coverage: fleet.MetricCoverage{Expected: b.Expected, Observed: len(b.Samples)}}
	seen := make(map[string]bool, len(b.Samples))
	histogramMismatch := false
	reference := now
	historical := now.Sub(w.End) > 5*time.Minute
	if historical {
		reference = w.End
	}
	for _, sample := range b.Samples {
		if sample.Component == "" || len(sample.Component) > 128 || seen[sample.Component] || !validMetricSample(sample) {
			return nil, ErrInvalid
		}
		seen[sample.Component] = true
		if !slices.Contains([]string{"", "pod_name_window", "empty_controller"}, sample.RuntimeIdentity) || len(sample.Source) > 64 || !slices.Contains([]string{"", "source_unavailable", "source_failed"}, sample.Reason) {
			return nil, ErrInvalid
		}
		source := fleet.MetricSourceObservation{Component: sample.Component, Source: sample.Source, Reason: sample.Reason, RuntimeIdentity: sample.RuntimeIdentity, RuntimeLive: sample.RuntimeLive}
		if !sample.RuntimeObservedAt.IsZero() {
			t := sample.RuntimeObservedAt.UTC()
			source.RuntimeObservedAt = &t
		}
		if !sample.ReplicasObservedAt.IsZero() {
			t := sample.ReplicasObservedAt.UTC()
			source.ReplicasObservedAt = &t
		}
		if !sample.ObservedAt.IsZero() {
			stamp := sample.ObservedAt
			source.ObservedAt = &stamp
		}
		out.Sources = append(out.Sources, source)
		if !sample.ObservedAt.IsZero() && (out.OldestObservation == nil || sample.ObservedAt.Before(*out.OldestObservation)) {
			t := sample.ObservedAt.UTC()
			out.OldestObservation = &t
		}
		fresh := func(stamp time.Time, live bool) bool {
			if stamp.IsZero() || stamp.After(now) || stamp.Before(w.Start) || reference.Sub(stamp) > 5*time.Minute {
				return false
			}
			if live {
				return !historical
			}
			return !stamp.After(w.End)
		}
		trafficFresh := fresh(sample.ObservedAt, false)
		runtimeAt, replicasAt := sample.RuntimeObservedAt, sample.ReplicasObservedAt
		if runtimeAt.IsZero() && !sample.RuntimeLive {
			runtimeAt = sample.ObservedAt
		}
		if replicasAt.IsZero() && !sample.RuntimeLive {
			replicasAt = sample.ObservedAt
		}
		runtimeFresh, replicasFresh := fresh(runtimeAt, sample.RuntimeLive), fresh(replicasAt, sample.RuntimeLive)
		for _, stamp := range []time.Time{runtimeAt, replicasAt} {
			if !stamp.IsZero() && (out.OldestObservation == nil || stamp.Before(*out.OldestObservation)) {
				t := stamp.UTC()
				out.OldestObservation = &t
			}
		}
		if trafficFresh || runtimeFresh || replicasFresh {
			out.Coverage.Fresh++
		}

		if trafficFresh && sample.Requests != nil {
			addMetric(&out.Requests, *sample.Requests)
			out.Coverage.Requests++
		}
		if runtimeFresh && sample.CPUUsageMillicores != nil && sample.CPURequestMillicores != nil {
			addMetric(&out.CPUUsageMillicores, *sample.CPUUsageMillicores)
			addMetric(&out.CPURequestMillicores, *sample.CPURequestMillicores)
			out.Coverage.CPU++
		}
		if runtimeFresh && sample.MemoryBytes != nil {
			addMetric(&out.MemoryBytes, *sample.MemoryBytes)
			out.Coverage.Memory++
		}
		if replicasFresh && sample.DesiredReplicas != nil && sample.CurrentReplicas != nil && sample.ReadyReplicas != nil {
			if !addReplica(&out.DesiredReplicas, *sample.DesiredReplicas) || !addReplica(&out.CurrentReplicas, *sample.CurrentReplicas) || !addReplica(&out.ReadyReplicas, *sample.ReadyReplicas) {
				return nil, ErrInvalid
			}
			out.Coverage.Replicas++
		}
		if trafficFresh && sample.LatencyHistogram != nil {
			if out.LatencyHistogram == nil {
				out.LatencyHistogram = &fleet.LatencyHistogram{UpperBoundsMillis: slices.Clone(sample.LatencyHistogram.UpperBoundsMillis), Counts: make([]uint64, len(sample.LatencyHistogram.Counts))}
			}
			if !slices.Equal(out.LatencyHistogram.UpperBoundsMillis, sample.LatencyHistogram.UpperBoundsMillis) {
				histogramMismatch = true
				continue
			}
			for i, n := range sample.LatencyHistogram.Counts {
				if math.MaxUint64-out.LatencyHistogram.Counts[i] < n {
					return nil, ErrInvalid
				}
				out.LatencyHistogram.Counts[i] += n
			}
			out.Coverage.Latency++
		}
	}
	if !validMetricNumber(out.CPUUsageMillicores) || !validMetricNumber(out.CPURequestMillicores) || !validMetricNumber(out.MemoryBytes) || !validMetricNumber(out.Requests) {
		return nil, ErrInvalid
	}
	if out.CPURequestMillicores != nil && *out.CPURequestMillicores > 0 {
		v := *out.CPUUsageMillicores / *out.CPURequestMillicores
		if !validMetricNumber(&v) {
			return nil, ErrInvalid
		}
		out.CPUUtilization = &v
	}
	if histogramMismatch {
		out.LatencyHistogram = nil
		out.Coverage.Latency = 0
		out.Reason = "histogram_incompatible"
	}
	if out.LatencyHistogram != nil {
		var total uint64
		for _, n := range out.LatencyHistogram.Counts {
			if math.MaxUint64-total < n {
				return nil, ErrInvalid
			}
			total += n
		}
		if total > 0 {
			// ceil(total * .95), without overflowing an integer or losing precision.
			rank := total - total/20
			var cumulative uint64
			for i, n := range out.LatencyHistogram.Counts {
				cumulative += n
				if cumulative >= rank {
					if i < len(out.LatencyHistogram.UpperBoundsMillis) {
						v := out.LatencyHistogram.UpperBoundsMillis[i]
						out.LatencyP95Millis = &v
					}
					break
				}
			}
		}
	}
	switch {
	case out.Coverage.Fresh == b.Expected && b.Expected > 0:
		out.Freshness = "fresh"
	case out.Coverage.Fresh > 0:
		out.Freshness = "partial"
	case out.OldestObservation != nil:
		out.Freshness = "stale"
	}
	if historical && out.Coverage.Fresh > 0 {
		out.Freshness = "historical"
	}
	out.Partial = out.Partial || b.Expected == 0 || out.Coverage.CPU < b.Expected || out.Coverage.Memory < b.Expected || out.Coverage.Latency < b.Expected || out.Coverage.Replicas < b.Expected || out.Coverage.Requests < b.Expected
	return out, nil
}

func validateMetricWindow(w fleet.MetricWindow, now time.Time) error {
	if w.Start.IsZero() || !w.End.After(w.Start) || w.End.Sub(w.Start) > 24*time.Hour || w.End.After(now) || now.Sub(w.End) > 30*24*time.Hour {
		return ErrInvalid
	}
	return nil
}
func validMetricNumber(v *float64) bool {
	return v == nil || (!math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0)
}
func validMetricSample(s MetricSample) bool {
	for _, v := range []*float64{s.CPUUsageMillicores, s.CPURequestMillicores, s.MemoryBytes, s.LatencyP95Millis, s.Requests} {
		if !validMetricNumber(v) {
			return false
		}
	}
	for _, v := range []*int64{s.DesiredReplicas, s.CurrentReplicas, s.ReadyReplicas} {
		if v != nil && *v < 0 {
			return false
		}
	}
	if s.ReadyReplicas != nil && s.CurrentReplicas != nil && *s.ReadyReplicas > *s.CurrentReplicas {
		return false
	}
	if h := s.LatencyHistogram; h != nil {
		if len(h.UpperBoundsMillis) == 0 || len(h.UpperBoundsMillis) > 64 || len(h.Counts) != len(h.UpperBoundsMillis)+1 {
			return false
		}
		var previous float64
		for i, v := range h.UpperBoundsMillis {
			if !validMetricNumber(&v) || (i > 0 && v <= previous) {
				return false
			}
			previous = v
		}
	}
	return true
}
func addMetric(total **float64, v float64) {
	if *total == nil {
		n := 0.
		*total = &n
	}
	**total += v
}
func addReplica(total **int64, v int64) bool {
	if *total == nil {
		n := int64(0)
		*total = &n
	}
	if math.MaxInt64-**total < v {
		return false
	}
	**total += v
	return true
}

// MetricScope comes from an authorized, persisted instance, never request labels.
type MetricScope struct{ OrgID, FleetID, InstanceID, EnvironmentID string }

// MetricSource reuses provider readers and reports unsupported signals as missing.
type MetricSource interface {
	ReadMetrics(context.Context, MetricScope, fleet.MetricWindow) (MetricBatch, error)
}

// Metrics rechecks current access on every read, including reconciliation polls.
func (s *Service) Metrics(ctx context.Context, p security.Principal, id string, w fleet.MetricWindow) (*fleet.MetricSummary, error) {
	instance, err := s.GetInstance(ctx, p, id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if checkErr := validateMetricWindow(w, now); checkErr != nil {
		return nil, checkErr
	}
	batch := MetricBatch{Partial: true}
	reason := "source_unavailable"
	if s.metrics != nil {
		batch, err = s.metrics.ReadMetrics(ctx, MetricScope{OrgID: p.OrgID, FleetID: instance.FleetID, InstanceID: instance.ID, EnvironmentID: instance.EnvironmentID}, w)
		reason = ""
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			batch = MetricBatch{Partial: true}
			reason = "source_failed"
		}
	}
	out, err := AggregateMetrics(w, time.Now().UTC(), batch)
	if err != nil {
		fallback, fallbackErr := AggregateMetrics(w, time.Now().UTC(), MetricBatch{Partial: true})
		if fallbackErr != nil {
			return nil, fallbackErr
		}
		fallback.Reason = "source_invalid"
		return fallback, nil
	}
	if reason != "" {
		out.Reason = reason
	}
	return out, nil
}
