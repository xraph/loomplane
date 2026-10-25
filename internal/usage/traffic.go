package usage

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// ServiceTraffic is one service's requests and latency over the window,
// beside the window before it.
type ServiceTraffic struct {
	BindingID        string  `json:"binding_id"`
	ProjectID        string  `json:"project_id"`
	EnvironmentID    string  `json:"environment_id"`
	Name             string  `json:"name"`
	Provider         string  `json:"provider"`
	Requests         float64 `json:"requests"`
	PreviousRequests float64 `json:"previous_requests"`
	// Latency is in milliseconds, 0 when the provider reports none.
	Latency         float64 `json:"latency"`
	PreviousLatency float64 `json:"previous_latency"`
	// Statistic is what Latency is: p95, or average for a provider that
	// reports no percentile. Latencies are never combined across
	// services, since a percentile can't be.
	Statistic string `json:"statistic,omitempty"`
}

// TrafficReport is the body of GET /api/v1/usage/traffic.
type TrafficReport struct {
	Range metrics.Range `json:"range"`
	Start time.Time     `json:"start"`
	End   time.Time     `json:"end"`
	// Step is each bucket's width in seconds.
	Step int `json:"step"`
	// Requests are every service's requests added up, one per bucket.
	Requests      []metrics.Point `json:"requests"`
	Total         float64         `json:"total"`
	PreviousTotal float64         `json:"previous_total"`
	// Services are slowest first.
	Services []ServiceTraffic `json:"services"`
	Sources  []Source         `json:"sources"`
}

// noTraffic says why a provider's services can't report.
var noTraffic = map[string]string{
	"server":     "Services on your own servers report to your own monitoring, which Loomplane doesn't read.",
	"kubernetes": "Services on your own clusters report to your own monitoring, which Loomplane doesn't read.",
}

// measured is one service's two windows, or why they couldn't be read.
type measured struct {
	b             *planning.Binding
	current, prev *metrics.Measurement
	err           error
}

// Traffic reads the requests and latency of every service the principal
// can see, over rng and the same stretch before it. It needs
// infrastructure:read, and a project's services need project:read.
func (s *Service) Traffic(ctx context.Context, p security.Principal, rng string) (*TrafficReport, error) {
	r, err := metrics.ParseRange(rng)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if aerr := s.Authz.Require(ctx, p, security.InfrastructureRead, ""); aerr != nil {
		return nil, aerr
	}
	bindings, err := s.visible(ctx, p)
	if err != nil {
		return nil, err
	}
	all, err := s.Connections.All(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	conns := map[string]*connections.Connection{}
	for _, c := range all {
		conns[c.ID] = c
	}

	w := r.Window(s.now())
	results := make([]measured, len(bindings))
	g := errgroup.Group{}
	g.SetLimit(fanOut)
	for i, b := range bindings {
		g.Go(func() error {
			results[i] = s.measureBoth(ctx, conns[b.ConnectionID], b, w)
			return nil
		})
	}
	_ = g.Wait() //nolint:errcheck // every read records its own outcome

	rep := &TrafficReport{Range: r, Start: w.Start, End: w.End, Step: int(w.Step.Seconds()), Services: []ServiceTraffic{}}
	var current, previous [][]metrics.Point
	sources := map[[3]string]*Source{}
	for _, m := range results {
		status, reason := classify(m.err)
		key := [3]string{m.b.ConnectionID, status, reason}
		src := sources[key]
		if src == nil {
			src = &Source{ConnectionID: m.b.ConnectionID, Status: status, Reason: reason}
			if c := conns[m.b.ConnectionID]; c != nil {
				src.Cloud, src.Name = c.Cloud, c.Name
			}
			sources[key] = src
		}
		src.Services = append(src.Services, m.b.Name)
		if m.err != nil {
			continue
		}
		cur, prev := metrics.Grid(w, m.current.Requests), metrics.Grid(w.Previous(), m.prev.Requests)
		current, previous = append(current, cur), append(previous, prev)
		rep.Services = append(rep.Services, ServiceTraffic{
			BindingID: m.b.ID, ProjectID: m.b.ProjectID, EnvironmentID: m.b.EnvironmentID, Name: m.b.Name, Provider: m.b.Provider,
			Requests: metrics.Total(cur), PreviousRequests: metrics.Total(prev),
			Latency: m.current.Latency, PreviousLatency: m.prev.Latency, Statistic: m.current.Statistic,
		})
	}
	rep.Requests = metrics.Sum(w, current...)
	rep.Total = metrics.Total(rep.Requests)
	rep.PreviousTotal = metrics.Total(metrics.Sum(w.Previous(), previous...))
	slices.SortStableFunc(rep.Services, func(a, b ServiceTraffic) int {
		return cmp.Or(cmp.Compare(b.Latency, a.Latency), cmp.Compare(b.Requests, a.Requests), cmp.Compare(a.Name, b.Name))
	})
	rep.Sources = make([]Source, 0, len(sources))
	for _, src := range sources {
		slices.Sort(src.Services)
		rep.Sources = append(rep.Sources, *src)
	}
	slices.SortFunc(rep.Sources, func(a, b Source) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.Status, b.Status), strings.Compare(a.Reason, b.Reason))
	})
	return rep, nil
}

// visible is the organization's services in projects p may read. An
// authorization error other than a refusal fails the whole read, so an
// outage never looks like an organization with nothing running.
func (s *Service) visible(ctx context.Context, p security.Principal) ([]*planning.Binding, error) {
	all, err := s.Bindings.OrgServiceBindings(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	out := make([]*planning.Binding, 0, len(all))
	for _, b := range all {
		if b.ConnectionID == "" {
			continue
		}
		ok, seen := allowed[b.ProjectID]
		if !seen {
			switch err := s.Authz.Require(ctx, p, security.ProjectRead, b.ProjectID); {
			case err == nil:
				ok = true
			case errors.Is(err, security.ErrForbidden):
			default:
				return nil, err
			}
			allowed[b.ProjectID] = ok
		}
		if ok {
			out = append(out, b)
		}
	}
	return out, nil
}

// measureBoth reads a service's window and the one before.
func (s *Service) measureBoth(ctx context.Context, c *connections.Connection, b *planning.Binding, w metrics.Window) measured {
	m := measured{b: b}
	meter := s.Meters[b.Provider]
	switch {
	case c == nil:
		m.err = metrics.Unavailablef("The connection that deployed %s is gone.", b.Name)
		return m
	case meter == nil:
		reason := noTraffic[b.Provider]
		if reason == "" {
			reason = s.Title(b.Provider) + " doesn't report requests or latency through an API Loomplane reads."
		}
		m.err = &metrics.UnavailableError{Reason: reason}
		return m
	}
	if m.current, m.err = s.measure(ctx, meter, c, b, w); m.err != nil {
		return m
	}
	m.prev, m.err = s.measure(ctx, meter, c, b, w.Previous())
	return m
}

// measure reads one window, from the cache when it can.
func (s *Service) measure(ctx context.Context, meter Meter, c *connections.Connection, b *planning.Binding, w metrics.Window) (*metrics.Measurement, error) {
	key := fmt.Sprintf("traffic|%s|%s|%d|%d|%d", b.OrgID, b.ID, c.Generation, w.Start.Unix(), w.End.Unix())
	return cached(s.cache, key, trafficTTL, func() (*metrics.Measurement, error) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
		defer cancel()
		live, cred, err := s.credential(rctx, c)
		if err != nil {
			return nil, err
		}
		got, err := meter.Measure(rctx, live, cred, b, w)
		s.Telemetry.UsageRead("traffic", b.Provider, outcome(err))
		if err != nil {
			if _, ok := metrics.IsUnavailable(err); !ok {
				s.Warn("read traffic for "+b.Name, err)
			}
			return nil, err
		}
		if got == nil {
			got = &metrics.Measurement{}
		}
		return got, nil
	})
}
