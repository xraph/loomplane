package notifications

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type metricsWorkerProbe struct {
	entered, exited chan struct{}
	calls           atomic.Int32
	prunes          atomic.Int32
}

func (p *metricsWorkerProbe) SampleNext(ctx context.Context) error {
	if p.calls.Add(1) == 1 {
		close(p.entered)
	}
	<-ctx.Done()
	close(p.exited)
	return ctx.Err()
}
func (p *metricsWorkerProbe) PrunePage(context.Context) (bool, error) {
	p.prunes.Add(1)
	return true, nil
}
func TestFleetMetricWorkerCancellationDrainsOwnedWork(t *testing.T) {
	probe := &metricsWorkerProbe{entered: make(chan struct{}), exited: make(chan struct{})}
	svc := &Service{}
	svc.SetFleetMetricSampler(probe)
	ctx, cancel := context.WithCancel(t.Context())
	svc.startFleetMetrics(ctx)
	select {
	case <-probe.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("sampler did not start")
	}
	cancel()
	done := make(chan struct{})
	go func() { svc.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sampler did not drain")
	}
	select {
	case <-probe.exited:
	default:
		t.Fatal("worker returned before source stopped")
	}
	if probe.calls.Load() != 1 || probe.prunes.Load() != 0 {
		t.Fatal("cancellation allowed new sampler work")
	}
}
func TestFleetMetricWorkerCannotStartAfterShutdown(t *testing.T) {
	probe := &metricsWorkerProbe{entered: make(chan struct{}), exited: make(chan struct{})}
	svc := &Service{closed: true}
	svc.SetFleetMetricSampler(probe)
	svc.startFleetMetrics(t.Context())
	svc.wg.Wait()
	if probe.calls.Load() != 0 {
		t.Fatal("sampler started after shutdown")
	}
}

type metricsBacklogProbe struct {
	calls   atomic.Int32
	drained chan struct{}
}

func (p *metricsBacklogProbe) SampleNext(context.Context) error { return nil }
func (p *metricsBacklogProbe) PrunePage(context.Context) (bool, error) {
	n := p.calls.Add(1)
	if n == 3 {
		close(p.drained)
	}
	return n >= 3, nil
}
func TestFleetMetricWorkerDrainsRetentionBacklog(t *testing.T) {
	probe := &metricsBacklogProbe{drained: make(chan struct{})}
	svc := &Service{}
	svc.SetFleetMetricSampler(probe)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	svc.startFleetMetrics(ctx)
	select {
	case <-probe.drained:
	case <-time.After(5 * time.Second):
		cancel()
		svc.wg.Wait()
		t.Fatal("full retention page was postponed for an hour")
	}
	cancel()
	svc.wg.Wait()
	if probe.calls.Load() != 3 {
		t.Fatalf("prune passes %d", probe.calls.Load())
	}
}
