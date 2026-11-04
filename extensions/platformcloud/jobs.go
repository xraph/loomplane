package platformcloud

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/dispatch/job"

	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/platformcloud"
)

// jobQueue is the queue the platform Cloud's jobs run on.
const jobQueue = orchestration.Queue

// Cron entries and the jobs they enqueue. Keep the names stable: queued
// jobs refer to them by name.
const (
	CronSync    = "platform-cloud-sync"
	CronSample  = "platform-cloud-sample"
	CronEgress  = "platform-cloud-egress"
	CronReclaim = "platform-cloud-reclaim"
	CronRestore = "platform-cloud-restore"

	JobSync    = "loomplane.platform_cloud.sync"
	JobSample  = "loomplane.platform_cloud.sample"
	JobEgress  = "loomplane.platform_cloud.egress"
	JobReclaim = "loomplane.platform_cloud.reclaim"
	JobRestore = "loomplane.platform_cloud.restore"
)

// jobs are the platform Cloud's scheduled jobs:
//   - sync, every minute: projections, host re-checks, what moves and failed
//     unpublishes left behind, tenants whose organization is gone;
//   - sample, every minute: compute metering;
//   - egress, every five minutes: egress metering;
//   - reclaim, hourly: stops again what a suspension missed, takes long
//     suspended VMs off their hosts, and repairs placements whose host lost
//     their VM;
//   - restore, every minute: brings back work that should run again.
func jobs(svc *platformcloud.Service) []cronJob {
	return []cronJob{
		{CronSync, "@every 1m", JobSync, svc.Sync},
		{CronSample, "@every 1m", JobSample, func(ctx context.Context) error { return svc.SampleCompute(ctx, time.Now()) }},
		{CronEgress, "@every 5m", JobEgress, func(ctx context.Context) error { return svc.SampleEgress(ctx, time.Now()) }},
		{CronReclaim, "@every 1h", JobReclaim, func(ctx context.Context) error {
			return errors.Join(svc.Reclaim(ctx, time.Now()), svc.RepairMissingVMs(ctx))
		}},
		{CronRestore, "@every 1m", JobRestore, svc.RetryRestores},
	}
}

// jobDefinition is a job that isn't retried: the next run is at most an
// hour away and does the same work.
func jobDefinition(name string, fn func(context.Context, tick) error) *job.Definition[tick] {
	return job.NewDefinition(name, fn, job.WithMaxRetries(0))
}
