package releaseprep

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// Evidence keys: the names in ReadinessEvidence.Unavailable, each read by
// the gate that needs it.
const (
	KeyDiff              = "diff"
	KeyPins              = "pins"
	KeySoak              = "soak"
	KeyStagingRelease    = "staging_release"
	KeyProductionRelease = "production_release"
	KeyCoverage          = "coverage"
	KeyCanaries          = "canaries"
	KeyReports           = "open_reports"
	KeyBudget            = "budget"
)

// evidenceKeys are every key, in the order the evidence is read.
var evidenceKeys = []string{KeyDiff, KeyPins, KeySoak, KeyStagingRelease, KeyProductionRelease, KeyCoverage, KeyCanaries, KeyReports, KeyBudget}

// WarningProductionMovesBack is the warning the evidence adds when staging
// runs a commit behind production's, or diverged from it, for a source the
// plan moves.
const WarningProductionMovesBack = "production-moves-back"

// ReadinessEvidence is what a release preparation read about a promotion,
// in Loomplane code, before any model saw it. Anything it couldn't read is
// named in Unavailable, and fails the gate that needs it.
//
// It holds untrusted text: commit titles and authors, migration paths and
// patches, open report titles, canary reasons, and error text in
// SourceDiff.Error and Unavailable. It's stored as data, never sent in a
// realtime event, and never quoted in a gate's detail.
type ReadinessEvidence struct {
	CollectedAt time.Time `json:"collected_at"`
	PlanStatus  string    `json:"plan_status"`
	// Sources are the plan's in-scope sources, production's commit to
	// staging's.
	Sources  []SourceDiff       `json:"sources"`
	Problems []planning.Finding `json:"problems"`
	// Warnings are the plan's, plus production-moves-back.
	Warnings []planning.Finding `json:"warnings"`
	// Destroy are the action IDs that make the plan destroy-class: a
	// Delete, or a Replace of something that holds data.
	Destroy []string `json:"destroy"`
	// DataUpdates are the action IDs that Update something that holds
	// data.
	DataUpdates []string `json:"data_updates"`
	// BackupSchedule says the plan sets a default backup schedule, which a
	// person who manages backups approves.
	BackupSchedule  bool                      `json:"backup_schedule"`
	ChangedServices []ChangedService          `json:"changed_services"`
	Artifacts       []planning.ArtifactChoice `json:"artifacts"`
	// PinsMatch says staging still runs every in-scope pin and the
	// manifest revision the plan uses; PinDifferences say what moved.
	PinsMatch         bool                          `json:"pins_match"`
	PinDifferences    []string                      `json:"pin_differences"`
	Soak              *releasewatch.Soak            `json:"soak"`
	StagingRelease    *releasewatch.ReleaseEvidence `json:"staging_release"`
	ProductionRelease *releasewatch.ReleaseEvidence `json:"production_release"`
	Coverage          *releasewatch.Coverage        `json:"coverage"`
	// Canaries are the newest production canary run of each changed
	// service, each run once (ChangedService.LastCanary names it). Nil
	// means they weren't read.
	Canaries []CanarySummary `json:"canaries"`
	// OpenReports are the agent reports open on staging. Nil means they
	// weren't read.
	OpenReports []OpenReport               `json:"open_reports"`
	Budget      *agentsettings.BudgetState `json:"budget"`
	// Unavailable maps an evidence key to why it couldn't be read.
	Unavailable map[string]string `json:"unavailable"`
}

// ChangedService is a service the plan creates, updates or replaces in
// production.
type ChangedService struct {
	ActionID string `json:"action_id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Op       string `json:"op"`
	// Canary says the plan's release ramps it as a canary.
	Canary bool `json:"canary"`
	// LastCanary is the deployment of the newest production canary that
	// ramped it, in Canaries; "" when none ever did.
	LastCanary string `json:"last_canary"`
}

// CanarySummary is a production canary run.
type CanarySummary struct {
	DeploymentID string `json:"deployment_id"`
	PlanID       string `json:"plan_id"`
	// Status is the canary's: promoted, rolled_back, revert_failed, ...
	Status string `json:"status"`
	// Reason is the canary's reason. Untrusted: the provider's words.
	Reason   string   `json:"reason,omitempty"`
	Services []string `json:"services"`
	// ActionIDs are the plan actions of the services it ramped, which gate
	// 5 matches changed services by.
	ActionIDs  []string   `json:"action_ids"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Collector reads the evidence for a promotion.
type Collector struct {
	Sources Sources
	Now     func() time.Time
}

// CollectInput is what to read the evidence for.
type CollectInput struct {
	// Principal reads everything: the agent acting for the person who
	// asked.
	Principal security.Principal
	Plan      *planning.Plan
	From, To  *projects.Environment
	// Diffs nil computes every diff. Non-nil reuses a given diff for a
	// source only while it names the commits production and the plan run
	// now; any other source is compared again.
	Diffs []SourceDiff
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

// Collect reads every piece of evidence once. It never fails: a read that
// errors, or a source that isn't there, lands in Unavailable with why, and
// the rest is still read.
func (c *Collector) Collect(ctx context.Context, in CollectInput) *ReadinessEvidence {
	ev := &ReadinessEvidence{
		CollectedAt: c.now(), Sources: []SourceDiff{}, Problems: []planning.Finding{}, Warnings: []planning.Finding{},
		Destroy: []string{}, DataUpdates: []string{}, ChangedServices: []ChangedService{}, Artifacts: []planning.ArtifactChoice{},
		PinDifferences: []string{}, Unavailable: map[string]string{},
	}
	if in.Plan == nil || in.From == nil || in.To == nil {
		for _, k := range evidenceKeys {
			ev.Unavailable[k] = "the plan or its environments couldn't be read"
		}
		return ev
	}
	plan := in.Plan
	ev.PlanStatus = plan.Status
	ev.Problems = append(ev.Problems, plan.Problems...)
	ev.Warnings = append(ev.Warnings, plan.Warnings...)
	ev.Artifacts = append(ev.Artifacts, plan.Artifacts...)
	classify(plan, ev)

	c.pins(ctx, in, ev)
	c.diffs(ctx, in, ev)
	c.watches(ctx, in, ev)
	c.canaries(ctx, in, ev)
	c.reports(ctx, in, ev)
	c.budget(ctx, in, ev)
	return ev
}

// classify sorts the plan's actions into what destroys, what updates data,
// and which services change.
func classify(plan *planning.Plan, ev *ReadinessEvidence) {
	for _, a := range plan.Actions {
		// The authorizer's own rule, one action at a time, so the report
		// names exactly the actions that make the plan destroy-class.
		if agentops.PlanRisk(&planning.Plan{Actions: []planning.Action{a}}, projects.KindProduction) == defs.RiskDestroyProduction {
			ev.Destroy = append(ev.Destroy, a.ID)
		}
		if a.Op == planning.Update && planning.DataBearing(a) {
			ev.DataUpdates = append(ev.DataUpdates, a.ID)
		}
		if a.Subject != planning.ServiceSubject {
			continue
		}
		switch a.Op {
		case planning.Create, planning.Update, planning.Replace:
			provider := a.Provider
			if provider == "" && a.Service != nil {
				provider = a.Service.Provider
			}
			ev.ChangedServices = append(ev.ChangedServices, ChangedService{
				ActionID: a.ID, Name: a.Name, Provider: provider, Op: string(a.Op), Canary: plan.Release.Ramps(a.ID),
			})
		}
	}
	ev.BackupSchedule = agentops.SetsBackupSchedule(plan)
}

// unavailable records why key couldn't be read.
func unavailable(ev *ReadinessEvidence, key string, err error) {
	ev.Unavailable[key] = err.Error()
}

var (
	errNotWired = errors.New("this server can't read it")
	errNoAnswer = errors.New("the read returned nothing")
)

func (c *Collector) pins(ctx context.Context, in CollectInput, ev *ReadinessEvidence) {
	if c.Sources.Plans == nil {
		unavailable(ev, KeyPins, errNotWired)
		return
	}
	match, diffs, err := c.Sources.Plans.PinsMatchSource(ctx, in.Principal.OrgID, in.Plan)
	if err != nil {
		unavailable(ev, KeyPins, err)
		return
	}
	ev.PinsMatch = match
	ev.PinDifferences = append(ev.PinDifferences, diffs...)
}

// diffs compares production's commit of each in-scope source to the one
// the plan pins (staging's), through BuildDiff, so an incomplete or
// unreadable diff always carries an Error.
func (c *Collector) diffs(ctx context.Context, in CollectInput, ev *ReadinessEvidence) {
	plan := in.Plan
	var why []string
	var prod map[string]string
	var prodErr error
	if c.Sources.Plans == nil {
		prodErr = errNotWired
	} else if deployed, err := c.Sources.Plans.Deployed(ctx, in.Principal, plan.ProjectID, in.To.Name); err != nil {
		prodErr = err
	} else {
		prod = map[string]string{}
		for _, d := range deployed {
			if d != nil {
				prod[d.SourceID] = d.CommitSHA
			}
		}
	}
	given := map[string]SourceDiff{}
	for _, d := range in.Diffs {
		given[d.SourceID] = d
	}
	for _, ps := range plan.Sources {
		if !ps.InScope {
			continue
		}
		d := SourceDiff{SourceID: ps.SourceID, Alias: ps.Alias, Repository: ps.Repository, StagingCommit: ps.CommitSHA}
		if prodErr != nil {
			d.Error = "production's commits couldn't be read"
			why = append(why, ps.Alias+": "+prodErr.Error())
			ev.Sources = append(ev.Sources, d)
			continue
		}
		d.ProductionCommit = prod[ps.SourceID]
		// A given diff of the same commits is reused, an unreadable one
		// included: the agent reviewed the report without it, so a read
		// that works now mustn't pass a diff nobody reviewed.
		if g, ok := given[ps.SourceID]; ok && in.Diffs != nil && g.Repository == d.Repository &&
			g.ProductionCommit == d.ProductionCommit && g.StagingCommit == d.StagingCommit {
			d = g
		} else {
			d = c.diff(ctx, in.Principal.OrgID, d)
		}
		if d.Error != "" {
			why = append(why, d.Alias+": "+d.Error)
		}
		ev.Sources = append(ev.Sources, d)
		if d.Direction == DirectionBehind || d.Direction == DirectionDiverged {
			ev.Warnings = append(ev.Warnings, movesBack(d, in.From.Name, in.To.Name))
		}
	}
	if len(why) > 0 {
		ev.Unavailable[KeyDiff] = strings.Join(why, "; ")
	}
}

// diff fills one source's diff. Production never ran it: new. The same
// commit: identical. Otherwise GitHub compares them.
func (c *Collector) diff(ctx context.Context, orgID string, d SourceDiff) SourceDiff {
	switch {
	case d.ProductionCommit == "":
		return BuildDiff(d, nil)
	case d.ProductionCommit == d.StagingCommit:
		d.Direction = DirectionIdentical
		return d
	case c.Sources.Diffs == nil:
		d.Error = errNotWired.Error()
		return d
	}
	cmp, err := c.Sources.Diffs.Compare(ctx, orgID, d.Repository, d.ProductionCommit, d.StagingCommit)
	if err != nil {
		d.Error = err.Error()
		return d
	}
	return BuildDiff(d, cmp)
}

// movesBack is the warning for a source whose staging commit is behind
// production's, or diverged from it.
func movesBack(d SourceDiff, from, to string) planning.Finding {
	how := "is behind"
	if d.Direction == DirectionDiverged {
		how = "has diverged from"
	}
	return planning.Finding{
		Code: WarningProductionMovesBack, Name: d.Alias,
		Message: fmt.Sprintf("%s: %s runs %s which %s %s's %s", d.Alias, from, shortSHA(d.StagingCommit), how, to, shortSHA(d.ProductionCommit)),
	}
}

// shortSHA is a commit as people read it.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func (c *Collector) watches(ctx context.Context, in CollectInput, ev *ReadinessEvidence) {
	w := c.Sources.Watches
	if w == nil {
		for _, k := range []string{KeySoak, KeyStagingRelease, KeyProductionRelease, KeyCoverage} {
			unavailable(ev, k, errNotWired)
		}
		return
	}
	p, projectID := in.Principal, in.Plan.ProjectID
	if soak, err := w.SoakForPlan(ctx, p, in.Plan.ID); err != nil {
		unavailable(ev, KeySoak, err)
	} else if soak == nil {
		unavailable(ev, KeySoak, errNoAnswer)
	} else {
		ev.Soak = soak
	}
	if r, err := w.Evidence(ctx, p, projectID, in.From.Name); err != nil {
		unavailable(ev, KeyStagingRelease, err)
	} else if r == nil {
		unavailable(ev, KeyStagingRelease, errNoAnswer)
	} else {
		ev.StagingRelease = r
	}
	if r, err := w.Evidence(ctx, p, projectID, in.To.Name); err != nil {
		unavailable(ev, KeyProductionRelease, err)
	} else if r == nil {
		unavailable(ev, KeyProductionRelease, errNoAnswer)
	} else {
		ev.ProductionRelease = r
	}
	if cov, err := w.Armable(ctx, p, projectID, in.To.Name); err != nil {
		unavailable(ev, KeyCoverage, err)
	} else if cov == nil {
		unavailable(ev, KeyCoverage, errNoAnswer)
	} else {
		ev.Coverage = cov
	}
}

// canaries reads each changed service's newest production canary on its
// own, so no number of canaries of other services hides a failed one. Any
// read that fails makes all of them unavailable.
func (c *Collector) canaries(ctx context.Context, in CollectInput, ev *ReadinessEvidence) {
	if c.Sources.Canaries == nil {
		unavailable(ev, KeyCanaries, errNotWired)
		return
	}
	runs := []CanarySummary{}
	var why []string
	for i, s := range ev.ChangedServices {
		r, err := c.Sources.Canaries.LastCanary(ctx, in.Principal, in.Plan.ProjectID, in.To.ID, s.ActionID)
		switch {
		case err != nil:
			why = append(why, s.Name+": "+err.Error())
			continue
		case r == nil || r.Canary == nil:
			continue
		}
		ev.ChangedServices[i].LastCanary = r.DeploymentID
		if slices.ContainsFunc(runs, func(x CanarySummary) bool { return x.DeploymentID == r.DeploymentID }) {
			continue
		}
		sum := CanarySummary{
			DeploymentID: r.DeploymentID, PlanID: r.PlanID, Status: r.Canary.Status, Reason: r.Canary.Reason,
			Services: []string{}, ActionIDs: []string{}, FinishedAt: r.FinishedAt,
		}
		for _, svc := range r.Canary.Services {
			sum.Services = append(sum.Services, svc.Name)
			sum.ActionIDs = append(sum.ActionIDs, svc.ActionID)
		}
		runs = append(runs, sum)
	}
	if len(why) > 0 {
		for i := range ev.ChangedServices {
			ev.ChangedServices[i].LastCanary = ""
		}
		ev.Unavailable[KeyCanaries] = strings.Join(why, "; ")
		return
	}
	ev.Canaries = runs
}

// reports reads the agent reports open on staging.
func (c *Collector) reports(ctx context.Context, in CollectInput, ev *ReadinessEvidence) {
	if c.Sources.Reports == nil {
		unavailable(ev, KeyReports, errNotWired)
		return
	}
	open, err := c.Sources.Reports.Open(ctx, in.Principal.OrgID, in.Plan.ProjectID, in.From.ID)
	if err != nil {
		unavailable(ev, KeyReports, err)
		return
	}
	ev.OpenReports = append([]OpenReport{}, open...)
}

func (c *Collector) budget(ctx context.Context, in CollectInput, ev *ReadinessEvidence) {
	if c.Sources.Budgets == nil {
		unavailable(ev, KeyBudget, errNotWired)
		return
	}
	b, err := c.Sources.Budgets.Budget(ctx, in.Principal.OrgID)
	if err != nil {
		unavailable(ev, KeyBudget, err)
		return
	}
	ev.Budget = &b
}
