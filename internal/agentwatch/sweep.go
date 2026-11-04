package agentwatch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releasehealth"
)

// How the binding sweep runs.
const (
	SweepEvery          = 5 * time.Minute
	ErrorWindow         = 10 * time.Minute // the error rate judged
	BaselineWindow      = time.Hour        // what it's judged against: the hour before
	ClearAfterPasses    = 2
	MaxBindingsPerSweep = 50
)

// Check is what the sweep last found for one binding.
type Check struct {
	CheckedAt time.Time `json:"checked_at"`
	Failing   bool      `json:"failing"`
	Passes    int       `json:"passes"`
}

// Sweep checks up to MaxBindingsPerSweep production bindings, least
// recently checked first, for drift and error rate. It returns the checks
// of every production binding, so ones that left production drop out.
func (d *Detector) Sweep(ctx context.Context, orgID string, checks map[string]Check) ([]agentreports.Signal, []agentreports.Clearance, map[string]Check, error) {
	bindings, err := d.World.ProductionBindings(ctx, orgID)
	var skipped *SkippedProjectsError
	switch {
	case errors.As(err, &skipped):
		for id, perr := range skipped.Errs {
			d.warn("agentwatch: read the production bindings of project "+id, perr)
		}
	case err != nil:
		return nil, nil, checks, fmt.Errorf("list production bindings: %w", err)
	}
	now := d.now()
	next := make(map[string]Check, len(bindings))
	if skipped != nil {
		// The skipped projects' bindings aren't known to have left
		// production, so their checks stay.
		maps.Copy(next, checks)
	}
	var due []*planning.Binding
	for _, b := range bindings {
		c := checks[b.ID]
		next[b.ID] = c
		if now.Sub(c.CheckedAt) >= SweepEvery {
			due = append(due, b)
		}
	}
	slices.SortFunc(due, func(a, b *planning.Binding) int {
		return cmp.Or(next[a.ID].CheckedAt.Compare(next[b.ID].CheckedAt), strings.Compare(a.ID, b.ID))
	})
	if len(due) > MaxBindingsPerSweep {
		due = due[:MaxBindingsPerSweep]
	}

	s := &sweep{
		d: d, orgID: orgID, now: now, envs: map[string]*projects.Environment{}, busy: map[string]bool{},
		plans: map[string]releasehealth.Thresholds{}, repos: repoReads{},
	}
	s.current, s.baseline, err = windows(now)
	if err != nil {
		// The windows are constants, so this never happens; if it did,
		// drift is still worth checking.
		d.warn("agentwatch: the sweep's error windows", err)
	}
	for _, b := range due {
		c := next[b.ID]
		c.CheckedAt = now
		next[b.ID] = s.binding(ctx, b, c)
	}
	return s.signals, s.clearances, next, nil
}

// windows are the error window, ending the metrics lag before now so the
// cloud has reported all of it, and the baseline hour that ends where it
// starts.
func windows(now time.Time) (current, baseline releasehealth.Window, err error) {
	end := now.Add(-releasehealth.MetricsLag)
	cur, err := metrics.ShortWindow(end.Add(-ErrorWindow), end)
	if err != nil {
		return current, baseline, err
	}
	base, err := metrics.ShortWindow(cur.Start.Add(-BaselineWindow), cur.Start)
	if err != nil {
		return current, baseline, err
	}
	return releasehealth.Window{Start: cur.Start, End: cur.End}, releasehealth.Window{Start: base.Start, End: base.End}, nil
}

// sweep is one Sweep: what it found, and what it read once for several
// bindings.
type sweep struct {
	d                 *Detector
	orgID             string
	now               time.Time
	current, baseline releasehealth.Window
	envs              map[string]*projects.Environment
	busy              map[string]bool // environment ID: a canary or a watch holds it
	plans             map[string]releasehealth.Thresholds
	repos             repoReads
	signals           []agentreports.Signal
	clearances        []agentreports.Clearance
}

// binding checks one binding and returns its check.
func (s *sweep) binding(ctx context.Context, b *planning.Binding, c Check) Check {
	env, err := s.environment(ctx, b)
	if err != nil {
		s.d.warn("agentwatch: read the environment of binding "+b.ID, err)
		return c
	}
	// Drift, from the row the drift checker wrote, catches what an event
	// gap missed.
	if b.Drift != "" {
		if sig := s.d.driftSignal(ctx, s.repos, b, env, s.now); sig != nil {
			s.signals = append(s.signals, *sig)
		}
	} else {
		at := b.UpdatedAt
		if b.ObservedAt != nil {
			at = *b.ObservedAt
		}
		s.clearances = append(s.clearances, *driftCleared(s.orgID, b.ProjectID, b.EnvironmentID, b.Name, at))
	}
	return s.errorRate(ctx, b, env, c)
}

func (s *sweep) environment(ctx context.Context, b *planning.Binding) (*projects.Environment, error) {
	if env, ok := s.envs[b.EnvironmentID]; ok {
		return env, nil
	}
	env, err := s.d.World.Environment(ctx, s.orgID, b.ProjectID, b.EnvironmentID)
	if err != nil {
		return nil, err
	}
	s.envs[b.EnvironmentID] = env
	return env, nil
}

// errorRate judges the service's last ErrorWindow against the hour
// before, where the cloud counts errors and no canary or watch is judging
// the environment already.
func (s *sweep) errorRate(ctx context.Context, b *planning.Binding, env *projects.Environment, c Check) Check {
	if b.Subject != planning.ServiceSubject || !releasehealth.SupportFor(b.Provider).Errors || s.d.Health == nil || s.current.End.IsZero() {
		return c
	}
	busy, err := s.held(ctx, env)
	if err != nil {
		s.d.warn("agentwatch: see whether a release holds "+env.ID, err)
		return c
	}
	if busy {
		return c
	}
	current, err := s.d.Health.Health(ctx, s.orgID, b.ID, "", s.current)
	if err != nil {
		s.d.warn("agentwatch: read the health of binding "+b.ID, err)
		return c
	}
	baseline, err := s.d.Health.Health(ctx, s.orgID, b.ID, "", s.baseline)
	if err != nil {
		s.d.warn("agentwatch: read the baseline health of binding "+b.ID, err)
		return c
	}
	t := s.thresholds(ctx, b)
	verdict, _ := releasehealth.Judge(baseline, current, t)
	switch verdict {
	case releasehealth.Fail:
		c.Failing, c.Passes = true, 0
		s.signals = append(s.signals, s.errorSignal(ctx, b, env, current, baseline, t))
	case releasehealth.Pass:
		c.Passes = min(c.Passes+1, ClearAfterPasses)
		if c.Failing && c.Passes >= ClearAfterPasses {
			c.Failing, c.Passes = false, 0
			s.clearances = append(s.clearances, agentreports.Clearance{
				OrgID: s.orgID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID,
				Signals: []string{agentreports.SignalErrorRate}, Components: []string{b.Name},
				Reason: fmt.Sprintf("%s's error rate was back within its thresholds for %d checks in a row", b.Name, ClearAfterPasses),
				At:     s.current.End,
			})
		}
	case releasehealth.Inconclusive:
		// Too few requests, or no metrics: nothing to go on either way.
	}
	return c
}

// held reports whether a ramping canary or a release watch is judging the
// environment, which then reports what it finds itself.
func (s *sweep) held(ctx context.Context, env *projects.Environment) (bool, error) {
	if busy, ok := s.busy[env.ID]; ok {
		return busy, nil
	}
	ramping, err := s.d.World.RampingCanary(ctx, s.orgID, env.ID)
	if err != nil {
		return false, err
	}
	busy := ramping != ""
	if !busy {
		if busy, err = s.d.World.WatchingNow(ctx, s.orgID, env.ProjectID, env.Name); err != nil {
			return false, err
		}
	}
	s.busy[env.ID] = busy
	return busy, nil
}

// thresholds are the error thresholds of the plan that last wrote b, or
// the defaults. Latency isn't judged: a whole service's latency moves with
// its traffic.
func (s *sweep) thresholds(ctx context.Context, b *planning.Binding) releasehealth.Thresholds {
	t, ok := s.plans[b.PlanID]
	if !ok {
		t = releasehealth.DefaultThresholds()
		if b.PlanID != "" {
			plan, err := s.d.World.Plan(ctx, s.orgID, b.PlanID)
			switch {
			case err == nil:
				t = plan.Thresholds()
			case !errors.Is(err, planning.ErrNotFound):
				s.d.warn("agentwatch: read the thresholds of plan "+b.PlanID, err)
			}
		}
		s.plans[b.PlanID] = t
	}
	t.P95LatencyRatio = 0
	return t
}

func (s *sweep) errorSignal(ctx context.Context, b *planning.Binding, env *projects.Environment, current, baseline releasehealth.Health, t releasehealth.Thresholds) agentreports.Signal {
	rate := current.ErrorRate()
	return agentreports.Signal{
		OrgID: s.orgID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID, Environment: env.Name,
		Kind: agentreports.SignalErrorRate, Component: b.Name, Severity: agentreports.SeverityHigh,
		Title: fmt.Sprintf("%s's error rate jumped to %.1f%% on %s", b.Name, rate*100, env.Name),
		Facts: []agentreports.Fact{
			{Label: "Requests", Value: number(current.Requests)},
			{Label: "Errors", Value: number(current.Errors)},
			{Label: "Error rate now", Value: percent(rate)},
			{Label: "Error rate before", Value: percent(baseline.ErrorRate())},
			{Label: "Threshold", Value: thresholds(t)},
		},
		Evidence:   agentreports.Evidence{BindingID: b.ID, PlanID: b.PlanID},
		Repository: s.d.repository(ctx, s.repos, s.orgID, b.ProjectID, string(b.Subject)+":"+b.Name),
		At:         s.current.End,
	}
}
