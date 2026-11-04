//go:build integration

package platformcloud

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/metering"
)

type recordingMeter struct {
	mu     sync.Mutex
	byID   map[string]metering.Event
	record int
}

func newRecordingMeter() *recordingMeter { return &recordingMeter{byID: map[string]metering.Event{}} }

// Record is idempotent on ID the way the real meter is: the first event wins.
func (m *recordingMeter) Record(_ context.Context, e metering.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record++
	if _, ok := m.byID[e.ID]; !ok {
		m.byID[e.ID] = e
	}
	return nil
}

func (m *recordingMeter) total(feature string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var sum float64
	for _, e := range m.byID {
		if e.Feature == feature {
			sum += e.Quantity
		}
	}
	return sum
}

func (m *recordingMeter) ids() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{}
	for id := range m.byID {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

var meterHour = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

// testClock is the service's clock, set by the test.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func meterEnv(t *testing.T) (*placerEnv, *recordingMeter) {
	t.Helper()
	e, _ := suspendEnv(t)
	m := newRecordingMeter()
	e.svc.WithMeter(m)
	e.clock = &testClock{t: meterHour}
	e.svc.now = e.clock.now
	return e, m
}

// restarted is a fresh Service over the same database and meter.
func (e *placerEnv) restarted(m metering.Meter) *Service {
	s := NewService(e.store, e.svc.settings, e.svc.orgs).WithMeter(m)
	s.usage = e.svc.usage
	s.now = e.svc.now
	return s
}

func TestComputeEventsAreStableAcrossRestarts(t *testing.T) {
	e, m := meterEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	p := e.running(t, "web") // 1 vCPU, 512 MiB

	for i := 1; i <= 3; i++ {
		if err := e.svc.SampleCompute(ctx, meterHour.Add(time.Duration(i)*time.Minute+7*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	// A restarted sampler sees minute 3 again.
	again := e.restarted(m)
	if err := again.SampleCompute(ctx, meterHour.Add(3*time.Minute+40*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Nothing is billed until the hour has closed.
	if got := m.total("compute_vcpu_hours"); got != 0 {
		t.Fatalf("emitted before the hour closed: %v", got)
	}
	// The placement stops, then the hour closes.
	if err := e.svc.Stopped(ctx, "org_a", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := again.SampleCompute(ctx, meterHour.Add(time.Hour+emitDelay)); err != nil {
		t.Fatal(err)
	}
	if got, want := m.total("compute_vcpu_hours"), 3.0/60; !near(got, want) {
		t.Fatalf("vcpu hours = %v, want %v (3 minutes)", got, want)
	}
	if got, want := m.total("compute_gb_ram_hours"), 0.5*3/60; !near(got, want) {
		t.Fatalf("gb ram hours = %v, want %v", got, want)
	}
	wantID := "platform:" + p.ID + ":3:" + meterHour.Format(time.RFC3339) + ":compute_vcpu_hours"
	found := false
	for _, id := range m.ids() {
		found = found || id == wantID
	}
	if !found {
		t.Fatalf("event ids %v lack %s", m.ids(), wantID)
	}
	// Sampling the closed hour again changes nothing.
	if err := e.restarted(m).SampleCompute(ctx, meterHour.Add(2*time.Hour+2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	closed := m.byID[wantID].Quantity
	m.mu.Unlock()
	if !near(closed, 3.0/60) {
		t.Fatalf("closed hour changed: %v", closed)
	}
}

func TestComputeCountsAMissedMinuteOnce(t *testing.T) {
	e, m := meterEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	p := e.running(t, "web")
	for _, min := range []int{1, 2, 5} { // minutes 3 and 4 missed while the sampler was down
		if err := e.svc.SampleCompute(ctx, meterHour.Add(time.Duration(min)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.Stopped(ctx, "org_a", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SampleCompute(ctx, meterHour.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, want := m.total("compute_vcpu_hours"), 5.0/60; !near(got, want) {
		t.Fatalf("vcpu hours = %v, want %v", got, want)
	}
}

func TestComputeSplitsAcrossHours(t *testing.T) {
	e, m := meterEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	e.clock.set(meterHour.Add(57 * time.Minute))
	p := e.running(t, "web")
	for _, at := range []time.Duration{58 * time.Minute, 59 * time.Minute, 60 * time.Minute, 61 * time.Minute} {
		if err := e.svc.SampleCompute(ctx, meterHour.Add(at)); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.Stopped(ctx, "org_a", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SampleCompute(ctx, meterHour.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	first := "platform:" + p.ID + ":3:" + meterHour.Format(time.RFC3339) + ":compute_vcpu_hours"
	second := "platform:" + p.ID + ":3:" + meterHour.Add(time.Hour).Format(time.RFC3339) + ":compute_vcpu_hours"
	m.mu.Lock()
	defer m.mu.Unlock()
	if q := m.byID[first].Quantity; !near(q, 2.0/60) {
		t.Fatalf("first hour %v", q)
	}
	if q := m.byID[second].Quantity; !near(q, 2.0/60) {
		t.Fatalf("second hour %v", q)
	}
}

func TestComputeSkipsStoppedPlacements(t *testing.T) {
	e, m := meterEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	p := e.running(t, "web")
	if err := e.svc.Stopped(ctx, "org_a", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SampleCompute(ctx, meterHour.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SampleCompute(ctx, meterHour.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(m.ids()) != 0 {
		t.Fatalf("events for a stopped placement: %v", m.ids())
	}
}

func usageFrom(samples *[]fc.UsageSample) func(context.Context, string) ([]fc.UsageSample, error) {
	return func(context.Context, string) ([]fc.UsageSample, error) { return *samples, nil }
}

func sample(p *Placement, counter string, bytes uint64) fc.UsageSample {
	return fc.UsageSample{Scope: p.Scope(), VMID: "vm-secret-" + p.ID, CounterID: counter, Bytes: bytes}
}

func TestEgressCounterReset(t *testing.T) {
	e, m := meterEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	p := e.placement(t, e.running(t, "web").ID)
	var samples []fc.UsageSample
	e.svc.usage = usageFrom(&samples)

	for i, step := range []struct {
		counter string
		bytes   uint64
	}{{"c1", 100}, {"c1", 250}, {"c2", 40}} {
		samples = []fc.UsageSample{sample(p, step.counter, step.bytes)}
		if err := e.svc.SampleEgress(ctx, meterHour.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	// A restart re-reads the same counter and adds nothing.
	if err := e.restarted(m).SampleEgress(ctx, meterHour.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SampleEgress(ctx, meterHour.Add(time.Hour+emitDelay)); err != nil {
		t.Fatal(err)
	}
	if got, want := m.total("egress_gb"), 290/1e9; !near(got, want) {
		t.Fatalf("egress = %v, want %v", got, want)
	}
	if ids := m.ids(); len(ids) != 1 || ids[0] != "platform-egress:"+p.ID+":"+meterHour.Format(time.RFC3339) {
		t.Fatalf("ids %v", ids)
	}
}

func TestEgressCounterGoingBackwardsIsAReset(t *testing.T) {
	e, m := meterEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	p := e.placement(t, e.running(t, "web").ID)
	var samples []fc.UsageSample
	e.svc.usage = usageFrom(&samples)
	for i, b := range []uint64{500, 30} {
		samples = []fc.UsageSample{sample(p, "c1", b)}
		if err := e.svc.SampleEgress(ctx, meterHour.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.SampleEgress(ctx, meterHour.Add(time.Hour+emitDelay)); err != nil {
		t.Fatal(err)
	}
	if got, want := m.total("egress_gb"), 530/1e9; !near(got, want) {
		t.Fatalf("egress = %v, want %v", got, want)
	}
}

func TestEgressIgnoresUnknownScopes(t *testing.T) {
	e, m := meterEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	p := e.running(t, "web")
	other := *p
	other.InstanceID = "inst_nobody"
	otherOrg := *p
	otherOrg.OrgID = "org_b"
	samples := []fc.UsageSample{sample(&other, "c", 999), sample(&otherOrg, "c", 999)}
	e.svc.usage = usageFrom(&samples)
	if err := e.svc.SampleEgress(ctx, meterHour); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SampleEgress(ctx, meterHour.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(m.ids()) != 0 {
		t.Fatalf("events for unknown scopes: %v", m.ids())
	}
}

func TestBuildMinutesRoundUp(t *testing.T) {
	e, m := meterEnv(t)
	p := Placement{ID: "plc_build", OrgID: "org_a", Kind: "build"}
	start := meterHour
	if err := e.svc.RecordBuild(t.Context(), p, start, start.Add(61*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RecordBuild(t.Context(), p, start, start.Add(61*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := m.total("build_minutes"); got != 2 {
		t.Fatalf("build minutes = %v, want 2", got)
	}
	if ids := m.ids(); len(ids) != 1 || ids[0] != "platform-build:plc_build" {
		t.Fatalf("ids %v", ids)
	}
	e2 := Placement{ID: "plc_exact", OrgID: "org_a"}
	if err := e.svc.RecordBuild(t.Context(), e2, start, start.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	q := m.byID["platform-build:plc_exact"].Quantity
	m.mu.Unlock()
	if q != 2 {
		t.Fatalf("exact two minutes = %v", q)
	}
}

func TestOnlyPlatformEventsAreMarked(t *testing.T) {
	e, m := meterEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	p := e.placement(t, e.running(t, "web").ID)
	samples := []fc.UsageSample{sample(p, "c", 5000)}
	e.svc.usage = usageFrom(&samples)
	if err := e.svc.SampleCompute(ctx, meterHour.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SampleEgress(ctx, meterHour.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RecordBuild(ctx, Placement{ID: "plc_b", OrgID: "org_a"}, meterHour, meterHour.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	later := meterHour.Add(time.Hour + 5*time.Minute)
	if err := e.svc.SampleCompute(ctx, later); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SampleEgress(ctx, later); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Two compute features, one egress and one build event.
	if len(m.byID) != 4 {
		t.Fatalf("events %d, want 4", len(m.byID))
	}
	for id, ev := range m.byID {
		if !ev.PlatformOwned {
			t.Errorf("%s is not platform owned", id)
		}
		if ev.OrgID != "org_a" {
			t.Errorf("%s org %q", id, ev.OrgID)
		}
		if ev.ResourceRef != p.ID && ev.ResourceRef != "plc_b" {
			t.Errorf("%s resource ref %q", id, ev.ResourceRef)
		}
	}
}

func closeHours(t *testing.T, e *placerEnv, placementID string, at time.Time) {
	t.Helper()
	if err := e.svc.Stopped(t.Context(), "org_a", placementID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SampleCompute(t.Context(), at); err != nil {
		t.Fatal(err)
	}
}

func TestSuspendedMinutesAreNotBilled(t *testing.T) {
	e, m := meterEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	p := e.running(t, "web")
	for i := 1; i <= 3; i++ {
		if err := e.svc.SampleCompute(ctx, meterHour.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.set(meterHour.Add(3 * time.Minute))
	if err := e.svc.Suspend(ctx, "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SampleCompute(ctx, meterHour.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	e.clock.set(meterHour.Add(33 * time.Minute))
	if err := e.svc.Restore(ctx, "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	if got := e.placement(t, p.ID); got.State != "running" || got.Generation != 3 {
		t.Fatalf("placement %+v", got)
	}
	if err := e.svc.SampleCompute(ctx, meterHour.Add(34*time.Minute)); err != nil {
		t.Fatal(err)
	}
	closeHours(t, e, p.ID, meterHour.Add(2*time.Hour))
	if got, want := m.total("compute_vcpu_hours"), 4.0/60; !near(got, want) {
		t.Fatalf("vcpu hours = %v, want 4 minutes (the 30 suspended minutes are not billed)", got)
	}
}

func TestSamplerOutageWhileRunningIsBilledInFull(t *testing.T) {
	e, m := meterEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	p := e.running(t, "web")
	for _, at := range []time.Duration{time.Minute, 61 * time.Minute} {
		if err := e.svc.SampleCompute(ctx, meterHour.Add(at)); err != nil {
			t.Fatal(err)
		}
	}
	closeHours(t, e, p.ID, meterHour.Add(3*time.Hour))
	if got, want := m.total("compute_vcpu_hours"), 61.0/60; !near(got, want) {
		t.Fatalf("vcpu hours = %v, want 61 minutes", got)
	}
}

type logBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *logBuffer) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(b)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func TestCatchUpPastTheLimitIsLogged(t *testing.T) {
	e, m := meterEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	p := e.running(t, "web")
	logs := &logBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	defer slog.SetDefault(old)
	if err := e.svc.SampleCompute(ctx, meterHour.Add(30*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if out := logs.String(); !strings.Contains(out, "dropped") || !strings.Contains(out, p.ID) || !strings.Contains(out, "minutes=360") {
		t.Fatalf("no log of the dropped minutes:\n%s", out)
	}
	closeHours(t, e, p.ID, meterHour.Add(40*time.Hour))
	if got, want := m.total("compute_vcpu_hours"), 24.0; !near(got, want) {
		t.Fatalf("vcpu hours = %v, want 24h", got)
	}
}

// hookMeter runs hook once, on the first event it records.
type hookMeter struct {
	*recordingMeter
	once sync.Once
	hook func()
}

func (h *hookMeter) Record(ctx context.Context, e metering.Event) error {
	h.once.Do(h.hook)
	return h.recordingMeter.Record(ctx, e)
}

func TestEmitDoesNotFreezeAPartialHour(t *testing.T) {
	e, rec := meterEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	p := e.placement(t, e.running(t, "web").ID)
	for i := 1; i <= 3; i++ {
		if err := e.svc.SampleCompute(ctx, meterHour.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	e.svc.WithMeter(&hookMeter{recordingMeter: rec, hook: func() {
		// A count for the same hour arrives while the hour is being emitted.
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.svc.countMinutes(ctx, *p, meterHour.Add(30*time.Minute)); err != nil {
				t.Error(err)
			}
		}()
		time.Sleep(300 * time.Millisecond)
	}})
	if err := e.svc.emitComputeHours(ctx, meterHour.Add(time.Hour+5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	var minutes int
	if err := e.pg.QueryRow(ctx, `SELECT minutes FROM loomplane_platform_meter_compute WHERE placement_id=$1 AND hour=$2`, p.ID, meterHour).Scan(&minutes); err != nil {
		t.Fatal(err)
	}
	if got, want := rec.total("compute_vcpu_hours"), float64(minutes)/60; !near(got, want) {
		t.Fatalf("emitted %v, but the hour's final total is %d minutes", got, minutes)
	}
}

func TestConcurrentEgressSamplersNeverReadAFalseReset(t *testing.T) {
	e, m := meterEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	p := e.placement(t, e.running(t, "web").ID)
	var mu sync.Mutex
	counter := uint64(0)
	e.svc.usage = func(context.Context, string) ([]fc.UsageSample, error) {
		mu.Lock()
		defer mu.Unlock()
		counter += 100
		return []fc.UsageSample{sample(p, "c1", counter)}, nil
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.svc.SampleEgress(ctx, meterHour.Add(time.Minute)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := e.svc.SampleEgress(ctx, meterHour.Add(time.Hour+emitDelay)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	final := counter
	mu.Unlock()
	if got, want := m.total("egress_gb"), float64(final-100)/1e9; !near(got, want) {
		// The last sample ran in the next hour, so its 100 bytes are not in the first.
		t.Fatalf("egress = %v, want %v", got, want)
	}
}

func TestHungHostDoesNotStallLaterHosts(t *testing.T) {
	e, m := meterEnv(t)
	ctx := t.Context()
	hung := e.readyHost(t, "a", bigAllotment)
	e.readyHost(t, "b", bigAllotment)
	p := e.placement(t, e.running(t, "web").ID)
	e.svc.usageTimeout = 200 * time.Millisecond
	e.svc.usage = func(ctx context.Context, hostID string) ([]fc.UsageSample, error) {
		if hostID == hung.ID {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []fc.UsageSample{sample(p, "c1", 4000)}, nil
	}
	start := time.Now()
	err := e.svc.SampleEgress(ctx, meterHour.Add(time.Minute))
	if err == nil {
		t.Fatal("the hung host's failure was swallowed")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("SampleEgress took %v", took)
	}
	if err = e.svc.SampleEgress(ctx, meterHour.Add(time.Hour+emitDelay)); err == nil {
		t.Fatal("expected the hung host to fail again")
	}
	if got, want := m.total("egress_gb"), 4000/1e9; !near(got, want) {
		t.Fatalf("egress = %v, want %v from the host that answered", got, want)
	}
}

func TestRunningWithANewGenerationRestartsTheBillingWindow(t *testing.T) {
	e, _ := meterEnv(t)
	e.readyHost(t, "a", bigAllotment)
	p := e.running(t, "web") // generation 3, since 10:00
	e.clock.set(meterHour.Add(20 * time.Minute))
	if err := e.svc.Running(t.Context(), "org_a", p.ID, "inst_web", 3); err != nil {
		t.Fatal(err)
	}
	since := func() time.Time {
		var at time.Time
		if err := e.pg.QueryRow(t.Context(), `SELECT running_since FROM loomplane_platform_placements WHERE id=$1`, p.ID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at.UTC()
	}
	if got := since(); !got.Equal(meterHour) {
		t.Fatalf("same generation moved running_since to %v", got)
	}
	if err := e.svc.Running(t.Context(), "org_a", p.ID, "inst_web", 4); err != nil {
		t.Fatal(err)
	}
	if got := since(); !got.Equal(meterHour.Add(20 * time.Minute)) {
		t.Fatalf("new generation left running_since at %v", got)
	}
	if err := e.svc.Stopped(t.Context(), "org_a", p.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.set(meterHour.Add(50 * time.Minute))
	if err := e.svc.Running(t.Context(), "org_a", p.ID, "inst_web", 4); err != nil {
		t.Fatal(err)
	}
	if got := since(); !got.Equal(meterHour.Add(50 * time.Minute)) {
		t.Fatalf("running again after a stop left running_since at %v", got)
	}
}
