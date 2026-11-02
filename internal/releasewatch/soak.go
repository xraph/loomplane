package releasewatch

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/security"
)

// Pin is one repository's commit a soak asks about: Name is how people
// call the repository.
type Pin struct {
	SourceID  string `json:"source_id"`
	Name      string `json:"name"`
	CommitSHA string `json:"commit_sha"`
}

// SoakService is how one service built from the soaked commits has done.
type SoakService struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	// Verdict is pass, fail or inconclusive (no evidence either way).
	Verdict releasehealth.Verdict `json:"verdict"`
	Reason  string                `json:"reason"`
}

// Soak says how long an environment has run exactly a set of commits and
// whether it ran them healthily.
type Soak struct {
	Environment string `json:"environment"`
	// Running is true when the environment runs every named commit.
	Running bool `json:"running"`
	// Since is when the last of the commits was deployed; zero unless Running.
	Since time.Time `json:"since,omitzero"`
	// Duration is how long the environment has run them; 0 unless Running.
	Duration time.Duration `json:"duration"`
	// Healthy is true only when Running and every service built from the
	// commits passed.
	Healthy bool `json:"healthy"`
	// Unverified is true when Running and the evidence is missing: low
	// traffic, no metrics, or too short a time. A service that failed is
	// evidence, so it isn't unverified.
	Unverified bool          `json:"unverified"`
	Reasons    []string      `json:"reasons"`
	Services   []SoakService `json:"services"`
}

// Soak measures how the environment has done since it began running pins,
// judging each service built from them over that time against t's error
// ceiling and minimum requests alone: a soaking environment has no
// baseline that means anything. A soak longer than a day is judged over
// its last day.
func (s *Service) Soak(ctx context.Context, orgID, environmentID, environment string, pins []Pin, t releasehealth.Thresholds) (*Soak, error) {
	out := &Soak{Environment: environment, Reasons: []string{}, Services: []SoakService{}}
	deployed, err := s.plans.DeployedSources(ctx, orgID, environmentID)
	if err != nil {
		return nil, err
	}
	if len(pins) == 0 {
		out.Reasons = append(out.Reasons, "the promotion moves no repository")
		return out, nil
	}
	var since time.Time
	for _, pin := range pins {
		var got *planning.DeployedSource
		for _, d := range deployed {
			if d.SourceID == pin.SourceID {
				got = d
				break
			}
		}
		switch {
		case got == nil:
			out.Reasons = append(out.Reasons, fmt.Sprintf("%s has no %s deployed", environment, pin.Name))
		case got.CommitSHA != pin.CommitSHA:
			out.Reasons = append(out.Reasons, fmt.Sprintf("%s runs %s@%s, not %s", environment, pin.Name, short(got.CommitSHA), short(pin.CommitSHA)))
		case got.DeployedAt.After(since):
			since = got.DeployedAt
		}
	}
	if len(out.Reasons) > 0 {
		return out, nil
	}
	now := s.now()
	out.Running, out.Since = true, since
	if out.Duration = max(now.Sub(since), 0); out.Duration < releasehealth.MetricsLag {
		out.Unverified = true
		out.Reasons = append(out.Reasons, fmt.Sprintf("%s has run these commits for less than the metrics lag (%s)", environment, formatLag(releasehealth.MetricsLag)))
		return out, nil
	}

	bindings, err := s.plans.ListBindings(ctx, orgID, environmentID)
	if err != nil {
		return nil, err
	}
	commits := make([]string, 0, len(pins))
	for _, pin := range pins {
		commits = append(commits, pin.CommitSHA)
	}
	for _, b := range bindings {
		if b.Subject == planning.ServiceSubject && slices.Contains(commits, b.CommitSHA) {
			out.Services = append(out.Services, s.soakService(ctx, orgID, b, since, now, t))
		}
	}
	slices.SortFunc(out.Services, func(a, b SoakService) int { return strings.Compare(a.Name, b.Name) })
	if len(out.Services) == 0 {
		out.Unverified = true
		out.Reasons = append(out.Reasons, fmt.Sprintf("%s has no service built from these commits to judge", environment))
		return out, nil
	}
	out.Healthy = true
	for _, svc := range out.Services {
		if svc.Verdict == releasehealth.Pass {
			continue
		}
		out.Healthy = false
		out.Unverified = out.Unverified || svc.Verdict == releasehealth.Inconclusive
		out.Reasons = append(out.Reasons, svc.Name+": "+svc.Reason)
	}
	return out, nil
}

// soakService judges one binding over the time since the soak began.
func (s *Service) soakService(ctx context.Context, orgID string, b *planning.Binding, since, now time.Time, t releasehealth.Thresholds) SoakService {
	svc := SoakService{Name: b.Name, Provider: b.Provider, Verdict: releasehealth.Inconclusive}
	if ok, reason := s.watchable(b.Provider); !ok {
		svc.Reason = reason
		return svc
	}
	end := now.Add(-releasehealth.MetricsLag)
	if end.Sub(since) > 24*time.Hour {
		since = end.Add(-24 * time.Hour)
	}
	w, err := metrics.ShortWindow(since, end)
	if err != nil {
		svc.Reason = "there isn't a full minute of metrics to read yet"
		return svc
	}
	c, err := s.health.Health(ctx, orgID, b.ID, "", releasehealth.Window{Start: w.Start, End: w.End})
	if err != nil {
		svc.Reason = err.Error()
		return svc
	}
	if !c.Available {
		svc.Reason = c.Reason
		if svc.Reason == "" {
			svc.Reason = "metrics aren't available"
		}
		return svc
	}
	svc.Verdict, svc.Reason = releasehealth.Judge(c, c, t)
	return svc
}

// SoakForPlan measures how long the promotion's From environment has run
// the commits the plan moves, against the plan's own thresholds. Only a
// person may ask: it needs deployment:read on the project.
func (s *Service) SoakForPlan(ctx context.Context, p security.Principal, planID string) (*Soak, error) {
	if p.IsSystem() {
		return nil, fmt.Errorf("%w: %s doesn't read soaks", security.ErrForbidden, security.SystemActorName)
	}
	plan, err := s.plans.GetPlan(ctx, p.OrgID, planID)
	if err != nil {
		return nil, err
	}
	if authErr := s.authz.Require(ctx, p, security.DeploymentRead, plan.ProjectID); authErr != nil {
		return nil, authErr
	}
	if plan.From == "" || plan.FromEnvironmentID == "" {
		return nil, fmt.Errorf("%w: only a promotion has a soak", planning.ErrInvalid)
	}
	var pins []Pin
	for _, src := range plan.Sources {
		if !src.InScope {
			continue
		}
		name := src.Alias
		if name == "" {
			name = src.Repository
		}
		pins = append(pins, Pin{SourceID: src.SourceID, Name: name, CommitSHA: src.CommitSHA})
	}
	return s.Soak(ctx, p.OrgID, plan.FromEnvironmentID, plan.From, pins, plan.Thresholds())
}

// short cuts a commit to the seven characters people read.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// formatLag writes a lag as whole minutes, such as 3m.
func formatLag(d time.Duration) string { return fmt.Sprintf("%dm", int(d/time.Minute)) }
