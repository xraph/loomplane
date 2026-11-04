package agentwatch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releasehealth"
)

// fakeReader answers the current window with current and the hour before
// it with baseline, and records every read.
type fakeReader struct {
	current, baseline releasehealth.Health
	reads             []read
}

type read struct {
	bindingID string
	w         releasehealth.Window
}

func (r *fakeReader) Health(_ context.Context, _, bindingID, revision string, w releasehealth.Window) (releasehealth.Health, error) {
	if revision != "" {
		return releasehealth.Health{}, fmt.Errorf("the sweep read revision %q, not the whole service", revision)
	}
	r.reads = append(r.reads, read{bindingID, w})
	if w.End.Sub(w.Start) == BaselineWindow {
		return r.baseline, nil
	}
	return r.current, nil
}

func (r *fakeReader) readBindings() []string {
	var out []string
	for _, rd := range r.reads {
		if !slices.Contains(out, rd.bindingID) {
			out = append(out, rd.bindingID)
		}
	}
	return out
}

func health(requests, errors float64) releasehealth.Health {
	return releasehealth.Health{Requests: requests, Errors: errors, LatencyMS: 100, Statistic: "p95", Available: true}
}

func productionBinding(id, environmentID, provider string) *planning.Binding {
	return &planning.Binding{
		ID: id, OrgID: org, ProjectID: project, EnvironmentID: environmentID, Subject: planning.ServiceSubject,
		Name: "api", Provider: provider, UpdatedAt: t0,
	}
}

type sweeper struct {
	t      *testing.T
	world  *fakeWorld
	reader *fakeReader
	now    time.Time
	checks map[string]Check
}

func newSweeper(t *testing.T, bindings ...*planning.Binding) *sweeper {
	w := newWorld()
	w.production = bindings
	return &sweeper{
		t: t, world: w, now: t0,
		reader: &fakeReader{baseline: health(5000, 10), current: health(3000, 6)},
		checks: map[string]Check{},
	}
}

func (s *sweeper) sweep() ([]agentreports.Signal, []agentreports.Clearance) {
	s.t.Helper()
	d := &Detector{World: s.world, Health: s.reader, Now: func() time.Time { return s.now }}
	sigs, clears, checks, err := d.Sweep(s.t.Context(), org, s.checks)
	if err != nil {
		s.t.Fatalf("Sweep: %v", err)
	}
	s.checks = checks
	return sigs, clears
}

func ofKind(sigs []agentreports.Signal, kind string) []agentreports.Signal {
	var out []agentreports.Signal
	for _, s := range sigs {
		if s.Kind == kind {
			out = append(out, s)
		}
	}
	return out
}

func clearing(clears []agentreports.Clearance, kind string) []agentreports.Clearance {
	var out []agentreports.Clearance
	for _, c := range clears {
		if slices.Contains(c.Signals, kind) {
			out = append(out, c)
		}
	}
	return out
}

func TestErrorRateJumpFires(t *testing.T) {
	s := newSweeper(t, productionBinding("b_1", "env_prod", "cloud-run"))
	s.reader.current = health(3000, 120)
	sigs, _ := s.sweep()
	got := ofKind(sigs, agentreports.SignalErrorRate)
	if len(got) != 1 {
		t.Fatalf("signals = %+v", sigs)
	}
	sig := got[0]
	if sig.Title != "api's error rate jumped to 4.0% on production" || sig.Severity != agentreports.SeverityHigh ||
		sig.Component != "api" || sig.EnvironmentID != "env_prod" || sig.Environment != "production" || sig.Occurrence != "" ||
		sig.Evidence.BindingID != "b_1" || sig.Repository != "acme/app" {
		t.Fatalf("signal = %+v", sig)
	}
	for label, want := range map[string]string{
		"Requests": "3000", "Errors": "120", "Error rate now": "4.0%", "Error rate before": "0.2%",
	} {
		if f := fact(t, sig, label); f.Value != want {
			t.Errorf("%s = %q, want %q", label, f.Value, want)
		}
	}
	fact(t, sig, "Threshold")
	if c := s.checks["b_1"]; !c.Failing || !c.CheckedAt.Equal(s.now) {
		t.Errorf("check = %+v", c)
	}
}

func TestQuietServicesNeverFire(t *testing.T) {
	s := newSweeper(t, productionBinding("b_1", "env_prod", "cloud-run"))
	s.reader.current = health(50, 25)
	sigs, clears := s.sweep()
	if len(ofKind(sigs, agentreports.SignalErrorRate))+len(clearing(clears, agentreports.SignalErrorRate)) != 0 {
		t.Fatalf("signals %+v, clearances %+v", sigs, clears)
	}
	if c := s.checks["b_1"]; c.Failing || c.Passes != 0 {
		t.Errorf("check = %+v", c)
	}
	// Unavailable metrics change nothing either, even while failing.
	s.checks["b_1"] = Check{Failing: true, Passes: 1}
	s.reader.current = releasehealth.Health{Reason: "no metrics yet"}
	s.now = s.now.Add(SweepEvery)
	s.sweep()
	if c := s.checks["b_1"]; !c.Failing || c.Passes != 1 {
		t.Errorf("unavailable changed the check: %+v", c)
	}
}

func TestErrorRateClearsAfterTwoPasses(t *testing.T) {
	s := newSweeper(t, productionBinding("b_1", "env_prod", "cloud-run"))
	s.reader.current = health(3000, 120)
	s.sweep()

	s.reader.current = health(3000, 6)
	s.now = s.now.Add(SweepEvery)
	if _, clears := s.sweep(); len(clearing(clears, agentreports.SignalErrorRate)) != 0 {
		t.Fatalf("one pass cleared: %+v", clears)
	}
	if c := s.checks["b_1"]; !c.Failing || c.Passes != 1 {
		t.Fatalf("after one pass: %+v", c)
	}

	s.now = s.now.Add(SweepEvery)
	_, clears := s.sweep()
	got := clearing(clears, agentreports.SignalErrorRate)
	if len(got) != 1 {
		t.Fatalf("clearances = %+v", clears)
	}
	if c := got[0]; !slices.Equal(c.Signals, []string{agentreports.SignalErrorRate}) || !slices.Equal(c.Components, []string{"api"}) ||
		c.EnvironmentID != "env_prod" || c.ProjectID != project || c.Reason == "" || c.At.After(s.now.Add(-releasehealth.MetricsLag)) {
		t.Fatalf("clearance = %+v", c)
	}
	if c := s.checks["b_1"]; c.Failing || c.Passes != 0 {
		t.Errorf("after clearing: %+v", c)
	}
}

func TestSweepSkipsCanariesAndWatches(t *testing.T) {
	s := newSweeper(t,
		productionBinding("b_canary", "env_eu", "cloud-run"),
		productionBinding("b_watch", "env_us", "cloud-run"),
		productionBinding("b_quiet", "env_prod", "cloud-run"),
	)
	s.world.envs["env_eu"] = &projects.Environment{ID: "env_eu", ProjectID: project, Name: "production-eu", Kind: projects.KindProduction}
	s.world.envs["env_us"] = &projects.Environment{ID: "env_us", ProjectID: project, Name: "production-us", Kind: projects.KindProduction}
	s.world.ramping["env_eu"] = "dep_9"
	s.world.watching["production-us"] = true
	s.sweep()
	if got := s.reader.readBindings(); !slices.Equal(got, []string{"b_quiet"}) {
		t.Fatalf("read %v", got)
	}
	for _, id := range []string{"b_canary", "b_watch"} {
		if !s.checks[id].CheckedAt.Equal(s.now) {
			t.Errorf("%s wasn't checked for drift: %+v", id, s.checks[id])
		}
	}
}

func TestSweepSkipsProvidersWithoutErrorCounts(t *testing.T) {
	b := productionBinding("b_k8s", "env_prod", "kubernetes")
	b.Drift, b.DriftMessage = planning.DriftChanged, "another image is live"
	resource := productionBinding("b_db", "env_prod", "cloud-run")
	resource.Subject, resource.Name = planning.ResourceSubject, "db"
	s := newSweeper(t, b, resource)
	sigs, clears := s.sweep()
	if len(s.reader.reads) != 0 {
		t.Fatalf("read %v", s.reader.readBindings())
	}
	drift := ofKind(sigs, agentreports.SignalDrift)
	if len(drift) != 1 || drift[0].Component != "api" || drift[0].Title != "api drifted on production: changed outside Loomplane" {
		t.Fatalf("drift = %+v", sigs)
	}
	// The database matched, which clears its drift.
	got := clearing(clears, agentreports.SignalDrift)
	if len(got) != 1 || !slices.Equal(got[0].Components, []string{"db"}) || got[0].EnvironmentID != "env_prod" {
		t.Fatalf("clearances = %+v", clears)
	}
}

func TestSweepIsBoundedAndRoundRobin(t *testing.T) {
	bindings := make([]*planning.Binding, 0, 120)
	for i := range 120 {
		bindings = append(bindings, productionBinding(fmt.Sprintf("b_%03d", i), "env_prod", "kubernetes"))
	}
	s := newSweeper(t, bindings...)
	checked := func() []string {
		var out []string
		for _, b := range bindings {
			if s.checks[b.ID].CheckedAt.Equal(s.now) {
				out = append(out, b.ID)
			}
		}
		return out
	}
	s.sweep()
	first := checked()
	if len(first) != MaxBindingsPerSweep || first[0] != "b_000" || first[49] != "b_049" {
		t.Fatalf("first sweep checked %d: %v", len(first), first)
	}
	s.now = s.now.Add(time.Minute)
	s.sweep()
	second := checked()
	if len(second) != MaxBindingsPerSweep || second[0] != "b_050" || second[49] != "b_099" {
		t.Fatalf("second sweep checked %d: %v", len(second), second)
	}
	// The rest, then nothing: everything was checked within SweepEvery.
	s.now = s.now.Add(time.Minute)
	s.sweep()
	if third := checked(); len(third) != 20 || third[0] != "b_100" {
		t.Fatalf("third sweep checked %d", len(third))
	}
	s.now = s.now.Add(time.Minute)
	s.sweep()
	if fourth := checked(); len(fourth) != 0 {
		t.Fatalf("fourth sweep checked %d", len(fourth))
	}
	// Once SweepEvery passed, the least recently checked go first.
	s.now = t0.Add(SweepEvery)
	s.sweep()
	if fifth := checked(); len(fifth) != MaxBindingsPerSweep || fifth[0] != "b_000" {
		t.Fatalf("fifth sweep checked %d", len(fifth))
	}

	// A binding that's no longer in production drops out of the checks.
	s.world.production = bindings[:3]
	s.sweep()
	if len(s.checks) != 3 {
		t.Fatalf("checks kept %d bindings", len(s.checks))
	}
}

func TestSweepWindowsEndBeforeTheLag(t *testing.T) {
	s := newSweeper(t, productionBinding("b_1", "env_prod", "cloud-run"))
	s.now = t0.Add(17*time.Minute + 42*time.Second)
	s.sweep()
	if len(s.reader.reads) != 2 {
		t.Fatalf("reads = %+v", s.reader.reads)
	}
	var current, baseline releasehealth.Window
	for _, rd := range s.reader.reads {
		if rd.w.End.After(s.now.Add(-releasehealth.MetricsLag)) {
			t.Errorf("window %+v ends after now - MetricsLag", rd.w)
		}
		if rd.w.End.Sub(rd.w.Start) == BaselineWindow {
			baseline = rd.w
		} else {
			current = rd.w
		}
	}
	if current.End.Sub(current.Start) != ErrorWindow {
		t.Errorf("current window = %+v", current)
	}
	if !baseline.End.Equal(current.Start) {
		t.Errorf("baseline %+v doesn't end where current %+v starts", baseline, current)
	}
}

func TestSweepReadsAProjectsRepositoriesOnce(t *testing.T) {
	bindings := make([]*planning.Binding, 0, 4)
	for i := range 4 {
		b := driftedBinding(fmt.Sprintf("b_%d", i), "env_prod", planning.DriftChanged)
		b.Name = fmt.Sprintf("svc%d", i)
		b.Provider = "fly" // no error counts: drift alone
		bindings = append(bindings, b)
	}
	s := newSweeper(t, bindings...)
	sigs, _ := s.sweep()
	drift := ofKind(sigs, agentreports.SignalDrift)
	if len(drift) != 4 {
		t.Fatalf("drift signals = %d, want 4", len(drift))
	}
	for _, sig := range drift {
		if sig.Repository != "acme/app" {
			t.Errorf("%s repository = %q, want acme/app", sig.Component, sig.Repository)
		}
	}
	// One Sources read and one ProjectBindings read for the project.
	if s.world.sourceReads != 2 {
		t.Errorf("four drifted bindings of one project made %d repository reads, want 2", s.world.sourceReads)
	}
}

func TestSweepGoesOnPastAProjectItCantRead(t *testing.T) {
	s := newSweeper(t, productionBinding("b_1", "env_prod", "cloud-run"))
	s.checks = map[string]Check{"b_other": {CheckedAt: t0.Add(-time.Hour), Failing: true}}
	s.world.skipped = &SkippedProjectsError{Errs: map[string]error{"prj_2": errors.New("the database is down")}}
	s.reader.current = health(3000, 300)
	var warned []string
	d := &Detector{World: s.world, Health: s.reader, Now: func() time.Time { return s.now },
		Warn: func(msg string, _ error) { warned = append(warned, msg) }}
	sigs, _, checks, err := d.Sweep(t.Context(), org, s.checks)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(ofKind(sigs, agentreports.SignalErrorRate)) != 1 {
		t.Errorf("the readable project's failing binding didn't fire: %+v", sigs)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "prj_2") {
		t.Errorf("warned = %q, want one warning naming prj_2", warned)
	}
	// The skipped project's check isn't known to have left production.
	if c, ok := checks["b_other"]; !ok || !c.Failing {
		t.Errorf("the skipped project's check = %+v, %v; want it kept", c, ok)
	}
}
