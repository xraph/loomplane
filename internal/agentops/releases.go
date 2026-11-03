package agentops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// binding_metrics reads the last DefaultMetricsMinutes minutes unless asked
// for more, and never more than MaxMetricsMinutes: a release window is a
// day at most.
const (
	DefaultMetricsMinutes = 30
	MaxMetricsMinutes     = 1440
)

// errNoService is binding_metrics about a service that doesn't run in the
// environment.
var errNoService = errors.New("that service doesn't run in this environment")

// errNoHealth is binding_metrics on a server that can't read health.
var errNoHealth = errors.New("this server can't read service metrics")

// The text in a canary, a watch or a metric reading that comes from the
// cloud's monitoring, or is built from it, is evidence from outside: it
// reaches the model quoted as a tool result.
const evidence = agentsafety.SourceToolResult

// Outputs.
type (
	recentCanariesOutput struct {
		Canaries []deployments.CanaryRun `json:"canaries"`
	}
	bindingMetricsOutput struct {
		Service     string    `json:"service"`
		Environment string    `json:"environment"`
		BindingID   string    `json:"binding_id"`
		Provider    string    `json:"provider"`
		Minutes     int       `json:"minutes"`
		From        time.Time `json:"from"`
		To          time.Time `json:"to"`
		Available   bool      `json:"available"`
		// UnavailableReason says why the cloud gave no reading.
		UnavailableReason string  `json:"unavailable_reason,omitempty"`
		Requests          float64 `json:"requests"`
		Errors            float64 `json:"errors"`
		ErrorRate         float64 `json:"error_rate"`
		LatencyMS         float64 `json:"latency_ms"`
		Statistic         string  `json:"statistic,omitempty"`
	}
)

func registerReleases(r *Registry, d *Deps) {
	Register(r, def("canary_report"), security.DeploymentRead, func(ctx context.Context, call Call, in defs.DeploymentInput) (*deployments.CanaryReport, error) {
		if _, err := d.deployment(ctx, call, in.DeploymentID); err != nil {
			return nil, err
		}
		report, err := d.Deployments.Canary(ctx, call.Principal, in.DeploymentID)
		if err != nil {
			return nil, err
		}
		return &deployments.CanaryReport{Canary: d.quoteCanary(ctx, report.Canary), Verdicts: d.quoteVerdicts(ctx, report.Verdicts)}, nil
	})

	Register(r, def("recent_canaries"), security.DeploymentRead, func(ctx context.Context, call Call, in defs.RecentCanariesInput) (recentCanariesOutput, error) {
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return recentCanariesOutput{}, err
		}
		env, err := d.environment(ctx, call, p.ID, in.Environment)
		if err != nil {
			return recentCanariesOutput{}, err
		}
		runs, err := d.Deployments.RecentCanaries(ctx, call.Principal, p.ID, env.ID, in.Limit)
		if err != nil {
			return recentCanariesOutput{}, err
		}
		out := recentCanariesOutput{Canaries: make([]deployments.CanaryRun, 0, len(runs))}
		for _, run := range runs {
			run.Canary = d.quoteCanary(ctx, run.Canary)
			run.Verdicts = d.quoteVerdicts(ctx, run.Verdicts)
			out.Canaries = append(out.Canaries, run)
		}
		return out, nil
	})

	Register(r, def("binding_metrics"), security.InfrastructureRead, func(ctx context.Context, call Call, in defs.BindingMetricsInput) (bindingMetricsOutput, error) {
		return d.bindingMetrics(ctx, call, in)
	})

	Register(r, def("rollback_target"), security.DeploymentRead, func(ctx context.Context, call Call, in defs.RollbackTargetInput) (*planning.RollbackTarget, error) {
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return nil, err
		}
		if _, err = d.environment(ctx, call, p.ID, in.Environment); err != nil {
			return nil, err
		}
		return d.Plans.RollbackTarget(ctx, call.Principal, p.ID, in.Environment, in.DeploymentID)
	})

	Register(r, def("create_rollback"), security.DeploymentCreate, func(ctx context.Context, call Call, in defs.CreateRollbackInput) (*planning.Plan, error) {
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return nil, err
		}
		if _, err = d.environment(ctx, call, p.ID, in.Environment); err != nil {
			return nil, err
		}
		plan, err := d.Plans.CreateRollback(ctx, call.Principal, p.ID, planning.RollbackInput{Environment: in.Environment, Reason: in.Reason, DeploymentID: in.DeploymentID})
		if err != nil {
			return nil, err
		}
		if plan, err = d.waitPlan(ctx, call, plan); err != nil {
			return nil, err
		}
		return d.quotePlan(ctx, plan), nil
	})

	Register(r, def("get_release_watch"), security.DeploymentRead, func(ctx context.Context, call Call, in defs.ReleaseWatchInput) (*releasewatch.Watch, error) {
		if _, err := d.deployment(ctx, call, in.DeploymentID); err != nil {
			return nil, err
		}
		w, err := d.Watches.ForDeployment(ctx, call.Principal, in.DeploymentID)
		if err != nil {
			return nil, err
		}
		return d.quoteWatch(ctx, w), nil
	})
}

// bindingMetrics reads a service's traffic in an environment from its
// cloud's monitoring. The reader takes no principal, so the project's
// infrastructure:read is checked here, before anything is read.
func (d *Deps) bindingMetrics(ctx context.Context, call Call, in defs.BindingMetricsInput) (bindingMetricsOutput, error) {
	p, err := d.findProject(ctx, call, in.Project)
	if err != nil {
		return bindingMetricsOutput{}, err
	}
	if denied := d.Authz.Require(ctx, call.Principal, security.InfrastructureRead, p.ID); denied != nil {
		return bindingMetricsOutput{}, denied
	}
	env, err := d.environment(ctx, call, p.ID, in.Environment)
	if err != nil {
		return bindingMetricsOutput{}, err
	}
	list, err := d.Plans.Bindings(ctx, call.Principal, p.ID, env.Name)
	if err != nil {
		return bindingMetricsOutput{}, err
	}
	service := strings.TrimSpace(in.Service)
	i := slices.IndexFunc(list, func(b *planning.Binding) bool { return runsService(b, service) })
	if i < 0 {
		return bindingMetricsOutput{}, fmt.Errorf("%w: no service with graph ID %q runs in %s; pass the graph ID status shows in a binding's config.service", errNoService, service, env.Name)
	}
	// The reader takes only a binding ID: read the binding back by it in
	// the caller's organization, and check it's the one found above.
	b, err := d.Bindings.GetBinding(ctx, call.Principal.OrgID, list[i].ID)
	if err != nil {
		return bindingMetricsOutput{}, err
	}
	if b.ProjectID != p.ID || b.EnvironmentID != env.ID || !runsService(b, service) {
		return bindingMetricsOutput{}, fmt.Errorf("%w: %q in %s", errNoService, service, env.Name)
	}
	if d.Health == nil {
		return bindingMetricsOutput{}, errNoHealth
	}
	minutes := in.Minutes
	if minutes <= 0 {
		minutes = DefaultMetricsMinutes
	}
	minutes = min(minutes, MaxMetricsMinutes)
	// A window is read no sooner than the metrics lag after it ends, so
	// the cloud has reported all of it.
	end := time.Now().Add(-releasehealth.MetricsLag)
	w, err := metrics.ShortWindow(end.Add(-time.Duration(minutes)*time.Minute), end)
	if err != nil {
		return bindingMetricsOutput{}, err
	}
	h, err := d.Health.Health(ctx, call.Principal.OrgID, b.ID, "", releasehealth.Window{Start: w.Start, End: w.End})
	if err != nil {
		return bindingMetricsOutput{}, err
	}
	return bindingMetricsOutput{
		Service: service, Environment: env.Name, BindingID: b.ID, Provider: b.Provider,
		Minutes: minutes, From: w.Start, To: w.End,
		Available: h.Available, UnavailableReason: d.quote(ctx, evidence, h.Reason),
		Requests: h.Requests, Errors: h.Errors, ErrorRate: h.ErrorRate(), LatencyMS: h.LatencyMS, Statistic: h.Statistic,
	}, nil
}

// runsService reports whether b runs the service whose graph ID is
// graphID: a service binding's stored configuration names the service in
// the analysis's graph, the ID set_variable and request_secret take too.
func runsService(b *planning.Binding, graphID string) bool {
	if b.Subject != planning.ServiceSubject || graphID == "" {
		return false
	}
	var cfg struct {
		Service string `json:"service"`
	}
	return json.Unmarshal(b.Config, &cfg) == nil && cfg.Service == graphID
}

// quoteCanary quotes why a canary stands as it does.
func (d *Deps) quoteCanary(ctx context.Context, c *deployments.Canary) *deployments.Canary {
	if c == nil {
		return nil
	}
	out := *c
	out.Reason = d.quote(ctx, evidence, c.Reason)
	return &out
}

// quoteVerdicts quotes each verdict's reason and the reasons its readings
// were unavailable.
func (d *Deps) quoteVerdicts(ctx context.Context, in []deployments.CanaryVerdict) []deployments.CanaryVerdict {
	out := slices.Clone(in)
	if out == nil {
		out = []deployments.CanaryVerdict{}
	}
	for i := range out {
		v := &out[i]
		v.Reason = d.quote(ctx, evidence, v.Reason)
		v.Baseline.Reason = d.quote(ctx, evidence, v.Baseline.Reason)
		v.Candidate.Reason = d.quote(ctx, evidence, v.Candidate.Reason)
	}
	return out
}

// quoteWatch quotes a release watch's reasons: its own, each watched
// service's and each unwatched one's.
func (d *Deps) quoteWatch(ctx context.Context, w *releasewatch.Watch) *releasewatch.Watch {
	out := *w
	out.Reason = d.quote(ctx, evidence, w.Reason)
	out.Services = slices.Clone(w.Services)
	for i := range out.Services {
		out.Services[i].Reason = d.quote(ctx, evidence, out.Services[i].Reason)
	}
	out.Unwatched = slices.Clone(w.Unwatched)
	for i := range out.Unwatched {
		out.Unwatched[i].Reason = d.quote(ctx, evidence, out.Unwatched[i].Reason)
	}
	return &out
}
