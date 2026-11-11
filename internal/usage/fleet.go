package usage

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/fleet"
)

// FleetMetricInstances resolves the instance before any connection credential is read.
type FleetMetricInstances interface {
	GetInstance(context.Context, string, string) (*fleet.Instance, error)
}

// FleetMetricBindings reads only one persisted environment's service bindings.
type FleetMetricBindings interface {
	ListBindings(context.Context, string, string) ([]*planning.Binding, error)
}

type fleetMetricSource struct {
	usage     *Service
	instances FleetMetricInstances
	bindings  FleetMetricBindings
}

// NewFleetMetricSource adapts existing meters. Unsupported CPU, queue and histogram
// signals remain unknown until a qualified reader supplies them.
func NewFleetMetricSource(usage *Service, instances FleetMetricInstances, bindings FleetMetricBindings) fleets.MetricSource {
	return &fleetMetricSource{usage: usage, instances: instances, bindings: bindings}
}
func (s *fleetMetricSource) ReadMetrics(ctx context.Context, scope fleets.MetricScope, w fleet.MetricWindow) (fleets.MetricBatch, error) {
	out := fleets.MetricBatch{Partial: true}
	if s.usage == nil || s.instances == nil || s.bindings == nil {
		return out, errors.New("fleet metric source unavailable")
	}
	instance, err := s.instances.GetInstance(ctx, scope.OrgID, scope.InstanceID)
	if err != nil || instance == nil || instance.FleetID != scope.FleetID || instance.EnvironmentID != scope.EnvironmentID {
		return out, fleets.ErrNotFound
	}
	if w.Start.IsZero() || !w.End.After(w.Start) || w.End.Sub(w.Start) > 24*time.Hour {
		return out, fleets.ErrInvalid
	}
	bindings, err := s.bindings.ListBindings(ctx, scope.OrgID, scope.EnvironmentID)
	if err != nil {
		return out, err
	}
	if len(bindings) > 1000 {
		return out, fleets.ErrInvalid
	}
	seen := map[string]bool{}
	for _, binding := range bindings {
		if binding == nil || binding.OrgID != scope.OrgID || binding.EnvironmentID != scope.EnvironmentID {
			return out, fleets.ErrNotFound
		}
		if binding.Subject != planning.ServiceSubject {
			continue
		}
		if seen[binding.Name] {
			return out, fleets.ErrInvalid
		}
		seen[binding.Name] = true
	}
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	all, err := s.usage.Connections.All(ctx, scope.OrgID)
	if err != nil {
		return out, err
	}
	conns := map[string]*connections.Connection{}
	for _, c := range all {
		if c != nil && c.OrgID == scope.OrgID {
			conns[c.ID] = c
		}
	}
	window := metrics.Window{Start: w.Start, End: w.End, Step: time.Minute}
	reads := 0
	for _, b := range bindings {
		if b == nil || b.OrgID != scope.OrgID || b.EnvironmentID != scope.EnvironmentID {
			return out, fleets.ErrNotFound
		}
		if b.Subject != planning.ServiceSubject {
			continue
		}
		out.Expected++
		sample := fleets.MetricSample{Component: b.Name, Source: b.Provider}
		meter := s.usage.Meters[b.Provider]
		conn := conns[b.ConnectionID]
		if meter == nil || conn == nil {
			sample.Reason = "source_unavailable"
			out.Samples = append(out.Samples, sample)
			continue
		}
		if reads >= 32 || ctx.Err() != nil {
			sample.Reason = "source_unavailable"
			out.Samples = append(out.Samples, sample)
			continue
		}
		reads++
		live, credential, readErr := s.usage.credential(ctx, conn)
		var measurement *metrics.Measurement
		if readErr == nil {
			measurement, readErr = meter.Measure(ctx, live, credential, b, window)
		}
		s.usage.Telemetry.UsageRead("traffic", b.Provider, outcome(readErr))
		if readErr != nil || measurement == nil {
			sample.Reason = "source_failed"
			out.Samples = append(out.Samples, sample)
			continue
		}
		var total float64
		known := false
		for _, p := range measurement.Requests {
			if p.T.Before(w.Start) || !p.T.Before(w.End) {
				continue
			}
			known = true
			total += p.V
			if p.T.After(sample.ObservedAt) {
				sample.ObservedAt = p.T
			}
		}
		if known {
			sample.Requests = &total
		}
		if measurement.Statistic == metrics.P95 {
			value := measurement.Latency
			sample.LatencyP95Millis = &value
		}
		out.Samples = append(out.Samples, sample)
	}
	return out, nil
}
