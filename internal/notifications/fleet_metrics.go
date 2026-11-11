package notifications

import (
	"context"
	"time"
)

// FleetMetricSampler uses the existing notification worker lifecycle. Each call
// leases at most one instance; observations never authorize provider writes.
type FleetMetricSampler interface {
	SampleNext(context.Context) error
	PrunePage(context.Context) (bool, error)
}

// SetFleetMetricSampler is called during application composition, before Run.
func (s *Service) SetFleetMetricSampler(sampler FleetMetricSampler) { s.fleetMetricSampler = sampler }
func (s *Service) startFleetMetrics(ctx context.Context) {
	if s.fleetMetricSampler == nil || ctx.Err() != nil {
		return
	}
	worker, ok := s.startBackground()
	if !ok {
		return
	}
	worker, cancel := context.WithCancel(worker)
	stop := context.AfterFunc(ctx, cancel)
	go func() {
		defer s.wg.Done()
		defer cancel()
		defer stop()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var prunedAt time.Time
		for {
			if worker.Err() != nil {
				return
			}
			err := s.fleetMetricSampler.SampleNext(worker)
			if worker.Err() != nil {
				return
			}
			if time.Since(prunedAt) >= time.Hour {
				done, pruneErr := s.fleetMetricSampler.PrunePage(worker)
				if pruneErr == nil && done {
					prunedAt = time.Now()
				}
				if err == nil {
					err = pruneErr
				}
			}
			s.statusMu.Lock()
			s.fleetMetricErr = err
			s.statusMu.Unlock()
			select {
			case <-worker.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
