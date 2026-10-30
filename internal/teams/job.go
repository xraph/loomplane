package teams

import (
	"context"

	"github.com/xraph/dispatch/job"
)

// JobSync drains the role sync queue (Service.Drain). Its cron entry runs
// it every minute. Keep the name stable: queued jobs refer to it by name.
const JobSync = "teams.sync"

// SyncPayload is the payload of JobSync. It has nothing in it: a run
// covers the whole queue.
type SyncPayload struct{}

// SyncJob is JobSync's definition. It isn't retried: the next run is a
// minute away, and every row it couldn't sync is still queued for it.
func SyncJob(s *Service) *job.Definition[SyncPayload] {
	return job.NewDefinition(JobSync, func(ctx context.Context, _ SyncPayload) error {
		return s.Drain(ctx)
	}, job.WithMaxRetries(0))
}
