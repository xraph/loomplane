package releaseprep

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/repositories"
)

func TestCollectComparesProductionToStagingPins(t *testing.T) {
	w := greenWorld()
	ev := w.collect(nil)

	if want := []diffCall{{"org_a", "acme/api", shaA, shaB}}; !reflect.DeepEqual(w.diffs.calls, want) {
		t.Fatalf("compare calls %+v, want %+v (web is held, so it isn't compared)", w.diffs.calls, want)
	}
	if len(ev.Sources) != 1 {
		t.Fatalf("sources %+v, want api only", ev.Sources)
	}
	d := ev.Sources[0]
	if d.SourceID != "src_api" || d.Alias != "api" || d.Repository != "acme/api" || d.ProductionCommit != shaA || d.StagingCommit != shaB {
		t.Fatalf("diff %+v", d)
	}
	if d.Direction != DirectionAhead || d.Error != "" || len(d.Commits) != 1 || d.Commits[0].Title != "Add users" {
		t.Fatalf("diff built %+v", d)
	}
	if len(ev.Unavailable) != 0 {
		t.Fatalf("unavailable %v", ev.Unavailable)
	}
	if !ev.CollectedAt.Equal(collectedAt) || ev.PlanStatus != planning.StatusReady || !ev.PinsMatch {
		t.Fatalf("collected at %v, plan status %q, pins match %v", ev.CollectedAt, ev.PlanStatus, ev.PinsMatch)
	}
	if ev.Soak == nil || ev.StagingRelease == nil || ev.ProductionRelease == nil || ev.Coverage == nil || ev.Budget == nil {
		t.Fatalf("missing evidence %+v", ev)
	}
	if ev.StagingRelease.Environment != "staging" || ev.ProductionRelease.Environment != "production" {
		t.Fatalf("release evidence read for the wrong environments: %+v %+v", ev.StagingRelease, ev.ProductionRelease)
	}
	if ev.OpenReports == nil || ev.Canaries == nil || ev.Destroy == nil || ev.DataUpdates == nil || ev.PinDifferences == nil || ev.Problems == nil || ev.Warnings == nil {
		t.Fatalf("lists must be empty, never nil: %+v", ev)
	}
	if !reflect.DeepEqual(ev.Artifacts, w.plan.Artifacts) {
		t.Fatalf("artifacts %+v", ev.Artifacts)
	}
	// Open reports are read on staging, canaries on production.
	if !reflect.DeepEqual(w.reports.calls, [][3]string{{"org_a", "proj_a", "env_stg"}}) {
		t.Fatalf("open reports read with %v", w.reports.calls)
	}
	if w.canaries.env != "env_prd" || !slices.Equal(w.canaries.asked, []string{"service:api"}) {
		t.Fatalf("canaries read for %q, %v", w.canaries.env, w.canaries.asked)
	}
	if ev.ChangedServices[0].LastCanary != "dep_p1" || len(ev.Canaries) != 1 || ev.Canaries[0].DeploymentID != "dep_p1" {
		t.Fatalf("last canary %+v, canaries %+v", ev.ChangedServices, ev.Canaries)
	}
}

func TestCollectMarksNewAndIdenticalWithoutCompare(t *testing.T) {
	w := greenWorld()
	w.plan.Sources = []planning.PlanSource{
		{SourceID: "src_api", Alias: "api", Repository: "acme/api", CommitSHA: shaA, InScope: true},
		{SourceID: "src_jobs", Alias: "jobs", Repository: "acme/jobs", CommitSHA: shaC, InScope: true},
	}
	ev := w.collect(nil)

	if len(w.diffs.calls) != 0 {
		t.Fatalf("compared %+v", w.diffs.calls)
	}
	if len(ev.Sources) != 2 {
		t.Fatalf("sources %+v", ev.Sources)
	}
	if d := ev.Sources[0]; d.Alias != "api" || d.Direction != DirectionIdentical || d.Error != "" || d.ProductionCommit != shaA {
		t.Fatalf("identical %+v", d)
	}
	if d := ev.Sources[1]; d.Alias != "jobs" || d.Direction != DirectionNew || d.Error != "" || d.ProductionCommit != "" || d.StagingCommit != shaC {
		t.Fatalf("new %+v", d)
	}
	if _, ok := ev.Unavailable[KeyDiff]; ok {
		t.Fatalf("unavailable %v", ev.Unavailable)
	}
}

func TestCollectWarnsWhenProductionMovesBack(t *testing.T) {
	for _, status := range []string{DirectionBehind, DirectionDiverged} {
		t.Run(status, func(t *testing.T) {
			w := greenWorld()
			w.plan.Warnings = []planning.Finding{{Code: "newer-manifest", Message: "an older manifest"}}
			w.diffs.answer = &repositories.Comparison{Status: status, BehindBy: 2, AheadBy: 1, TotalCommits: 0}
			if status == DirectionBehind {
				w.diffs.answer.AheadBy = 0
			}
			ev := w.collect(nil)

			if len(ev.Warnings) != 2 || ev.Warnings[0].Code != "newer-manifest" {
				t.Fatalf("warnings %+v: the plan's come first", ev.Warnings)
			}
			got := ev.Warnings[1]
			if got.Code != WarningProductionMovesBack || got.Name != "api" || !strings.HasPrefix(got.Message, "api: staging runs bbbbbbb") ||
				!strings.Contains(got.Message, "production's aaaaaaa") {
				t.Fatalf("warning %+v", got)
			}
			// The plan itself isn't changed.
			if len(w.plan.Warnings) != 1 {
				t.Fatalf("plan warnings changed: %+v", w.plan.Warnings)
			}
		})
	}
}

func TestCollectRecordsUnavailableEvidence(t *testing.T) {
	cases := []struct {
		key     string
		breakIt func(w *world)
		check   func(t *testing.T, ev *ReadinessEvidence)
	}{
		{KeySoak, func(w *world) { w.watches.soakErr = errors.New("cloud monitoring is down") }, func(t *testing.T, ev *ReadinessEvidence) {
			if ev.Soak != nil {
				t.Fatal("soak kept")
			}
		}},
		{KeyStagingRelease, func(w *world) {
			w.watches.evidenceErr = map[string]error{"staging": errors.New("watch store down")}
		}, func(t *testing.T, ev *ReadinessEvidence) {
			if ev.StagingRelease != nil || ev.ProductionRelease == nil {
				t.Fatal("release evidence")
			}
		}},
		{KeyProductionRelease, func(w *world) {
			w.watches.evidenceErr = map[string]error{"production": errors.New("watch store down")}
		}, func(t *testing.T, ev *ReadinessEvidence) {
			if ev.ProductionRelease != nil || ev.StagingRelease == nil {
				t.Fatal("release evidence")
			}
		}},
		{KeyCoverage, func(w *world) { w.watches.coverageErr = errors.New("manifest store down") }, func(t *testing.T, ev *ReadinessEvidence) {
			if ev.Coverage != nil {
				t.Fatal("coverage kept")
			}
		}},
		{KeyCanaries, func(w *world) { w.canaries.err = deployments.ErrNoCanaries }, func(t *testing.T, ev *ReadinessEvidence) {
			if ev.Canaries != nil {
				t.Fatal("canaries must be nil when unread")
			}
		}},
		{KeyReports, func(w *world) { w.reports.err = errors.New("3c store down") }, func(t *testing.T, ev *ReadinessEvidence) {
			if ev.OpenReports != nil {
				t.Fatal("open reports must be nil when unread")
			}
		}},
		{KeyBudget, func(w *world) { w.budgets.err = errors.New("ledger down") }, func(t *testing.T, ev *ReadinessEvidence) {
			if ev.Budget != nil {
				t.Fatal("budget kept")
			}
		}},
		{KeyPins, func(w *world) { w.plans.pinsErr = errors.New("bindings unreadable") }, func(t *testing.T, ev *ReadinessEvidence) {
			if ev.PinsMatch {
				t.Fatal("pins can't match when unread")
			}
		}},
		{KeyDiff, func(w *world) { w.diffs.err = errors.New("GitHub answered 502") }, func(t *testing.T, ev *ReadinessEvidence) {
			if len(ev.Sources) != 1 || ev.Sources[0].Error == "" || ev.Sources[0].Direction != "" {
				t.Fatalf("source %+v", ev.Sources)
			}
		}},
		{KeyDiff + " (production unread)", func(w *world) { w.plans.deployedErr = errors.New("deployed sources down") }, func(t *testing.T, ev *ReadinessEvidence) {
			if len(ev.Sources) != 1 || ev.Sources[0].Error == "" {
				t.Fatalf("source %+v", ev.Sources)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			w := greenWorld()
			tc.breakIt(w)
			ev := w.collect(nil)

			key, _, _ := strings.Cut(tc.key, " ")
			if len(ev.Unavailable) != 1 || ev.Unavailable[key] == "" {
				t.Fatalf("unavailable %v, want only %s", ev.Unavailable, key)
			}
			tc.check(t, ev)
			// Every other piece is still read.
			if key != KeySoak && ev.Soak == nil || key != KeyCoverage && ev.Coverage == nil || key != KeyBudget && ev.Budget == nil ||
				key != KeyCanaries && len(ev.Canaries) != 1 || key != KeyReports && ev.OpenReports == nil ||
				key != KeyPins && !ev.PinsMatch || key != KeyDiff && (len(ev.Sources) != 1 || ev.Sources[0].Error != "") {
				t.Fatalf("other evidence missing: %+v", ev)
			}
		})
	}

	t.Run("the error's text is kept", func(t *testing.T) {
		w := greenWorld()
		w.watches.soakErr = errors.New("cloud monitoring is down")
		ev := w.collect(nil)
		if !strings.Contains(ev.Unavailable[KeySoak], "cloud monitoring is down") {
			t.Fatalf("unavailable %v", ev.Unavailable)
		}
	})

	t.Run("a source that answers nothing is unavailable", func(t *testing.T) {
		w := greenWorld()
		w.watches.soak, w.watches.coverage = nil, nil
		ev := w.collect(nil)
		if ev.Unavailable[KeySoak] == "" || ev.Unavailable[KeyCoverage] == "" {
			t.Fatalf("unavailable %v", ev.Unavailable)
		}
	})

	t.Run("a source that isn't wired is unavailable", func(t *testing.T) {
		w := greenWorld()
		c := w.collector()
		c.Sources.Reports, c.Sources.Diffs, c.Sources.Budgets = nil, nil, nil
		ev := c.Collect(context.Background(), CollectInput{Principal: agent(), Plan: w.plan, From: w.staging, To: w.production})
		for _, k := range []string{KeyReports, KeyDiff, KeyBudget} {
			if ev.Unavailable[k] == "" {
				t.Fatalf("%s: unavailable %v", k, ev.Unavailable)
			}
		}
	})

	t.Run("no plan or environment makes everything unavailable", func(t *testing.T) {
		w := greenWorld()
		ev := w.collector().Collect(context.Background(), CollectInput{Principal: agent(), Plan: w.plan, From: nil, To: w.production})
		for _, k := range []string{KeyDiff, KeyPins, KeySoak, KeyStagingRelease, KeyProductionRelease, KeyCoverage, KeyCanaries, KeyReports, KeyBudget} {
			if ev.Unavailable[k] == "" {
				t.Fatalf("%s: unavailable %v", k, ev.Unavailable)
			}
		}
	})
}

func TestCollectClassifiesActions(t *testing.T) {
	w := greenWorld()
	w.plan.Release = nil
	w.plan.Actions = []planning.Action{
		{ID: "service:web", Op: planning.Delete, Subject: planning.ServiceSubject, Name: "web", Provider: "cloud-run"},
		{ID: "resource:db", Op: planning.Update, Subject: planning.ResourceSubject, Name: "db", Provider: "cloud-sql", Resource: &planning.ResourceConfig{}},
		{ID: "service:api", Op: planning.Replace, Subject: planning.ServiceSubject, Name: "api", Provider: "cloud-run", Service: &planning.ServiceConfig{}},
		{ID: "resource:cache", Op: planning.Create, Subject: planning.ResourceSubject, Name: "cache", Provider: "memorystore", Resource: &planning.ResourceConfig{}, BackupEveryHours: 24},
		{ID: "resource:old", Op: planning.Replace, Subject: planning.ResourceSubject, Name: "old", Provider: "cloud-sql", Resource: &planning.ResourceConfig{}},
		{ID: "service:jobs", Op: planning.Create, Subject: planning.ServiceSubject, Name: "jobs", Service: &planning.ServiceConfig{Provider: "render"}},
		{ID: "service:same", Op: planning.Keep, Subject: planning.ServiceSubject, Name: "same", Provider: "cloud-run"},
	}
	ev := w.collect(nil)

	if want := []string{"service:web", "resource:old"}; !slices.Equal(ev.Destroy, want) {
		t.Fatalf("destroy %v, want %v", ev.Destroy, want)
	}
	if want := []string{"resource:db"}; !slices.Equal(ev.DataUpdates, want) {
		t.Fatalf("data updates %v, want %v", ev.DataUpdates, want)
	}
	want := []ChangedService{
		{ActionID: "service:api", Name: "api", Provider: "cloud-run", Op: "replace", Canary: false, LastCanary: "dep_p1"},
		{ActionID: "service:jobs", Name: "jobs", Provider: "render", Op: "create", Canary: false},
	}
	if !reflect.DeepEqual(ev.ChangedServices, want) {
		t.Fatalf("changed services %+v, want %+v", ev.ChangedServices, want)
	}
	if !ev.BackupSchedule {
		t.Fatal("a plan that sets a default backup schedule must say so")
	}

	// A service the release ramps is marked as canaried.
	w = greenWorld()
	ev = w.collect(nil)
	if len(ev.ChangedServices) != 1 || !ev.ChangedServices[0].Canary || ev.BackupSchedule {
		t.Fatalf("changed services %+v, backup %v", ev.ChangedServices, ev.BackupSchedule)
	}
}

// Each changed service's newest production canary is read on its own,
// however many canaries of other services ran since.
func TestCollectSummarizesCanaries(t *testing.T) {
	w := greenWorld()
	w.plan.Actions = append(w.plan.Actions,
		planning.Action{ID: "service:web", Op: planning.Update, Subject: planning.ServiceSubject, Name: "web", Provider: "cloud-run", Service: &planning.ServiceConfig{}},
		planning.Action{ID: "service:jobs", Op: planning.Update, Subject: planning.ServiceSubject, Name: "jobs", Provider: "cloud-run", Service: &planning.ServiceConfig{}},
	)
	base := collectedAt.Add(-48 * time.Hour)
	done := base.Add(time.Hour)
	w.canaries.runs = []deployments.CanaryRun{{
		DeploymentID: "dep_3", PlanID: "plan_3", Status: "failed", StartedAt: base, FinishedAt: &done, Canary: &deployments.Canary{
			Status: deployments.CanaryRolledBack, Reason: "errors rose",
			Services: []deployments.CanaryService{{ActionID: "service:api", Name: "api"}, {ActionID: "service:web", Name: "web"}},
		},
	}}
	for i := range 12 {
		w.canaries.runs = append(w.canaries.runs, deployments.CanaryRun{
			DeploymentID: fmt.Sprintf("dep_web_%d", i), Status: "succeeded", StartedAt: base.Add(time.Duration(i+2) * time.Hour),
			Canary: &deployments.Canary{Status: deployments.CanaryPromoted, Services: []deployments.CanaryService{{ActionID: "service:web", Name: "web"}}},
		})
	}
	ev := w.collect(nil)

	last := map[string]string{}
	for _, s := range ev.ChangedServices {
		last[s.Name] = s.LastCanary
	}
	if want := map[string]string{"api": "dep_3", "web": "dep_web_11", "jobs": ""}; !reflect.DeepEqual(last, want) {
		t.Fatalf("last canaries %v, want %v", last, want)
	}
	if len(ev.Canaries) != 2 {
		t.Fatalf("canaries %+v", ev.Canaries)
	}
	want := CanarySummary{
		DeploymentID: "dep_3", PlanID: "plan_3", Status: deployments.CanaryRolledBack, Reason: "errors rose",
		Services: []string{"api", "web"}, ActionIDs: []string{"service:api", "service:web"}, FinishedAt: &done,
	}
	if !reflect.DeepEqual(ev.Canaries[0], want) || ev.Canaries[1].DeploymentID != "dep_web_11" {
		t.Fatalf("canaries %+v", ev.Canaries)
	}
	// And the gate reads api's rollback, twelve web canaries later.
	in := green()
	in.Evidence = ev
	in.Evidence.Coverage = &releasewatch.Coverage{Environment: "production", Duration: 15 * time.Minute, Services: []releasewatch.ServiceCoverage{}}
	if c := Checks(in)[GateSafetyNet-1]; c.Passed || !strings.Contains(c.Detail, "api's last canary rolled back") {
		t.Fatalf("gate 5 %+v", c)
	}
}

func TestCollectReusesGivenDiffs(t *testing.T) {
	w := greenWorld()
	given := []SourceDiff{{
		SourceID: "src_api", Alias: "api", Repository: "acme/api", ProductionCommit: shaA, StagingCommit: shaB,
		Direction: DirectionAhead, AheadBy: 3, TotalCommits: 3, Commits: []DiffCommit{{SHA: shaB, Title: "from the report"}},
	}}
	ev := w.collect(given)
	if len(w.diffs.calls) != 0 {
		t.Fatalf("compared %+v", w.diffs.calls)
	}
	if !reflect.DeepEqual(ev.Sources, given) {
		t.Fatalf("sources %+v", ev.Sources)
	}

	// A given diff that names other commits than the plan and production
	// now do is read again, never trusted.
	for name, stale := range map[string]SourceDiff{
		"staging moved":    {SourceID: "src_api", Alias: "api", Repository: "acme/api", ProductionCommit: shaA, StagingCommit: shaC, Direction: DirectionAhead},
		"production moved": {SourceID: "src_api", Alias: "api", Repository: "acme/api", ProductionCommit: shaC, StagingCommit: shaB, Direction: DirectionAhead},
	} {
		t.Run(name, func(t *testing.T) {
			sw := greenWorld()
			got := sw.collect([]SourceDiff{stale})
			if len(sw.diffs.calls) != 1 || got.Sources[0].ProductionCommit != shaA || got.Sources[0].StagingCommit != shaB {
				t.Fatalf("calls %+v, sources %+v", sw.diffs.calls, got.Sources)
			}
		})
	}

	// A given diff that couldn't be read stays unreadable.
	w = greenWorld()
	ev = w.collect([]SourceDiff{{SourceID: "src_api", Alias: "api", Repository: "acme/api", ProductionCommit: shaA, StagingCommit: shaB, Error: "the diff is incomplete"}})
	if ev.Unavailable[KeyDiff] == "" {
		t.Fatalf("unavailable %v", ev.Unavailable)
	}
}

// githubServer answers every compare with one commit and counts requests.
func githubServer(t *testing.T, auth *atomic.Value) (*repositories.GitHub, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		auth.Store(r.Header.Get("Authorization") + " " + r.URL.Path)
		_, _ = w.Write([]byte(`{"status":"ahead","ahead_by":1,"behind_by":0,"total_commits":1,
			"commits":[{"sha":"` + shaB + `","commit":{"message":"Add users","author":{"name":"Ada","date":"2026-11-01T10:00:00Z"}}}],"files":[]}`))
	}))
	t.Cleanup(srv.Close)
	return repositories.NewGitHub(srv.URL), &calls
}

type fakeTokens struct {
	calls []string
	repo  repositories.Repository
}

func (f *fakeTokens) Token(_ context.Context, orgID string, repo repositories.Repository) (string, error) {
	f.calls = append(f.calls, orgID)
	f.repo = repo
	return "tok-" + orgID, nil
}

func TestGitHubDiffsUsesTheOrgsToken(t *testing.T) {
	var seen atomic.Value
	gh, calls := githubServer(t, &seen)
	tokens := &fakeTokens{}
	d := GitHubDiffs{GitHub: gh, Tokens: tokens}

	c, err := d.Compare(context.Background(), "org_a", "acme/api", shaA, shaB)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "ahead" || len(c.Commits) != 1 || calls.Load() != 1 {
		t.Fatalf("comparison %+v, %d calls", c, calls.Load())
	}
	if !slices.Equal(tokens.calls, []string{"org_a"}) || tokens.repo != (repositories.Repository{Owner: "acme", Name: "api"}) {
		t.Fatalf("token asked for %v %+v", tokens.calls, tokens.repo)
	}
	if got, want := seen.Load(), "Bearer tok-org_a /repos/acme/api/compare/"+shaA+"..."+shaB; got != want {
		t.Fatalf("request %q, want %q", got, want)
	}

	if _, err := d.Compare(context.Background(), "org_a", "not a key", shaA, shaB); err == nil {
		t.Fatal("a stored key that doesn't parse must be an error")
	}
}

func TestNonGitHubSourceIsUnreadable(t *testing.T) {
	var seen atomic.Value
	gh, calls := githubServer(t, &seen)
	tokens := &fakeTokens{}
	d := GitHubDiffs{GitHub: gh, Tokens: tokens}

	for _, key := range []string{"gitlab.com/acme/api", "git.example.com/group/sub/api", "bitbucket.org/acme/api"} {
		c, err := d.Compare(context.Background(), "org_a", key, shaA, shaB)
		if !errors.Is(err, repositories.ErrUnsupportedHost) || c != nil {
			t.Fatalf("%s: %v %+v", key, err, c)
		}
	}
	if calls.Load() != 0 || len(tokens.calls) != 0 {
		t.Fatalf("%d requests, tokens %v: nothing may be asked for another host", calls.Load(), tokens.calls)
	}

	// Collected, such a source's diff is unreadable evidence.
	w := greenWorld()
	w.plan.Sources[0].Repository = "gitlab.com/acme/api"
	c := w.collector()
	c.Sources.Diffs = d
	ev := c.Collect(context.Background(), CollectInput{Principal: agent(), Plan: w.plan, From: w.staging, To: w.production})
	if len(ev.Sources) != 1 || ev.Sources[0].Error == "" || ev.Unavailable[KeyDiff] == "" {
		t.Fatalf("sources %+v, unavailable %v", ev.Sources, ev.Unavailable)
	}
}

func TestNoReportsIsEmpty(t *testing.T) {
	got, err := NoReports{}.Open(context.Background(), "org_a", "proj_a", "env_stg")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestWatcherReportsPassesTheStagingEnvironmentThrough(t *testing.T) {
	first, second := collectedAt.Add(-time.Hour), collectedAt.Add(-time.Minute)
	f := &fake3c{reports: []*agentreports.Report{
		{ID: "arep_1", Title: "Errors up on api", Severity: "high", FirstSeenAt: first},
		{ID: "arep_2", Title: "Latency", Severity: "low", FirstSeenAt: second},
	}}
	got, err := WatcherReports{Reports: f}.Open(context.Background(), "org_a", "proj_a", "env_stg")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, [][3]string{{"org_a", "proj_a", "env_stg"}}) {
		t.Fatalf("asked %v", f.calls)
	}
	want := []OpenReport{
		{ID: "arep_1", Title: "Errors up on api", Severity: "high", Path: "/agent/reports/arep_1", OpenedAt: first},
		{ID: "arep_2", Title: "Latency", Severity: "low", Path: "/agent/reports/arep_2", OpenedAt: second},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}

	// None open is an empty list, not nil.
	got, err = WatcherReports{Reports: &fake3c{}}.Open(context.Background(), "org_a", "proj_a", "env_stg")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestWatcherReportsRefusesAnEmptyEnvironment(t *testing.T) {
	f := &fake3c{}
	for _, args := range [][2]string{{"proj_a", ""}, {"", "env_stg"}} {
		if _, err := (WatcherReports{Reports: f}).Open(context.Background(), "org_a", args[0], args[1]); err == nil {
			t.Fatalf("%v: an empty argument would widen the read", args)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("asked %v", f.calls)
	}
	if _, err := (WatcherReports{}).Open(context.Background(), "org_a", "proj_a", "env_stg"); err == nil {
		t.Fatal("no report service must be an error")
	}
}

func TestWatcherReportsErrorIsAnError(t *testing.T) {
	_, err := WatcherReports{Reports: &fake3c{err: errors.New("store down")}}.Open(context.Background(), "org_a", "proj_a", "env_stg")
	if err == nil {
		t.Fatal("an error must stay an error, so gate 6 fails as unreadable")
	}
}
