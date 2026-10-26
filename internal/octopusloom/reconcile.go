package octopusloom

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xraph/dispatch/job"
)

// JobOctopusReconcile syncs every Octopus gateway. Its cron entry runs it
// every 30 seconds on the leader only. Keep the name stable: queued jobs
// refer to it by name.
const JobOctopusReconcile = "loomplane.octopus.reconcile"

// CronOctopusReconcile names the cron entry that enqueues
// JobOctopusReconcile.
const CronOctopusReconcile = "loomplane.octopus"

// ReconcilePayload is the payload of JobOctopusReconcile. It has nothing
// in it: a reconcile covers every gateway.
type ReconcilePayload struct{}

// reconcileParallel is how many gateways a reconcile syncs at once.
const reconcileParallel = 8

// reconcileSyncTimeout bounds one gateway's sync in a reconcile.
var reconcileSyncTimeout = 10 * time.Second

// ReconcileJob is JobOctopusReconcile's definition. It syncs each gateway,
// which pushes what a write or a release didn't get through, puts back
// what an Octopus restart lost and keeps Docker services' FARP
// registrations alive. A gateway that doesn't sync is logged and left for
// the next run; the job fails only when it can't list the gateways. It
// isn't retried: the next run is 30 seconds away.
func ReconcileJob(s *Syncer) *job.Definition[ReconcilePayload] {
	return job.NewDefinition(JobOctopusReconcile, func(ctx context.Context, _ ReconcilePayload) error {
		return reconcile(ctx, s)
	}, job.WithMaxRetries(0))
}

func reconcile(ctx context.Context, s *Syncer) error {
	gateways, err := s.Store.Gateways(ctx)
	if err != nil {
		return fmt.Errorf("list the Octopus gateways: %w", err)
	}
	slots := make(chan struct{}, reconcileParallel)
	var wg sync.WaitGroup
	for _, g := range gateways {
		if g == nil {
			continue
		}
		slots <- struct{}{}
		wg.Add(1)
		go func(ref GatewayRef) {
			defer func() {
				<-slots
				wg.Done()
			}()
			syncCtx, cancel := context.WithTimeout(ctx, reconcileSyncTimeout)
			defer cancel()
			res, err := s.Sync(syncCtx, ref)
			if err != nil {
				s.warn(fmt.Sprintf("Octopus gateway %s in environment %s didn't sync (%s): %s", ref.Resource, ref.EnvironmentID, res.State, res.Message), err)
			}
		}(g.GatewayRef)
	}
	wg.Wait()
	return nil
}
