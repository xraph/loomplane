package agentwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/releasewatch"
)

// The events detectors read. Every other event, canary_verdict.updated
// among them, carries nothing a report needs: the deployment's canary
// status says how a canary ended.
const (
	eventDeployment = "deployment.updated"
	eventWatch      = "release_watch.updated"
	eventBinding    = "binding.updated"
	eventAnalysis   = "analysis.updated"
)

const (
	// componentDeployment is a problem with a deployment as a whole, such
	// as one that failed before any step.
	componentDeployment = "deployment"
	// failingAnalyses is how many failed analyses in a row of one source
	// are a report.
	failingAnalyses = 3
	// recheckDeployments is how many of an environment's newest
	// deployments a recheck looks through for a later success.
	recheckDeployments = 20
)

// Detector turns events and current state into report signals and
// clearances. It reads only through World and Health, and calls no model.
type Detector struct {
	World  World
	Health releasehealth.Reader
	Now    func() time.Time
	// Warn hears what a sweep skipped, such as a binding whose health
	// couldn't be read. nil drops it.
	Warn func(msg string, err error)
}

func (d *Detector) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func (d *Detector) warn(msg string, err error) {
	if d.Warn != nil {
		d.Warn(msg, err)
	}
}

// eventData is the little of an event's payload the pre-filters read, so
// a routine update costs a decode and nothing else. Name is a binding's,
// which a drift clearance names; Source and Pinned are an analysis's.
type eventData struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	DeploymentID  string `json:"deployment_id"`
	Status        string `json:"status"`
	Drift         string `json:"drift"`
	Name          string `json:"name"`
	Source        string `json:"source"`
	Pinned        bool   `json:"pinned"`
	Canary        *struct {
		Status string `json:"status"`
	} `json:"canary"`
}

// FromEvent pre-filters on the event's data, then re-reads state. Unknown
// event types return nothing.
func (d *Detector) FromEvent(ctx context.Context, ev realtime.Event) ([]agentreports.Signal, []agentreports.Clearance, error) {
	switch ev.Type {
	case eventDeployment, eventWatch, eventBinding, eventAnalysis:
	default:
		return nil, nil, nil
	}
	var data eventData
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return nil, nil, fmt.Errorf("read the %s event %s: %w", ev.Type, ev.ID, err)
	}
	if data.ProjectID == "" {
		data.ProjectID = ev.ProjectID
	}
	var sig *agentreports.Signal
	var cl *agentreports.Clearance
	var err error
	switch ev.Type {
	case eventDeployment:
		sig, cl, err = d.fromDeployment(ctx, ev, data)
	case eventWatch:
		sig, err = d.fromWatch(ctx, ev, data)
	case eventBinding:
		sig, cl, err = d.fromBinding(ctx, ev, data)
	case eventAnalysis:
		sig, cl, err = d.fromAnalysis(ctx, ev, data)
	}
	if gone(err) {
		// What the event named is gone: there's nothing left to report.
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("the %s event %s: %w", ev.Type, ev.ID, err)
	}
	var sigs []agentreports.Signal
	var clears []agentreports.Clearance
	if sig != nil {
		sigs = append(sigs, *sig)
	}
	if cl != nil {
		clears = append(clears, *cl)
	}
	return sigs, clears, nil
}

// gone reports whether err says a record no longer exists.
func gone(err error) bool {
	for _, nf := range []error{
		deployments.ErrNotFound, planning.ErrNotFound, releasewatch.ErrNotFound, discovery.ErrNotFound,
		projects.ErrNotFound, projects.ErrEnvironmentNotFound,
	} {
		if errors.Is(err, nf) {
			return true
		}
	}
	return false
}

func (d *Detector) fromDeployment(ctx context.Context, ev realtime.Event, data eventData) (*agentreports.Signal, *agentreports.Clearance, error) {
	canary := ""
	if data.Canary != nil {
		canary = data.Canary.Status
	}
	switch {
	case canaryEnded(canary):
		sig, err := d.canarySignal(ctx, ev, data.ID)
		return sig, nil, err
	case data.Status == deployments.StatusFailed:
		sig, err := d.deploymentFailed(ctx, ev, data.ID)
		return sig, nil, err
	case data.Status == deployments.StatusSucceeded:
		cl, err := d.deploymentSucceeded(ctx, ev, data.ID)
		return nil, cl, err
	}
	return nil, nil, nil
}

// canaryEnded reports whether a canary status is one a canary signal
// reports, which a deployment failure then doesn't.
func canaryEnded(status string) bool {
	return status == deployments.CanaryRolledBack || status == deployments.CanaryRevertFailed
}

func (d *Detector) deploymentFailed(ctx context.Context, ev realtime.Event, id string) (*agentreports.Signal, error) {
	dep, err := d.World.Deployment(ctx, ev.OrgID, id)
	if err != nil {
		return nil, err
	}
	if dep.Status != deployments.StatusFailed || (dep.Canary != nil && canaryEnded(dep.Canary.Status)) {
		return nil, nil
	}
	env, err := d.World.Environment(ctx, ev.OrgID, dep.ProjectID, dep.EnvironmentID)
	if err != nil {
		return nil, err
	}
	var severity string
	switch env.Kind {
	case projects.KindProduction:
		severity = agentreports.SeverityHigh
	case projects.KindStaging:
		severity = agentreports.SeverityMedium
	default:
		// Preview and development failures reach whoever started them
		// through the deployment's own notice, and open no report.
		return nil, nil
	}
	component, title := componentDeployment, fmt.Sprintf("Deployment to %s failed", env.Name)
	facts := deploymentFacts(dep)
	facts = appendFact(facts, agentreports.Fact{Label: "Commit", Value: dep.CommitSHA})
	facts = appendFact(facts, agentreports.Fact{Label: "Error", Value: dep.Error, Untrusted: true})
	if step := failedStep(dep); step != nil {
		component, title = step.ActionID, fmt.Sprintf("Deployment to %s failed at %s", env.Name, step.Name)
		facts = appendFact(facts, agentreports.Fact{Label: "Failed step", Value: step.Name})
		facts = appendFact(facts, agentreports.Fact{Label: "Step message", Value: step.Message, Untrusted: true})
	}
	return &agentreports.Signal{
		OrgID: ev.OrgID, ProjectID: dep.ProjectID, EnvironmentID: dep.EnvironmentID, Environment: env.Name,
		Kind: agentreports.SignalDeploymentFailed, Component: component, Severity: severity, Title: title, Facts: facts,
		Evidence:   agentreports.Evidence{DeploymentID: dep.ID, PlanID: dep.PlanID},
		Repository: d.deploymentRepository(ctx, dep, component),
		Occurrence: fmt.Sprintf("%s#%d", dep.ID, dep.Retries),
		At:         finished(dep, ev.Time),
	}, nil
}

func failedStep(dep *deployments.Deployment) *deployments.Step {
	for i := range dep.Steps {
		if dep.Steps[i].Status == deployments.StepFailed {
			return &dep.Steps[i]
		}
	}
	return nil
}

// deploymentFacts link the deployment and its plan. The dashboard shows a
// deployment on its plan's page.
func deploymentFacts(dep *deployments.Deployment) []agentreports.Fact {
	return []agentreports.Fact{
		{Label: "Deployment", Value: dep.ID, Path: "/deployments/" + dep.PlanID + "?tab=logs"},
		{Label: "Plan", Value: dep.PlanID, Path: "/deployments/" + dep.PlanID + "?tab=changes"},
	}
}

// appendFact adds f when it says something.
func appendFact(facts []agentreports.Fact, f agentreports.Fact) []agentreports.Fact {
	if strings.TrimSpace(f.Value) == "" {
		return facts
	}
	return append(facts, f)
}

func finished(dep *deployments.Deployment, fallback time.Time) time.Time {
	if dep.FinishedAt != nil {
		return *dep.FinishedAt
	}
	return fallback
}

// deploymentRepository is where a deployment's signal belongs: the one
// repository it moved for a deployment-level signal, else RepositoryFor.
func (d *Detector) deploymentRepository(ctx context.Context, dep *deployments.Deployment, component string) string {
	if component == componentDeployment && len(dep.Sources) == 1 {
		return dep.Sources[0].Repository
	}
	return d.repository(ctx, dep.OrgID, dep.ProjectID, component)
}

// repository is RepositoryFor, with a read error costing the report its
// repository rather than the report itself.
func (d *Detector) repository(ctx context.Context, orgID, projectID, component string) string {
	repo, err := d.World.RepositoryFor(ctx, orgID, projectID, component)
	if err != nil {
		d.warn("agentwatch: find the repository of "+component, err)
		return ""
	}
	return repo
}

func (d *Detector) canarySignal(ctx context.Context, ev realtime.Event, id string) (*agentreports.Signal, error) {
	dep, err := d.World.Deployment(ctx, ev.OrgID, id)
	if err != nil {
		return nil, err
	}
	if dep.Canary == nil || !canaryEnded(dep.Canary.Status) {
		return nil, nil
	}
	report, err := d.World.Canary(ctx, ev.OrgID, id)
	if err != nil {
		return nil, err
	}
	c := report.Canary
	if c == nil {
		c = dep.Canary
	}
	var verdict *deployments.CanaryVerdict
	var verdictIDs []string
	for i := range report.Verdicts {
		if report.Verdicts[i].Verdict != releasehealth.Fail {
			continue
		}
		if verdict == nil {
			verdict = &report.Verdicts[i]
		}
		verdictIDs = append(verdictIDs, report.Verdicts[i].ID)
	}
	component, service, bindingID, weight := componentDeployment, "this release", "", c.Weight
	if 0 <= c.Step && c.Step < len(c.Steps) {
		weight = c.Steps[c.Step]
	}
	if len(c.Services) > 0 {
		component, service, bindingID = c.Services[0].ActionID, c.Services[0].Name, c.Services[0].BindingID
	}
	facts := deploymentFacts(dep)
	if verdict != nil {
		component, service, weight = verdict.ActionID, verdict.Service, verdict.Weight
		for _, s := range c.Services {
			if s.ActionID == verdict.ActionID {
				bindingID = s.BindingID
			}
		}
		facts = appendFact(facts, agentreports.Fact{Label: "Reason", Value: verdict.Reason, Untrusted: true})
		facts = append(facts,
			agentreports.Fact{Label: "Baseline error rate", Value: percent(verdict.Baseline.ErrorRate())},
			agentreports.Fact{Label: "Candidate error rate", Value: percent(verdict.Candidate.ErrorRate())},
			agentreports.Fact{Label: "Baseline latency", Value: latency(verdict.Baseline)},
			agentreports.Fact{Label: "Candidate latency", Value: latency(verdict.Candidate)},
			agentreports.Fact{Label: "Thresholds", Value: thresholds(verdict.Thresholds)},
		)
	} else {
		facts = appendFact(facts, agentreports.Fact{Label: "Reason", Value: c.Reason, Untrusted: true})
		facts = append(facts, agentreports.Fact{Label: "Thresholds", Value: thresholds(c.Thresholds)})
	}
	sig := &agentreports.Signal{
		OrgID: ev.OrgID, ProjectID: dep.ProjectID, EnvironmentID: dep.EnvironmentID, Environment: dep.Environment,
		Kind: agentreports.SignalCanaryRolledBack, Component: component, Severity: agentreports.SeverityHigh,
		Title: fmt.Sprintf("Canary for %s rolled back at %d%%", service, weight), Facts: facts,
		Evidence:   agentreports.Evidence{DeploymentID: dep.ID, PlanID: dep.PlanID, BindingID: bindingID, VerdictIDs: verdictIDs},
		Repository: d.deploymentRepository(ctx, dep, component),
		Occurrence: dep.ID,
		At:         canaryAt(c, ev.Time),
	}
	if c.Status == deployments.CanaryRevertFailed {
		sig.Kind, sig.Severity = agentreports.SignalCanaryRevertFailed, agentreports.SeverityCritical
		sig.Title = fmt.Sprintf("Canary for %s couldn't be rolled back: traffic may still be split", service)
	}
	return sig, nil
}

func canaryAt(c *deployments.Canary, fallback time.Time) time.Time {
	if !c.UpdatedAt.IsZero() {
		return c.UpdatedAt
	}
	return fallback
}

func percent(f float64) string { return strconv.FormatFloat(f*100, 'f', 1, 64) + "%" }

func number(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// ceiling renders a threshold without trailing zeros: 1%, 0.5%.
func ceiling(f float64) string { return strconv.FormatFloat(f*100, 'f', -1, 64) + "%" }

func latency(h releasehealth.Health) string {
	if h.LatencyMS <= 0 {
		return "not reported"
	}
	ms := strconv.FormatFloat(h.LatencyMS, 'f', 0, 64) + "ms"
	if h.Statistic != "" {
		return h.Statistic + " " + ms
	}
	return ms
}

func thresholds(t releasehealth.Thresholds) string {
	var parts []string
	if t.ErrorRate > 0 {
		parts = append(parts, "error rate above "+ceiling(t.ErrorRate))
	}
	if t.ErrorRateDelta > 0 {
		parts = append(parts, "error rate more than "+ceiling(t.ErrorRateDelta)+" above the baseline")
	}
	if t.P95LatencyRatio > 0 {
		parts = append(parts, "latency above "+number(t.P95LatencyRatio)+" times the baseline")
	}
	if len(parts) == 0 {
		return "none"
	}
	return "Fails on " + strings.Join(parts, ", or ")
}

// deploymentSucceeded clears what a success fixes in its environment. A
// rollback's success restores the release before, so it leaves the reports
// that say why the newer one was rolled back.
func (d *Detector) deploymentSucceeded(ctx context.Context, ev realtime.Event, id string) (*agentreports.Clearance, error) {
	dep, err := d.World.Deployment(ctx, ev.OrgID, id)
	if err != nil {
		return nil, err
	}
	if dep.Status != deployments.StatusSucceeded {
		return nil, nil
	}
	plan, err := d.World.Plan(ctx, ev.OrgID, dep.PlanID)
	if err != nil {
		return nil, err
	}
	return &agentreports.Clearance{
		OrgID: ev.OrgID, ProjectID: dep.ProjectID, EnvironmentID: dep.EnvironmentID,
		Signals:    clearedBy(plan),
		Components: stepComponents(dep),
		Reason:     fmt.Sprintf("A later deployment (%s) succeeded", dep.ID),
		At:         finished(dep, ev.Time),
	}, nil
}

// clearedBy is what a successful deployment of plan clears.
func clearedBy(plan *planning.Plan) []string {
	kinds := []string{agentreports.SignalDeploymentFailed, agentreports.SignalCanaryRevertFailed}
	if plan.Kind != planning.KindRollback {
		kinds = append(kinds, agentreports.SignalCanaryRolledBack, agentreports.SignalWatchRolledBack,
			agentreports.SignalWatchRollbackBlocked, agentreports.SignalWatchRollbackFailed)
	}
	return kinds
}

// stepComponents are the actions a deployment ran, each once, and
// "deployment".
func stepComponents(dep *deployments.Deployment) []string {
	out := make([]string, 0, len(dep.Steps)+1)
	for _, s := range dep.Steps {
		if s.ActionID != "" && !slices.Contains(out, s.ActionID) {
			out = append(out, s.ActionID)
		}
	}
	return append(out, componentDeployment)
}

// watchKind is the signal a watch's status reports, "" for none. An
// unverified watch with no breach is missing evidence, not an incident,
// and passes, settles inconclusive or is superseded: none of those report.
func watchKind(status string) (kind, severity string) {
	switch status {
	case releasewatch.StatusRollingBack, releasewatch.StatusRolledBack:
		return agentreports.SignalWatchRolledBack, agentreports.SeverityHigh
	case releasewatch.StatusRollbackBlocked:
		return agentreports.SignalWatchRollbackBlocked, agentreports.SeverityHigh
	case releasewatch.StatusRollbackFailed:
		return agentreports.SignalWatchRollbackFailed, agentreports.SeverityCritical
	}
	return "", ""
}

func (d *Detector) fromWatch(ctx context.Context, ev realtime.Event, data eventData) (*agentreports.Signal, error) {
	if kind, _ := watchKind(data.Status); kind == "" || data.DeploymentID == "" {
		return nil, nil
	}
	w, err := d.World.Watch(ctx, ev.OrgID, data.DeploymentID)
	if err != nil {
		return nil, err
	}
	kind, severity := watchKind(w.Status)
	if kind == "" {
		return nil, nil
	}
	var svc *releasewatch.WatchedService
	for i := range w.Services {
		if w.Services[i].Verdict == string(releasehealth.Fail) {
			svc = &w.Services[i]
			break
		}
	}
	if svc == nil && len(w.Services) > 0 {
		svc = &w.Services[0]
	}
	component, name, bindingID := componentDeployment, "This release", ""
	if svc != nil {
		component, name, bindingID = string(planning.ServiceSubject)+":"+svc.Name, svc.Name, svc.BindingID
	}
	var title string
	switch kind {
	case agentreports.SignalWatchRolledBack:
		title = name + " was rolled back after its release breached its thresholds"
	case agentreports.SignalWatchRollbackBlocked:
		title = name + " breached its thresholds and its rollback is waiting for a person"
	default:
		title = name + " breached its thresholds and its rollback failed"
	}
	facts := []agentreports.Fact{{Label: "Deployment", Value: w.DeploymentID, Path: "/deployments/" + w.PlanID}}
	facts = appendFact(facts, agentreports.Fact{Label: "Reason", Value: w.Reason, Untrusted: true})
	if svc != nil {
		facts = appendFact(facts, agentreports.Fact{Label: "Service verdict", Value: svc.Reason, Untrusted: true})
	}
	if w.RollbackPlanID != "" {
		facts = append(facts, agentreports.Fact{Label: "Rollback plan", Value: w.RollbackPlanID, Path: "/deployments/" + w.RollbackPlanID})
	}
	at := w.UpdatedAt
	if w.SettledAt != nil {
		at = *w.SettledAt
	}
	if at.IsZero() {
		at = ev.Time
	}
	return &agentreports.Signal{
		OrgID: ev.OrgID, ProjectID: w.ProjectID, EnvironmentID: w.EnvironmentID, Environment: w.Environment,
		Kind: kind, Component: component, Severity: severity, Title: title, Facts: facts,
		Evidence:   agentreports.Evidence{DeploymentID: w.DeploymentID, PlanID: w.PlanID, WatchID: w.ID, BindingID: bindingID},
		Repository: d.repository(ctx, ev.OrgID, w.ProjectID, component),
		Occurrence: w.ID,
		At:         at,
	}, nil
}

func (d *Detector) fromBinding(ctx context.Context, ev realtime.Event, data eventData) (*agentreports.Signal, *agentreports.Clearance, error) {
	if data.Drift == "" {
		if data.Name == "" || data.ProjectID == "" {
			return nil, nil, nil
		}
		return nil, driftCleared(ev.OrgID, data.ProjectID, data.EnvironmentID, data.Name, ev.Time), nil
	}
	b, err := d.World.Binding(ctx, ev.OrgID, data.ID)
	if err != nil {
		return nil, nil, err
	}
	if b.Drift == "" {
		return nil, driftCleared(ev.OrgID, b.ProjectID, b.EnvironmentID, b.Name, observed(b, ev.Time)), nil
	}
	env, err := d.World.Environment(ctx, ev.OrgID, b.ProjectID, b.EnvironmentID)
	if err != nil {
		return nil, nil, err
	}
	return d.driftSignal(ctx, b, env, ev.Time), nil, nil
}

// driftSignal reports b's drift when it runs in production: drift
// elsewhere is someone's experiment, not an incident.
func (d *Detector) driftSignal(ctx context.Context, b *planning.Binding, env *projects.Environment, fallback time.Time) *agentreports.Signal {
	if b.Drift == "" || env.Kind != projects.KindProduction {
		return nil
	}
	what := "changed outside Loomplane"
	if b.Drift == planning.DriftMissing {
		what = "deleted outside Loomplane"
	}
	facts := appendFact(nil, agentreports.Fact{Label: "Drift", Value: b.DriftMessage, Untrusted: true})
	facts = appendFact(facts, agentreports.Fact{Label: "Provider", Value: b.Provider})
	if b.ObservedAt != nil {
		facts = append(facts, agentreports.Fact{Label: "Observed at", Value: b.ObservedAt.UTC().Format(time.RFC3339)})
	}
	return &agentreports.Signal{
		OrgID: b.OrgID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID, Environment: env.Name,
		Kind: agentreports.SignalDrift, Component: b.Name, Severity: agentreports.SeverityMedium,
		Title: fmt.Sprintf("%s drifted on %s: %s", b.Name, env.Name, what), Facts: facts,
		Evidence:   agentreports.Evidence{BindingID: b.ID, PlanID: b.PlanID},
		Repository: d.repository(ctx, b.OrgID, b.ProjectID, string(b.Subject)+":"+b.Name),
		At:         observed(b, fallback),
	}
}

func driftCleared(orgID, projectID, environmentID, name string, at time.Time) *agentreports.Clearance {
	return &agentreports.Clearance{
		OrgID: orgID, ProjectID: projectID, EnvironmentID: environmentID,
		Signals: []string{agentreports.SignalDrift}, Components: []string{name},
		Reason: name + " matches what Loomplane deployed again", At: at,
	}
}

// observed is when the drift check last read b.
func observed(b *planning.Binding, fallback time.Time) time.Time {
	if b.ObservedAt != nil {
		return *b.ObservedAt
	}
	return fallback
}

func (d *Detector) fromAnalysis(ctx context.Context, ev realtime.Event, data eventData) (*agentreports.Signal, *agentreports.Clearance, error) {
	if data.Pinned {
		// A promotion's analysis of one commit says nothing about the
		// source's own analyses.
		return nil, nil, nil
	}
	switch data.Status {
	case discovery.StatusFailed:
		sig, err := d.analysisFailing(ctx, ev, data.ID)
		return sig, nil, err
	case discovery.StatusSucceeded:
		if data.Source == "" || data.ProjectID == "" {
			return nil, nil, nil
		}
		return nil, &agentreports.Clearance{
			OrgID: ev.OrgID, ProjectID: data.ProjectID,
			Signals: []string{agentreports.SignalAnalysisFailing}, Components: []string{data.Source},
			Reason: fmt.Sprintf("A later analysis of %s (%s) succeeded", data.Source, data.ID), At: ev.Time,
		}, nil
	}
	return nil, nil, nil
}

func (d *Detector) analysisFailing(ctx context.Context, ev realtime.Event, id string) (*agentreports.Signal, error) {
	a, err := d.World.Analysis(ctx, ev.OrgID, id)
	if err != nil {
		return nil, err
	}
	if a.Status != discovery.StatusFailed || a.Pinned {
		return nil, nil
	}
	recent, err := d.World.RecentAnalyses(ctx, ev.OrgID, a.ProjectID, a.SourceID, failingAnalyses)
	if err != nil {
		return nil, err
	}
	if len(recent) < failingAnalyses {
		return nil, nil
	}
	for _, r := range recent {
		if r.Status != discovery.StatusFailed {
			return nil, nil
		}
	}
	facts := appendFact(nil, agentreports.Fact{Label: "Last error", Value: a.Error, Untrusted: true})
	facts = appendFact(facts, agentreports.Fact{Label: "Commit", Value: a.CommitSHA})
	at := ev.Time
	if a.FinishedAt != nil {
		at = *a.FinishedAt
	}
	return &agentreports.Signal{
		OrgID: ev.OrgID, ProjectID: a.ProjectID,
		Kind: agentreports.SignalAnalysisFailing, Component: a.Source, Severity: agentreports.SeverityLow,
		Title: fmt.Sprintf("Analysis of %s has failed %d times in a row", a.Source, failingAnalyses), Facts: facts,
		Evidence:   agentreports.Evidence{AnalysisID: a.ID, SourceAlias: a.Source},
		Repository: a.Repository,
		Occurrence: a.ID,
		At:         at,
	}, nil
}

// Recheck says whether an open report's signal still holds, from current
// state. A report whose records are gone no longer holds; one Loomplane
// can't judge, such as a manual report, does.
func (d *Detector) Recheck(ctx context.Context, r *agentreports.Report) (bool, error) {
	holds, err := d.recheck(ctx, r)
	if gone(err) {
		return false, nil
	}
	return holds, err
}

func (d *Detector) recheck(ctx context.Context, r *agentreports.Report) (bool, error) {
	switch r.Signal {
	case agentreports.SignalDeploymentFailed, agentreports.SignalCanaryRevertFailed, agentreports.SignalCanaryRolledBack:
		fixed, err := d.succeededSince(ctx, r)
		return !fixed, err
	case agentreports.SignalWatchRolledBack, agentreports.SignalWatchRollbackBlocked, agentreports.SignalWatchRollbackFailed:
		if fixed, err := d.succeededSince(ctx, r); err != nil || fixed {
			return false, err
		}
		if r.Evidence.DeploymentID == "" {
			return true, nil
		}
		w, err := d.World.Watch(ctx, r.OrgID, r.Evidence.DeploymentID)
		if err != nil {
			return false, err
		}
		kind, _ := watchKind(w.Status)
		return kind == r.Signal, nil
	case agentreports.SignalDrift, agentreports.SignalErrorRate:
		if r.Evidence.BindingID == "" {
			return true, nil
		}
		b, err := d.World.Binding(ctx, r.OrgID, r.Evidence.BindingID)
		if err != nil {
			return false, err
		}
		// The sweep judges error rates; a recheck only drops a report
		// whose binding went away.
		return r.Signal == agentreports.SignalErrorRate || b.Drift != "", nil
	case agentreports.SignalAnalysisFailing:
		return d.analysisStillFailing(ctx, r)
	}
	return true, nil
}

// succeededSince reports whether a deployment in the report's environment
// that ran its component succeeded after the report was last seen, which
// is what the success clearance says.
func (d *Detector) succeededSince(ctx context.Context, r *agentreports.Report) (bool, error) {
	recent, err := d.World.RecentDeployments(ctx, r.OrgID, r.ProjectID, r.EnvironmentID, recheckDeployments)
	if err != nil {
		return false, err
	}
	for _, dep := range recent {
		if dep.Status != deployments.StatusSucceeded || !finished(dep, dep.StartedAt).After(r.LastSeenAt) ||
			!slices.Contains(stepComponents(dep), r.Component) {
			continue
		}
		plan, err := d.World.Plan(ctx, r.OrgID, dep.PlanID)
		if err != nil {
			return false, err
		}
		if slices.Contains(clearedBy(plan), r.Signal) {
			return true, nil
		}
	}
	return false, nil
}

// analysisStillFailing reports whether the source's newest finished
// analysis failed. One still running hasn't changed anything yet.
func (d *Detector) analysisStillFailing(ctx context.Context, r *agentreports.Report) (bool, error) {
	if r.Evidence.AnalysisID == "" {
		return true, nil
	}
	a, err := d.World.Analysis(ctx, r.OrgID, r.Evidence.AnalysisID)
	if err != nil {
		return false, err
	}
	recent, err := d.World.RecentAnalyses(ctx, r.OrgID, r.ProjectID, a.SourceID, failingAnalyses)
	if err != nil {
		return false, err
	}
	for _, x := range recent {
		if x.Finished() {
			return x.Status == discovery.StatusFailed, nil
		}
	}
	return true, nil
}
