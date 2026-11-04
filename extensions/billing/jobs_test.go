//go:build cloud

package billing

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/xraph/dispatch"
	"github.com/xraph/dispatch/cron"
	"github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/store/memory"

	"github.com/xraph/loomplane/internal/config"
)

// testEngine is a dispatch engine over an in-memory store, and the store,
// so a test can read the crons it registered.
func testEngine(t *testing.T) (*engine.Engine, *memory.Store) {
	t.Helper()
	st := memory.New()
	d, err := dispatch.New(dispatch.WithStore(st))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Build(d)
	if err != nil {
		t.Fatal(err)
	}
	return eng, st
}

func TestJobsAreScheduledInUTCOnTheBillingQueue(t *testing.T) {
	want := map[string]string{
		"billing.push_usage":    "@every 15m",
		"billing.close_month":   "15 1 1 * *",
		"billing.sync_invoices": "@every 1h",
	}
	defs := cronDefinitions()
	if len(defs) != len(want) {
		t.Fatalf("%d crons, want %d", len(defs), len(want))
	}
	for _, d := range defs {
		if want[d.Name] != d.Schedule || d.JobName != d.Name || d.Queue != "billing" {
			t.Errorf("cron %+v", d)
		}
	}
	// The close fires at 01:15 UTC on the 1st, wherever the process runs.
	fires, loc, err := cron.NextFires("15 1 1 * *", time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), 2)
	if err != nil || loc != time.UTC {
		t.Fatalf("next fires: %v in %v", err, loc)
	}
	if !fires[0].Equal(time.Date(2026, 11, 1, 1, 15, 0, 0, time.UTC)) || !fires[1].Equal(time.Date(2026, 12, 1, 1, 15, 0, 0, time.UTC)) {
		t.Fatalf("the close fires at %v", fires)
	}
}

// Register registers the three jobs and Start schedules their crons, on
// the billing queue.
func TestRegisterAndStartScheduleTheJobs(t *testing.T) {
	c := testContainer(t)
	eng, st := testEngine(t)
	e := New(config.Config{Identity: config.IdentityConfig{AppID: testAppID}}, func() *engine.Engine { return eng })
	if err := e.register(c); err != nil {
		t.Fatal(err)
	}
	names := eng.Registry().Names()
	for _, j := range []string{JobPushUsage, JobCloseMonth, JobSyncInvoices} {
		if !slices.Contains(names, j) {
			t.Fatalf("job %s isn't registered: %v", j, names)
		}
	}
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	entries, err := st.ListCrons(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, en := range entries {
		if en.Queue != Queue || en.JobName != en.Name {
			t.Errorf("cron %s on queue %q runs %q", en.Name, en.Queue, en.JobName)
		}
		got[en.Name] = en.Schedule
	}
	if len(got) != 3 || got[JobCloseMonth] != "15 1 1 * *" {
		t.Fatalf("crons %v", got)
	}
	// A second start (another replica) schedules nothing twice.
	if err = e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if entries, _ = st.ListCrons(t.Context()); len(entries) != 3 {
		t.Fatalf("%d crons after a second start", len(entries))
	}
}

func TestRegisterNeedsTheEngine(t *testing.T) {
	c := testContainer(t)
	e := New(config.Config{Identity: config.IdentityConfig{AppID: testAppID}}, func() *engine.Engine { return nil })
	if err := e.register(c); err == nil {
		t.Fatal("registered billing's jobs with no engine")
	}
}

// billerSpy records the jobs' calls, in order.
type billerSpy struct {
	calls    []string
	priceErr error
	pushErr  error
}

func (b *billerSpy) PriceAIClaims(context.Context) error {
	b.calls = append(b.calls, "price")
	return b.priceErr
}

func (b *billerSpy) PushUsage(context.Context) ([]string, error) {
	b.calls = append(b.calls, "push")
	return nil, b.pushErr
}
func (b *billerSpy) EvaluateAll(context.Context) error {
	b.calls = append(b.calls, "evaluate")
	return nil
}
func (b *billerSpy) CloseMonth(context.Context) error { b.calls = append(b.calls, "close"); return nil }
func (b *billerSpy) CloseMonthIfOpen(context.Context) error {
	b.calls = append(b.calls, "close-if-open")
	return nil
}
func (b *billerSpy) SyncInvoices(context.Context) error {
	b.calls = append(b.calls, "sync")
	return nil
}

func TestJobs_RunTheirStepsInOrder(t *testing.T) {
	now := time.Date(2026, 11, 1, 1, 15, 0, 0, time.UTC)
	for job, want := range map[string][]string{
		JobPushUsage:    {"price", "push", "advance", "evaluate"},
		JobCloseMonth:   {"close"},
		JobSyncInvoices: {"close-if-open", "sync"},
	} {
		spy := &billerSpy{}
		var advancedTo time.Time
		h := jobHandlers(spy, func(_ context.Context, at time.Time) error {
			spy.calls = append(spy.calls, "advance")
			advancedTo = at
			return nil
		}, func() time.Time { return now })
		if err := h[job](t.Context(), jobPayload{}); err != nil {
			t.Fatalf("%s: %v", job, err)
		}
		if !slices.Equal(spy.calls, want) {
			t.Fatalf("%s ran %v, want %v", job, spy.calls, want)
		}
		if job == JobPushUsage && !advancedTo.Equal(now) {
			t.Fatalf("advanced to %v", advancedTo)
		}
	}
}

// A push that fails still lets the clock roll and the limits settle, and
// the job reports the failure.
func TestPushJob_KeepsGoingPastAFailedPush(t *testing.T) {
	spy := &billerSpy{pushErr: errors.New("one group failed")}
	h := jobHandlers(spy, func(context.Context, time.Time) error {
		spy.calls = append(spy.calls, "advance")
		return nil
	}, time.Now)
	if err := h[JobPushUsage](t.Context(), jobPayload{}); err == nil {
		t.Fatal("the failed push was dropped")
	}
	if !slices.Equal(spy.calls, []string{"price", "push", "advance", "evaluate"}) {
		t.Fatalf("ran %v", spy.calls)
	}
}

// AI pricing that fails still lets the push, the clock and the limits run,
// and the job reports the failure.
func TestPushJob_KeepsGoingPastFailedAIPricing(t *testing.T) {
	spy := &billerSpy{priceErr: errors.New("nexus is down")}
	h := jobHandlers(spy, func(context.Context, time.Time) error {
		spy.calls = append(spy.calls, "advance")
		return nil
	}, time.Now)
	if err := h[JobPushUsage](t.Context(), jobPayload{}); err == nil {
		t.Fatal("the failed pricing was dropped")
	}
	if !slices.Equal(spy.calls, []string{"price", "push", "advance", "evaluate"}) {
		t.Fatalf("ran %v", spy.calls)
	}
}
