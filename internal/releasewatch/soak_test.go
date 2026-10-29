package releasewatch

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/security"
)

var soakNow = time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC)

var soakPins = []Pin{{SourceID: "src_web", Name: "web", CommitSHA: "aaa"}, {SourceID: "src_api", Name: "api", CommitSHA: "bbb"}}

// soakRig is staging running web@aaa (deployed 09:00) and api@bbb (10:30),
// with a web service built from aaa and an api service from bbb.
func soakRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t, "")
	r.clock.now = soakNow
	r.plans.deployed = map[string][]*planning.DeployedSource{"env_s": {
		{EnvironmentID: "env_s", SourceID: "src_web", CommitSHA: "aaa", DeployedAt: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)},
		{EnvironmentID: "env_s", SourceID: "src_api", CommitSHA: "bbb", DeployedAt: time.Date(2026, 11, 2, 10, 30, 0, 0, time.UTC)},
	}}
	r.plans.bindings["env_s"] = []*planning.Binding{
		{ID: "bind_web", Subject: planning.ServiceSubject, Name: "web", Provider: "cloud-run", CommitSHA: "aaa"},
		{ID: "bind_api", Subject: planning.ServiceSubject, Name: "api", Provider: "cloud-run", CommitSHA: "bbb"},
		{ID: "bind_old", Subject: planning.ServiceSubject, Name: "old", Provider: "cloud-run", CommitSHA: "zzz"},
		{ID: "bind_db", Subject: planning.ResourceSubject, Name: "db", Provider: "cloud-run", CommitSHA: "aaa"},
	}
	return r
}

func (r *rig) soak(t *testing.T, pins []Pin) *Soak {
	t.Helper()
	s, err := r.svc.Soak(t.Context(), "org", "env_s", "staging", pins, releasehealth.DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSoak_measuresSinceTheEnvironmentRanExactlyTheseCommits(t *testing.T) {
	r := soakRig(t)
	s := r.soak(t, soakPins)
	since := time.Date(2026, 11, 2, 10, 30, 0, 0, time.UTC)
	if !s.Running || !s.Since.Equal(since) || s.Duration != 2*time.Hour+30*time.Minute || !s.Healthy || s.Unverified || len(s.Reasons) != 0 {
		t.Fatalf("soak = %+v", s)
	}
	if len(s.Services) != 2 || s.Services[0].Name != "api" || s.Services[1].Name != "web" {
		t.Fatalf("services = %+v", s.Services)
	}
	for _, svc := range s.Services {
		if svc.Verdict != releasehealth.Pass {
			t.Fatalf("%s verdict %s: %s", svc.Name, svc.Verdict, svc.Reason)
		}
	}
	w, err := metrics.ShortWindow(since, soakNow.Add(-releasehealth.MetricsLag))
	if err != nil {
		t.Fatal(err)
	}
	want := releasehealth.Window{Start: w.Start, End: w.End}
	calls := r.reader.calls()
	if len(calls) != 2 {
		t.Fatalf("reads = %+v", calls)
	}
	for _, c := range calls {
		if c.org != "org" || c.revision != "" || c.w != want || (c.binding != "bind_web" && c.binding != "bind_api") {
			t.Fatalf("read = %+v, want window %+v", c, want)
		}
	}
}

func TestSoak_aSoakOverADayIsJudgedOverItsLastDay(t *testing.T) {
	r := soakRig(t)
	r.plans.deployed["env_s"][1].DeployedAt = soakNow.Add(-72 * time.Hour)
	r.plans.deployed["env_s"][0].DeployedAt = soakNow.Add(-80 * time.Hour)
	s := r.soak(t, soakPins)
	if !s.Healthy || s.Duration != 72*time.Hour {
		t.Fatalf("soak = %+v", s)
	}
	for _, c := range r.reader.calls() {
		if c.w.End.Sub(c.w.Start) != 24*time.Hour {
			t.Fatalf("window = %+v", c.w)
		}
	}
}

func TestSoak_notRunningWhenAnyCommitDiffers(t *testing.T) {
	r := soakRig(t)
	r.plans.deployed["env_s"][1].CommitSHA = "ccc"
	s := r.soak(t, soakPins)
	if s.Running || s.Duration != 0 || s.Healthy || s.Unverified || !slices.Equal(s.Reasons, []string{"staging runs api@ccc, not bbb"}) {
		t.Fatalf("soak = %+v", s)
	}
	if len(r.reader.calls()) != 0 {
		t.Fatal("a soak that isn't running reads no metrics")
	}
	r.plans.deployed["env_s"] = r.plans.deployed["env_s"][:1]
	s = r.soak(t, soakPins)
	if s.Running || !slices.Equal(s.Reasons, []string{"staging has no api deployed"}) {
		t.Fatalf("missing source: %+v", s)
	}
}

func TestSoak_usesAbsoluteThresholdsOnly(t *testing.T) {
	r := soakRig(t)
	// The ceiling is 1%: 2% fails however much better than a baseline.
	r.reader.answer = func(binding string, _ releasehealth.Window) (releasehealth.Health, error) {
		h := healthy()
		if binding == "bind_api" {
			h.Requests, h.Errors = 1000, 20
		}
		return h, nil
	}
	s := r.soak(t, soakPins)
	if s.Healthy || s.Unverified || len(s.Reasons) != 1 {
		t.Fatalf("soak = %+v", s)
	}
	want := "api: 2.0% of the candidate's requests failed; the ceiling is 1%."
	if s.Reasons[0] != want {
		t.Fatalf("reason %q, want %q", s.Reasons[0], want)
	}
	if s.Services[0].Verdict != releasehealth.Fail {
		t.Fatalf("verdict = %s", s.Services[0].Verdict)
	}
	// A latency that would breach a ratio against a baseline doesn't matter.
	r.reader.answer = func(string, releasehealth.Window) (releasehealth.Health, error) {
		h := healthy()
		h.LatencyMS = 100000
		return h, nil
	}
	if s = r.soak(t, soakPins); !s.Healthy {
		t.Fatalf("latency alone must not fail a soak: %+v", s)
	}
}

func TestSoak_lowTrafficIsNotHealthy(t *testing.T) {
	r := soakRig(t)
	r.reader.answer = func(binding string, _ releasehealth.Window) (releasehealth.Health, error) {
		h := healthy()
		if binding == "bind_web" {
			h.Requests, h.Errors = 50, 0
		}
		return h, nil
	}
	s := r.soak(t, soakPins)
	if s.Healthy || !s.Unverified || len(s.Reasons) != 1 || s.Reasons[0] != "web: The candidate served 50 requests; at least 200 are needed to judge it." {
		t.Fatalf("soak = %+v", s)
	}
	// A failing service beside a quiet one is still unverified: the quiet one has no evidence.
	// A failing service alone is evidence, and bad.
	r.reader.answer = func(string, releasehealth.Window) (releasehealth.Health, error) {
		h := healthy()
		h.Errors = 500
		return h, nil
	}
	if s = r.soak(t, soakPins); s.Healthy || s.Unverified {
		t.Fatalf("a failure is evidence: %+v", s)
	}
}

func TestSoak_metricsUnavailableIsNotHealthy(t *testing.T) {
	r := soakRig(t)
	r.reader.answer = func(binding string, _ releasehealth.Window) (releasehealth.Health, error) {
		if binding == "bind_api" {
			return releasehealth.Health{Reason: "CloudWatch isn't connected"}, nil
		}
		return healthy(), nil
	}
	s := r.soak(t, soakPins)
	if s.Healthy || !s.Unverified || !slices.Equal(s.Reasons, []string{"api: CloudWatch isn't connected"}) {
		t.Fatalf("soak = %+v", s)
	}

	r = soakRig(t)
	r.plans.bindings["env_s"][1].Provider = "vercel"
	s = r.soak(t, soakPins)
	ok, reason := Watchable("vercel")
	if ok || s.Healthy || !s.Unverified || len(s.Reasons) != 1 || s.Reasons[0] != "api: "+reason {
		t.Fatalf("unwatched: %+v (%q)", s, reason)
	}
	for _, c := range r.reader.calls() {
		if c.binding == "bind_api" {
			t.Fatal("a service that isn't watched isn't read")
		}
	}

	r = soakRig(t)
	r.reader.answer = func(string, releasehealth.Window) (releasehealth.Health, error) {
		return releasehealth.Health{}, errors.New("monitoring timed out")
	}
	if s = r.soak(t, soakPins); s.Healthy || !s.Unverified || s.Reasons[0] != "api: monitoring timed out" {
		t.Fatalf("reader error: %+v", s)
	}
}

func TestSoak_tooShortToJudge(t *testing.T) {
	r := soakRig(t)
	for _, d := range r.plans.deployed["env_s"] {
		d.DeployedAt = time.Date(2026, 11, 2, 12, 58, 0, 0, time.UTC)
	}
	s := r.soak(t, soakPins)
	if !s.Running || s.Duration != 2*time.Minute || s.Healthy || !slices.Equal(s.Reasons, []string{"staging has run these commits for less than the metrics lag (3m)"}) {
		t.Fatalf("soak = %+v", s)
	}
	if len(r.reader.calls()) != 0 {
		t.Fatal("too short to read")
	}
}

func TestSoakForPlan(t *testing.T) {
	r := soakRig(t)
	r.plans.put(&planning.Plan{
		ID: "plan_p", OrgID: "org", ProjectID: "proj", EnvironmentID: "env_p", Environment: "production",
		Kind: planning.KindPromotion, From: "staging", FromEnvironmentID: "env_s",
		Sources: []planning.PlanSource{
			{SourceID: "src_web", Alias: "web", CommitSHA: "aaa", InScope: true},
			{SourceID: "src_api", Alias: "api", CommitSHA: "bbb", InScope: true},
			{SourceID: "src_docs", Alias: "docs", CommitSHA: "ddd"},
		},
		Release: &planning.Release{Thresholds: releasehealth.Thresholds{ErrorRate: 0.001, MinRequests: 100}},
	})
	r.reader.answer = func(string, releasehealth.Window) (releasehealth.Health, error) {
		return releasehealth.Health{Requests: 1000, Errors: 2, Available: true}, nil // 0.2%
	}
	s, err := r.svc.SoakForPlan(t.Context(), ada, "plan_p")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Running || s.Environment != "staging" || s.Healthy || len(s.Reasons) != 2 {
		t.Fatalf("the plan's own ceiling (0.1%%) decides: %+v", s)
	}
	if got := r.authz.checks; len(got) != 1 || got[0] != security.DeploymentRead {
		t.Fatalf("checks = %v", got)
	}

	r.plans.put(&planning.Plan{ID: "plan_r", OrgID: "org", ProjectID: "proj", EnvironmentID: "env_p", Kind: planning.KindRelease})
	if _, err = r.svc.SoakForPlan(t.Context(), ada, "plan_r"); !errors.Is(err, planning.ErrInvalid) {
		t.Fatalf("a plan without From: %v", err)
	}
	if _, err = r.svc.SoakForPlan(t.Context(), security.SystemPrincipal("org"), "plan_p"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("system principal: %v", err)
	}
	r.authz.deny = true
	if _, err = r.svc.SoakForPlan(t.Context(), ada, "plan_p"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("denied: %v", err)
	}
}
