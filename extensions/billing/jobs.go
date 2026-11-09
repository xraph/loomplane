//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/xraph/dispatch/cron"
	"github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/job"
)

// Billing's jobs. Each one runs on the billing queue, from a cron that
// fires on the cron leader only, in UTC (a schedule with no time zone runs
// in UTC). None retries: each is safe to run again, and its next run is
// the retry.
const (
	// JobPushUsage prices AI on the server's key from nexus's records,
	// moves usage into ledger, rolls ledger's periods on and settles every
	// workspace's spending limit.
	JobPushUsage = "billing.push_usage"
	// JobCloseMonth invoices the month that just ended.
	JobCloseMonth = "billing.close_month"
	// JobSyncInvoices finishes a month close that didn't finish and sends
	// to Stripe the invoices that aren't there yet.
	JobSyncInvoices = "billing.sync_invoices"
)

// jobPayload is every billing job's input: there is none.
type jobPayload struct{}

// schedules are billing's crons, by job. The close runs at 01:15 UTC on
// the 1st, 73 minutes after the month's last hour of usage.
var schedules = []struct{ job, schedule string }{
	{JobPushUsage, "@every 15m"},
	{JobCloseMonth, "15 1 1 * *"},
	{JobSyncInvoices, "@every 1h"},
}

// cronDefinitions are the cron entries Start registers.
func cronDefinitions() []*cron.Definition[jobPayload] {
	out := make([]*cron.Definition[jobPayload], 0, len(schedules))
	for _, s := range schedules {
		out = append(out, &cron.Definition[jobPayload]{Name: s.job, JobName: s.job, Schedule: s.schedule, Queue: Queue})
	}
	return out
}

// biller is what the jobs run, so a test can count the calls.
type biller interface {
	PriceAIClaims(ctx context.Context) error
	PushUsage(ctx context.Context) ([]string, error)
	EvaluateAll(ctx context.Context) error
	CloseMonth(ctx context.Context) error
	CloseMonthIfOpen(ctx context.Context) error
	SyncInvoices(ctx context.Context) error
}

// advancer rolls ledger's periods on, as *ledger.Ledger does.
type advancer func(ctx context.Context, now time.Time) error

// jobHandlers are the jobs' handlers, by name.
func jobHandlers(svc biller, advance advancer, now func() time.Time) map[string]func(context.Context, jobPayload) error {
	return map[string]func(context.Context, jobPayload) error{
		// Every step runs even when one before it fails: a failed push
		// still leaves the limits to settle on what did arrive. AI is
		// priced first, so its usage goes out in the same push.
		JobPushUsage: func(ctx context.Context, _ jobPayload) error {
			priceErr := svc.PriceAIClaims(ctx)
			_, pushErr := svc.PushUsage(ctx)
			advErr := advance(ctx, now().UTC())
			if advErr != nil {
				advErr = fmt.Errorf("billing: advance ledger's clock: %w", advErr)
			}
			return errors.Join(priceErr, pushErr, advErr, svc.EvaluateAll(ctx))
		},
		JobCloseMonth: func(ctx context.Context, _ jobPayload) error {
			return svc.CloseMonth(ctx)
		},
		// A close that stopped part way is finished first, and it syncs
		// what it invoiced; the sync after it retries what failed before.
		JobSyncInvoices: func(ctx context.Context, _ jobPayload) error {
			closeErr := svc.CloseMonthIfOpen(ctx)
			if closeErr != nil {
				slog.ErrorContext(ctx, "billing: finishing the month close failed; the next hour tries again", "error", closeErr)
			}
			return errors.Join(closeErr, svc.SyncInvoices(ctx))
		},
	}
}

// registerJobs registers billing's jobs with the engine, on the billing
// queue and with no retries.
func registerJobs(eng *engine.Engine, svc biller, advance advancer, now func() time.Time) {
	handlers := jobHandlers(svc, advance, now)
	for _, s := range schedules {
		engine.Register(eng, job.NewDefinition(s.job, handlers[s.job], job.WithQueue(Queue), job.WithMaxRetries(0)))
	}
}

// scheduleJobs registers billing's crons, which fire on the leader only.
func scheduleJobs(ctx context.Context, eng *engine.Engine) error {
	for _, def := range cronDefinitions() {
		if err := engine.RegisterCron(ctx, eng, def); err != nil {
			return fmt.Errorf("schedule %s: %w", def.Name, err)
		}
	}
	return nil
}
