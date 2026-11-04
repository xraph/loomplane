package agentwatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	dispatchengine "github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/job"

	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentruns"
)

// The watcher's jobs and its schedule. The AI-off extension registers
// JobTick, and defensively JobOrg and JobDeliver, with handlers that do
// nothing; it can import them from here, since nothing agentwatch imports
// imports the extensions.
const (
	JobTick    = "loomplane.agent.watch.tick"
	CronTick   = "loomplane.agent.watch"
	JobOrg     = "loomplane.agent.watch.org"
	JobDeliver = "loomplane.agent.report.deliver"
	// TickSchedule is how often the cron queues TickJob.
	TickSchedule = "@every 30s"
)

// How long a pass and a delivery may run. A pass is bounded by its own
// context well under LeaseTTL; a delivery's job ends before its
// agentreports.DeliveryLease runs out, so a worker never sends under a
// lease another worker may have taken.
const (
	orgJobTimeout     = 2 * time.Minute
	deliverJobTimeout = 90 * time.Second
	deliverJobRetries = 8
)

// OrgPayload names the workspace a pass watches.
type OrgPayload struct{ OrgID string }

// DeliverPayload names the report to deliver.
type DeliverPayload struct{ OrgID, ReportID string }

// TickJob is the cron job's: it queues a pass for each workspace whose
// agent is on. It runs on agentruns.SweepQueue, which every server works,
// so a schedule left from AI on never piles up on a queue nobody works.
func TickJob(w *Watcher) *job.Definition[struct{}] {
	return job.NewDefinition(JobTick, func(ctx context.Context, _ struct{}) error {
		return w.Tick(ctx)
	}, job.WithQueue(agentruns.SweepQueue), job.WithMaxRetries(0))
}

// OrgJob is one workspace's pass. It isn't retried: the next tick queues
// another. Like DeliverJob it runs on agentruns.SweepQueue, not the
// agent's queue: neither calls a model, and model runs busy on the
// agent's queue must never hold the watcher back past OffForLong.
func OrgJob(w *Watcher) *job.Definition[OrgPayload] {
	return job.NewDefinition(JobOrg, func(ctx context.Context, in OrgPayload) error {
		return w.RunOrg(ctx, in.OrgID)
	}, orgOptions()...)
}

func orgOptions() []job.Option {
	return []job.Option{job.WithQueue(agentruns.SweepQueue), job.WithMaxRetries(0), job.WithTimeout(orgJobTimeout)}
}

// DeliverJob delivers one report, on agentruns.SweepQueue. Each attempt
// holds the delivery under its own owner, replica and a random suffix,
// so a retry or another job on the same worker never passes for the
// attempt before it.
func DeliverJob(r *agentreports.Service, replica string) *job.Definition[DeliverPayload] {
	return job.NewDefinition(JobDeliver, func(ctx context.Context, in DeliverPayload) error {
		owner, err := attemptOwner(replica)
		if err != nil {
			return err
		}
		return r.Deliver(ctx, in.OrgID, in.ReportID, owner)
	}, deliverOptions()...)
}

func deliverOptions() []job.Option {
	return []job.Option{job.WithQueue(agentruns.SweepQueue), job.WithMaxRetries(deliverJobRetries), job.WithTimeout(deliverJobTimeout)}
}

// attemptOwner is one delivery attempt's owner.
func attemptOwner(replica string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("agentwatch: make a delivery owner: %w", err)
	}
	if replica == "" {
		replica = "delivery"
	}
	return replica + ":" + hex.EncodeToString(b), nil
}

// DispatchEnqueuer queues the watcher's jobs through Dispatch.
type DispatchEnqueuer struct {
	Engine func() *dispatchengine.Engine
	// Queue is where the jobs go; "" is agentruns.SweepQueue.
	Queue string
}

var _ Enqueuer = DispatchEnqueuer{}

func (e DispatchEnqueuer) engine() (*dispatchengine.Engine, error) {
	if e.Engine == nil {
		return nil, errors.New("agentwatch: the job engine isn't configured")
	}
	eng := e.Engine()
	if eng == nil {
		return nil, errors.New("agentwatch: the job engine isn't ready")
	}
	return eng, nil
}

func (e DispatchEnqueuer) queue() string {
	if e.Queue == "" {
		return agentruns.SweepQueue
	}
	return e.Queue
}

// WatchOrg implements Enqueuer.
func (e DispatchEnqueuer) WatchOrg(ctx context.Context, orgID string) error {
	eng, err := e.engine()
	if err != nil {
		return err
	}
	opts := append(orgOptions(), job.WithQueue(e.queue()))
	_, err = dispatchengine.Enqueue(ctx, eng, JobOrg, OrgPayload{OrgID: orgID}, opts...)
	return err
}

// Deliver implements Enqueuer.
func (e DispatchEnqueuer) Deliver(ctx context.Context, orgID, reportID string) error {
	eng, err := e.engine()
	if err != nil {
		return err
	}
	opts := append(deliverOptions(), job.WithQueue(e.queue()))
	_, err = dispatchengine.Enqueue(ctx, eng, JobDeliver, DeliverPayload{OrgID: orgID, ReportID: reportID}, opts...)
	return err
}
